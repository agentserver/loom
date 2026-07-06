# WT-2-task-resume — Spec

> Source: `/root/paper_writing/docs/final/todo_list.md` Phase 2 table row
> **WT-2-task-resume**; deliverable defined in
> `/root/paper_writing/docs/intermediate/12_loom_development_tasks_for_v3.md`
> §A6.
> Branch: `paper/v3/p2-task-resume`.
> Base: `origin/paper/v3-integration` HEAD `d053897`.
> Depends on Phase 0 A5 `internal/driver/task_journal.go` (`e1ab112` +
> follow-ups on this base) and Phase 1 `internal/contract.TaskContract`
> (`d1659cb`).
> Downstream consumers: eval-runner §D4 `RecoverySuccessRate` /
> `DuplicateSideEffectRate` extractors.
>
> **Stage**: Stage 1 design spec of the three-stage workflow (Spec → Plan
> → Code). The Go files declared in §1 do NOT yet exist on disk — Stage
> 3 creates them. A reviewer evaluating this document is reviewing
> **design intent**: do the proposed types, signatures, DDL, and security
> mitigations form a coherent and complete contract? Whether the
> implementation files are present yet is out of scope for Stage 1
> review.

---

## 1. Task boundary & file scope

This worktree adds a **task-level generic resume path** on top of the
existing `TaskJournal` (§A5, already merged), plus a **`write_ids`
deduplication table** that lets driver-restart / slave-disconnect
recoveries replay events without emitting duplicate side effects.

The humanloop `ask_user` / `request_permission` pause/resume path
(`internal/humanloop/`) is orthogonal and untouched — that is a
model-loop pause tied to a single tool call, whereas this worktree
targets a whole `delegate_task` run (see §2.4).

Files this worktree owns (new or appended-to):

| Path | Action |
|---|---|
| `multi-agent/internal/driver/resume.go` | **NEW**. `ResumeTask(ctx, deps, runID, taskID)` entry point, task-id regex, `ReconstructSteps` helper (§6.2a driver-side payload recovery), contract loader dispatch, `RecordResumeAttempt` audit-emit. `ErrJournalChainBroken` alias re-exports the observerstore sentinel for `errors.Is`. |
| `multi-agent/internal/driver/resume_test.go` | **NEW**. Full §7 test matrix for driver-side behaviour (regex, journal-not-consulted, contract-missing error path, dispatch-fail error path, audit row transitions). |
| `multi-agent/internal/executor/resume.go` | **NEW**. `ExecutorResume(ctx, deps, req)` slave-side entry point, per-write `Reserve` gate, tri-state resume loop. Consumes `WriteID` / `WriteIDStore` from `observerstore`. |
| `multi-agent/internal/executor/resume_test.go` | **NEW**. Full §7 test matrix for executor-side behaviour (`Reserve` tri-state, content-change reject, duplicate-write prevention). |
| `multi-agent/internal/observerstore/schema.sql` | **APPEND ONLY**. Add `write_ids`, `write_id_reserve_events`, `resume_task_attempts`, `write_id_payloads`, `task_run_bindings` tables + indices at end of file. Do not touch any existing `CREATE TABLE` / `CREATE INDEX` statement. Postgres schema parity is out of scope; see §1.2. |
| `multi-agent/internal/observerstore/write_ids_writer.go` | **NEW**. Owns `WriteID` type + `NewWriteID` derivation + `ReserveRequest` / `CommitRequest` / `ReserveState` value types + `WriteIDStore` interface + `SQLiteWriteIDStore` implementation with `Reserve` (atomic `INSERT OR IGNORE`), `Commit`, `Vacuum`, `RecordResumeAttempt` methods, all parameterised. Also declares the `PayloadStager` interface and its default `SQLitePayloadStager` implementation over `write_id_payloads`, plus `ErrJournalChainBroken` (retained sentinel, not raised in this worktree) / `ErrPayloadUnavailable` (re-exported from `driver` and `executor` as aliases). Living here (not in `driver/`) is intentional — `driver` and `executor` already import `observerstore`, so a `driver.WriteID` would create an import cycle. |
| `multi-agent/internal/observerstore/write_ids_writer_test.go` | **NEW**. WriteID derivation (including content-hash validation), atomicity, idempotency, retention/vacuum, SQL-injection round-trip, audit-row + reservation atomicity, payload stager tests. |

Files this worktree **must not** modify:

- `multi-agent/internal/driver/task_journal.go` — read-only dependency.
  The A5 journal file is strictly untouched by this worktree AND
  `ResumeTask` does not read it either; every invariant this spec
  relies on is derived from the observer DB (`write_ids`,
  `task_contracts`, `write_id_payloads`, `resume_task_attempts`,
  `write_id_reserve_events`).
- `multi-agent/internal/humanloop/*` — the tool-call pause/resume path is
  outside this scope.
- `multi-agent/internal/contract/*` — `TaskContract` field set is stable
  from Phase 1 (`d1659cb`). We consume its `ConversationID` and
  `DataContract.WriteTargets`; we do not add fields.
- Any other table in `schema.sql`. Append-only.

`multi-agent/go.mod` is not modified: every new import
(`crypto/sha256`, `encoding/hex`, `encoding/json`, `regexp`, `context`,
`database/sql`, `errors`, `fmt`, `log`, `time`) is either standard
library or already a direct dependency of the target package.

### 1.1 Why `driver/resume.go` and `executor/resume.go` split

`driver/resume.go` handles the **decision** to resume: validate
`task_id`, LOAD the authoritative `TaskContract` from the observer
DB (`LoadContract`, §6.1b — no journal I/O), emit the pre-dispatch
audit row, and dispatch. `executor/resume.go` handles the
**replay** inside a slave: iterate the contract's `Step` values,
Load-or-Stage payload, Reserve, WriteStep, Commit. Splitting the
two mirrors the existing driver-decides-slave-executes shape of
`internal/driver/` vs `internal/executor/` and keeps the WriteID
gate close to the code that actually performs the write. Neither
side reads the journal file.

### 1.2 SQLite only; Postgres parity deferred

Same posture as WT-1-capability-snapshot §1.2: the SQLite observer
(`schema.sql`) is what the on-host eval-runner consumes today. The
Postgres schema (`postgres/schema.sql`) can add the same table when a
production-observer worktree needs it. `WriteIDStore` is defined
against `database/sql` with `?` placeholders only — the same
implementation compiles against `pgx.Stdlib` when the day comes.

### 1.3 Concrete imported Go-type anchors

| Symbol used by resume machinery | Declared at | Underlying type |
|---|---|---|
| `driver.TaskJournal` | `multi-agent/internal/driver/task_journal.go:134` | struct owning append-only JSONL journal |
| `driver.TaskRecord` | `multi-agent/internal/driver/task_journal.go:22` | typed row — not consumed by `ResumeTask`; retained here for reference because the reordered `contract_tools.go` still appends `TaskRecord` values for `list_driver_tasks` visibility |
| `contract.TaskContract` | `multi-agent/internal/contract/types.go:25` | contract body with `ConversationID` + `DataContract.WriteTargets` |
| `contract.WriteTarget` | `multi-agent/internal/contract/types.go:65` | `struct{ Type, Kind, Name string }` |
| `database/sql.DB` | stdlib | injected DB handle (real SQLite in prod, `t.TempDir()` SQLite in tests) |

Import direction: `driver` and `executor` both already import
`observerstore` (verified: `driver/register_mcp_tool.go:9`,
`driver/contract_tools.go:13`). Therefore all new resume value types
— `WriteID`, `ReserveRequest`, `CommitRequest`, `ReserveState`,
`WriteIDStore`, `PayloadStager` — live in `observerstore`. Placing them in
`driver/` would create an `observerstore → driver` back-edge and a
cycle. The `driver` and `executor` packages consume the observerstore
symbols via a normal import.

### 1.4 Scope of this worktree: primitives, not integration wiring

This spec defines the **resume primitives**:
`WriteID` / `WriteIDStore` / `PayloadStager` / `ExecutorResume` /
`ResumeTask` (skeleton). It does NOT modify existing driver /
executor code paths (e.g. `driver/contract_tools.go`,
`internal/executor/file.go`, `internal/executor/bash.go`) to
route through these primitives. That integration wiring is a
separate follow-up worktree (tentatively
`wt2-executor-writes-through-gate` and
`wt2-contract-tools-run-binding`).

Concretely, what this worktree delivers vs. what a downstream
integration worktree must add:

| Concern | This worktree | Downstream integration |
|---|---|---|
| WriteID derivation + Reserve/Commit/Vacuum + write_ids/write_id_reserve_events DDL | ✅ | — |
| PayloadStager interface + write_id_payloads DDL | ✅ | — |
| resume_task_attempts DDL + RecordResumeAttempt | ✅ | — |
| task_run_bindings DDL | ✅ | — |
| ResumeTask (validate + audit + LoadContract + Dispatch skeleton) | ✅ | — |
| ExecutorResume iterator (Stage → Reserve → WriteStep → Commit) | ✅ | — |
| Unit tests + fixture integration tests over fake WriteStep + fake filesystem | ✅ | — |
| Reorder `contract_tools.go` to persist contract-then-binding BEFORE journal append | ❌ | ✅ |
| Add `SaveTaskContractAndBindRun` to `ObserverRelay` + observerweb endpoint | ❌ | ✅ |
| Wire the driver's `--run-id` CLI flag and thread through `s.t.cfg.RunID` | ❌ | ✅ |
| Retrofit `internal/executor/file.go` / `bash.go` to invoke ExecutorResume | ❌ | ✅ |

**Why this split.** The primitives can be reviewed, tested, and
merged independently of the surrounding integration surgery.
Each integration change touches call-sites the resume primitives
depend on, but the primitives are complete when they satisfy the
§10 acceptance criteria against a fake `WriteStep` closure.

Recovery guarantees this worktree makes are therefore
**primitive-level only**:

- `ExecutorResume` invoked with a fake `WriteStep` closure and a
  fake `PayloadStager`/`WriteIDStore` backed by an in-memory
  SQLite drives the pipeline correctly for every §10 scenario.
- `ResumeTask` invoked with a fake `Dispatch` and fake
  `LoadContract` emits the correct `resume_task_attempts` rows
  for every entry/exit path.
- `SQLiteWriteIDStore.Reserve` / `Commit` / `Vacuum` /
  `RecordResumeAttempt` uphold the atomicity, lease, and audit
  contracts.
- `SQLitePayloadStager.Stage` / `Load` / `ListForStep` uphold the
  content-hash keying, idempotency, and committed-priority
  invariants.

This worktree does NOT claim:

- End-to-end recovery from a real driver crash. That guarantee
  requires the downstream `wt2-contract-tools-run-binding`
  worktree to reorder `contract_tools.go` and add
  `SaveTaskContractAndBindRun`.
- Prevention of duplicate writes on real slave first-run paths.
  That guarantee requires the downstream
  `wt2-executor-writes-through-gate` worktree to retrofit
  `internal/executor/file.go`, `bash.go`, etc. to invoke
  `ExecutorResume`.

The end-to-end story (driver restart → DB discovers running
task_ids via `task_run_bindings` → `ResumeTask` re-drives without
duplicate writes) requires all three worktrees merged. THIS
worktree lands the substrate; the end-to-end guarantee is
composed downstream.

### 1.5 Intentional divergence from todo_list wording

The §A6 row sketches "task journal + observer artifact ID + idempotent
write key串起来". This spec commits to the following concrete
mapping:

- **Task journal** = the existing `driver.TaskJournal` (untouched).
- **Observer artifact ID** = the existing `writes.id` column in
  `writes` (§schema.sql line 148), unchanged.
- **Idempotent write key** = the new `write_ids.id` column added in
  §5 of this spec.

We do NOT reuse `writes.id` as the idempotency key because `writes.id`
is a per-artifact identity (semantic: "this file object"), while the
WriteID is a per-attempt idempotency token (semantic: "this
conversation, this step, wrote this content"). Collapsing the two
would prevent legitimate re-writes of the same artifact from
succeeding after a genuine content change (see §7 (a)).

---

## 2. Motivating scenarios & non-goals

### 2.1 Driver-restart mid-`delegate_task`

Driver appends the `delegate_task` record to `TaskJournal`, dispatches
to a slave, then the driver process crashes before receiving the
`terminal` record. The slave may have already performed some or all of
its writes (i.e. side effects hit disk / hit the observer's `writes`
table).

On restart the eval-runner / operator CLI discovers resumable
`task_id`s via DB queries on `task_contracts` +
`resume_task_attempts` (§5.1), then calls `ResumeTask(ctx, deps,
runID, taskID)` for each. `ResumeTask` reads the authoritative
`TaskContract` body from the observer DB via `LoadContract`
(§6.1b), audits the attempt in `resume_task_attempts` (§5.3),
and dispatches to the slave which then runs `ExecutorResume`.
No journal I/O.

### 2.2 Slave disconnect mid-write

Slave is executing a `TaskContract` with N `WriteTargets`. Network
drops after write M. When the slave reconnects it receives a
`resume` command and re-enters the write loop. For each
`WriteTarget` it computes the `WriteID` and calls `Reserve` before
touching the filesystem / making the tool call. The tri-state
outcome (§4) drives what happens next:

- `ReserveFresh` → first observer of this id; perform the write, then
  Commit.
- `ReserveUncommitted` → a prior attempt reserved this id but never
  committed (crash between Reserve and Commit); re-issue the
  byte-identical write and Commit. Safety comes from the payload
  source in §6.2a plus the atomic-rename `WriteStep` contract.
- `ReserveCommitted` → the write already durably completed in a
  prior attempt; skip.

### 2.3 Duplicate-write prevention

Both restarts and disconnects converge on the same primitive:
`Reserve(id)` MUST be called before any observable side effect. The
observer's `write_ids` table is the single source of truth for "did
this exact (conv, step, path, content) tuple ever commit?". Anything
that skips `Reserve` and writes directly bypasses de-dup; §7(b) makes
this an atomic `INSERT OR IGNORE`.

### 2.4 Non-goals

- **Not** a replacement for the humanloop `ask_user` pause/resume path
  in `internal/humanloop/`. That is a model-loop pause tied to a single
  tool call.
- **Not** a snapshot of full mid-run in-memory state. The recovery
  granularity is per `WriteTarget` step.
- **Not** a distributed coordinator. Single observer DB, single driver
  process, one slave per task. Multi-driver election is out of scope.
- **Not** a schema migration for the existing `writes` table. That
  table's semantics are preserved; `write_ids` is additive.

---

## 3. `WriteID` type & derivation

Declared in `internal/observerstore/write_ids_writer.go` (see §1.3 for
the import-direction rationale).

```go
// WriteID is the idempotency key gated by WriteIDStore.Reserve.
// Its byte-value is derived from four inputs that together identify
// "one specific attempted side-effect within one task's contract".
type WriteID string

// contentHashPattern requires exactly 64 hex characters (SHA-256 hex
// encoding). This defends against a caller passing a placeholder like
// "placeholder" or "" that would collapse content changes into one
// WriteID and defeat §7(a). Compiled once at package init.
var contentHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// NewWriteID derives the id.
//   taskID         — matches ^[A-Za-z0-9_-]{8,128}$
//   conversationID — TaskContract.ConversationID (stable per task run)
//   stepID         — deterministic index/name within the contract
//   targetPath     — filesystem path or logical target (e.g. "artifact:foo")
//   contentHash    — LOWERCASE hex sha256 of the payload bytes about
//                    to be written. MUST match ^[0-9a-f]{64}$.
//
// Every input is required. Returns:
//   ("", ErrEmptyWriteIDComponent)   — any of the five inputs empty.
//   ("", ErrInvalidContentHash)      — contentHash not 64 lowercase hex.
//   ("", ErrInvalidTaskIDForm)       — taskID fails ^[A-Za-z0-9_-]{8,128}$.
//                                     (Distinct from driver.ErrInvalidTaskID;
//                                     lives in observerstore to avoid an
//                                     import cycle. driver re-exports as a
//                                     plain var alias so errors.Is works
//                                     from either side.)
//
// The contentHash validation is what makes §7(a) enforceable at the
// boundary: a caller cannot slip a placeholder past NewWriteID.
func NewWriteID(taskID, conversationID, stepID, targetPath, contentHash string) (WriteID, error)
```

Derivation (§7(a)):

```
raw = uvarint(len(taskID))         || taskID
   || uvarint(len(conversationID)) || conversationID
   || uvarint(len(stepID))         || stepID
   || uvarint(len(targetPath))     || targetPath
   || uvarint(len(contentHash))    || contentHash
id  = hex(sha256(raw))
```

`taskID` is included so that two DIFFERENT tasks that happen to
share the same `(conversationID, stepID, targetPath, contentHash)`
tuple (e.g. two parallel task delegations from the same
conversation that both write the same bytes to the same path)
produce distinct WriteIDs. Without `taskID` in the derivation, the
second task's Reserve would skip the write as a duplicate of the
first, silently corrupting the second task's audit trail. WT-2
mandates `TaskContract.ConversationID` uniqueness per task, but
that is a convention not enforced by the schema; deriving the
WriteID from BOTH values makes the safety property fail-safe.

Length prefixes (Go's `encoding/binary.PutUvarint`, LEB128 encoding
of `uint64`) make the derivation injective: `("aa","bb",…)` cannot
collide with `("aab","b",…)` even if a caller passes bytes
containing the separator we would otherwise have used. This is
strictly stronger than the `\x1e` sentinel design because
`targetPath` is caller-provided and could in principle contain any
byte value on filesystems that permit unusual filenames; length
prefixes take collision resistance out of the "what bytes can this
input contain" question entirely.

`contentHash` is the SHA-256 of the exact bytes the caller will
write, hex-encoded. `NewWriteID` validates it against
`^[0-9a-f]{64}$` (§3 code block below). The other three inputs are
validated non-empty; no other content restrictions apply because
the length-prefix encoding makes any byte sequence safe.

### 3.1 Why `content_hash` is required

Consider a task that legitimately overwrites the same output path
twice with different contents mid-run (e.g. iterative edits). Without
`content_hash` the WriteID collapses on the second write and the
resume path would `false`-skip it, leaving the earlier bytes on disk
while the agent believes the write succeeded. §7(a) is the
most-severe defect described in the prompt.

### 3.2 Rejected alternatives

- `(conv, step, path)` only — false-skips content changes (see §3.1).
- Random UUID per attempt — impossible to de-dup across a restart
  because the second attempt generates a new UUID.
- Hash of the payload only — collides across contracts that legitimately
  write the same file bytes into different tasks.

---

## 4. `WriteIDStore` interface

```go
// ReserveState is the tri-state returned by Reserve.
// It lets the caller distinguish "first attempt" from "prior attempt
// crashed after reserving but before committing" from "prior attempt
// already committed" from "prior attempt is still in-flight under a
// live lease" — four fundamentally different situations.
type ReserveState int

const (
    // ReserveFresh — no prior row for this id; caller MUST perform the
    // write and then call Commit. Reserve installs a lease owned by
    // req.WorkerID with lease_expires_at = now + req.LeaseTTL.
    ReserveFresh ReserveState = iota
    // ReserveUncommitted — a prior Reserve for this id succeeded but
    // no Commit ever landed AND the prior lease has expired (i.e.
    // now > lease_expires_at). Caller MUST retry the write (the
    // earlier attempt did NOT durably complete) and then Commit.
    // Reserve installs a NEW lease owned by the current req.WorkerID.
    ReserveUncommitted
    // ReserveInFlight — a prior Reserve for this id is still active
    // (its lease has NOT expired) and holds owner != req.WorkerID.
    // Caller MUST NOT write; a concurrent worker owns the attempt.
    // The caller should back off / retry after lease_expires_at OR
    // treat this as "someone else has it, my job is done".
    ReserveInFlight
    // ReserveCommitted — the write for this id already durably
    // completed in a prior attempt. Caller MUST skip.
    ReserveCommitted
)

// WriteIDStore mediates access to observer's write_ids table.
// Implementations are safe for concurrent use.
type WriteIDStore interface {
    // Reserve claims id if not already present, and reports the state
    // of any prior row.
    //
    // Return contract:
    //   (ReserveFresh,       nil) — atomic INSERT OR IGNORE inserted a
    //       new row. Caller MUST perform the write and then Commit.
    //   (ReserveUncommitted, nil) — a prior row exists with
    //       committed_at IS NULL. Caller MUST retry the write and then
    //       Commit. §7(a) content_hash guarantee makes this safe: the
    //       WriteID collides only when the payload bytes match, so
    //       "retry" is byte-identical to the earlier attempt.
    //   (ReserveCommitted,   nil) — a prior row exists with
    //       committed_at IS NOT NULL. Caller MUST skip.
    //   (_, err) — DB error; caller MUST abort the resume path.
    //
    // Implementation MUST be an atomic INSERT OR IGNORE followed by a
    // single SELECT of the row that ended up present (§7(b)); the two
    // statements run inside a single BeginTx transaction so no
    // concurrent Commit can slip between them. SELECT-then-INSERT is
    // forbidden — it has a race window between the check and the
    // insert that lets two concurrent resumes both conclude "id is
    // fresh" and both write.
    //
    // Reserve claims id if not already present, and reports the
    // state of any prior row. All state is DB-local; no filesystem
    // journal is consulted (§5.1).
    Reserve(ctx context.Context, req ReserveRequest) (ReserveState, error)

    // Commit marks a reserved id as durably written and appends the
    // corresponding audit row in the same transaction. Idempotent:
    // calling Commit twice with the same id updates zero rows on the
    // second call and does NOT emit a second audit row (the update's
    // RowsAffected drives the audit-row conditional). Called after
    // the side effect completes successfully.
    //
    // req.RunID / req.TaskID are required so the audit row can be
    // scoped to the current run (§7(g)).
    Commit(ctx context.Context, req CommitRequest) error

    // Vacuum drops committed rows older than cutoff AND orphaned
    // write_id_payloads rows (staged but never Reserved) older
    // than cutoff. Returns the number of write_ids rows removed.
    // Called by a periodic janitor; §7(f). Round-3 review added
    // the orphan-payload sweep.
    Vacuum(ctx context.Context, cutoff time.Time) (int64, error)

    // VacuumAudit drops append-only audit rows older than cutoff:
    // write_id_reserve_events.occurred_at < cutoff AND
    // resume_task_attempts.updated_at < cutoff. Returns
    // (reserveEventsDeleted, resumeAttemptsDeleted, err). Runs
    // inside a single transaction. Caller contract: the D4
    // evaluator MUST have materialised any aggregates
    // (DuplicateSideEffectRate / RecoverySuccessRate) it cares
    // about BEFORE calling this — the deletes are unconditional.
    // Eval-runner (D3) invokes this at run-teardown after writing
    // its CSV/JSONL; observer-server operators schedule it against
    // their own retention policy. Round-3 review P1 #3 added this
    // separate retention pass from Vacuum because prior rounds'
    // doc-only "grows unbounded" acknowledgement was inadequate
    // for prod deployments.
    VacuumAudit(ctx context.Context, cutoff time.Time) (int64, int64, error)

    // RecordResumeAttempt UPSERTs one row in resume_task_attempts
    // keyed by (run_id, task_id). outcome ∈ {"started", "replayed",
    // "error"}:
    //   - "started" INSERT (or UPDATE if a row already exists —
    //     re-invocation on the same (runID, taskID) is safe;
    //     re-invocation refreshes started_at only when outcome
    //     transitions FROM a terminal state back TO "started",
    //     which is expected on driver restart mid-resume).
    //   - "replayed" / "error" UPDATE (or INSERT if missing, in
    //     which case started_at is set to now).
    // errorKind is empty unless outcome=="error"; when non-empty
    // it names the sentinel (e.g. "ErrContractNotFound") for D4
    // failure-mode analysis. Called by driver.ResumeTask at every
    // step 2 (entry) and step 5 (terminal exit).
    RecordResumeAttempt(ctx context.Context, runID, taskID, outcome, errorKind string) error
}


// ReserveRequest bundles the columns Reserve writes to write_ids
// and write_id_reserve_events. Every field required; empty values
// return ErrEmptyReserveField.
type ReserveRequest struct {
    ID              WriteID       // §3 derivation
    RunID           string        // §D1 runs.run_id — enables (g) audit
    TaskID          string        // matches ^[A-Za-z0-9_-]{8,128}$
    ConversationID  string        // TaskContract.ConversationID
    StepID          string        // deterministic per-attempt identifier
    WorkerID        string        // identifies the executing process; must be non-empty
    LeaseTTL        time.Duration // duration for which this Reserve owns the id; typical 30s, minimum 1s, maximum 5min
}
```

Reserve is intentionally NOT a variadic-arg or option-fn API: every
field is required so a caller cannot accidentally omit `RunID` or
`WorkerID` and blind the D4 audit or the lease enforcement.

```go
// CommitRequest bundles the id + run scoping + lease owner Commit
// needs. Every field required; empty values return
// ErrEmptyReserveField.
type CommitRequest struct {
    ID       WriteID
    RunID    string
    TaskID   string
    WorkerID string // must match the lease_owner installed by Reserve
}
```

### 4.1 SQLite implementation

Reserve runs the following statements inside a single `BeginTx`
transaction:

```sql
-- 1. Attempt fresh reservation, installing a lease.
INSERT OR IGNORE INTO write_ids
    (id, task_id, conversation_id, step_id, reserved_at,
     lease_owner, lease_expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- 2. If rowcount was 0, try to steal an EXPIRED lease from a
--    prior dead worker. UPDATE only fires when committed_at IS
--    NULL AND lease_expires_at < now, so an actively-leased row
--    is untouched.
UPDATE write_ids
   SET lease_owner = ?, lease_expires_at = ?
 WHERE id = ?
   AND committed_at IS NULL
   AND lease_expires_at < ?;

-- 3. Read the current row (either the one we just inserted,
--    the one we just stole, or an untouched pre-existing row).
SELECT committed_at, lease_owner, lease_expires_at
  FROM write_ids WHERE id = ?;

-- 4. Append the per-attempt audit row for (g).
INSERT INTO write_id_reserve_events
    (event_id, id, run_id, task_id, outcome, occurred_at)
VALUES (?, ?, ?, ?, ?, ?);
```

State classification (Go-side, from the results of statements 1, 2,
and 3):

- Statement 1 rowcount = 1 → `ReserveFresh`. `outcome = 'fresh'`.
- Statement 1 rowcount = 0 AND statement 3's `committed_at` IS NOT
  NULL → `ReserveCommitted`. `outcome = 'committed'`.
- Statement 1 rowcount = 0 AND `committed_at` IS NULL AND
  statement 2 rowcount = 1 (i.e. we stole an expired lease) →
  `ReserveUncommitted`. `outcome = 'uncommitted'`.
- Statement 1 rowcount = 0 AND `committed_at` IS NULL AND
  statement 2 rowcount = 0 (a live lease is present) AND
  `lease_owner == req.WorkerID` → `ReserveUncommitted` (this
  worker crashed and restarted, its own lease is still valid).
  `outcome = 'uncommitted'`.
- Statement 1 rowcount = 0 AND `committed_at` IS NULL AND
  statement 2 rowcount = 0 AND `lease_owner != req.WorkerID` →
  `ReserveInFlight`. `outcome = 'inflight'`.

Because SQLite serialises writers, the whole 3-statement transaction
provides ordering; no `Commit` can slip between statements 1 and 2.
No filesystem is consulted — the journal file plays no part in
Reserve's decision (§5.1).

Test coverage in `observerstore/write_ids_writer_test.go`:
`TestReserve_FreshOnFirstCall`,
`TestReserve_UncommittedAfterCrash`,
`TestReserve_CommittedAfterHappyPath`,
`TestReserve_AtomicUnderConcurrency`,
`TestReserve_AllStatementsRollBackOnAuditFailure`,
`TestReserve_SQLMetaCharactersRoundTrip`.

(Full lease-aware classifier is above.) The `INSERT OR IGNORE`
is atomic under SQLite's default rollback
journal AND under WAL (the observerstore uses WAL, `store.go:54`).
Wrapping (1)+(2)+(3) in `BeginTx` guarantees no concurrent `Commit`
slips between (1) and (2), and guarantees the reservation and its
audit row land or fail together — a partial transaction that
inserted the reservation but not the audit row would blind §7(g).

`Commit` is `UPDATE write_ids SET committed_at = ?, lease_owner =
'', lease_expires_at = NULL WHERE id = ? AND committed_at IS NULL
AND lease_owner = ?` inside its own transaction, plus a
`write_id_reserve_events` audit row with `outcome = 'commit'`.
The `lease_owner = ?` guard (bound to req.WorkerID) prevents a
stale worker from committing under someone else's lease. Idempotent
because the `committed_at IS NULL` clause makes the second call a
no-op (the audit row is not re-emitted in that case — we detect the
no-op by inspecting the update's `RowsAffected`). If RowsAffected =
0 due to lease-owner mismatch, Commit returns `ErrLeaseLost` so the
caller can propagate the failure rather than silently succeeding.

Reserve/Commit callers within `ExecutorResume` share a single
`WorkerID` for the lifetime of the resume invocation. The recommended
pattern is a fresh UUIDv4 minted at the top of each ExecutorResume
call and stored on `ExecutorDeps.WorkerID` before threading through
Reserve and Commit. See §6.2 for how the
executor loop consumes `ReserveInFlight`.

---

## 5. DDL

Appended to `multi-agent/internal/observerstore/schema.sql` at the end
of the file:

```sql
-- WT-2-task-resume: idempotency ledger for task-level resume.
-- One row per (conversation, step, target_path, content_hash) tuple;
-- see docs/specs/wt2-task-resume.spec.md §3 for derivation.
-- reserved_at is set by Reserve (atomic INSERT OR IGNORE); committed_at
-- is set by Commit after the observable side effect completes.
CREATE TABLE IF NOT EXISTS write_ids (
    id                 TEXT PRIMARY KEY,
    task_id            TEXT NOT NULL,
    conversation_id    TEXT NOT NULL,
    step_id            TEXT NOT NULL,
    reserved_at        TEXT NOT NULL,
    committed_at       TEXT,
    lease_owner        TEXT NOT NULL DEFAULT '',    -- WorkerID of active leaseholder; '' when committed
    lease_expires_at   TEXT                          -- NULL when committed; else RFC3339Nano UTC
);

-- Lookup by (conversation, step) for resume planning: "which steps in
-- this conversation have been reserved / committed already?"
CREATE INDEX IF NOT EXISTS idx_write_ids_conv_step
    ON write_ids(conversation_id, step_id);

-- Lookup by task_id supports the retention vacuum's payload cleanup
-- join (§6.2a) and D4 ad-hoc audit queries.
CREATE INDEX IF NOT EXISTS idx_write_ids_task_id
    ON write_ids(task_id);

-- Retention vacuum (§7(f)): sweeps committed_at < cutoff. Also enables
-- efficient DuplicateSideEffectRate consumer view (§7(g)).
CREATE INDEX IF NOT EXISTS idx_write_ids_committed_at
    ON write_ids(committed_at);

-- Per-attempt audit trail for §7(g). One row per Reserve invocation
-- (fresh, uncommitted, inflight, or committed outcome) and one row
-- per Commit invocation ('commit' outcome). Keyed by run_id + write_id
-- so the D4 evaluator can compute
--   DuplicateSideEffectRate =
--       count(outcome IN ('uncommitted','committed'))
--     / count(outcome IN ('fresh','uncommitted','inflight','committed'))
-- The denominator MUST filter to Reserve outcomes only — 'commit'
-- rows are Commit events, not reserve attempts, and would double-count.
-- Scoped to a single run without joining on task_contracts.
-- worker_id (round-3 review P1 #4) records which worker emitted
-- the event. Used by Commit's noop-branch disambiguation: if a
-- commit already exists and its worker_id != req.WorkerID, return
-- ErrLeaseLostAfterWrite instead of silently succeeding. Default ''
-- so pre-existing rows (there are none in prod today) don't reject.
CREATE TABLE IF NOT EXISTS write_id_reserve_events (
    event_id     TEXT PRIMARY KEY,
    id           TEXT NOT NULL,
    run_id       TEXT NOT NULL,
    task_id      TEXT NOT NULL,
    outcome      TEXT NOT NULL CHECK(outcome IN ('fresh','uncommitted','inflight','committed','commit')),
    occurred_at  TEXT NOT NULL,
    worker_id    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_write_id_reserve_events_run
    ON write_id_reserve_events(run_id, occurred_at);
CREATE INDEX IF NOT EXISTS idx_write_id_reserve_events_id
    ON write_id_reserve_events(id, occurred_at);

-- (No task_journal_anchors table in this design; the observer DB
-- itself is the authoritative source of task/write state and the
-- journal is treated as an untrusted local mirror. See §5.1.)

-- Task-level resume attempt audit for §5.3 RecoverySuccessRate.
-- One row per (run_id, task_id) — the row is INSERTed at
-- ResumeTask entry with outcome='started', then UPDATEd on exit
-- to outcome='replayed' or 'error'. A row stuck in 'started'
-- means ResumeTask crashed between dispatch and audit-update:
-- D4 counts these as incomplete recoveries.
CREATE TABLE IF NOT EXISTS resume_task_attempts (
    run_id       TEXT NOT NULL,
    task_id      TEXT NOT NULL,
    outcome      TEXT NOT NULL CHECK(outcome IN ('started','replayed','error')),
    error_kind   TEXT NOT NULL DEFAULT '',
    started_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    PRIMARY KEY (run_id, task_id)
);
CREATE INDEX IF NOT EXISTS idx_resume_task_attempts_run
    ON resume_task_attempts(run_id, updated_at);

-- Run-scoped task index used by ResumeTask candidate discovery
-- (§5.1). One row per (run_id, task_id); written by
-- submit_contract_task immediately after SaveTaskContract.
CREATE TABLE IF NOT EXISTS task_run_bindings (
    run_id      TEXT NOT NULL,
    task_id     TEXT NOT NULL,
    bound_at    TEXT NOT NULL,
    PRIMARY KEY (run_id, task_id)
);
CREATE INDEX IF NOT EXISTS idx_task_run_bindings_run
    ON task_run_bindings(run_id, bound_at);

-- Pre-write payload staging for §6.2a byte-identical retry after
-- crash. Written BEFORE Reserve so a resumed ExecutorResume can
-- reconstruct contentHash and re-issue the exact byte stream that
-- was pending at crash time. Keyed by WriteID (which is
-- content-derived) so legitimate content changes never conflict.
CREATE TABLE IF NOT EXISTS write_id_payloads (
    id          TEXT PRIMARY KEY,
    task_id     TEXT NOT NULL,
    step_id     TEXT NOT NULL,
    payload     BLOB NOT NULL,
    sha256      TEXT NOT NULL,
    staged_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_write_id_payloads_task_step
    ON write_id_payloads(task_id, step_id, staged_at);
```

Column semantics:

- `id` — the hex-encoded `WriteID` from §3.
- `conversation_id` — the `TaskContract.ConversationID` this write
  belongs to; duplicated onto every row so `Vacuum` and the D4
  consumer view can filter without joining `task_contracts`.
- `step_id` — deterministic per-attempt identifier (e.g. write target
  index inside the contract). Not required to be globally unique — the
  `(conversation_id, step_id)` pair is unique per attempt.
- `reserved_at` — RFC3339Nano UTC. Set by `Reserve`.
- `committed_at` — RFC3339Nano UTC or NULL. Set by `Commit`. NULL means
  "reserved but the side effect never completed" — a resume caller
  MUST NOT skip such a row (see §6.2).

### 5.1 Journal trust boundary (§7(e))

The existing `TaskJournal` is an untrusted local file for this
worktree's purposes. `task_journal.go` is not modified. Instead of
authenticating the journal file (which the previous design attempts
established cannot be done without touching `Append` at write time),
this spec makes the observer DB the AUTHORITATIVE source of every
security-relevant fact about a task:

| Fact | Authoritative source (DB) | Untrusted mirror (journal) |
|---|---|---|
| Task exists | `task_contracts.task_id` | `delegate_task` record |
| Task is terminal | (implicit — the run's own driver never resumes a task after producing a terminal result) | `terminal: true` record |
| A specific write attempt was made | `write_ids.id` row | (not mirrored) |
| A specific write attempt committed | `write_ids.committed_at IS NOT NULL` | (not mirrored) |
| Payload bytes for a step | `write_id_payloads` row | (not mirrored) |
| Run scoping | `write_id_reserve_events.run_id`, `resume_task_attempts.run_id` | (not mirrored) |

`ResumeTask` performs NO journal I/O. The caller (eval-runner
harness or operator CLI) discovers candidate `task_id`s to resume
via the observer DB itself:

- Query `task_run_bindings` (new table added in §5 below) —
  scoped to `run_id` — joined with `task_contracts`, filtering
  out tasks that already have a `resume_task_attempts` row with
  `outcome='replayed'` scoped to the SAME `run_id`. The
  `task_run_bindings` row is written ATOMICALLY with the
  contract row: `SaveTaskContract` is extended (or a new
  `SaveTaskContractAndBindRun(taskID, convID, body, runID)`
  helper is added on `ObserverRelay`) so that BOTH `task_contracts`
  and `task_run_bindings` land inside a SINGLE observer DB
  transaction. A crash between the two inserts is therefore
  impossible — either both rows are durable or neither is, and
  the driver-side retry loop in §6.1b either sees success (both
  rows landed) or retries the whole pair.
- This scoping prevents a resume driver from picking up historical
  `task_contracts` rows from previous runs when the same observer
  DB is reused across runs.
- The eval-runner's resume driver iterates the resulting task_id
  set and calls `ResumeTask(runID, taskID)` for each. The
  DB-only path covers driver crashes at every stage AFTER
  SaveTaskContract, including "dispatched-but-slave-hadn't-yet-
  Reserved" — the contract row is present, no reservations exist
  yet, ResumeTask still runs LoadContract + Dispatch and the
  slave re-drives from scratch (all Reserves return
  `ReserveFresh`).
- The residual pre-persist crash window (before SaveTaskContract
  succeeds) is covered by §6.1b's fail-loud tool-call error —
  the caller sees the failure at submit time.

The eval-runner's resume driver iterates that union and calls
`ResumeTask(runID, taskID)` for each. `TaskJournal` remains a
local convenience for operator `list_driver_tasks` inspection,
but never influences resume decisions.

The resulting safety property: an attacker who forges journal
bytes (appending, mutating, deleting) cannot influence resume in
any way — `ResumeTask` never reads the journal. All safety-critical
decisions are DB-authoritative: `Reserve` returns fresh /
uncommitted / inflight / committed from `write_ids` alone;
`PayloadStager` reads payload bytes from `write_id_payloads` alone;
`LoadContract` reads from `task_contracts` alone.
**Journal appends are unrestricted.** Nothing about this worktree
changes how `driver/tools.go:161,:208` (or any other future
`TaskJournal.Append` caller) writes to the journal. The journal
remains an append-only local file with its existing mutex; the
resume path treats it as an untrusted index.

`ErrJournalChainBroken` is retained as a sentinel for the specific
symptom "the journal returned a record whose `task_id` field
disagrees with a `write_ids.task_id` under the same primary-key
row" — an impossible-by-construction case that would indicate DB
corruption. In normal operation it is never raised. It is kept only
so that a future security-hardening worktree that DOES introduce
per-record hash chaining has a stable sentinel to reuse without
breaking callers' `errors.Is` checks.

### 5.2 Retention policy (§7(f))

`Vacuum` deletes rows with `committed_at IS NOT NULL AND committed_at
< cutoff`. Default operator-supplied cutoff: `now - 30 days`. The
spec commits to "≥ 30 days old committed rows MAY be vacuumed"; the
exact cadence is the operator's choice (D3 eval-runner harness will
call it once per run tear-down for now).

Reserved-but-uncommitted rows are NEVER vacuumed by the default
cutoff — a stuck reservation is a signal of a real problem and MUST
surface via manual inspection. A separate operator command (out of
scope) can force-vacuum stale reservations after human review.

### 5.3 Consumer view for `DuplicateSideEffectRate` (§7(g))

The D4 evaluator computes `DuplicateSideEffectRate` as:

```sql
SELECT
  CAST(SUM(CASE WHEN outcome IN ('uncommitted','committed')
                THEN 1 ELSE 0 END) AS REAL)
  / NULLIF(SUM(CASE WHEN outcome IN ('fresh','uncommitted','committed','inflight')
                    THEN 1 ELSE 0 END), 0)
FROM write_id_reserve_events
WHERE run_id = ?;
```

The denominator EXPLICITLY filters to Reserve-outcome rows only,
so `commit` audit rows do not inflate the denominator — a reservation
followed by a commit is 1 attempt, not 2. `inflight` outcomes ARE
counted in the denominator but NOT the numerator: a Reserve that
saw a live concurrent lease is a real reserve attempt, but it did
NOT retry a prior write (the other worker is doing that), so
counting it as a "duplicate" would double-count concurrency-aware
work. `NULLIF(..., 0)` returns NULL for runs with zero reservations
(SQL null propagates cleanly to the D4 extractor).

`RecoverySuccessRate` is TASK-level, not write-level (§A6's metric
definition is "task recovered without human help / tasks that
needed recovery"). It reads from the `resume_task_attempts` audit
table declared once in §5 (DDL keyed by `(run_id, task_id)`,
outcome vocabulary `started | replayed | error`). D4 computes:

```sql
SELECT
  CAST(SUM(CASE WHEN outcome = 'replayed' THEN 1 ELSE 0 END) AS REAL)
  / NULLIF(COUNT(*), 0)
FROM resume_task_attempts
WHERE run_id = ?;
```

Only `replayed` counts as "recovered without human help". Rows
stuck in `started` (driver crashed mid-resume before the audit
UPDATE landed) and rows in `error` both contribute 0 to the
numerator and 1 to the denominator — the correct accounting for
incomplete or failed recoveries.

The spec commits to the following queryable evidence so the D4
evaluator can compute both ratios from observer DB tables alone (no
counter-scraping required, no run attribution ambiguity). Note the
split-table contract: **`DuplicateSideEffectRate` reads
`write_id_reserve_events`; `RecoverySuccessRate` reads
`resume_task_attempts`.** They are not the same table because they
measure different things — per-write reservation attempts vs
per-task recovery outcomes.

Populated by `Reserve`/`Commit` (per-write audit — feeds
`DuplicateSideEffectRate`):

- Every `Reserve` invocation writes one row to
  `write_id_reserve_events` with the tuple `(event_id, id, run_id,
  task_id, outcome IN {fresh, uncommitted, inflight, committed},
  occurred_at)` inside the same transaction as the `write_ids`
  insertion (§4.1 statement 4). `inflight` is emitted even though
  no row was inserted or updated on `write_ids` in that transaction;
  the audit row records the attempt for D4.
- Every `Commit` invocation writes one row with `outcome = 'commit'`.
- `event_id` is a hex-sha256 of `(id, occurred_at, outcome)` so the
  row is deterministic under retries but the PRIMARY KEY still
  prevents accidental double-insert.
- Test coverage for inflight audit:
  `TestReserve_EmitsAuditRowForInflight`, and
  `TestReserve_InflightRowsCounted_ByDenominator` (asserts the D4
  SQL includes inflight rows in the denominator but not the
  numerator, matching §5.3's formula).

Populated by `ResumeTask` (per-task audit — feeds
`RecoverySuccessRate`):

- Step 2 of `ResumeTask` writes one row (UPSERT) to
  `resume_task_attempts` with `outcome='started'` before dispatch.
- Step 5 UPDATEs that row to `outcome='replayed'` or
  `outcome='error'` after dispatch returns.
- Both writes go through
  `WriteIDStore.RecordResumeAttempt(runID, taskID, outcome,
  errorKind)`; the row PRIMARY KEY is `(run_id, task_id)`.

Because each audit row lives in the same transaction as the state
mutation it describes, both audits are guaranteed complete: if the
mutation landed, the audit row landed. The counter metrics in §8
remain as best-effort operational signals for live dashboards; the
D4 evaluator does NOT depend on them.

If a future D4 revision wants a pre-materialised
`duplicate_reserve_count` column on `runs`, it can add one as a
`SUM(CASE WHEN outcome IN ('uncommitted','committed') THEN 1 ELSE 0
END)` roll-up. Nothing in this spec blocks that follow-up.

---

## 6. Driver / executor resume flow

### 6.1 `driver.ResumeTask`

```go
// ResumeDeps bundles the collaborators ResumeTask needs. All fields
// required; nil returns ErrInvalidResumeDeps. Notably, there is NO
// TaskJournal field — ResumeTask does not read the journal.
type ResumeDeps struct {
    Store          observerstore.WriteIDStore
    // LoadContract returns the TaskContract associated with taskID.
    // Implementations MUST read from a persisted source (the observer
    // `task_contracts` table populated by the reordered
    // `submit_contract_task` flow — see §6.1b for the follow-up
    // worktree that does the reorder) rather than an in-memory
    // cache: after a driver restart, in-memory state is gone.
    LoadContract   func(ctx context.Context, taskID string) (contract.TaskContract, error)
    // Dispatch drives the slave-side ExecutorResume. The runID is
    // threaded through so that every ReserveRequest and
    // CommitRequest inside the slave carries the same run scoping
    // as the audit row in resume_task_attempts.
    Dispatch       func(ctx context.Context, runID, taskID string, contract contract.TaskContract) error
}

// ResumeTask validates taskID, audits the attempt, loads the
// authoritative contract from the observer DB, and asks the
// dispatcher to re-drive the task. It performs NO journal I/O —
// journal appends by other paths never influence its behaviour.
// Actual writes happen inside executor.ExecutorResume after
// Reserve gates each one. runID scopes the audit row emitted in
// resume_task_attempts (§5.3) so D4 can compute
// RecoverySuccessRate per run.
func ResumeTask(ctx context.Context, deps ResumeDeps, runID, taskID string) error
```

Behaviour, in order (no journal read at any step — the journal is
not consulted by `ResumeTask` in this design; consult §5.1 for
the trust boundary rationale):

1. `taskID` MUST match `^[A-Za-z0-9_-]{8,128}$`. Otherwise return
   `ErrInvalidTaskID` (§7(d)).
2. `RecordResumeAttempt(runID, taskID, "started", "")` — write a
   pre-dispatch audit row. The row's PRIMARY KEY is `(run_id,
   task_id)` so re-invocation of `ResumeTask` for the same
   `(runID, taskID)` upserts (refreshes `started_at`, resets
   `outcome` to `"started"`, clears `error_kind`) but does not
   create duplicates.
3. Call `contractBody, err = deps.LoadContract(ctx, taskID)`
   (§6.1b). On `ErrContractNotFound`: update the audit row with
   `outcome='error', error_kind='ErrContractNotFound'` and bubble.
4. Call `deps.Dispatch(ctx, runID, taskID, contractBody)`. Dispatch
   is the code path that eventually invokes `ExecutorResume` on the
   slave; the driver-side `ResumeTask` awaits its completion. `runID`
   flows through to `ExecutorDeps.WorkerID` seeding (a fresh UUIDv4
   per invocation) and to every `ReserveRequest.RunID` /
   `CommitRequest.RunID` inside the executor.
5. On success: update the audit row with `outcome='replayed'`.
   On error: update the audit row with `outcome='error',
   error_kind=classifyErr(err)`, bubble the error.
6. `ResumeTask` does NOT retry.

The audit row is UPDATED (not re-inserted) at the terminal exit,
via `RecordResumeAttempt` with the transition semantics: `started
→ replayed | error`. If the driver crashes between step 4 and step
5, the audit row remains in `started` state — D4 correctly counts
this as an incomplete recovery (`outcome NOT IN ('replayed')`).
On restart, re-invoking
`ResumeTask` for the same `(runID, taskID)` re-enters via step 2
and either succeeds (bringing the row to `replayed`) or fails
(bringing it to `error`); D4's final read wins.

**No journal-controlled fast-path exists.** `ResumeTask` performs
no journal I/O — the journal is neither read for scheduling nor
consulted for terminal detection. The tradeoff: `ResumeTask` on an
already-finished task now costs one Dispatch + N
`Reserve(ReserveCommitted)` round-trips instead of a journal scan.
That is O(N) DB reads for a single-task resume, bounded by the
contract's `WriteTargets` count. This is acceptable because it
eliminates the "forged terminal suppresses recovery" attack —
safety trumps the cost.

**No `--resume-task` operator bypass** is defined (previous drafts
proposed one; it is unnecessary once the default `ResumeTask`
never consults the journal). Callers invoke `ResumeTask` directly.

RecordResumeAttempt outcome vocabulary:
`started` (initial insert), `replayed` (success),
`error` (failure). No `noop` outcome; a run with zero writes
still records `replayed`.

Test coverage in `driver/resume_test.go`:
`TestResumeTask_StartedRowInsertedBeforeDispatch`,
`TestResumeTask_ReplayedRowAfterSuccess`,
`TestResumeTask_ErrorRowOnContractMissing`,
`TestResumeTask_ErrorRowOnDispatchFail`,
`TestResumeTask_CrashAfterDispatch_LeavesStartedRow`,
`TestResumeTask_ReinvocationUpdatesRowInPlace`,
`TestResumeTask_ForgedTerminalStillDispatches` (P0 regression:
appending `terminal:true` DOES NOT suppress the Dispatch call).

### 6.1b Contract loader source

The `LoadContract` implementation in prod reads the observer's
existing `task_contracts` table (`schema.sql:166`, populated by
`driver/contract_tools.go:217`'s `SaveTaskContract` call). Selection
key is `(workspace_id, task_id)`; the returned body is the JSON
already stored there. Test doubles pass a map-backed implementation.

**Persist ordering discipline (integration follow-up scope,
§1.4).** The current `driver/contract_tools.go` sequence
(submit_contract_task handler) is:
1. `s.t.sdk.DelegateTask(ctx, ...)` (line 178) — dispatches to slave.
2. `s.t.recordDelegatedTask(...)` → `TaskJournal.Append(delegate_task)` (line 208).
3. `s.t.observerRelay().SaveTaskContract(...)` (line 217).

That order is unsafe for resume: a crash between step 1 and step 3
leaves a task that the slave is actively working on with NO
persisted contract for a later `ResumeTask.LoadContract` to find.
Fixing this ordering — including atomic contract + run-binding
persistence via a new `SaveTaskContractAndBindRun` on the
observerstore/observerweb layer, plus a `--run-id` CLI flag on
the driver binary — is scoped to the
`wt2-contract-tools-run-binding` follow-up worktree (§1.4). THIS
worktree provides only the DDL (`task_run_bindings`) and the
LoadContract signature that the reordered flow will target; it
does not modify `contract_tools.go` or `observer_relay.go`.

Because `agentsdk.Client.DelegateTask` (external module
`github.com/agentserver/agentserver/pkg/agentsdk`, verified at
`driver/agentsdk_client.go:10`) is the entity that MINTS the
`task_id` server-side and returns it in the response, this
worktree cannot fully pre-persist the contract before dispatch
without either (a) an upstream sdk API change to accept a
client-supplied task_id — out of scope, and (b) a local
pre-allocation that later contradicts the server's minted id —
worse than the status quo.

**Downstream integration recipe** (implemented in
`wt2-contract-tools-run-binding`, not here):

1. `s.t.sdk.DelegateTask(ctx, ...)` (unchanged) → returns
   `resp.TaskID`.
2. Immediately call the new atomic helper
   `s.t.observerRelay().SaveTaskContractAndBindRun(ctx,
   resp.TaskID, tc.ConversationID, contractBody,
   s.t.cfg.RunID)` — inserts `task_contracts` +
   `task_run_bindings` inside one DB transaction. Retry
   6× with exponential backoff on transient failures; hard
   give-up returns an error to the tool caller.
3. `s.t.recordDelegatedTask(...)` — journal append LAST.

Whichever worktree lands the integration MUST validate the
accepted residual crash window (SIGKILL between DelegateTask
return and SaveTaskContractAndBindRun success) surfaces as
`ErrContractNotFound` on a later `ResumeTask` — never a silent
duplicate write. THIS worktree's tests only cover the primitives
against a fake in-memory helper.

### 6.1c First-run write gating scope

`ExecutorResume`'s `Stage → Reserve → WriteStep → Commit` gate
protects RESUME reissue against duplication. For the same gate to
work as a first-run duplicate-prevention mechanism, the initial
slave write path must ALSO route through this pipeline; otherwise
a crash-then-resume finds no `write_ids` row and issues a
duplicate write.

**In scope for this worktree**: the `driver.Dispatch` →
`slave.ExecutorResume(FreshExecute)` code path. Because
`FreshExecute` is required (§6.2 step 1a) and calls the same
Stage/Reserve/WriteStep/Commit primitives, wiring the slave's
first-run write to invoke `ExecutorResume` with a nil-payload
`Step` (rather than writing directly) gives the same gate.

**Concrete first-run wiring** — the slave's file/observer write
tools that this worktree considers "gated" MUST NOT be modified
by this worktree (that is a large surgery scoped to Phase 2 WT-2
wiring worktrees, e.g. wt2-executor-writes-through-gate). This
worktree only:

1. Defines the primitives (`ExecutorResume`, `PayloadStager`,
   `WriteIDStore`) needed for the gate.
2. Provides `FreshExecute` as the API shape a first-run write
   would use to opt into gating.
3. Adds `TestFirstRunWriteGoesThroughGate` in
   `executor/resume_test.go` — a fixture test that constructs a
   fake `WriteStep` and asserts a fresh-execution Step
   traverses `Stage → Reserve → WriteStep → Commit` producing
   exactly one row per new WriteID in `write_ids`, matching
   what a subsequent resume would find. This test proves the
   primitives compose correctly.

**Out of scope, tracked**: retrofitting the pre-existing slave
write paths (e.g. `internal/executor/file.go`,
`internal/executor/bash.go`) to route through `ExecutorResume`.
That work is a separate worktree; without it, first-run writes
via those paths remain outside the resume-safety guarantee. §11
lists this as an explicit out-of-scope follow-up.

This worktree does NOT modify `contract_tools.go` (§1.4). The
reorder + retry lands in the follow-up worktree; this spec only
defines the `LoadContract` signature the reordered flow will
target.

`LoadContract` returns `ErrContractNotFound` (wrapping
`observerstore`'s query-returned-no-rows sentinel) if no contract
row exists for `taskID`. `ResumeTask` maps `ErrContractNotFound`
to the `error` audit outcome and surfaces the wrapped error; it
does NOT auto-fabricate a stub contract (that would violate
§7(a) content-hash safety — an empty contract has no
`WriteTargets` and would replay-skip legitimate writes). Test
coverage in `driver/resume_test.go`:
`TestResumeTask_ContractNotFound_ErrorOutcome` (fake LoadContract
returns ErrContractNotFound; assert the audit row and error).


### 6.2 `executor.ExecutorResume`

```go
type ResumeRequest struct {
    TaskID         string
    RunID          string
    ConversationID string
    Contract       contract.TaskContract
    // Steps is the caller's authoritative source of intended payload
    // bytes and target order. len(Steps) == len(Contract.DataContract.WriteTargets)
    // is required; the executor iterates Steps in order (not
    // WriteTargets) so that a caller can supply a subset in edge
    // cases (e.g. contract mid-edit — currently disallowed, but the
    // API is compatible).
    Steps []Step
}

type Step struct {
    Index   int                  // 0-based position in the contract
    Target  contract.WriteTarget // mirrored from contract for convenience
    Payload []byte               // intended bytes to write; caller MUST have already computed sha256
}

type ExecutorDeps struct {
    Store         observerstore.WriteIDStore
    PayloadStager observerstore.PayloadStager // see §6.2a
    // WorkerID identifies this ExecutorResume invocation for lease
    // tracking (§4). MUST be INVOCATION-UNIQUE (not merely
    // process-unique): the recommended pattern is a fresh UUIDv4
    // minted at the top of each ExecutorResume call. This closes
    // the "two concurrent ExecutorResume calls inside one process
    // share hostname-pid and race" hole: with distinct WorkerIDs,
    // the second call's Reserve sees the first's live lease as a
    // foreign owner and returns ReserveInFlight. Reusing the same
    // WorkerID across a lease TTL is ONLY safe when the caller
    // guarantees serial execution (single-threaded in-process retry
    // after a transient DB error); do NOT reuse it across
    // goroutines.
    WorkerID string
    // LeaseTTL is passed into every Reserve call. Recommended: 30s
    // for interactive slaves, up to 5min for batch jobs. Minimum 1s.
    LeaseTTL time.Duration
    // FreshExecute handles a Step whose Payload is nil (the "step
    // never staged" case, §6.2 step 1a). Implementations must
    // themselves call Stage + Reserve + WriteStep + Commit — the
    // same primitives the non-resume happy path uses. Nil is not
    // allowed; the driver-side caller MUST wire this before invoking
    // ExecutorResume.
    FreshExecute func(ctx context.Context, step Step) error
    // WriteStep performs the actual side effect for one WriteTarget.
    // It MUST perform the write atomically (temp file + rename, or
    // equivalent) so that a crash during WriteStep leaves the target
    // path either untouched or containing exactly step.Payload — never
    // a partial write. Combined with the byte-identical retry
    // guarantee from PayloadStager (§6.2a), this makes
    // ReserveUncommitted retries safe.
    WriteStep func(ctx context.Context, step Step) error
}

// ExecutorResume iterates req.Steps in declared order. For each
// step it computes the WriteID (§3), calls Reserve, and drives the
// write-or-skip decision from the returned ReserveState.
func ExecutorResume(ctx context.Context, deps ExecutorDeps, req ResumeRequest) error
```

The `req.Steps` slice is REQUIRED to carry `Payload` bytes for
every step. Callers that survive process loss (e.g. driver-side
orchestrator restarting after slave crash) reconstruct
`Step.Payload` by calling
`deps.PayloadStager.ListForStep(ctx, req.TaskID, stepID)`,
picking the most recent uncommitted PayloadRef, and calling
`deps.PayloadStager.Load(ctx, ref.ID)` — the driver's resume
orchestrator does this ONCE per step before calling
`ExecutorResume`. See §6.2a for the driver-side reconstruction
loop.

Behaviour of `ExecutorResume` itself, per `Step`, given that
`Step.Payload` is authoritative when non-nil:

1. Compute `stepID = fmt.Sprintf("write-%d-%s", step.Index,
   step.Target.Name)`. Non-empty, deterministic across restarts.
1a. **Nil-payload fallthrough.** If `step.Payload == nil`, the
    resume orchestrator has no bytes to replay for this step; the
    slave must produce them by rerunning the model / tool-call
    that would have generated the bytes on first execution. Skip
    steps 2–6 for this step; delegate to the caller-supplied
    fallthrough handler `deps.FreshExecute(ctx, step)`. That
    handler is responsible for calling `Stage` + `Reserve` +
    `WriteStep` + `Commit` itself using the same primitives the
    non-resume happy path uses. Return whatever `FreshExecute`
    returns.
2. Compute the INTENDED `contentHash = hex(sha256(step.Payload))`
   from the caller-supplied payload FIRST. This is the tie-breaker
   that closes the "prior uncommitted attempt with different bytes
   confuses recovery" race.
3. Compute `id, err = NewWriteID(req.TaskID, req.ConversationID,
   stepID, step.Target.Name, contentHash)`; on err, return it.
4. Consult the stager for the row keyed by this INTENDED id, using
   `PayloadStager.Load(ctx, id)`:
   - `err == nil`: byte-identical payload already staged under this
     exact WriteID — skip the re-Stage in step 5 (idempotent no-op).
   - `errors.Is(err, ErrPayloadUnavailable)`: no prior stage for
     this specific WriteID → proceed to step 5.
   - other err: return it.
5. `deps.PayloadStager.Stage(ctx, id, req.TaskID, stepID,
   step.Payload)` — durably persists the exact bytes. Idempotent
   (WriteID-keyed). Any pre-existing row under a DIFFERENT WriteID
   for the same (taskID, stepID) is preserved (D4 audit retains
   both attempts); the executor is uninterested in it.
6. Call `state, err = deps.Store.Reserve(ctx, ReserveRequest{ID:
   id, RunID: req.RunID, TaskID: req.TaskID, ConversationID:
   req.ConversationID, StepID: stepID, WorkerID: deps.WorkerID,
   LeaseTTL: deps.LeaseTTL})`.
   - `ReserveFresh` → call `deps.WriteStep(ctx, step)`, then
     `Commit(CommitRequest{ID: id, RunID: req.RunID, TaskID:
     req.TaskID, WorkerID: deps.WorkerID})`. If `WriteStep`
     errors, return; the row remains reserved with our lease until
     expiry, so the next resume by ANOTHER worker sees
     `ReserveInFlight` until the lease dies, then
     `ReserveUncommitted`.
   - `ReserveUncommitted` → we own the (fresh or stolen) lease; call
     `deps.WriteStep(ctx, step)` again — safety comes from
     `WriteStep`'s atomic-rename contract combined with the
     content-hash-derived WriteID. Then Commit.
   - `ReserveInFlight` → another worker owns an active lease.
     Return `ErrConcurrentLease` to the caller and STOP the loop
     for this taskID. The caller (driver-side orchestrator)
     decides whether to back off and retry after the lease TTL or
     accept that another driver is handling this task and no local
     write is needed.
   - `ReserveCommitted` → skip; the earlier attempt durably
     completed.
   - `(_, err)` → return the error immediately; no rollback of
     prior steps is attempted (this is what §7(a) protects
     against, not this path).

The tri-state contract closes the "reserved-but-uncommitted skip
forever" hole from the earlier codex round: a slave crash between
`Reserve` and `WriteStep` leaves `committed_at = NULL`; the next
resume sees `ReserveUncommitted`, re-issues the byte-identical write,
and calls `Commit` — the write DOES happen, exactly once observably.

### 6.2a Payload source across restarts

The retry semantics require that after a slave restart the retry
produces byte-identical payload bytes to the original attempt.
Since after slave process loss in-memory payload state is gone,
the driver-side resume orchestrator reconstructs `Step.Payload`
from the `write_id_payloads` table BEFORE calling
`ExecutorResume`. The reconstruction algorithm, per step:

1. `refs, err = deps.PayloadStager.ListForStep(ctx, taskID, stepID)`.
   Each ref carries `CommittedAt` populated via the LEFT JOIN on
   `write_ids`.
2. **Committed-wins priority.** If ANY `ref.CommittedAt != nil`,
   the step already durably completed in a prior run. Use the
   payload bytes of the most-recently-committed ref: `payload, _ =
   PayloadStager.Load(ctx, mostRecentCommittedRef.ID)`. This
   forces the executor's step-2 contentHash to match the committed
   WriteID, so step 6 returns `ReserveCommitted` and NO rewrite
   occurs regardless of what fresh bytes the model might have
   regenerated. This is the safety-critical rule: a durable prior
   commit is FINAL for that step within the task's lifetime.
3. Otherwise, if `refs` contains any uncommitted element, pick
   the most recent one and use those bytes — the crashed prior
   attempt is being retried byte-identically.
4. If `refs` is empty → the step has never been staged. This is
   the "driver crashed after dispatch but before the slave staged
   any bytes" case AND the normal "step not yet reached in the
   contract's execution order" case — they are indistinguishable
   from the DB alone. `ReconstructSteps` returns a `Step` with
   `Payload = nil` for this step. `ExecutorResume` handles a
   nil-payload step by SKIPPING the Load-or-Stage-or-Reserve
   pipeline and delegating to the standard slave execution path
   (i.e. re-request the model, receive tool-call arguments, stage
   + reserve + write from scratch). This makes the "no-stage
   restart" case behave the same as a fresh contract execution:
   the slave produces the writes just like it would have on the
   first attempt, and any writes it does produce go through the
   normal Stage/Reserve/Commit pipeline. Test:
   `TestExecutorResume_NilPayloadFallsThroughToFreshExecution`,
   `TestReconstructSteps_UnstagedStepYieldsNilPayload`.

The driver-side reconstruction lives in a helper
`driver.ReconstructSteps(ctx, deps, taskID, contract)` that
returns `[]Step` for `ResumeRequest.Steps`.

`PayloadStager` implementations own BOTH the write path (`Stage`
during first attempt) AND the read path (`Load` / `ListForStep`
during resume), giving one authoritative source of truth for
"what bytes were about to be written for this (taskID, stepID)".

```go
type PayloadStager interface {
    // Stage durably persists payload keyed by WriteID. Because the
    // WriteID itself is derived from sha256(payload) (§3),
    // duplicate Stage calls for the same WriteID are idempotent
    // (same payload → same id → PRIMARY KEY collision → no-op).
    // A legitimate content change produces a NEW WriteID and
    // therefore a NEW staging row, without any conflict against
    // the old row — this is the primitive that makes §7(a) work.
    Stage(ctx context.Context, id WriteID, taskID, stepID string, payload []byte) error

    // Load returns the byte payload previously staged for id, or
    // (nil, ErrPayloadUnavailable) if no row exists. Used by
    // ExecutorResume to verify a pre-existing staged row (§6.2 step
    // 4). Deterministic across restarts.
    Load(ctx context.Context, id WriteID) ([]byte, error)

    // ListForStep enumerates staged rows for (taskID, stepID) in
    // reverse-chronological order (most-recently-staged first).
    // Each element carries id + sha256 + staged_at. Used by
    // resume orchestrators to reconstruct the caller-supplied
    // payload after slave process loss: the caller iterates the
    // list, loads each id via Load(id), and picks the row whose
    // sha256 matches the contract's declared intent (if the
    // contract records one) or defers to the most recent
    // uncommitted row (checked against write_ids.committed_at IS
    // NULL). Empty slice + nil err means "no prior stage".
    ListForStep(ctx context.Context, taskID, stepID string) ([]PayloadRef, error)
}

type PayloadRef struct {
    ID          WriteID
    SHA256      string
    StagedAt    time.Time
    // CommittedAt is nil when the associated write_ids row's
    // committed_at IS NULL (the attempt is uncommitted or in-flight),
    // non-nil when the write durably committed. Populated by a
    // LEFT JOIN in the SQLite implementation:
    //   SELECT wp.id, wp.sha256, wp.staged_at, wi.committed_at
    //     FROM write_id_payloads wp
    //     LEFT JOIN write_ids wi ON wi.id = wp.id
    //    WHERE wp.task_id = ? AND wp.step_id = ?
    //    ORDER BY wp.staged_at DESC;
    // Driver-side ReconstructSteps consumes CommittedAt to distinguish
    // "prior attempt is done — supply intended payload for verify
    // ReserveCommitted" from "prior attempt crashed uncommitted —
    // supply THOSE bytes so the WriteID collides and Reserve returns
    // ReserveUncommitted".
    CommittedAt *time.Time
}
```

DDL for the staging table (appended to `schema.sql` alongside the
other new tables in §5):

```sql
CREATE TABLE IF NOT EXISTS write_id_payloads (
    id          TEXT PRIMARY KEY,      -- the WriteID (§3)
    task_id     TEXT NOT NULL,
    step_id     TEXT NOT NULL,
    payload     BLOB NOT NULL,
    sha256      TEXT NOT NULL,          -- redundant with id but useful for audit
    staged_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_write_id_payloads_task_step
    ON write_id_payloads(task_id, step_id, staged_at);
```

`Stage` uses `INSERT OR IGNORE`: duplicate calls with the same id
land 0-rowcount (idempotent no-op). Content changes produce
distinct ids and therefore distinct rows, both retained until
retention vacuum (§5.2) removes committed ones — but see §6.3 for
the "prior uncommitted attempt overtaken by content change" case
where the older row is preserved for audit but harmlessly ignored
by the executor's Reserve loop.

`ErrPayloadStagingConflict` from the previous round is REMOVED —
keying by WriteID (which is content-derived) makes conflict
structurally impossible; a "conflict" would require two different
payloads producing the same sha256, i.e. a SHA-256 collision.

Vacuum: `Stage` rows are eligible for cleanup after the
corresponding `write_ids.committed_at` is older than the §5.2
30-day cutoff. The vacuum SQL is:

```sql
DELETE FROM write_id_payloads
 WHERE id IN (
   SELECT id FROM write_ids
    WHERE committed_at IS NOT NULL AND committed_at < ?
 );
```

The driver-side `ReconstructSteps` helper populates each
`Step.Payload` before `ExecutorResume` runs (see §6.2a). Inside
`ExecutorResume`, `PayloadStager.Load(ctx, id)` is called with the
intended WriteID (computed from `sha256(step.Payload)`); an
`ErrPayloadUnavailable` there is NOT fatal — it simply means the
executor must call `Stage(id, ...)` before Reserve.

#### 6.2a1 Stage-payload-before-Reserve ordering (single owner)

`ExecutorResume` is the SOLE owner of the WriteID-derive →
Load-or-Stage → Reserve → WriteStep → Commit sequence per write.
Callers do NOT interleave Reserve or Commit with their own logic —
they hand `ExecutorResume` a `ResumeRequest{Steps: []Step}` (one
per contract `WriteTarget`) plus the `WriteStep` closure, and
`ExecutorResume` runs the pipeline for every Step.

The pipeline sequence per Step (mirrors §6.2 numbered steps —
five inputs, taskID first):

1. Compute intended `contentHash = sha256(step.Payload)`.
2. Compute intended `id = NewWriteID(taskID, convID, stepID,
   targetName, contentHash)`.
3. `Load(id)` — if found (byte-identical prior stage), use the
   recovered bytes and skip step 4.
4. `Stage(id, taskID, stepID, step.Payload)` — durably persist.
   Idempotent (WriteID-keyed). Multiple prior stages for the same
   `(taskID, stepID)` under DIFFERENT WriteIDs (i.e. content
   changes over time) coexist as separate rows.
5. `Reserve(ReserveRequest{ID: id, ...})` — decide the outcome.
6. `WriteFresh|ReserveUncommitted` → `WriteStep(ctx, step)`, then
   `Commit`. `ReserveCommitted` → skip.

Because Stage (4) happens BEFORE Reserve (5), and Reserve happens
BEFORE WriteStep (6), any crash after Reserve leaves the payload
retrievable via `Load(id)`. The next `ExecutorResume` invocation
computes the same WriteID from the caller's Step.Payload,
verifies the staged row exists (step 3 finds it), sees
`ReserveUncommitted` at step 5, and completes the write.

**Content-change safety.** If the caller supplies a DIFFERENT
`Step.Payload` on the retry (legitimate content change), step 2
produces a NEW WriteID; step 3 `Load(newID)` returns
`ErrPayloadUnavailable`; step 4 stages a fresh row; step 5 gets
`ReserveFresh`; the write proceeds under the new id. The old
uncommitted row for the earlier attempt lives on independently
under its own id, harmlessly ignored by this iteration.

The pre-existing `writes` table (`schema.sql:146`) plays a
different role: it stores the final visible write's sha256 for
observer audit trail. The new `write_id_payloads` table stages
the pre-write bytes and is populated/vacuumed independently.

Test coverage in `executor/resume_test.go`:
`TestExecutorResume_CrashAfterReserveReplaysFromStagedPayload`
simulates a slave crash between (5) and (6), then invokes
`ExecutorResume` on a fresh process with byte-identical Step and
asserts: (a) `Load(id)` returns bytes; (b) Reserve returns
`ReserveUncommitted`; (c) WriteStep runs; (d) Commit completes;
(e) target file contains staged bytes;
`TestExecutorResume_ContentChangeYieldsFreshWriteID` supplies a
new Step.Payload after a stuck uncommitted prior attempt and
asserts fresh WriteID + no interaction with old row.

### 6.3 Reserved-but-uncommitted rows: content-change interaction

If a genuine content change happens AFTER a stuck (uncommitted)
reservation, the NEW `WriteID` differs (because `content_hash`
differs). A fresh `Reserve` on the new id returns `ReserveFresh` and
the write proceeds. The stuck uncommitted row from the earlier failed
attempt lives independently under its own id; it does not block the
retry, and the D4 audit still counts it as a separate reservation.
That is intentional and is the reason `content_hash` MUST be part of
the derivation (§7(a) again from a different angle).

---

## 7. Security & safety mitigations

Each item corresponds to the numbered risks in the WT-2 prompt.

### (a) `content_hash` MUST be part of WriteID derivation

**Threat:** without `content_hash`, a mid-run overwrite of the same
path with new bytes is falsely detected as a duplicate; `Reserve`
returns `(false, nil)` and the write is skipped. Result: user believes
the file was updated; on-disk state still holds the earlier version.

**Mitigation:** §3's `NewWriteID` takes `contentHash string` as a
required input and validates it against `^[0-9a-f]{64}$` — returning
`ErrEmptyWriteIDComponent` (for empty inputs) or
`ErrInvalidContentHash` (for placeholder / non-hex / wrong-length
inputs). The regex is compiled once at package init so a subsequent
editor cannot loosen it accidentally without breaking tests.

Test coverage in `observerstore/write_ids_writer_test.go`:
`TestNewWriteID_ContentChangeYieldsDistinctID` (positive — same conv,
step, path with two different payload sha256s produce two distinct
WriteIDs), `TestNewWriteID_EmptyContentHashRejected`,
`TestNewWriteID_PlaceholderContentHashRejected` (input =
`"placeholder"`), `TestNewWriteID_ShortContentHashRejected` (input =
63 hex chars), `TestNewWriteID_UppercaseContentHashRejected`,
`TestNewWriteID_NonHexContentHashRejected`. And in
`executor/resume_test.go`: `TestExecutorResume_ContentChangeRewrites`
demonstrates end-to-end that a content change triggers a fresh
reservation and a real write.

### (b) `Reserve` MUST be atomic `INSERT OR IGNORE`

**Threat:** if `Reserve` were `SELECT ... WHERE id = ?`
followed by `INSERT`, two concurrent resume attempts could both see
"no row" and both `INSERT`, and both would perform the side effect.

**Mitigation:** §4.1 mandates atomic `INSERT OR IGNORE` in a single
statement, wrapped in a `BeginTx` transaction that also runs the
follow-up SELECT (state classifier) and the audit row INSERT. The
caller uses the `INSERT OR IGNORE` rowcount + the SELECT's
`committed_at` NULL/NOT-NULL to derive `ReserveState`. Because the
SELECT runs inside the same transaction, no concurrent `Commit`
can slip between the two. Test coverage:
`TestReserve_AtomicUnderConcurrency` in `write_ids_writer_test.go`
spawns N goroutines each with a DISTINCT `WorkerID` all calling
`Reserve(sameID)`; asserts exactly one returns `ReserveFresh` and
every other returns `ReserveInFlight` (NOT `ReserveUncommitted` —
distinct live workers must never race each other into `WriteStep`).
Separate test `TestReserve_ExpiredLeaseCanBeStolenOnce` fast-forwards
the clock past the winner's lease, calls Reserve from a NEW worker,
and asserts exactly one caller now sees `ReserveUncommitted` (the
lease-stealer). `TestReserve_AllStatementsRollBackOnAuditFailure`
uses a partial-DB fault to prove the reservation and audit rows
land or fail as a unit.

### (c) SQL is fully parameterised

**Threat:** attacker-controlled fields (`conversation_id`, `step_id`,
`taskID`) flowing into a `fmt.Sprintf` SQL builder → SQL injection.

**Mitigation:** every statement in `write_ids_writer.go` is a
compile-time-constant `const … = ` string with `?` placeholders. Bound
values pass through `ExecContext`/`QueryRowContext` only. Test
coverage: `TestReserve_SQLMetaCharactersRoundTrip` reserves an id whose
`conversation_id = "'; DROP TABLE write_ids; --"` and asserts the row
is stored verbatim and the table is intact.

### (d) `task_id` regex enforcement

**Threat:** attacker passes a `task_id` containing SQL meta-characters
or path-traversal sequences (`../`, `\0`). Even with (c) parameterised,
downstream sinks (log lines, journal filenames, HTTP handlers) may
mishandle unusual bytes.

**Mitigation:** `ResumeTask` rejects any `taskID` not matching
`^[A-Za-z0-9_-]{8,128}$`. The regex is a `var` compiled once at package
init so a subsequent editor cannot loosen it accidentally without
breaking tests. Test coverage: table-driven
`TestResumeTask_InvalidTaskIDRejected` covers empty string, too-short,
too-long, SQL meta, path traversal, control chars, unicode.

### (e) Journal is untrusted; safety-critical decisions come from DB

**Threat:** the journal file is corrupted, truncated, or tampered
with. If we let the journal drive safety-critical decisions,
`ResumeTask` could re-drive a task from a wrong / partial history —
potentially triggering writes that were already committed by a
prior run, or missing writes that should have replayed.

**Mitigation:** §5.1 makes the observer DB the authoritative source
of every safety-critical fact:

- Reserve's fresh/uncommitted/committed decision reads ONLY
  `write_ids` (§4.1) — no filesystem access, no journal access.
- Payload byte-identity comes from `write_id_payloads` (§6.2a) —
  keyed by WriteID (content-derived).
- Contract body comes from `task_contracts` (§6.1b) — a
  pre-existing observer table.
- Run scoping comes from `write_id_reserve_events.run_id` and
  `resume_task_attempts.run_id` (§5.3) — populated in the same
  transactions as their state mutations.

`ResumeTask` does NOT read the journal (§6.1); candidate task_id
discovery is DB-only (§5.1 bulleted queries). A forged, mutated,
or missing journal cannot influence which task_ids the eval-runner
resumes, nor can it change any Reserve/Commit outcome.

Test coverage:
`TestResumeTask_ForgedJournalCannotCauseDuplicateWrite`
(inject synthetic bogus records; assert Reserve outcomes are
DB-driven);
`TestResumeTask_ForgedTerminalCannotSuppressResume`
(append forged `terminal:true` for a mid-flight task; assert
`ResumeTask(task)` still calls `Dispatch` and completes pending
writes because it never reads the journal);
`TestResumeTask_MissingJournalStillCompletes`
(delete the journal file entirely; `ResumeTask` succeeds from DB
state alone).

`ErrJournalChainBroken` is retained as a sentinel for a future
hardening worktree that DOES add per-record hash chaining; in this
worktree it is never raised in normal operation.

### (f) Retention policy for `write_ids` table

**Threat:** the table grows unboundedly across long-lived deployments,
eventually degrading `Reserve`'s index seek and consuming disk.

**Mitigation:** §5.2 defines a 30-day committed-row vacuum policy and
`Vacuum(cutoff)` API. The D3 eval-runner harness calls
`Vacuum(now.Add(-30*24*time.Hour))` at tear-down. Test coverage:
`TestVacuum_DropsCommittedRowsOlderThanCutoff` and
`TestVacuum_KeepsUncommittedRegardlessOfAge`.

### (g) Consumer-view reverse audit for `DuplicateSideEffectRate`

**Threat:** the D4 evaluator can't reconstruct the "duplicate reserve
count / total reserve count" ratio from `write_ids` + `runs` alone,
forcing us to add extra columns after code has shipped, OR relying on
lossy counter metrics that can't be attributed to a `run_id`.

**Mitigation:** §5.3 defines two append-only audit tables, both keyed
by `run_id`, populated inside the same transactions as their
respective state mutations: `write_id_reserve_events` (per-write —
one row per Reserve/Commit) drives `DuplicateSideEffectRate`;
`resume_task_attempts` (per-task — one row per ResumeTask exit)
drives `RecoverySuccessRate`. D4 reads each ratio from its
respective table with no join beyond `run_id`, no metric scraping,
no ambiguity about which run a given attempt belongs to. Test
coverage: `TestReserveEmitsAuditRowFresh`,
`TestReserveEmitsAuditRowUncommitted`,
`TestReserveEmitsAuditRowCommitted`, `TestCommitEmitsAuditRow`,
`TestReserveAndAuditRollBackTogetherOnDBError`,
`TestRecordResumeAttemptRowsShapeCorrect` in
`write_ids_writer_test.go`.

### (h) CI-appropriate performance assertions

**Threat:** perf assertions (e.g. "1000 `Reserve` calls in <1s") are
flaky under CI's shared runners.

**Mitigation:** any perf assertion is guarded by `if testing.Short()
|| os.Getenv("CI") != "" { t.Skip("perf assertion skipped in CI") }`.
The correctness tests (atomicity, idempotency, chain break, regex,
content-change) do NOT include time bounds and always run.

---

## 8. Metrics emitted

The primary evidence for D4 lives in the `write_id_reserve_events`
table (§5.3) — that is the audit trail Reserve/Commit write inside
their transactions. The counters below are best-effort operational
signals for live dashboards; D4 does NOT depend on them.

| Metric | Type | Labels | Emitted by | Consumer |
|---|---|---|---|---|
| `resume_write_id_reserve_total` | counter | `outcome={fresh,uncommitted,inflight,committed}` | `SQLiteWriteIDStore.Reserve` | live dashboard; D4 uses events table |
| `resume_write_id_commit_total` | counter | `outcome={applied,noop}` | `SQLiteWriteIDStore.Commit` | live dashboard |
| `resume_task_completed_total` | counter | `outcome={started,replayed,error}` | `driver.ResumeTask` | operational visibility |

Emit path: the writer packages accept an optional `Metrics interface{
IncCounter(name string, labels map[string]string) }` collaborator. The
interface has a no-op default so tests and Phase 2 harness don't need
a real Prometheus registry wired up.


---

## 9. Errors returned

| Error | Cause | Callers can act on it? |
|---|---|---|
| Error | Cause | Declared in | Callers can act on it? |
|---|---|---|---|
| `driver.ErrInvalidTaskID` | §7(d) regex mismatch inside `ResumeTask` | `driver/resume.go` | yes — user input rejection. Aliased to `observerstore.ErrInvalidTaskIDForm` (same underlying value) so `errors.Is(err, driver.ErrInvalidTaskID)` matches either. |
| `observerstore.ErrInvalidTaskIDForm` | `NewWriteID`'s taskID input fails regex | `observerstore/write_ids_writer.go` | yes — programmer bug, fix caller |
| `driver.ErrInvalidResumeDeps` | §6.1 any dep nil | `driver/resume.go` | yes — programmer bug |
| `driver.ErrContractNotFound` | §6.1b `LoadContract` returned no row for taskID | `driver/resume.go` | yes — rare append-order crash window |
| `observerstore.ErrJournalChainBroken` | Reserved sentinel; not raised in this worktree (§7(e)). Kept for future per-record chain hardening. | `observerstore/write_ids_writer.go` | yes — `driver` re-exports as `driver.ErrJournalChainBroken = observerstore.ErrJournalChainBroken` (plain `var` alias) so `errors.Is` works from either package. |
| `observerstore.ErrEmptyWriteIDComponent` | §3 any of five inputs empty | `observerstore/write_ids_writer.go` | yes — programmer bug, fix caller |
| `observerstore.ErrInvalidContentHash` | §3 contentHash fails `^[0-9a-f]{64}$` | `observerstore/write_ids_writer.go` | yes — programmer bug, fix caller |
| `observerstore.ErrEmptyReserveField` | §4 any ReserveRequest field empty | `observerstore/write_ids_writer.go` | yes — programmer bug, fix caller |
| `observerstore.ErrPayloadUnavailable` | §6.2a `PayloadStager.Load` found no row for id | `observerstore/write_ids_writer.go` (re-exported by `executor`) | yes — driver-side reconstructor supplies fallback bytes |
| `observerstore.ErrConcurrentLease` | §4 `Reserve` returned `ReserveInFlight` | `observerstore/write_ids_writer.go` | yes — back off then retry after lease TTL |
| `observerstore.ErrLeaseLost` | §4 `Commit` update matched zero rows because lease_owner != req.WorkerID (row still uncommitted) | `observerstore/write_ids_writer.go` | yes — the write happened but another worker won the lease |
| `observerstore.ErrNoReservation` | §4 `Commit` invoked for an id that was never Reserved OR was Vacuum'd before Commit ran | `observerstore/write_ids_writer.go` | yes — programmer bug or retention race |
| `observerstore.ErrInvalidLeaseTTL` | §4 `Reserve` called with `LeaseTTL <= 0` | `observerstore/write_ids_writer.go` | yes — programmer bug, fix caller |
| `observerstore.ErrLeaseLostAfterWrite` | §4 `Commit` invoked after another worker committed the same id (per the `write_id_reserve_events.worker_id` audit trail); this caller's local side-effects may have partially landed. Diagnostic-only — no automated cleanup path in this worktree. Re-exported as `executor.ErrLeaseLostAfterWrite`. | `observerstore/write_ids_writer.go` | yes — log + emit metric; automated rollback is a follow-up worktree |
| wrapped SQL errors | DB connection / statement failures | `observerstore/*` | yes — retry loop lives above `ResumeTask` |

Ownership follows the import graph: `driver` and `executor` both
import `observerstore`, so all types the store needs to return
(WriteID errors, Reserve errors, ChainBroken, PayloadUnavailable)
live in `observerstore`. Resume-orchestration errors (invalid task
id, invalid deps, contract-not-found) live with `driver`.

Cross-package `errors.Is` policy: any sentinel a caller might want
to check from either side of the boundary is declared once in the
leaf package (`observerstore`) and re-exported by the higher
package as a plain `var Alias = leaf.ErrX` (same value, so
`errors.Is` compares equal from either name). Every listed
sentinel is a package-level `var ErrXxx = errors.New(...)`, never a
struct or a wrapped-only value, so `errors.Is` works without unwrap
boilerplate. Test coverage in `driver/resume_test.go`:
`TestErrJournalChainBroken_IsFromBothPackages` — asserts
`errors.Is(err, driver.ErrJournalChainBroken)` AND
`errors.Is(err, observerstore.ErrJournalChainBroken)` both return
true for the same returned error value.

---

## 10. Acceptance criteria (mirrored in §7 plan test matrix)

1. **Driver-restart-analogue (primitive-level)**: with an
   in-memory SQLite store pre-seeded to reflect a mid-run
   crash — a `task_contracts` row + a `task_run_bindings` row
   + zero or partial `write_ids` rows — invoking `ResumeTask`
   with a fake `Dispatch` that calls into `ExecutorResume` on a
   fake `WriteStep` completes without duplicating any side
   effect: `write_ids.committed_at` rows equal the
   contract-declared step count exactly, and one
   `write_id_reserve_events` row per attempt is present with
   the expected `outcome`. The end-to-end real-driver-crash
   test lives in the downstream integration worktree.
   Test: `TestResumeTask_PrimitiveDriverRestartFixture`.
2. **Slave-disconnect scenario**: dropping the slave connection
   mid-`WriteStep` → the next resume observes `ReserveUncommitted`,
   re-issues the byte-identical write, commits, and the target path
   ends with exactly the contract-declared bytes (asserted by SHA-256
   equality against the intended payload).
3. **Duplicate-write prevention (concurrent live workers)**: two
   concurrent `ExecutorResume` invocations from DIFFERENT
   `WorkerID`s on the same WriteID produce exactly one
   `ReserveFresh` outcome (the winner) and every subsequent
   concurrent attempt returns `ReserveInFlight` for as long as the
   winner's lease is live. Exactly ONE `WriteStep` runs and the
   target path contains the winner's bytes; other workers back off
   without invoking `WriteStep`. After the lease expires WITHOUT a
   Commit (winner crash simulation), the next Reserve from ANY
   worker returns `ReserveUncommitted` and completes the write
   exactly once observably. Test:
   `TestReserve_ConcurrentLiveLeaseYieldsInflight`,
   `TestReserve_ExpiredLeaseCanBeStolenOnce`.
4. **Content-change positive**: rewriting the same path with NEW
   bytes yields a fresh WriteID (different `content_hash`) and the
   write proceeds — the old stuck row is not consulted (§7(a)).
5. **Journal state cannot influence `ResumeTask` (primitive
   level)**: `ResumeTask` never opens the journal file (§6.1
   step numbering has no `Journal.Recent` call). Test:
   `TestResumeTask_DoesNotOpenJournalFile` — inspects the fake
   `ResumeDeps` (which has no `Journal` field) and confirms the
   type does not compile with a `Journal` field. §7(e) trust
   boundary is enforced by omission at the API layer.
6. **`task_id` regex reject**: table-driven set of invalid ids all
   return `ErrInvalidTaskID` (§7(d)).
7. **Retention vacuum**: seeded old committed rows + new uncommitted
   rows → `Vacuum(cutoff)` removes the first set only (§7(f)).
8. **SQL injection round-trip**: hostile `conversation_id` stored
   verbatim, table intact (§7(c)).

D4 audit derivation:
- `DuplicateSideEffectRate` is computed from
  `write_id_reserve_events` scoped to `run_id` (§5.3, per-write).
- `RecoverySuccessRate` is computed from `resume_task_attempts`
  scoped to `run_id` (§5.3, per-task).
Both have well-defined non-zero denominators after (1)–(3) run and
require no join beyond `run_id`.

---

## 11. Out-of-scope for this worktree (tracked for later)

- Postgres `write_ids` DDL — mirror when a production-observer
  worktree is on deck.
- On-disk hash chain baked into `TaskJournal.Append` — would replace
  the sibling `.chain` file; separate worktree, must migrate existing
  journals.
- Multi-driver election / leases — a full HA story, not needed for
  eval-runner today.
- Automatic vacuum of reserved-but-uncommitted rows — operator-only
  because such rows are diagnostic signals.
- `duplicate_reserve_count` materialised column — add only if the D4
  extractor demonstrates the counter-based path is too expensive.
- **Retrofitting first-run write paths onto the resume gate** —
  the pre-existing slave write paths in `internal/executor/file.go`,
  `internal/executor/bash.go`, and other tools that write directly
  to the filesystem or the observer's `writes` table without going
  through `ExecutorResume` remain outside this worktree's resume
  guarantee. A follow-up worktree (tentative name
  `wt2-executor-writes-through-gate`) is required to widen the
  safety guarantee to those paths. Documented explicitly so that
  a reviewer of Phase 2 D3 wiring can see what is not yet covered.
- Multi-run driver invocation of the same task_id — the current
  `task_run_bindings` PK is `(run_id, task_id)`, so re-running the
  same task_id under a new run_id is silently allowed and produces
  a fresh binding row. This is intentional (an operator retry
  gets a fresh run_id) but a cross-run duplicate-write audit is
  out of scope for D4.
- Postgres schema parity for the new tables (`write_ids`,
  `write_id_reserve_events`, `resume_task_attempts`,
  `write_id_payloads`, `task_run_bindings`) — mirror when the
  production-observer worktree lands.
