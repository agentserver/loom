package driver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/stretchr/testify/require"
)

func TestRunSlaveBashDelegatesBashSkillAndWaits(t *testing.T) {
	var delegated agentsdk.DelegateTaskRequest
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-a", DisplayName: "slave-a", Status: "available", Card: json.RawMessage(`{"skills":["bash"],"short_id":"sa"}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			delegated = req
			return &agentsdk.DelegateTaskResponse{TaskID: "task-1"}, nil
		},
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{
				TaskID: "task-1",
				Status: "completed",
				Result: json.RawMessage(`"{\"exit_code\":0,\"stdout\":\"ok\\n\",\"stderr\":\"\",\"workdir\":\"/w\"}"`),
			}, nil
		},
	}
	tool := toolByName(t, newTestTools(t, sdk), "run_slave_bash")
	out, err := tool.Call(context.Background(), json.RawMessage(`{"target_display_name":"slave-a","script":"echo ok","timeout_sec":30}`))
	require.NoError(t, err)
	require.Equal(t, "slave-a", delegated.TargetID)
	require.Equal(t, "bash", delegated.Skill)
	require.JSONEq(t, `{"script":"echo ok","timeout_sec":30}`, delegated.Prompt)
	require.Contains(t, string(out), `"task_id":"task-1"`)
	require.Contains(t, string(out), `"stdout":"ok\n"`)
}

func TestRunSlaveBashRejectsMissingBashSkill(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-a", DisplayName: "slave-a", Status: "available", Card: json.RawMessage(`{"skills":["chat"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			t.Fatalf("must not delegate without bash skill")
			return nil, nil
		},
	}
	tool := toolByName(t, newTestTools(t, sdk), "run_slave_bash")
	_, err := tool.Call(context.Background(), json.RawMessage(`{"target_display_name":"slave-a","script":"echo ok"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not advertise bash")
}

func TestGetSlaveClaudePermissionsDelegatesPermissionSkillAndWaits(t *testing.T) {
	var delegated agentsdk.DelegateTaskRequest
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-a", DisplayName: "slave-a", Status: "available", Card: json.RawMessage(`{"skills":["claude_permissions"],"short_id":"sa"}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			delegated = req
			return &agentsdk.DelegateTaskResponse{TaskID: "task-2"}, nil
		},
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{
				TaskID: "task-2",
				Status: "completed",
				Result: json.RawMessage(`"{\"path\":\"/w/.claude/settings.local.json\",\"allow\":[\"Read\"],\"deny\":[]}"`),
			}, nil
		},
	}
	tool := toolByName(t, newTestTools(t, sdk), "get_slave_claude_permissions")
	out, err := tool.Call(context.Background(), json.RawMessage(`{"target_display_name":"slave-a"}`))
	require.NoError(t, err)
	require.Equal(t, "slave-a", delegated.TargetID)
	require.Equal(t, "claude_permissions", delegated.Skill)
	require.JSONEq(t, `{"op":"get"}`, delegated.Prompt)
	require.Contains(t, string(out), `"task_id":"task-2"`)
	require.Contains(t, string(out), `"allow":["Read"]`)
}

func TestUpdateSlaveClaudePermissionsDelegatesPermissionSkillAndWaits(t *testing.T) {
	var delegated agentsdk.DelegateTaskRequest
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-a", DisplayName: "slave-a", Status: "available", Card: json.RawMessage(`{"skills":["claude_permissions"],"short_id":"sa"}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			delegated = req
			return &agentsdk.DelegateTaskResponse{TaskID: "task-3"}, nil
		},
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{
				TaskID: "task-3",
				Status: "completed",
				Result: json.RawMessage(`"{\"path\":\"/w/.claude/settings.local.json\",\"allow\":[\"Bash(curl *)\",\"Read\"],\"deny\":[]}"`),
			}, nil
		},
	}
	tool := toolByName(t, newTestTools(t, sdk), "update_slave_claude_permissions")
	out, err := tool.Call(context.Background(), json.RawMessage(`{"target_display_name":"slave-a","allow_presets":["curl"],"allow_add":["Read"]}`))
	require.NoError(t, err)
	require.Equal(t, "slave-a", delegated.TargetID)
	require.Equal(t, "claude_permissions", delegated.Skill)
	require.JSONEq(t, `{"op":"patch","allow_presets":["curl"],"allow_add":["Read"]}`, delegated.Prompt)
	require.Contains(t, string(out), `"task_id":"task-3"`)
	require.Contains(t, string(out), `"allow":["Bash(curl *)","Read"]`)
}

func TestSlaveClaudePermissionsRejectsMissingPermissionSkill(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-a", DisplayName: "slave-a", Status: "available", Card: json.RawMessage(`{"skills":["bash"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			t.Fatalf("must not delegate without claude_permissions skill")
			return nil, nil
		},
	}
	tool := toolByName(t, newTestTools(t, sdk), "get_slave_claude_permissions")
	_, err := tool.Call(context.Background(), json.RawMessage(`{"target_display_name":"slave-a"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not advertise claude_permissions")
}
