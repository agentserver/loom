package driver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/stretchr/testify/require"
)

func TestUnregisterSlaveMCP_DelegatesAsUnregisterMCPSkill(t *testing.T) {
	var delegated agentsdk.DelegateTaskRequest
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-b", DisplayName: "slave-b", Status: "available", Card: json.RawMessage(`{"skills":["chat","unregister_mcp"],"short_id":"sb"}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			delegated = req
			return &agentsdk.DelegateTaskResponse{TaskID: "task-unreg-1"}, nil
		},
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{
				TaskID: "task-unreg-1",
				Status: "completed",
				Result: json.RawMessage(`"unregistered"`),
			}, nil
		},
	}
	tools := newTestTools(t, sdk)
	tools.cfg.Observer.WorkspaceID = "ws-abc12345"
	tool := toolByName(t, tools, "unregister_slave_mcp")
	args := `{"target_display_name":"slave-b","name":"echo","if_present":true,"timeout_sec":60` + validAuditFieldsJSON + `}`
	out, err := tool.Call(context.Background(), json.RawMessage(args))
	require.NoError(t, err)
	require.Equal(t, "slave-b", delegated.TargetID)
	require.Equal(t, "unregister_mcp", delegated.Skill)
	require.Contains(t, delegated.Prompt, `"name":"echo"`)
	require.Contains(t, delegated.Prompt, `"if_present":true`)
	require.Contains(t, string(out), "task-unreg-1")
}

func TestUnregisterSlaveMCP_RejectsSlaveWithoutSkill(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-c", DisplayName: "slave-c", Status: "available", Card: json.RawMessage(`{"skills":["chat"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			t.Fatalf("must not delegate when unregister_mcp skill is missing")
			return nil, nil
		},
	}
	tools := newTestTools(t, sdk)
	tools.cfg.Observer.WorkspaceID = "ws-abc12345"
	tool := toolByName(t, tools, "unregister_slave_mcp")
	args := `{"target_display_name":"slave-c","name":"echo"` + validAuditFieldsJSON + `}`
	_, err := tool.Call(context.Background(), json.RawMessage(args))
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not advertise unregister_mcp")
}

func TestUnregisterSlaveMCP_RejectsEmptyName(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-d", DisplayName: "slave-d", Status: "available", Card: json.RawMessage(`{"skills":["unregister_mcp"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			t.Fatalf("must not delegate with empty name")
			return nil, nil
		},
	}
	tools := newTestTools(t, sdk)
	tools.cfg.Observer.WorkspaceID = "ws-abc12345"
	tool := toolByName(t, tools, "unregister_slave_mcp")
	args := `{"target_display_name":"slave-d","name":""` + validAuditFieldsJSON + `}`
	_, err := tool.Call(context.Background(), json.RawMessage(args))
	require.Error(t, err)
	require.Contains(t, err.Error(), "name is required")
}

// --- B6 audit-field guard tests (unregister) -----------------------

func TestUnregisterSlaveMCP_RequiresAllFourAuditFields_MissingUserID(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-x", DisplayName: "slave-x", Status: "available", Card: json.RawMessage(`{"skills":["unregister_mcp"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			t.Fatalf("audit precheck must reject before delegate")
			return nil, nil
		},
	}
	tools := newTestTools(t, sdk)
	tools.cfg.Observer.WorkspaceID = "ws-abc12345"
	tool := toolByName(t, tools, "unregister_slave_mcp")
	args := `{"target_display_name":"slave-x","name":"echo","driver_thread_id":"thread_01_a","promotion_reason":"explicit_user_request","candidate_source_task_id":"task_12345678"}`
	_, err := tool.Call(context.Background(), json.RawMessage(args))
	require.Error(t, err)
	require.Contains(t, err.Error(), "promoted_by_user_id")
}

// TestUnregisterSlaveMCP_ClearsSlaveViewEntry — register X then
// unregister X returns LastRegistryHash to the pre-register value.
func TestUnregisterSlaveMCP_ClearsSlaveViewEntry(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-b", DisplayName: "slave-b", Status: "available", Card: json.RawMessage(`{"skills":["chat","register_mcp","unregister_mcp"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			return &agentsdk.DelegateTaskResponse{TaskID: "task-" + req.Skill}, nil
		},
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{TaskID: id, Status: "completed", Result: json.RawMessage(`"ok"`)}, nil
		},
	}
	tools := newTestTools(t, sdk)
	tools.cfg.Observer.WorkspaceID = "ws-abc12345"
	tools.SetPromotionAuditWriter(&recordingWriter{})
	resetRegistryForTest()

	preHash := LastRegistryHash()

	regTool := toolByName(t, tools, "register_slave_mcp")
	regArgs := `{"target_display_name":"slave-b","spec":{"name":"echo","description":"e","tools":[{"name":"e","description":"e","args_schema":{"type":"object"},"result_description":"r"}]},"source_path":"dist/echo.js"` + validAuditFieldsJSON + `}`
	_, err := regTool.Call(context.Background(), json.RawMessage(regArgs))
	require.NoError(t, err)
	require.NotEqual(t, preHash, LastRegistryHash())

	unregTool := toolByName(t, tools, "unregister_slave_mcp")
	unregArgs := `{"target_display_name":"slave-b","name":"echo","if_present":true` + validAuditFieldsJSON + `}`
	_, err = unregTool.Call(context.Background(), json.RawMessage(unregArgs))
	require.NoError(t, err)
	require.Equal(t, preHash, LastRegistryHash(), "unregister should restore pre-register hash")
}
