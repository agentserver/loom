package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/contract"
)

// captureDriverLog rewires the stdlib log writer and restores on cleanup.
// Used by ablation tests that grep for "[ablation] ..." substrings.
func captureDriverLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	priorOut := log.Writer()
	priorFlags := log.Flags()
	priorPrefix := log.Prefix()
	log.SetOutput(buf)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(priorOut)
		log.SetFlags(priorFlags)
		log.SetPrefix(priorPrefix)
	})
	return buf
}

func withAblationFlagDriver(t *testing.T, flag *bool, v bool) {
	t.Helper()
	prior := *flag
	*flag = v
	t.Cleanup(func() { *flag = prior })
}

// T19 — partial contract must NOT reach DiscoverAgents or DelegateTask.
// Spec §7 (a) headline regression.
func TestContractToolsEntry_SchemaEnforceBeforeDispatch(t *testing.T) {
	var discoverCalled atomic.Int32
	var delegateCalled atomic.Int32
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			discoverCalled.Add(1)
			t.Fatal("DiscoverAgents must NOT be called when EnforceContract rejects a partial contract — §7 (a)")
			return nil, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			delegateCalled.Add(1)
			t.Fatal("DelegateTask must NOT be called when EnforceContract rejects a partial contract — §7 (a)")
			return nil, nil
		},
	}
	tools := newTestTools(t, sdk)
	_, err := tools.BindThread(context.Background(), "thr-enforce-before-dispatch")
	require.NoError(t, err)

	// Partial contract: only intent.goal set. Attacker-chosen skill
	// "admin" would route to a privileged slave if the validator were
	// bypassed.
	args, _ := json.Marshal(map[string]any{
		"contract": map[string]any{
			"version":         1,
			"conversation_id": "conv-partial",
			"intent": map[string]any{
				"goal":             "trigger admin slave",
				"success_criteria": []string{"the slave runs"},
			},
			"capability_requirements": map[string]any{"skills": []string{"admin"}},
			// data_contract missing entirely, recovery_hint missing
		},
	})

	out, err := submitContractToolForTest(t, tools).Call(context.Background(), args)
	require.Error(t, err, "partial contract MUST be rejected")
	require.Nil(t, out)

	var mcpErr *MCPToolError
	require.True(t, errors.As(err, &mcpErr), "expected *MCPToolError; got %T", err)
	require.Contains(t, mcpErr.Message, "invalid contract")

	// Side-effect counters: both must be zero. (t.Fatal already would
	// have failed the test if either ran; these counters are belt-and-
	// suspenders for clarity in the failure message.)
	require.Equal(t, int32(0), discoverCalled.Load(), "DiscoverAgents leaked through enforce gate")
	require.Equal(t, int32(0), delegateCalled.Load(), "DelegateTask leaked through enforce gate")
}

// T20 — static AST: the Call method's first non-If/non-decl statement
// after the json.Unmarshal error-return IfStmt must be a call to
// contract.EnforceContract. Spec §7 (a) refactor-resistance pin.
func TestSubmitContractTaskHandler_FirstCallIsEnforce(t *testing.T) {
	// Locate contract_tools.go relative to this _test.go file.
	wd, _ := filepathAbs(t)
	src := filepath.Join(wd, "contract_tools.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, src, nil, 0)
	require.NoError(t, err, "parse contract_tools.go")

	// Find the func (s *submitContractTaskTool) Call(...) method.
	var callBody *ast.BlockStmt
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Call" || fn.Recv == nil {
			continue
		}
		// Receiver must be *submitContractTaskTool.
		if !receiverIs(fn.Recv, "submitContractTaskTool") {
			continue
		}
		callBody = fn.Body
		break
	}
	require.NotNil(t, callBody, "Call method on *submitContractTaskTool not found")

	// Walk TOP-LEVEL statements only (not descending into bodies of
	// if/for/etc.). The expected shape is:
	//   stmt[i]   *ast.IfStmt — Init contains json.Unmarshal(...)
	//   stmt[i+1] *ast.AssignStmt — tc := args.Contract
	//   stmt[i+2] *ast.IfStmt — Init contains contract.EnforceContract(&tc)
	// We find the first IfStmt whose Init has json.Unmarshal, then
	// require the very next IfStmt's Init to call contract.EnforceContract.
	// Statements between the unmarshal-If and the enforce-If may only be
	// simple assignments / decls — no calls (no DiscoverAgents, no logs,
	// no observer writes between unmarshal and enforce, per §7 (a)).
	var unmarshalIdx int = -1
	for i, stmt := range callBody.List {
		ifStmt, ok := stmt.(*ast.IfStmt)
		if !ok || ifStmt.Init == nil {
			continue
		}
		if initContainsCall(ifStmt.Init, "json", "Unmarshal") {
			unmarshalIdx = i
			break
		}
	}
	require.GreaterOrEqual(t, unmarshalIdx, 0, "no top-level IfStmt with json.Unmarshal init found in Call body")

	// Inspect statements after the unmarshal-If for two things:
	//   (a) no call expression appears in any statement BEFORE the
	//       enforce-If (so we forbid an extra DiscoverAgents or
	//       observer call slipping above the guard);
	//   (b) the next IfStmt's Init MUST call contract.EnforceContract.
	var enforceFound bool
	for j := unmarshalIdx + 1; j < len(callBody.List); j++ {
		stmt := callBody.List[j]
		if ifStmt, ok := stmt.(*ast.IfStmt); ok && ifStmt.Init != nil {
			if initContainsCall(ifStmt.Init, "contract", "EnforceContract") {
				enforceFound = true
				break
			}
			t.Fatalf("first IfStmt after json.Unmarshal does not call contract.EnforceContract; init = %#v", ifStmt.Init)
		}
		// Non-If statement: must not contain a call expression
		// (other than allowed simple `tc := args.Contract`-style
		// assignments). Use ast.Inspect on this single statement.
		ast.Inspect(stmt, func(n ast.Node) bool {
			if _, ok := n.(*ast.CallExpr); ok {
				t.Fatalf("call expression between json.Unmarshal and contract.EnforceContract is forbidden (§7 (a)); stmt=%v", stmt)
			}
			return true
		})
	}
	require.True(t, enforceFound, "contract.EnforceContract IfStmt not found after json.Unmarshal IfStmt")
}

func receiverIs(recv *ast.FieldList, typeName string) bool {
	if recv == nil || len(recv.List) == 0 {
		return false
	}
	star, ok := recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		// Value receiver — also fine, but our method uses pointer.
		ident, ok := recv.List[0].Type.(*ast.Ident)
		return ok && ident.Name == typeName
	}
	ident, ok := star.X.(*ast.Ident)
	return ok && ident.Name == typeName
}

func isSelector(e ast.Expr, pkg, sym string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return ident.Name == pkg && sel.Sel.Name == sym
}

// initContainsCall reports whether init contains a CallExpr whose Fun
// is the SelectorExpr `pkg.sym`. Used for matching IfStmt initialisers
// like `if err := json.Unmarshal(...); err != nil { ... }` and
// `if err := contract.EnforceContract(&tc); err != nil { ... }`.
func initContainsCall(init ast.Stmt, pkg, sym string) bool {
	found := false
	ast.Inspect(init, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && isSelector(call.Fun, pkg, sym) {
			found = true
			return false
		}
		return true
	})
	return found
}

// T26 — NoContractFormalization log line + fallback route + no completeness event.
func TestNoContractFormalization_FallsBackButLogsDrop(t *testing.T) {
	withAblationFlagDriver(t, &contract.DisableContractEntirely, true)
	buf := captureDriverLog(t)

	var delegated agentsdk.DelegateTaskRequest
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "sbx-driver", DisplayName: "driver", Status: "available", Card: json.RawMessage(`{"skills":[]}`)},
				{AgentID: "slave-a", DisplayName: "slave-a", Status: "available", Card: json.RawMessage(`{"skills":["chat"],"short_id":"sa"}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			delegated = req
			return &agentsdk.DelegateTaskResponse{TaskID: "task-fb"}, nil
		},
	}
	tools := newTestTools(t, sdk)

	// Spy on the completeness sink to assert no emit.
	priorSink := contract.RegisterCompletenessSink(&assertNoEmitSink{t: t})
	t.Cleanup(func() { contract.RegisterCompletenessSink(priorSink) })

	// Contract is partial (no recovery_hint) — would normally fail
	// EnforceContract — but DisableContractEntirely short-circuits to
	// fallback before that check, regardless of contract validity.
	args, _ := json.Marshal(map[string]any{
		"contract": map[string]any{
			"version":         1,
			"conversation_id": "conv-fb-1",
			"intent": map[string]any{
				"goal":             "do work via fallback",
				"success_criteria": []string{"finishes"},
			},
		},
		"prompt":              "raw natural-language prompt",
		"skill":               "chat",
		"target_display_name": "slave-a",
	})

	out, err := submitContractToolForTest(t, tools).Call(context.Background(), args)
	require.NoError(t, err)
	require.NotNil(t, out)

	// Response shape: route == "natural_language_fallback".
	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(out, &resp))
	require.Equal(t, "natural_language_fallback", resp["route"])
	require.Equal(t, "task-fb", resp["task_id"])

	// Delegated prompt is the raw operator prompt (no envelope).
	require.Equal(t, "raw natural-language prompt", delegated.Prompt)

	// Log line MUST contain the §3.2 + §7 (c) substrings.
	got := buf.String()
	require.Contains(t, got, "[ablation] NoContractFormalization: dropped contract body")
	require.Contains(t, got, `conversation="conv-fb-1"`)
}

// TestNoContractFormalization_LogIsInjectionResistant pins that a
// malicious conversation_id with embedded newlines cannot forge a
// second "[ablation] ..." line in the audit trail. See
// internal/contract/ablation_test.go::TestNoTypedContracts_LogIsInjectionResistant
// for the same mitigation on the contract-package log line. Round-3
// review P1-2.
func TestNoContractFormalization_LogIsInjectionResistant(t *testing.T) {
	withAblationFlagDriver(t, &contract.DisableContractEntirely, true)
	buf := captureDriverLog(t)

	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "sbx-driver", DisplayName: "driver", Status: "available", Card: json.RawMessage(`{"skills":[]}`)},
				{AgentID: "slave-a", DisplayName: "slave-a", Status: "available", Card: json.RawMessage(`{"skills":["chat"],"short_id":"sa"}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			return &agentsdk.DelegateTaskResponse{TaskID: "t-inj"}, nil
		},
	}
	tools := newTestTools(t, sdk)

	args, _ := json.Marshal(map[string]any{
		"contract": map[string]any{
			"version":         1,
			"conversation_id": "conv-attack\n[ablation] FAKE: dropped contract body on conversation=evil",
			"intent":          map[string]any{"goal": "do", "success_criteria": []string{"done"}},
		},
		"prompt":              "raw",
		"skill":               "chat",
		"target_display_name": "slave-a",
	})
	_, err := submitContractToolForTest(t, tools).Call(context.Background(), args)
	require.NoError(t, err)

	got := buf.String()
	// The forgery test: an attacker tried to inject a SECOND line that
	// starts with "[ablation]". We assert only ONE line in the output
	// starts with that token. (The string `[ablation]` appears twice
	// inside the captured log — once at line-start as the real entry,
	// once inside the %q-escaped quoted attacker payload — that second
	// occurrence is NOT at the start of a line, so it cannot be
	// confused with a real ablation event by a line-oriented log
	// grep.) The %q escape collapses the attacker's "\n" to two
	// literal characters, so the whole payload stays on one physical
	// line.
	lines := strings.Split(got, "\n")
	starts := 0
	for _, ln := range lines {
		if strings.HasPrefix(ln, "[ablation]") {
			starts++
		}
	}
	if starts != 1 {
		t.Errorf("audit log contains %d lines starting with [ablation], want exactly 1 — possible log injection; full log:\n%s",
			starts, got)
	}
	// Sanity: the attacker payload IS in the log (we captured it), but
	// escaped as literal "\n[ablation] FAKE".
	if !strings.Contains(got, `\n[ablation] FAKE`) {
		t.Errorf("attacker payload not properly escaped — expected literal \\n escape; got:\n%s", got)
	}
}

// T27 — body-selection table per spec §4 fallback rules.
func TestNoContractFormalization_FallbackBodySelection(t *testing.T) {
	withAblationFlagDriver(t, &contract.DisableContractEntirely, true)
	captureDriverLog(t)

	cases := []struct {
		name      string
		prompt    string
		goal      string
		wantBody  string
		wantError bool
	}{
		{name: "prompt non-empty wins", prompt: "use this", goal: "fallback goal", wantBody: "use this"},
		{name: "prompt empty, goal used", prompt: "", goal: "salvage me", wantBody: "(contract formalization disabled) salvage me"},
		{name: "both empty errors", prompt: "", goal: "", wantError: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var delegated agentsdk.DelegateTaskRequest
			sdk := &fakeSDK{
				discoverFunc: func() ([]agentsdk.AgentCard, error) {
					return []agentsdk.AgentCard{
						{AgentID: "sbx-driver", DisplayName: "driver", Status: "available", Card: json.RawMessage(`{"skills":[]}`)},
						{AgentID: "slave-a", DisplayName: "slave-a", Status: "available", Card: json.RawMessage(`{"skills":["chat"],"short_id":"sa"}`)},
					}, nil
				},
				delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
					delegated = req
					return &agentsdk.DelegateTaskResponse{TaskID: "t-1"}, nil
				},
			}
			tools := newTestTools(t, sdk)

			args, _ := json.Marshal(map[string]any{
				"contract": map[string]any{
					"version":         1,
					"conversation_id": "conv-body-sel",
					"intent": map[string]any{
						"goal":             tc.goal,
						"success_criteria": []string{"x"},
					},
				},
				"prompt":              tc.prompt,
				"skill":               "chat",
				"target_display_name": "slave-a",
			})
			_, err := submitContractToolForTest(t, tools).Call(context.Background(), args)
			if tc.wantError {
				require.Error(t, err)
				var mcpErr *MCPToolError
				require.True(t, errors.As(err, &mcpErr))
				require.Contains(t, mcpErr.Message, "no prompt and no intent.goal to delegate")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantBody, delegated.Prompt)
		})
	}
}

// T28 — both ablations: NoContractFormalization wins.
func TestEnforceContract_BothAblations_ContractEntirelyWins(t *testing.T) {
	withAblationFlagDriver(t, &contract.DisableSchemaEnforce, true)
	withAblationFlagDriver(t, &contract.DisableContractEntirely, true)
	buf := captureDriverLog(t)

	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "sbx-driver", DisplayName: "driver", Status: "available", Card: json.RawMessage(`{"skills":[]}`)},
				{AgentID: "slave-a", DisplayName: "slave-a", Status: "available", Card: json.RawMessage(`{"skills":["chat"],"short_id":"sa"}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			return &agentsdk.DelegateTaskResponse{TaskID: "t-both"}, nil
		},
	}
	tools := newTestTools(t, sdk)

	args, _ := json.Marshal(map[string]any{
		"contract": map[string]any{
			"version":         1,
			"conversation_id": "conv-both",
			"intent":          map[string]any{"goal": "do", "success_criteria": []string{"done"}},
		},
		"prompt":              "raw",
		"skill":               "chat",
		"target_display_name": "slave-a",
	})

	out, err := submitContractToolForTest(t, tools).Call(context.Background(), args)
	require.NoError(t, err)

	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(out, &resp))
	require.Equal(t, "natural_language_fallback", resp["route"])

	got := buf.String()
	require.Contains(t, got, "dropped contract body")
	require.NotContains(t, got, "skipped enforce",
		"NoContractFormalization wins — NoTypedContracts skip line must NOT appear because Validate is never reached")
}

// TestDraftTaskContract_NullResourcesProducesValidContract pins
// PR #52 round-3 review P1-1: an operator who passes
// `"resources": null` to the draft tool gets `[]byte("null")` (4
// bytes, non-nil) at the args struct level. Without the
// "effectively absent" extension in the default-fill condition, the
// drafted contract carries nil Skills + nil Tools + non-nil
// `[]byte("null")` Resources, which `resourcesDeclared` rejects —
// the operator's next submit_contract_task fails with the cryptic
// "capability_requirements is required". This test pins the fix.
func TestDraftTaskContract_NullResourcesProducesValidContract(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) { return nil, nil },
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) { return nil, nil },
	}
	tools := newTestTools(t, sdk)
	draftTool := toolByName(t, tools, "draft_task_contract")

	for _, payload := range []string{
		`{"goal":"x","resources":null}`,
		`{"goal":"x","resources":{}}`,
		`{"goal":"x"}`, // baseline (already covered by sibling test) — re-verified here
	} {
		t.Run(payload, func(t *testing.T) {
			draftRaw, err := draftTool.Call(context.Background(), json.RawMessage(payload))
			require.NoError(t, err)
			var draftResp struct {
				Contract contract.TaskContract `json:"contract"`
			}
			require.NoError(t, json.Unmarshal(draftRaw, &draftResp))
			draftResp.Contract.RecoveryHint = "operator-filled"
			require.NoError(t, contract.EnforceContract(&draftResp.Contract),
				"drafted contract must pass EnforceContract after operator fills recovery_hint; payload=%s", payload)
		})
	}
}

// TestDraftTaskContract_OutputPassesEnforceContract pins the round-trip
// invariant: the contract emitted by draft_task_contract MUST be
// acceptable to EnforceContract without any further operator edits
// beyond a recovery_hint (which the draft tool leaves empty and prompts
// the operator to fill via clarification_questions).
//
// Regression: pre-fix, the draft tool left ReadArtifacts as a nil
// slice; the operator's subsequent submit_contract_task call would
// then fail with "data_contract.read_artifacts is required" — a poor
// UX hazard caught by PR review.
func TestDraftTaskContract_OutputPassesEnforceContract(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) { return nil, nil },
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) { return nil, nil },
	}
	tools := newTestTools(t, sdk)
	draftTool := toolByName(t, tools, "draft_task_contract")

	// Minimum-input draft: only goal. All other fields take defaults.
	draftRaw, err := draftTool.Call(context.Background(),
		json.RawMessage(`{"goal":"do a thing"}`))
	require.NoError(t, err)

	var draftResp struct {
		Contract               contract.TaskContract `json:"contract"`
		ClarificationQuestions []string              `json:"clarification_questions"`
	}
	require.NoError(t, json.Unmarshal(draftRaw, &draftResp))

	// The drafted contract intentionally leaves recovery_hint empty so
	// the operator fills it via the clarification flow. Fill it here to
	// simulate a fully-completed-then-submitted contract.
	require.Empty(t, draftResp.Contract.RecoveryHint,
		"draft tool should NOT auto-fill recovery_hint (operator must own this)")
	draftResp.Contract.RecoveryHint = "fill-in by operator before submit"

	// EnforceContract should now accept the contract. Pre-fix this
	// failed because ReadArtifacts was a nil slice.
	require.NoError(t, contract.EnforceContract(&draftResp.Contract),
		"draft tool output must be valid after operator fills recovery_hint")

	// And the clarification flow must explicitly tell the operator
	// recovery_hint is required.
	var sawRecoveryHintQuestion bool
	for _, q := range draftResp.ClarificationQuestions {
		if strings.Contains(q, "recovery_hint") {
			sawRecoveryHintQuestion = true
			break
		}
	}
	require.True(t, sawRecoveryHintQuestion,
		"clarification_questions must mention recovery_hint; got: %v", draftResp.ClarificationQuestions)
}

// --- helpers ---

// filepathAbs returns the absolute path of the test file's directory.
// Used by the static-AST test to locate contract_tools.go regardless
// of `go test`'s working directory.
func filepathAbs(t *testing.T) (string, error) {
	t.Helper()
	// `go test` runs with the package directory as cwd, so contract_tools.go
	// is a sibling.
	wd, err := os.Getwd()
	require.NoError(t, err)
	return wd, nil
}

// assertNoEmitSink fails the test if EmitContractCompleteness is called.
type assertNoEmitSink struct {
	t *testing.T
	// no mutex needed — t.Fatal terminates the test goroutine.
}

func (a *assertNoEmitSink) EmitContractCompleteness(ev contract.ContractCompletenessEvent) {
	a.t.Fatalf("completeness event emitted in NoContractFormalization fallback path: %+v", ev)
}

// Imports we want to ensure are referenced (otherwise gofmt thinks we
// have unused imports if the test file is heavily edited later).
var (
	_ = strings.Contains
	_ = sync.Mutex{}
)
