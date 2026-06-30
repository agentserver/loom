# WT-1-contract-schema — Plan

> Companion to `docs/specs/wt1-contract-schema.spec.md`.
> All section references below (§1, §2.x, §6, §7) point at the spec unless
> otherwise stated.

---

## 1. Goal of this plan

Translate the spec into a concrete file-by-file work breakdown plus the
exhaustive test matrix the implementation must satisfy. The plan adds no
new requirements — anything not in the spec is out of scope and must round-
trip through a spec edit.

## 2. Implementation order (TDD strict)

The order below is chosen so that every step adds exactly one logical
change with a failing test we then make pass. Each step is a separate
commit candidate — the final `git log` may squash, but the working tree
must compile and tests must pass between every two steps.

| # | Step | New / modified | Pre-step test | Make-pass change |
|---|------|----------------|---------------|------------------|
| 1 | Add `RecoveryHint` field to `TaskContract` + struct tag | `internal/contract/types.go` | `TestTaskContract_RecoveryHint_JSONRoundTrip` | Add field with `json:"recovery_hint"` (no omitempty) |
| 2 | Introduce `ValidationError`, sentinels, and the `Missing []string` mechanic | `internal/contract/validate.go` | `TestValidate_MissingRecoveryHint_Reject` (single-missing) + `TestValidate_MissingMultipleFields_AllReported` (multi-missing in §2.2 order) | Add `ValidationError`, sentinels (`ErrMissingFields`, `ErrRecoveryHintTooLong`, `ErrRecoveryHintContainsControlChar`, `ErrRecoveryHintLooksLikeHTML`); rewrite `Validate()` body to collect Missing list and return `*ValidationError` |
| 3 | RecoveryHint content checks (length, control char, HTML prefix) | `internal/contract/validate.go` | `TestValidate_RecoveryHint_TooLong_Reject` (4097 fail, 4096 pass), `TestValidate_RecoveryHint_ControlChar_Reject` (`\x07` fail, `\t\n\r` pass), `TestValidate_RecoveryHint_HTMLPrefix_Reject` (each of 8 prefixes, case-insensitive) | Add helper `validateRecoveryHint(string) []error`; call from `Validate()` only when RecoveryHint non-empty (the empty case is already covered by Missing-fields path) |
| 4 | `read_artifacts` nil-vs-empty enforcement (§2.2.1) | `internal/contract/validate.go` | `TestValidate_ReadArtifacts_NilFails`, `TestValidate_ReadArtifacts_EmptyExplicitPasses`, `TestValidate_ReadArtifacts_PopulatedPasses` | Add `tc.DataContract.ReadArtifacts == nil` check; counts toward Missing list |
| 5 | `ContractCompletenessEvent` + `EventSink` plumbing | `internal/contract/completeness.go` (NEW) | `TestCompleteness_Full7Of7`, `TestCompleteness_PartialBitmap`, `TestCompleteness_RatioDenominator7` (the four-assertion test from §6) | Define event type, `EventSink` interface, atomic-backed `currentSink()`/`RegisterCompletenessSink`, `presentFields(tc)` helper, `const completenessDenominator = 7` (exported for test); emit from `Validate()` on success path |
| 6 | Bitmap rules (nil-slice / struct-zero / trimmed-string) | `internal/contract/completeness.go` | `TestPresentFields_NilSliceAbsent`, `TestPresentFields_EmptyExplicitSlicePresent`, `TestPresentFields_StructAllZeroAbsent`, `TestPresentFields_TrimmedStringAbsent` | Implement `presentFields(tc TaskContract) []string` per §2.6 |
| 7 | Body-only payload assertion (§7 (d)) | `internal/contract/completeness_test.go` | `TestContractCompleteness_OnlyBitmap_NoBody` | None — the test marshals the event with distinctive UUIDs in the contract body and asserts they do NOT appear in the JSON output |
| 8 | Ablation flag registration | `internal/contract/ablation.go` (NEW) | `TestAblationRegisteredOnDefault` | Define `DisableSchemaEnforce`, `DisableContractEntirely` bools; `init()` registers both with `ablation.Default` — fatal `panic` on registration error so a misconfigured test setup is loud |
| 9 | `EnforceContract` wrapper | `internal/contract/validate.go` | `TestEnforceContract_AppliesDefaultsThenValidates`, `TestEnforceContract_BothAblations_ContractEntirelyWins` (mutex via `t.Cleanup` resets) | Implement `EnforceContract(tc *TaskContract) error` per §2.7 |
| 10 | `NoTypedContracts` ablation behaviour | `internal/contract/validate.go` | `TestNoTypedContracts_SkipsEnforceButParsesJSON` (missing field → nil; invalid policy still rejects), `TestNoTypedContracts_LogsSkip` (`log.SetOutput` capture), `TestNoTypedContracts_EmitsEventWithPartialBitmap` (3/7 fixture under ablation) | Branch in `EnforceContract` (or in `Validate` proper) to skip required-field checks + RecoveryHint content checks; emit event from `Validate` on the not-rejected path; log line per call |
| 11 | `NoContractFormalization` ablation behaviour | `internal/contract/validate.go` | `TestEnforceContract_ContractEntirelyDisabled_ReturnsSentinel` (errors.Is matches `ErrContractFormalizationDisabled`) | Branch in `EnforceContract` (first check after ApplyDefaults is skipped) to return the sentinel |
| 12 | Wire entry tool to `EnforceContract` | `internal/driver/contract_tools.go` | `TestSubmitContractTaskHandler_FirstCallIsEnforce` (static AST pin), `TestContractToolsEntry_SchemaEnforceBeforeDispatch` (runtime t.Fatal pin), updated existing `TestSubmitContractTaskRoutesToSingleMatchingSlave` (and the other ~30 submit_contract_task tests) | Replace lines 49–52 of contract_tools.go with a single `contract.EnforceContract(&tc)` call; add `callNaturalLanguageFallback` helper per §4 |
| 13 | Fixture migration | `examples/dynamic-mcp/scenarios/*/request/*.json`, all Go-literal `TaskContract{}` test fixtures | All existing tests in `internal/contract/...`, `internal/driver/...`, `internal/orchestrator/...`, `internal/dispatch/...`, `examples/dynamic-mcp/...` | Add `"recovery_hint": "..."` to JSON; add `RecoveryHint: "..."` and `ReadArtifacts: []ArtifactRef{}` to Go literals — exhaustive enumeration in §3 |
| 14 | Fuzz | `internal/contract/fuzz_test.go` (NEW) | `FuzzValidate` (mutates JSON contract bytes, asserts no panic/timeout) | Add fuzz target with the §7 (e) seed corpus |
| 15 | Final guards | — | `go test -race -shuffle=on`, `go vet`, `gofmt -l`, `go test -fuzz=FuzzValidate -fuzztime=30s` | None — verification gate |

A failing make-pass step rolls back: the partial work from steps before
that step stays on disk (they have passing tests), but no further step
runs until the failing test is investigated and the implementation
adjusted.

---

## 3. Fixture migration — exhaustive list

Spec §5 says "test helpers that build `TaskContract` literals MUST be
updated". The exhaustive list below is the migration step's checklist. A
missed entry fails CI; an extra entry is harmless.

### 3.1 JSON fixtures (add `"recovery_hint": "..."` plus, if missing, `"read_artifacts": [...]`)

| Path | Insertion |
|------|-----------|
| `examples/dynamic-mcp/scenarios/driver-files-multi-mcp/request/contract.json` | Inside the top-level object, after `"capability_requirements"`: `,"recovery_hint": "Re-run is idempotent; outputs land under outputs/. Inspect manifest.json before retry."` |
| `examples/dynamic-mcp/scenarios/driver-clarify-contract/request/drafted_contract.json` | Inside `contract`, after `"capability_requirements"`: `,"recovery_hint": "If refund-risk-report.md is missing, re-run is safe (the report is the only output)."` |

### 3.2 Go-literal `TaskContract{}` test fixtures (add `RecoveryHint` field; add `ReadArtifacts` if currently absent)

| File | Function | Lines (current) | Change |
|------|----------|-----------------|--------|
| `internal/contract/contract_test.go` | `TestTaskContractApplyDefaults` | ~14–24 | Add `RecoveryHint: "hint"`, `DataContract.ReadArtifacts: []ArtifactRef{}` |
| `internal/contract/contract_test.go` | `TestTaskContractValidateRejectsMissingIntent` (and the 4 sibling reject tests at lines 106, 125, 147, 167, 191) | as listed | Add `RecoveryHint: "hint"` and explicit `ReadArtifacts: []ArtifactRef{}` to the constructed literal so the rejection under test is the documented one and not "you forgot recovery_hint" |
| `internal/contract/contract_test.go` | `TestEnvelopeRoundTrip` | ~247 | Add `RecoveryHint`, explicit empty `ReadArtifacts` |
| `internal/driver/tools_test.go` | `testTaskContract()` helper | ~165–178 | Add `RecoveryHint: "test recovery hint"`, `DataContract.ReadArtifacts: []ArtifactRef{}` — fixes all ~30 tests that call this helper |
| `internal/orchestrator/route_test.go` | `routeContractPrompt()` helper | ~177–201 | Add `RecoveryHint`, explicit `ReadArtifacts` |
| `internal/orchestrator/contract_policy_test.go` | `TestContractFromPromptRejectsAllowMasterFalse` | ~12–28 | Add `RecoveryHint`, `ReadArtifacts` |
| `internal/orchestrator/fanout_test.go` | `fanoutContractPrompt()` helper | ~923–939 | Add `RecoveryHint`, `ReadArtifacts` (the helper takes a `tc` parameter that the call at line 771 leaves zero-value, so the helper itself fills defaults if the caller didn't) |
| `internal/dispatch/dispatch_test.go` | `envelopedPrompt()` helper | ~178–195 | Add `RecoveryHint`, `ReadArtifacts` |
| `internal/driver/capability_tools.go` | `draftTaskContract` handler (lines 124–142) | code, not test | Add `RecoveryHint: ""` is wrong (would fail Validate). Instead: the draft tool's intent is to PRODUCE a contract with `recovery_hint` empty for the operator to fill. The migration here is more subtle — see §3.3 |

### 3.3 `draft_task_contract` tool: special handling

The `draft_task_contract` tool builds a contract from operator input and
returns it as JSON (no `Validate` call — `clarificationQuestions(tc)` is
the only downstream use). After this worktree, the drafted contract still
has `recovery_hint == ""`; we keep that behaviour because the whole point
of a draft is that the operator fills the missing pieces.

But: the drafted contract is then submitted via `submit_contract_task`,
which DOES call `EnforceContract`. An operator who forgets to fill
`recovery_hint` gets `ErrMissingFields` from the submit tool — which is
the correct user-visible behaviour and matches the spec.

Migration for `draft_task_contract`: add `"recovery_hint"` to the
`clarificationQuestions` list (the slice the tool returns alongside the
draft) with the literal text:
`"What is the recovery_hint? (key artifact paths + idempotency notes; see spec §2.1)"`.
This is the user-facing prompt to fill the field, not a Validate change.

Affected test: `TestDriverClarifyContractScenarioFixtures` in
`examples/dynamic-mcp/scenario_test.go` reads
`expected/clarification_questions.json` and compares — that fixture must
gain the recovery_hint question:

| Path | Change |
|------|--------|
| `examples/dynamic-mcp/scenarios/driver-clarify-contract/expected/clarification_questions.json` | Append the question above to the JSON array (matching the order produced by `clarificationQuestions(tc)`; verify by running the test once the migration is in) |

### 3.4 `online-driver-first-e2e` smoke binary

`dev/tmp/online-driver-first-e2e/main.go:210` calls `submit_contract_task`
with a hand-built JSON contract. Add `"recovery_hint": "e2e smoke recovery hint"` to its payload.

---

## 4. Test matrix — full enumeration

This is the table referenced by spec §6. Each row maps to (a) a test
function name we will write, (b) the spec section it verifies, and (c)
the security item from spec §7 it pins (if any). All tests live under
`internal/contract/contract_test.go` (the existing file), with a new
`internal/contract/completeness_test.go` for sink-specific tests and
`internal/contract/ablation_test.go` for the registration test, plus the
new `internal/driver/contract_tools_test.go` for entry-tool tests, and
`internal/contract/fuzz_test.go` for the fuzz target.

| # | Test name | Spec § | Security |
|---|-----------|--------|----------|
| T1 | `TestTaskContract_RecoveryHint_JSONRoundTrip` (Marshal then Unmarshal preserves the key even when empty) | 2.1, 5 | — |
| T2 | `TestValidate_FullContract_OK` (7+2 fields → nil error + event ratio 1.0) | 2.2, 2.6 | — |
| T3 | `TestValidate_MissingRecoveryHint_Reject` (errors.Is(err, ErrMissingFields); Missing contains exactly "recovery_hint") | 2.2, 2.5 | (a) |
| T4 | `TestValidate_MissingMultipleFields_AllReported` (5 fields missing, Missing slice has all 5 in §2.2 order, no duplicates) | 2.2, 2.5 | (a) |
| T5 | `TestValidate_MissingFieldsErrorString_BackwardCompat` (Error() includes literal "intent.goal is required" so legacy `strings.Contains` assertions still pass) | 2.5 | regression |
| T6 | `TestValidate_ReadArtifacts_NilFails` (Go-literal contract with `ReadArtifacts == nil`) | 2.2.1 | (a) |
| T7 | `TestValidate_ReadArtifacts_EmptyExplicitPasses` (Go-literal contract with `ReadArtifacts: []ArtifactRef{}`) | 2.2.1 | (a) |
| T8 | `TestValidate_ReadArtifacts_JSONExplicitEmptyPasses` (JSON `"read_artifacts": []` decodes and passes) | 2.2.1 | (a) |
| T9 | `TestValidate_ReadArtifacts_JSONMissingFails` (JSON missing the key decodes to nil and fails) | 2.2.1 | (a) |
| T10 | `TestValidate_RecoveryHint_TooLong_Reject` (4097 runes → ErrRecoveryHintTooLong; 4096 runes → nil) | 2.4 (a) | (b) |
| T11 | `TestValidate_RecoveryHint_TooLong_UsesRuneCount` (string of 4096 4-byte runes (`"𝟘"`*4096) passes; the byte-length check would fail it) | 2.4 (a) | (b) |
| T12 | `TestValidate_RecoveryHint_ControlChar_RejectBEL` (`\x07` → ErrRecoveryHintContainsControlChar) | 2.4 (b) | (b) |
| T13 | `TestValidate_RecoveryHint_ControlChar_RejectESC` (`\x1b` → reject) | 2.4 (b) | (b) |
| T14 | `TestValidate_RecoveryHint_ControlChar_RejectDEL` (`\x7f` → reject) | 2.4 (b) | (b) |
| T15 | `TestValidate_RecoveryHint_ControlChar_AllowsTabNewlineCR` (string containing `\t\n\r` passes — table-driven over the three) | 2.4 (b) | (b) |
| T16 | `TestValidate_RecoveryHint_HTMLPrefix_Reject` (table-driven over all 8 prefixes from spec §2.4 (c) + a mixed-case variant of each → ErrRecoveryHintLooksLikeHTML; total 16 cases) | 2.4 (c) | (b) |
| T17 | `TestValidate_RecoveryHint_LooksLikeXSSInBackticks_StillRejected` (current spec is strict — `\`<script>\`` IS rejected; pinned to make the rule's strictness explicit, NOT to claim backticks are an escape hatch) | 2.4 (c) | (b) |
| T18 | `TestValidate_RecoveryHint_PolicyValidationStillFires` (recovery_hint OK but `MaxDAGNodes = -1` → existing validatePolicy error, not a ValidationError) | 2.3 | regression |
| T19 | `TestContractToolsEntry_SchemaEnforceBeforeDispatch` (runtime: partial contract → handler errors; fakeSDK discoverFunc + delegateFunc both t.Fatal; observer relay save methods also t.Fatal) | 4, 7 (a) | (a) |
| T20 | `TestSubmitContractTaskHandler_FirstCallIsEnforce` (static: go/parser + ast.Walk + locate Call method on submitContractTaskTool, assert first non-IfStmt CallExpr after the json.Unmarshal error If is `contract.EnforceContract`) | 4, 7 (a) | (a) |
| T21 | `TestEnforceContract_AppliesDefaultsThenValidates` (calls EnforceContract on a TC with empty ExecutionPolicy.Routing; asserts Routing is now `direct_first` AND no error) | 2.7 | regression |
| T22 | `TestEnforceContract_ValidatesAfterDefaults` (calls EnforceContract on a TC missing intent.goal; asserts ErrMissingFields with goal listed) | 2.7 | (a) |
| T23 | `TestNoTypedContracts_SkipsEnforceButParsesJSON` (table: (a) missing intent.goal + DisableSchemaEnforce=true → nil; (b) `MaxDAGNodes = -1` + DisableSchemaEnforce=true → still errors via validatePolicy; (c) `RecoveryHint` length 9999 + DisableSchemaEnforce=true → nil) | 3.1 | (c) |
| T24 | `TestNoTypedContracts_LogsSkip` (`log.SetOutput(&buf)`; call EnforceContract under ablation; assert `buf.String()` contains `[ablation] NoTypedContracts: skipped enforce on conversation=<id>`; restore via t.Cleanup) | 3.1 | (c) |
| T25 | `TestNoTypedContracts_EmitsEventWithPartialBitmap` (3-field fixture under ablation → event PresentFields has 3 names, CompletenessRatio = 3.0/7.0) | 3.1, 2.6 | (c) |
| T26 | `TestNoContractFormalization_FallsBackButLogsDrop` (`log.SetOutput`; call submit_contract_task; assert log contains `[ablation] NoContractFormalization: dropped contract body on conversation=<id>`; assert response `route == "natural_language_fallback"`; assert NO completeness event emitted via spy sink) | 3.2 | (c) |
| T27 | `TestNoContractFormalization_FallbackBodySelection` (table: (a) Prompt non-empty → body = Prompt; (b) Prompt empty, Goal non-empty → body = `(contract formalization disabled) <goal>`; (c) Prompt empty, Goal empty → MCPToolError "no prompt and no intent.goal to delegate") | 3.2, 4 | (c) |
| T28 | `TestEnforceContract_BothAblations_ContractEntirelyWins` (both flags true; submit_contract_task called; asserts response route is `natural_language_fallback`, log contains "dropped contract body", log does NOT contain "skipped enforce") | 3.3 | (c) |
| T29 | `TestContractCompleteness_OnlyBitmap_NoBody` (spy sink; submit contract with UUID-laden goal/criteria/hint/write_target name; marshal captured event; assert JSON output does NOT contain any of those UUIDs) | 2.6, 7 (d) | (d) |
| T30 | `TestContractCompleteness_RatioDenominator7` (four-assertion test from spec §6: ratio==1.0, len(PresentFields)==7, completenessDenominator==7, separate 4/7 fixture within 1e-9) | 2.6 | (d) |
| T31 | `TestPresentFields_NilSliceAbsent` (ReadArtifacts nil → name not in bitmap) | 2.6 | (d) |
| T32 | `TestPresentFields_EmptyExplicitSlicePresent` (ReadArtifacts = []ArtifactRef{} → name IS in bitmap) | 2.6 | (d) |
| T33 | `TestPresentFields_StructAllZeroAbsent` (CapabilityRequirements zero-value → name not in bitmap) | 2.6 | (d) |
| T34 | `TestPresentFields_TrimmedStringAbsent` (Intent.Goal = "  \t  " → name not in bitmap) | 2.6 | (d) |
| T35 | `TestEventSink_SwapAtomic` (RegisterCompletenessSink swap mid-flight via two goroutines + atomic; -race clean) | 2.6 | regression |
| T36 | `TestEventSink_NilNoOp` (RegisterCompletenessSink(nil); Validate runs; no panic; no observable effect) | 2.6 | regression |
| T37 | `TestAblationRegisteredOnDefault` (after blank import of internal/contract, `ablation.Default.List()` contains both NoTypedContracts and NoContractFormalization) | 3 | integration |
| T38 | `FuzzValidate` (corpus per spec §7 (e): empty TC, full TC, RecoveryHint at 0/4096/4097/100000 runes, `\x00`*N, each of the 8 HTML prefixes, `MaxDAGNodes=-1`, `MaxConcurrency=0`, 10 MiB Intent.Goal; 30s run; no panic / no deadlock / bounded wall-time per iter) | 2.3, 2.6, 7 (e) | (e) |

Test infrastructure helpers (new, in `internal/contract/testutil_test.go`):

- `freshSink(t *testing.T) *spySink` — captures emitted events; registers via
  `RegisterCompletenessSink` and arranges `t.Cleanup` to restore the prior.
- `withAblationFlag(t *testing.T, flag *bool, v bool)` — sets the flag and
  arranges `t.Cleanup` to restore. Tests that touch ablation MUST go through
  this helper so flag leaks between tests are impossible.
- `captureLog(t *testing.T) *bytes.Buffer` — wraps `log.SetOutput` with
  `t.Cleanup` restore.

Concurrency on the ablation flag vars: spec §3 says the pattern is "set
once before workload start"; in tests we relax to "set + restore around a
single goroutine call" via the helper. Tests that touch the flag MUST NOT
run in parallel with other ablation tests (no `t.Parallel()` in those
specific tests). Tests that don't touch the flags MAY use `t.Parallel()`.

---

## 5. Wiring touchpoints (`internal/driver/contract_tools.go`)

The replacement structure (spec §4):

```go
func (s *submitContractTaskTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
    var args struct {
        Contract          contract.TaskContract `json:"contract"`
        Prompt            string                `json:"prompt"`
        TargetDisplayName string                `json:"target_display_name"`
        Skill             string                `json:"skill"`
        TimeoutSec        int                   `json:"timeout_sec"`
    }
    if err := json.Unmarshal(raw, &args); err != nil {
        return nil, &MCPToolError{Message: "invalid args: " + err.Error(), Category: observerstore.FailContractViolation}
    }
    tc := args.Contract
    if err := contract.EnforceContract(&tc); err != nil {
        if errors.Is(err, contract.ErrContractFormalizationDisabled) {
            return s.callNaturalLanguageFallback(ctx, tc, fallbackArgs{
                Prompt:            args.Prompt,
                TargetDisplayName: args.TargetDisplayName,
                Skill:             args.Skill,
                TimeoutSec:        args.TimeoutSec,
            })
        }
        return nil, &MCPToolError{Message: "invalid contract: " + err.Error(), Category: observerstore.FailContractViolation}
    }
    // ... existing code from line 54 onward (cards, snapshot, body, finalPrompt, needsBind, observer relay, dispatch) unchanged.
}
```

The existing `tc.ApplyDefaults()` on line 49 and `tc.Validate()` on line 50
are both REMOVED — `EnforceContract` does both. The downstream code paths
that depend on tc having defaults (line 60 `analyzeContractCapabilities`,
line 71 `EncodeEnvelope`) get the same defaulted tc because `EnforceContract`
mutated in place through the pointer receiver.

`callNaturalLanguageFallback` is a new method on `submitContractTaskTool`,
defined immediately after `Call`. Internal structure:

```go
type fallbackArgs struct { Prompt, TargetDisplayName, Skill string; TimeoutSec int }

func (s *submitContractTaskTool) callNaturalLanguageFallback(
    ctx context.Context, tc contract.TaskContract, args fallbackArgs,
) (json.RawMessage, error) {
    log.Printf("[ablation] NoContractFormalization: dropped contract body on conversation=%s", tc.ConversationID)
    body := strings.TrimSpace(args.Prompt)
    if body == "" {
        if strings.TrimSpace(tc.Intent.Goal) == "" {
            return nil, &MCPToolError{Message: "no prompt and no intent.goal to delegate", Category: observerstore.FailContractViolation}
        }
        body = "(contract formalization disabled) " + tc.Intent.Goal
    }
    cards, err := s.t.sdk.DiscoverAgents(ctx)
    if err != nil {
        return nil, &MCPToolError{Message: "discover agents: " + err.Error(), Category: observerstore.FailUnknown}
    }
    targetID, targetName, _, skill, _, err := s.selectTarget(ctx, cards, tc, args.TargetDisplayName, args.Skill)
    if err != nil {
        return nil, err
    }
    timeout := args.TimeoutSec
    if timeout == 0 {
        timeout = s.t.cfg.DriverDefaults.TaskTimeoutSec
    }
    resp, err := s.t.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
        TargetID: targetID, Skill: skill, Prompt: body, TimeoutSeconds: timeout,
    })
    if err != nil {
        return nil, &MCPToolError{Message: "delegate: " + err.Error(), Category: observerstore.FailUnknown}
    }
    return json.Marshal(map[string]interface{}{
        "task_id":             resp.TaskID,
        "target_id":           targetID,
        "target_display_name": targetName,
        "skill":               skill,
        "route":               "natural_language_fallback",
        "warnings":            []string{},
    })
}
```

The `SystemContext` is intentionally NOT populated in the fallback path —
the ablation point of "no contract" is also "no parent-link metadata"; an
operator who wants parent-link tracing under this ablation should not be
using the ablation.

---

## 6. Verification commands

The make-pass gate per spec §8. Run in order from
`/root/multi-agent/.worktrees/p1-contract-schema/multi-agent`:

```bash
# Build + vet
go vet ./...

# Format check (must produce no output)
gofmt -l internal/contract internal/driver

# Unit + integration tests with race + shuffle (race catches sink swap, shuffle catches order-dependent tests)
go test ./internal/contract/... ./internal/driver/... -count=1 -shuffle=on -race

# Fuzz (30s)
go test ./internal/contract/... -fuzz=FuzzValidate -fuzztime=30s -run=^$
```

Plus a broader regression sweep to catch fixture migration misses:

```bash
go test ./internal/orchestrator/... ./internal/dispatch/... -count=1 -race
go test ./examples/dynamic-mcp/... -count=1
```

Any non-zero exit fails the gate.

---

## 7. Risks and mitigations

| Risk | Mitigation |
|------|------------|
| Ablation flag leak between tests (one test sets DisableSchemaEnforce=true, next test sees it) | All ablation tests go through `withAblationFlag` helper that uses `t.Cleanup` to restore. No `t.Parallel()` in ablation tests. |
| Sink-swap test (T35) races with other tests sharing the package-level sink | `freshSink(t)` helper uses `t.Cleanup` to restore the prior sink atomically. -race catches any missed restoration. |
| Static AST test (T20) is fragile to harmless reformatting | The test asserts shape ("first CallExpr after the unmarshal-err IfStmt is a SelectorExpr `contract.EnforceContract`"), not exact bytes. Refactors that move logic around within the Call body fail loudly with a precise message. |
| `recovery_hint` migration to ~30 test fixtures is tedious and error-prone | The §3.2 table is exhaustive; missing fixtures fail loudly in the regression sweep (§6). Implementation order has fixture migration as a single dedicated step (step 13). |
| `read_artifacts` becoming required breaks `dev/tmp/online-driver-first-e2e/main.go` | Smoke binary is updated in §3.4. |
| Fuzz crash on a runtime-pathological input | Spec §7 (e) lists the seed corpus; crashes generate `testdata/fuzz/FuzzValidate/<hash>` which are checked in as regression seeds. The implementation responds with a defensive cap (e.g. on `Intent.Goal` length) only if the fuzz finds a concrete pathological pattern. |

---

## 8. Out-of-scope confirmations

Reaffirming spec §9:

- Wiring `EventSink` to the real observer relay over HTTP — §D2.
- CLI binding `--ablation NoTypedContracts` — Phase 2 WT-2-flag-integration.
- Reformulating `success_criteria` into a structured success-oracle —
  future spec, not this worktree.
- `draft_task_contract` filling `recovery_hint` automatically — operator
  responsibility; the tool prompts via clarification_questions (§3.3).
