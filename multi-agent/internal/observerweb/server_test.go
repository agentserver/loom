package observerweb

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/observer"
	"github.com/yourorg/multi-agent/internal/observerstore"
)

func TestPostEventAuthAndViews(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.UpsertWorkspace(observerstore.Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{WorkspaceID: "ws1", ID: "driver", Role: observer.RoleDriver, DisplayName: "Driver"}, "tok"))

	h := New(st)
	body, _ := json.Marshal(observer.Event{
		WorkspaceID: "ws1", AgentID: "driver", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: "t1", Summary: "build thing", Status: "assigned",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/events", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusAccepted, rr.Code)

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/drivers", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "build thing")
}

func TestPostEventRejectsWrongAgent(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.UpsertWorkspace(observerstore.Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{WorkspaceID: "ws1", ID: "driver", Role: observer.RoleDriver, DisplayName: "Driver"}, "tok"))

	h := New(st)
	body, _ := json.Marshal(observer.Event{
		WorkspaceID: "ws1", AgentID: "other", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: "t1",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/events", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusForbidden, rr.Code)
}

func TestPostEventRejectsOversizedBody(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.UpsertWorkspace(observerstore.Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{WorkspaceID: "ws1", ID: "driver", Role: observer.RoleDriver, DisplayName: "Driver"}, "tok"))

	h := New(st)
	body := io.LimitReader(bytes.NewReader(append([]byte(`{"summary":"`), bytes.Repeat([]byte("x"), maxEventBodyBytes)...)), maxEventBodyBytes+1)
	req := httptest.NewRequest(http.MethodPost, "/api/events", body)
	req.Header.Set("Authorization", "Bearer tok")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusRequestEntityTooLarge, rr.Code)
}

func TestPostEventRejectsTrailingJSONValue(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.UpsertWorkspace(observerstore.Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{WorkspaceID: "ws1", ID: "driver", Role: observer.RoleDriver, DisplayName: "Driver"}, "tok"))

	h := New(st)
	body, _ := json.Marshal(observer.Event{
		WorkspaceID: "ws1", AgentID: "driver", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: "t1", Summary: "build thing", Status: "assigned",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/events", bytes.NewReader(append(body, []byte(` {}`)...)))
	req.Header.Set("Authorization", "Bearer tok")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)

	count, err := st.EventCount()
	require.NoError(t, err)
	require.Zero(t, count)
}

func TestArtifactLazyHTTPFlow(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.UpsertWorkspace(observerstore.Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{WorkspaceID: "ws1", ID: "driver", Role: observer.RoleDriver, DisplayName: "Driver"}, "driver-token"))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{WorkspaceID: "ws1", ID: "slave", Role: observer.RoleSlave, DisplayName: "Slave"}, "slave-token"))

	h := New(st)
	createBody := bytes.NewBufferString(`{"path":"/tmp/input.txt","kind":"file","mime":"text/plain","mode":"lazy"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/artifacts", createBody)
	req.Header.Set("Authorization", "Bearer driver-token")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
	var created struct {
		ArtifactID string `json:"artifact_id"`
		URL        string `json:"url"`
		State      string `json:"state"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &created))
	require.NotEmpty(t, created.ArtifactID)
	require.Contains(t, created.URL, "/api/artifacts/"+created.ArtifactID)

	req = httptest.NewRequest(http.MethodGet, "/api/artifacts/"+created.ArtifactID, nil)
	req.Header.Set("Authorization", "Bearer slave-token")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusAccepted, rr.Code, rr.Body.String())
	require.Equal(t, "2", rr.Header().Get("Retry-After"))

	req = httptest.NewRequest(http.MethodGet, "/api/artifact-requests", nil)
	req.Header.Set("Authorization", "Bearer driver-token")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), created.ArtifactID)

	req = httptest.NewRequest(http.MethodPut, "/api/artifacts/"+created.ArtifactID+"/content", bytes.NewBufferString("hello"))
	req.Header.Set("Authorization", "Bearer driver-token")
	req.Header.Set("Content-Type", "text/plain")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	req = httptest.NewRequest(http.MethodGet, "/api/artifacts/"+created.ArtifactID, nil)
	req.Header.Set("Authorization", "Bearer slave-token")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Equal(t, "hello", rr.Body.String())
	require.Equal(t, "text/plain", rr.Header().Get("Content-Type"))

	req = httptest.NewRequest(http.MethodGet, "/api/artifacts/"+created.ArtifactID+"?token=slave-token", nil)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Equal(t, "hello", rr.Body.String())
}

func TestWriteHTTPFlow(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.UpsertWorkspace(observerstore.Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{WorkspaceID: "ws1", ID: "driver", Role: observer.RoleDriver, DisplayName: "Driver"}, "driver-token"))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{WorkspaceID: "ws1", ID: "slave", Role: observer.RoleSlave, DisplayName: "Slave"}, "slave-token"))

	h := New(st)
	req := httptest.NewRequest(http.MethodPost, "/api/write-tokens", bytes.NewBufferString(`{"task_id":"task-1","path":"/tmp/out.txt","overwrite":true}`))
	req.Header.Set("Authorization", "Bearer driver-token")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
	var created struct {
		WriteID string `json:"write_id"`
		PutURL  string `json:"put_url"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &created))
	require.NotEmpty(t, created.WriteID)
	require.Contains(t, created.PutURL, "/api/writes/"+created.WriteID)

	req = httptest.NewRequest(http.MethodPut, "/api/writes/"+created.WriteID, bytes.NewBufferString("done"))
	req.Header.Set("Authorization", "Bearer slave-token")
	req.Header.Set("Content-Type", "text/plain")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	req = httptest.NewRequest(http.MethodGet, "/api/writes?task_id=task-1", nil)
	req.Header.Set("Authorization", "Bearer driver-token")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), `"path":"/tmp/out.txt"`)
	require.Contains(t, rr.Body.String(), `"content":"ZG9uZQ=="`)
}

func TestAPITasksExposesMCPToolDescriptors(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.UpsertWorkspace(observerstore.Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{WorkspaceID: "ws1", ID: "driver", Role: observer.RoleDriver, DisplayName: "Driver"}, "driver-token"))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{WorkspaceID: "ws1", ID: "master", Role: observer.RoleMaster, DisplayName: "Master"}, "master-token"))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{WorkspaceID: "ws1", ID: "slave", Role: observer.RoleSlave, DisplayName: "Slave"}, "slave-token"))

	require.NoError(t, st.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "driver", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: "mt1", Summary: "build thing",
		TargetAgentID: "master", Status: "assigned",
	}))
	require.NoError(t, st.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "master", AgentRole: observer.RoleMaster,
		Type: observer.EventMasterSubtaskDispatched, TaskID: "mt1", SubtaskID: "n1",
		ChildTaskID: "st1", SubtaskSummary: "make calculator", TargetAgentID: "slave",
		Status: "assigned",
	}))
	require.NoError(t, st.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "slave", AgentRole: observer.RoleSlave,
		Type: observer.EventMCPServerCreated, TaskID: "st1", ParentTaskID: "mt1",
		MCPServerName: "calc", MCPTools: []string{"add"},
		Payload: json.RawMessage(`{"mcp_tool_descriptors":[{"server":"calc","name":"add","input_schema":{"type":"object","properties":{"a":{"type":"number"}}}}]}`),
	}))

	rr := httptest.NewRecorder()
	New(st).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/tasks", nil))
	require.Equal(t, http.StatusOK, rr.Code)

	var tasks []struct {
		MCPServers []struct {
			ToolDescriptors json.RawMessage `json:"tool_descriptors"`
		} `json:"mcp_servers"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &tasks))
	require.Len(t, tasks, 1)
	require.Len(t, tasks[0].MCPServers, 1)
	require.JSONEq(t, `[{"server":"calc","name":"add","input_schema":{"type":"object","properties":{"a":{"type":"number"}}}}]`, string(tasks[0].MCPServers[0].ToolDescriptors))
}

func TestAPITasksIncludesLatestProgress(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.UpsertWorkspace(observerstore.Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{WorkspaceID: "ws1", ID: "driver", Role: observer.RoleDriver, DisplayName: "Driver"}, "driver-token"))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{WorkspaceID: "ws1", ID: "master", Role: observer.RoleMaster, DisplayName: "Master"}, "master-token"))

	require.NoError(t, st.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "driver", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: "t1", Summary: "build thing",
		TargetAgentID: "master", TargetRole: observer.RoleMaster, Status: "assigned",
	}))
	require.NoError(t, st.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "master", AgentRole: observer.RoleMaster,
		Type: observer.EventMasterPlanningProgress, TaskID: "t1",
		Payload: json.RawMessage(`{"phase":"planning","message":"planner still running","is_final":false}`),
	}))

	rr := httptest.NewRecorder()
	New(st).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/tasks", nil))
	require.Equal(t, http.StatusOK, rr.Code)

	var tasks []observerstore.TaskView
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &tasks))
	require.Len(t, tasks, 1)
	require.Equal(t, "planner still running", tasks[0].LatestProgress)
	require.False(t, tasks[0].IsFinal)
}

func TestTaskContractAPI(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.UpsertWorkspace(observerstore.Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{WorkspaceID: "ws1", ID: "driver", Role: observer.RoleDriver, DisplayName: "Driver"}, "driver-token"))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{WorkspaceID: "ws1", ID: "master", Role: observer.RoleMaster, DisplayName: "Master"}, "master-token"))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{WorkspaceID: "ws1", ID: "slave", Role: observer.RoleSlave, DisplayName: "Slave"}, "slave-token"))
	h := New(st)

	body := `{"task_id":"task-1","conversation_id":"conv-1","body":{"version":1,"intent":{"goal":"g","success_criteria":["s"]}}}`
	req := httptest.NewRequest(http.MethodPost, "/api/task-contracts", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer driver-token")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusCreated, rr.Code)

	req = httptest.NewRequest(http.MethodPost, "/api/task-contracts", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer slave-token")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusForbidden, rr.Code)

	req = httptest.NewRequest(http.MethodGet, "/api/task-contracts/task-1", nil)
	req.Header.Set("Authorization", "Bearer slave-token")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusForbidden, rr.Code)

	req = httptest.NewRequest(http.MethodGet, "/api/task-contracts/task-1", nil)
	req.Header.Set("Authorization", "Bearer master-token")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	var got observerstore.TaskContractRecord
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&got))
	require.Equal(t, "conv-1", got.ConversationID)
}

func TestResourceSnapshotAPIRestrictsRoles(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.UpsertWorkspace(observerstore.Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{WorkspaceID: "ws1", ID: "driver", Role: observer.RoleDriver, DisplayName: "Driver"}, "driver-token"))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{WorkspaceID: "ws1", ID: "master", Role: observer.RoleMaster, DisplayName: "Master"}, "master-token"))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{WorkspaceID: "ws1", ID: "slave", Role: observer.RoleSlave, DisplayName: "Slave"}, "slave-token"))
	h := New(st)

	body := `{"snapshot_id":"snap-1","body":{"generated_at":"2026-05-14T00:00:00Z","agents":[]}}`
	req := httptest.NewRequest(http.MethodPost, "/api/resource-snapshots", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer slave-token")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusForbidden, rr.Code)

	req = httptest.NewRequest(http.MethodPost, "/api/resource-snapshots", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer driver-token")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusCreated, rr.Code)

	req = httptest.NewRequest(http.MethodGet, "/api/resource-snapshots/latest", nil)
	req.Header.Set("Authorization", "Bearer slave-token")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusForbidden, rr.Code)

	req = httptest.NewRequest(http.MethodGet, "/api/resource-snapshots/latest", nil)
	req.Header.Set("Authorization", "Bearer master-token")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
}

func TestAPITasksAndEventsExposeValidationFailureEvidence(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.UpsertWorkspace(observerstore.Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{WorkspaceID: "ws1", ID: "driver", Role: observer.RoleDriver, DisplayName: "Driver"}, "driver-token"))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{WorkspaceID: "ws1", ID: "master", Role: observer.RoleMaster, DisplayName: "Master"}, "master-token"))
	require.NoError(t, st.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "driver", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: "mt1", Summary: "build thing",
		TargetAgentID: "master", TargetRole: observer.RoleMaster, Status: "assigned",
	}))
	require.NoError(t, st.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "master", AgentRole: observer.RoleMaster,
		Type: observer.EventMasterMCPCallValidationFailed, TaskID: "mt1", SubtaskID: "n1",
		Status:  "failed",
		Payload: json.RawMessage(`{"validation_error":"unknown argument put_url_128","required":true}`),
	}))

	h := New(st)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/tasks", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	var tasks []struct {
		Events []observer.Event `json:"events"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &tasks))
	require.Len(t, tasks, 1)
	require.Len(t, tasks[0].Events, 2)
	require.Equal(t, observer.EventMasterMCPCallValidationFailed, tasks[0].Events[1].Type)
	require.JSONEq(t, `{"validation_error":"unknown argument put_url_128","required":true}`, string(tasks[0].Events[1].Payload))

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/events?task_id=mt1", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	var events []observer.Event
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &events))
	require.Len(t, events, 2)
	require.Equal(t, observer.EventMasterMCPCallValidationFailed, events[1].Type)
}

func TestAPITasksEncodesEmptyCollectionsAsArrays(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.UpsertWorkspace(observerstore.Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{WorkspaceID: "ws1", ID: "driver", Role: observer.RoleDriver, DisplayName: "Driver"}, "driver-token"))
	require.NoError(t, st.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "driver", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: "mt1", Summary: "standalone task",
		Status: "assigned",
	}))

	rr := httptest.NewRecorder()
	New(st).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/tasks", nil))
	require.Equal(t, http.StatusOK, rr.Code)

	var tasks []struct {
		Subtasks   json.RawMessage `json:"subtasks"`
		MCPServers json.RawMessage `json:"mcp_servers"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &tasks))
	require.Len(t, tasks, 1)
	require.JSONEq(t, `[]`, string(tasks[0].Subtasks))
	require.JSONEq(t, `[]`, string(tasks[0].MCPServers))
}

func TestRolePagesRenderDistinctViewsAndMCPStatus(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.UpsertWorkspace(observerstore.Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{WorkspaceID: "ws1", ID: "driver", Role: observer.RoleDriver, DisplayName: "Driver"}, "driver-token"))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{WorkspaceID: "ws1", ID: "master", Role: observer.RoleMaster, DisplayName: "Master"}, "master-token"))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{WorkspaceID: "ws1", ID: "slave", Role: observer.RoleSlave, DisplayName: "Slave"}, "slave-token"))
	require.NoError(t, st.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "driver", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: "mt1", Summary: "build thing",
		TargetAgentID: "master", Status: "assigned",
	}))
	require.NoError(t, st.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "master", AgentRole: observer.RoleMaster,
		Type: observer.EventMasterSubtaskDispatched, TaskID: "mt1", SubtaskID: "n1",
		ChildTaskID: "st1", SubtaskSummary: "make tool", TargetAgentID: "slave",
		Status: "assigned",
	}))
	require.NoError(t, st.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "slave", AgentRole: observer.RoleSlave,
		Type: observer.EventMCPServerBlocked, TaskID: "st1", MCPServerName: "calc",
		Status: "blocked",
	}))

	h := New(st)
	for _, tc := range []struct {
		path string
		want string
	}{
		{path: "/drivers", want: "Slaves"},
		{path: "/masters", want: "Decomposition"},
		{path: "/slaves", want: "Task / Subtask"},
		{path: "/slaves", want: "blocked"},
	} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, tc.path, nil))
		require.Equal(t, http.StatusOK, rr.Code)
		require.Contains(t, rr.Body.String(), tc.want)
	}
}

func TestRolePagesRenderLatestProgress(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.UpsertWorkspace(observerstore.Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{WorkspaceID: "ws1", ID: "driver", Role: observer.RoleDriver, DisplayName: "Driver"}, "driver-token"))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{WorkspaceID: "ws1", ID: "master", Role: observer.RoleMaster, DisplayName: "Master"}, "master-token"))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{WorkspaceID: "ws1", ID: "slave", Role: observer.RoleSlave, DisplayName: "Slave"}, "slave-token"))
	require.NoError(t, st.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "driver", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: "mt1", Summary: "build thing",
		TargetAgentID: "master", Status: "assigned",
	}))
	require.NoError(t, st.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "master", AgentRole: observer.RoleMaster,
		Type: observer.EventMasterPlanningProgress, TaskID: "mt1",
		Payload: json.RawMessage(`{"phase":"planning","message":"planner still running","is_final":false}`),
	}))
	require.NoError(t, st.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "master", AgentRole: observer.RoleMaster,
		Type: observer.EventMasterSubtaskDispatched, TaskID: "mt1", SubtaskID: "n1",
		ChildTaskID: "st1", SubtaskSummary: "make tool", TargetAgentID: "slave",
		Status: "assigned",
	}))
	require.NoError(t, st.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "slave", AgentRole: observer.RoleSlave,
		Type: observer.EventSlaveTaskProgress, TaskID: "st1",
		Payload: json.RawMessage(`{"phase":"build","message":"slave halfway done"}`),
	}))

	h := New(st)
	for _, tc := range []struct {
		path string
		want string
	}{
		{path: "/drivers", want: "Progress: planning - planner still running"},
		{path: "/masters", want: "Progress: planning - planner still running"},
		{path: "/masters", want: "Progress: build - slave halfway done"},
		{path: "/slaves", want: "Progress: build - slave halfway done"},
	} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, tc.path, nil))
		require.Equal(t, http.StatusOK, rr.Code)
		require.Contains(t, rr.Body.String(), tc.want)
	}
}

func TestSlavesPageFiltersOutDriverOnlyTasks(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.UpsertWorkspace(observerstore.Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{WorkspaceID: "ws1", ID: "driver", Role: observer.RoleDriver, DisplayName: "Driver"}, "driver-token"))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{WorkspaceID: "ws1", ID: "master", Role: observer.RoleMaster, DisplayName: "Master"}, "master-token"))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{WorkspaceID: "ws1", ID: "slave", Role: observer.RoleSlave, DisplayName: "Slave"}, "slave-token"))
	require.NoError(t, st.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "driver", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: "driver-only", Summary: "driver only",
		TargetAgentID: "master", Status: "assigned",
	}))
	require.NoError(t, st.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "driver", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: "with-slave", Summary: "with slave",
		TargetAgentID: "master", Status: "assigned",
	}))
	require.NoError(t, st.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "master", AgentRole: observer.RoleMaster,
		Type: observer.EventMasterSubtaskDispatched, TaskID: "with-slave", SubtaskID: "n1",
		ChildTaskID: "st1", SubtaskSummary: "make tool", TargetAgentID: "slave",
		Status: "assigned",
	}))

	rr := httptest.NewRecorder()
	New(st).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/slaves", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	require.NotContains(t, rr.Body.String(), "driver only")
	require.Contains(t, rr.Body.String(), "with slave")
}

func TestSlavesPageShowsDirectSlaveTask(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.UpsertWorkspace(observerstore.Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{WorkspaceID: "ws1", ID: "driver", Role: observer.RoleDriver, DisplayName: "Driver"}, "driver-token"))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{WorkspaceID: "ws1", ID: "slave", Role: observer.RoleSlave, DisplayName: "Slave"}, "slave-token"))
	require.NoError(t, st.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "driver", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: "task-direct", Summary: "run benchmark",
		TargetAgentID: "agentserver-slave-id", Status: "assigned",
	}))
	require.NoError(t, st.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "slave", AgentRole: observer.RoleSlave,
		Type: observer.EventSlaveTaskCompleted, TaskID: "task-direct", Status: "completed",
		Payload: json.RawMessage(`{"output":"done"}`),
	}))

	rr := httptest.NewRecorder()
	New(st).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/slaves", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "run benchmark")
	require.Contains(t, rr.Body.String(), "slave")
	require.Contains(t, rr.Body.String(), "completed")
	require.Contains(t, rr.Body.String(), "done")
}

func TestRegisterSuccessAndIssuedTokenIngests(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.UpsertWorkspace(observerstore.Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, st.UpsertAPIKey("ws1", "ak-default", "ak_secret"))

	h := New(st)

	body := bytes.NewBufferString(`{"agent_id":"slave-a","role":"slave","display_name":"Slave A"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agents/register", body)
	req.Header.Set("Authorization", "Bearer ak_secret")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var resp struct {
		WorkspaceID string `json:"workspace_id"`
		AgentID     string `json:"agent_id"`
		Role        string `json:"role"`
		DisplayName string `json:"display_name"`
		Token       string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Equal(t, "ws1", resp.WorkspaceID)
	require.Equal(t, "slave-a", resp.AgentID)
	require.Equal(t, observer.RoleSlave, resp.Role)
	require.Equal(t, "Slave A", resp.DisplayName)
	require.Len(t, resp.Token, 64, "token must be 32 random bytes hex-encoded")

	// Issued token must work for ingest end-to-end.
	evBody, _ := json.Marshal(observer.Event{
		WorkspaceID: "ws1", AgentID: "slave-a", AgentRole: observer.RoleSlave,
		Type: observer.EventDriverTaskSubmitted, TaskID: "t1", Summary: "first", Status: "assigned",
	})
	ev := httptest.NewRequest(http.MethodPost, "/api/events", bytes.NewReader(evBody))
	ev.Header.Set("Authorization", "Bearer "+resp.Token)
	ev2 := httptest.NewRecorder()
	h.ServeHTTP(ev2, ev)
	require.Equal(t, http.StatusAccepted, ev2.Code, ev2.Body.String())
}

func TestRegisterDefaultsDisplayNameToAgentID(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.UpsertWorkspace(observerstore.Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, st.UpsertAPIKey("ws1", "ak-default", "ak_secret"))

	h := New(st)
	body := bytes.NewBufferString(`{"agent_id":"slave-a","role":"slave"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agents/register", body)
	req.Header.Set("Authorization", "Bearer ak_secret")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var resp struct {
		DisplayName string `json:"display_name"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Equal(t, "slave-a", resp.DisplayName)
}

func TestRegisterRejectsBadAPIKey(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.UpsertWorkspace(observerstore.Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, st.UpsertAPIKey("ws1", "ak-default", "ak_real"))

	h := New(st)
	body := bytes.NewBufferString(`{"agent_id":"slave-a","role":"slave"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agents/register", body)
	req.Header.Set("Authorization", "Bearer ak_FAKE")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusForbidden, rr.Code)
}

func TestRegisterRejectsMissingBearer(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.UpsertWorkspace(observerstore.Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, st.UpsertAPIKey("ws1", "ak-default", "ak_real"))

	h := New(st)
	body := bytes.NewBufferString(`{"agent_id":"slave-a","role":"slave"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agents/register", body)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestRegisterRejectsBadRole(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.UpsertWorkspace(observerstore.Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, st.UpsertAPIKey("ws1", "ak-default", "ak_real"))

	h := New(st)
	body := bytes.NewBufferString(`{"agent_id":"slave-a","role":"hacker"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agents/register", body)
	req.Header.Set("Authorization", "Bearer ak_real")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "role")
}

func TestRegisterRejectsBadAgentID(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.UpsertWorkspace(observerstore.Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, st.UpsertAPIKey("ws1", "ak-default", "ak_real"))

	h := New(st)
	cases := []string{
		`{"agent_id":"","role":"slave"}`,                  // empty
		`{"agent_id":"has space","role":"slave"}`,         // space
		`{"agent_id":"weird/slash","role":"slave"}`,       // slash
		`{"agent_id":"` + strings.Repeat("a", 65) + `","role":"slave"}`, // too long
	}
	for _, body := range cases {
		req := httptest.NewRequest(http.MethodPost, "/api/agents/register", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer ak_real")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		require.Equal(t, http.StatusBadRequest, rr.Code, "body=%s", body)
	}
}

func TestRegisterRejectsBadJSON(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.UpsertWorkspace(observerstore.Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, st.UpsertAPIKey("ws1", "ak-default", "ak_real"))

	h := New(st)
	req := httptest.NewRequest(http.MethodPost, "/api/agents/register", bytes.NewBufferString(`{not json`))
	req.Header.Set("Authorization", "Bearer ak_real")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestRegisterRejectsWrongMethod(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.UpsertWorkspace(observerstore.Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, st.UpsertAPIKey("ws1", "ak-default", "ak_real"))

	h := New(st)
	req := httptest.NewRequest(http.MethodGet, "/api/agents/register", nil)
	req.Header.Set("Authorization", "Bearer ak_real")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

func TestRegisterReissueInvalidatesOldToken(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.UpsertWorkspace(observerstore.Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, st.UpsertAPIKey("ws1", "ak-default", "ak_real"))

	h := New(st)
	register := func() string {
		body := bytes.NewBufferString(`{"agent_id":"slave-a","role":"slave","display_name":"Slave A"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/agents/register", body)
		req.Header.Set("Authorization", "Bearer ak_real")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		var resp struct {
			Token string `json:"token"`
		}
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
		require.NotEmpty(t, resp.Token)
		return resp.Token
	}

	first := register()
	second := register()
	require.NotEqual(t, first, second, "reissue must return a new token")

	// Old token: ingest now fails 401.
	evBody, _ := json.Marshal(observer.Event{
		WorkspaceID: "ws1", AgentID: "slave-a", AgentRole: observer.RoleSlave,
		Type: observer.EventDriverTaskSubmitted, TaskID: "t1", Summary: "x", Status: "assigned",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/events", bytes.NewReader(evBody))
	req.Header.Set("Authorization", "Bearer "+first)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusUnauthorized, rr.Code, "old token must be invalidated")

	// New token still works.
	req = httptest.NewRequest(http.MethodPost, "/api/events", bytes.NewReader(evBody))
	req.Header.Set("Authorization", "Bearer "+second)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusAccepted, rr.Code, rr.Body.String())
}

func TestRegisterRejectsPerAgentTokenAsAPIKey(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.UpsertWorkspace(observerstore.Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, st.UpsertAPIKey("ws1", "ak-default", "ak_real"))
	// Statically seed an agent token (mimics a per-agent token issued by an
	// earlier register call).
	require.NoError(t, st.UpsertAgent(
		observerstore.Agent{WorkspaceID: "ws1", ID: "slave-a", Role: observer.RoleSlave, DisplayName: "Slave A"},
		"leaked_agent_token",
	))

	h := New(st)
	body := bytes.NewBufferString(`{"agent_id":"slave-b","role":"slave"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agents/register", body)
	req.Header.Set("Authorization", "Bearer leaked_agent_token")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusForbidden, rr.Code,
		"per-agent token must not be accepted as an api-key")
}
