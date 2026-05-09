package driver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentserver/agentserver/pkg/agentsdk"
)

// fakeSDK satisfies SDKClient for tests.
type fakeSDK struct {
	discoverFunc  func() ([]agentsdk.AgentCard, error)
	delegateFunc  func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error)
	getTaskFunc   func(id string, includeOutput bool) (*agentsdk.TaskInfo, error)
	peerProxyFunc func(method, target, path string, body io.Reader) (*http.Response, error)
}

func (f *fakeSDK) DiscoverAgents(ctx context.Context) ([]agentsdk.AgentCard, error) {
	return f.discoverFunc()
}
func (f *fakeSDK) DelegateTask(ctx context.Context, req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
	return f.delegateFunc(req)
}
func (f *fakeSDK) GetTask(ctx context.Context, id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
	return f.getTaskFunc(id, includeOutput)
}
func (f *fakeSDK) PeerProxy(ctx context.Context, method, target, path string, body io.Reader) (*http.Response, error) {
	return f.peerProxyFunc(method, target, path, body)
}

func newTestTools(t *testing.T, sdk SDKClient) *Tools {
	t.Helper()
	dir := t.TempDir()
	a, err := NewAuditLog(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	cfg := &Config{}
	cfg.Server.URL = "https://srv.example.com"
	cfg.Credentials.ShortID = "drv-001"
	cfg.Credentials.SandboxID = "sbx-driver"
	cfg.DriverDefaults.TaskTimeoutSec = 600
	return NewTools(NewFileRegistry(50000), a, sdk, cfg)
}

func TestTool_ListAgents_FiltersSelf(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "sbx-driver", DisplayName: "driver-yuzishu"},
				{AgentID: "sbx-master", DisplayName: "master-prod",
					Card: json.RawMessage(`{"skills":["fanout"],"tools":[]}`)},
			}, nil
		},
	}
	tools := newTestTools(t, sdk)
	for _, tt := range tools.All() {
		if tt.Name() == "list_agents" {
			out, err := tt.Call(context.Background(), json.RawMessage(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(out), "driver-yuzishu") {
				t.Errorf("self not filtered: %s", out)
			}
			if !strings.Contains(string(out), "master-prod") {
				t.Errorf("master missing: %s", out)
			}
			return
		}
	}
	t.Fatal("list_agents tool not registered")
}

func TestTool_SubmitTask_RegistersFilesAndDelegates(t *testing.T) {
	dir := t.TempDir()
	in1 := filepath.Join(dir, "a.txt")
	in2 := filepath.Join(dir, "b.txt")
	out := filepath.Join(dir, "out.txt")
	os.WriteFile(in1, []byte("hello"), 0o644)
	os.WriteFile(in2, []byte("world"), 0o644)

	var gotPrompt string
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "sbx-master", DisplayName: "master-prod",
					Card: json.RawMessage(`{"skills":["fanout"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			gotPrompt = req.Prompt
			if req.TargetID != "sbx-master" {
				t.Errorf("target_id: %s", req.TargetID)
			}
			if req.Skill != "fanout" {
				t.Errorf("skill: %s", req.Skill)
			}
			if req.TimeoutSeconds != 600 {
				t.Errorf("timeout: %d", req.TimeoutSeconds)
			}
			return &agentsdk.DelegateTaskResponse{TaskID: "t-1"}, nil
		},
	}
	tools := newTestTools(t, sdk)
	args := json.RawMessage(`{
        "prompt": "merge these",
        "read_paths": ["` + in1 + `", "` + in2 + `"],
        "write_paths": [{"path": "` + out + `", "overwrite": true}]
    }`)
	for _, tt := range tools.All() {
		if tt.Name() == "submit_task" {
			res, err := tt.Call(context.Background(), args)
			if err != nil {
				t.Fatalf("submit_task: %v", err)
			}
			var parsed struct {
				TaskID   string `json:"task_id"`
				Manifest struct {
					Files  []FileEntry         `json:"files"`
					Writes []WriteRequestEntry `json:"writes"`
				} `json:"manifest"`
			}
			json.Unmarshal(res, &parsed)
			if parsed.TaskID != "t-1" {
				t.Errorf("task_id: %s", parsed.TaskID)
			}
			if len(parsed.Manifest.Files) != 2 {
				t.Errorf("files: %+v", parsed.Manifest.Files)
			}
			if len(parsed.Manifest.Writes) != 1 || !parsed.Manifest.Writes[0].Overwrite {
				t.Errorf("writes: %+v", parsed.Manifest.Writes)
			}
			if !strings.Contains(gotPrompt, "<USER_FILES_MANIFEST") {
				t.Errorf("manifest not in prompt: %s", gotPrompt)
			}
			if !strings.Contains(gotPrompt, "merge these") {
				t.Errorf("user prompt not preserved: %s", gotPrompt)
			}
			return
		}
	}
	t.Fatal("submit_task tool not registered")
}

func TestTool_SubmitTask_RejectsAmbiguousTarget(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "m1", DisplayName: "master-a", Card: json.RawMessage(`{"skills":["fanout"]}`)},
				{AgentID: "m2", DisplayName: "master-b", Card: json.RawMessage(`{"skills":["fanout"]}`)},
			}, nil
		},
	}
	tools := newTestTools(t, sdk)
	for _, tt := range tools.All() {
		if tt.Name() == "submit_task" {
			_, err := tt.Call(context.Background(), json.RawMessage(`{"prompt":"x"}`))
			if err == nil {
				t.Fatal("expected ambiguous-target error")
			}
			if !strings.Contains(err.Error(), "ambiguous") && !strings.Contains(err.Error(), "candidates") {
				t.Errorf("error message: %v", err)
			}
			return
		}
	}
}

func TestTool_GetTask_ReturnsStatus(t *testing.T) {
	sdk := &fakeSDK{
		getTaskFunc: func(id string, _ bool) (*agentsdk.TaskInfo, error) {
			if id != "t-1" {
				return nil, errors.New("nope")
			}
			return &agentsdk.TaskInfo{TaskID: "t-1", Status: "completed", Output: "done"}, nil
		},
	}
	tools := newTestTools(t, sdk)
	for _, tt := range tools.All() {
		if tt.Name() == "get_task" {
			res, err := tt.Call(context.Background(), json.RawMessage(`{"task_id":"t-1"}`))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(res), `"status":"completed"`) {
				t.Errorf("res: %s", res)
			}
			return
		}
	}
}

func TestTool_WaitTask_ReturnsWrittenFiles(t *testing.T) {
	sdk := &fakeSDK{
		getTaskFunc: func(id string, _ bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{TaskID: id, Status: "completed", Output: "hi"}, nil
		},
	}
	tools := newTestTools(t, sdk)
	tools.reg.RecordWritten("t-2", WrittenFile{Path: "/p", Bytes: 5, SHA256: "s"})
	for _, tt := range tools.All() {
		if tt.Name() == "wait_task" {
			res, err := tt.Call(context.Background(),
				json.RawMessage(`{"task_id":"t-2","poll_interval_sec":1,"timeout_sec":5}`))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(res), `"written_files"`) || !strings.Contains(string(res), `"/p"`) {
				t.Errorf("res: %s", res)
			}
			return
		}
	}
}

func TestTool_TailSubtasks_PeerProxiesMaster(t *testing.T) {
	called := false
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "sbx-master", DisplayName: "master-prod",
					ShortID: "m-short",
					Card: json.RawMessage(`{"skills":["fanout"]}`)},
			}, nil
		},
		peerProxyFunc: func(method, target, path string, body io.Reader) (*http.Response, error) {
			called = true
			if target != "m-short" {
				t.Errorf("target: %s", target)
			}
			if !strings.Contains(path, "/tasks/t-9/children") {
				t.Errorf("path: %s", path)
			}
			body2 := `[{"node_id":"n1","status":"completed","target_id":"slv","created_at":"2026-01-01T00:00:00Z"}]`
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(body2)),
				Header:     http.Header{},
			}, nil
		},
	}
	tools := newTestTools(t, sdk)
	for _, tt := range tools.All() {
		if tt.Name() == "tail_subtasks" {
			res, err := tt.Call(context.Background(),
				json.RawMessage(`{"task_id":"t-9","since_seq":0,"max_wait_sec":1}`))
			if err != nil {
				t.Fatal(err)
			}
			if !called {
				t.Fatal("PeerProxy not called")
			}
			if !strings.Contains(string(res), `"events"`) {
				t.Errorf("res: %s", res)
			}
			return
		}
	}
}

func TestTool_CancelTask_StubReturnsNotSupported(t *testing.T) {
	sdk := &fakeSDK{
		getTaskFunc: func(id string, _ bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{TaskID: id, Status: "running"}, nil
		},
	}
	tools := newTestTools(t, sdk)
	for _, tt := range tools.All() {
		if tt.Name() == "cancel_task" {
			res, err := tt.Call(context.Background(), json.RawMessage(`{"task_id":"t-3"}`))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(res), `"ok":false`) {
				t.Errorf("res: %s", res)
			}
			if !strings.Contains(string(res), "running") {
				t.Errorf("res must include current status: %s", res)
			}
			return
		}
	}
}
