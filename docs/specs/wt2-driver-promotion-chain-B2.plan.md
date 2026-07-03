# WT-2-driver-promotion-chain — Sub-B2 plan

Implementation plan for `docs/specs/wt2-driver-promotion-chain-B2.spec.md`.
TDD throughout. Steps ordered so each compiles + tests pass before the
next starts.

## Step 1 — `stage_note` DDL + `AuditFields.StageNote` + tests (spec §2.4.1, §7 (b))

Tests first:

- `TestSchema_PromotionAuditHasStageNote` — reopen an existing DB (from
  before this column) and assert the migration adds `stage_note`.
- `TestSchema_PromotionAuditStageNoteFreshDB` — fresh DB has the column
  via the DDL append.
- `TestAuditFields_StageNoteRegex` — table-driven: accept `""`,
  `"dry_run"`, `"fail_scaffold"`, `"a"` × 128 chars; reject `"a b"`,
  `"a-b"` (hyphen not in the tight regex), 129-char, `"a\nb"`,
  `"UPPER"`.
- `TestSQLiteWriter_StageNoteRoundTrip` — write a row with
  `StageNote="dry_run"`, select, assert byte-equal.
- `TestSQLiteWriter_EmptyStageNoteDefaults` — write with
  `StageNote=""`, select, assert empty string (not NULL).

Then implement:

- `internal/observerstore/schema.sql`: append `stage_note TEXT NOT
  NULL DEFAULT ''` at the end of the `promotion_audit` CREATE TABLE.
- `internal/observerstore/store.go`: add one `ALTER TABLE
  promotion_audit ADD COLUMN stage_note …` line to `ensureColumns`
  guarded by `isDuplicateColumn`.
- `internal/observerstore/promotion_audit_schema_test.go`: extend
  `TestSchema_PromotionAuditTableExists` to include the new column
  name in the required set.
- `internal/promotionaudit/fields.go`: add `StageNote string` field,
  `stageNoteRE = regexp.MustCompile(`^[a-z0-9_]{0,128}$`)`,
  `ErrInvalidStageNote` sentinel, Validate check.
- `internal/promotionaudit/writer.go`: extend `insertSQL` column list
  and `Write` bind list.

## Step 2 — `internal/promotionpipeline` package + tests (spec §3.1, §7 (a), §7 (i))

Tests first (in `pipeline_test.go`):

- `TestPipeline_HappyPath_ThreeStageRowsThreeEvents` — mock Delegate
  returns success for scaffold and acceptance; mock RegisterCall
  returns nil; assert 3 audit rows written in order, 3 observer
  events emitted with `Type=promotion_pipeline_stage`.
- `TestPipeline_HardRejectsRegisterOnAcceptanceFail` (spec §7 (a)) —
  Delegate returns non-zero `acceptance_exit_code`; assert
  RegisterCall was NOT called; only 2 audit rows (scaffold ok +
  acceptance fail) written.
- `TestPipeline_AcceptanceGateAblation_StillInvokesAcceptance` (§7 (a))
  — set `IsAcceptanceGateDisabled=func() bool { return true }`;
  assert Delegate WAS called with skill=mcp-acceptance; the ablation
  ONLY masks the per-case decision; register still runs.
- `TestPipeline_UserPromotionPathAblation_RefusedAtBoundary` (§7 (d))
  — `IsPromotionPathDisabled=func() bool { return true }`; expect
  `FailPolicyViolation`; no Delegate calls.
- `TestPipeline_UserPromotionPathPredicateNil_RunsWithWarnLog` (§7 (d))
  — `IsPromotionPathDisabled=nil`; pipeline runs; one WARN log line
  `[warn] NoUserPromotionPath predicate unwired…`.
- `TestPipeline_DryRunRegisterAuditRowMarker` (§7 (b)) — dry-run mode;
  register-stage row has `stage_result='fail'`,
  `stage_note='dry_run'`, `registry_hash_after=<empty-bytes sha256>`.
- `TestPipeline_DryRunTagsAllThreeStageRows` (§7 (b)) — dry-run mode;
  scaffold + acceptance + register rows ALL carry
  `stage_note='dry_run'`.
- `TestPipeline_DryRunDoesNotUpdateRegistryView` (§7 (b)) — assert
  `driver.LastRegistryHash()` unchanged.
- `TestPipeline_ConsumerViewJoinKeyPresentOnAllStages` (§7 (e)) — all
  3 audit rows carry the same `candidate_source_task_id`.
- `TestPipeline_EventPayloadDoesNotContainUserOrThreadID` (§7 (f)) —
  emit observer events; assert their Payload JSON does not contain
  the user_id or thread_id strings.
- `TestPipeline_TimeoutIsPerStageNotTotal` (§7 (g)) — timeout_sec=1;
  each Delegate returns after 500ms; pipeline completes.
- `TestPipeline_ProperlyReadsCasesPath_NoDirectoryTraversal` (§7 (h))
  — table-driven: reject `../evil`, `safe/../evil`, `a/../b`,
  `./..`, `/etc/passwd`, `%2e%2e/foo`; accept
  `tests/eval/golden/csv-profiler/acceptance/cases.jsonl`.
- `TestPipeline_AuditWriteFailureDegradesButPipelineProceeds` (§7 (i))
  — AuditWrite returns an error on stage 1; stages 2 & 3 still run.
- `TestPipeline_NoObserverAblation_AuditDroppedWithLogEventsUnaffected`
  (§7 (j)) — flip `evalrun.DisableTelemetry=true`; audit rows
  dropped (0 rows) with `[ablation] NoObserver:` log; observer
  events still land on the sink.
- `TestInstall_PathBypassesUserPromotionPathAblation` (§7 (k)) —
  lives in `cmd/mcp-userspace/cmd_install_test.go` (NOT in the
  pipeline test file, because the invariant is about install, not
  pipeline). The test asserts by CODE INSPECTION rather than a live
  ablation flip, since the install CLI does not read the
  `NoUserPromotionPath` flag AT ALL: use `go/parser` +
  `go/ast.Inspect` to walk `cmd/mcp-userspace/cmd_install.go` and
  fail if any identifier `NoUserPromotionPath` (from
  `internal/ablation` or elsewhere) appears in the AST. That AST
  guard encodes the invariant "install cannot become dependent on
  this ablation without a plan change". Live behaviour is exercised
  by the existing `TestInstall_HappyPathWithWorkspace_WritesAuditRow`
  which does not touch the flag either way.

Then implement:

- `internal/promotionpipeline/pipeline.go`: `Pipeline`, `Deps`,
  `Request`, `Run`, `StageOutcomes`, `Stage` type.
- `internal/promotionpipeline/stages.go`: `Stage` enum
  (`StageScaffold`, `StageAcceptance`, `StageRegister`) + stage
  runners `runScaffoldStage`, `runAcceptanceStage`,
  `runRegisterStage`. Each returns `StageOutcome`. Failure emits
  `Skipped=true` on later outcomes.
- `internal/promotionpipeline/cases_path.go`: path-traversal guard
  helper `validateCasesPath(p string) error`.

## Step 3 — Extract `registerCore` from `register_mcp_tool.go` (spec §3.3)

**Ordered before Step 4** so Step 4 can call `registerCore` without a
forward reference — keeps the "each step compiles" rule.

Tests first (in `register_mcp_tool_test.go`):

- `TestRegisterCore_DoesNotWriteAudit` — call `registerCore` directly;
  assert 0 audit rows written by that call.
- `TestRegisterCore_ReturnsRegistryHash` — canonical happy path
  returns non-empty hash.
- `TestRegisterSlaveMCP_StillWritesAuditRowAtToolBoundary` — the
  existing behaviour must not regress; the tool call still writes
  exactly one audit row after the refactor.

Then refactor `register_mcp_tool.go`: extract the delegate +
waitDelegatedTask + registry-hash publish into
`func (t *Tools) registerCore(ctx context.Context, args registerCoreArgs) (registryHash string, err error)`.
The tool boundary `registerSlaveMCPTool.Call` calls `registerCore`
then writes the audit row. The pipeline (Step 4) will call
`registerCore` and write its own stage-3 audit row separately.

## Step 4 — Driver tool `promotion_pipeline_tool.go` + tests (spec §2.1, §3.2)

Now that `registerCore` exists (Step 3), the pipeline tool's
`RegisterCall` dep points at `tools.registerCore` and the file
compiles.

Tests first (in `promotion_pipeline_tool_test.go`):

- `TestPromotionPipelineTool_HappyPath` — end-to-end via the tool
  boundary; assert wire-shape of the returned JSON.
- `TestPromotionPipelineTool_RequiresAllFourAuditFields` — table
  driven for the four required B6 fields.
- `TestPromotionPipelineTool_RejectsMalformedCasesPath` — traversal
  guard fires at the tool boundary.

Then implement `promotion_pipeline_tool.go` — implements `Tool`,
builds `Deps` from `Tools`, calls `pipeline.Run`.

## Step 5 — e2e bash script + Go wrapper test (spec §2.6, §7 (c))

Tests first (in `scaffold_acceptance_register_e2e_test.go`):

- `TestE2EScript_StartsWithShebangAndSet` — grep the script file for
  `^#!/usr/bin/env bash$` and `set -euo pipefail`.
- `TestE2EScript_UsesHeredocQuoted` — grep for `<<'` (quoted
  heredoc) at any invocation of embedded bash.
- `TestE2EScript_DryRunFlag` — pass `--dry-run`, assert the
  driver-mock returns success but records `dry_run_register=true`.

Then create `tests/scripts/scaffold_acceptance_register_e2e.sh` per
§2.6. The script drives the driver's `promotion_pipeline` MCP tool
via `curl` against the stub-mode HTTP endpoint. The Go test spins up
a fake driver HTTP server that captures the request body and returns
canned MCP responses.

## Step 6 — Wire predicates in driver-agent (spec §3.2 wiring)

Tests: covered by existing driver-agent smoke tests. No new tests
needed here — the wiring is a 3-line change verified transitively.

Modify `cmd/driver-agent/main.go`:

- Import `internal/ablation`.
- After `tools := driver.NewTools(...)`, if the pipeline tool is
  present in the tool list, wire `Deps` via a new
  `tools.SetPromotionPipelineDeps(deps promotionpipeline.Deps)`
  setter. Predicates: `IsPromotionPathDisabled=nil` (B1 unwired),
  `IsAcceptanceGateDisabled=ablation.IsNoAcceptanceGate`.

## Verification (Step 7, before commit)

```
cd multi-agent
go vet ./internal/promotionpipeline/... ./internal/driver/... ./internal/observerstore/... ./internal/promotionaudit/...
go test ./internal/promotionpipeline/... ./internal/driver/... ./internal/observerstore/... ./internal/promotionaudit/... ./cmd/driver-agent/ -count=1 -race
bash tests/scripts/scaffold_acceptance_register_e2e.sh --dry-run
```

## Commit shape

```
WT-2-driver-promotion-chain B2: acceptance pipeline + e2e script + hard-reject on gate fail

- New internal/promotionpipeline package: 3-stage pipeline
  (scaffold → acceptance → register) with per-stage audit rows and
  observer trace events. Predicate-injected ablation reads so B2
  compiles before B1 lands.
- New driver tool `promotion_pipeline` (internal/driver).
- internal/promotionaudit: AuditFields gains StageNote (tight regex);
  writer / DDL / migration append column.
- Refactor: registerCore extracted from registerSlaveMCPTool so
  stage-3 register can be invoked without double-audit.
- e2e: tests/scripts/scaffold_acceptance_register_e2e.sh drives the
  pipeline end-to-end in stub mode; Go wrapper test asserts
  shebang/set/heredoc invariants.
- Docs: docs/specs/wt2-driver-promotion-chain-B2.{spec,plan}.md.

Security: acceptance failure hard-rejects register (§7 (a));
NoAcceptanceGate ablation masks per-case decision only, never
skips invocation; NoUserPromotionPath refuses pipeline at boundary
when wired; dry-run rows tagged stage_note='dry_run' on ALL three
stages so metric filter is numerator/denominator symmetric; cases_path
traversal guard covers mid-path .. and URL-encoded forms.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
```
