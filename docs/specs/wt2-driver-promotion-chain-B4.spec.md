# WT-2-driver-promotion-chain — Sub-B4: lookup-hook

**Chain position**: 3 of 4 (B6 done → B2 done → **B4** → B1). Adds a
driver-side helper that queries the in-process per-slave registry
view + userspace FTS5 BEFORE the driver prompts the user with a
promote candidate, and
exposes a `RegistryLookupHitRate` counter for the paper's B4 metric.

**Sources**:

- `/root/paper_writing/docs/final/todo_list.md` Phase 2 合流注意事项 §3.
- `/root/paper_writing/docs/intermediate/12_loom_development_tasks_for_v3.md` §B B4.
- Existing: `internal/userspace/store.go:SearchPackagesForIdentity`
  (FTS5-backed search, landed in Phase 0
  WT-0-personal-skill-space).
- Existing: `internal/driver/registryhash.go:snapshotAll` — driver's
  in-process per-slave registry view populated by B6 register /
  unregister flow.
- Existing: `internal/ablation.NoRegistryLookup` — the FlagName is
  declared in `internal/ablation/registry.go:23` and included in
  `KnownFlags()`, but no `*bool` target is registered yet. B4
  registers the target via `ablation.Default.Register` in an `init()`
  block owned by the new package.

---

## 1. Goal

Add `internal/driver/registry_lookup.go` (per chain notes) exposing a
`Lookup(ctx, query string) []Hit` function that:

1. Searches the DRIVER'S IN-PROCESS PER-SLAVE REGISTRY VIEW (the same
   view B6 populates via `noteRegister` / `noteUnregister`) for
   registered MCP names that match the query (exact + case-insensitive
   substring). See §4 for the rationale — reading a slave's
   dynamic_mcp.yaml remotely is closed by B6 §7 (j).
2. Searches the userspace FTS5 index (via
   `userspace.Store.SearchPackagesForIdentity`) for slugs / descriptions
   that match the query.
3. Returns a merged, deduped, score-ranked `[]Hit`, capped at 20 entries.
4. Publishes a per-process `RegistryLookupHitRate = hits / queries`
   counter (via a small `internal/driver/registrylookupmetrics.go`)
   readable at any time AND writes one `registry_lookup_samples` row
   per non-ablated call (§5).
5. Wires a `NoRegistryLookup` ablation target — when the flag is on,
   `Lookup` short-circuits, returns `nil`, and emits ONE structured
   log line per invocation (per §7 (h)'s sanitized+hash form).

The driver's *automatic* lookup-before-prompt integration in
`draft_task_contract` (the surface that turns a user intent into a
proposed contract) is included in THIS sub-task per todo_list §3 and
12号 §B B4: `draftTaskContractTool.Call` now runs `Lookup` on the
user's intent text BEFORE emitting the "you may want to reuse an
existing MCP" prompt to the user, and threads the hits into the
returned draft so the driver's LLM can see them. The B1 sub-task will
add a distinct SURFACING logic for "we saw a promote-candidate signal
— want to fixate?"; that's an orthogonal surface (candidate ≠ reuse).

## 2. Public API

`internal/driver/registry_lookup.go`:

```go
package driver

// Hit is one lookup result. Source identifies which side produced it;
// Score is a normalized [0,1] float. Registry side: 1.0 exact match,
// 0.7 case-insensitive substring on the fully-qualified name.
// Userspace side: position-derived score (first result 1.0, twentieth
// 0.5) — see PackageHit.Rank rationale below; a future userspace API
// exposing bm25 rank can be plugged in through the same adapter.
type Hit struct {
    Source  string  `json:"source"`  // "registry" | "userspace"
    MCPName string  `json:"mcp_name"` // registry: <slave_id>:<mcp_name>; userspace: slug
    Score   float64 `json:"score"`
    // Detail is the human-readable snippet the driver shows the user.
    // Registry-side: "registered on <slave_id>".
    // Userspace-side: the slug's description, truncated to 200 chars.
    Detail  string  `json:"detail,omitempty"`
}

// Lookup queries both sources and returns merged, capped hits. Never
// returns an error — on FTS5 failure it degrades to registry-only
// with a log line; on registry read failure it degrades to
// userspace-only. Returns nil (NOT empty slice) when the
// NoRegistryLookup ablation is on so callers can distinguish
// "explicitly disabled" from "nothing found".
//
// query length is capped at 256 chars; longer inputs are truncated
// (with a log line) — §7 (a).
func Lookup(ctx context.Context, query string) []Hit

// LookupHitRate returns the current running RegistryLookupHitRate =
// hits / queries. Queries with 0 hits count in the denominator;
// queries suppressed by NoRegistryLookup do NOT count either way
// (they never ran).
func LookupHitRate() float64

// ResetLookupMetricsForTest zeroes the counters. Test-only helper;
// production code MUST NOT call it.
func ResetLookupMetricsForTest()
```

Dependencies (injected via package-level `var`s at init time from the
driver-agent wiring):

```go
// SetLookupDeps is called once at process start by the driver-agent
// main.go. Nil dependency → that source is silently skipped in Lookup
// (with a first-call warn log, matching the pipeline pattern).
type LookupDeps struct {
    UserspaceStore UserspaceSearcher // interface — see below
    WorkspaceID    string
    UserID         string
    SampleWrite    func(ctx context.Context, s RegistryLookupSample) error // nil-safe; see §5
    CurrentRunID   func() string                                           // nil-safe; see §5
}

// UserspaceSearcher is the narrow surface Lookup needs from the
// userspace store — kept as an interface so tests can inject a mock
// without a real *userspace.Store.
type UserspaceSearcher interface {
    SearchPackagesForIdentity(q, workspaceID, userID, kindFilter string, limit int) ([]PackageHit, error)
}

// PackageHit is a driver-side view of a userspace search result.
// Defined here (not imported from userspace) so internal/driver stays
// free of the internal/userspace import — the wiring in
// cmd/driver-agent adapts. Rank is DERIVED, not surfaced by the
// current userspace API: since SearchPackagesForIdentity returns
// []PackageView with no per-row score, the adapter assigns Rank by
// position (first result → 1.0, subsequent linearly decreasing to
// 0.5 for the 20th). A future userspace API extension can plug real
// FTS5 bm25 scores through the same interface without changing
// callers.
type PackageHit struct {
    Slug        string
    Description string
    Rank        float64 // derived; see comment above
}

func SetLookupDeps(d LookupDeps)
```

## 3. Query safety (spec §7 (a) + (b))

The user-supplied `query` string flows into:

1. `strings.Contains` over the in-process registry view's fully
   qualified names (harmless — no I/O side-effect).
2. `SearchPackagesForIdentity(q, ...)` — which passes `q` into an FTS5
   `MATCH ?` parameterised query.

Both are safe by construction, BUT FTS5 has reserved syntax
(`AND`, `OR`, `NOT`, `NEAR/N`, `^`, `*`, `-`, `"`, `(`, `)`) that can
turn a benign-looking query into an expensive scan or an accidental
NEAR/proximity query. To keep behaviour deterministic:

- Cap query length at 256 chars; truncate longer with a log line.
- BEFORE calling SearchPackagesForIdentity, sanitize the query:
  strip characters in `[^A-Za-z0-9_ .-]`, collapse whitespace,
  trim. The sanitized form is what goes to FTS5 AND is what the
  ablation-skip log line echoes.
- Cap result rows from FTS5 at 20 — matches
  `SearchPackagesForIdentity`'s default when limit<=0, but pass 20
  explicitly so a future change to the default doesn't widen this
  surface.
- After merge + dedup, cap total returned `[]Hit` at 20 — this is a
  belt-and-braces cap for the DoS-via-huge-query concern.

## 4. Registry read path

**Source**: the same in-process per-slave view B6's
`driver.snapshotAll()` builds. Reading a slave's dynamic_mcp.yaml
remotely is REJECTED as a design because it would re-introduce the
"driver reads slave file" trap B6 §7 (j) explicitly closes. The
lookup reads:

```go
names, descHashFn := snapshotAll()
```

Match rules apply to `names` (each name is `<slave_id>:<mcp_name>`; we
split on the last `:` for matching but keep the fully-qualified name
in the returned Hit for downstream identification):

- Exact `mcp_name` match: score 1.0.
- Case-insensitive substring on `mcp_name`: score 0.7.
- Tool-name substring match is NOT supported here — the per-slave
  view stores only names + spec hashes (§3.4), not the tool list. A
  future enhancement could extend the view to include a tool-name
  bag; deferred.
- Highest single-server score wins if multiple rules hit.

Trade-off (documented in §7 (j)): this loses the ability to match
against a slave-local MCP that was registered by a PRIOR driver
process. For the paper's eval semantics (fresh-state slaves per §J.1)
this is acceptable; prod deployments should call an initial "batch
import" cycle at startup to seed the view.

## 5. Metric implementation

Two levels of counters, so D2 can aggregate `RegistryLookupHitRate`
by (run, workload, ablation) without needing to reset per-call state:

1. **Per-process aggregate** (used by driver-agent debug endpoints
   only): `LookupHitRate()` returns the ratio.
2. **Per-invocation raw sample** (used by D2): every non-ablated
   `Lookup` call also writes ONE row into a new
   `registry_lookup_samples` observer table via an injected
   `SampleWrite func(ctx, RegistryLookupSample) error` dependency
   (nil-safe — nil means "no observer store wired", pipeline logs
   a first-call warn per §7 (d)).

DDL append to `internal/observerstore/schema.sql`:

```sql
-- WT-2-driver-promotion-chain B4: one row per driver.Lookup call
-- (with NoRegistryLookup off) so D2 can aggregate
-- RegistryLookupHitRate by run / workload / ablation. Carries
-- run_id EXPLICITLY (not derived by time-range JOIN) so Phase 3's
-- parallel-runs harness stays unambiguous. See
-- docs/specs/wt2-driver-promotion-chain-B4.spec.md §5.
CREATE TABLE IF NOT EXISTS registry_lookup_samples (
    row_id             TEXT PRIMARY KEY,
    ts                 TEXT NOT NULL,
    run_id             TEXT NOT NULL DEFAULT '',
    workspace_id       TEXT NOT NULL DEFAULT '',
    -- No query text stored; only an 8-hex-prefix hash of the raw
    -- query for cross-referencing with log lines. Avoids persisting
    -- user-intent text (which may contain alphanumeric secret-shaped
    -- material that the sanitizer cannot strip).
    query_hash_prefix  TEXT NOT NULL DEFAULT '',
    hit_count          INTEGER NOT NULL DEFAULT 0,
    registry_hits      INTEGER NOT NULL DEFAULT 0,
    userspace_hits     INTEGER NOT NULL DEFAULT 0,
    top_score          REAL NOT NULL DEFAULT 0.0
);
CREATE INDEX IF NOT EXISTS idx_registry_lookup_samples_run
    ON registry_lookup_samples(run_id, ts);
```

`RegistryLookupSample` shape:

```go
type RegistryLookupSample struct {
    Queried         time.Time
    RunID           string   // MUST be non-empty when the driver was
                             //   started under an eval-runner run;
                             //   empty is accepted only for
                             //   ad-hoc / interactive driver sessions
    WorkspaceID     string
    QueryHashPrefix string   // 8-hex prefix of sha256(raw query);
                             //   correlates DB rows with log lines
                             //   without persisting user-intent text
    HitCount        int
    RegistryHits    int
    UserspaceHits   int
    TopScore        float64
}
```

`RunID` is read from a new `LookupDeps.CurrentRunID func() string`
(nil-safe — returns "" when the driver is not running under an
eval-runner). The eval-runner sets this via a small
`driver.SetCurrentRunID(runID string)` hook at run start; parallel
runs each own their own driver process (per the eval-runner's
process-per-run model in WT-1-eval-runner-skeleton), so a single
process's CurrentRunID is monotone.

D2's `RegistryLookupHitRate` computation, per (run, workload,
ablation):
`(SELECT COUNT(*) FROM registry_lookup_samples WHERE hit_count > 0
AND run_id = ?)
÷ (SELECT COUNT(*) FROM registry_lookup_samples WHERE run_id = ?)`.
JOIN on `run_id` — unambiguous even under Phase 3 parallel
runs, and does not require `workspace_id` in `runs` (which today
`internal/evalrun/schema.go` does not have).

The per-process `LookupHitRate()` aggregate is kept as a debug
convenience; production analysis MUST go through
`registry_lookup_samples` for per-run attribution.

**Ablation semantics unchanged**: `Lookup` under `NoRegistryLookup=on`
short-circuits BEFORE writing a sample row, so ablated runs produce
0 rows and D2 correctly treats their `RegistryLookupHitRate` as
"undefined / not applicable" rather than 0/0.

## 6. Ablation flag

New file `internal/driver/registry_lookup_ablation.go`:

```go
var (
    noRegistryLookup           bool
    noRegistryLookupInitErr    error
    noRegistryLookupWarnOnce   sync.Once
)

func IsNoRegistryLookup() bool { return noRegistryLookup }

func init() {
    // Register the *bool target for the FlagName already declared in
    // internal/ablation. Same never-panic-in-init pattern as
    // evalrun.DisableTelemetry.
    if err := ablation.Default.Register(ablation.NoRegistryLookup, &noRegistryLookup); err != nil {
        noRegistryLookupInitErr = err
        log.Printf("driver: ablation.Default.Register(NoRegistryLookup) failed: %v — --ablation NoRegistryLookup will not gate Lookup", err)
    }
}
```

`Lookup` reads `noRegistryLookupInitErr` on entry and, if non-nil,
emits a `sync.Once`-guarded `[error] NoRegistryLookup ablation wiring
inert (init err: <e>) — subsequent Lookup calls will run
unguarded` line. Matches the WT-1-run-schema pattern where init
failures are surfaced again on first-use to survive `2>/dev/null`.

This is `mustRegister`-in-spirit — the init logs an ERROR (not just
info) and the first Lookup call surfaces it again. Both are
observable. We do NOT `panic()` because ablation is a research knob;
DoS-ing the driver at boot because an ablation didn't register
would prevent operators from running any experiment at all.

When `noRegistryLookup=true`, `Lookup` emits `[ablation]
NoRegistryLookup: skipped query_sanitized=%q query_hash=<8-hex>` (per
§7 (h)) and returns `nil`.

## 7. Security

### (a) query length + sanitization

Cap at 256; sanitize per §3. FTS5-reserved characters that survived
sanitization (there are none in the accepted class `[A-Za-z0-9_ .-]`,
so this reduces to the length + character-class check).

### (b) FTS5 result count cap

Explicit `limit=20` passed to SearchPackagesForIdentity. Merge-and-dedup
cap AGAIN at 20 (§3).

### (c) `NoRegistryLookup` ablation observable

Every skipped call emits ONE structured log line. Silent skip would
break the paper's ablation audit story.

### (d) Nil deps degrade with warn, not panic

If `LookupDeps.UserspaceStore` is nil, the userspace branch is
skipped and a first-call warn log is emitted (via `sync.Once`). Same
for a nil `SampleWrite` (no sample rows recorded but aggregate
metric still updated). Panics in init or on first call would
DoS the driver.

### (e) Score doesn't leak internal state

`Score` is a plain float, not e.g. the raw FTS5 rank (which encodes
term frequencies + document lengths — mildly sensitive). We rescale
to [0,1] before returning.

### (f) `Lookup` never returns an error

Errors are logged and the affected source is dropped. A driver call
site that surfaces "we couldn't reach the userspace API" to the
user leaks internal-network shape and doesn't help the user; a
graceful degrade to registry-only is the right UX.

### (g) Perf assertion conditional

`TestLookup_PerfBench_ConditionalOnShort` — pre-populate the
per-slave view with 100 registered MCPs via `noteRegister` + a mock
UserspaceSearcher; assert Lookup completes in
< 10 ms. Guarded by `testing.Short()` — see B6 §7 (h) for the
established pattern.

### (h) Query in log — sanitized AND length-capped

The `[ablation]` log line echoes the SANITIZED query, truncated to
64 chars, NOT the raw input. Sanitizer strips FTS5 meta-characters
BUT does not filter alphanumeric secret-shaped strings (e.g. an API
key with only letters+digits would pass through). To bound leak
risk, the log truncates AND emits only a sha256-hex `[8:]` prefix
of the raw input as `query_hash=<8-hex>` alongside the sanitized
form — this lets an operator correlate log entries without
retaining the full raw query in disk-persistent logs. If the raw
input is empty the log omits the query_hash field.

Concretely: `[ablation] NoRegistryLookup: skipped
query_sanitized=%q query_hash=%s` with `%s` being an 8-hex prefix
of sha256(raw). Tests assert the log line contains neither the raw
input (in cases where it differs from sanitized) nor a full
64-hex hash.

## 8. Test plan

| Security | Test                                                                 |
|----------|----------------------------------------------------------------------|
| §7 (a)   | `TestLookup_QueryTruncatedAtCap` — 300-char input → sanitized to 256 |
| §7 (a)   | `TestLookup_SanitizerStripsReservedChars` — `foo NEAR/3 bar` → `foo NEAR3 bar` (space kept, `/` stripped) |
| §7 (a)   | `TestLookup_SanitizerStripsQuotesAndCarets` — `"foo^*"` → `foo` |
| §7 (b)   | `TestLookup_FTSResultCapAt20` — mock returns 100 entries, Lookup returns ≤20 |
| §7 (b)   | `TestLookup_MergedCapAt20` — 15 registry + 15 userspace unique names → 20 total |
| §7 (c)   | `TestLookup_NoRegistryLookupAblationSkipsAndLogs` |
| §7 (c)   | `TestLookup_NoRegistryLookupAblationDoesNotBumpCounters` — ablation on: LookupHitRate stays at whatever it was, queries counter does NOT advance |
| §7 (d)   | `TestLookup_NilUserspaceStoreDegradesToRegistry` |
| §7 (d)   | `TestLookup_EmptyRegistryViewDegradesToUserspace` — snapshotAll returns 0 names → Lookup relies on userspace only, no error. |
| §7 (h)   | `TestLookup_LogEchoesSanitizedAndPrefixHash_NotFullHashOrRaw` — pass a raw query with stripped chars and verify: log contains sanitized truncated form + `query_hash=<exactly 8 hex chars>`; log does NOT contain the raw input; log does NOT contain a 64-hex hash. |
| §7 (e)   | `TestLookup_ScoreIsInUnitInterval` — every hit's score ∈ [0,1] |
| §7 (f)   | `TestLookup_FTSErrorDoesNotPropagate` — mock returns error → Lookup returns registry-only, no error |
| §7 (g)   | `TestLookup_PerfBench_ConditionalOnShort` |
| Metric   | `TestLookupHitRate_IncrementsOnHit` — 3 queries, 2 hit; rate = 0.667 |
| Metric   | `TestLookupHitRate_ZeroWhenNoQueries` — pristine process → 0.0 |
| Registry | `TestLookup_RegistryExactMatch_ScoreOne` |
| Registry | `TestLookup_RegistrySubstringMatch_ScoreSevenTenths` |
| Sample   | `TestLookup_WritesSampleRowPerCall` — 5 non-ablated queries → 5 rows in `registry_lookup_samples`. Each row carries the correct run_id read from `LookupDeps.CurrentRunID`. |
| Sample   | `TestLookup_AblatedCallDoesNotWriteSample` — with NoRegistryLookup on, 0 rows written even after 5 calls. |
| Sample   | `TestLookup_SampleRowStoresHashPrefixNotQueryText` — query with a distinct string (`super-secret-1234567890`); sample row's `query_hash_prefix` is exactly 8 hex chars AND the sample-writer input never contained the raw string. |
| RunID    | `TestSetCurrentRunID_RoundTrip` — SetCurrentRunID("run-x"); CurrentRunID() == "run-x"; empty default. |
| RunID    | `TestSetCurrentRunID_ConcurrentSafe` — 8 goroutines flipping, 8 reading; assert never returns garbage under -race. |
| Draft    | `TestDraftTaskContract_CallsLookupAndAttachesHits` — driver-side integration: given an intent string, the returned draft carries a `registry_hits` field populated by `Lookup`. |
| Draft    | `TestDraftTaskContract_UnderNoRegistryLookup_NoHits` — with ablation on, `registry_hits` is nil / omitted. |

## 9. Files touched

Created:

- `multi-agent/internal/driver/registry_lookup.go`
- `multi-agent/internal/driver/registry_lookup_test.go`
- `multi-agent/internal/driver/registry_lookup_ablation.go`
- `multi-agent/internal/driver/registrylookupmetrics.go`
- `multi-agent/internal/driver/current_run_id.go` — houses the
  `SetCurrentRunID(runID string)` + `CurrentRunID() string` pair
  read by `LookupDeps.CurrentRunID`. Package-level `atomic.Pointer`
  so a set from the eval-runner goroutine is safely observed by the
  Lookup goroutine.
- `multi-agent/internal/observerstore/registry_lookup_samples_writer.go`
  — thin writer keyed on the observer store's *sql.DB, matching the
  route_reasons_writer / capability_snapshots_writer shape.
- `docs/specs/wt2-driver-promotion-chain-B4.{spec,plan}.md`

Modified:

- `multi-agent/cmd/driver-agent/main.go` — call
  `driver.SetLookupDeps(...)` after opening the observer store, wiring
  the workspace's userspace store (via
  `observerstore.SQLiteStore.DB()` shared handle → thin
  `userspaceSearcherAdapter{store: userspace.NewStore(db)}`).
- `multi-agent/internal/driver/contract_tools.go` (`draftTaskContractTool.Call`)
  — call `Lookup` on the user's intent before returning the draft;
  attach hits to the response under a new `registry_hits` field so
  the driver's LLM sees them. Skip when NoRegistryLookup is on
  (Lookup returns nil naturally).
- `multi-agent/internal/observerstore/schema.sql` — append
  `registry_lookup_samples` DDL per §5.
- `multi-agent/internal/observerstore/store.go` — `ensureColumns`
  needs no change (table is CREATE TABLE IF NOT EXISTS only; no
  ALTER paths).

## 10. What this spec explicitly does NOT do

- Does NOT modify `internal/userspace`. The store search API already
  exists; we adapt via an interface in the driver-agent wiring.
- Does NOT touch B1 or B2 code paths beyond a doc-comment tie-in. The
  `draft_task_contract` integration in §9 is the driver-side surface
  B4 owns; B1's promote-candidate surfacing logic is a distinct
  surface with different semantics.
- Does NOT define a new observer EVENT (the observer.Event stream is
  separate from the per-invocation sample table). Sample rows land in
  `registry_lookup_samples` via a direct SQL writer per §5.
- Does NOT sanitize registry-side content. Registered MCP names are
  already regex-constrained by `buildspec.Validate` in B6's register
  path.
- Does NOT wire the `RegistryLookupHitRate` aggregate into the D1
  `runs` write. That's a follow-up in WT-2-metric-extract; D2 reads
  the per-invocation `registry_lookup_samples` table directly by
  `run_id` per §5.
