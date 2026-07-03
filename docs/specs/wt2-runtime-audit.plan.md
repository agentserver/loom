# WT-2-runtime-audit — Plan

> Drives [wt2-runtime-audit.spec.md](wt2-runtime-audit.spec.md). Pure TDD;
> every test maps to a spec section (§2–§6) or a Security mitigation
> (§7 (a)–(i)).

## Global Constraints (copied verbatim from spec §Scope)

- **File domain (do not widen):**
  - `multi-agent/internal/journal/audit.go` (NEW)
  - `multi-agent/internal/journal/audit_test.go` (NEW)
  - `multi-agent/internal/observerstore/schema.sql` (APPEND at tail only —
    do not reorder existing DDL blocks; the appended text is the `audit_events`
    table + `contract_violations` view of spec §4)
  - `multi-agent/internal/observerstore/contract_violations_view.go` (NEW)
  - `multi-agent/internal/observerstore/contract_violations_view_test.go` (NEW)
- **No signature changes and no touch** of `internal/executor/*`,
  `cmd/slave-agent/*`, `internal/evalrun/writer.go`, or `pkg/agentbackend/*`
  anywhere in this WT. Every instrumentation site listed in spec §3.1–§3.5
  is documentation for the FOLLOW-UP wiring WT (candidate:
  WT-2-audit-wiring), not a Phase-3 stage of this WT. That means: no line
  added at `internal/executor/{bash,file,mcp}.go`, no field added to
  `BashConfig` / `FileConfig` / `MCPExecutor` in this WT; no line added at
  `cmd/slave-agent/main.go`, no `TODO` comment at any of these files.
  The `git diff --stat` for this WT MUST show insertions only inside the
  five files listed in the file map below.
- Go module root: `multi-agent/`. All `go test` / `go vet` / `gofmt` commands
  below run from `multi-agent/.worktrees/p2-runtime-audit/multi-agent/`.
- Test commands (each must be green before the phase advances):
  ```
  go test ./internal/journal/... ./internal/observerstore/... -count=1 -shuffle=on -race
  go test ./internal/journal/... -fuzz=FuzzVerify -fuzztime=30s
  go vet ./...
  gofmt -l internal/journal internal/observerstore
  ```
- Every `git commit` ends with:
  `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- **DO NOT push.**

---

## File map

| Path | Action | Responsibility |
|---|---|---|
| `multi-agent/internal/journal/audit.go` | CREATE | `AuditKind`, `AuditEvent`, `Recorder`, `NopRecorder`, `NewSQLRecorder`, `Violation`, `ViolationKind`, `Verifier`, `NewVerifier`, `ArtifactHashAppender`, `NopArtifactHashAppender`, `scrubTarget`, `sensitivePathRe`, sentinel errors (`ErrInvalidArtifactHash`, `ErrTargetTooLong`, `ErrInvalidKind`, `ErrInvalidTimestamp`, `ErrInvalidHashOnNonWrite`, `ErrInvalidSizeOnNonWrite`, `ErrEmptyConversationID`), expvar counters (`audit_write_dropped_total`, `audit_target_redacted_total`). Delegates the SQL insert to `observerstore.WriteAuditEvent`. |
| `multi-agent/internal/journal/audit_test.go` | CREATE | Unit + integration + fuzz. See Test Matrix. |
| `multi-agent/internal/observerstore/schema.sql` | APPEND | Tail-only: the DDL of spec §4 (`audit_events` table + 2 indexes + `contract_violations` view). |
| `multi-agent/internal/observerstore/contract_violations_view.go` | CREATE | `AuditEventRow`, `AuditWriter` interface, `NewAuditWriter`, `WriteAuditEvent` (the SOLE writer to `audit_events`), `ContractViolationRow`, `ViolationsQuery`, `NewViolationsQuery`, `ByRunID`. All SQL is `const` string with `?` placeholders. |
| `multi-agent/internal/observerstore/contract_violations_view_test.go` | CREATE | Writer round-trip, INSERT parameterisation, VIEW-static-grep test, sensitive-path scrub round-trip. |

## TDD ordering (Phase 3)

Each stage: (a) write the failing test, (b) run `go test` and confirm the
expected failure mode, (c) minimal code to green, (d) run the full test
matrix + `go vet` + `gofmt`. No stage merges its own test-and-code commit.

| Stage | Deliverable | Depends on | Verifies |
|---|---|---|---|
| 0 | schema.sql append + `audit_events` table + `contract_violations` view | — | SQLite parses the appended DDL; existing WT-1 tests still pass; the new SELECT-from-empty-view returns 0 rows. |
| 1 | `AuditEvent` type + kind-invariant validators | 0 | spec §2.1 field set; §7 (d) hash regex; §7 (b) 4 KiB Target cap; kind-vs-size/hash mismatches rejected. |
| 2 | `scrubTarget` + `sensitivePathRe` + `audit_target_redacted_total` | 1 | spec §7 (b) — every pattern in the spec set produces `[REDACTED]`; secretscrub tokens redacted first; scrub is idempotent. |
| 3 | `WriteAuditEvent` + `AuditWriter` | 0, 1, 2 | round-trip; SQL parameterised; SQL string is a `const`; single INSERT SQL literal (grep-check). |
| 4 | `NewSQLRecorder` + `NopRecorder` | 1, 2, 3 | Recorder wires scrub → validate → WriteAuditEvent; failed insert returns error (does NOT panic, does NOT swallow); expvar counter bumps on redaction. |
| 5 | `Violation`, `ViolationKind`, `Verifier`, `NewVerifier` | 1 | spec §2.3 rules including the `Name ∪ ArtifactID` union for reads; pure function (no I/O; no globals); empty-slice contract. |
| 6 | `FuzzVerify` | 5 | spec §7 (c) — 30 s fuzz, no panic, no invariant break. |
| 7 | `ArtifactHashAppender` + `NopArtifactHashAppender` | 1, 5 | spec §2.4 + §5.1 + §7 (d) — Append idempotent, Nop impl STILL validates every hash against `^[a-f0-9]{64}$` and returns `ErrInvalidArtifactHash` on the first non-hex entry; sort+dedupe contract; happy path returns nil without touching DB. |
| 8 | `ViolationsQuery` + `ByRunID` | 0, 3 | end-to-end SELECT against seeded `task_contracts` + `audit_events` returns the diff rows required by spec §6 acceptance; tied-contracts semantics from spec §5. |
| 9 | Static grep tests (view-write ban; single INSERT audit_events; file-domain audit) | 0, 3, 8 | spec §7 (e) + §7 (f); §Global-Constraints file-domain — grep asserts no `.go` file OUTSIDE the five listed above was touched by this WT (`git diff --name-only` against baseline). |
| 10 | Design-test `TestWiringContract_HookCallShape` | 4 | spec §7 (i) — the required hook-site shape is spelled as a Go string constant inside `audit_test.go`; it is grep-anchored so the follow-up wiring WT can extend it. |
| 11 | Consumer-view reverse-audit sanity test | 8 | spec §7 (g) — SELECT statements that §D2 ContractViolationRate metric extractor will use return the expected shape. |

## Test matrix

Every row is one Go test; column *"Spec/Security"* names the exact spec
section or Security item the test enforces.

| # | Test | Verifies | Spec / Security |
|---|---|---|---|
| 1 | `TestAuditEvent_Validate_HappyPath` | All four kinds with legal fields pass validation. | §2.1 |
| 2 | `TestAuditEvent_Validate_EmptyConversationIDRejected` | ConversationID="" → `ErrEmptyConversationID`. | §2.1 |
| 3 | `TestAuditEvent_Validate_InvalidKindRejected` | `AuditKind("bogus")` → `ErrInvalidKind`; DDL CHECK also catches it (integration). | §2.1, §4 |
| 4 | `TestAuditEvent_Validate_SizeOnNonWriteRejected` | `KindRead` with `SizeBytes=1` → `ErrInvalidSizeOnNonWrite`. | §2.1 |
| 5 | `TestAuditEvent_Validate_HashOnNonWriteRejected` | `KindToolCall` with `Hash="a"*64` → `ErrInvalidHashOnNonWrite`. | §2.1 |
| 6 | `TestAuditEvent_Validate_NonHexHashRejected` | `KindWrite` with `Hash="not-hex"` → wrapped `ErrInvalidArtifactHash`. | §7 (d) |
| 7 | `TestAuditEvent_Validate_EmptyHashOnWriteAccepted` | `KindWrite` with `Hash=""` accepted (streaming write case). | §2.1 |
| 8 | `TestAuditEvent_Validate_ZeroTimestampRejected` | `Ts=time.Time{}` → `ErrInvalidTimestamp`. | §2.1 |
| 9 | `TestScrubTarget_SensitivePaths` | Table-driven: each pattern in spec §7 (b) → replaced by `[REDACTED]`; counter bumped once per redacted call. | §7 (b) |
| 10 | `TestScrubTarget_SecretsRedactedFirst` | `sk-ant-abcdefgh...` inside a legitimate path is `[REDACTED]`; sensitive-path rule then leaves the rest alone. | §7 (b) |
| 11 | `TestScrubTarget_Idempotent` | For every case in test 9 and 10: `scrubTarget(scrubTarget(x)) == scrubTarget(x)` **and** the counter is bumped exactly once across the two calls. | §7 (b) |
| 12 | `TestScrubTarget_LongInputCapped` | 8 KiB target → returns `ErrTargetTooLong` from validator (scrub runs first; oversize check happens after). | §7 (b) |
| 13 | `TestScrubTarget_NoRedactionsCounterStable` | Fully-clean path → counter NOT bumped. | §7 (b) |
| 14 | `TestWriteAuditEvent_RoundTrip` | Insert one row for each Kind; SELECT * returns the same values (post-scrub). | §5 |
| 15 | `TestWriteAuditEvent_SQLInjection_Parameterised` | Malicious `conv_id = "x'); DROP TABLE audit_events;--"` stored verbatim; table still exists; row count == 1. | §7 (f) |
| 16 | `TestWriteAuditEvent_UniqueEventIDConflictSilent` | Second INSERT with the same `event_id` returns a UNIQUE-constraint error (writer surfaces error; does NOT silently upsert). | §7 (f) |
| 17 | `TestWriteAuditEvent_ConstSQL` | Static-source check: `grep -c 'INSERT INTO audit_events' contract_violations_view.go == 1`; the SQL string is a Go `const`, not a `fmt.Sprintf`. | §7 (f) |
| 18 | `TestOnlyOneAuditEventsWriter` | Static-source check: `grep -rn 'INSERT INTO audit_events\|UPDATE audit_events\|DELETE FROM audit_events' internal/` returns paths matching only `contract_violations_view.go` (plus test files, which are grep-excluded). | §7 (f) |
| 19 | `TestNewSQLRecorder_NilDBReturnsNop` | `NewSQLRecorder(nil)` returns a `NopRecorder`; every Record call is a no-op returning nil. | §2.2 |
| 20 | `TestRecorder_ScrubHappensBeforeInsert` | Inject `Target="/root/.aws/credentials"`; SELECT the row and assert `target = '<HOME>/.aws/[REDACTED]'`; `audit_target_redacted_total` bumped. | §7 (b) |
| 21 | `TestRecorder_InsertFailure_ReturnsErrorNoSwallow` | Fake db returning `sql.ErrConnDone`; Record returns that error wrapped; NO panic; `audit_write_dropped_total` NOT bumped by the recorder itself (the caller is expected to bump — the "no swallow" property means the error propagates). | §7 (a) |
| 22 | `TestRecorder_CallerLogsAndContinues` | An in-package test harness `recordOrLog(r, ev, logSink)` executes: an insert-failing Recorder → the sink captures `journal: audit write dropped:` AND `audit_write_dropped_total.Value() == 1` AND the harness returns nil (no propagation). | §7 (a) |
| 23 | `TestRecorder_DoesNotBlockOnDBHang` | Recorder called with a context whose deadline is in the past; INSERT MUST honour the deadline (returns `context.DeadlineExceeded`); Record returns quickly; business logic can proceed. | §7 (a) |
| 24 | `TestVerifier_Pure_HappyPath` | Contract declares one read + one write; two matching events → empty violation slice. | §2.3 |
| 25 | `TestVerifier_UndeclaredRead` | Event reads `/etc/passwd` not in contract → 1 violation `undeclared_read`. | §2.3 |
| 26 | `TestVerifier_UndeclaredWrite` | Event writes `/tmp/x` not in contract → 1 violation `undeclared_write`. | §2.3 |
| 27 | `TestVerifier_UndeclaredToolCall` | Event calls `mcp:evil:x` not in contract.Tools → 1 violation `undeclared_tool_call`. | §2.3 |
| 28 | `TestVerifier_UndeclaredModelCall` | Event calls model `bad-model` not in contract.Skills → 1 violation `undeclared_model_call`. | §2.3 |
| 29 | `TestVerifier_OrderedByTs` | Two violations with different Ts → returned slice sorted ascending by Ts. | §2.3 |
| 30 | `TestVerifier_ReturnsEmptySliceNotNil` | No violations → returns non-nil empty slice. | §2.3 |
| 31 | `TestVerifier_PurityGrep` | Static grep of `audit.go` inside `Verify` function body has zero hits for `os.`, `net.`, `sql.`, `http.`, `time.Now`, `os.Getenv`, `log.`, `expvar.`. | §7 (c) |
| 32 | `FuzzVerify` | Table-driven fuzz — 30 s, seeded with 8 corpora spanning valid + invalid contracts and event slices; asserts no panic, no invariant break (len ≤ len(events)); every violation Target matches an event Target (post-scrub). | §7 (c) |
| 33 | `TestArtifactHashAppender_NopReturnsNilOnEmpty` | `NopArtifactHashAppender.Append(ctx, "run-1", []string{})` returns nil without any DB touch. | §2.4 |
| 33a | `TestArtifactHashAppender_NopReturnsNilOnAllValidHex` | `NopArtifactHashAppender.Append(ctx, "run-1", [64-hex, 64-hex])` returns nil (Nop validates but has no other work). | §2.4 |
| 33b | `TestArtifactHashAppender_NopRejectsNonHex` | `NopArtifactHashAppender.Append(ctx, "run-1", ["not-hex"])` returns `ErrInvalidArtifactHash` wrapped with the index and offending value. Repeat for uppercase-A-F (must reject; regex is lowercase-only), 63-char, 65-char. | §7 (d) |
| 34 | `TestArtifactHashAppender_HashSetIsSortedDeduped` | Fake appender captures its input; feeding the audit-layer helper with 3 events (one duplicate hash) yields Append arg = sorted, deduped set. | §2.4 |
| 35 | `TestArtifactHashAppender_NonHexRejectedByRecorder` | `Recorder.Record(KindWrite, Hash="not-hex")` returns wrapped `ErrInvalidArtifactHash`; row is NOT inserted (SELECT count == 0). | §7 (d) |
| 36 | `TestArtifactHashAppender_NotCalledOnViolationPresent` | Verify returns ≥1 violation → downstream harness does NOT call Append. Fake appender's call count assertion: `== 0`. | §2.4 |
| 37 | `TestContractViolationsView_ByRunID_ReturnsExpectedRows` | E2E: seed one `task_contracts` row + 5 `audit_events` (3 matching, 2 violating), assert `ByRunID(runID)` returns exactly 2 rows with the correct `violation_kind` + `target`. | §6 acceptance |
| 37a | `TestContractViolationsView_ReadArtifactID_MatchedByEither` | Seed a contract declaring one read with `artifact_id="abc123"` and no `name`; event with `Target="abc123"` → 0 violations. Symmetric: contract with `name="foo"` no id + event `Target="foo"` → 0 violations. | §5, §2.3 |
| 37b | `TestContractViolationsView_TiedContracts_AnyDeclaresDefeats` | Seed TWO `task_contracts` rows for the same `(workspace_id, conversation_id)` with identical `updated_at`, contract A declaring `read=/tmp/a` only and contract B declaring `read=/tmp/b` only; event reads `/tmp/a` → 0 violations (contract A declares it); event reads `/tmp/b` → 0 violations (contract B declares it); event reads `/tmp/c` → 1 violation. Row count is 1 (not multiplied by the tie). | §5 (tied-contracts semantics) |
| 37c | `TestContractViolationsView_NoContract_AllUndeclared` | Insert events with NO matching `task_contracts` row → every event surfaces as `undeclared_*` (fail-closed). | §5 |
| 37d | `TestContractViolationsView_LaterContract_ShadowsEarlier` | Seed TWO `task_contracts` rows for the same conversation, updated_at differ; only the later row's declaration counts (earlier is not tied so is not in `all_conv_contracts`). Event that would be declared by the earlier but not the later → violation surfaces. | §5 (MAX(updated_at) semantics) |
| 39 | `TestContractViolationsView_NoTrigger_Static` | `grep -c 'CREATE.*TRIGGER.*contract_violations' schema.sql == 0` AND `grep -c 'INSTEAD OF' schema.sql == 0`. | §7 (e) |
| 40 | `TestContractViolationsView_NoWritePaths_Static` | `grep -rnE 'INSERT INTO contract_violations|UPDATE contract_violations|DELETE FROM contract_violations' internal/` returns zero matches. | §7 (e) |
| 41 | `TestConsumerView_ContractViolationRate_SQL` | Full SQL from spec §7 (g): (a) `SELECT COUNT(*) FROM contract_violations WHERE run_id = ?`, (b) `SELECT run_id FROM runs WHERE experiment_id = ?`, (c) `SELECT violation_kind, COUNT(*) FROM contract_violations WHERE run_id IN (…) GROUP BY violation_kind`. Runs against seeded data and asserts numerator/denominator arithmetic. | §7 (g) |
| 42 | `TestPerfBench_SkippedInCI` | If a `Benchmark*` file exists and asserts an ops/sec floor, it MUST `t.Skip` when `testing.Short() || os.Getenv("CI") != ""`. This test asserts the guard is present as source text (`grep`). | §7 (h) |
| 43 | `TestFileDomain_NoExternalFileTouched` | Static: `git diff --name-only <baseline>..HEAD` restricted to `*.go`, `*.sql`, and `*.md` shows only the five files in the file map + `docs/specs/wt2-runtime-audit.spec.md` + `docs/specs/wt2-runtime-audit.plan.md`. No `internal/executor/*`, `cmd/slave-agent/*`, `internal/evalrun/*`, `pkg/agentbackend/*` in the list. | §Global-Constraints file-domain |
| 44 | `TestWiringContract_HookCallShape` | The Go string constant `wantHookShape` inside `audit_test.go` matches the hook-call template from spec §7 (i); the test uses `strings.Contains` on the pinned template rather than diff-ing to the executor sources (which are untouched in this WT). | §7 (i) |

**Security-item → test coverage grid** (§7 (a)–(i) each has ≥1 negative test):

| Security item | Test(s) |
|---|---|
| (a) log-and-continue, no silent drop, no block | 21, 22, 23 |
| (b) target scrub via secretscrub + sensitive-path set | 9, 10, 11, 12, 13, 20 |
| (c) Verifier purity + fuzz | 31, 32 |
| (d) hash regex + reject | 6, 33b, 35 |
| (e) view read-only + no trigger | 39, 40 |
| (f) parameterised SQL + single writer | 15, 16, 17, 18 |
| (g) consumer-view reverse-audit | 41 |
| (h) CI-conditional perf | 42 |
| (i) hook-call-shape design test | 44 |

Additional invariants covered by dedicated tests:

| Invariant | Test(s) |
|---|---|
| Read-artifact matching by Name OR ArtifactID | 37a |
| Contract-tie semantics (any declares defeats) | 37b |
| Fail-closed on missing contract | 37c |
| Later contract shadows earlier | 37d |
| File domain (five files only) | 43 |

## Fuzz corpus (test 32 `FuzzVerify`)

Seed with these `f.Add(...)` inputs so the corpus starts non-empty:

1. Empty contract + empty events → 0 violations.
2. Empty contract + 1 event → 1 violation (fail-closed).
3. Contract declares `read=/tmp/a` + event reads `/tmp/a` → 0 violations.
4. Contract declares `read=/tmp/a` + event reads `/tmp/b` → 1 violation.
5. Contract declares `write=/tmp/x` + event writes `/tmp/x` with correct hash → 0 violations.
6. Contract declares `tool=bash` + event calls `bash` → 0 violations.
7. Contract declares `tool=bash` + event calls `mcp:foo:bar` → 1 violation.
8. Contract declares `skill=chat` + event `model_call` on `bad-model` → 1 violation.

Fuzz body mutates: `Kind` (any string, including invalid), `Target` (any UTF-8),
`Hash` (any string), `SizeBytes` (any int64 including negative).

Invariants asserted per fuzz iteration:

- No panic.
- Returned slice length ≤ len(events).
- Every violation's `Target` field equals some event's `Target` (post-scrub).
- Slice is sorted ascending by Ts.

## Commands (Phase-3 stages)

Per stage:

```bash
cd /root/multi-agent/.worktrees/p2-runtime-audit/multi-agent
go test ./internal/journal/... ./internal/observerstore/... -count=1 -shuffle=on -race
go vet ./...
gofmt -l internal/journal internal/observerstore
```

After stage 6 (fuzz landed):

```bash
go test ./internal/journal/... -fuzz=FuzzVerify -fuzztime=30s
```

After every stage (belt-and-braces the file domain):

```bash
git diff --name-only origin/paper/v3-integration..HEAD -- '*.go' '*.sql' '*.md'
# Expected: only the five files from the file map + this spec + this plan.
```

## Commit granularity

- One commit per stage above (12 commits total: stages 0–11). Every commit
  compiles + passes the stage-specific tests. Every commit ends with the
  `Co-Authored-By` trailer. NO push at any point.
- If Codex Phase-3 review flags a P0/P1, add a fix-up commit; do NOT
  force-push or squash — the audit trail of "spec-implied invariant found by
  review" is itself a paper contribution.

## Open questions

None. Everything deferred is called out in spec §8.
