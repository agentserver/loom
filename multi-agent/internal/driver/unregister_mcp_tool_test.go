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
	tool := toolByName(t, newTestTools(t, sdk), "unregister_slave_mcp")
	args := `{"target_display_name":"slave-b","name":"echo","if_present":true,"timeout_sec":60}`
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
	tool := toolByName(t, newTestTools(t, sdk), "unregister_slave_mcp")
	args := `{"target_display_name":"slave-c","name":"echo"}`
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
	tool := toolByName(t, newTestTools(t, sdk), "unregister_slave_mcp")
	args := `{"target_display_name":"slave-d","name":""}`
	_, err := tool.Call(context.Background(), json.RawMessage(args))
	require.Error(t, err)
	require.Contains(t, err.Error(), "name is required")
}
