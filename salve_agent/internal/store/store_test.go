package store

import (
	"path/filepath"
	"testing"

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
