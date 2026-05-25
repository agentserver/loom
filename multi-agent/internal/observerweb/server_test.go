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

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newTestHandler creates a fresh store + HTTP handler pair backed by a
// temporary on-disk SQLite database (avoids multi-connection ":memory:" issues).
func newTestHandler(t *testing.T) (http.Handler, *observerstore.Store) {
	t.Helper()
	st, err := observerstore.New(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })
	return New(st), st
}

// seedAPIKey adds one api_key row to st.
func seedAPIKey(t *testing.T, st *observerstore.Store, id, key string) {
	t.Helper()
	require.NoError(t, st.UpsertAPIKey(observerstore.APIKeySpec{ID: id, Key: key}))
}

// postRegister POSTs to /api/agents/register and asserts the expected status.
// Returns the raw body string so callers can inspect it.
func postRegister(t *testing.T, h http.Handler, apiKey, jsonBody string, wantStatus int) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/agents/register", strings.NewReader(jsonBody))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, wantStatus, rr.Code, "body=%s", rr.Body.String())
	return rr.Body.String()
}

// extractToken decodes a register response body and asserts the token is non-empty.
func extractToken(t *testing.T, body string) string {
	t.Helper()
	var resp struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &resp))
	require.NotEmpty(t, resp.Token)
	return resp.Token
}

// seedWorkspaceAndAgents is a helper that sets up a workspace with some agents
// for tests that don't exercise the register endpoint.
func seedWorkspaceAndAgents(t *testing.T, st *observerstore.Store) {
	t.Helper()
	require.NoError(t, st.UpsertAPIKey(observerstore.APIKeySpec{ID: "ak-seed", Key: "seed-key"}))
	require.NoError(t, st.UpsertWorkspaceLazy("ws1", "Workspace", "ak-seed"))
	require.NoError(t, st.UpsertAgent(
		observerstore.Agent{WorkspaceID: "ws1", ID: "driver", Role: observer.RoleDriver, DisplayName: "Driver"},
		"driver-token", "ak-seed",
	))
	require.NoError(t, st.UpsertAgent(
		observerstore.Agent{WorkspaceID: "ws1", ID: "master", Role: observer.RoleMaster, DisplayName: "Master"},
		"master-token", "ak-seed",
	))
	require.NoError(t, st.UpsertAgent(
		observerstore.Agent{WorkspaceID: "ws1", ID: "slave", Role: observer.RoleSlave, DisplayName: "Slave"},
		"slave-token", "ak-seed",
	))
}

// ---------------------------------------------------------------------------
// Event / view tests (non-register)
// ---------------------------------------------------------------------------

func TestPostEventAuthAndViews(t *testing.T) {
	h, st := newTestHandler(t)
	seedWorkspaceAndAgents(t, st)

	body, _ := json.Marshal(observer.Event{
		WorkspaceID: "ws1", AgentID: "driver", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: "t1", Summary: "build thing", Status: "assigned",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/events", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer driver-token")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusAccepted, rr.Code)

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/drivers", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "build thing")
}

func TestPostEventRejectsWrongAgent(t *testing.T) {
	h, st := newTestHandler(t)
	seedWorkspaceAndAgents(t, st)

	body, _ := json.Marshal(observer.Event{
		WorkspaceID: "ws1", AgentID: "other", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: "t1",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/events", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer driver-token")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusForbidden, rr.Code)
}

func TestPostEventRejectsOversizedBody(t *testing.T) {
	h, st := newTestHandler(t)
	seedWorkspaceAndAgents(t, st)

	body := io.LimitReader(bytes.NewReader(append([]byte(`{"summary":"`), bytes.Repeat([]byte("x"), maxEventBodyBytes)...)), maxEventBodyBytes+1)
	req := httptest.NewRequest(http.MethodPost, "/api/events", body)
	req.Header.Set("Authorization", "Bearer driver-token")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusRequestEntityTooLarge, rr.Code)
}

func TestPostEventRejectsTrailingJSONValue(t *testing.T) {
	h, st := newTestHandler(t)
	seedWorkspaceAndAgents(t, st)

	body, _ := json.Marshal(observer.Event{
		WorkspaceID: "ws1", AgentID: "driver", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: "t1", Summary: "build thing", Status: "assigned",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/events", bytes.NewReader(append(body, []byte(` {}`)...)))
	req.Header.Set("Authorization", "Bearer driver-token")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)

	count, err := st.EventCount()
	require.NoError(t, err)
	require.Zero(t, count)
}

func TestArtifactLazyHTTPFlow(t *testing.T) {
	h, st := newTestHandler(t)
	seedWorkspaceAndAgents(t, st)

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
	h, st := newTestHandler(t)
	seedWorkspaceAndAgents(t, st)

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
	h, st := newTestHandler(t)
	seedWorkspaceAndAgents(t, st)

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
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/tasks", nil))
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
	h, st := newTestHandler(t)
	seedWorkspaceAndAgents(t, st)

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
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/tasks", nil))
	require.Equal(t, http.StatusOK, rr.Code)

	var tasks []observerstore.TaskView
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &tasks))
	require.Len(t, tasks, 1)
	require.Equal(t, "planner still running", tasks[0].LatestProgress)
	require.False(t, tasks[0].IsFinal)
}

func TestTaskContractAPI(t *testing.T) {
	h, st := newTestHandler(t)
	seedWorkspaceAndAgents(t, st)

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
	h, st := newTestHandler(t)
	seedWorkspaceAndAgents(t, st)

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
	h, st := newTestHandler(t)
	seedWorkspaceAndAgents(t, st)

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
	h, st := newTestHandler(t)
	seedWorkspaceAndAgents(t, st)

	require.NoError(t, st.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "driver", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: "mt1", Summary: "standalone task",
		Status: "assigned",
	}))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/tasks", nil))
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
	h, st := newTestHandler(t)
	seedWorkspaceAndAgents(t, st)

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
	h, st := newTestHandler(t)
	seedWorkspaceAndAgents(t, st)

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
	h, st := newTestHandler(t)
	seedWorkspaceAndAgents(t, st)

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
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/slaves", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	require.NotContains(t, rr.Body.String(), "driver only")
	require.Contains(t, rr.Body.String(), "with slave")
}

func TestSlavesPageShowsDirectSlaveTask(t *testing.T) {
	h, st := newTestHandler(t)
	seedWorkspaceAndAgents(t, st)

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
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/slaves", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "run benchmark")
	require.Contains(t, rr.Body.String(), "slave")
	require.Contains(t, rr.Body.String(), "completed")
	require.Contains(t, rr.Body.String(), "done")
}

// ---------------------------------------------------------------------------
// Register endpoint tests
// ---------------------------------------------------------------------------

func TestRegisterSuccessAndIssuedTokenIngests(t *testing.T) {
	h, st := newTestHandler(t)
	seedAPIKey(t, st, "ak-default", "ak_secret")

	body := postRegister(t, h, "ak_secret",
		`{"agent_id":"slave-a","role":"slave","display_name":"Slave A","workspace_id":"ws1"}`,
		http.StatusOK)

	var resp struct {
		WorkspaceID string `json:"workspace_id"`
		AgentID     string `json:"agent_id"`
		Role        string `json:"role"`
		DisplayName string `json:"display_name"`
		Token       string `json:"token"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &resp))
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
	h, st := newTestHandler(t)
	seedAPIKey(t, st, "ak-default", "ak_secret")

	body := postRegister(t, h, "ak_secret",
		`{"agent_id":"slave-a","role":"slave","workspace_id":"ws1"}`,
		http.StatusOK)

	var resp struct {
		DisplayName string `json:"display_name"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &resp))
	require.Equal(t, "slave-a", resp.DisplayName)
}

func TestRegisterRejectsBadAPIKey(t *testing.T) {
	h, st := newTestHandler(t)
	seedAPIKey(t, st, "ak-default", "ak_real")

	// Bad API key must return 401 (token error), not 403.
	postRegister(t, h, "ak_FAKE",
		`{"agent_id":"slave-a","role":"slave","workspace_id":"ws1"}`,
		http.StatusUnauthorized)
}

func TestRegisterRejectsMissingBearer(t *testing.T) {
	h, st := newTestHandler(t)
	seedAPIKey(t, st, "ak-default", "ak_real")

	req := httptest.NewRequest(http.MethodPost, "/api/agents/register",
		bytes.NewBufferString(`{"agent_id":"slave-a","role":"slave","workspace_id":"ws1"}`))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestRegisterRejectsBadRole(t *testing.T) {
	h, st := newTestHandler(t)
	seedAPIKey(t, st, "ak-default", "ak_real")

	body := postRegister(t, h, "ak_real",
		`{"agent_id":"slave-a","role":"hacker","workspace_id":"ws1"}`,
		http.StatusBadRequest)
	require.Contains(t, body, "role")
}

func TestRegisterRejectsBadAgentID(t *testing.T) {
	h, st := newTestHandler(t)
	seedAPIKey(t, st, "ak-default", "ak_real")

	cases := []string{
		`{"agent_id":"","role":"slave","workspace_id":"ws1"}`,                                     // empty
		`{"agent_id":"has space","role":"slave","workspace_id":"ws1"}`,                            // space
		`{"agent_id":"weird/slash","role":"slave","workspace_id":"ws1"}`,                          // slash
		`{"agent_id":"` + strings.Repeat("a", 65) + `","role":"slave","workspace_id":"ws1"}`,     // too long
	}
	for _, body := range cases {
		postRegister(t, h, "ak_real", body, http.StatusBadRequest)
	}
}

func TestRegisterRejectsBadJSON(t *testing.T) {
	h, st := newTestHandler(t)
	seedAPIKey(t, st, "ak-default", "ak_real")

	postRegister(t, h, "ak_real", `{not json`, http.StatusBadRequest)
}

func TestRegisterRejectsWrongMethod(t *testing.T) {
	h, st := newTestHandler(t)
	seedAPIKey(t, st, "ak-default", "ak_real")

	req := httptest.NewRequest(http.MethodGet, "/api/agents/register", nil)
	req.Header.Set("Authorization", "Bearer ak_real")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

func TestRegisterReissueInvalidatesOldToken(t *testing.T) {
	h, st := newTestHandler(t)
	seedAPIKey(t, st, "ak-default", "ak_real")

	register := func() string {
		body := postRegister(t, h, "ak_real",
			`{"agent_id":"slave-a","role":"slave","display_name":"Slave A","workspace_id":"ws1"}`,
			http.StatusOK)
		return extractToken(t, body)
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
	h, st := newTestHandler(t)
	seedAPIKey(t, st, "ak-default", "ak_real")
	// Statically seed an agent token (mimics a per-agent token issued by an
	// earlier register call).
	require.NoError(t, st.UpsertAgent(
		observerstore.Agent{WorkspaceID: "ws1", ID: "slave-a", Role: observer.RoleSlave, DisplayName: "Slave A"},
		"leaked_agent_token", "ak-default",
	))

	// Note: per-agent token is not an api_key, so LookupAPIKey returns !ok → 401.
	postRegister(t, h, "leaked_agent_token",
		`{"agent_id":"slave-b","role":"slave","workspace_id":"ws1"}`,
		http.StatusUnauthorized)
}

// ---------------------------------------------------------------------------
// New register tests (Task 5)
// ---------------------------------------------------------------------------

func TestRegister_RejectsMissingWorkspaceID(t *testing.T) {
	h, st := newTestHandler(t)
	seedAPIKey(t, st, "ak-1", "key1")
	body := postRegister(t, h, "key1", `{"agent_id":"a","role":"slave"}`, http.StatusBadRequest)
	require.Contains(t, body, "workspace_id must match")
}

func TestRegister_RejectsRebinding(t *testing.T) {
	h, st := newTestHandler(t)
	seedAPIKey(t, st, "ak-1", "key1")
	postRegister(t, h, "key1", `{"agent_id":"a","role":"slave","workspace_id":"ws-A"}`, http.StatusOK)
	body := postRegister(t, h, "key1", `{"agent_id":"a","role":"slave","workspace_id":"ws-B"}`, http.StatusConflict)
	require.Contains(t, body, "already bound to workspace ws-A")
}

func TestRegister_NameStickyOnSecondRegister(t *testing.T) {
	h, st := newTestHandler(t)
	seedAPIKey(t, st, "ak-1", "key1")
	postRegister(t, h, "key1", `{"agent_id":"a","role":"slave","workspace_id":"ws-X","workspace_name":"First"}`, http.StatusOK)
	postRegister(t, h, "key1", `{"agent_id":"b","role":"slave","workspace_id":"ws-X","workspace_name":"Second"}`, http.StatusOK)
	sums, err := st.ListWorkspaceSummaries()
	require.NoError(t, err)
	require.Len(t, sums, 1)
	require.Equal(t, "First", sums[0].Name, "first writer must win the name")
}

func TestRegister_RebindRejectedDoesNotCreateOrphanWorkspace(t *testing.T) {
	h, st := newTestHandler(t)
	seedAPIKey(t, st, "ak-1", "key1")
	postRegister(t, h, "key1", `{"agent_id":"a","role":"slave","workspace_id":"ws-A"}`, http.StatusOK)
	postRegister(t, h, "key1", `{"agent_id":"a","role":"slave","workspace_id":"ws-LEAK"}`, http.StatusConflict)
	sums, err := st.ListWorkspaceSummaries()
	require.NoError(t, err)
	for _, s := range sums {
		require.NotEqual(t, "ws-LEAK", s.ID, "rejected rebind must not create the workspace")
	}
}

func TestRegister_TokenRotation(t *testing.T) {
	h, st := newTestHandler(t)
	seedAPIKey(t, st, "ak-1", "key1")
	body1 := postRegister(t, h, "key1", `{"agent_id":"a","role":"slave","workspace_id":"ws-A"}`, http.StatusOK)
	body2 := postRegister(t, h, "key1", `{"agent_id":"a","role":"slave","workspace_id":"ws-A"}`, http.StatusOK)
	require.NotEqual(t, extractToken(t, body1), extractToken(t, body2))
	_, ok, err := st.ValidateToken(extractToken(t, body1))
	require.NoError(t, err)
	require.False(t, ok, "old token must be invalidated after rotation")
}

// ---------------------------------------------------------------------------
// GET /api/workspaces tests (Task 6)
// ---------------------------------------------------------------------------

func TestListWorkspaces_HappyPath(t *testing.T) {
	h, st := newTestHandler(t)
	seedAPIKey(t, st, "ak-1", "key1")
	postRegister(t, h, "key1", `{"agent_id":"a","role":"slave","workspace_id":"ws-1","workspace_name":"One"}`, http.StatusOK)
	postRegister(t, h, "key1", `{"agent_id":"b","role":"slave","workspace_id":"ws-2","workspace_name":"Two"}`, http.StatusOK)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/workspaces", nil))
	require.Equal(t, http.StatusOK, rr.Code)

	var got []observerstore.WorkspaceSummary
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &got))
	require.Len(t, got, 2)
	require.Equal(t, "ws-2", got[0].ID, "ordered by last_seen DESC")
}

// ---------------------------------------------------------------------------
// E2E multi-workspace isolation (Task 9)
// ---------------------------------------------------------------------------

func ingestEvent(t *testing.T, h http.Handler, token, wsID, agentID, role, eventType, taskID string) {
	t.Helper()
	body, _ := json.Marshal(observer.Event{
		WorkspaceID: wsID,
		AgentID:     agentID,
		AgentRole:   role,
		Type:        eventType,
		TaskID:      taskID,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/events", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusAccepted, rr.Code, "ingest body=%s", rr.Body.String())
}

func TestE2E_MultiWorkspaceIsolation(t *testing.T) {
	h, st := newTestHandler(t)
	seedAPIKey(t, st, "ak-1", "key1")

	// Register 3 agents across 2 workspaces.
	bodyA := postRegister(t, h, "key1",
		`{"agent_id":"driver-A","role":"driver","workspace_id":"ws-personal","workspace_name":"Personal"}`,
		http.StatusOK)
	bodyB := postRegister(t, h, "key1",
		`{"agent_id":"slave-A","role":"slave","workspace_id":"ws-personal"}`,
		http.StatusOK)
	bodyC := postRegister(t, h, "key1",
		`{"agent_id":"slave-W","role":"slave","workspace_id":"ws-work","workspace_name":"Work"}`,
		http.StatusOK)
	tokA, tokB, tokC := extractToken(t, bodyA), extractToken(t, bodyB), extractToken(t, bodyC)

	// Each emits an event in its own workspace.
	ingestEvent(t, h, tokA, "ws-personal", "driver-A", "driver", observer.EventDriverTaskSubmitted, "personal-task-1")
	ingestEvent(t, h, tokB, "ws-personal", "slave-A", "slave", observer.EventSlaveTaskStarted, "personal-task-1")
	ingestEvent(t, h, tokC, "ws-work", "slave-W", "slave", observer.EventSlaveTaskStarted, "work-task-1")

	// GET /api/workspaces returns both, ordered by last_seen DESC.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/workspaces", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	var sums []observerstore.WorkspaceSummary
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &sums))
	require.Len(t, sums, 2)

	// Verify cross-workspace isolation via Store's public read.
	personalEvents, err := st.ListEventsForWorkspace("ws-personal")
	require.NoError(t, err)
	workEvents, err := st.ListEventsForWorkspace("ws-work")
	require.NoError(t, err)
	require.Len(t, personalEvents, 2, "ws-personal must have exactly 2 events")
	require.Len(t, workEvents, 1, "ws-work must have exactly 1 event")
}

func TestListWorkspaces_WebTokenGuard(t *testing.T) {
	t.Setenv("OBSERVER_WEB_TOKEN", "secret")
	h, _ := newTestHandler(t)

	// missing token → 401
	rr1 := httptest.NewRecorder()
	h.ServeHTTP(rr1, httptest.NewRequest(http.MethodGet, "/api/workspaces", nil))
	require.Equal(t, http.StatusUnauthorized, rr1.Code)

	// wrong header value → 401
	req2 := httptest.NewRequest(http.MethodGet, "/api/workspaces", nil)
	req2.Header.Set("X-Observer-Web-Token", "nope")
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, req2)
	require.Equal(t, http.StatusUnauthorized, rr2.Code)

	// correct query param → 200
	req3 := httptest.NewRequest(http.MethodGet, "/api/workspaces?web_token=secret", nil)
	rr3 := httptest.NewRecorder()
	h.ServeHTTP(rr3, req3)
	require.Equal(t, http.StatusOK, rr3.Code)
}

func TestRegister_RejectsBadWorkspaceID(t *testing.T) {
	h, st := newTestHandler(t)
	seedAPIKey(t, st, "ak-1", "key1")

	bad := []struct {
		name string
		ws   string
	}{
		{"space", "ws space"},
		{"semicolon", "ws;drop"},
		{"slash", "ws/sub"},
		{"too long", strings.Repeat("a", 65)},
		{"unicode", "ws-中文"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			body := postRegister(t, h, "key1",
				`{"agent_id":"a","role":"slave","workspace_id":"`+tc.ws+`"}`,
				http.StatusBadRequest)
			require.Contains(t, body, "workspace_id must match")
		})
	}
}
