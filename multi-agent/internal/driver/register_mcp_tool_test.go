package driver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/buildspec"
)

// validTestSpec returns a minimal buildspec.Spec JSON payload that passes Validate.
func validTestSpecJSON() string {
	return `{
		"name": "mytool",
		"description": "A test MCP server",
		"tools": [{"name":"do_thing","description":"does a thing","args_schema":{"type":"object"},"result_description":"result"}]
	}`
}

// validAuditFieldsJSON returns the four B6-required audit fields as a
// JSON fragment (leading comma; caller splices into args). All four
// values match the promotionaudit regexes and the enum.
const validAuditFieldsJSON = `,"promoted_by_user_id":"user_abcdef","driver_thread_id":"thread_01_a","promotion_reason":"explicit_user_request","candidate_source_task_id":"task_12345678"`

func TestRegisterSlaveMCP_DelegatesAsRegisterMCPSkill(t *testing.T) {
	var delegated agentsdk.DelegateTaskRequest
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-b", DisplayName: "slave-b", Status: "available", Card: json.RawMessage(`{"skills":["chat","register_mcp"],"short_id":"sb"}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			delegated = req
			return &agentsdk.DelegateTaskResponse{TaskID: "task-reg-1"}, nil
		},
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{
				TaskID: "task-reg-1",
				Status: "completed",
				Result: json.RawMessage(`"registered"`),
			}, nil
		},
	}
	tool := toolByName(t, newTestTools(t, sdk), "register_slave_mcp")
	args := `{"target_display_name":"slave-b","spec":` + validTestSpecJSON() + `,"source_path":"dist/mytool.js","timeout_sec":60` + validAuditFieldsJSON + `}`
	out, err := tool.Call(context.Background(), json.RawMessage(args))
	require.NoError(t, err)
	require.Equal(t, "slave-b", delegated.TargetID)
	require.Equal(t, "register_mcp", delegated.Skill)
	// Prompt must contain spec name and source_path.
	require.Contains(t, delegated.Prompt, "mytool")
	require.Contains(t, delegated.Prompt, "dist/mytool.js")
	// Result must mention the task id.
	require.Contains(t, string(out), "task-reg-1")
}

func TestRegisterSlaveMCP_RejectsSlaveWithoutSkill(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-c", DisplayName: "slave-c", Status: "available", Card: json.RawMessage(`{"skills":["chat"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			t.Fatalf("must not delegate when register_mcp skill is missing")
			return nil, nil
		},
	}
	tool := toolByName(t, newTestTools(t, sdk), "register_slave_mcp")
	args := `{"target_display_name":"slave-c","spec":` + validTestSpecJSON() + `,"source_path":"dist/mytool.js"` + validAuditFieldsJSON + `}`
	_, err := tool.Call(context.Background(), json.RawMessage(args))
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not advertise register_mcp")
}

func TestRegisterSlaveMCP_RejectsEmptySourcePath(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-d", DisplayName: "slave-d", Status: "available", Card: json.RawMessage(`{"skills":["register_mcp"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			t.Fatalf("must not delegate with empty source_path")
			return nil, nil
		},
	}
	tool := toolByName(t, newTestTools(t, sdk), "register_slave_mcp")
	args := `{"target_display_name":"slave-d","spec":` + validTestSpecJSON() + `,"source_path":""` + validAuditFieldsJSON + `}`
	_, err := tool.Call(context.Background(), json.RawMessage(args))
	require.Error(t, err)
	require.Contains(t, err.Error(), "source_path is required")
}

func TestRegisterSlaveMCP_RejectsInvalidSpec(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-e", DisplayName: "slave-e", Status: "available", Card: json.RawMessage(`{"skills":["register_mcp"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			t.Fatalf("must not delegate with invalid spec")
			return nil, nil
		},
	}
	tool := toolByName(t, newTestTools(t, sdk), "register_slave_mcp")
	// "X bad" contains a space and uppercase — does not match [a-z][a-z0-9_]{0,31}
	args := `{"target_display_name":"slave-e","spec":{"name":"X bad","description":"d","tools":[{"name":"t","description":"d","args_schema":{"type":"object"},"result_description":"r"}]},"source_path":"dist/x.js"` + validAuditFieldsJSON + `}`
	_, err := tool.Call(context.Background(), json.RawMessage(args))
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid spec")
}

// --- B6 audit-field guard tests. -------------------------------------

// registerAuditGuardSDK returns an sdk that fails the test if it opens
// a delegate — the audit precheck must reject BEFORE any slave-side
// side-effect (spec §7 (a)).
func registerAuditGuardSDK(t *testing.T) *fakeSDK {
	return &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-x", DisplayName: "slave-x", Status: "available", Card: json.RawMessage(`{"skills":["register_mcp"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			t.Fatalf("audit precheck must reject before delegate")
			return nil, nil
		},
	}
}

// registerAuditGuardTools wraps newTestTools with a workspace set so
// the field-order-agnostic guard tests exercise the field they intend
// to test (not workspace_id-missing, which is a separate concern).
func registerAuditGuardTools(t *testing.T, sdk *fakeSDK) *Tools {
	tools := newTestTools(t, sdk)
	tools.cfg.Observer.WorkspaceID = "ws-abc12345"
	return tools
}

func TestRegisterSlaveMCP_RequiresAllFourAuditFields_MissingUserID(t *testing.T) {
	sdk := registerAuditGuardSDK(t)
	tool := toolByName(t, registerAuditGuardTools(t, sdk), "register_slave_mcp")
	// Omit promoted_by_user_id.
	args := `{"target_display_name":"slave-x","spec":` + validTestSpecJSON() + `,"source_path":"dist/x.js","driver_thread_id":"thread_01_a","promotion_reason":"explicit_user_request","candidate_source_task_id":"task_12345678"}`
	_, err := tool.Call(context.Background(), json.RawMessage(args))
	require.Error(t, err)
	require.Contains(t, err.Error(), "promoted_by_user_id")
}

func TestRegisterSlaveMCP_RequiresAllFourAuditFields_MissingThreadID(t *testing.T) {
	sdk := registerAuditGuardSDK(t)
	tool := toolByName(t, registerAuditGuardTools(t, sdk), "register_slave_mcp")
	args := `{"target_display_name":"slave-x","spec":` + validTestSpecJSON() + `,"source_path":"dist/x.js","promoted_by_user_id":"user_abcdef","promotion_reason":"explicit_user_request","candidate_source_task_id":"task_12345678"}`
	_, err := tool.Call(context.Background(), json.RawMessage(args))
	require.Error(t, err)
	require.Contains(t, err.Error(), "driver_thread_id")
}

func TestRegisterSlaveMCP_RequiresAllFourAuditFields_MissingReason(t *testing.T) {
	sdk := registerAuditGuardSDK(t)
	tool := toolByName(t, registerAuditGuardTools(t, sdk), "register_slave_mcp")
	args := `{"target_display_name":"slave-x","spec":` + validTestSpecJSON() + `,"source_path":"dist/x.js","promoted_by_user_id":"user_abcdef","driver_thread_id":"thread_01_a","candidate_source_task_id":"task_12345678"}`
	_, err := tool.Call(context.Background(), json.RawMessage(args))
	require.Error(t, err)
	require.Contains(t, err.Error(), "promotion_reason")
}

func TestRegisterSlaveMCP_RequiresAllFourAuditFields_MissingCandidateTaskID(t *testing.T) {
	sdk := registerAuditGuardSDK(t)
	tool := toolByName(t, registerAuditGuardTools(t, sdk), "register_slave_mcp")
	args := `{"target_display_name":"slave-x","spec":` + validTestSpecJSON() + `,"source_path":"dist/x.js","promoted_by_user_id":"user_abcdef","driver_thread_id":"thread_01_a","promotion_reason":"explicit_user_request"}`
	_, err := tool.Call(context.Background(), json.RawMessage(args))
	require.Error(t, err)
	require.Contains(t, err.Error(), "candidate_source_task_id")
}

func TestRegisterSlaveMCP_RejectsMalformedUserID(t *testing.T) {
	sdk := registerAuditGuardSDK(t)
	tool := toolByName(t, registerAuditGuardTools(t, sdk), "register_slave_mcp")
	args := `{"target_display_name":"slave-x","spec":` + validTestSpecJSON() + `,"source_path":"dist/x.js","promoted_by_user_id":"x","driver_thread_id":"thread_01_a","promotion_reason":"explicit_user_request","candidate_source_task_id":"task_12345678"}`
	_, err := tool.Call(context.Background(), json.RawMessage(args))
	require.Error(t, err)
	require.Contains(t, err.Error(), "promoted_by_user_id")
}

func TestRegisterSlaveMCP_RejectsMalformedThreadID(t *testing.T) {
	sub := []struct {
		name  string
		value string
	}{
		{"space", "th 01aaaa"},
		{"sql_meta", "th'--xxxx"},
	}
	for _, tc := range sub {
		t.Run(tc.name, func(t *testing.T) {
			sdk := registerAuditGuardSDK(t)
			tool := toolByName(t, registerAuditGuardTools(t, sdk), "register_slave_mcp")
			args := `{"target_display_name":"slave-x","spec":` + validTestSpecJSON() + `,"source_path":"dist/x.js","promoted_by_user_id":"user_abcdef","driver_thread_id":"` + tc.value + `","promotion_reason":"explicit_user_request","candidate_source_task_id":"task_12345678"}`
			_, err := tool.Call(context.Background(), json.RawMessage(args))
			require.Error(t, err)
			require.Contains(t, err.Error(), "driver_thread_id")
		})
	}
}

func TestRegisterSlaveMCP_RejectsMalformedCandidateTaskID(t *testing.T) {
	sub := []struct {
		name  string
		value string
	}{
		{"path_traversal", "../etc/xxxx"},
		{"too_short", "abcdefg"},
	}
	for _, tc := range sub {
		t.Run(tc.name, func(t *testing.T) {
			sdk := registerAuditGuardSDK(t)
			tool := toolByName(t, registerAuditGuardTools(t, sdk), "register_slave_mcp")
			args := `{"target_display_name":"slave-x","spec":` + validTestSpecJSON() + `,"source_path":"dist/x.js","promoted_by_user_id":"user_abcdef","driver_thread_id":"thread_01_a","promotion_reason":"explicit_user_request","candidate_source_task_id":"` + tc.value + `"}`
			_, err := tool.Call(context.Background(), json.RawMessage(args))
			require.Error(t, err)
			require.Contains(t, err.Error(), "candidate_source_task_id")
		})
	}
}

func TestRegisterSlaveMCP_RejectsUnknownReason(t *testing.T) {
	sdk := registerAuditGuardSDK(t)
	tool := toolByName(t, registerAuditGuardTools(t, sdk), "register_slave_mcp")
	args := `{"target_display_name":"slave-x","spec":` + validTestSpecJSON() + `,"source_path":"dist/x.js","promoted_by_user_id":"user_abcdef","driver_thread_id":"thread_01_a","promotion_reason":"garbage","candidate_source_task_id":"task_12345678"}`
	_, err := tool.Call(context.Background(), json.RawMessage(args))
	require.Error(t, err)
	require.Contains(t, err.Error(), "promotion_reason")
}

// TestRegisterSlaveMCP_HappyPath_WritesAuditRow — full valid call
// against an in-memory promotionaudit writer, assert one row lands.
func TestRegisterSlaveMCP_HappyPath_WritesAuditRow(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-b", DisplayName: "slave-b", Status: "available", Card: json.RawMessage(`{"skills":["chat","register_mcp"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			return &agentsdk.DelegateTaskResponse{TaskID: "task-reg-9"}, nil
		},
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{TaskID: "task-reg-9", Status: "completed", Result: json.RawMessage(`"ok"`)}, nil
		},
	}
	tools := newTestTools(t, sdk)
	tools.cfg.Observer.WorkspaceID = "ws-abc12345"
	rec := &recordingWriter{}
	tools.SetPromotionAuditWriter(rec)
	resetRegistryForTest()

	tool := toolByName(t, tools, "register_slave_mcp")
	args := `{"target_display_name":"slave-b","spec":` + validTestSpecJSON() + `,"source_path":"dist/mytool.js"` + validAuditFieldsJSON + `}`
	_, err := tool.Call(context.Background(), json.RawMessage(args))
	require.NoError(t, err)
	require.Len(t, rec.rows, 1)
	row := rec.rows[0]
	require.Equal(t, "mytool", row.MCPName)
	require.Equal(t, "user_abcdef", row.PromotedByUserID)
	require.Equal(t, "thread_01_a", row.DriverThreadID)
	require.Equal(t, "task_12345678", row.CandidateSourceTaskID)
	require.NotEqual(t, "", row.RegistryHashAfter)
	require.NotEqual(t, emptyBytesSHA256Hex, row.RegistryHashAfter)
}

// TestRegisterSlaveMCP_HappyPath_PublishesRegistryHash — after
// register, driver.LastRegistryHash() != the empty-bytes sha256.
func TestRegisterSlaveMCP_HappyPath_PublishesRegistryHash(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-b", DisplayName: "slave-b", Status: "available", Card: json.RawMessage(`{"skills":["chat","register_mcp"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			return &agentsdk.DelegateTaskResponse{TaskID: "task-reg-10"}, nil
		},
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{TaskID: "task-reg-10", Status: "completed", Result: json.RawMessage(`"ok"`)}, nil
		},
	}
	tools := newTestTools(t, sdk)
	tools.cfg.Observer.WorkspaceID = "ws-abc12345"
	tools.SetPromotionAuditWriter(&recordingWriter{})
	resetRegistryForTest()

	tool := toolByName(t, tools, "register_slave_mcp")
	args := `{"target_display_name":"slave-b","spec":` + validTestSpecJSON() + `,"source_path":"dist/mytool.js"` + validAuditFieldsJSON + `}`
	_, err := tool.Call(context.Background(), json.RawMessage(args))
	require.NoError(t, err)
	if LastRegistryHash() == emptyBytesSHA256Hex {
		t.Fatal("LastRegistryHash should have advanced past empty-bytes after register")
	}
}

// TestRegisterCore_DoesNotWriteAudit — registerCore is the
// stage-3 entry point for the B2 pipeline; it MUST NOT write an
// audit row itself.
func TestRegisterCore_DoesNotWriteAudit(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-b", DisplayName: "slave-b", Status: "available", Card: json.RawMessage(`{"skills":["chat","register_mcp"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			return &agentsdk.DelegateTaskResponse{TaskID: "task-core"}, nil
		},
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{TaskID: id, Status: "completed", Result: json.RawMessage(`"ok"`)}, nil
		},
	}
	tools := newTestTools(t, sdk)
	rec := &recordingWriter{}
	tools.SetPromotionAuditWriter(rec)
	resetRegistryForTest()

	spec := buildspec.Normalize(buildspec.Spec{
		Name:        "mytool",
		Description: "d",
		Tools: []buildspec.ToolSpec{
			{Name: "do_thing", Description: "d", ArgsSchema: json.RawMessage(`{"type":"object"}`), ResultDescription: "r"},
		},
	})
	result, err := tools.registerCore(context.Background(), registerCoreArgs{
		TargetDisplayName: "slave-b",
		Spec:              spec,
		SourcePath:        "dist/mytool.js",
	}, "test_caller")
	require.NoError(t, err)
	require.Len(t, rec.rows, 0, "registerCore MUST NOT write an audit row")
	require.NotEmpty(t, result.RegistryHash)
}

// TestRegisterCore_ReturnsRegistryHash — happy path returns
// non-empty 64-hex.
func TestRegisterCore_ReturnsRegistryHash(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-b", DisplayName: "slave-b", Status: "available", Card: json.RawMessage(`{"skills":["register_mcp"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			return &agentsdk.DelegateTaskResponse{TaskID: "task-core-2"}, nil
		},
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{TaskID: id, Status: "completed", Result: json.RawMessage(`"ok"`)}, nil
		},
	}
	tools := newTestTools(t, sdk)
	resetRegistryForTest()

	spec := buildspec.Normalize(buildspec.Spec{
		Name:        "othertool",
		Description: "d",
		Tools: []buildspec.ToolSpec{
			{Name: "op", Description: "d", ArgsSchema: json.RawMessage(`{"type":"object"}`), ResultDescription: "r"},
		},
	})
	result, err := tools.registerCore(context.Background(), registerCoreArgs{
		TargetDisplayName: "slave-b",
		Spec:              spec,
		SourcePath:        "dist/other.js",
	}, "test_caller")
	require.NoError(t, err)
	require.Len(t, result.RegistryHash, 64)
	for _, c := range result.RegistryHash {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("hash must be lowercase hex: %q", result.RegistryHash)
		}
	}
}

// TestRegisterSlaveMCP_AuditWriteFailure_DegradesButRegistrationStillSucceeds
// — §3.4 step 6: audit write failure is logged, register still succeeds.
func TestRegisterSlaveMCP_AuditWriteFailure_DegradesButRegistrationStillSucceeds(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-b", DisplayName: "slave-b", Status: "available", Card: json.RawMessage(`{"skills":["chat","register_mcp"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			return &agentsdk.DelegateTaskResponse{TaskID: "task-reg-11"}, nil
		},
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{TaskID: "task-reg-11", Status: "completed", Result: json.RawMessage(`"ok"`)}, nil
		},
	}
	tools := newTestTools(t, sdk)
	tools.cfg.Observer.WorkspaceID = "ws-abc12345"
	rec := &recordingWriter{writeErr: strings.NewReader("boom")}
	// Use a writer that always errors.
	tools.SetPromotionAuditWriter(&errorWriter{})
	resetRegistryForTest()

	tool := toolByName(t, tools, "register_slave_mcp")
	args := `{"target_display_name":"slave-b","spec":` + validTestSpecJSON() + `,"source_path":"dist/mytool.js"` + validAuditFieldsJSON + `}`
	_, err := tool.Call(context.Background(), json.RawMessage(args))
	// Register still succeeds even though audit failed.
	require.NoError(t, err)
	// No row recorded on the successful writer (we swapped in errorWriter).
	require.Len(t, rec.rows, 0)
}
