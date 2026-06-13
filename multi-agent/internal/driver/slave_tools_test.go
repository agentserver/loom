package driver

import (
	"context"
	"encoding/json"
	"strings"
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
	out, err := tool.Call(context.Background(), json.RawMessage(`{"target_display_name":"slave-a","script":"echo ok","timeout_sec":30,"wait":true}`))
	require.NoError(t, err)
	require.Equal(t, "slave-a", delegated.TargetID)
	require.Equal(t, "bash", delegated.Skill)
	require.JSONEq(t, `{"script":"echo ok","timeout_sec":30}`, delegated.Prompt)
	require.Contains(t, string(out), `"task_id":"task-1"`)
	require.Contains(t, string(out), `"stdout":"ok\n"`)
}

func TestRunSlaveBashWaitTrueRecordsTaskBeforePolling(t *testing.T) {
	getTaskCalled := false
	var tools *Tools
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{
					AgentID:     "agent-1",
					DisplayName: "slave-1",
					Status:      "available",
					Card:        json.RawMessage(`{"skills":["bash"],"command_interfaces":[{"skill":"bash","kind":"bash","command":"/bin/bash","default":true}]}`),
				},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			return &agentsdk.DelegateTaskResponse{TaskID: "task-wait", Status: "submitted"}, nil
		},
		getTaskFunc: func(taskID string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			getTaskCalled = true
			records, err := tools.taskJournal.Recent(1, "task-wait")
			require.NoError(t, err)
			require.Len(t, records, 1)
			require.Equal(t, "run_slave_bash", records[0].Tool)
			require.True(t, records[0].Wait)
			return &agentsdk.TaskInfo{TaskID: taskID, Status: "completed", Output: "ok"}, nil
		},
	}
	tools = newTestTools(t, sdk)

	_, err := toolByName(t, tools, "run_slave_bash").Call(context.Background(), json.RawMessage(`{"target_display_name":"slave-1","script":"sleep 30","wait":true}`))
	require.NoError(t, err)
	require.True(t, getTaskCalled)
}

func TestPermissionTaskRecordsBeforeWaiting(t *testing.T) {
	var tools *Tools
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{
					AgentID:     "agent-1",
					DisplayName: "slave-1",
					Status:      "available",
					Card:        json.RawMessage(`{"skills":["permissions"]}`),
				},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			return &agentsdk.DelegateTaskResponse{TaskID: "task-perm", Status: "submitted"}, nil
		},
		getTaskFunc: func(taskID string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			records, err := tools.taskJournal.Recent(1, "task-perm")
			require.NoError(t, err)
			require.Len(t, records, 1)
			require.Equal(t, "get_slave_claude_permissions", records[0].Tool)
			require.True(t, records[0].Wait)
			return &agentsdk.TaskInfo{TaskID: taskID, Status: "completed", Output: `{"ok":true}`}, nil
		},
	}
	tools = newTestTools(t, sdk)

	_, err := toolByName(t, tools, "get_slave_claude_permissions").Call(context.Background(), json.RawMessage(`{"target_display_name":"slave-1"}`))
	require.NoError(t, err)
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

func TestRunSlaveBashRejectsPowerShellOnlyTargetWithSuggestion(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-win", DisplayName: "slave-win", Status: "available", Card: json.RawMessage(`{
					"skills":["powershell"],
					"command_interfaces":[{"skill":"powershell","kind":"powershell","command":"pwsh","default":true}]
				}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			t.Fatalf("must not delegate bash to a PowerShell-only target")
			return nil, nil
		},
	}

	tool := toolByName(t, newTestTools(t, sdk), "run_slave_bash")
	_, err := tool.Call(context.Background(), json.RawMessage(`{"target_display_name":"slave-win","script":"echo ok"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "no Bash command interface")
	require.Contains(t, err.Error(), "run_slave_powershell")
}

func TestRunSlavePowerShellDelegatesPowerShellSkillAndJSONPrompt(t *testing.T) {
	var delegated agentsdk.DelegateTaskRequest
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-win", DisplayName: "slave-win", Status: "available", Card: json.RawMessage(`{
					"skills":["powershell"],
					"command_interfaces":[{"skill":"powershell","kind":"powershell","command":"pwsh","default":true}]
				}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			delegated = req
			return &agentsdk.DelegateTaskResponse{TaskID: "task-ps", Status: "submitted"}, nil
		},
	}

	tool := toolByName(t, newTestTools(t, sdk), "run_slave_powershell")
	out, err := tool.Call(context.Background(), json.RawMessage(`{
		"target_display_name":"slave-win",
		"script":"Write-Output ok",
		"timeout_sec":45,
		"env":{"A":"B"},
		"wait":false
	}`))

	require.NoError(t, err)
	require.Equal(t, "slave-win", delegated.TargetID)
	require.Equal(t, "powershell", delegated.Skill)
	require.Equal(t, 45, delegated.TimeoutSeconds)
	require.JSONEq(t, `{"script":"Write-Output ok","timeout_sec":45,"env":{"A":"B"}}`, delegated.Prompt)
	require.JSONEq(t, `{"task_id":"task-ps","target_id":"slave-win","target_display_name":"slave-win","skill":"powershell","status":"submitted"}`, string(out))
}

func TestRunSlavePowerShellReturnsTaskIDByDefault(t *testing.T) {
	var delegated agentsdk.DelegateTaskRequest
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-win", DisplayName: "slave-win", Status: "available", Card: json.RawMessage(`{
					"skills":["powershell"],
					"command_interfaces":[{"skill":"powershell","kind":"powershell","command":"pwsh","default":true}]
				}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			delegated = req
			return &agentsdk.DelegateTaskResponse{TaskID: "task-long", Status: "submitted"}, nil
		},
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			t.Fatalf("default run_slave_powershell must not wait for delegated task")
			return nil, nil
		},
	}

	tool := toolByName(t, newTestTools(t, sdk), "run_slave_powershell")
	out, err := tool.Call(context.Background(), json.RawMessage(`{
		"target_display_name":"slave-win",
		"script":"Start-Sleep -Seconds 600",
		"timeout_sec":600
	}`))

	require.NoError(t, err)
	require.Equal(t, "slave-win", delegated.TargetID)
	require.Equal(t, "powershell", delegated.Skill)
	require.Equal(t, 600, delegated.TimeoutSeconds)
	require.JSONEq(t, `{"task_id":"task-long","target_id":"slave-win","target_display_name":"slave-win","skill":"powershell","status":"submitted"}`, string(out))
}

func TestRunSlavePowerShellWaitTrueWaitsForCompletion(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-win", DisplayName: "slave-win", Status: "available", Card: json.RawMessage(`{
					"skills":["powershell"],
					"command_interfaces":[{"skill":"powershell","kind":"powershell","command":"pwsh","default":true}]
				}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			return &agentsdk.DelegateTaskResponse{TaskID: "task-ps", Status: "submitted"}, nil
		},
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{
				TaskID: "task-ps",
				Status: "completed",
				Result: json.RawMessage(`"{\"exit_code\":0,\"stdout\":\"done\\n\",\"stderr\":\"\",\"workdir\":\"C:\\\\work\"}"`),
			}, nil
		},
	}

	tool := toolByName(t, newTestTools(t, sdk), "run_slave_powershell")
	out, err := tool.Call(context.Background(), json.RawMessage(`{
		"target_display_name":"slave-win",
		"script":"Write-Output done",
		"wait":true
	}`))

	require.NoError(t, err)
	require.Contains(t, string(out), `"task_id":"task-ps"`)
	require.Contains(t, string(out), `"stdout":"done\n"`)
}

func TestRunSlaveShellUsesDefaultCommandInterface(t *testing.T) {
	var delegated agentsdk.DelegateTaskRequest
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-win", DisplayName: "slave-win", Status: "available", Card: json.RawMessage(`{
					"skills":["bash","powershell"],
					"command_interfaces":[
						{"skill":"bash","kind":"bash","command":"bash"},
						{"skill":"powershell","kind":"powershell","command":"pwsh","default":true}
					]
				}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			delegated = req
			return &agentsdk.DelegateTaskResponse{TaskID: "task-shell", Status: "queued"}, nil
		},
	}

	tool := toolByName(t, newTestTools(t, sdk), "run_slave_shell")
	out, err := tool.Call(context.Background(), json.RawMessage(`{"target_display_name":"slave-win","script":"Write-Output ok","wait":false}`))

	require.NoError(t, err)
	require.Equal(t, "powershell", delegated.Skill)
	require.JSONEq(t, `{"script":"Write-Output ok"}`, delegated.Prompt)
	require.JSONEq(t, `{"task_id":"task-shell","target_id":"slave-win","target_display_name":"slave-win","skill":"powershell","status":"queued"}`, string(out))
}

func TestListAgentsIncludesPlatformAndCommandInterfaces(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-win", DisplayName: "slave-win", Status: "available", Card: json.RawMessage(`{
					"skills":["powershell"],
					"platform":{"os":"windows","arch":"amd64"},
					"command_interfaces":[{"skill":"powershell","kind":"powershell","command":"pwsh","default":true}]
				}`)},
			}, nil
		},
	}

	tool := toolByName(t, newTestTools(t, sdk), "list_agents")
	out, err := tool.Call(context.Background(), json.RawMessage(`{}`))

	require.NoError(t, err)
	require.Contains(t, string(out), `"platform":{"os":"windows","arch":"amd64"}`)
	require.Contains(t, string(out), `"command_interfaces":[{"skill":"powershell","kind":"powershell","command":"pwsh","default":true}]`)
}

func TestGetSlaveClaudePermissionsDelegatesPermissionSkillAndWaits(t *testing.T) {
	var delegated agentsdk.DelegateTaskRequest
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-a", DisplayName: "slave-a", Status: "available", Card: json.RawMessage(`{"skills":["permissions"],"short_id":"sa"}`)},
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
	require.Equal(t, "permissions", delegated.Skill)
	require.JSONEq(t, `{"op":"get"}`, delegated.Prompt)
	require.Contains(t, string(out), `"task_id":"task-2"`)
	require.Contains(t, string(out), `"allow":["Read"]`)
}

func TestGetSlaveClaudePermissionsFallsBackToLegacySkill(t *testing.T) {
	var delegated agentsdk.DelegateTaskRequest
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-a", DisplayName: "slave-a", Status: "available", Card: json.RawMessage(`{"skills":["claude_permissions"],"short_id":"sa"}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			delegated = req
			return &agentsdk.DelegateTaskResponse{TaskID: "task-legacy"}, nil
		},
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{
				TaskID: "task-legacy",
				Status: "completed",
				Result: json.RawMessage(`"{\"path\":\"/w/.claude/settings.local.json\",\"allow\":[],\"deny\":[]}"`),
			}, nil
		},
	}
	tool := toolByName(t, newTestTools(t, sdk), "get_slave_claude_permissions")
	_, err := tool.Call(context.Background(), json.RawMessage(`{"target_display_name":"slave-a"}`))
	require.NoError(t, err)
	require.Equal(t, "claude_permissions", delegated.Skill)
	require.JSONEq(t, `{"op":"get"}`, delegated.Prompt)
}

func TestUpdateSlaveClaudePermissionsDelegatesPermissionSkillAndWaits(t *testing.T) {
	var delegated agentsdk.DelegateTaskRequest
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-a", DisplayName: "slave-a", Status: "available", Card: json.RawMessage(`{"skills":["permissions"],"short_id":"sa"}`)},
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
	require.Equal(t, "permissions", delegated.Skill)
	require.JSONEq(t, `{"op":"patch","presets":["curl"],"allow_add":["Read"]}`, delegated.Prompt)
	require.NotContains(t, delegated.Prompt, "allow_presets")
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
	require.Contains(t, err.Error(), "does not advertise permissions or claude_permissions")
}

// TestDelegateShellTask_DegradesRecordDelegatedTaskFailureToWarning verifies
// the §1.1 #1 invariant for delegateShellTask's wait=false path: when
// DelegateTask succeeds but the local task journal append fails, the tool
// must still return task_id (slave is already running) and only log the
// failure via logRelayErr — never surface it as an error. There is no
// warnings field on this response shape, so the only visible signal is the
// stderr/audit log entry written by logRelayErr.
func TestDelegateShellTask_DegradesRecordDelegatedTaskFailureToWarning(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-a", DisplayName: "slave-a", Status: "available",
					Card: json.RawMessage(`{"skills":["bash"],"command_interfaces":[{"skill":"bash","kind":"bash","command":"/bin/bash","default":true}]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			return &agentsdk.DelegateTaskResponse{TaskID: "task-shell-77", Status: "submitted"}, nil
		},
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			t.Fatalf("wait=false must not poll GetTask")
			return nil, nil
		},
	}
	tools := newTestTools(t, sdk)
	// Close the journal file so the next Append fails. The tool must still
	// return task_id rather than propagating that failure.
	require.NoError(t, tools.taskJournal.Close())

	var out json.RawMessage
	var callErr error
	stderr := captureStderr(t, func() {
		out, callErr = toolByName(t, tools, "run_slave_bash").Call(context.Background(),
			json.RawMessage(`{"target_display_name":"slave-a","script":"echo ok","wait":false}`))
	})
	require.NoError(t, callErr, "run_slave_bash must NOT return error; DelegateTask already succeeded")
	require.Contains(t, string(out), `"task_id":"task-shell-77"`)
	require.True(t,
		strings.Contains(stderr, "record_delegated_task") ||
			strings.Contains(stderr, "task journal"),
		"logRelayErr should have emitted a stderr message about the record_delegated_task failure; got: %q", stderr)
}
