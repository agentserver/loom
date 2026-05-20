package observerstore

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/capability"
	"github.com/yourorg/multi-agent/internal/observer"
)

func testStore(t *testing.T) *Store {
	t.Helper()

	s, err := Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	require.NoError(t, s.UpsertWorkspace(Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, s.UpsertAgent(Agent{WorkspaceID: "ws1", ID: "driver", Role: observer.RoleDriver, DisplayName: "Driver"}, "driver-token"))
	require.NoError(t, s.UpsertAgent(Agent{WorkspaceID: "ws1", ID: "master", Role: observer.RoleMaster, DisplayName: "Master"}, "master-token"))
	require.NoError(t, s.UpsertAgent(Agent{WorkspaceID: "ws1", ID: "slave", Role: observer.RoleSlave, DisplayName: "Slave"}, "slave-token"))
	return s
}

func TestOpenConfiguresSQLiteForConcurrentObserverAccess(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer s.Close()

	var journalMode string
	require.NoError(t, s.db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode))
	require.Equal(t, "wal", journalMode)

	var busyTimeout int
	require.NoError(t, s.db.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout))
	require.GreaterOrEqual(t, busyTimeout, 5000)
}

func TestOpenConfiguresSQLiteBusyTimeoutOnNewConnections(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer s.Close()

	ctx := context.Background()
	firstConn, err := s.db.Conn(ctx)
	require.NoError(t, err)
	defer firstConn.Close()

	secondConn, err := s.db.Conn(ctx)
	require.NoError(t, err)
	defer secondConn.Close()

	var busyTimeout int
	require.NoError(t, secondConn.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busyTimeout))
	require.GreaterOrEqual(t, busyTimeout, 5000)
}

func mustJSON(t *testing.T, v interface{}) []byte {
	t.Helper()
	out, err := json.Marshal(v)
	require.NoError(t, err)
	return out
}

func TestValidateToken(t *testing.T) {
	s := testStore(t)
	defer s.Close()

	a, ok, err := s.ValidateToken("master-token")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "ws1", a.WorkspaceID)
	require.Equal(t, "master", a.ID)
	require.Equal(t, observer.RoleMaster, a.Role)
	require.Equal(t, "Master", a.DisplayName)

	_, ok, err = s.ValidateToken("unknown-token")
	require.NoError(t, err)
	require.False(t, ok)

	require.Error(t, s.UpsertAgent(Agent{WorkspaceID: "ws1", ID: "empty", Role: observer.RoleSlave, DisplayName: "Empty"}, ""))

	_, ok, err = s.ValidateToken("")
	require.NoError(t, err)
	require.False(t, ok)

	err = s.UpsertAgent(Agent{WorkspaceID: "ws1", ID: "dupe", Role: observer.RoleSlave, DisplayName: "Duplicate"}, "master-token")
	require.Error(t, err)
}

func TestIngestDriverMasterSlaveAndMCP(t *testing.T) {
	s := testStore(t)
	defer s.Close()

	require.NoError(t, s.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "driver", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: "mt1", Summary: "build thing",
		TargetAgentID: "master", TargetRole: observer.RoleMaster, Status: "assigned",
	}))
	require.NoError(t, s.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "master", AgentRole: observer.RoleMaster,
		Type: observer.EventMasterSubtaskDispatched, TaskID: "mt1", SubtaskID: "n1",
		ChildTaskID: "st1", SubtaskSummary: "make tool", TargetAgentID: "slave", Status: "assigned",
	}))
	require.NoError(t, s.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "slave", AgentRole: observer.RoleSlave,
		Type: observer.EventMCPServerCreated, TaskID: "st1", ParentTaskID: "mt1",
		MCPServerName: "calc", MCPTools: []string{"add", "sub"},
	}))

	tasks, err := s.ListTasks()
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, "build thing", tasks[0].Summary)
	require.Equal(t, "assigned", tasks[0].Status)
	require.Equal(t, "driver", tasks[0].DriverID)
	require.Equal(t, "master", tasks[0].MasterID)
	require.True(t, tasks[0].HasMCP)
	require.Equal(t, "created", tasks[0].MCPStatus)
	require.Len(t, tasks[0].Subtasks, 1)
	require.Equal(t, "n1", tasks[0].Subtasks[0].SubtaskID)
	require.Equal(t, "st1", tasks[0].Subtasks[0].ChildTaskID)
	require.Equal(t, "master", tasks[0].Subtasks[0].MasterID)
	require.Equal(t, "slave", tasks[0].Subtasks[0].SlaveID)
	require.Equal(t, "build thing - make tool", tasks[0].Subtasks[0].DisplayLabel)
	require.Equal(t, "created", tasks[0].Subtasks[0].MCPStatus)
}

func TestIngestMCPCreatedStoresPayloadDescriptorsInTaskView(t *testing.T) {
	s := testStore(t)
	defer s.Close()

	descriptors := []capability.MCPToolDescriptor{{
		Server:      "calc",
		Name:        "add",
		Description: "add two numbers",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"a":{"type":"number"}}}`),
	}}
	payload, err := json.Marshal(map[string]interface{}{"mcp_tool_descriptors": descriptors})
	require.NoError(t, err)

	require.NoError(t, s.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "driver", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: "mt1", Summary: "build thing",
		TargetAgentID: "master", TargetRole: observer.RoleMaster, Status: "assigned",
	}))
	require.NoError(t, s.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "master", AgentRole: observer.RoleMaster,
		Type: observer.EventMasterSubtaskDispatched, TaskID: "mt1", SubtaskID: "n1",
		ChildTaskID: "st1", SubtaskSummary: "make tool", TargetAgentID: "slave", Status: "assigned",
	}))
	require.NoError(t, s.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "slave", AgentRole: observer.RoleSlave,
		Type: observer.EventMCPServerCreated, TaskID: "st1", ParentTaskID: "mt1",
		MCPServerName: "calc", MCPTools: []string{"add"}, Payload: payload,
	}))

	tasks, err := s.ListTasks()
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Len(t, tasks[0].MCPServers, 1)
	require.Equal(t, "calc", tasks[0].MCPServers[0].Name)
	require.JSONEq(t, string(mustJSON(t, descriptors)), string(tasks[0].MCPServers[0].ToolDescriptors))
}

func TestIngestMCPCreatedRejectsWrongShapePayloadDescriptors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload json.RawMessage
	}{
		{name: "string", payload: json.RawMessage(`{"mcp_tool_descriptors":"bad"}`)},
		{name: "object", payload: json.RawMessage(`{"mcp_tool_descriptors":{}}`)},
		{name: "number", payload: json.RawMessage(`{"mcp_tool_descriptors":123}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testStore(t)
			defer s.Close()

			err := s.Ingest(observer.Event{
				WorkspaceID: "ws1", AgentID: "slave", AgentRole: observer.RoleSlave,
				Type: observer.EventMCPServerCreated, TaskID: "st1",
				MCPServerName: "calc", MCPTools: []string{"add"}, Payload: tc.payload,
			})
			require.Error(t, err)
			require.ErrorContains(t, err, "mcp_tool_descriptors")
		})
	}
}

func TestIngestValidationFailurePersistsPayload(t *testing.T) {
	s := testStore(t)
	defer s.Close()

	payload := json.RawMessage(`{"validation_error":"unknown argument put_url_128","required":true,"prompt":"{}"}`)
	require.NoError(t, s.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "master", AgentRole: observer.RoleMaster,
		Type: observer.EventMasterMCPCallValidationFailed, TaskID: "mt1", SubtaskID: "n1",
		TargetAgentID: "slave", TargetRole: observer.RoleSlave, Status: "failed", Payload: payload,
	}))

	count, err := s.EventCount()
	require.NoError(t, err)
	require.Equal(t, 1, count)

	var storedType, storedPayload string
	require.NoError(t, s.db.QueryRow(`SELECT type, payload FROM events WHERE task_id=? AND subtask_id=?`, "mt1", "n1").Scan(&storedType, &storedPayload))
	require.Equal(t, observer.EventMasterMCPCallValidationFailed, storedType)
	require.JSONEq(t, string(payload), storedPayload)

	tasks, err := s.ListTasks()
	require.NoError(t, err)
	require.Len(t, tasks, 0)

	events, err := s.ListEvents("mt1")
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, observer.EventMasterMCPCallValidationFailed, events[0].Type)
	require.JSONEq(t, string(payload), string(events[0].Payload))
}

func TestIngestIsIdempotentByEventID(t *testing.T) {
	s := testStore(t)
	defer s.Close()

	ev := observer.Event{
		EventID: "e1", WorkspaceID: "ws1", AgentID: "driver", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: "mt1", Summary: "build thing",
	}
	require.NoError(t, s.Ingest(ev))
	require.NoError(t, s.Ingest(ev))

	count, err := s.EventCount()
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

func TestGeneratedEventIDsDoNotCollideForRepeatedEvents(t *testing.T) {
	s := testStore(t)
	defer s.Close()

	ev := observer.Event{
		WorkspaceID: "ws1", AgentID: "driver", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: "mt1", Summary: "build thing",
	}
	require.NoError(t, s.Ingest(ev))
	require.NoError(t, s.Ingest(ev))

	count, err := s.EventCount()
	require.NoError(t, err)
	require.Equal(t, 2, count)
}

func TestGeneratedEventIDsDoNotDeduplicateSameContentAndTimestamp(t *testing.T) {
	s := testStore(t)
	defer s.Close()

	ev := observer.Event{
		TS: "2026-05-11T00:00:00Z", WorkspaceID: "ws1", AgentID: "driver", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: "mt1", Summary: "build thing",
	}
	require.NoError(t, s.Ingest(ev))
	require.NoError(t, s.Ingest(ev))

	count, err := s.EventCount()
	require.NoError(t, err)
	require.Equal(t, 2, count)
}

func TestGeneratedEventIDsDoNotCollideWhenFieldsContainNUL(t *testing.T) {
	s := testStore(t)
	defer s.Close()

	ev1 := observer.Event{
		TS: "2026-05-11T00:00:00Z", WorkspaceID: "ws1", AgentID: "driver\x00x", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskStatus, TaskID: "mt1", Status: "running",
	}
	ev2 := observer.Event{
		TS: "2026-05-11T00:00:00Z", WorkspaceID: "ws1\x00driver", AgentID: "x", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskStatus, TaskID: "mt1", Status: "running",
	}
	require.NoError(t, s.Ingest(ev1))
	require.NoError(t, s.Ingest(ev2))

	count, err := s.EventCount()
	require.NoError(t, err)
	require.Equal(t, 2, count)
}

func TestIngestAggregateStatusAndFallbacks(t *testing.T) {
	s := testStore(t)
	defer s.Close()

	require.NoError(t, s.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "master", AgentRole: observer.RoleMaster,
		Type: observer.EventMasterTaskReceived, TaskID: "mt1",
	}))
	require.NoError(t, s.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "master", AgentRole: observer.RoleMaster,
		Type: observer.EventMasterSubtaskDispatched, TaskID: "mt1",
		ChildTaskID: "st1", SubtaskSummary: "make tool", TargetAgentID: "slave",
	}))
	require.NoError(t, s.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "master", AgentRole: observer.RoleMaster,
		Type: observer.EventMasterTaskCompleted, TaskID: "mt1", Status: "completed",
	}))

	tasks, err := s.ListTasks()
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, "mt1", tasks[0].Summary)
	require.Equal(t, "completed", tasks[0].Status)
	require.Equal(t, "master", tasks[0].MasterID)
	require.Len(t, tasks[0].Subtasks, 1)
	require.Equal(t, "st1", tasks[0].Subtasks[0].SubtaskID)
	require.Equal(t, "assigned", tasks[0].Subtasks[0].Status)
	require.Equal(t, "mt1 - make tool", tasks[0].Subtasks[0].DisplayLabel)
}

func TestProgressEventsUpdateLatestProgressWithoutTerminalState(t *testing.T) {
	s := testStore(t)
	defer s.Close()

	require.NoError(t, s.Ingest(observer.Event{
		TS: "2026-05-11T00:00:00Z", WorkspaceID: "ws1", AgentID: "driver", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: "mt1", Summary: "build thing",
		TargetAgentID: "master", TargetRole: observer.RoleMaster, Status: "assigned",
	}))
	require.NoError(t, s.Ingest(observer.Event{
		TS: "2026-05-11T00:00:01Z", WorkspaceID: "ws1", AgentID: "master", AgentRole: observer.RoleMaster,
		Type: observer.EventMasterPlanningCompleted, TaskID: "mt1", Status: "completed",
		Summary: "plan ready", Payload: json.RawMessage(`{"phase":"planning","message":"planned 2 subtasks"}`),
	}))
	require.NoError(t, s.Ingest(observer.Event{
		TS: "2026-05-11T00:00:02Z", WorkspaceID: "ws1", AgentID: "master", AgentRole: observer.RoleMaster,
		Type: observer.EventMasterSubtaskDispatched, TaskID: "mt1", SubtaskID: "n1",
		ChildTaskID: "st1", SubtaskSummary: "make tool", TargetAgentID: "slave",
	}))
	require.NoError(t, s.Ingest(observer.Event{
		TS: "2026-05-11T00:00:03Z", WorkspaceID: "ws1", AgentID: "slave", AgentRole: observer.RoleSlave,
		Type: observer.EventSlaveTaskProgress, TaskID: "st1", Summary: "half done",
	}))

	tasks, err := s.ListTasks()
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, "assigned", tasks[0].Status)
	require.False(t, tasks[0].IsFinal)
	require.Empty(t, tasks[0].FinalOutput)
	require.Equal(t, "planned 2 subtasks", tasks[0].LatestProgress)
	require.Equal(t, "planning", tasks[0].LatestProgressPhase)
	require.Equal(t, "2026-05-11T00:00:01Z", tasks[0].LatestProgressAt)
	require.Len(t, tasks[0].Subtasks, 1)
	require.Equal(t, "half done", tasks[0].Subtasks[0].LatestProgress)
	require.Equal(t, observer.EventSlaveTaskProgress, tasks[0].Subtasks[0].LatestProgressPhase)
	require.Equal(t, "2026-05-11T00:00:03Z", tasks[0].Subtasks[0].LatestProgressAt)
}

func TestProgressEventWithMissingExplicitSubtaskDoesNotUpdateParent(t *testing.T) {
	s := testStore(t)
	defer s.Close()

	require.NoError(t, s.Ingest(observer.Event{
		TS: "2026-05-11T00:00:00Z", WorkspaceID: "ws1", AgentID: "driver", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: "mt1", Summary: "build thing",
		TargetAgentID: "master", TargetRole: observer.RoleMaster, Status: "assigned",
	}))
	require.NoError(t, s.Ingest(observer.Event{
		TS: "2026-05-11T00:00:01Z", WorkspaceID: "ws1", AgentID: "master", AgentRole: observer.RoleMaster,
		Type: observer.EventMasterPlanningProgress, TaskID: "mt1",
		Payload: json.RawMessage(`{"phase":"planning","message":"parent progress"}`),
	}))
	require.NoError(t, s.Ingest(observer.Event{
		TS: "2026-05-11T00:00:02Z", WorkspaceID: "ws1", AgentID: "slave", AgentRole: observer.RoleSlave,
		Type: observer.EventSlaveTaskProgress, TaskID: "st1", ParentTaskID: "mt1", SubtaskID: "n1",
		Payload: json.RawMessage(`{"phase":"build","message":"early subtask progress"}`),
	}))

	tasks, err := s.ListTasks()
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, "parent progress", tasks[0].LatestProgress)
	require.Equal(t, "planning", tasks[0].LatestProgressPhase)
	require.Equal(t, "2026-05-11T00:00:01Z", tasks[0].LatestProgressAt)
	require.Empty(t, tasks[0].Subtasks)
}

func TestProgressEventWithUnknownSubtaskDoesNotUpdateParent(t *testing.T) {
	s := testStore(t)
	defer s.Close()

	require.NoError(t, s.Ingest(observer.Event{
		TS: "2026-05-11T00:00:00Z", WorkspaceID: "ws1", AgentID: "driver", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: "mt1", Summary: "build thing",
		TargetAgentID: "master", TargetRole: observer.RoleMaster, Status: "assigned",
	}))
	require.NoError(t, s.Ingest(observer.Event{
		TS: "2026-05-11T00:00:01Z", WorkspaceID: "ws1", AgentID: "master", AgentRole: observer.RoleMaster,
		Type: observer.EventMasterPlanningProgress, TaskID: "mt1",
		Payload: json.RawMessage(`{"phase":"planning","message":"parent progress"}`),
	}))
	require.NoError(t, s.Ingest(observer.Event{
		TS: "2026-05-11T00:00:02Z", WorkspaceID: "ws1", AgentID: "master", AgentRole: observer.RoleMaster,
		Type: observer.EventMasterSubtaskDispatched, TaskID: "mt1", SubtaskID: "n1",
		ChildTaskID: "st1", SubtaskSummary: "make tool", TargetAgentID: "slave",
	}))
	require.NoError(t, s.Ingest(observer.Event{
		TS: "2026-05-11T00:00:03Z", WorkspaceID: "ws1", AgentID: "slave", AgentRole: observer.RoleSlave,
		Type: observer.EventSlaveTaskProgress, TaskID: "st2", ParentTaskID: "mt1", SubtaskID: "unknown",
		Payload: json.RawMessage(`{"phase":"build","message":"unknown subtask progress"}`),
	}))

	tasks, err := s.ListTasks()
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, "parent progress", tasks[0].LatestProgress)
	require.Equal(t, "planning", tasks[0].LatestProgressPhase)
	require.Equal(t, "2026-05-11T00:00:01Z", tasks[0].LatestProgressAt)
	require.Len(t, tasks[0].Subtasks, 1)
	require.Empty(t, tasks[0].Subtasks[0].LatestProgress)
	require.Empty(t, tasks[0].Subtasks[0].LatestProgressPhase)
	require.Empty(t, tasks[0].Subtasks[0].LatestProgressAt)
}

func TestTerminalEventSetsIsFinalAndFinalOutput(t *testing.T) {
	s := testStore(t)
	defer s.Close()

	require.NoError(t, s.Ingest(observer.Event{
		TS: "2026-05-11T00:00:00Z", WorkspaceID: "ws1", AgentID: "driver", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: "mt1", Summary: "build thing",
		TargetAgentID: "master", TargetRole: observer.RoleMaster, Status: "assigned",
	}))
	require.NoError(t, s.Ingest(observer.Event{
		TS: "2026-05-11T00:00:01Z", WorkspaceID: "ws1", AgentID: "master", AgentRole: observer.RoleMaster,
		Type: observer.EventMasterTaskCompleted, TaskID: "mt1", Status: "completed",
		Payload: json.RawMessage(`{"output":"final answer"}`),
	}))

	tasks, err := s.ListTasks()
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, "completed", tasks[0].Status)
	require.True(t, tasks[0].IsFinal)
	require.Equal(t, "final answer", tasks[0].FinalOutput)
	require.Empty(t, tasks[0].Output)
	require.Empty(t, tasks[0].Error)
}

func TestSubtaskDonePreservesSparseMetadata(t *testing.T) {
	s := testStore(t)
	defer s.Close()

	require.NoError(t, s.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "driver", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: "mt1", Summary: "build thing",
		TargetAgentID: "master",
	}))
	require.NoError(t, s.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "master", AgentRole: observer.RoleMaster,
		Type: observer.EventMasterSubtaskDispatched, TaskID: "mt1", SubtaskID: "n1",
		ChildTaskID: "st1", SubtaskSummary: "make tool", TargetAgentID: "slave",
	}))
	require.NoError(t, s.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "master", AgentRole: observer.RoleMaster,
		Type: observer.EventMasterSubtaskDone, ParentTaskID: "mt1", SubtaskID: "n1",
		Status: "completed", Payload: json.RawMessage(`{"output":"built","error":"warning"}`),
	}))

	tasks, err := s.ListTasks()
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Len(t, tasks[0].Subtasks, 1)
	require.Equal(t, "st1", tasks[0].Subtasks[0].ChildTaskID)
	require.Equal(t, "slave", tasks[0].Subtasks[0].SlaveID)
	require.Equal(t, "make tool", tasks[0].Subtasks[0].Summary)
	require.Equal(t, "build thing - make tool", tasks[0].Subtasks[0].DisplayLabel)
	require.Equal(t, "completed", tasks[0].Subtasks[0].Status)
	require.Equal(t, "built", tasks[0].Subtasks[0].Output)
	require.Equal(t, "warning", tasks[0].Subtasks[0].Error)
}

func TestMCPBeforeTaskStillMarksCreatedTask(t *testing.T) {
	s := testStore(t)
	defer s.Close()

	require.NoError(t, s.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "slave", AgentRole: observer.RoleSlave,
		Type: observer.EventMCPServerCreated, TaskID: "st1", ParentTaskID: "mt1",
		MCPServerName: "calc", MCPTools: []string{"add"},
	}))
	require.NoError(t, s.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "driver", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: "mt1", Summary: "build thing",
		TargetAgentID: "master",
	}))

	tasks, err := s.ListTasks()
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.True(t, tasks[0].HasMCP)
	require.Equal(t, "created", tasks[0].MCPStatus)
}

func TestMCPCreatedLinksToParentByChildTaskID(t *testing.T) {
	s := testStore(t)
	defer s.Close()

	require.NoError(t, s.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "driver", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: "mt1", Summary: "build thing",
		TargetAgentID: "master",
	}))
	require.NoError(t, s.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "master", AgentRole: observer.RoleMaster,
		Type: observer.EventMasterSubtaskDispatched, TaskID: "mt1", SubtaskID: "n1",
		ChildTaskID: "st1", SubtaskSummary: "make tool", TargetAgentID: "slave",
	}))
	require.NoError(t, s.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "slave", AgentRole: observer.RoleSlave,
		Type: observer.EventMCPServerCreated, TaskID: "st1",
		MCPServerName: "calc", MCPTools: []string{"add"},
	}))

	tasks, err := s.ListTasks()
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.True(t, tasks[0].HasMCP)
	require.Equal(t, "created", tasks[0].MCPStatus)
	require.Equal(t, "created", tasks[0].Subtasks[0].MCPStatus)
}

func TestMCPCreatedBeforeDispatchReconcilesWhenChildTaskIDArrives(t *testing.T) {
	s := testStore(t)
	defer s.Close()

	require.NoError(t, s.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "driver", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: "mt1", Summary: "build thing",
		TargetAgentID: "master",
	}))
	require.NoError(t, s.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "slave", AgentRole: observer.RoleSlave,
		Type: observer.EventMCPServerCreated, TaskID: "st1",
		MCPServerName: "calc", MCPTools: []string{"add"},
	}))
	require.NoError(t, s.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "master", AgentRole: observer.RoleMaster,
		Type: observer.EventMasterSubtaskDispatched, TaskID: "mt1", SubtaskID: "n1",
		ChildTaskID: "st1", SubtaskSummary: "make tool", TargetAgentID: "slave",
	}))

	tasks, err := s.ListTasks()
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.True(t, tasks[0].HasMCP)
	require.Equal(t, "created", tasks[0].MCPStatus)
	require.Len(t, tasks[0].MCPServers, 1)
	require.Equal(t, "mt1", tasks[0].MCPServers[0].ParentTaskID)
	require.Equal(t, "created", tasks[0].Subtasks[0].MCPStatus)
}

func TestMCPBlockedLinksToParentByChildTaskID(t *testing.T) {
	s := testStore(t)
	defer s.Close()

	require.NoError(t, s.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "driver", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: "mt1", Summary: "build thing",
		TargetAgentID: "master",
	}))
	require.NoError(t, s.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "master", AgentRole: observer.RoleMaster,
		Type: observer.EventMasterSubtaskDispatched, TaskID: "mt1", SubtaskID: "n1",
		ChildTaskID: "st1", SubtaskSummary: "make tool", TargetAgentID: "slave",
	}))
	require.NoError(t, s.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "slave", AgentRole: observer.RoleSlave,
		Type: observer.EventMCPServerBlocked, TaskID: "st1", MCPServerName: "calc",
		Status: "blocked", Payload: json.RawMessage(`{"stage":"validate_imports","reason":"missing dep"}`),
	}))

	tasks, err := s.ListTasks()
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.False(t, tasks[0].HasMCP)
	require.Equal(t, "blocked", tasks[0].MCPStatus)
	require.Equal(t, "blocked", tasks[0].Subtasks[0].MCPStatus)
}

func TestSlaveLifecycleUpdatesLinkedSubtask(t *testing.T) {
	s := testStore(t)
	defer s.Close()

	require.NoError(t, s.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "driver", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: "mt1", Summary: "build thing",
		TargetAgentID: "master",
	}))
	require.NoError(t, s.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "master", AgentRole: observer.RoleMaster,
		Type: observer.EventMasterSubtaskDispatched, TaskID: "mt1", SubtaskID: "n1",
		ChildTaskID: "st1", SubtaskSummary: "make tool", TargetAgentID: "slave",
	}))
	require.NoError(t, s.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "slave", AgentRole: observer.RoleSlave,
		Type: observer.EventSlaveTaskStarted, TaskID: "st1", Status: "running",
	}))
	require.NoError(t, s.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "slave", AgentRole: observer.RoleSlave,
		Type: observer.EventSlaveTaskCompleted, TaskID: "st1", Status: "completed",
		Payload: json.RawMessage(`{"output":"done"}`),
	}))

	tasks, err := s.ListTasks()
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, "completed", tasks[0].Subtasks[0].Status)
	require.Equal(t, "done", tasks[0].Subtasks[0].Output)
	require.Equal(t, "slave", tasks[0].Subtasks[0].SlaveID)
}

func TestSlaveLifecycleUpdatesDirectDriverTask(t *testing.T) {
	s := testStore(t)
	defer s.Close()

	require.NoError(t, s.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "driver", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: "task-direct", Summary: "run benchmark",
		TargetAgentID: "agentserver-slave-id", Status: "assigned",
	}))
	require.NoError(t, s.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "slave", AgentRole: observer.RoleSlave,
		Type: observer.EventSlaveTaskStarted, TaskID: "task-direct", Status: "running",
	}))
	require.NoError(t, s.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "slave", AgentRole: observer.RoleSlave,
		Type: observer.EventSlaveTaskCompleted, TaskID: "task-direct", Status: "completed",
		Payload: json.RawMessage(`{"output":"done"}`),
	}))

	tasks, err := s.ListTasks()
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, "slave", tasks[0].SlaveID)
	require.Equal(t, "completed", tasks[0].Status)
	require.Equal(t, "done", tasks[0].Output)
}

func TestDriverSubmittedToSlaveDoesNotSetMasterID(t *testing.T) {
	s := testStore(t)
	defer s.Close()

	require.NoError(t, s.Ingest(observer.Event{
		WorkspaceID: "ws1", AgentID: "driver", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: "task-direct", Summary: "run benchmark",
		TargetAgentID: "slave", TargetRole: observer.RoleSlave, Status: "assigned",
	}))

	tasks, err := s.ListTasks()
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Empty(t, tasks[0].MasterID)
	require.Equal(t, "slave", tasks[0].SlaveID)
}

func TestSchemaRequiresUniqueTokenHashes(t *testing.T) {
	s := testStore(t)
	defer s.Close()

	rows, err := s.db.Query(`PRAGMA index_list(agents)`)
	require.NoError(t, err)
	defer rows.Close()

	foundUnique := false
	for rows.Next() {
		var seq int
		var name string
		var unique bool
		var origin string
		var partial bool
		require.NoError(t, rows.Scan(&seq, &name, &unique, &origin, &partial))
		if name == "idx_agents_token_hash" && unique {
			foundUnique = true
		}
	}
	require.NoError(t, rows.Err())
	require.True(t, foundUnique)
}

func TestArtifactLazyLifecycle(t *testing.T) {
	s := testStore(t)
	defer s.Close()

	art, err := s.CreateArtifact(ArtifactCreate{
		WorkspaceID: "ws1", OwnerAgentID: "driver", Path: "/tmp/input.txt",
		Kind: "file", MIME: "text/plain", State: ArtifactStateRegistered,
	})
	require.NoError(t, err)
	require.NotEmpty(t, art.ID)
	require.Equal(t, ArtifactStateRegistered, art.State)

	req, err := s.RequestArtifact("ws1", "slave", art.ID)
	require.NoError(t, err)
	require.Equal(t, ArtifactStatePending, req.State)
	require.NotEmpty(t, req.RequestID)

	pending, err := s.ListArtifactRequests("ws1", "driver")
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, art.ID, pending[0].ArtifactID)
	require.Equal(t, "/tmp/input.txt", pending[0].Path)

	err = s.StoreArtifactContent("ws1", "driver", art.ID, "text/plain", bytes.NewBufferString("hello"))
	require.NoError(t, err)

	got, err := s.OpenArtifactContent("ws1", art.ID)
	require.NoError(t, err)
	body, err := io.ReadAll(got.Body)
	require.NoError(t, err)
	require.NoError(t, got.Body.Close())
	require.Equal(t, "hello", string(body))
	require.Equal(t, int64(5), got.Bytes)
	require.Equal(t, "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824", got.SHA256)
}

func TestWriteLifecycle(t *testing.T) {
	s := testStore(t)
	defer s.Close()

	wr, err := s.CreateWrite(WriteCreate{
		WorkspaceID: "ws1", OwnerAgentID: "driver", TaskID: "task-1",
		Path: "/tmp/out.txt", Overwrite: true,
	})
	require.NoError(t, err)
	require.NotEmpty(t, wr.ID)

	err = s.StoreWriteContent("ws1", "slave", wr.ID, "text/plain", bytes.NewBufferString("done"))
	require.NoError(t, err)

	writes, err := s.ListCompletedWrites("ws1", "driver", "task-1")
	require.NoError(t, err)
	require.Len(t, writes, 1)
	require.Equal(t, "/tmp/out.txt", writes[0].Path)
	require.Equal(t, int64(4), writes[0].Bytes)
	require.Equal(t, "a4c3ed04a95a3da14a9d235c83d868bed7c0f45cf7f3faa751ee8f50598d2211", writes[0].SHA256)
	require.Equal(t, "done", string(writes[0].Content))
}

func TestTaskContractPersistence(t *testing.T) {
	s := testStore(t)

	body := json.RawMessage(`{"version":1,"conversation_id":"conv-1","intent":{"goal":"g","success_criteria":["s"]}}`)
	require.NoError(t, s.SaveTaskContract(TaskContractRecord{
		WorkspaceID:    "ws1",
		TaskID:         "task-1",
		ConversationID: "conv-1",
		OwnerAgentID:   "driver",
		Body:           body,
	}))

	got, err := s.GetTaskContract("ws1", "task-1")
	require.NoError(t, err)
	require.Equal(t, "conv-1", got.ConversationID)
	require.JSONEq(t, string(body), string(got.Body))
}

func TestTaskContractRejectsOverwriteByDifferentOwner(t *testing.T) {
	s := testStore(t)

	original := json.RawMessage(`{"version":1,"conversation_id":"conv-1","intent":{"goal":"original","success_criteria":["s"]}}`)
	require.NoError(t, s.SaveTaskContract(TaskContractRecord{
		WorkspaceID:    "ws1",
		TaskID:         "task-1",
		ConversationID: "conv-1",
		OwnerAgentID:   "driver",
		Body:           original,
	}))

	err := s.SaveTaskContract(TaskContractRecord{
		WorkspaceID:    "ws1",
		TaskID:         "task-1",
		ConversationID: "conv-2",
		OwnerAgentID:   "other-driver",
		Body:           json.RawMessage(`{"version":1,"conversation_id":"conv-2","intent":{"goal":"overwrite","success_criteria":["s"]}}`),
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "task contract owner mismatch")

	got, err := s.GetTaskContract("ws1", "task-1")
	require.NoError(t, err)
	require.Equal(t, "driver", got.OwnerAgentID)
	require.Equal(t, "conv-1", got.ConversationID)
	require.JSONEq(t, string(original), string(got.Body))
}

func TestResourceSnapshotPersistence(t *testing.T) {
	s := testStore(t)
	firstBody := json.RawMessage(`{"generated_at":"first","agents":[{"agent_id":"a","display_name":"slave"}]}`)
	secondBody := json.RawMessage(`{"generated_at":"second","agents":[{"agent_id":"b","display_name":"driver"}]}`)

	require.NoError(t, s.SaveResourceSnapshot(ResourceSnapshotRecord{
		WorkspaceID:  "ws1",
		SnapshotID:   "snap-1",
		OwnerAgentID: "driver",
		Body:         firstBody,
	}))
	time.Sleep(time.Millisecond)
	require.NoError(t, s.SaveResourceSnapshot(ResourceSnapshotRecord{
		WorkspaceID:  "ws1",
		SnapshotID:   "snap-2",
		OwnerAgentID: "driver",
		Body:         secondBody,
	}))

	got, err := s.GetLatestResourceSnapshot("ws1")
	require.NoError(t, err)
	require.Equal(t, "snap-2", got.SnapshotID)
	require.JSONEq(t, string(secondBody), string(got.Body))
}

func TestUpsertAPIKeyRoundTrip(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer s.Close()
	require.NoError(t, s.UpsertWorkspace(Workspace{ID: "ws1", Name: "Workspace"}))

	require.NoError(t, s.UpsertAPIKey("ws1", "ak-default", "ak_secret_abc"))

	wsID, keyID, ok, err := s.LookupAPIKey("ak_secret_abc")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "ws1", wsID)
	require.Equal(t, "ak-default", keyID)
}

func TestUpsertAPIKeyReplacesKey(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer s.Close()
	require.NoError(t, s.UpsertWorkspace(Workspace{ID: "ws1", Name: "Workspace"}))

	require.NoError(t, s.UpsertAPIKey("ws1", "ak-default", "first-key"))
	require.NoError(t, s.UpsertAPIKey("ws1", "ak-default", "second-key"))

	_, _, ok, err := s.LookupAPIKey("first-key")
	require.NoError(t, err)
	require.False(t, ok, "old key value should no longer resolve")

	wsID, _, ok, err := s.LookupAPIKey("second-key")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "ws1", wsID)
}

func TestUpsertAPIKeyRejectsEmptyKey(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer s.Close()
	require.NoError(t, s.UpsertWorkspace(Workspace{ID: "ws1", Name: "Workspace"}))

	require.Error(t, s.UpsertAPIKey("ws1", "ak-default", ""))
}

func TestLookupAPIKeyMiss(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer s.Close()

	_, _, ok, err := s.LookupAPIKey("unknown")
	require.NoError(t, err)
	require.False(t, ok)

	_, _, ok, err = s.LookupAPIKey("")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestReplaceAPIKeysForWorkspaceDeletesMissing(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer s.Close()
	require.NoError(t, s.UpsertWorkspace(Workspace{ID: "ws1", Name: "Workspace"}))

	require.NoError(t, s.ReplaceAPIKeysForWorkspace("ws1", []APIKeySpec{
		{ID: "ak-a", Key: "key-a"},
		{ID: "ak-b", Key: "key-b"},
	}))

	_, _, ok, err := s.LookupAPIKey("key-a")
	require.NoError(t, err)
	require.True(t, ok)

	// Reconcile with a shorter list — "ak-a" disappears.
	require.NoError(t, s.ReplaceAPIKeysForWorkspace("ws1", []APIKeySpec{
		{ID: "ak-b", Key: "key-b"},
	}))

	_, _, ok, err = s.LookupAPIKey("key-a")
	require.NoError(t, err)
	require.False(t, ok, "ak-a should be deleted by reconcile")

	_, _, ok, err = s.LookupAPIKey("key-b")
	require.NoError(t, err)
	require.True(t, ok)
}

func TestReplaceAPIKeysForWorkspaceScopedByWorkspace(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer s.Close()
	require.NoError(t, s.UpsertWorkspace(Workspace{ID: "ws1", Name: "Workspace 1"}))
	require.NoError(t, s.UpsertWorkspace(Workspace{ID: "ws2", Name: "Workspace 2"}))

	require.NoError(t, s.ReplaceAPIKeysForWorkspace("ws1", []APIKeySpec{
		{ID: "ak-a", Key: "key-1a"},
	}))
	require.NoError(t, s.ReplaceAPIKeysForWorkspace("ws2", []APIKeySpec{
		{ID: "ak-a", Key: "key-2a"},
	}))

	// Reconcile ws1 with an empty list — ws2's row must survive.
	require.NoError(t, s.ReplaceAPIKeysForWorkspace("ws1", nil))

	_, _, ok, err := s.LookupAPIKey("key-1a")
	require.NoError(t, err)
	require.False(t, ok)

	_, _, ok, err = s.LookupAPIKey("key-2a")
	require.NoError(t, err)
	require.True(t, ok, "reconciling ws1 should not touch ws2 rows")
}
