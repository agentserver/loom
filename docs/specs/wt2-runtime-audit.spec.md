# WT-2-runtime-audit — Spec

> Scope: add a per-execution runtime audit layer to `multi-agent/internal/journal/`
> that records the actual read / write / tool_call / model_call events observed at
> the driver–executor seam, diffs them against the `contract.TaskContract` in
> force for the conversation, and persists the diff as a read-only
> `contract_violations` view on top of a new `audit_events` table. On the same
> commit path it also feeds sha256 write hashes to the D1 `runs.artifact_hashes`
> column via an `ArtifactHashAppender` seam so §D2 metric extraction can compute
> the `ContractViolationRate` metric (12 号 §A4, todo_list Phase 2 line 98).
>
> **Deliverable file set (do not widen — other Phase-2 worktrees own the sibling
> files):**
> - `multi-agent/internal/journal/audit.go` (new)
> - `multi-agent/internal/journal/audit_test.go` (new)
> - `multi-agent/internal/observerstore/schema.sql` (**append only** — `audit_events`
>   table + `contract_violations` view DDL at the tail; do not reorder existing DDL)
> - `multi-agent/internal/observerstore/contract_violations_view.go` (new — pure
>   query helper + the writer for `audit_events`)
> - `multi-agent/internal/observerstore/contract_violations_view_test.go` (new)
>
> **Instrumentation wiring is DEFERRED to a follow-up WT (out of scope here).**
> The instrumentation-site enumeration in §3 is DESIGN DOCUMENTATION for the
> follow-up wiring WT (candidate: WT-2-audit-wiring, spawned after this one
> lands) — the current WT ships the *core* (types, recorder, verifier, view,
> writer, tests) inside the five files above and does not touch
> `internal/executor/*`, `cmd/slave-agent/*`, or `pkg/agentbackend/*` at all.
> This avoids racing with WT-2-dry-run-validator, WT-2-driver-promotion-chain,
> and any Phase-2 sibling that also edits those files. The follow-up WT is
> guarded by a design test (§7 (i), spec §3.5) so it stays "additive only".
>
> **Out of scope:**
> - No changes to `internal/executor/executor.go`'s `Task` / `Result` / `Sink`
>   signatures.
> - No new field on `BashConfig` / `FileConfig` / `MCPExecutor` / `backendExecutor`
>   in this WT — see the design note above.
> - No new ablation flag. `NoObserver` (already registered by WT-1-run-schema)
>   silences the DB write path implicitly because the writer wraps a `*sql.DB`
>   owned by the caller; if the caller passes a nil writer the recorder is a
>   no-op (§2.2).
> - No dispatch-time diff. `Verifier.Verify` runs **after** the task returns and
>   before the run-schema row is inserted; §A3 (WT-2-dry-run-validator) still
>   owns the pre-execution check.
> - No pg schema. SQLite-only, same posture as WT-1-routing-trace §6 (a pg
>   follow-up ships with a `$N`-flavor writer or dies with a syntax error at
>   first call, exactly as the routing-trace writer does today).

## 1. Background

`internal/contract.TaskContract` (WT-1 A2, PR #52) makes the *declared* read
artifacts, write targets, allowed tools, and required skills first-class. But
today, once dispatch hands the task to `executor.BashExecutor.Run` /
`FileExecutor.Run` / `MCPExecutor.Run` / `backendExecutor.Run` (slave-agent
main.go:474), nothing checks that the *actual* filesystem reads, filesystem
writes, tool invocations, or model calls match what the contract declared.
The consequence:

- A slave with a legitimate contract can silently read `/etc/shadow` or write to
  `/tmp/leaked.tar` and no analytics catch the gap.
- `runs.artifact_hashes` (WT-1 D1 column, §evalrun/schema.go:42) has a validated
  regex (`^[a-f0-9]{64}$`) but no *producer* — every current call site passes
  `nil` or `[]`. The oracle in §F1 has no way to check "did the workload
  actually write the expected artifact bytes."

The routing-trace writer (WT-1 C2) established the pattern: pluggable writer +
`secretscrub.Sanitize` at both the emit-side and the writer-boundary + monotonic
timestamps. This spec reuses that pattern for audit events, and adds a purely
read-time `contract_violations` view so no violation-row insert path can drift
away from the raw event insert path.

### 1.1 Why a new `audit_events` table and not the existing `events` table

The existing `events` table (`schema.sql:40`) is the wire schema of
`internal/observer` — chat progress, subtask summaries, mcp registration
callbacks. Adding a `type = 'audit_read'` bucket there would (a) require the
observer HTTP handler to grow a new payload shape and route it through the
event bus, (b) let any component with `WriteEvent` permission forge audit
events (a real security regression — the observer HTTP surface is
network-exposed inside the workspace), and (c) mix two invariants (chat
delivery ordering vs audit completeness) on one table.

A dedicated `audit_events` table with a **narrow writer** owned by this
package (`WriteAuditEvent`) is:
- Not reachable from any HTTP handler in this repo (grep verified).
- Not part of any existing consumer's projection.
- Easy to lock down at the migration level without touching `events`.

### 1.2 Why the diff is a VIEW, not a table

`contract_violations` is a **derived read** of `audit_events` joined against the
in-force `task_contracts` row for the conversation. If it were a physical
table, three separate writers would need to stay in sync:

1. `WriteAuditEvent` (raw event).
2. `Verifier.Verify` output (diff rows).
3. Any future contract mutation (must invalidate diff rows for that conv).

A view collapses (2) and (3) into a `SELECT` that always reflects the *current*
contract body at read time — a schema decision equivalent to "always recompute,
never cache." The cost (per-query JSON1 parse of `task_contracts.body`) is
paid by a metric extractor that runs once per experiment, not by a hot path.
See §5 for the view definition; see §7 (e) for the invariant "view MUST be
read-only" that the DDL enforces via SQLite view semantics.

### 1.3 Why `Recorder` is synchronous and unbuffered

The routing-trace writer (WT-1 C2 §6 a) chose synchronous + secretscrub-at-emit
because dropping a trace row silently defeats the trace. Same argument here:
if the audit is buffered and the process crashes, a hostile task's evidence
disappears. The write is one INSERT to a local SQLite file (median <200 µs on
the WT-1 fixtures per `capability_snapshots_writer_test.go` bench). The
recorder returns `error` — but the caller is expected to log-and-continue,
never to short-circuit business logic (§7 (a)); a failed audit-write must
emit `journal: audit write dropped: <err>` at WARN and bump the
`audit_write_dropped_total` expvar counter, then return control to the caller.

## 2. Data structures

All public types live in `internal/journal/audit.go`. The file is one package
with `journal.go`; if the aggregate file grows past ~400 LOC we split into
`audit_event.go` / `audit_verifier.go` in a follow-up, not this WT.

### 2.1 `AuditEvent`

```go
type AuditKind string

const (
    KindRead      AuditKind = "read"
    KindWrite     AuditKind = "write"
    KindToolCall  AuditKind = "tool_call"
    KindModelCall AuditKind = "model_call"
)

// AuditEvent is one observation of a runtime effect. Field set is fixed at
// spec time; adding a field is a wire-compat break because contract_violations
// selects by column name.
type AuditEvent struct {
    // ConversationID pins the event to the TaskContract in force. Empty is
    // rejected by Recorder.Record — a runtime effect that cannot be tied
    // to a contract cannot participate in the diff.
    ConversationID string

    // RunID is the D1 run_id (runs.run_id). Empty is permitted so the
    // recorder still works during unit tests that don't spin up the eval
    // runner; the view LEFT JOINs runs, so an empty RunID surfaces as
    // NULL in contract_violations.run_id (§5).
    RunID string

    // Kind is one of the four constants above. Any other value is rejected.
    Kind AuditKind

    // Target is:
    //   - for KindRead / KindWrite: an ABSOLUTE filesystem path (rooted or
    //     jail-resolved by the caller). May contain sensitive substrings;
    //     ALWAYS re-scrubbed by Recorder.Record before insert (§7 (b)).
    //   - for KindToolCall: the tool name (bare, e.g. "register_slave_mcp"
    //     or "mcp:my-server:list_files"); does not include arguments.
    //   - for KindModelCall: the model id (e.g. "glm-5.2"); no prompt.
    // Length capped at 4 KiB post-scrub; anything longer is a
    // contract-violation of the audit contract itself and returns
    // ErrTargetTooLong (rejects the event; §7 (b) rationale).
    Target string

    // Ts is a monotonic-source wall-clock time (time.Now, not stubbed).
    // Serialised via evalrun's fixed-width UTC RFC3339Nano encoder so
    // lexicographic sort matches chronological order (see §5 note on
    // ORDER BY in the view).
    Ts time.Time

    // SizeBytes is meaningful only for KindWrite; must be 0 for the other
    // three kinds. Non-zero on non-write kinds is rejected.
    SizeBytes int64

    // Hash is meaningful only for KindWrite. When non-empty it MUST match
    // ^[a-f0-9]{64}$ (see §7 (d) — the same regex evalrun uses). Empty
    // Hash on a KindWrite is permitted (e.g. streaming write whose final
    // hash is not yet known); such writes DO NOT contribute to the run's
    // artifact_hashes at ArtifactHashAppender.Append time.
    Hash string
}
```

### 2.2 `Recorder`

```go
// Recorder persists an AuditEvent to the audit_events table. Implementations
// MUST be safe for concurrent use across goroutines (executor and driver may
// both instrument the same run).
type Recorder interface {
    Record(ctx context.Context, ev AuditEvent) error
}

// NopRecorder swallows every event and returns nil. Used as the zero-value
// default so a slave-agent main that has not yet wired an SQLite recorder
// does not have to guard every Record call with a nil check.
type NopRecorder struct{}

func (NopRecorder) Record(context.Context, AuditEvent) error { return nil }

// NewSQLRecorder returns a Recorder backed by *sql.DB. It shares the
// audit_events writer with contract_violations_view.go (WriteAuditEvent).
// A nil db returns NopRecorder — makes the "observer DB not yet mounted"
// path a no-op instead of a construction error.
func NewSQLRecorder(db *sql.DB) Recorder { ... }
```

Behavioural contract:

1. Validate `ev` (see §7 (d), §7 (b) length cap, kind-vs-size/hash invariants).
   On invalid: return the wrapped sentinel; DO NOT insert.
2. Scrub `ev.Target` through `secretscrub.Sanitize` **and** the local
   `sensitivePathRe` extension (§7 (b)). If the scrub replaced any substring,
   bump `audit_target_redacted_total` (expvar).
3. INSERT via a parameterised SQL statement in `contract_violations_view.go`
   (`WriteAuditEvent`). No other writer is permitted to touch `audit_events`
   (§7 (f)).
4. On insert error: return the error; caller is expected to log-and-continue
   (§7 (a)) and bump `audit_write_dropped_total`. The recorder does NOT log
   itself — logging is the caller's decision so a test can assert on
   behaviour without stderr noise.

### 2.3 `Violation` and `Verifier`

```go
type ViolationKind string

const (
    ViolationUndeclaredRead      ViolationKind = "undeclared_read"
    ViolationUndeclaredWrite     ViolationKind = "undeclared_write"
    ViolationUndeclaredToolCall  ViolationKind = "undeclared_tool_call"
    ViolationUndeclaredModelCall ViolationKind = "undeclared_model_call"
)

// Violation is one row of the actual-vs-contract diff. Emitted by
// Verifier.Verify AND surfaced by the contract_violations view — the two
// projections MUST agree on column names (see §5).
type Violation struct {
    ConversationID string
    RunID          string        // may be "" — see AuditEvent.RunID
    Kind           ViolationKind
    Target         string        // already-scrubbed, from the event
    Expected       string        // JSON-encoded string of the declared set
                                 // that would have permitted this action;
                                 // "" if the contract declared nothing of
                                 // that kind. Bounded at 4 KiB (§7 (b)).
    Observed       string        // ev.Target (post-scrub)
    Ts             time.Time
}

// Verifier is a pure function. No I/O, no globals read, no globals written.
// Given a contract and a slice of events tied to the same conversation, it
// returns the violation list in event-ts ascending order.
type Verifier interface {
    Verify(c contract.TaskContract, events []AuditEvent) []Violation
}

func NewVerifier() Verifier { ... }
```

Rules for the diff (each rule is one negative test in the plan):

| Event kind    | Declared set (from contract)                                                        | Undeclared → violation |
|---------------|-------------------------------------------------------------------------------------|------------------------|
| `read`        | `c.DataContract.ReadArtifacts[].Name` ∪ `c.DataContract.ReadArtifacts[].ArtifactID` | Target ∉ declared set  |
| `write`       | `c.DataContract.WriteTargets[].Name`                                                | Target ∉ declared set  |
| `tool_call`   | `c.CapabilityRequirements.Tools[]`                                                  | Target ∉ declared set  |
| `model_call`  | `c.CapabilityRequirements.Skills[]` (skill names carry model requirement)           | Target ∉ declared set  |

Matching is **exact string** on the Target vs each declared entry — no path
normalisation, no glob, no case folding. Rationale: (a) the contract entries
are already expected to be canonical (Phase 1 A2 validator rejects non-canonical
paths); (b) any relaxation is an attack surface — an attacker who can persuade
the contract to declare `./secrets` and then read `secrets` benefits from a
normaliser. If a caller has semantic-normalisation needs it lives in the
extractor, not the verifier.

For `read`: the declared set is the **UNION** of `Name` and `ArtifactID`
across every `ReadArtifacts[]` entry. Either handle is a legitimate
canonical reference to the same artifact (see `contract.ArtifactRef` in
`internal/contract/types.go:58`) — a contract that lists an artifact only
by `artifact_id` MUST NOT trigger `undeclared_read` when the audit event
reports the same `artifact_id` as Target, and symmetrically for
`Name`-only entries. Entries where BOTH `Name` and `ArtifactID` are set
contribute BOTH strings to the union. Entries where NEITHER is set
contribute nothing (there is no other handle to match on) — such an
entry is a contract-authoring bug caught by the Phase-1 A2 validator,
not by this WT. Verify's matching rule is the Go equivalent of the SQL
`.name / .artifact_id` union in §5's view.

`Verify` returns an empty slice (not nil) when no violations. It never
allocates for the happy path.

### 2.4 `ArtifactHashAppender` (D1 stub — §5.1 of the prompt)

```go
// ArtifactHashAppender is the seam through which the audit layer feeds the
// D1 runs.artifact_hashes column. As of 2026-07-01, internal/evalrun.Writer
// exposes only whole-row Insert; there is no AppendArtifactHashes yet. This
// interface is the stub; the noop impl is the default; the real wiring
// lands in a Phase-2 follow-up (candidates: WT-2-metric-extract or a
// dedicated D1-completion WT) whose owner MUST match the signature below
// and replace journal.NopArtifactHashAppender in the slave-agent main.
type ArtifactHashAppender interface {
    // Append idempotently unions hashes into the artifact_hashes JSON
    // array for run_id. Every implementation (including the Nop) MUST
    // reject any hash that does not match ^[a-f0-9]{64}$ with
    // ErrInvalidArtifactHash before doing any other work (§7 (d)). Sort
    // order in the persisted array MUST be sha256-hex lexicographic
    // ascending; duplicates MUST be de-duped.
    Append(ctx context.Context, runID string, hashes []string) error
}

// NopArtifactHashAppender is a no-op appender used as the default when the
// slave-agent main has not yet been wired to a real appender. It STILL
// validates every hash against ^[a-f0-9]{64}$ before returning nil so the
// §7 (d) invariant "no non-hex hash ever reaches an Append call successfully"
// holds even in the noop path — a caller that would silently accept a
// bad-hex hash today would silently accept it after wiring too, and that
// class of bug MUST fail at the stub boundary.
type NopArtifactHashAppender struct{}

func (NopArtifactHashAppender) Append(_ context.Context, _ string, hashes []string) error {
    for i, h := range hashes {
        if !sha256HexRe.MatchString(h) {
            return fmt.Errorf("journal: artifact_hashes[%d]=%q: %w", i, h, ErrInvalidArtifactHash)
        }
    }
    return nil
}
```

The audit layer calls `Append` **only after** `Verify` returned an empty
violation list for that run's events, and only with `Kind == KindWrite &&
Hash != ""` hashes. Rationale: a contract-violating write is not a
contract-satisfying artifact; letting it into `artifact_hashes` would
contaminate the D1 row's downstream oracle comparison (§F1).

## 3. Instrumentation points (documented; wired by follow-up WT)

**Scope note:** these hook-site addresses are DESIGN DOCUMENTATION for the
follow-up WT that adds the instrumentation lines. The current WT ships only
the core types + writer + view listed at the top of this spec. The follow-up
adopts the "append-line only" contract below when it lands.

**Constraint (for the follow-up WT):** no signature change. Each site adds
exactly one call site of the form

```go
if err := r.Record(ctx, journal.AuditEvent{...}); err != nil {
    log.Printf("journal: audit write dropped: %v", err)
    journal.AuditWriteDroppedTotal().Add(1)
}
```

so `Recorder.Record` errors are LOGGED and COUNTED but never block business
logic (§7 (a)). The `Recorder` handle threads in through an existing config
struct on each executor (see §3.5) — not via a new function argument.

The exact insertion sites, with the current line numbers on
`paper/v3-integration` HEAD `d053897`:

### 3.1 `internal/executor/file.go` (FileExecutor)

- `doRead` (currently ends at file.go:270). After the successful `sink.Write`
  emit and before `return Result{...}, nil`, emit
  `AuditEvent{Kind: KindRead, Target: abs, Ts: time.Now(), ...}`.
- `doWrite` (currently ends at file.go:357). After `sink.Write` and before
  `return`, emit
  `AuditEvent{Kind: KindWrite, Target: abs, SizeBytes: int64(n), Hash: sha256Hex(bytesPayload), Ts: time.Now(), ...}`.
  Hash MUST be computed synchronously against the actual bytes that hit the
  filesystem (`bytesPayload` is already in-hand); the audit event MUST NOT
  compute a hash on a partial write that failed (there is a `return` on
  write error immediately preceding — the emit is placed *after* the error
  return so a failed write does not surface a hash).

### 3.2 `internal/executor/bash.go` (BashExecutor)

- After `cmd.Run()` and after the `body, marshalErr := json.Marshal(result)`
  block (currently around bash.go:105–108), before the `sink.Write` call:
  the bash script is a *tool_call* on the "bash" tool (matches
  `capability_requirements.tools` entries populated by dispatch). Emit
  `AuditEvent{Kind: KindToolCall, Target: "bash", Ts: time.Now(), ...}`.
  No SizeBytes / no Hash.

### 3.3 `internal/executor/mcp.go` (MCPExecutor.Run)

- Around mcp.go:82 (start of Run). After we know we have a valid MCP request
  and before we dispatch to the transport: emit
  `AuditEvent{Kind: KindToolCall, Target: "mcp:" + serverName + ":" + toolName, Ts: time.Now(), ...}`.
  This gives the contract-vs-actual diff visibility into MCP-hosted tool
  invocations (which are the largest class of undeclared-tool violations).

### 3.4 `cmd/slave-agent/main.go` — `backendExecutor.Run` (currently line 474)

- The Run bridge to `agentbackend.Backend.Run` is where the actual LLM call
  happens (the codex / claude / opencode backends). After the backend returns
  its `Result` and before `backendExecutor.Run` returns: emit
  `AuditEvent{Kind: KindModelCall, Target: <model_id>, Ts: time.Now(), ...}`.
- **Model-id sourcing.** As of 2026-07-01 `pkg/agentbackend.Backend` (see
  `backend.go:35`) exposes `Kind()` / `Run` / `RunResume` / `LLM()` /
  `Permissions()` / `Detect()` / `ListSessions()`; there is no
  `ModelName()` method, and this WT does NOT add one (that would widen the
  file-domain contract to `pkg/agentbackend/*`). `agentbackend.Config`
  (`pkg/agentbackend/config.go:12`) exposes `Kind` / `Bin` / `WorkDir` /
  `ExtraArgs` / `WorkerMode` / `CodexHome` — no `Model` field either.
  The model id therefore MUST be recovered from **backend-native config
  sources** at wiring time, by the follow-up WT, using per-backend
  logic:
  1. Codex — parse the `model = "…"` line from
     `<CodexHome>/config.toml` (see
     `pkg/agentbackend/codex/permissions_test.go:100` for the exact
     literal) at backend construction time; stash the parsed string on
     the local `backendExecutor` struct (a per-instance string field;
     not a new method on `Backend`).
  2. Claude / opencode — parse the `--model` argument out of
     `agentbackend.Config.ExtraArgs`; when absent, backend default
     applies (opencode: whatever the CLI negotiates; claude: whatever
     the CLI default is). The wiring WT captures the negotiated value
     by reading the backend's stdout kind marker once available; if
     that path is not usable at construction time, Target is `""`.
  3. If the follow-up WT cannot recover a model id by any of the above
     means, Target is `""`. `Verifier.Verify` sees the empty Target as
     "target not in declared set" (Skills is a slice of non-empty
     strings) and reports `undeclared_model_call`. **Fail-closed**, on
     purpose: an unaudited model call must not silently escape the
     diff.
  4. Once `pkg/agentbackend.Backend` grows a first-class `ModelName()`
     accessor (deferred to the pkg-agentbackend evolution WT), the
     wiring WT switches to that call. That change is a wire-compat
     *extension* of `Backend`, not a signature change to any existing
     method.

### 3.5 Wiring the Recorder handle

Each executor struct gains an **optional** field (not a constructor arg — nil
default keeps every existing test compiling):

- `BashExecutor` — `Auditor journal.Recorder` added to `BashConfig`; nil
  ⇒ NopRecorder.
- `FileExecutor` — same on `FileConfig`.
- `MCPExecutor` — new field on the struct; new `SetAuditor(journal.Recorder)`
  method with the same nil-default semantics.
- `backendExecutor` in `cmd/slave-agent/main.go` — new field on the struct;
  populated in the main-agent wiring code that already builds the executor
  (existing site; grep-verifiable at slave-agent/main.go:474 vicinity).

The slave-agent main wiring passes the SAME `Recorder` instance to all four
executors so `Verifier.Verify` sees the union of one run's events. This does
not change any exported signature — the config-struct extension is a Go-source
compat change (all existing callers use `BashConfig{...}` positional-by-name,
so a new field is additive).

## 4. DDL (appended to `internal/observerstore/schema.sql`)

Append at the tail (after the `capability_snapshot_usages` block, which is
today's last entry per schema.sql:250). WT-2-dry-run-validator appends its
own `dry_run_blocks` table — merge order does not matter because the two
appends do not share a table.

```sql
-- WT-2-runtime-audit: per-execution runtime audit events for the
-- ContractViolationRate metric (12号 §A4). Writer: NewSQLRecorder /
-- WriteAuditEvent in internal/journal + internal/observerstore/
-- contract_violations_view.go. No other writer path is permitted.
CREATE TABLE IF NOT EXISTS audit_events (
    event_id        TEXT PRIMARY KEY,
    workspace_id    TEXT NOT NULL DEFAULT '',
    conversation_id TEXT NOT NULL,
    run_id          TEXT NOT NULL DEFAULT '',
    kind            TEXT NOT NULL CHECK(kind IN
                        ('read','write','tool_call','model_call')),
    target          TEXT NOT NULL,
    size_bytes      INTEGER NOT NULL DEFAULT 0,
    hash            TEXT NOT NULL DEFAULT '',
    ts              TEXT NOT NULL     -- fixed-width UTC RFC3339Nano
);
CREATE INDEX IF NOT EXISTS idx_audit_events_conv
    ON audit_events(workspace_id, conversation_id, ts);
CREATE INDEX IF NOT EXISTS idx_audit_events_run
    ON audit_events(run_id, ts);

-- WT-2-runtime-audit: contract_violations is a read-only VIEW over
-- audit_events joined against the in-force task_contracts row for the
-- conversation. SQLite treats a plain CREATE VIEW as read-only unless a
-- trigger promotes it — no trigger is defined here, and §7 (e) forbids
-- one in this package. Adding INSTEAD OF INSERT/UPDATE/DELETE triggers
-- against this view is a schema regression the plan's negative test
-- explicitly catches.
CREATE VIEW IF NOT EXISTS contract_violations AS
WITH
    -- task_contracts is keyed by (workspace_id, task_id) — not by
    -- (workspace_id, conversation_id) (schema.sql:166). One conversation
    -- can therefore have multiple task rows (retries, dispatch-per-turn,
    -- follow-ups). The "in-force" contract set for an audit event is the
    -- UNION of every task-row for that (workspace_id, conversation_id)
    -- pair whose updated_at is greater than or equal to the maximum
    -- updated_at across the pair minus a zero-tolerance tie window (i.e.
    -- rows whose updated_at is EXACTLY the max). Multiple rows are
    -- expected here: a caller who rewrote the contract at the same
    -- RFC3339Nano instant would legitimately have two matching rows;
    -- the diff semantics below treat the target as declared if ANY
    -- tied row declares it.
    all_conv_contracts AS (
        SELECT tc.workspace_id, tc.conversation_id, tc.body, tc.updated_at
        FROM task_contracts tc
        WHERE tc.updated_at = (
            SELECT MAX(tc2.updated_at)
            FROM task_contracts tc2
            WHERE tc2.workspace_id    = tc.workspace_id
              AND tc2.conversation_id = tc.conversation_id
        )
    )
SELECT
    ae.run_id                                                       AS run_id,
    ae.conversation_id                                              AS conversation_id,
    CASE ae.kind
         WHEN 'read'       THEN 'undeclared_read'
         WHEN 'write'      THEN 'undeclared_write'
         WHEN 'tool_call'  THEN 'undeclared_tool_call'
         WHEN 'model_call' THEN 'undeclared_model_call'
    END                                                             AS violation_kind,
    ae.target                                                       AS target,
    -- Expected: a JSON1 array UNION over every tied contract's declared
    -- set for the event's kind. Rendered via json_group_array over the
    -- unrolled elements so a consumer sees ONE combined declared set,
    -- not N separate ones. NULL (surfaces as SQL NULL) when no tied
    -- contract exists at all.
    (
        SELECT json_group_array(declared.value)
        FROM all_conv_contracts tc
        JOIN json_each(
            CASE ae.kind
                 WHEN 'read'       THEN json_extract(tc.body, '$.data_contract.read_artifacts')
                 WHEN 'write'      THEN json_extract(tc.body, '$.data_contract.write_targets')
                 WHEN 'tool_call'  THEN json_extract(tc.body, '$.capability_requirements.tools')
                 WHEN 'model_call' THEN json_extract(tc.body, '$.capability_requirements.skills')
            END
        ) AS declared
        WHERE tc.workspace_id    = ae.workspace_id
          AND tc.conversation_id = ae.conversation_id
    )                                                               AS expected,
    ae.target                                                       AS observed,
    ae.ts                                                           AS ts
FROM audit_events AS ae
WHERE
    -- No contract in force at all ⇒ every actual is undeclared
    -- (fail-closed).
    NOT EXISTS (
        SELECT 1 FROM all_conv_contracts tc
        WHERE tc.workspace_id    = ae.workspace_id
          AND tc.conversation_id = ae.conversation_id
    )
    -- Or: NO tied contract declares this target for this kind. The
    -- outer NOT EXISTS is over the CROSS PRODUCT of (tied contracts) x
    -- (that contract's declared elements for this kind); a single hit
    -- anywhere in the product defeats the violation, which is the
    -- correct "any tied contract declares it ⇒ not undeclared"
    -- semantics.
    OR NOT EXISTS (
        SELECT 1
        FROM all_conv_contracts tc
        JOIN json_each(
            CASE ae.kind
                 WHEN 'read'       THEN json_extract(tc.body, '$.data_contract.read_artifacts')
                 WHEN 'write'      THEN json_extract(tc.body, '$.data_contract.write_targets')
                 WHEN 'tool_call'  THEN json_extract(tc.body, '$.capability_requirements.tools')
                 WHEN 'model_call' THEN json_extract(tc.body, '$.capability_requirements.skills')
            END
        ) AS declared
        WHERE tc.workspace_id    = ae.workspace_id
          AND tc.conversation_id = ae.conversation_id
          AND (
              -- For read_artifacts the JSON array items are OBJECTS
              -- with BOTH `.name` and `.artifact_id` (see
              -- contract.ArtifactRef in internal/contract/types.go:58).
              -- For write_targets the objects carry `.name`. For
              -- tools/skills the items are bare strings — SQLite
              -- json_extract on a non-JSON scalar raises `malformed
              -- JSON`, so the two object-path branches are gated on
              -- `declared.type = 'object'` (a `json_each` output
              -- column that discriminates object / array / text /
              -- integer / null; the string cases hit the plain
              -- `declared.value = ae.target` arm above).
              declared.value = ae.target
              OR (declared.type = 'object' AND json_extract(declared.value, '$.name')        = ae.target)
              OR (declared.type = 'object' AND json_extract(declared.value, '$.artifact_id') = ae.target)
          )
    )
ORDER BY ae.ts ASC;
```

Notes:

- The `all_conv_contracts` CTE surfaces the *set* of tied task_contracts
  rows for each `(workspace_id, conversation_id)` — the real PK is
  `(workspace_id, task_id)` (schema.sql:166), so more than one row per
  pair is possible whenever a caller retries or dispatches multiple times
  in one conversation. The read-time "in-force" contract set is the
  rows whose `updated_at` matches the pair's MAX.
- The WHERE has two branches:
  1. `NOT EXISTS (SELECT 1 FROM all_conv_contracts ...)` — the pair has
     no contract row at all ⇒ every audit event is undeclared. This
     replaces the earlier `LEFT JOIN … WHERE tc.body IS NULL` idiom;
     the NOT EXISTS form does not multiply rows when the join surface
     is > 1.
  2. `NOT EXISTS (SELECT 1 FROM all_conv_contracts JOIN json_each …)` —
     over the cross-product of tied contracts and their declared
     entries; a single hit anywhere defeats the violation. This
     implements "any tied contract declares target ⇒ not undeclared"
     without duplicating the outer event row across ties.
- `expected` is a correlated JSON1 `json_group_array` over the union
  of tied contracts' declared entries for the event's kind, so a
  consumer sees ONE combined declared set.
- The outer SELECT emits exactly one row per `audit_events` row (no
  ties can multiply the count), which matches the metric extractor's
  expectation (test 41 in the plan).
- Both `tools` and `skills` are consulted for `model_call` in a follow-up
  when §A2 grows a first-class `models` field on `CapabilityRequirements`;
  today Skills carries the model requirement (see §2.3 table row 4).
- `ORDER BY ae.ts ASC` in the view relies on the fixed-width RFC3339Nano
  encoder (§2.1) — a truncated form would sort wrong (see WT-1-run-schema
  §formatRFC3339NanoUTC comment for the failure mode).

## 5. `contract_violations_view.go` — writer + query helper

New file `internal/observerstore/contract_violations_view.go`. Contents:

- `type AuditEventRow struct { ... }` mirroring §2.1 field-for-field but
  storage-typed (string time is the wire form).
- `AuditWriter` interface + `NewAuditWriter(*sql.DB) AuditWriter`; single
  method `WriteAuditEvent(ctx, AuditEventRow) error`. Parameterised
  INSERT; no other SQL. Style matches `route_reasons_writer.go`.
- `type ContractViolationRow struct { RunID, ConversationID, ViolationKind, Target, Expected, Observed, TS string }`.
- `type ViolationsQuery interface { ByRunID(ctx, runID string) ([]ContractViolationRow, error) }` +
  a `SELECT * FROM contract_violations WHERE run_id = ?` implementation.
  Exported so §D2 metric-extract can SELECT without duplicating SQL.
- **No INSERT, UPDATE, or DELETE against the view.** Grep-enforced by
  the plan's static test (§7 (e)).

## 5.1 D1 wiring fallback

`internal/evalrun.Writer` today exposes only `Insert(ctx, Schema) error`. There
is no `AppendArtifactHashes` yet, so:

- This WT ships `NopArtifactHashAppender` inside `internal/journal/audit.go`
  as the default appender.
- A `// TODO(WT-2-metric-extract or D1-completion): replace NopArtifactHashAppender
  with an SQLWriter-backed appender that updates runs.artifact_hashes for
  run_id. Signature MUST be Append(ctx, runID, hashes []string) error.`
  comment sits at the `ArtifactHashAppender` interface definition inside
  `audit.go` — the ONLY file in this WT that mentions the follow-up wiring
  seam. The follow-up WT (candidate: WT-2-audit-wiring or WT-2-metric-extract)
  owns any change to `cmd/slave-agent/main.go`, `internal/evalrun/writer.go`,
  or `pkg/agentbackend/*` — this WT MUST NOT modify those files, MUST NOT
  add TODO comments there, and MUST NOT add anchor test cases that grep
  them. The follow-up WT is expected to add
  `func (w *SQLWriter) AppendArtifactHashes(ctx, runID string, hashes []string) error`
  to `internal/evalrun/writer.go` and swap the default appender at its own
  wiring site.

Rationale for the stub over a direct write path: the alternative (this WT
adds `AppendArtifactHashes` to `evalrun.Writer`) crosses the file-domain
boundary declared at the top of this spec, would race against the D1
completion worktree if it also touches `writer.go`, and would let this WT
own two unrelated schema surfaces (audit + runs.artifact_hashes writer),
each of which then can't be reviewed independently.

## 6. Acceptance

An E2E scenario in `audit_test.go`:

1. Seed an in-memory SQLite with `observerstore/schema.sql` applied.
2. Insert one `task_contracts` row declaring read=`/tmp/a`, write=`/tmp/b`,
   tools=`[bash]`, skills=`[chat]` for a fixed conversation_id.
3. Instantiate `NewSQLRecorder(db)` and record five events for that
   conversation: three matching the contract, two violating it (an
   undeclared read of `/etc/passwd` and an undeclared tool call to
   `mcp:evil:exfil`).
4. Assert `ViolationsQuery.ByRunID(ctx, runID)` returns exactly two rows,
   with `violation_kind` values `undeclared_read` and
   `undeclared_tool_call`, and `target` values matching the injected
   events.
5. Instantiate a real `ArtifactHashAppender` fake capturing calls; after
   `Verifier.Verify` sees the two violations, assert the appender was
   **not** called (contract-violating writes never contribute to
   artifact_hashes — §2.4). Repeat with a clean run and assert the appender
   received sorted, deduped sha256-hex strings that also fit the
   `^[a-f0-9]{64}$` regex.

## 7. Security (P0)

### (a) Recorder write failures MUST log-and-continue, not silent-drop, not block business logic

Consequence table (each row is one negative test in the plan):

| Bad behaviour                       | Attack it enables / harm caused                                    |
|-------------------------------------|--------------------------------------------------------------------|
| Silent drop (no log, no counter)    | Attacker DoS's the observer DB → audit disappears silently        |
| Block business logic on write error | Attacker DoS's the observer DB → workload halts (availability DoS) |
| panic()                             | Same as above; also crashes the slave process                     |

Required behaviour:

- `Recorder.Record` returns the wrapped error; the caller (each executor
  hook site listed in §3) MUST wrap the call as
  `if err := r.Record(...); err != nil { log.Printf("journal: audit write dropped: %v", err); journal.AuditWriteDroppedTotal().Add(1) }`.
- `journal.AuditWriteDroppedTotal()` is a package-level accessor that
  returns the `*expvar.Int` published under the name
  `audit_write_dropped_total`. It is exported as a function (not a bare
  variable) so external test code can assert on the value without
  reaching into a package-private symbol, and so the counter identity
  is stable across the follow-up wiring WT. Grep for
  `AuditWriteDroppedTotal` at review time to catch missed hook sites.
- Business logic MUST proceed regardless of the audit outcome.

### (b) Target scrub — sensitive-path pattern set + secretscrub union

Threat model: an attacker (or an over-eager LLM) causes an executor to `read`
or `write` a path that IS the secret (e.g. `/home/alice/.aws/credentials`),
or contains a token in a filename (e.g. `/tmp/sk-ant-abcdefg/whatever`). The
raw path would land in `audit_events.target`, become part of the observer DB,
and reach anyone with SQL SELECT permission.

Mitigation (implemented in `audit.go::scrubTarget(s string) (string, bool)`):

1. Run `secretscrub.Sanitize(s)` first (already redacts sk-… / eyJ… /
   AKIA… / gh?_… / github_pat_… / glpat-… / AIza… / xox?-… / PEM
   headers — see scrub.go:45).
2. Then apply the **sensitive-path pattern set** below to REDACT the
   path down to the top-level marker (so the fact that "some credential
   file was touched" is still evident, but the specific credential-file
   identity is not). The path-scrub extends `secretscrub` *by
   composition* in `audit.go`; we do NOT edit `secretscrub/scrub.go`
   in this WT (out of file domain), but the design intent is that a
   future refactor moves the pattern set into `secretscrub` proper.
3. If either pass changed the string, bump `audit_target_redacted_total`
   (new expvar name; distinct from the routing-trace counter so operators
   can tell audit-side from route-side).

The initial sensitive-path pattern set (regex, case-sensitive except where
noted; `\bHOME\b` = a home-directory prefix `/home/<user>` or `/root` or
`/Users/<user>` — implemented as one alternation, not a lookup):

- `(?:/home/[^/]+|/root|/Users/[^/]+)/\.aws(?:/[^\s]*)?`   → `<HOME>/.aws/[REDACTED]`
- `(?:/home/[^/]+|/root|/Users/[^/]+)/\.ssh(?:/[^\s]*)?`   → `<HOME>/.ssh/[REDACTED]`
- `(?:/home/[^/]+|/root|/Users/[^/]+)/\.gnupg(?:/[^\s]*)?` → `<HOME>/.gnupg/[REDACTED]`
- `(?:/home/[^/]+|/root|/Users/[^/]+)/\.config/gcloud(?:/[^\s]*)?` → `<HOME>/.config/gcloud/[REDACTED]`
- `(?:/home/[^/]+|/root|/Users/[^/]+)/\.docker/config\.json` → `<HOME>/.docker/config.json.[REDACTED]`
- `(?:/home/[^/]+|/root|/Users/[^/]+)/\.netrc` → `<HOME>/.netrc.[REDACTED]`
- `(?:/home/[^/]+|/root|/Users/[^/]+)/\.kube/config` → `<HOME>/.kube/config.[REDACTED]`
- `/etc/shadow(?:-)?` → `/etc/shadow.[REDACTED]`
- `/private/etc/shadow(?:-)?` → `/private/etc/shadow.[REDACTED]`

Enforcement:

- **Pre-scrub** 4 KiB cap on `AuditEvent.Target`. Anything longer →
  return `ErrTargetTooLong` (a positive length signal keeps the writer
  from becoming a DB filler primitive). The cap runs BEFORE scrub so a
  hostile 100-MiB target is rejected without paying the secretscrub
  regex sweep. **Note on the post-scrub effective ceiling:**
  `secretscrub.Sanitize` already truncates to 256 runes before
  returning, so audit_events.target values NEVER exceed 256 runes in
  practice — the 4-KiB cap is a defence-in-depth pre-scrub gate, not
  the operational limit.
- The scrub is **idempotent** — `scrubTarget(scrubTarget(x)) == scrubTarget(x)`
  (verified by a dedicated test; same posture as `secretscrub.Sanitize`).
- New expvar: `audit_target_redacted_total *expvar.Int`.

### (c) `Verifier.Verify` is a pure function

- No file I/O, no network, no globals read *or* written, no goroutines
  spawned. Testable in isolation. Signature is `Verify(contract.TaskContract,
  []AuditEvent) []Violation`.
- Fuzz target `FuzzVerify(f *testing.F)` in `audit_test.go`. Seeds: one
  synthetic contract; corpora vary `AuditEvent.Kind`, `Target`, `Hash`,
  `SizeBytes`. Runs 30 s (`-fuzztime=30s`) under CI-conditional gate (§7 (h)).
  Assertions: no panic; returned slice length ≤ len(events); every
  returned Violation's `Target` matches the corresponding event's
  post-scrub target.

### (d) `Hash` regex + rejection sentinel

- `Recorder.Record` rejects `AuditEvent{Kind: KindWrite, Hash: "not-hex"}`
  with `ErrInvalidArtifactHash` (same sentinel-value contract as
  `evalrun.ErrInvalidArtifactHash` — spelled the same, defined
  independently in this package to keep the import graph shallow).
- `ArtifactHashAppender.Append` implementers MUST reject any element
  that fails `^[a-f0-9]{64}$`; the audit layer will not silently drop.
- `Verifier.Verify` does NOT re-validate hash format — the invariant is
  established at Record time; Verify sees only in-DB events, and the
  reject-at-write property means an in-DB event with a bad hash is
  impossible unless somebody bypassed the writer (which §7 (f) forbids).

### (e) `contract_violations` view is read-only

- SQLite's plain `CREATE VIEW` has no INSERT/UPDATE/DELETE surface unless
  an `INSTEAD OF` trigger is attached. **This WT MUST NOT define any
  trigger on `contract_violations`, and the plan MUST include a static
  test that greps the DDL for `TRIGGER.*contract_violations` and fails
  on any hit.** The rationale is future-proofing: without the trigger
  ban, a Phase-3 refactor could add "convenience" trigger logic that
  quietly turns the view into a write path, subverting §7 (f).
- The static test also greps `contract_violations_view.go` for the
  strings `INSERT INTO contract_violations`, `UPDATE contract_violations`,
  `DELETE FROM contract_violations`.

### (f) All SQL is parameterised; no bypass of the audit writer

- `WriteAuditEvent` is the ONE and ONLY writer to `audit_events`. Grep-
  enforced (test greps the codebase for `INSERT INTO audit_events` and
  asserts the only file hit is `contract_violations_view.go`).
- Every SQL string in this WT is a Go `const` with `?` placeholders. No
  `fmt.Sprintf(..., userInput)` for SQL. No table names templated from
  runtime input.
- `ViolationsQuery.ByRunID` binds `runID` as a placeholder; the SELECT
  string is a compile-time const.

### (g) Consumer-view reverse audit

**Scenario:** I am the §D2 `ContractViolationRate` metric extractor. Can I
compute "violation rate per experiment" using only the schema this WT
adds?

- `SELECT COUNT(*) FROM contract_violations WHERE run_id = ?` gives the
  numerator per run.
- `SELECT run_id FROM runs WHERE experiment_id = ?` gives the run set
  for the denominator.
- Join surface: `contract_violations.run_id` ↔ `runs.run_id` — direct
  equality; already exposed by the view (§5).
- Bucket by kind: `SELECT violation_kind, COUNT(*) FROM contract_violations
  WHERE run_id IN (...) GROUP BY violation_kind` — no schema gap.
- Conversation-level attribution: `contract_violations.conversation_id`
  is exposed too (§5), so a per-conversation cut is available without a
  further join back to `task_contracts`.

**Verdict:** the view is sufficient for `ContractViolationRate` end-to-end.
No additional column needs to leak out.

### (h) CI-conditional performance assertions

- Any `> N ops/sec` / `< M ns` benchmark in `audit_test.go` MUST
  `t.Skip("perf assertion skipped in CI / short mode")` when
  `testing.Short() || os.Getenv("CI") != ""` is true. This is the
  Phase-2 convention that PR #59 introduced as a hotfix for WT-1-
  fault-injection; codifying it here up-front avoids repeating that
  hotfix.
- The fuzz target `FuzzVerify` is NOT a perf assertion — it runs 30 s
  under `-fuzztime=30s` but the test itself only asserts no-panic and
  no-invariant-break, both of which run in normal CI.

### (i) Design test for the follow-up wiring WT

- `audit_test.go` includes a static test `TestWiringContract_HookCallShape` that
  spells out — as a Go string constant — the required call-site shape:

  ```
  if err := r.Record(ctx, journal.AuditEvent{...}); err != nil {
      log.Printf("journal: audit write dropped: %v", err)
      journal.AuditWriteDroppedTotal().Add(1)
  }
  ```

- The test does not grep the executor sources today (they are still
  untouched by this WT). It exists so the follow-up WT can extend it
  with a `grep`-of-source assertion at wiring time, and so a reviewer
  can point at ONE place that names the wiring invariant. This is
  cheap insurance against a follow-up author who log-and-swallows the
  Recorder error while forgetting the counter bump.

## 8. Open questions

None P0; the following are deferred to Phase-2 follow-ups and are documented
here so a reviewer does not think they are missing scope:

- Real `ArtifactHashAppender` wiring: WT-2-metric-extract (or a dedicated
  D1-completion WT). Cross-referenced in §5.1.
- pg-native writer for `audit_events`: same follow-up posture as
  WT-1-routing-trace §6 (SQLite today, pg later).
- Path normalisation policy for the diff (§2.3): deferred — a change here
  is user-visible and needs its own design cycle.

## 9. Acceptance checklist

- [ ] `audit.go` + `audit_test.go` land, following the type layout of §2.
- [ ] `contract_violations_view.go` + `contract_violations_view_test.go` add
      the `audit_events` writer, the view query helper, and their tests.
- [ ] `schema.sql` is appended with the DDL of §4; the file is otherwise
      untouched (append at the tail; no reordering).
- [ ] `go test ./internal/journal/... ./internal/observerstore/... -count=1 -shuffle=on -race` is green.
- [ ] `go test ./internal/journal/... -fuzz=FuzzVerify -fuzztime=30s` runs 30 s
      with zero panics and zero invariant failures.
- [ ] `go vet ./...` is clean.
- [ ] `internal/executor/*`, `cmd/slave-agent/*`, `pkg/agentbackend/*` are
      NOT touched by this WT — wiring is deferred to the follow-up.
- [ ] All nine §7 security items ((a)–(i)) have a corresponding negative
      test in the plan and in `audit_test.go`.
- [ ] Commit trailer: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- [ ] NOT pushed.
