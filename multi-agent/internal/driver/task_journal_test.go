package driver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
