# WT-2-driver-promotion-chain — Sub-B1 plan

TDD throughout. Steps ordered so each compiles + tests pass before the
next starts.

## Step 1 — DDL + writer + tests (spec §2.1)

Tests first (in
`internal/observerstore/promote_candidates_writer_test.go`):

- `TestSchema_PromoteCandidatesExists` — column set including run_id.
- `TestSchema_PromoteCandidatesRejectsBadEnumViaCheck` (§7 (g)).
- `TestPromoteCandidatesWriter_InsertRoundTrip`.
- `TestPromoteCandidatesWriter_InsertOrIgnoreOnDuplicate` — same
  (run_id, candidate_id) twice → 1 row.
- `TestPromoteCandidatesWriter_UpdateDecision`.
- `TestPromoteCandidatesWriter_Expire_LeavesTerminalDecisionsAlone`.

Then implement:

- Append DDL to `internal/observerstore/schema.sql`.
- Create `internal/observerstore/promote_candidates_writer.go`:
  `PromoteCandidatesWriter` interface + SQLite impl. INSERT uses
  `INSERT OR IGNORE INTO promote_candidates ... ON CONFLICT DO NOTHING`
  (or `ON CONFLICT(run_id, candidate_id) DO NOTHING` — same semantic).

## Step 2 — Ablation + init-error surfacing + tests (spec §5)

Tests first (in
`internal/driver/promote_candidate_ablation_test.go`):

- `TestIsNoUserPromotionPath_ZeroValue`.
- `TestSurfacePromotionInitErrorOnce_EmitsOnce` — inject
  noUserPromotionPathInitErr; call surfacePromotionInitErrorOnce
  twice; assert one log.

Then `internal/driver/promote_candidate_ablation.go` per spec §5.

## Step 3 — CandidateID derivation + tests (spec §4.1, §7 (i))

Tests first (in
`internal/driver/promote_candidate_test.go`):

- `TestCandidateID_DerivedFromFamilyAndSortedTaskIDs`.
- `TestCandidateID_DifferentRunsProduceDifferentIDs`.

Then implement `computeCandidateID(runID, family string, taskIDs
[]string) string` as an internal helper in
`internal/driver/promote_candidate.go`.

## Step 4 — SurfacePromoteCandidate + RecordCandidateDecision +
ExpireCandidatesOlderThan + tests (spec §3, §7 (a-h))

Tests first — one per Security row + Metric row per §8. Uses a
`recordingWriter` mock (analogous to B2's fake writer). Include:

- Field validation (family/task_ids regex).
- Ablation short-circuit (§7 (e), side-effect-free).
- Init-error first-use surfacing.
- Idempotent decision update.
- Expiry logging + terminal-decision-safety.
- run_id captured from `driver.CurrentRunID` (empty allowed).

Then implement the three public functions +
`PromoteCandidateDeps` + `SetPromoteCandidateDeps`.

## Step 5 — Detector + tests (spec §4.2)

Tests first (in `internal/driver/promote_candidate_detector_test.go`):

- `TestRecordAdHocScriptTask_FiresCandidateAfterSecondFamily`.
- `TestRecordAdHocScriptTask_UnderNoUserPromotionPath_NoCandidate`.
- `TestRecordAdHocScriptTask_LOOMEvalTaskFamilyEnvOverride`.

Then implement `RecordAdHocScriptTask(family, taskID,
workspaceID)`. Family is `first_token_of_summary` (or
`LOOM_EVAL_TASK_FAMILY` env) — implemented as a small helper the
caller passes in via `familyOfTask` (test-injectable).

## Step 6 — B6 register guard extension + tests (spec §1 third gate)

Tests first (extend `register_mcp_tool_test.go`):

- `TestRegisterSlaveMCP_UnderNoUserPromotionPath_RefusesAllExceptExplicitUser`
  — table: `driver_agent_inferred` / `batch_import` / `ci_seed`
  each rejects with FailPolicyViolation;
  `explicit_user_request` succeeds.

Then modify `registerSlaveMCPTool.Call`: early-reject when
`IsNoUserPromotionPath() &&
promotionReason != string(promotionaudit.ReasonExplicitUserRequest)`.

## Step 7 — B2 predicate wire-up + integration test

Test first (in `promotion_pipeline_tool_test.go` or a new integration
test file):

- `TestPromotionPipeline_UsesLiveNoUserPromotionPathPredicate` — set
  `noUserPromotionPath=true` (via ablation flag Set); call
  promotion_pipeline tool; assert FailPolicyViolation.

Then modify `promotion_pipeline_tool.go:buildProdPipeline` to point
`IsPromotionPathDisabled` at `driver.IsNoUserPromotionPath` (was nil
in B2).

## Step 8 — driver-agent wiring + expiry goroutine (spec §6)

Modify `cmd/driver-agent/main.go`:

- After `SetLookupDeps`, call
  `driver.SetPromoteCandidateDeps({Writer: observerstore.NewPromoteCandidatesWriter(promoStore.DB()), Events: obs, Now: time.Now})`.
- Start a goroutine that calls `driver.ExpireCandidatesOlderThan`
  every 5 minutes with cutoff `time.Now().Add(-24 * time.Hour)`.

## Verification

```
cd multi-agent
go vet ./internal/driver/... ./internal/observerstore/... ./cmd/driver-agent/...
go test ./internal/driver/... ./internal/observerstore/... ./cmd/driver-agent/... ./internal/promotionpipeline/... ./internal/promotionaudit/... -count=1 -race
```

## Commit shape

```
WT-2-driver-promotion-chain B1: promote-candidate surfacing + NoUserPromotionPath ablation target

- driver.SurfacePromoteCandidate + RecordCandidateDecision +
  ExpireCandidatesOlderThan (24h TTL, monotonic-clock docs).
- driver.RecordAdHocScriptTask detector fires
  SurfacePromoteCandidate when the same family+task_ids-set is
  observed twice within a run. INSERT OR IGNORE dedups.
- observerstore.promote_candidates table keyed by
  UNIQUE(run_id, candidate_id) so parallel runs stay unambiguous;
  candidate_id itself embeds run_id in its sha256 so the B6 JOIN
  key stays run-attributable.
- NoUserPromotionPath ablation target registered in driver
  (name declared in Phase 1). Gates: (1) SurfacePromoteCandidate
  short-circuits with structured log; (2) promotion_pipeline_tool
  refuses at boundary (predicate wired live);
  (3) register_slave_mcp refuses driver-initiated reasons —
  explicit_user_request still passes.
- Init-registration failure surfaces on first driver-promotion
  entrypoint call (matches B4 NoRegistryLookup pattern).
- driver-agent wires the writer + starts a 5-min expiry sweep.

Security: parameterised SQL; source_task_ids + family regex-bounded;
run-scoped candidate_id closes B6 join ambiguity; INSERT OR IGNORE
idempotent; ablation short-circuits BEFORE side-effects; observer
events exclude source_task_ids (which live only in DB).

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
```
