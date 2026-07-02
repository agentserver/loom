package driver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/stretchr/testify/require"
)

func promotionPipelineArgsJSON() string {
	return `{"target_display_name":"slave-b","spec":` + validTestSpecJSON() +
		`,"cases_path":"tests/eval/golden/csv-profiler/acceptance/cases.jsonl","promoted_by_user_id":"user_abcdef","driver_thread_id":"thread_01_a","promotion_reason":"explicit_user_request","candidate_source_task_id":"task_12345678"}`
}

// TestPromotionPipelineTool_HappyPath — end-to-end through the tool
// boundary. The fake sdk answers scaffold + acceptance + register
// delegates; acceptance carries `acceptance_exit_code:0`. Assert the
// tool responds with 3 stage outcomes and 3 audit rows land.
func TestPromotionPipelineTool_HappyPath(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-b", DisplayName: "slave-b", Status: "available", Card: json.RawMessage(`{"skills":["chat","register_mcp","scaffold-mcp-server","mcp-acceptance"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			return &agentsdk.DelegateTaskResponse{TaskID: "task-" + req.Skill}, nil
		},
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			if strings.HasSuffix(id, "mcp-acceptance") {
				return &agentsdk.TaskInfo{TaskID: id, Status: "completed", Result: json.RawMessage(`{"acceptance_exit_code":0}`)}, nil
			}
			return &agentsdk.TaskInfo{TaskID: id, Status: "completed", Result: json.RawMessage(`{}`)}, nil
		},
	}
	tools := newTestTools(t, sdk)
	rec := &recordingWriter{}
	tools.SetPromotionAuditWriter(rec)
	resetRegistryForTest()

	tool := toolByName(t, tools, "promotion_pipeline")
	out, err := tool.Call(context.Background(), json.RawMessage(promotionPipelineArgsJSON()))
	require.NoError(t, err)
	require.Contains(t, string(out), `"stage":"scaffold"`)
	require.Contains(t, string(out), `"stage":"acceptance"`)
	require.Contains(t, string(out), `"stage":"register"`)
	require.GreaterOrEqual(t, len(rec.rows), 3, "expected at least 3 stage audit rows")
}

// TestPromotionPipelineTool_RequiresAllFourAuditFields — table-driven.
func TestPromotionPipelineTool_RequiresAllFourAuditFields(t *testing.T) {
	cases := []struct {
		name     string
		omitKey  string
		errMatch string
	}{
		{"missing_user_id", `"promoted_by_user_id":"user_abcdef",`, "promoted_by_user_id"},
		{"missing_thread_id", `"driver_thread_id":"thread_01_a",`, "driver_thread_id"},
		{"missing_reason", `"promotion_reason":"explicit_user_request",`, "promotion_reason"},
		{"missing_cand", `,"candidate_source_task_id":"task_12345678"`, "candidate_source_task_id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sdk := &fakeSDK{
				delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
					t.Fatal("audit precheck must reject before delegate")
					return nil, nil
				},
			}
			tools := newTestTools(t, sdk)
			tool := toolByName(t, tools, "promotion_pipeline")
			args := promotionPipelineArgsJSON()
			args = strings.Replace(args, tc.omitKey, "", 1)
			_, err := tool.Call(context.Background(), json.RawMessage(args))
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.errMatch)
		})
	}
}

// TestPromotionPipelineTool_RejectsMalformedCasesPath — traversal
// guard fires at the tool boundary.
func TestPromotionPipelineTool_RejectsMalformedCasesPath(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-b", DisplayName: "slave-b", Status: "available", Card: json.RawMessage(`{"skills":["scaffold-mcp-server"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			t.Fatal("delegate must not be called for a rejected cases_path")
			return nil, nil
		},
	}
	tools := newTestTools(t, sdk)
	tool := toolByName(t, tools, "promotion_pipeline")

	badArgs := `{"target_display_name":"slave-b","spec":` + validTestSpecJSON() +
		`,"cases_path":"safe/../evil","promoted_by_user_id":"user_abcdef","driver_thread_id":"thread_01_a","promotion_reason":"explicit_user_request","candidate_source_task_id":"task_12345678"}`
	_, err := tool.Call(context.Background(), json.RawMessage(badArgs))
	require.Error(t, err)
	require.Contains(t, err.Error(), "path traversal")
}
