package driver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/stretchr/testify/require"
)

// validTestSpec returns a minimal buildspec.Spec JSON payload that passes Validate.
func validTestSpecJSON() string {
	return `{
		"name": "mytool",
		"description": "A test MCP server",
		"tools": [{"name":"do_thing","description":"does a thing","args_schema":{"type":"object"},"result_description":"result"}]
	}`
}

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
	args := `{"target_display_name":"slave-b","spec":` + validTestSpecJSON() + `,"source_path":"dist/mytool.js","timeout_sec":60}`
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
	args := `{"target_display_name":"slave-c","spec":` + validTestSpecJSON() + `,"source_path":"dist/mytool.js"}`
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
	args := `{"target_display_name":"slave-d","spec":` + validTestSpecJSON() + `,"source_path":""}`
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
	args := `{"target_display_name":"slave-e","spec":{"name":"X bad","description":"d","tools":[{"name":"t","description":"d","args_schema":{"type":"object"},"result_description":"r"}]},"source_path":"dist/x.js"}`
	_, err := tool.Call(context.Background(), json.RawMessage(args))
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid spec")
}
