package driver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/stretchr/testify/require"
)

func TestReadSlaveFile_CachesAndRegistersAndReturnsHandle(t *testing.T) {
	var delegated agentsdk.DelegateTaskRequest
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{{
				AgentID: "slave-a", DisplayName: "slave-a", Status: "available",
				Card: json.RawMessage(`{"skills":["file"],"short_id":"sa"}`),
			}}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			delegated = req
			return &agentsdk.DelegateTaskResponse{TaskID: "task-1"}, nil
		},
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			// slave returns hello\n (6 bytes, utf-8)
			return &agentsdk.TaskInfo{
				TaskID: "task-1", Status: "completed",
				Result: json.RawMessage(`"{\"path\":\"/abs/in.txt\",\"bytes\":6,\"encoding\":\"utf-8\",\"content\":\"hello\\n\",\"eof\":true}"`),
			}, nil
		},
	}
	tools := newTestTools(t, sdk)
	tool := toolByName(t, tools, "read_slave_file")
	raw, err := tool.Call(context.Background(), json.RawMessage(`{"target_display_name":"slave-a","path":"in.txt"}`))
	require.NoError(t, err)
	require.Equal(t, "file", delegated.Skill)
	require.JSONEq(t, `{"op":"read","path":"in.txt"}`, delegated.Prompt)

	var out map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &out))
	require.Equal(t, "/abs/in.txt", out["slave_path"])
	require.EqualValues(t, 6, out["size"])
	require.Equal(t, "hello\n", out["content"]) // inline (≤ 4 KiB, utf-8)
	sha, _ := out["sha256"].(string)
	require.NotEmpty(t, sha)
	require.Equal(t, "sha256:"+sha, out["blob_handle"])

	// Cache file exists with that sha as filename.
	cachePath, _ := out["cache_path"].(string)
	require.FileExists(t, cachePath)
	body, _ := os.ReadFile(cachePath)
	require.Equal(t, "hello\n", string(body))

	// FileRegistry has it.
	path, ok := tools.reg.LookupBlob(sha)
	require.True(t, ok)
	require.Equal(t, cachePath, path)
}

func TestReadSlaveFile_OmitsContentWhenLargerThanInlineCap(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{{
				AgentID: "slave-a", DisplayName: "slave-a", Status: "available",
				Card: json.RawMessage(`{"skills":["file"],"short_id":"sa"}`),
			}}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			return &agentsdk.DelegateTaskResponse{TaskID: "task-1"}, nil
		},
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{
				TaskID: "task-1", Status: "completed",
				Result: json.RawMessage(`"{\"path\":\"/abs/big.txt\",\"bytes\":10,\"encoding\":\"utf-8\",\"content\":\"0123456789\",\"eof\":true}"`),
			}, nil
		},
	}
	tool := toolByName(t, newTestTools(t, sdk), "read_slave_file")
	raw, err := tool.Call(context.Background(), json.RawMessage(`{"target_display_name":"slave-a","path":"big.txt","inline_max_bytes":4}`))
	require.NoError(t, err)
	var out map[string]interface{}
	json.Unmarshal(raw, &out)
	_, hasContent := out["content"]
	require.False(t, hasContent, "content must be omitted when size > inline_max_bytes")
}

func TestReadSlaveFile_OmitsContentForBase64EvenWhenSmall(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{{
				AgentID: "slave-a", DisplayName: "slave-a", Status: "available",
				Card: json.RawMessage(`{"skills":["file"],"short_id":"sa"}`),
			}}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			return &agentsdk.DelegateTaskResponse{TaskID: "task-1"}, nil
		},
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			// 3-byte binary, base64 = "AP9C"
			return &agentsdk.TaskInfo{
				TaskID: "task-1", Status: "completed",
				Result: json.RawMessage(`"{\"path\":\"/abs/bin\",\"bytes\":3,\"encoding\":\"base64\",\"content\":\"AP9C\",\"eof\":true}"`),
			}, nil
		},
	}
	tool := toolByName(t, newTestTools(t, sdk), "read_slave_file")
	raw, err := tool.Call(context.Background(), json.RawMessage(`{"target_display_name":"slave-a","path":"bin","encoding":"base64"}`))
	require.NoError(t, err)
	var out map[string]interface{}
	json.Unmarshal(raw, &out)
	_, hasContent := out["content"]
	require.False(t, hasContent, "base64 reads never inline content")
}

func TestReadSlaveFile_RejectsMissingFileSkill(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{{
				AgentID: "slave-a", DisplayName: "slave-a", Status: "available",
				Card: json.RawMessage(`{"skills":["chat"]}`),
			}}, nil
		},
	}
	tool := toolByName(t, newTestTools(t, sdk), "read_slave_file")
	_, err := tool.Call(context.Background(), json.RawMessage(`{"target_display_name":"slave-a","path":"in.txt"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not advertise file")
}

func TestReadSlaveFile_AuditsRegisterReadWithSlaveShortID(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{{
				AgentID: "slave-a", DisplayName: "slave-a", Status: "available",
				Card: json.RawMessage(`{"skills":["file"],"short_id":"sa"}`),
			}}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			return &agentsdk.DelegateTaskResponse{TaskID: "task-1"}, nil
		},
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{
				TaskID: "task-1", Status: "completed",
				Result: json.RawMessage(`"{\"path\":\"/abs/in.txt\",\"bytes\":2,\"encoding\":\"utf-8\",\"content\":\"hi\",\"eof\":true}"`),
			}, nil
		},
	}
	tools := newTestTools(t, sdk)
	tool := toolByName(t, tools, "read_slave_file")
	_, err := tool.Call(context.Background(), json.RawMessage(`{"target_display_name":"slave-a","path":"in.txt"}`))
	require.NoError(t, err)

	dir := tools.cfg.DriverDefaults.AuditLogDir
	require.NotEmpty(t, dir, "test helper must set AuditLogDir")
	body, err := os.ReadFile(filepath.Join(dir, "audit.log"))
	require.NoError(t, err)
	require.Contains(t, string(body), `"event":"register_read"`)
	require.Contains(t, string(body), `"peer_short_id":"sa"`)
}
