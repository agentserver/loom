# WT-2-driver-promotion-chain — Sub-B2: acceptance pipeline

**Chain position**: 2 of 4 (B6 done → **B2** → B4 → B1). Depends on B6's
audit-row shape and `Stage` / `StageResult` reserved columns.

**Sources**:

- `/root/paper_writing/docs/final/todo_list.md` Phase 2 合流注意事项 §2.
- `/root/paper_writing/docs/intermediate/12_loom_development_tasks_for_v3.md` §B B2.
- Existing skills: `skills/scaffold-mcp-server/SKILL.md`,
  `skills/mcp-acceptance/SKILL.md` (--cases mode landed in
  WT-1-acceptance-golden PR #57).
- Existing golden families: `multi-agent/tests/eval/golden/{csv-profiler,
  log-parser, refund-policy-checker, image-metadata-extractor,
  api-wrapper-for-local-service}/acceptance/cases.jsonl`.
- Ablation: `internal/ablation.NoAcceptanceGate` (registered in Phase 1
  WT-1-acceptance-golden).

---

## 1. Goal

Give the driver a first-class **`scaffold → acceptance → register`**
pipeline that:

1. Chains the three stages IN THE SAME PROCESS so failure propagation is
   an ordinary function-return, not "hope the next shell command sees
   the exit code".
2. Emits ONE `promotion_audit` row per stage (using the B6 table's
   reserved `stage` / `stage_result` columns), so a downstream
   `UserInitiatedSynthesisSuccessRate` scorer can compute
   `(register.ok / scaffold.ok)` from the audit table alone.
3. **Hard-rejects the register when acceptance fails** — even if a
   caller has `NoAcceptanceGate` on, register still bypasses only the
   gate's per-case pass/fail check, NOT the pipeline's "we ran the
   gate at all" invariant (see §5 truth table).
4. Supports `--dry-run-register`: everything runs, register is stubbed
   with a log line, no observer register row is written (but the three
   stage-audit rows still are, so the pipeline stays observable).
5. Ships an e2e bash script
   `tests/scripts/scaffold_acceptance_register_e2e.sh` that exercises
   the pipeline against a golden family in stub mode.

The **security invariant** that makes this worth doing at all: without
B2, a user can `register_slave_mcp` directly, bypassing acceptance —
that's C3 tool-poisoning by definition. B2 makes the composite pipeline
the *only* recommended registration path AND makes the `stage_result`
audit trail the gate the paper's `ValidationFalseAcceptRate` metric
depends on. §7 (a) is non-negotiable.

## 2. Surface changes

### 2.1 New driver tool `promotion_pipeline`

Location: `multi-agent/internal/driver/promotion_pipeline_tool.go`
(same package as `register_slave_mcp` so both share the `Tools` struct
and the promotion-audit writer B6 wires).

MCP tool name: `promotion_pipeline`.

InputSchema (JSON):

| JSON key                    | Go type          | Format                                                 | Required |
|-----------------------------|------------------|--------------------------------------------------------|----------|
| `target_agent_id`           | string           | slave agent id                                         | one of target_agent_id/target_display_name |
| `target_display_name`       | string           | slave display name                                     | one of target_agent_id/target_display_name |
| `spec`                      | `buildspec.Spec` | the same shape `register_slave_mcp` accepts           | yes      |
| `cases_path`                | string           | path to a `cases.jsonl` (readable by the slave)       | yes      |
| `dry_run_register`          | bool             | default false                                          | no       |
| `timeout_sec`               | int              | applies to each stage                                  | no       |
| `promoted_by_user_id`       | string           | same regex as B6                                       | yes      |
| `driver_thread_id`          | string           | same regex as B6                                       | yes      |
| `promotion_reason`          | string           | B6 enum                                                | yes      |
| `candidate_source_task_id`  | string           | same regex as B6                                       | yes      |

`additionalProperties=false`. All four B6 audit fields are required at
the pipeline boundary too — the reason is that B2 writes THREE audit
rows (one per stage) and the four fields identify each row consistently.

### 2.2 Stages

Executed strictly in order; on ANY failure the pipeline aborts and
subsequent stages are SKIPPED (they emit no audit row):

1. **`scaffold`** — invokes the slave's `scaffold-mcp-server` skill via
   `sdk.DelegateTask` with `skill="scaffold-mcp-server"` and a JSON
   prompt `{"spec": spec}`. Success = slave task completes with
   non-error result. On success, the pipeline records
   `source_path = <slave-cwd>/generated_mcp/<spec.name>/server.py` (or
   the language-specific default) — the exact path is returned by the
   slave in the task Result body under key `source_path`. If the slave
   result omits `source_path`, the pipeline computes the conventional
   default and warns.
2. **`acceptance`** — invokes the slave's `mcp-acceptance` skill via
   `sdk.DelegateTask` with `skill="mcp-acceptance"` and prompt
   `{"cases_path": cases_path, "server_cmd": <derived from spec>}`.
   Success = slave task completes AND the acceptance runner exits 0
   (the slave surfaces the exit code in its Result body under
   `acceptance_exit_code`; any non-zero → fail).
3. **`register`** — invokes `registerSlaveMCPTool.Call` internally with
   the same four B6 audit fields, the spec, and the source_path from
   stage 1. Success = the underlying tool returns non-error.

### 2.3 Per-stage audit row

Each stage's outcome writes ONE `promotion_audit` row via the same
`promotionaudit.SQLiteWriter` the driver has wired. Row fields:

| Column                    | Value                                                                        |
|---------------------------|------------------------------------------------------------------------------|
| `mcp_name`                | `spec.name`                                                                  |
| `action`                  | `register` (all three stages log against the same action — the pipeline is a register attempt) |
| `stage`                   | `scaffold` \| `acceptance` \| `register`                                      |
| `stage_result`            | `ok` on success, `fail` on failure                                           |
| `registry_hash_after`     | For scaffold / acceptance: sha256 of empty bytes (nothing changed). For register success: the post-register hash from `driver.LastRegistryHash()`. |
| other B6 fields           | verbatim from the four required audit params (§2.1)                           |

Failure emits ONE row for the failing stage with `stage_result='fail'`
plus NO rows for downstream stages. This is the invariant a
`UserInitiatedSynthesisSuccessRate` scorer relies on.

### 2.4 `--dry-run-register` semantics

- Stage 1 (scaffold) and stage 2 (acceptance) run normally.
- Stage 3 (register) is SKIPPED. Instead, the pipeline writes one
  `stage='register'` audit row with `stage_result='fail'` and
  `registry_hash_after = <empty-bytes sha256>` (the constant hex value
  from B6 §7 (d)). B6's `promotionaudit.Validate` requires
  `RegistryHashAfter` to be 64 lowercase hex for register/unregister
  actions; the empty-bytes sha256 satisfies that AND semantically
  says "no change to registry state, because we didn't register".
- To distinguish dry-run from a real
  acceptance-passed-but-register-failed outcome, the pipeline uses a
  NEW column added in this sub-task: `stage_note TEXT NOT NULL DEFAULT
  ''`. Under `dry_run_register=true`, ALL THREE stage rows
  (scaffold, acceptance, register) carry `stage_note='dry_run'` —
  see §4 for why: the metric filter must drop both numerator and
  denominator symmetrically so dry-run does not deflate the ratio.
  Real fail rows may set `stage_note=<short reason>`. The metric
  scorer applies `WHERE stage_note != 'dry_run'` to both sides of
  the ratio before computing.
- The pipeline emits a log line prefixed `[dry-run]` for the register
  step, per §7 (b).
- Stage 3 does NOT compute `LastRegistryHash()` in dry-run mode —
  updating the per-slave view for a not-actually-registered MCP would
  pollute the D1 hash.

### 2.4.1 DDL append for `stage_note`

`internal/observerstore/schema.sql` gets one more line at the end of
the `promotion_audit` CREATE TABLE (in the same statement, keeping the
table shape backwards-compatible via `IF NOT EXISTS` — freshly-opened
DBs get the column; existing DBs get it via the `ensureColumns`
migration path in `internal/observerstore/store.go`):

```sql
    stage_note                TEXT NOT NULL DEFAULT ''
```

Migration line added to `ensureColumns`:

```go
if _, err := db.Exec(`ALTER TABLE promotion_audit ADD COLUMN stage_note TEXT NOT NULL DEFAULT ''`); err != nil && !isDuplicateColumn(err) {
    return err
}
```

`promotionaudit.AuditFields` gains one field:

```go
StageNote string // "" default; "dry_run" for dry-run register rows; short reason for real fail rows; max 128 chars, /^[a-z_0-9]{0,128}$/
```

`Validate` adds:

```go
if f.StageNote != "" && !stageNoteRE.MatchString(f.StageNote) {
    return wrap("stage_note", ErrInvalidStageNote)
}
```

The tighter regex (letters/digits/underscore only, no whitespace or
SQL meta) prevents the `stage_note` column from becoming a
free-string injection surface. `dry_run` is deliberately part of the
enumeration surface but not a full enum — B2 is one call site;
future stages may need short structured notes without a DDL change.

### 2.5 Observer trace `lifecycle` event

In addition to the audit rows, the pipeline emits ONE observer event
per stage transition (matches the existing observer event surface):

| Field           | Value                                                     |
|-----------------|-----------------------------------------------------------|
| `Type`          | `promotion_pipeline_stage`                                 |
| `Status`        | `started` \| `completed` \| `failed`                       |
| `MCPServerName` | `spec.name`                                                |
| `Payload`       | JSON `{"stage": "scaffold"\|"acceptance"\|"register", "stage_result": ...}` |

The event carries no user-id / thread-id (those live in the audit row).

### 2.6 e2e bash script

`multi-agent/tests/scripts/scaffold_acceptance_register_e2e.sh`.

- `set -euo pipefail`
- Uses `bash <<'HEREDOC'` for any embedded scripts (drift-check-safe).
- Flags:
  - `--dry-run` — pass `dry_run_register=true` through to the driver
    tool AND assert (post-run) that no MCP was registered.
  - `--family <name>` — one of the five golden families
    (`csv-profiler` default).
  - `--driver-url <url>` — driver MCP endpoint; defaults to
    `http://127.0.0.1:8888/mcp` (matches WT-1-eval-runner-skeleton
    stub layout).
- Uses `curl -s -X POST` against the driver's MCP `tools/call`
  endpoint (the same HTTP surface `runServe` exposes).
- Asserts:
  - exit 0 on the happy path;
  - non-zero when the family's `cases.jsonl` contains a deliberate
    failing case (test-only: the script uses a
    `--force-fail-acceptance` mode that swaps in a broken cases file).
- Emits structured `[e2e]` prefixed log lines; script consumers grep
  them.

### 2.7 CLI `--dry-run-register` on register_slave_mcp (rejected)

**Explicit non-goal**: the `--dry-run-register` flag lives on the
PIPELINE, NOT on the underlying `register_slave_mcp` tool. Adding it
there would make the tool's already-complex ordering worse and
duplicate the dry-run branch. Callers that want dry-run go through
`promotion_pipeline`.

## 3. Code shape

### 3.1 New package `internal/promotionpipeline`

Placement rationale: `internal/driver` already exposes the tool; the
STAGE ORCHESTRATION logic (which is 200+ lines of stage-transition +
error-mapping) lives in a sibling package so the driver stays lean
and the pipeline is unit-testable without a full `Tools` bundle.

Files:

```
multi-agent/internal/promotionpipeline/
    pipeline.go        // Pipeline struct + Run + stage runners
    pipeline_test.go
    stages.go          // StageKind enum + stage-specific error mapping
    stages_test.go
```

Public API:

```go
type Pipeline struct { /* private fields */ }

type Deps struct {
    Delegate func(ctx context.Context, skill, prompt string, timeoutSec int) (result string, err error)
    RegisterCall func(ctx context.Context, spec buildspec.Spec, sourcePath string,
                     audit promotionaudit.AuditFields) error
    AuditWrite  func(ctx context.Context, f promotionaudit.AuditFields) error
    EventEmit   func(ev observer.Event)         // may be nil in tests
    Now         func() time.Time                // testable clock
    // IsPromotionPathDisabled returns the current value of the
    // NoUserPromotionPath ablation. The pipeline calls this before
    // stage 1 and refuses if true. Nil means "unwired" — the pipeline
    // logs a WARN line and proceeds. When B1 lands, the driver wires
    // this to `func() bool { return b1promotion.NoUserPromotionPath }`.
    IsPromotionPathDisabled func() bool
    // IsAcceptanceGateDisabled returns the current value of the
    // NoAcceptanceGate ablation (registered in Phase 1
    // WT-1-acceptance-golden). Nil means "unwired" — the pipeline
    // treats as false. Used by the acceptance stage to decide whether
    // to bypass per-case gating.
    IsAcceptanceGateDisabled func() bool
}

func New(deps Deps) *Pipeline

type Request struct {
    Spec                  buildspec.Spec
    CasesPath             string
    DryRunRegister        bool
    TimeoutSec            int
    SlaveAgentID          string   // for LastRegistryHash slave scoping
    SlaveDisplayName      string   // for event payload
    WorkspaceID           string
    PromotedByUserID      string
    DriverThreadID        string
    PromotionReason       promotionaudit.Reason
    CandidateSourceTaskID string
}

// Run executes the three stages in order and returns the outcome per
// stage. The returned StageOutcomes slice has exactly 3 entries in
// order; a failed stage causes downstream entries to have
// StageResult="" and Error=ErrStageSkipped so the caller can render
// a complete UI.
func (p *Pipeline) Run(ctx context.Context, r Request) (StageOutcomes, error)

type StageOutcome struct {
    Stage        Stage
    Success      bool
    RegistryHash string   // "" for scaffold/acceptance; hash on register success; empty-bytes sha256 on dry-run
    StageNote    string   // "" or "dry_run" or short structured fail reason
    Error        error
    Skipped      bool
}
```

### 3.2 Driver tool wiring

`internal/driver/promotion_pipeline_tool.go` implements `Tool`, wires
`Deps` from the surrounding `Tools`:

```go
Deps{
    Delegate:                 tools.callDelegateWithWait,  // small adapter
    RegisterCall:             tools.callRegisterInternal,  // calls register logic without the tool-boundary audit
    AuditWrite:               tools.promoAudit.Write,      // nil-safe wrapper
    EventEmit:                tools.observer.Emit,
    Now:                      time.Now,
    // IsPromotionPathDisabled is nil at B2's commit (B1's target is
    // not wired yet); driver-agent main.go swaps in the concrete
    // predicate when B1 lands within this chain. Nil is the safe-open
    // state. IsAcceptanceGateDisabled uses the accessor already
    // exported by WT-1-acceptance-golden — that flag's target is
    // registered, so no deferred wiring needed.
    IsPromotionPathDisabled:  nil,
    IsAcceptanceGateDisabled: ablation.IsNoAcceptanceGate,
}
```

`ablation.IsNoAcceptanceGate()` is the exported accessor already
provided by WT-1-acceptance-golden (see
`internal/ablation/skill_flags.go`). The pipeline reads it via the
predicate wrapper so `internal/promotionpipeline` stays free of the
`internal/ablation` import — that dep lives only in the driver-tool
wiring file.

### 3.3 Stage-result adapter for the register call in stage 3

Stage 3 delegates to `registerSlaveMCPTool.Call` but MUST NOT write
its OWN audit row (we already write the stage-3 audit row here). To
avoid double-audit, the stage-3 call path uses a private code path
that skips the tool-internal audit write:

```go
// callRegisterInternal is stage-3's entry into the register logic.
// It does NOT call the registerSlaveMCPTool at the MCP-boundary
// (which would run audit) — it invokes an unexported helper
// registerCore(ctx, args) that returns after waitDelegatedTask and
// leaves audit to the pipeline caller.
```

This split keeps the invariant "one audit row per stage" tight.

## 4. Consumer view

| Metric                                | Source                                                            | Computable? |
|---------------------------------------|-------------------------------------------------------------------|-------------|
| `UserInitiatedSynthesisSuccessRate`   | `promotion_audit` filtered `stage='register' AND stage_result='ok' AND stage_note!='dry_run'` ÷ `stage='scaffold' AND stage_result='ok' AND stage_note!='dry_run'`, grouped by `candidate_source_task_id`. To keep the numerator and denominator symmetric — dry-run pipelines produce dry-run rows on ALL THREE stages, not just register — the pipeline sets `stage_note='dry_run'` on the scaffold and acceptance audit rows too when `dry_run_register=true`. The metric filter drops both sides of the ratio; only real pipelines count. | ✅ (needs B2 stage rows — this sub-task) |
| `ValidationFalseAcceptRate`           | `promotion_audit` `stage='acceptance' AND stage_result='ok'` JOIN downstream oracle=fail — cross-join with eval-runner run results | ✅ (needs B2 stage rows + runs table) |
| `TimeFromUserDecisionToRegisteredMCP` | first stage row `ts` → register row `ts`                          | ✅ (needs B2 stage rows) |

## 5. Ablation flag composition truth table

| `NoUserPromotionPath` | `NoAcceptanceGate` | Effect on `promotion_pipeline`                                                                   |
|-----------------------|--------------------|--------------------------------------------------------------------------------------------------|
| off                   | off                | Baseline: 3 stages run, 3 audit rows written, register may succeed or fail based on acceptance.  |
| off                   | ON                 | Acceptance is INVOKED but its per-case pass/fail is bypassed (this matches the existing `NoAcceptanceGate` semantics from WT-1-acceptance-golden). The pipeline STILL writes the `stage='acceptance'` audit row with `stage_result='ok'` sourced from the bypassed gate AND logs `[ablation] NoAcceptanceGate: passed <family> cases WITHOUT gating`. §7 (a) is preserved: the pipeline still ran the gate at all; the ablation only masks the pass/fail decision. |
| ON                    | any                | The pipeline is REFUSED at the boundary: `promotion_pipeline_tool.Call` returns `MCPToolError{Message: "driver-initiated promotion path disabled by NoUserPromotionPath", Category: FailPolicyViolation}`. USER-initiated bare `mcp-userspace install` (which does not go through this pipeline) is unaffected. *"ON" here means `Deps.IsPromotionPathDisabled != nil AND IsPromotionPathDisabled() == true`. When the predicate is nil (B1's target has not been wired yet — safe-open intra-chain state), the pipeline runs with a one-time WARN log; see §9. Once B1 lands and wires `IsPromotionPathDisabled`, this row's semantics are fully live.* |

**Non-negotiable (§7 (a))**: even under `NoAcceptanceGate=ON`, the
acceptance stage MUST be invoked. Refusing to invoke it would let a
malicious MCP land in the registry without producing ANY acceptance
audit signal — exactly the C3 tool-poisoning failure mode. The
ablation masks the gate DECISION, not the gate EXECUTION.

## 6. Test plan (mapped to Security items in §7)

| Security item | Test                                                                       |
|---------------|----------------------------------------------------------------------------|
| §7 (a)        | `TestPipeline_HardRejectsRegisterOnAcceptanceFail` — fake acceptance returns non-zero → register stage MUST NOT be invoked; stage row `stage='register', stage_result='fail'` is NOT written; only stage-2 fail row appears. |
| §7 (a)        | `TestPipeline_AcceptanceGateAblation_StillInvokesAcceptance` — flip `NoAcceptanceGate=true`; acceptance stage still ran (Delegate was called with skill=mcp-acceptance); register still ran and succeeded; audit row for acceptance has stage_result='ok' but log line `[ablation] NoAcceptanceGate:` present. |
| §7 (b)        | `TestPipeline_DryRunRegisterLogsPrefix` — dry-run mode logs `[dry-run]`. |
| §7 (b)        | `TestPipeline_DryRunRegisterAuditRowMarker` — register-stage audit row has `stage_result='fail'`, `stage_note='dry_run'`, and `registry_hash_after` equal to the B6 empty-bytes sha256 constant (satisfies §7 (d) of B6 while unambiguously marking dry-run). |
| §7 (b)        | `TestPipeline_DryRunTagsAllThreeStageRows` — dry-run pipeline invocation persists three audit rows (scaffold, acceptance, register) ALL with `stage_note='dry_run'`; the numerator/denominator symmetry that §4's metric filter depends on hinges on this. |
| §7 (b)        | `TestPipeline_DryRunDoesNotUpdateRegistryView` — asserts `driver.LastRegistryHash()` is unchanged across a dry-run pipeline invocation. |
| §7 (c)        | `TestPipeline_E2EScriptShebangAndSet` — assert the bash script starts with `#!/usr/bin/env bash` and `set -euo pipefail`. |
| §7 (d)        | `TestPipeline_UserPromotionPathAblation_RefusedAtBoundary` — construct `Deps{IsPromotionPathDisabled: func() bool { return true }, ...}`; pipeline tool returns `FailPolicyViolation`; no delegate task opened; no audit row written. |
| §7 (d)        | `TestPipeline_UserPromotionPathPredicateNil_RunsWithWarnLog` — construct `Deps{IsPromotionPathDisabled: nil, ...}`; call the pipeline; assert the run proceeds AND exactly one log line matches `[warn] NoUserPromotionPath predicate unwired — pipeline running unguarded until B1 lands`. Encodes the safe-open intra-chain semantics from §5 / §9. |
| §7 (e)        | `TestPipeline_ConsumerViewJoinKeyPresentOnAllStages` — assert each of the 3 stage rows carries the same `candidate_source_task_id`. |
| §7 (f)        | `TestPipeline_EventPayloadDoesNotContainUserOrThreadID` — assert emitted observer events' payload JSON does not contain the user_id or thread_id (those live in audit rows only). |
| §7 (g)        | `TestPipeline_TimeoutIsPerStageNotTotal` — pass `timeout_sec=1`, make scaffold sleep 500ms, acceptance sleep 500ms, register sleep 500ms; assert all three complete successfully (per-stage timeout, not cumulative). |
| §7 (h)        | `TestPipeline_ProperlyReadsCasesPath_NoDirectoryTraversal` — table-driven subtests reject every `..`-bearing path (`../evil`, `safe/../evil`, `a/../b`, `./..`), absolute paths under `/etc/`, and paths with URL-encoded traversals (`%2e%2e/foo`). The reject check is a `strings.Contains(clean(path), "..")` + prefix check, not merely a `strings.HasPrefix(path, "../")` — the latter misses the mid-path form the P1 finding called out. |
| §7 (i)        | `TestPipeline_AuditWriteFailureDegradesButPipelineProceeds` — audit-writer error on stage 1 → pipeline continues to stage 2 (matches B6 degrade pattern). Emit log line. |
| §7 (j)        | `TestPipeline_NoObserverAblation_AuditDroppedWithLogEventsUnaffected` — with `NoObserver=true` the audit writer drops rows with the standard `[ablation] NoObserver:` log line (matching B6); observer events (which live on a separate `events` table with different governance) are NOT suppressed by this flag. Test asserts BOTH: (a) 0 promotion_audit rows persisted, (b) the 3 stage-transition events landed on the observer sink. |
| §7 (k)        | `TestPipeline_UserInitiatedInstallStillWorksUnderNoUserPromotionPath` — with `NoUserPromotionPath=true`, driver `promotion_pipeline` tool refuses (from §7 (d) test), BUT a separate call to `mcp-userspace install` with the four B6 audit fields still succeeds and writes an `action='install'` row. Documents the divergence: driver-initiated is blocked, user-initiated is preserved. |

## 7. Security

### (a) Hard-reject register on acceptance fail

Non-negotiable. Without this the pipeline is theatre — a malicious
caller who owns the slave-side `mcp-acceptance` skill's exit code
still can't slip through, because the pipeline consults the exit code
directly.

Ablation `NoAcceptanceGate` bypasses the *per-case* decision only;
the acceptance skill is STILL invoked and STILL emits an audit row.
An ablation that could skip invocation would defeat the audit story.

### (b) `--dry-run-register` semantics locked

- `[dry-run]` log prefix on the register step.
- Dry-run register row: `stage_result='fail'`,
  `registry_hash_after=<empty-bytes sha256 constant>` (64-hex, satisfies
  B6 §7 (d)), `stage_note='dry_run'`. All three stage rows (scaffold,
  acceptance, register) carry `stage_note='dry_run'` so the downstream
  metric filter drops both numerator and denominator symmetrically —
  see §4.
- Per-slave registry view is NOT updated in dry-run — updating for a
  not-actually-registered MCP would pollute `driver.LastRegistryHash()`
  for the D1 field.

### (c) Bash script starts with `#!/usr/bin/env bash` + `set -euo pipefail`

The e2e script MUST fail-fast on any unbound variable / non-zero exit.
Enforced by a test in `tests/scripts/scaffold_acceptance_register_e2e_test.go`
(Go test that greps the script file — fixes drift where a copy-paste
strips `set -e`).

### (d) `NoUserPromotionPath` refuses the pipeline

Prevents driver-initiated promotion under the ablation while leaving
user-initiated `mcp-userspace install` unaffected (B6 handles install
audit separately).

### (e) Consumer-view join key present on ALL stage rows

Every stage row carries the SAME `candidate_source_task_id` so the
metric scorer can join across stages by that key without duplication.

### (f) Observer events do NOT carry user_id / thread_id

Only audit rows do. Events go to a different table (`events`) with
looser access controls; leaking the user id there widens the
disclosure surface unnecessarily.

### (g) Timeout is per-stage, not cumulative

`timeout_sec` applies to each individual stage's delegate call. A user
who sets `timeout_sec=60` gets up to 3 minutes of total pipeline
wall-clock — which matches the intuitive "60s per remote call" mental
model. A cumulative interpretation would make `timeout_sec=60`
effectively `20s per stage`, which is misleading.

### (h) `cases_path` traversal guard

Reject any `cases_path` that starts with `../` or `/etc/` or contains
`..` segments. Slaves resolve the path relative to their workdir; a
traversal-shaped value could point at a file outside the golden root
and change the acceptance decision by pointing at a permissive cases
file.

### (i) Audit-write failure degrades

Same pattern as B6: if `AuditWrite` fails for stage N, the pipeline
logs and CONTINUES to stage N+1. Alternative — abort — would let a
transient DB blip cause an unnecessary failure that reads as "MCP
broke" instead of "audit sink broke".

### (j) `NoObserver` interaction

The `promotionaudit.SQLiteWriter` already respects `NoObserver`.
Observer events go through a separate `observer.Event` pipe; the
pipeline emits them independently. If a downstream ablation wants BOTH
suppressed together, that's a WT-2-flag-integration concern.

### (k) `NoUserPromotionPath` register-tool interaction

The bare `register_slave_mcp` tool (B6) still works with
`NoUserPromotionPath=off`. Under `NoUserPromotionPath=on` — once B1
lands and wires the flag target — B1's tool guard rejects
driver-initiated bare register calls (see B1 spec); this pipeline is
also refused at its own boundary. USER-initiated `mcp-userspace
install` remains available in ALL ablation combinations because it
represents user intent, not driver-initiated promotion.

Until B1 lands (this chain runs B2 before B1), the driver constructs
the pipeline with `Deps.IsPromotionPathDisabled = nil`; the pipeline
logs one WARN line and treats the flag as off — see §9 for the
predicate-injection design that lets B2 compile without importing
anything from B1.

## 8. Files touched

Created:

- `multi-agent/internal/promotionpipeline/pipeline.go`
- `multi-agent/internal/promotionpipeline/pipeline_test.go`
- `multi-agent/internal/promotionpipeline/stages.go`
- `multi-agent/internal/promotionpipeline/stages_test.go`
- `multi-agent/internal/driver/promotion_pipeline_tool.go`
- `multi-agent/internal/driver/promotion_pipeline_tool_test.go`
- `multi-agent/tests/scripts/scaffold_acceptance_register_e2e.sh`
- `multi-agent/tests/scripts/scaffold_acceptance_register_e2e_test.go`
  (Go wrapper that shells out to the bash script in a hermetic env)
- `docs/specs/wt2-driver-promotion-chain-B2.spec.md` (this file)
- `docs/specs/wt2-driver-promotion-chain-B2.plan.md`

Modified:

- `multi-agent/internal/driver/mcp_server.go` — register the new tool
  in `NewMCPServer`'s tool list.
- `multi-agent/internal/driver/register_mcp_tool.go` — refactor to
  expose `registerCore` for stage-3 reuse without double-audit.
- `multi-agent/internal/observerstore/schema.sql` — append
  `stage_note` column to `promotion_audit` CREATE TABLE.
- `multi-agent/internal/observerstore/store.go` — `ensureColumns`
  gets one `ALTER TABLE promotion_audit ADD COLUMN stage_note …` line
  guarded by `isDuplicateColumn` (matches the existing migration
  pattern in the same function).
- `multi-agent/internal/observerstore/promotion_audit_schema_test.go`
  — extend `TestSchema_PromotionAuditTableExists` to require
  `stage_note`.
- `multi-agent/internal/promotionaudit/fields.go` — `AuditFields`
  gets a `StageNote string` field; `Validate` gains the
  `stageNoteRE` regex check with `ErrInvalidStageNote` sentinel.
- `multi-agent/internal/promotionaudit/fields_test.go` — table-driven
  tests for `StageNote` accept `""`, `"dry_run"`, `"fail_scaffold"`,
  reject `"a b"`, `"a-b"` (hyphen not in the tighter regex),
  overlong.
- `multi-agent/internal/promotionaudit/writer.go` — `insertSQL`
  column list and `Write` bind list gain `stage_note` in canonical
  position.
- `multi-agent/internal/promotionaudit/writer_test.go` — extend the
  canonical-row test to round-trip `stage_note`.
- `multi-agent/cmd/driver-agent/main.go` — construct the pipeline
  tool with the two predicates per §3.2 wiring snippet.

## 9. What this spec explicitly does NOT do

- Does NOT add a new ablation flag. `NoAcceptanceGate` (registered in
  Phase 1 WT-1-acceptance-golden) and `NoUserPromotionPath` (name is
  in `ablation.KnownFlags()` from WT-1-ablation-registry, but its
  target `*bool` is REGISTERED BY B1 — the next sub-task) are the
  ablations that gate this pipeline. The pipeline reads both flags via
  caller-supplied predicates (`Deps.IsPromotionPathDisabled` /
  `Deps.IsAcceptanceGateDisabled`) rather than reaching into
  `ablation.Default` directly. This lets B2 land BEFORE B1 without a
  compile-time dependency on B1's exported var: the driver wires
  `nil` for `IsPromotionPathDisabled` at B2's commit, then swaps in
  `func() bool { return b1promotion.NoUserPromotionPath }` at B1's
  commit. When the predicate is nil, the pipeline logs one WARN line
  and treats the flag as off (safe-open intra-chain window). B4 will
  add `NoRegistryLookup`.
- Does NOT modify `mcp-acceptance` skill behavior. The `--cases` mode
  landed in WT-1-acceptance-golden; the pipeline invokes it verbatim.
- Does NOT modify `scaffold-mcp-server` skill behavior.
- Does NOT introduce a Postgres path. SQLite matches the observer
  store's stub-mode policy.
- Does NOT wire the pipeline into eval-runner. That's a follow-up
  under WT-2-metric-extract.
