package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpen_CreatesSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	s, err := Open(path)
	require.NoError(t, err)
	defer s.Close()

	var n int
	require.NoError(t, s.DB().QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('tasks','task_chunks','pending_acks')`,
	).Scan(&n))
	require.Equal(t, 3, n)
}

func TestInsert_GetByID(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "x.db"))
	defer s.Close()

	require.NoError(t, s.Insert(Task{ID: "t1", Skill: "chat", Prompt: "hi"}))
	row, _, err := s.GetTaskWithChunks("t1")
	require.NoError(t, err)
	require.Equal(t, "assigned", row.Status)
	require.Equal(t, "chat", row.Skill)
}

func TestComplete_FromRunning(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "x.db"))
	defer s.Close()

	require.NoError(t, s.Insert(Task{ID: "t1"}))
	require.NoError(t, s.MarkRunning("t1"))
	require.NoError(t, s.Complete("t1", "done"))
	row, _, _ := s.GetTaskWithChunks("t1")
	require.Equal(t, "completed", row.Status)
	require.Equal(t, "done", row.Output)
	require.NotEmpty(t, row.FinishedAt)
}

func TestFail_RecordsReason(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "x.db"))
	defer s.Close()

	require.NoError(t, s.Insert(Task{ID: "t1"}))
	require.NoError(t, s.Fail("t1", "boom"))
	row, _, _ := s.GetTaskWithChunks("t1")
	require.Equal(t, "failed", row.Status)
	require.Equal(t, "boom", row.Error)
}

func TestChunkSink_PersistsAndPublishes(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "x.db"))
	defer s.Close()
	require.NoError(t, s.Insert(Task{ID: "t1"}))

	ch, cancel := s.Subscribe("t1")
	defer cancel()

	sink := s.ChunkSink("t1")
	sink.Write("chunk", "hello")
	sink.Write("capability", "ok")
	sink.Close()

	received := drain(ch, 3, time.Second)
	require.Equal(t, []EventType{EventChunk, EventCapability, EventDone}, typesOf(received))

	_, chunks, _ := s.GetTaskWithChunks("t1")
	require.Len(t, chunks, 2) // done event is not persisted
}

func drain(ch <-chan Event, n int, timeout time.Duration) []Event {
	var got []Event
	deadline := time.After(timeout)
	for len(got) < n {
		select {
		case e := <-ch:
			got = append(got, e)
		case <-deadline:
			return got
		}
	}
	return got
}

func typesOf(es []Event) []EventType {
	out := make([]EventType, len(es))
	for i, e := range es {
		out[i] = e.Type
	}
	return out
}

func TestRecover_MarksInflightAsFailedAndQueues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	s, _ := Open(path)
	require.NoError(t, s.Insert(Task{ID: "t-running"}))
	require.NoError(t, s.MarkRunning("t-running"))
	require.NoError(t, s.Insert(Task{ID: "t-assigned"}))
	s.Close()

	s2, _ := Open(path)
	defer s2.Close()
	require.NoError(t, s2.Recover())

	pa, err := s2.PopPendingAcks()
	require.NoError(t, err)
	require.Len(t, pa, 2)
	for _, p := range pa {
		require.Equal(t, "failed", p.Status)
	}
}

func TestSubTasks_InsertAndList(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "x.db"))
	defer s.Close()
	require.NoError(t, s.Insert(Task{ID: "p1"}))

	rows := []SubTaskRow{
		{ParentID: "p1", NodeID: "n1", TargetID: "agent-a", Prompt: "do a", DependsOn: nil, Status: "pending"},
		{ParentID: "p1", NodeID: "n2", TargetID: "agent-b", Prompt: "do b", DependsOn: []string{"n1"}, Status: "pending"},
	}
	require.NoError(t, s.InsertSubTasks("p1", rows))

	got, err := s.ListSubTasks("p1")
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "n1", got[0].NodeID)
	require.Equal(t, []string{"n1"}, got[1].DependsOn)
	require.Equal(t, "pending", got[0].Status)
}

func TestSubTasks_Update(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "x.db"))
	defer s.Close()
	require.NoError(t, s.Insert(Task{ID: "p1"}))
	require.NoError(t, s.InsertSubTasks("p1", []SubTaskRow{
		{ParentID: "p1", NodeID: "n1", TargetID: "a", Prompt: "p", Status: "pending"},
	}))

	require.NoError(t, s.UpdateSubTask("p1", "n1", map[string]interface{}{
		"child_task_id": "ct-1",
		"status":        "assigned",
	}))
	require.NoError(t, s.UpdateSubTask("p1", "n1", map[string]interface{}{
		"status":      "completed",
		"output":      "done",
		"finished_at": "2026-04-28T10:00:00Z",
	}))

	rows, _ := s.ListSubTasks("p1")
	require.Equal(t, "ct-1", rows[0].ChildTaskID)
	require.Equal(t, "completed", rows[0].Status)
	require.Equal(t, "done", rows[0].Output)
}

func TestRecover_CancelsInflightSubTasks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	s, _ := Open(path)
	require.NoError(t, s.Insert(Task{ID: "p"}))
	require.NoError(t, s.MarkRunning("p"))
	require.NoError(t, s.InsertSubTasks("p", []SubTaskRow{
		{ParentID: "p", NodeID: "n1", TargetID: "x", Prompt: "x", Status: "pending"},
		{ParentID: "p", NodeID: "n2", TargetID: "x", Prompt: "x", Status: "assigned"},
		{ParentID: "p", NodeID: "n3", TargetID: "x", Prompt: "x", Status: "completed", Output: "ok"},
	}))
	s.Close()

	s2, _ := Open(path)
	defer s2.Close()
	require.NoError(t, s2.Recover())

	rows, _ := s2.ListSubTasks("p")
	statuses := map[string]string{}
	for _, r := range rows {
		statuses[r.NodeID] = r.Status
	}
	require.Equal(t, "cancelled", statuses["n1"])
	require.Equal(t, "cancelled", statuses["n2"])
	require.Equal(t, "completed", statuses["n3"])
}

func TestStore_InsertIfAbsent_FirstInsertReturnsTrue(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "x.db"))
	inserted, err := s.InsertIfAbsent(Task{ID: "t-1", Skill: "chat", Prompt: "hi"})
	if err != nil {
		t.Fatalf("InsertIfAbsent: %v", err)
	}
	if !inserted {
		t.Fatalf("expected inserted=true for fresh row")
	}
}

func TestStore_InsertIfAbsent_DuplicateReturnsFalse(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "x.db"))
	if _, err := s.InsertIfAbsent(Task{ID: "t-1", Skill: "chat", Prompt: "hi"}); err != nil {
		t.Fatalf("first: %v", err)
	}
	inserted, err := s.InsertIfAbsent(Task{ID: "t-1", Skill: "chat", Prompt: "hi-different"})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if inserted {
		t.Fatalf("expected inserted=false for duplicate id")
	}
	row, _, err := s.GetTaskWithChunks("t-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.Prompt != "hi" {
		t.Fatalf("prompt overwritten on duplicate: %q", row.Prompt)
	}
}

func TestPopPendingAcksReturnsSkill(t *testing.T) {
	// PendingAck must carry the original task's skill so the poller's
	// drain path can decide whether to apply chat-envelope unwrap logic
	// or leave non-chat outputs (which may coincidentally look like an
	// envelope) untouched. #24 P2 review 6.
	s, _ := Open(filepath.Join(t.TempDir(), "x.db"))
	defer s.Close()

	require.NoError(t, s.Insert(Task{ID: "t-chat", Skill: "chat", Prompt: "hi"}))
	require.NoError(t, s.Complete("t-chat", `{"kind":"final","summary":"ok","session_id":""}`))
	require.NoError(t, s.EnqueuePendingAck("t-chat", "completed"))

	require.NoError(t, s.Insert(Task{ID: "t-bash", Skill: "bash", Prompt: "echo"}))
	require.NoError(t, s.Complete("t-bash", `{"kind":"final","summary":"x","session_id":""}`))
	require.NoError(t, s.EnqueuePendingAck("t-bash", "completed"))

	pa, err := s.PopPendingAcks()
	require.NoError(t, err)
	require.Len(t, pa, 2)
	bySkill := map[string]PendingAck{}
	for _, p := range pa {
		bySkill[p.TaskID] = p
	}
	require.Equal(t, "chat", bySkill["t-chat"].Skill)
	require.Equal(t, "bash", bySkill["t-bash"].Skill)
}
