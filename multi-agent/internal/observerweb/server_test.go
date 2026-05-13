package observerweb

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
