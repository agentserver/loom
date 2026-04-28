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
