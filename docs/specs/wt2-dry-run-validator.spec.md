# WT-2-dry-run-validator — §A3 four-class pre-exec check

**Worktree:** `paper/v3/p2-dry-run-validator`  \
**Baseline:** `origin/paper/v3-integration` = `d053897`  \
**Scope:** todo_list.md Phase 2 line 97 · 12号 §A3 · 14号 memo §5 handoff  \
**Owner:** single-owner worktree

Phase 1 shipped the contract schema (`TaskContract` + `RecoveryHint`) and
`ContractCompleteness`; the `dryRunContractTool` in `internal/driver/`
already covers **skill / tool / resource** availability against advertised
agent cards. It does **not** yet cover the four §A3 host-side pre-execution
faults that operate against the WT-1 `capability.Snapshot`:

| Class              | Snapshot input                                | Contract input                                  |
| ------------------ | --------------------------------------------- | ----------------------------------------------- |
| `missing_file`     | `Snapshot.Files[]` (`kind_detail`+`path_pattern`) | `DataContract.ReadArtifacts[].Name`         |
| `wrong_version`    | `Snapshot.Tools[]` (`Name`+`Version`)         | `CapabilityRequirements.Tools[]` min-version    |
| `forbidden_cred`   | `Snapshot.Credentials[]` (alias strings)      | `CapabilityRequirements.ForbiddenAliases[]`     |
| `policy_violation` | `Snapshot.Network` (`NetworkReach`)           | `ExecutionPolicy.RequiredReach`                 |

This spec adds those four checks in a new `internal/contract/validator/`
subpackage, extends `dryRunContractTool` to invoke it, persists blocks to
a new `dry_run_blocks` table, emits three per-invocation metric events,
and registers the `NoDryRun` ablation flag with mandatory bypass logging.

## 1. Files touched

| File                                                                     | Change       |
| ------------------------------------------------------------------------ | ------------ |
| `multi-agent/internal/contract/validator/validator.go`                   | **new pkg**  |
| `multi-agent/internal/contract/validator/validator_test.go`              | **new**      |
| `multi-agent/internal/contract/validator/ablation.go`                    | **new**      |
| `multi-agent/internal/contract/validator/ablation_test.go`               | **new**      |
| `multi-agent/internal/contract/types.go`                                 | +2 fields   |
| `multi-agent/internal/driver/capability_tools.go`                        | extend tool  |
| `multi-agent/internal/driver/capability_tools_test.go`                   | +injects     |
| `multi-agent/internal/observerstore/schema.sql`                          | +table       |
| `multi-agent/internal/observerstore/dry_run_blocks_writer.go`            | **new**      |
| `multi-agent/internal/observerstore/dry_run_blocks_writer_test.go`       | **new**      |

`contract.TaskContract` needs three additive fields to carry the pre-exec
inputs (numbered below):

1. `CapabilityRequirements.ForbiddenAliases []string` — alias strings the
   caller declares MUST NOT be present on the executing host. Non-secret;
   already-registered `capability.CredentialAlias` domain (`^[a-z][a-z0-9_]{2,63}$`).
2. `CapabilityRequirements.Tools` upgrade: today `Tools` is `[]string`
   (opaque names). Introduce a sibling **`ToolRequirements
   []ToolRequirement`** where `ToolRequirement{Name, MinVersion}` carries the
   version predicate. The legacy `Tools []string` field stays for backward
   compatibility with dispatchers that only care about presence; the two
   never diverge (see §3.2).
3. `ExecutionPolicy.RequiredReach capability.NetworkReach` (optional,
   empty ⇒ no reach constraint). Reuses the WT-1 enum verbatim to avoid
   a parallel string vocabulary.

## 2. Validator interface (`internal/contract/validator/`)

```go
package validator

import (
    "context"

    "github.com/yourorg/multi-agent/internal/capability"
    "github.com/yourorg/multi-agent/internal/contract"
)

// Severity is deliberately reduced to a single value in this spec —
// every block short-circuits the dispatch. Kept as a type so future
// classes (advisory / info) can extend without an API break.
type Severity string

const SeverityBlock Severity = "block"

type Kind string

const (
    KindMissingFile     Kind = "missing_file"
    KindWrongVersion    Kind = "wrong_version"
    KindForbiddenCred   Kind = "forbidden_cred"
    KindPolicyViolation Kind = "policy_violation"
)

// Block is one violation surfaced by a Validator. Detail is the
// human-readable rendering; Field / Expected / Actual are the machine
// parts §7(f) constrains.
type Block struct {
    Kind     Kind     `json:"kind"`
    Severity Severity `json:"severity"`
    Field    string   `json:"field"`     // e.g. "capability_requirements.tools[0].min_version"
    Expected string   `json:"expected"`  // e.g. ">=1.22.0"
    Actual   string   `json:"actual"`    // e.g. "1.18.4"
    Detail   string   `json:"detail"`    // sprintf of the three above, ≤ 8 KiB
}

// Validator runs every registered check against (tc, snap) and returns
// zero or more blocks. Implementations MUST be pure — no I/O, no
// filesystem access, no network. The context is threaded only so
// future block-cancellation surfaces are compatible; the default
// StaticValidator never observes it.
type Validator interface {
    Check(ctx context.Context, tc contract.TaskContract, snap capability.Snapshot) []Block
}

// New returns the default validator that runs all four §A3 checks in
// deterministic order (missing_file, wrong_version, forbidden_cred,
// policy_violation). Deterministic ordering is a testability contract:
// consumers grep by index in table tests.
func New() Validator
```

`New()` returns a concrete `staticValidator` running four private
functions. Adding a check is one line + one test — no plugin registry
(YAGNI).

## 3. Four check semantics

### 3.1 `missing_file`

For each `ReadArtifact` where `Kind == "file"` (or `Kind == ""` and the
`Name` looks path-shaped — has a slash or a dot-extension), require that
**some** `FileResource` in `snap.Files` has a matching `PathPattern`
under the same `KindDetail` bucket. Matching semantics:

- `PathPattern` is treated as a **glob**, matched with the Go stdlib
  `path.Match` (forward-slash separator; no `**`). This is deliberately
  the same primitive used elsewhere in the codebase; §7(a) bans
  compiling arbitrary regex from caller input.
- Match test: `path.Match(fileRes.PathPattern, artifact.Name)` for **any**
  `fileRes`. If none matches → emit `Block{Kind: KindMissingFile,
  Field: "data_contract.read_artifacts[i].name", Expected: <name>,
  Actual: "no snapshot file resource matches"}`.
- Empty snapshot (no `Files` at all) with any file-shaped artifact →
  emit one block per artifact.
- `path.Match` errors (malformed pattern) → treat as no-match and log
  `[validator] malformed path pattern` at WARN level; do NOT surface
  the pattern in `Block.Detail` (§7(f)).

`ArtifactRef.Path` per the prompt: the current `ArtifactRef` uses `Name`,
not `Path`. Reuse `Name` as the path-shape input; documented at the
declaration site.

### 3.2 `wrong_version`

For each `ToolRequirement{Name, MinVersion}` where `MinVersion` is non-empty:

- Look up `snap.Tools[i].Version` where `Name` matches (case-sensitive).
- If no snapshot entry: emit `wrong_version` block with `Actual =
  "<not installed>"` — this is a distinct failure from `missing_file`
  because a tool absence is a tool problem, not a file problem.
- If snapshot entry exists: compare using `golang.org/x/mod/semver` (see
  §7(b)). Both operands are first passed through a local
  `ensureVPrefix(s string) string` helper (`if s != "" && s[0] != 'v' {
  return "v" + s }` — required because `semver.Canonical("1.22.0")`
  returns `""`), then normalised via `semver.Canonical` (which returns
  `""` for still-invalid input);
  an unparseable snapshot version emits a block with
  `Actual = "<unparseable: X>"`, and an unparseable `MinVersion` is a
  contract-side programmer error → the block emits with
  `Actual = "<contract min_version unparseable: Y>"` so the operator
  fixes their contract instead of silently passing.
- Comparison: `semver.Compare(canonSnap, canonReq) < 0` ⇒ block.
- **Prerelease semantics**: `semver.Compare` treats `v1.22.0-rc1 <
  v1.22.0`, which is the correct behaviour for §7(b) — an `rc1`
  prerelease is NOT ≥ a released `1.22.0`.

`Tools []string` (legacy) is treated as "presence-only requirement" —
absent from snapshot → still counts as `wrong_version` with
`Expected = "<any version installed>"`, `Actual = "<not installed>"`.
Rationale: a tool the contract lists but never provides is a fault the
validator must catch even when the caller didn't specify a version.

### 3.3 `forbidden_cred`

For each `alias` in `CapabilityRequirements.ForbiddenAliases`:

- Compare with **exact case-sensitive string equality** against every
  `snap.Credentials[i]` (a `capability.CredentialAlias`). §7(e) forbids
  prefix / substring / case-insensitive matching.
- Hit → `Block{Kind: KindForbiddenCred, Field:
  "capability_requirements.forbidden_aliases[i]", Expected: "<absent>",
  Actual: <alias>}`. The alias IS the leaked datum, and it lives in the
  contract already — surfacing it in `Actual` is not a new disclosure.
- Malformed alias in the contract (fails `capability.NewCredentialAlias`
  shape check) → emit `Block` with `Actual = "<malformed alias>"` so the
  operator fixes their contract instead of silently passing.

### 3.4 `policy_violation`

Single check: `tc.ExecutionPolicy.RequiredReach` vs `snap.Network`.
Semantic ladder (least-to-most permissive): `none < loopback-only <
intranet < internet`. `RequiredReach` is a **minimum** — the host must
have AT LEAST that reach.

- Empty `RequiredReach` ⇒ no constraint, no block.
- `snap.Network` value not in the enum ⇒ block (defensive: reject
  hand-built snapshots that skip `NewSnapshot`).
- Snapshot reach less permissive than required ⇒ block with
  `Field = "execution_policy.required_reach"`, `Expected = <required>`,
  `Actual = <snap.Network>`.

## 4. `dryRunContractTool` extension

### 4.1 Input schema

Extend the tool's JSON schema to accept an **optional**
`capability_snapshot` field whose shape matches `capability.Snapshot`.
Semantics:

- Field **present**: parse, validate through `capability.NewSnapshot`
  (which enforces the WT-1 §3.4 invariants), then run all four §A3
  checks against it. Any parse / invariant failure returns a
  `MCPToolError{Category: FailContractViolation}` — same class the
  existing tool uses for a malformed contract, so a caller cannot
  distinguish a validator-off happy path from a broken-input dry-run.
- Field **absent**: the tool emits `[]Block{}` for the four new
  classes and retains its existing `recommended_route` output verbatim
  (backward compatibility). The three §5 events still fire, all with
  `numerator = 0`, so the metric denominator is not skewed by callers
  who omit the snapshot.

Rationale: the caller-driven-input pattern mirrors
`analyzeContractCapabilities` already using `DiscoverAgents` results,
keeps the tool a pure function of its args, and lets the executor
supply a snapshot with any provenance (WT-1 uploader, cached row,
hand-crafted eval fixture).

### 4.2 Output shape

The existing `dryRunReport` struct stays as-is (backward compatible).
Added:

```go
type dryRunReport struct {
    // ... existing fields ...
    Blocks    []validator.Block `json:"blocks"`
    AttemptID string            `json:"attempt_id"`
}
```

`AttemptID` is a 128-bit random hex string (via the existing
`randomHex(16)` helper — `crypto/rand` → 16 bytes → 32 hex chars).
Not a formal UUIDv4 (no version/variant bit-fiddling); collision
resistance is equivalent for this purpose. Groups every block from one
invocation for §5 metrics and §6 consumer SELECTs. `Runnable` is
AND-ed with `len(Blocks) == 0`.

### 4.3 Side-effects — order

1. Parse + validate contract (existing).
2. Discover agents + build resource snapshot (existing).
3. **Fetch existing `recommended_route`** (existing).
4. **NEW:** `blocks := validator.New().Check(ctx, tc, snap)`.
5. **NEW:** compute `contract_hash := sha256(canonical(tc))` and
   `capability_snapshot_hash := snap.Hash()`.
6. **NEW:** persist one row per block to `dry_run_blocks` via the new
   writer (best-effort — failure logs `warnings` but doesn't fail the
   tool call; matches existing `SaveResourceSnapshot` pattern in
   `inspectCapabilitiesTool`).
7. **NEW:** emit exactly three events (§5) via the observer relay.
8. Return `dryRunReport` with `Runnable = existingRunnable &&
   len(blocks) == 0`.

Ablation: the `NoDryRun` short-circuit runs BEFORE step 4 (see §7 for
the exact log line). Steps 1–3 always run — the existing
`recommended_route` behaviour is not gated by `NoDryRun`.

## 5. Metric events

Three events emitted per invocation into `observerstore.events` (the
existing conduit; the WT-1 route_reasons writer follows the same
"single per-decision emit" pattern). Emitted even when zero blocks fire
— that is what makes the ratios in §6 computable.

| `events.type`                     | Emitted always? | Payload                                                                             |
| --------------------------------- | --------------- | ----------------------------------------------------------------------------------- |
| `PreExecutionFaultCatchRate`      | ✔               | `{numerator: 0|1, blocks_total: N, attempt_id, contract_hash, experiment_id}`       |
| `MissingArtifactDetectionRate`    | ✔               | `{numerator: 0|1, missing_files: N, attempt_id, contract_hash, experiment_id}`      |
| `PolicyViolationPreventionRate`   | ✔               | `{numerator: 0|1, policy_violations: N, attempt_id, contract_hash, experiment_id}`  |

`experiment_id` is copied from the same source as
`dry_run_blocks.experiment_id` (§6). Empty string when the dry-run
happens outside an experiment. The §6.1 `PolicyViolationPreventionRate`
SELECT reads it via `json_extract(payload, '$.experiment_id')`.

Semantics:

- `PreExecutionFaultCatchRate` `numerator = 1` iff **any** block fires.
- `MissingArtifactDetectionRate` `numerator = 1` iff at least one
  `missing_file` block fires.
- `PolicyViolationPreventionRate` `numerator = 1` iff at least one
  `policy_violation` block fires.

Consumer computes `rate = SUM(numerator) / COUNT(*)` per event type.
Denominator is **every** dry-run invocation because the event fires
unconditionally.

Event fields: `type` as above; `task_id = ""` (dry-run has no task);
`payload` = JSON body above. `agent_id` / `workspace_id` are taken from
the tool's owning driver — the same values `SaveResourceSnapshot`
already uses.

Best-effort emission: relay error logs a warning in the tool's
`warnings` slice, does NOT fail the dry-run call. §5.1 test asserts
that a failing relay does not corrupt the returned `Blocks`.

### 5.1 Ablation-off contract

When `IsDryRunDisabled() == true`, all three events count as **zero**
— i.e. the writer emits nothing, and the mandatory log line (§7(d)) is
the ONLY externally-observable side-effect. The test in §6.5 asserts:
zero rows in `dry_run_blocks`, zero matching rows in `events`, and one
matching log line.

## 6. `dry_run_blocks` table

```sql
-- WT-2-dry-run-validator: §A3 four-class pre-execution block audit.
-- One row per Block returned by validator.Check(...) that the dry-run
-- tool persisted. Consumer view §7(g): SELECT ... FROM dry_run_blocks
-- LEFT JOIN runs ON dry_run_blocks.contract_hash = runs.task_contract_hash.
CREATE TABLE IF NOT EXISTS dry_run_blocks (
    block_id                 TEXT PRIMARY KEY,
    attempt_id               TEXT NOT NULL,
    conversation_id          TEXT NOT NULL,
    experiment_id            TEXT NOT NULL DEFAULT '',
    contract_hash            TEXT NOT NULL,
    capability_snapshot_hash TEXT NOT NULL,
    block_kind               TEXT NOT NULL
        CHECK (block_kind IN ('missing_file','wrong_version','forbidden_cred','policy_violation')),
    field                    TEXT NOT NULL DEFAULT '',
    expected                 TEXT NOT NULL DEFAULT '',
    actual                   TEXT NOT NULL DEFAULT '',
    detail                   TEXT NOT NULL DEFAULT '',
    blocked_at               TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_dry_run_blocks_conv
    ON dry_run_blocks(conversation_id, blocked_at);
CREATE INDEX IF NOT EXISTS idx_dry_run_blocks_contract_hash
    ON dry_run_blocks(contract_hash);
CREATE INDEX IF NOT EXISTS idx_dry_run_blocks_attempt
    ON dry_run_blocks(attempt_id);
```

Writer (`observerstore/dry_run_blocks_writer.go`):

```go
type DryRunBlockRow struct {
    BlockID                string
    AttemptID              string
    ConversationID         string
    ExperimentID           string
    ContractHash           string
    CapabilitySnapshotHash string
    BlockKind              string
    Field                  string
    Expected               string
    Actual                 string
    Detail                 string
    BlockedAt              time.Time
}

type DryRunBlockWriter interface {
    WriteDryRunBlock(ctx context.Context, r DryRunBlockRow) error
}
```

Implementation mirrors `route_reasons_writer.go`:

- Constant `INSERT ... VALUES(?,?,...)` — all placeholders, never
  concatenated (§7(c)).
- `detail` is truncated to `maxDetailBytes = 8192` bytes at the writer
  boundary (defense-in-depth; the validator also caps it at construction).
- `ON CONFLICT(block_id) DO NOTHING` — the driver may retry the write
  after a transient failure without duplicating rows.
- `field`, `expected`, `actual`, `detail` scrubbed with
  `secretscrub.Sanitize` before insert (matches `route_reasons_writer`
  pattern; forbidden_cred aliases are non-secret by construction but
  the guard is cheap and closes any writer-direct bypass).

### 6.1 Consumer view (§7(g))

For `PolicyViolationPreventionRate`, the consumer runs:

```sql
-- numerator: dry-run attempts that fired at least one policy_violation
WITH policy_blocked_attempts AS (
    SELECT DISTINCT attempt_id
    FROM dry_run_blocks
    WHERE block_kind = 'policy_violation'
)
-- denominator: EVERY dry-run attempt (whether blocked or clean)
, all_attempts AS (
    SELECT DISTINCT attempt_id
    FROM dry_run_blocks           -- attempts that logged at least one block
    UNION
    SELECT DISTINCT
        json_extract(payload, '$.attempt_id') AS attempt_id
    FROM events
    WHERE type = 'PreExecutionFaultCatchRate'   -- fires on EVERY attempt
)
SELECT
    (SELECT COUNT(*) FROM policy_blocked_attempts) * 1.0 /
    NULLIF((SELECT COUNT(*) FROM all_attempts), 0) AS rate;
```

Runs-table join for experiment scoping:

```sql
SELECT r.experiment_id,
       (SELECT COUNT(DISTINCT b.attempt_id)
        FROM dry_run_blocks b
        WHERE b.experiment_id = r.experiment_id
          AND b.block_kind = 'policy_violation') * 1.0 /
       NULLIF((SELECT COUNT(*)
               FROM events e
               WHERE e.type = 'PreExecutionFaultCatchRate'
                 AND json_extract(e.payload,'$.experiment_id') = r.experiment_id), 0)
FROM (SELECT DISTINCT experiment_id FROM runs) r;
```

Both queries are **testable** — §6.3 of the plan runs each SELECT against
a seeded SQLite fixture and asserts the numeric result.

### 6.2 Column rationale

- `attempt_id`: groups multiple blocks from one invocation for the
  DISTINCT counts above. Blocked dispatches never reach `runs` — a
  runs-only denominator would silently omit every attempt this
  validator successfully prevented, defeating the metric.
- `experiment_id`: partitions blocks by eval run. Copied from the same
  slot the driver reads for `runs.experiment_id` (see WT-1-run-schema).
  Empty string when the dry-run happens outside an experiment.
- `field / expected / actual`: mirror the Block struct so the SELECT can
  reconstruct a diagnostic without loading `detail`. Also lets us keep
  `detail` scrubbable / truncatable without losing structured data.

## 7. Security

### (a) Validator purity

`Validator.Check` is pure — no filesystem, no network, no goroutines, no
state outside its return value. `staticValidator` holds no mutable
state after construction. Enforced by:

- Package `internal/contract/validator/` imports only `context`,
  `errors`, `fmt`, `path`, `strings`, `sort`, `golang.org/x/mod/semver`,
  and the local `capability` + `contract` packages. `go vet` +
  package-import test asserts no `os` / `net` / `io` / `database/sql`.
- Rationale: a dry-run tool called by an untrusted caller MUST NOT
  become an attack surface for side-effects. Every I/O side-effect
  (event emit, DB write) lives in the **caller** — the driver tool —
  where the audit / auth surface already exists.

### (b) Semver — library, not split-by-dot

Use `golang.org/x/mod/semver` verbatim: `semver.Canonical` for
normalisation, `semver.Compare` for the ordering test. **Forbidden**:

- Hand-rolled `strings.Split(v, ".")` followed by `strconv.Atoi`.
- Any parser that discards the `-<prerelease>` suffix.

Enforcement: the plan's static-analysis note lists a grep-based CI
guard (`grep -R "strings.Split.*\"\\.\"" internal/contract/validator`
returning non-empty → fail). Negative test §6.2 asserts `v1.22.0-rc1`
is judged `< v1.22.0`.

### (c) SQL — parameterized + bounded row

- Every `INSERT` / `SELECT` in `dry_run_blocks_writer.go` uses `?`
  placeholders. Grep guard: `strings.Contains(sql, "'")` in a test
  asserts no single quotes leak into a query literal.
- `detail` truncated to `maxDetailBytes = 8192` at BOTH validator
  construction time AND writer boundary; the writer's cap is the
  authoritative one for DoS defence. A validator-produced 20-KiB
  `detail` (should not happen, but hand-built) is truncated to 8 KiB
  before the driver hands it to `Exec` — a `<... truncated>` sentinel
  is appended.

### (d) Ablation — no silent bypass

`internal/contract/validator/ablation.go` registers `NoDryRun` against
a package-private `disableDryRun bool`:

```go
package validator

import "github.com/yourorg/multi-agent/internal/ablation"

var disableDryRun bool

func IsDryRunDisabled() bool { return disableDryRun }

// SetDryRunDisabled is the test-only mutator. Production code MUST use
// ablation.Default.SetByName(...) — the CLI binder does this once,
// before the driver starts.
func SetDryRunDisabled(v bool) { disableDryRun = v }

func init() {
    if err := ablation.Default.Register(ablation.NoDryRun, &disableDryRun); err != nil {
        log.Printf("validator: ablation registration failed: %v", err)
    }
}
```

`dryRunContractTool.Call` checks `IsDryRunDisabled()` after validating
the contract and before invoking `Check`. If true it:

1. Emits log line:
   `[ablation] NoDryRun: skipped conversation=<conversation_id>`  \
   Uses the stdlib `log` package (matches WT-1 pattern). The
   `conversation_id` is a required contract field; NEVER emit an empty
   conversation-id in the log — a missing id is a validation failure
   caught by step 1.
2. Emits ZERO metric events.
3. Persists ZERO `dry_run_blocks` rows.
4. Returns `dryRunReport` with `Blocks: []` and existing route fields
   intact (route recommendation is not gated by `NoDryRun` — the
   ablation targets pre-exec-fault checks specifically).

Test §6.5 asserts the log line + event count + row count.

### (e) `forbidden_cred` — exact match

Comparison operator: `snap.Credentials[i] == contract.ForbiddenAliases[j]`
using Go `==` on the underlying string. **Explicitly banned** and
tested:

- `strings.HasPrefix` / `strings.Contains` / `strings.HasSuffix`.
- `strings.EqualFold` (case-insensitive).
- Regex compilation of the alias.

Negative test §6.4 seeds forbidden `openai_for_glm` and asserts
`openai_for_glm_backup`, `Openai_For_Glm`, and `openai_for_glm_v2` all
pass through as non-matches. The exact match is the ONLY guarantee the
contract writer can rely on — every substring-tolerant matcher creates
false positives that cause an operator to add sentinel prefixes and
those prefixes then themselves become bypass vectors.

### (f) `Block.Detail` — never raw dump

`Detail` is `fmt.Sprintf("%s: expected %s, actual %s", field, expected,
actual)`. These three inputs are:

- `Field`: a static literal from validator code (e.g.
  `"execution_policy.required_reach"`). Never operator-controlled.
- `Expected`: a static value from the contract (a version string, alias
  name, reach enum, or single artifact path). Bounded length; caller
  can't smuggle Kbytes because contract fields have their own upper
  bounds (RecoveryHint 4096 runes cap etc.).
- `Actual`: a single snapshot field value (a version string, an alias
  string, a `NetworkReach` enum, or the literal `"<not installed>"` /
  `"<unparseable: X>"` sentinels). Bounded similarly.

**Forbidden**: dumping `json.Marshal(tc)`, `json.Marshal(snap)`, or any
other multi-field payload into `Detail`. The one-field-plus-three-value
shape is the API surface — extending it requires a spec amendment.

Length guard: `Detail` is truncated to `maxDetailBytes = 8192` at
construction and again at writer entry. Test §6.6 asserts an
adversarial 40-KiB `expected` cannot balloon the row.

### (g) Consumer-view reverse-audit

Given only `dry_run_blocks` + `runs`, a `PolicyViolationPreventionRate`
scorer must compute `policy_violation blocks / total dispatch
attempts`. **Blocked dispatches never reach `runs`** — the whole point
of the block is to prevent execution. Therefore `runs` alone cannot
supply the denominator.

Resolution (implemented in schema §6):

- `dry_run_blocks.attempt_id` groups blocks from one dry-run
  invocation. `COUNT(DISTINCT attempt_id)` on the table gives the
  count of attempts that were **partly-blocked** — necessary but not
  sufficient (clean attempts write no rows).
- `events` of type `PreExecutionFaultCatchRate` fires on EVERY
  invocation (§5). `COUNT(*)` on those rows is the total denominator.
- `experiment_id` in both `dry_run_blocks` and `events.payload` scopes
  the ratio per experiment; `runs.experiment_id` provides the outer
  distinct-experiment set.

§6.1 SELECT is testable in §6.3 of the plan.

## 8. Acceptance

- [ ] 4 inject tests (`missing_file`, `wrong_version`, `forbidden_cred`,
      `policy_violation`) each fire exactly one block.
- [ ] 3-metric counts are correct on a mixed dry-run (one block from
      each class + one clean call).
- [ ] `NoDryRun` gate:
    - [ ] Zero events emitted (event-table SELECT returns 0).
    - [ ] Zero rows in `dry_run_blocks`.
    - [ ] Exactly one log line matching `\[ablation\] NoDryRun:
          skipped conversation=<id>` with the invocation's real
          conversation-id.
- [ ] Consumer-view SELECT §6.1 returns the correct rate on a
      seeded fixture (5 invocations, 2 with policy_violation ⇒ 0.4).
- [ ] `dryRunContractTool` still returns backward-compatible
      `recommended_route` when `capability_snapshot` is omitted (no
      new fault classes surface, but the existing route recommendation
      is unchanged).
- [ ] `go test ./internal/contract/validator/... ./internal/driver/...
      ./internal/observerstore/... -count=1 -shuffle=on -race` passes.
- [ ] `go vet ./...` clean.
- [ ] Commit trailer: `Co-Authored-By: Claude Opus 4.8 (1M context)
      <noreply@anthropic.com>`.
- [ ] No `git push` (spec-code and workflow constraint).
