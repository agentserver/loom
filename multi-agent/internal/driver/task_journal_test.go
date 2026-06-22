package driver

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTaskJournalAppendsAndReadsNewestFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "driver-tasks.jsonl")
	j, err := NewTaskJournal(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, j.Close()) })

	require.NoError(t, j.Append(TaskRecord{Tool: "submit_task", TaskID: "task-1", TargetID: "agent-1", Skill: "chat"}))
	require.NoError(t, j.Append(TaskRecord{Tool: "run_slave_bash", TaskID: "task-2", TargetID: "agent-2", Skill: "bash", Wait: true, TimeoutSec: 600}))

	records, err := j.Recent(10, "")
	require.NoError(t, err)
	require.Len(t, records, 2)
	require.Equal(t, "task-2", records[0].TaskID)
	require.Equal(t, "delegate_task", records[0].Event)
	require.NotEmpty(t, records[0].TS)
	require.Equal(t, "task-1", records[1].TaskID)
}

func TestTaskJournalRecentFiltersAndCapsLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "driver-tasks.jsonl")
	j, err := NewTaskJournal(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, j.Close()) })

	require.NoError(t, j.Append(TaskRecord{Tool: "submit_task", TaskID: "task-a"}))
	require.NoError(t, j.Append(TaskRecord{Tool: "submit_task", TaskID: "task-b"}))
	require.NoError(t, j.Append(TaskRecord{Tool: "resume_task", TaskID: "task-a"}))

	records, err := j.Recent(1, "task-a")
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "task-a", records[0].TaskID)
	require.Equal(t, "resume_task", records[0].Tool)
}

func TestListDriverTasksReturnsNewestFirst(t *testing.T) {
	tools := newTestTools(t, &fakeSDK{})
	require.NoError(t, tools.taskJournal.Append(TaskRecord{Tool: "submit_task", TaskID: "task-1"}))
	require.NoError(t, tools.taskJournal.Append(TaskRecord{Tool: "run_slave_bash", TaskID: "task-2"}))

	raw, err := toolByName(t, tools, "list_driver_tasks").Call(context.Background(), json.RawMessage(`{"limit":1}`))
	require.NoError(t, err)

	var out struct {
		JournalPath string       `json:"journal_path"`
		Tasks       []TaskRecord `json:"tasks"`
	}
	require.NoError(t, json.Unmarshal(raw, &out))
	require.NotEmpty(t, out.JournalPath)
	require.Len(t, out.Tasks, 1)
	require.Equal(t, "task-2", out.Tasks[0].TaskID)
}

func TestListDriverTasksFiltersTaskID(t *testing.T) {
	tools := newTestTools(t, &fakeSDK{})
	require.NoError(t, tools.taskJournal.Append(TaskRecord{Tool: "submit_task", TaskID: "task-1"}))
	require.NoError(t, tools.taskJournal.Append(TaskRecord{Tool: "resume_task", TaskID: "task-1"}))
	require.NoError(t, tools.taskJournal.Append(TaskRecord{Tool: "submit_task", TaskID: "task-2"}))

	raw, err := toolByName(t, tools, "list_driver_tasks").Call(context.Background(), json.RawMessage(`{"task_id":"task-1"}`))
	require.NoError(t, err)

	var out struct {
		Tasks []TaskRecord `json:"tasks"`
	}
	require.NoError(t, json.Unmarshal(raw, &out))
	require.Len(t, out.Tasks, 2)
	require.Equal(t, "resume_task", out.Tasks[0].Tool)
	require.Equal(t, "submit_task", out.Tasks[1].Tool)
}

func TestListDriverTasksReturnsEmptyArrayWhenJournalIsEmpty(t *testing.T) {
	tools := newTestTools(t, &fakeSDK{})

	raw, err := toolByName(t, tools, "list_driver_tasks").Call(context.Background(), json.RawMessage(`{}`))
	require.NoError(t, err)

	var out struct {
		Tasks []TaskRecord `json:"tasks"`
	}
	require.NoError(t, json.Unmarshal(raw, &out))
	require.NotNil(t, out.Tasks)
	require.Empty(t, out.Tasks)
}

// countJournalLines counts the number of non-empty lines in a JSONL file.
func countJournalLines(t *testing.T, path string) int {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) != "" {
			n++
		}
	}
	require.NoError(t, sc.Err())
	return n
}

// TestTerminalRecordDedupKeepsOnlyTerminal: delegation record + terminal record
// → Recent returns only the terminal row, carrying both child_session_id and child_agent_id.
func TestTerminalRecordDedupKeepsOnlyTerminal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "driver-tasks.jsonl")
	j, err := NewTaskJournal(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, j.Close()) })

	// Delegation-time record (non-terminal, with ChildAgentID)
	require.NoError(t, j.Append(TaskRecord{
		Tool:         "submit_task",
		TaskID:       "task-A",
		TargetID:     "slave-2",
		ChildAgentID: "slave-2",
		Skill:        "chat",
	}))
	// Terminal record for the same task_id
	require.NoError(t, j.Append(TaskRecord{
		Tool:           "submit_task",
		TaskID:         "task-A",
		TargetID:       "slave-2",
		ChildAgentID:   "slave-2",
		ChildSessionID: "child-sess",
		Status:         "completed",
		Terminal:       true,
	}))

	records, err := j.Recent(10, "")
	require.NoError(t, err)
	require.Len(t, records, 1, "only the terminal record should survive dedup")
	require.True(t, records[0].Terminal)
	require.Equal(t, "child-sess", records[0].ChildSessionID)
	require.Equal(t, "slave-2", records[0].ChildAgentID)

	latest, ok := j.LatestByTaskID("task-A")
	require.True(t, ok)
	require.True(t, latest.Terminal)
	require.Equal(t, "child-sess", latest.ChildSessionID)
}

// TestNonTerminalMultipleRowsNotDeduped: two non-terminal resume_task rows for
// the same task_id (no terminal counterpart) must both be returned newest-first.
func TestNonTerminalMultipleRowsNotDeduped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "driver-tasks.jsonl")
	j, err := NewTaskJournal(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, j.Close()) })

	require.NoError(t, j.Append(TaskRecord{Tool: "submit_task", TaskID: "task-1"}))
	require.NoError(t, j.Append(TaskRecord{Tool: "resume_task", TaskID: "task-1"}))

	records, err := j.Recent(10, "task-1")
	require.NoError(t, err)
	require.Len(t, records, 2, "both non-terminal rows must appear")
	require.Equal(t, "resume_task", records[0].Tool)
	require.Equal(t, "submit_task", records[1].Tool)
}

// TestMixedTerminalAndNonTerminal: task A (terminal) collapses to 1 row;
// task B (two non-terminal rows) stays at 2 rows → total 3 records.
func TestMixedTerminalAndNonTerminal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "driver-tasks.jsonl")
	j, err := NewTaskJournal(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, j.Close()) })

	// task-A: delegation + terminal
	require.NoError(t, j.Append(TaskRecord{Tool: "submit_task", TaskID: "task-A"}))
	require.NoError(t, j.Append(TaskRecord{Tool: "submit_task", TaskID: "task-A", Terminal: true, ChildSessionID: "sess-A", Status: "completed"}))
	// task-B: two non-terminal rows
	require.NoError(t, j.Append(TaskRecord{Tool: "submit_task", TaskID: "task-B"}))
	require.NoError(t, j.Append(TaskRecord{Tool: "resume_task", TaskID: "task-B"}))

	records, err := j.Recent(50, "")
	require.NoError(t, err)
	require.Len(t, records, 3, "1 terminal for A + 2 non-terminal for B")
	// newest-first: task-B resume, task-B submit, task-A terminal
	require.Equal(t, "task-B", records[0].TaskID)
	require.Equal(t, "resume_task", records[0].Tool)
	require.Equal(t, "task-B", records[1].TaskID)
	require.Equal(t, "task-A", records[2].TaskID)
	require.True(t, records[2].Terminal)
}

// TestTerminalRecordPreservesStatus: a terminal record with Status="failed" must
// be returned with status="failed", not silently rewritten.
func TestTerminalRecordPreservesStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "driver-tasks.jsonl")
	j, err := NewTaskJournal(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, j.Close()) })

	require.NoError(t, j.Append(TaskRecord{Tool: "submit_task", TaskID: "task-X"}))
	require.NoError(t, j.Append(TaskRecord{
		Tool:           "submit_task",
		TaskID:         "task-X",
		Terminal:       true,
		ChildSessionID: "sess-fail",
		Status:         "failed",
	}))

	records, err := j.Recent(10, "task-X")
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "failed", records[0].Status)
	require.True(t, records[0].Terminal)
}

func TestListDriverTasksSkipsMalformedJournalLinesWithWarning(t *testing.T) {
	tools := newTestTools(t, &fakeSDK{})
	require.NoError(t, tools.taskJournal.Append(TaskRecord{Tool: "submit_task", TaskID: "task-1"}))

	f, err := os.OpenFile(tools.taskJournal.Path(), os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString(`{"task_id":` + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	require.NoError(t, tools.taskJournal.Append(TaskRecord{Tool: "run_slave_bash", TaskID: "task-2"}))

	raw, err := toolByName(t, tools, "list_driver_tasks").Call(context.Background(), json.RawMessage(`{}`))
	require.NoError(t, err)

	var out struct {
		Tasks    []TaskRecord `json:"tasks"`
		Warnings []string     `json:"warnings"`
	}
	require.NoError(t, json.Unmarshal(raw, &out))
	require.Len(t, out.Tasks, 2)
	require.Equal(t, "task-2", out.Tasks[0].TaskID)
	require.Equal(t, "task-1", out.Tasks[1].TaskID)
	require.Len(t, out.Warnings, 1)
	require.Contains(t, out.Warnings[0], "skipped malformed task journal line 2")
}
