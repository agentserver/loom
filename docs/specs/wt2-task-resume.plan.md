# WT-2-task-resume — Plan

> Companion to `docs/specs/wt2-task-resume.spec.md` (Stage 1 CLEAN).
> Branch: `paper/v3/p2-task-resume`.
> Base: `origin/paper/v3-integration` HEAD `d053897`.
>
> **Scope reminder** (spec §1.4): this worktree delivers the resume
> **primitives** in 5 files. It does NOT modify existing driver /
> executor code paths. End-to-end wiring is scoped to the follow-up
> worktrees `wt2-contract-tools-run-binding` and
> `wt2-executor-writes-through-gate`.

---

## 1. TDD ordering

Every code file has a corresponding `_test.go` written FIRST. The
red-green-refactor cycle per file:

1. Write the test file. Compile FAILS with `undefined: <Symbol>`
   for every symbol the test references — that's the intended red
   state. (Optionally: add empty stubs of the required types/funcs
   in the production file so `go build` passes and `go test` fails
   on assertions instead; either red form is acceptable, pick the
   one the reviewer prefers per unit.)
2. Add the minimum code to make each test pass.
3. Refactor while keeping green.
4. Repeat until §10 spec acceptance is fully covered.

Files land in the following order (each is a coherent unit of work
that can be reviewed independently):

1. `multi-agent/internal/observerstore/schema.sql` — DDL append.
2. `multi-agent/internal/observerstore/write_ids_writer.go` + `_test.go` —
   primitives.
3. `multi-agent/internal/executor/resume.go` + `_test.go` — executor pipeline.
4. `multi-agent/internal/driver/resume.go` + `_test.go` — driver dispatch shell.

Each subsequent file consumes only symbols from earlier ones, so
`cd multi-agent && go test ./internal/observerstore/... -race`
should be green before starting file 3, and
`cd multi-agent && go test ./internal/observerstore/...
./internal/executor/... -race` should be green before starting
file 4.

---

## 2. File 1: `multi-agent/internal/observerstore/schema.sql`

**Change type:** APPEND ONLY at end of file (spec §1 file table).

**Blocks to append** (spec §5, verbatim — the plan does NOT
paraphrase, the implementation must copy the DDL from the spec
character-for-character):

- `write_ids` table (8 columns: id, task_id, conversation_id,
  step_id, reserved_at, committed_at, lease_owner,
  lease_expires_at). PRIMARY KEY (id). `lease_owner TEXT NOT NULL
  DEFAULT ''`, `lease_expires_at TEXT` (nullable).
- Index `idx_write_ids_conv_step` on (conversation_id, step_id).
- Index `idx_write_ids_task_id` on (task_id).
- Index `idx_write_ids_committed_at` on (committed_at).
- `write_id_reserve_events` table (6 columns: event_id, id, run_id,
  task_id, outcome, occurred_at). PRIMARY KEY (event_id). CHECK
  constraint on
  `outcome IN ('fresh','uncommitted','inflight','committed','commit')`.
  `event_id = hex(sha256(id || occurred_at || outcome))` per spec
  §5.3, computed at insert time.
- Index `idx_write_id_reserve_events_run` on (run_id, occurred_at).
- Index `idx_write_id_reserve_events_id` on (id, occurred_at).
- `resume_task_attempts` table (6 columns: run_id, task_id,
  outcome, error_kind, started_at, updated_at). PRIMARY KEY
  (run_id, task_id). CHECK constraint on
  `outcome IN ('started','replayed','error')`.
- Index `idx_resume_task_attempts_run` on (run_id, updated_at).
- `task_run_bindings` table (3 columns + PK).
- Index `idx_task_run_bindings_run` on (run_id, bound_at).
- `write_id_payloads` table (6 columns + PK). PRIMARY KEY (id).
- Index `idx_write_id_payloads_task_step` on (task_id, step_id,
  staged_at).

**Verification:** `OpenSQLite` runs the DDL via `db.Exec(schemaSQL)`
at `store.go:58`. `TestOpenSQLite_LoadsNewTables` (plan-only) in
`write_ids_writer_test.go` opens a fresh DB and asserts every new
table + index name is present in `sqlite_master`.

---

## 3. File 2: `multi-agent/internal/observerstore/write_ids_writer.go`

**Change type:** NEW.

**Symbols to declare (in order):**

1. Package-level `contentHashPattern` regex + `taskIDPattern` regex.
2. Sentinel errors: `ErrEmptyWriteIDComponent`,
   `ErrInvalidContentHash`, `ErrInvalidTaskIDForm`,
   `ErrEmptyReserveField`, `ErrPayloadUnavailable`,
   `ErrConcurrentLease`, `ErrLeaseLost`, `ErrJournalChainBroken`
   (retained for future hardening; not raised in this worktree).
3. `WriteID` type (`type WriteID string`).
4. `NewWriteID(taskID, conversationID, stepID, targetPath,
   contentHash string) (WriteID, error)` — the 5-input length-prefixed
   sha256 derivation.
5. `ReserveState` (iota: `ReserveFresh`, `ReserveUncommitted`,
   `ReserveInFlight`, `ReserveCommitted`).
6. Value types: `ReserveRequest`, `CommitRequest`, `PayloadRef`.
7. Interface types: `WriteIDStore`, `PayloadStager`.
8. `SQLiteWriteIDStore` struct + constructor + method implementations
   (`Reserve`, `Commit`, `Vacuum`, `RecordResumeAttempt`).
9. `SQLitePayloadStager` struct + constructor + method
   implementations (`Stage`, `Load`, `ListForStep`).
10. `Metrics` interface (spec §8) with a single method
    `IncCounter(name string, labels map[string]string)`; a
    package-level `nopMetrics` type implements it as a no-op.
    Both `SQLiteWriteIDStore` and `SQLitePayloadStager` accept an
    optional `Metrics` via `WithMetrics(m Metrics)` constructor
    option; default is `nopMetrics`. Reserve emits
    `resume_write_id_reserve_total{outcome=...}`; Commit emits
    `resume_write_id_commit_total{outcome=applied|noop}`;
    RecordResumeAttempt emits
    `resume_task_completed_total{outcome=...}`.

**Test matrix (in `write_ids_writer_test.go`)** — one test per
bullet. Every bullet corresponds to a spec citation. Bullets
without the `(plan-only)` marker are named exactly as they appear
in spec §7 / §10; bullets marked `(plan-only)` are additional
coverage the plan adds to size TDD rungs and are NOT required by
the spec. If any spec-listed name has been renamed here, that
rename is a plan bug — flag it.

### 3.1 WriteID derivation

- `TestNewWriteID_HappyPath` (plan-only) — five valid inputs (taskID satisfying
  `^[A-Za-z0-9_-]{8,128}$`) produce a deterministic hex sha256
  matching a hand-computed reference value. Fixture:
  `taskID = "task_ab12"` (9 chars), `conversationID = "conv-x"`,
  `stepID = "write-0-foo"`, `targetPath = "artifact:foo"`,
  `contentHash =
  "0000000000000000000000000000000000000000000000000000000000000000"`.
  Reference bytes (uvarint = LEB128 of len):
  `raw = 0x09 || "task_ab12" || 0x06 || "conv-x" || 0x0b ||
  "write-0-foo" || 0x0c || "artifact:foo" || 0x40 || contentHash`.
  The test computes `raw` in code (not by hand), then compares
  `NewWriteID`'s output against `hex.EncodeToString(sha256.Sum256(raw)[:])`
  from that same computation — this guards the derivation formula
  itself, not a magic constant.
- `TestNewWriteID_ContentChangeYieldsDistinctID` — same
  taskID/convID/stepID/targetPath with two different contentHashes
  produces two distinct WriteIDs (spec §7(a)).
- `TestNewWriteID_TaskIDChangeYieldsDistinctID` (plan-only) — same everything
  else, different taskID → distinct WriteIDs (spec §3 collision-avoid).
- `TestNewWriteID_EmptyContentHashRejected` — `""` →
  `ErrEmptyWriteIDComponent`.
- `TestNewWriteID_PlaceholderContentHashRejected` — `"placeholder"` →
  `ErrInvalidContentHash`.
- `TestNewWriteID_ShortContentHashRejected` — 63 hex chars →
  `ErrInvalidContentHash`.
- `TestNewWriteID_UppercaseContentHashRejected` — 64 UPPERCASE hex
  chars → `ErrInvalidContentHash`.
- `TestNewWriteID_NonHexContentHashRejected` — 64 chars containing
  `'g'` → `ErrInvalidContentHash`.
- `TestNewWriteID_EmptyTaskIDRejected` (plan-only) — `""` →
  `ErrEmptyWriteIDComponent` (or `ErrInvalidTaskIDForm`; deterministic
  which; spec §3 lists both, we pick empty-component to keep the empty
  check unified).
- `TestNewWriteID_InvalidTaskIDFormRejected` (plan-only) — table-driven set of
  taskIDs failing `^[A-Za-z0-9_-]{8,128}$` (SQL meta, path traversal,
  control chars, unicode, too-short, too-long) → `ErrInvalidTaskIDForm`.
- `TestNewWriteID_LengthPrefixInjectivityRegression` (plan-only) — asserts
  the length-prefix scheme is injective on the LATTER four inputs
  (taskID stays fixed at a valid `"task_ab12"` for both cases,
  since the regex constrains it): `("task_ab12","aa","bb","cc",
  contentHash)` vs `("task_ab12","aab","b","cc",contentHash)`
  yield distinct WriteIDs even though naïve concatenation without
  length prefix would collide.

### 3.2 Reserve state machine + audit rows

Setup helper: `openStoreWithRun(t)` returns a fresh in-memory SQLite
+ helpers for driving Reserve/Commit and reading back the audit
rows. `t.TempDir` file path per test.

- `TestReserve_FreshOnFirstCall` — one Reserve → `ReserveFresh` +
  one `write_id_reserve_events` row with `outcome='fresh'` +
  matching `run_id`.
- `TestReserve_UncommittedAfterCrash` — Reserve, wait past lease
  TTL, Reserve again with SAME id + DIFFERENT WorkerID →
  `ReserveUncommitted` + audit `outcome='uncommitted'`.
- `TestReserve_UncommittedForSameWorkerBeforeTTL` (plan-only) — Reserve, then
  Reserve again with SAME id + SAME WorkerID inside lease TTL →
  `ReserveUncommitted` (spec §4.1 classifier case 4).
- `TestReserve_InflightForForeignLive` (plan-only) — Reserve with worker A,
  then Reserve same id with worker B while A's lease is live →
  `ReserveInFlight` + audit `outcome='inflight'`.
- `TestReserve_CommittedAfterHappyPath` — Reserve + Commit + Reserve
  again → `ReserveCommitted` + audit `outcome='committed'`.
- `TestReserve_AtomicUnderConcurrency` — spawn N goroutines each
  with a DISTINCT WorkerID all calling `Reserve(sameID)`. Assert
  exactly one returns `ReserveFresh` and every other returns
  `ReserveInFlight`. Assert the correct number of audit rows land
  with correct outcomes.
- `TestReserve_ExpiredLeaseCanBeStolenOnce` — same as previous but
  fast-forwards the clock past the winner's lease before the second
  wave; assert exactly one caller sees `ReserveUncommitted` and the
  rest see `ReserveInFlight` (once the stealer's new lease is live).
- `TestReserve_AllStatementsRollBackOnAuditFailure` — use a
  driver-level fault injection (a `sql.DB` wrapper that fails the
  audit INSERT) to prove the transaction rolls back: no `write_ids`
  row and no `write_id_reserve_events` row after the failure.
- `TestReserve_SQLMetaCharactersRoundTrip` — `conversationID = "';
  DROP TABLE write_ids; --"`; assert row is stored verbatim and
  table survives (spec §7(c)).
- `TestReserve_EmitsAuditRowForInflight` — dedicated single-goroutine
  variant of the inflight test to isolate the audit-emit branch.
- `TestReserve_InflightRowsCounted_ByDenominator` — after seeding
  a run with 1 fresh + 1 inflight + 1 uncommitted + 1 commit, run
  the D4 SQL (spec §5.3) and assert numerator = 1, denominator = 3
  (fresh + inflight + uncommitted, not commit), ratio = 1/3.
- `TestReserve_ConcurrentLiveLeaseYieldsInflight` — spec §10
  criterion 3. Two goroutines with DISTINCT WorkerIDs Reserve the
  same id concurrently while the first's lease is live. Assert
  exactly one returns `ReserveFresh` and the other returns
  `ReserveInFlight`.
- `TestReserveEmitsAuditRowFresh` — spec §7(g). One Reserve, first
  attempt; assert exactly one `write_id_reserve_events` row with
  `outcome='fresh'`.
- `TestReserveEmitsAuditRowUncommitted` — spec §7(g). Reserve,
  wait past lease TTL, Reserve again from a NEW WorkerID; assert
  one new row with `outcome='uncommitted'`.
- `TestReserveEmitsAuditRowCommitted` — spec §7(g). Reserve +
  Commit + Reserve again; the third Reserve emits a row with
  `outcome='committed'`.
- `TestCommitEmitsAuditRow` — spec §7(g). Commit emits one
  `write_id_reserve_events` row with `outcome='commit'`.
- `TestReserve_EventIDDeterministicallyDerived` (plan-only) —
  spec §5.3. Reserve; read back the audit row; assert
  `event_id == hex(sha256(id || occurred_at || outcome))`. Then
  attempt a second insert with the SAME (id, occurred_at, outcome)
  tuple manually and assert the PRIMARY KEY constraint rejects it,
  proving the id is deterministic AND uniqueness-enforcing.
- `TestReserveAndAuditRollBackTogetherOnDBError` — spec §7(g).
  With a driver-level fault injection failing the audit INSERT
  inside Reserve's transaction, assert no `write_ids` row and no
  audit row survive.

### 3.3 Commit

- `TestCommit_MarksRowAndEmitsAuditRow` (plan-only) — happy path.
- `TestCommit_IdempotentOnSecondCall` (plan-only) — Commit twice, second is a
  no-op AND emits no second audit row (RowsAffected=0 branch).
- `TestCommit_ErrLeaseLost` (plan-only) — Commit with a WorkerID different from
  the one that Reserved → `ErrLeaseLost`, no committed_at update,
  no audit row.

### 3.4 Vacuum + retention

- `TestVacuum_DropsCommittedRowsOlderThanCutoff` — seed one row
  committed 31 days ago + one committed 1 day ago; Vacuum with
  `now - 30d` cutoff removes exactly one.
- `TestVacuum_KeepsUncommittedRegardlessOfAge` — seed a
  reserved-but-never-committed row aged 60 days; Vacuum removes 0
  rows.
- `TestVacuum_ReturnsRowsAffectedCount` (plan-only) — asserts the int64 return
  value matches the deletion count.

### 3.5 RecordResumeAttempt

- `TestRecordResumeAttempt_InitialStartedRow` (plan-only) — outcome='started'
  INSERT lands a row.
- `TestRecordResumeAttempt_TransitionToReplayed` (plan-only) — a subsequent
  outcome='replayed' UPDATE keeps started_at, refreshes updated_at,
  sets outcome.
- `TestRecordResumeAttempt_TransitionToError` (plan-only) — outcome='error' +
  error_kind populated.
- `TestRecordResumeAttempt_ReinvocationResetsOutcomeToStarted` (plan-only) —
  a second UPSERT with 'started' after a previous 'replayed' does
  overwrite (spec §5.3 UPSERT semantics — driver restart mid-resume).
- `TestRecordResumeAttempt_PrimaryKeyPreventsDuplicates` (plan-only) — two
  concurrent inserts with same `(run_id, task_id)` yield exactly
  one row.
- `TestRecordResumeAttemptRowsShapeCorrect` — spec §7(g). Insert
  one row via `RecordResumeAttempt("started", "")` then update via
  `RecordResumeAttempt("replayed", "")`; assert the persisted row
  has the exact column set/values declared in schema.sql (6 cols,
  correct outcome + timestamps).

### 3.6 PayloadStager

- `TestStage_HappyPath` (plan-only) — Stage a row, `Load(id)` returns byte-equal
  payload.
- `TestStage_IdempotentSameID` (plan-only) — Stage twice with same id + same
  bytes → 1 row (INSERT OR IGNORE).
- `TestStage_ContentChangeUnderSameStepIDCoexist` (plan-only) — Stage two rows
  under same (taskID, stepID) with two different WriteIDs (different
  payload sha256); both persist; `ListForStep` returns both in
  reverse-chronological order.
- `TestLoad_ReturnsErrPayloadUnavailableForUnknownID` (plan-only) — Load with
  a fresh id → `ErrPayloadUnavailable`.
- `TestListForStep_ReversedByStagedAt` (plan-only) — assert most-recent-first
  order.
- `TestListForStep_PopulatesCommittedAt` (plan-only) — Stage two rows for the
  same (taskID, stepID); commit one; ListForStep returns both, with
  the committed one carrying non-nil CommittedAt.
- `TestListForStep_EmptyReturnsNil` (plan-only) — no rows → `nil, nil`.

### 3.7 Retention hits payload table

- `TestVacuum_PayloadsCleanedForOldCommits` (plan-only) — Stage + Reserve +
  Commit + backdate; Vacuum removes both the write_id row and the
  write_id_payload row via §6.2a SQL.

### 3.8 Metrics collaborator

- `TestMetrics_ReserveEmitsCounters` (plan-only) — inject a recording Metrics
  double; assert one increment per Reserve with correct
  `outcome` label.
- `TestMetrics_CommitEmitsCounters` (plan-only) — assert Commit emits
  `resume_write_id_commit_total{outcome=applied}` on happy path,
  `outcome=noop` on second (idempotent) call.
- `TestMetrics_NopDefaultDoesNotPanic` (plan-only) — construct store without
  `WithMetrics`; run Reserve/Commit; no panic.

### 3.9 Perf assertion (CI-conditional)

- `TestReserve_ThroughputSmoke` (plan-only) — 1000 Reserves in <1s;
  `if testing.Short() || os.Getenv("CI") != "" { t.Skip("perf") }`
  guard (spec §7(h)).

---

## 4. File 3: `multi-agent/internal/executor/resume.go`

**Change type:** NEW. Consumes symbols from `internal/observerstore`
via normal import (no cycle; verified at spec §1.3).

**Symbols to declare:**

1. `Step` struct.
2. `ResumeRequest` struct.
3. `ExecutorDeps` struct (including `WriteStep`, `FreshExecute`,
   `WorkerID`, `LeaseTTL`, `Store`, `PayloadStager`).
4. `ExecutorResume(ctx, deps, req) error`.
5. Error alias: `var ErrPayloadUnavailable = observerstore.ErrPayloadUnavailable`
   so callers of `ExecutorResume` can use
   `errors.Is(err, executor.ErrPayloadUnavailable)` without importing
   `observerstore` themselves.
6. `snapshotJournal` helper — REMOVED (no journal I/O in this
   worktree per spec §5.1); do not add.

**Test matrix (in `resume_test.go`)**:

- `TestExecutorResume_HappyPathFresh` (plan-only) — one Step, no prior stage,
  ReserveFresh → WriteStep runs, Commit lands, target file has
  intended bytes.
- `TestExecutorResume_CrashAfterReserveReplaysFromStagedPayload` —
  spec §6.2a1 named test.
- `TestExecutorResume_ContentChangeYieldsFreshWriteID` — spec
  §6.2a1 named test.
- `TestExecutorResume_SkipsWhenReserveCommitted` (plan-only) — pre-seed a
  committed WriteID; ExecutorResume for the same Step doesn't call
  WriteStep.
- `TestExecutorResume_ReserveInFlightReturnsErrConcurrentLease` (plan-only) —
  pre-seed a live foreign lease; ExecutorResume errors with
  `ErrConcurrentLease`; WriteStep is not called.
- `TestExecutorResume_NilPayloadFallsThroughToFreshExecution` —
  spec §6.2 step 1a; a Step with `Payload=nil` invokes
  `deps.FreshExecute` and returns its result.
- `TestExecutorResume_ContentHashComputedBeforeStage` (plan-only) — hook
  Stage's WriteID input; assert it matches sha256(step.Payload).
- `TestExecutorResume_MultipleStepsOrdered` (plan-only) — 3 Steps in order;
  assert WriteStep called in Step.Index order.
- `TestExecutorResume_EmptyStepsReturnsNil` (plan-only) — no Steps → nil,
  no side effects.
- `TestFirstRunWriteGoesThroughGate` — spec §6.1c named test;
  covers the primitive-level first-run gating claim.
- `TestExecutorResume_ContentChangeRewrites` — spec §7(a) named
  test. First iteration: Step.Payload=A, WriteStep runs and
  commits under WriteID_A. Second iteration (fresh
  ExecutorResume): same taskID/stepID/target but
  Step.Payload=B → new contentHash → new WriteID_B; assert
  WriteStep runs again, WriteID_B is stored fresh, WriteID_A is
  untouched (no re-issue).
- `TestExecutorResume_ConcurrentLiveWorkersRunWriteStepOnce` (plan-only) —
  spec §10 acceptance criterion 3, expanded from the spec's
  primitive-level statement to an end-to-end executor-level
  assertion. TWO sub-scenarios (each a
  subtest via `t.Run`) so committed-vs-crash cases are separate:

  (a) `committed_wins` — spawn 2 goroutines both calling
  `ExecutorResume` for the same Step (same TaskID + same
  ConversationID + same StepID + byte-identical Payload) with
  DISTINCT `deps.WorkerID`. WriteStep is a slow-but-successful
  closure (uses a channel to serialise); the winner's Commit lands
  before the lease expires. Assert `WriteStep` invoked EXACTLY
  ONCE; loser returns `ErrConcurrentLease`. AFTER both goroutines
  return, invoke `ExecutorResume` a THIRD time from a fresh worker
  — assert Reserve returns `ReserveCommitted` and WriteStep does
  NOT run.

  (b) `winner_crashed_before_commit` — spawn 1 goroutine calling
  `ExecutorResume` where WriteStep BLOCKS forever (simulates
  crash); after Reserve is issued but before Commit, cancel the
  ctx / kill the goroutine. Fast-forward the clock past the
  winner's lease TTL. From a fresh worker, invoke `ExecutorResume`
  with byte-identical Step; assert Reserve returns
  `ReserveUncommitted`, WriteStep runs, Commit lands. Overall
  count of WriteStep invocations across scenario (b) = 2 (the
  crashed + the recovery). That is the correct behaviour — the
  crash meant the first write did NOT durably complete, so the
  retry is not a duplicate.

---

## 5. File 4: `multi-agent/internal/driver/resume.go`

**Change type:** NEW. Consumes `multi-agent/internal/contract` +
`multi-agent/internal/observerstore` + `multi-agent/internal/executor`
(the last is a NEW one-way import: `driver → executor`, needed
because `ReconstructSteps` returns `[]executor.Step`; verified no
cycle since `executor` does not import `driver`).

**Symbols to declare:**

1. `var taskIDPattern = regexp.MustCompile(...)`.
2. Sentinel errors (spec §9 error table):
   - `ErrInvalidResumeDeps` — declared here as new
     `var ErrInvalidResumeDeps = errors.New(...)`.
   - `ErrContractNotFound` — declared here as new
     `var ErrContractNotFound = errors.New(...)`.
   - `ErrInvalidTaskID` — declared here as an ALIAS to the
     observerstore sentinel:
     `var ErrInvalidTaskID = observerstore.ErrInvalidTaskIDForm`.
     Same underlying value → `errors.Is(err, driver.ErrInvalidTaskID)`
     and `errors.Is(err, observerstore.ErrInvalidTaskIDForm)` both
     succeed.
   - `ErrJournalChainBroken` — declared here as an ALIAS:
     `var ErrJournalChainBroken = observerstore.ErrJournalChainBroken`.
   The `ResumeTask` regex check calls `taskIDPattern.MatchString`
   and returns `ErrInvalidTaskID` (the alias) on failure — the
   observerstore's `NewWriteID` also returns that same underlying
   value from its own regex check, so callers matching on either
   name get the same behaviour.
3. `ResumeDeps` struct.
4. `ResumeTask(ctx, deps, runID, taskID)` — spec §6.1 numbered flow.
5. `ReconstructSteps(ctx, stager observerstore.PayloadStager,
   taskID string, c contract.TaskContract) ([]executor.Step, error)`
   helper — spec §6.2a driver-side payload recovery. Takes the
   `PayloadStager` directly (NOT `ResumeDeps`) so tests can drive
   it without wiring a full Dispatch/LoadContract pair. Iterates
   `c.DataContract.WriteTargets`; for each target computes
   `stepID = fmt.Sprintf("write-%d-%s", i, target.Name)`, calls
   `stager.ListForStep(ctx, taskID, stepID)`, applies the
   committed-wins/uncommitted/empty classifier from spec §6.2a
   steps 2–4, and returns the assembled `[]executor.Step`.
6. `classifyErr(err) string` — maps typed errors to the audit
   `error_kind` label.

**Test matrix (in `resume_test.go`)**:

- `TestResumeTask_InvalidTaskIDRejected` — table-driven regex
  negative test (spec §7(d)).
- `TestResumeTask_InvalidResumeDepsRejected` (plan-only) — every nil-field
  variant.
- `TestResumeTask_DoesNotOpenJournalFile` — asserts `ResumeDeps`
  does not include a `Journal` field via reflection; asserts a
  compile-time check `var _ = ResumeDeps{Store: nil, LoadContract: nil,
  Dispatch: nil}` compiles (spec §10 criterion 5).
- `TestResumeTask_StartedRowInsertedBeforeDispatch` — Dispatch is
  a spy; the audit row is present with outcome='started' when
  Dispatch runs.
- `TestResumeTask_ReplayedRowAfterSuccess` — Dispatch returns nil;
  audit row upserts to 'replayed'.
- `TestResumeTask_ErrorRowOnContractMissing` — LoadContract returns
  `ErrContractNotFound`; audit row 'error', error_kind='ErrContractNotFound'.
- `TestResumeTask_ErrorRowOnDispatchFail` — Dispatch returns a
  fresh error; audit row 'error', error_kind=classified name.
- `TestResumeTask_CrashAfterDispatch_LeavesStartedRow` — simulate
  by returning early after Dispatch spy call BEFORE the post-dispatch
  audit UPDATE; assert audit row is still 'started'.
- `TestResumeTask_ReinvocationUpdatesRowInPlace` — call ResumeTask
  twice for the same (runID, taskID); assert exactly one row exists
  (PK enforcement).
- `TestResumeTask_PrimitiveDriverRestartFixture` — spec §10
  criterion 1 primitive-level.
- `TestErrJournalChainBroken_IsFromBothPackages` — asserts
  `errors.Is(err, driver.ErrJournalChainBroken)` AND
  `errors.Is(err, observerstore.ErrJournalChainBroken)` both return
  true for the same value.
- `TestReconstructSteps_CommittedWinsPriority` (plan-only) — seed a step with
  both a committed staged row AND a newer uncommitted staged row;
  assert ReconstructSteps returns the COMMITTED bytes (spec §6.2a
  step 2 committed-wins).
- `TestReconstructSteps_UncommittedUsedWhenNoCommitted` (plan-only) — only
  uncommitted rows exist → most recent uncommitted bytes.
- `TestReconstructSteps_UnstagedStepYieldsNilPayload` — no rows for
  the step → Step.Payload is nil (spec §6.2a step 4).
- `TestResumeTask_ContractNotFound_ErrorOutcome` — spec §6.1b
  named test. Fake `LoadContract` returns
  `driver.ErrContractNotFound`; assert ResumeTask returns the
  wrapped error, `resume_task_attempts` row has
  `outcome='error'`, `error_kind='ErrContractNotFound'`.
- `TestResumeTask_ForgedTerminalStillDispatches` — spec §7(e)
  regression. Even with a synthetic `terminal:true` record in the
  journal, ResumeTask still calls Dispatch (asserted via a spy
  Dispatch fn's call count).
- `TestResumeTask_ForgedJournalCannotCauseDuplicateWrite` — spec
  §7(e). Wire ResumeTask with a real `ExecutorResume` chain over
  an in-memory store pre-seeded with a committed WriteID. Simulate
  a forged journal by writing arbitrary bytes to a `TaskJournal`
  file that lives on disk but is NEVER passed to ResumeTask.
  Assert: (a) ResumeTask completes without error; (b) no
  duplicate `write_ids` row appears; (c) `write_id_reserve_events`
  for the pre-committed WriteID reports `outcome='committed'`.
- `TestResumeTask_ForgedTerminalCannotSuppressResume` — spec
  §7(e). Same setup as above but the pre-seeded WriteID is
  UNCOMMITTED. Even if the journal contains synthetic
  `terminal:true` records, ResumeTask still calls Dispatch and
  the executor commits the pending write. Assert Dispatch spy was
  invoked exactly once.
- `TestResumeTask_MissingJournalStillCompletes` — spec §7(e).
  Delete the journal file entirely (rm the path). Assert
  ResumeTask still succeeds because it never reads the file.

---

## 6. Cross-cutting checks

### 6.1 Non-modification proof

Before merging, run:

```bash
git diff --stat origin/paper/v3-integration...HEAD
```

Assert the diff touches ONLY the 7 in-scope files below, plus the
two docs files (spec + plan):

Production files:
- `multi-agent/internal/observerstore/schema.sql`
- `multi-agent/internal/observerstore/write_ids_writer.go`
- `multi-agent/internal/executor/resume.go`
- `multi-agent/internal/driver/resume.go`

Test files:
- `multi-agent/internal/observerstore/write_ids_writer_test.go`
- `multi-agent/internal/executor/resume_test.go`
- `multi-agent/internal/driver/resume_test.go`

Docs:
- `docs/specs/wt2-task-resume.spec.md`
- `docs/specs/wt2-task-resume.plan.md`

Any change to `task_journal.go`, `contract_tools.go`,
`observer_relay.go`, `humanloop/*`, `contract/*`, or any file
outside the list above fails review.

### 6.2 Import-cycle safety

`cd multi-agent && go build ./...` from the module root must succeed.
The new files' import ordering:
- `internal/observerstore/write_ids_writer.go` — imports stdlib only
  (`database/sql`, `crypto/sha256`, `encoding/hex`,
  `encoding/binary`, `errors`, `fmt`, `regexp`, `time`, `context`).
- `internal/executor/resume.go` — imports
  `github.com/yourorg/multi-agent/internal/observerstore`,
  `github.com/yourorg/multi-agent/internal/contract`.
- `internal/driver/resume.go` — imports
  `github.com/yourorg/multi-agent/internal/observerstore`,
  `github.com/yourorg/multi-agent/internal/contract`,
  `github.com/yourorg/multi-agent/internal/executor` (one-way, for
  `[]executor.Step` return type of `ReconstructSteps`).

`internal/executor` does NOT import `internal/driver`
(pre-existing invariant, preserved). The new `driver → executor`
edge is safe because `executor` sits BELOW `driver` in the DAG.

### 6.3 Race + shuffle

Full test command per spec §3 acceptance:

```bash
cd multi-agent
go test ./internal/driver/... ./internal/executor/... \
        ./internal/observerstore/... -count=1 -shuffle=on -race
go vet ./...
```

Both must return exit 0.

### 6.4 Test-matrix ↔ security-section coverage

Every security item (a)–(h) in spec §7 has ≥1 named test above:

| Security item | Named test(s) |
|---|---|
| (a) content_hash required | `TestNewWriteID_ContentChangeYieldsDistinctID`, `TestNewWriteID_EmptyContentHashRejected`, `TestNewWriteID_PlaceholderContentHashRejected`, `TestNewWriteID_ShortContentHashRejected`, `TestNewWriteID_UppercaseContentHashRejected`, `TestNewWriteID_NonHexContentHashRejected`, `TestExecutorResume_ContentChangeYieldsFreshWriteID` |
| (b) atomic INSERT OR IGNORE | `TestReserve_AtomicUnderConcurrency`, `TestReserve_ExpiredLeaseCanBeStolenOnce`, `TestReserve_AllStatementsRollBackOnAuditFailure`, `TestReserve_InflightForForeignLive` |
| (c) SQL parameterisation | `TestReserve_SQLMetaCharactersRoundTrip` |
| (d) task_id regex | `TestResumeTask_InvalidTaskIDRejected`, `TestNewWriteID_InvalidTaskIDFormRejected` |
| (e) journal untrusted / chain-break sentinel retained | `TestResumeTask_DoesNotOpenJournalFile`, `TestErrJournalChainBroken_IsFromBothPackages`, `TestResumeTask_ForgedJournalCannotCauseDuplicateWrite`, `TestResumeTask_ForgedTerminalCannotSuppressResume`, `TestResumeTask_MissingJournalStillCompletes` |
| (f) retention vacuum | `TestVacuum_DropsCommittedRowsOlderThanCutoff`, `TestVacuum_KeepsUncommittedRegardlessOfAge`, `TestVacuum_PayloadsCleanedForOldCommits` |
| (g) D4 audit derivation | `TestRecordResumeAttempt_*`, `TestReserve_InflightRowsCounted_ByDenominator`, `TestReserve_EmitsAuditRowForInflight` |
| (h) CI-guarded perf | `TestReserve_ThroughputSmoke` |

Every spec §10 acceptance criterion has ≥1 named test:

| Acceptance | Named test |
|---|---|
| 1 driver-restart primitive fixture | `TestResumeTask_PrimitiveDriverRestartFixture` |
| 2 slave-disconnect | `TestExecutorResume_CrashAfterReserveReplaysFromStagedPayload` |
| 3 concurrent live workers | `TestExecutorResume_ConcurrentLiveWorkersRunWriteStepOnce` (end-to-end WriteStep-once assertion) + `TestReserve_AtomicUnderConcurrency` (store-level Reserve/InFlight assertion) |
| 4 content-change | `TestExecutorResume_ContentChangeYieldsFreshWriteID`, `TestStage_ContentChangeUnderSameStepIDCoexist` |
| 5 journal cannot influence ResumeTask | `TestResumeTask_DoesNotOpenJournalFile` |
| 6 task_id regex reject | `TestResumeTask_InvalidTaskIDRejected` |
| 7 retention vacuum | `TestVacuum_DropsCommittedRowsOlderThanCutoff`, `TestVacuum_KeepsUncommittedRegardlessOfAge` |
| 8 SQL injection round-trip | `TestReserve_SQLMetaCharactersRoundTrip` |

---

## 7. Rollback plan

Nothing in this worktree modifies existing tables or in-flight code
paths. Rollback = `git revert` the merge; the 5 new tables become
unused but do not break existing observer schema (they only add
CREATE TABLE IF NOT EXISTS statements, so re-applying is idempotent
and dropping them is `DROP TABLE IF EXISTS write_ids;` etc.).

## 8. Commit trailer

Every commit ends with:

```
Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
```

Do NOT push (spec explicit constraint).
