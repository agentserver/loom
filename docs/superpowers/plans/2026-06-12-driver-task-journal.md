# Driver Task Journal Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Persist every driver-created delegated task ID immediately after `DelegateTask` succeeds, and expose those records through a local MCP recovery tool.

**Architecture:** Add an append-only `TaskJournal` JSONL file beside `audit.log`, wire it into `Tools`, and record every successful delegated task through one shared helper before any wait loop or side effect. Add `list_driver_tasks` to read recent records newest-first, and update driver docs and skills so operators know how to recover task IDs after client timeouts or interrupts.

**Tech Stack:** Go 1.26.2, driver MCP JSON-RPC tools, agentserver SDK `DelegateTask`, append-only JSONL files, existing `stretchr/testify` tests.

---

## Source Spec

Implement:

- `docs/superpowers/specs/2026-06-12-driver-task-journal-design.md`

## File Structure

Create or modify:

- Create: `multi-agent/internal/driver/task_journal.go`
  - Owns `TaskRecord`, `TaskJournal`, append/fsync behavior, newest-first reads, filtering, and limit normalization.
- Create: `multi-agent/internal/driver/task_journal_test.go`
  - Covers append/read behavior and `list_driver_tasks` behavior.
- Modify: `multi-agent/internal/driver/tools.go`
  - Adds `taskJournal`, `SetTaskJournal`, shared `recordDelegatedTask`, `list_driver_tasks`, and `submit_task`/`resume_task` recording.
- Modify: `multi-agent/internal/driver/slave_tools.go`
  - Records shell and permission delegated tasks before waiting.
- Modify: `multi-agent/internal/driver/register_mcp_tool.go`
  - Records `register_slave_mcp`.
- Modify: `multi-agent/internal/driver/unregister_mcp_tool.go`
  - Records `unregister_slave_mcp`.
- Modify: `multi-agent/internal/driver/slave_file_tools.go`
  - Records read/write/stat file delegated tasks before waiting.
- Modify: `multi-agent/internal/driver/contract_tools.go`
  - Records `submit_contract_task` before observer contract writes.
- Modify: `multi-agent/internal/driver/tools_test.go`
  - Test helper opens a journal and exposes its path.
- Modify: `multi-agent/internal/driver/slave_tools_test.go`
  - Adds regression coverage for `wait:true` recording before polling.
- Modify: `multi-agent/cmd/driver-agent/main.go`
  - Opens `driver-tasks.jsonl` beside `audit.log` and wires it into `Tools`.
- Modify: `multi-agent/cmd/driver-agent/main_test.go`
  - Covers local path resolution for `audit.log` and `driver-tasks.jsonl`.
- Modify: `multi-agent/cmd/driver-agent/README.md`
  - Documents default async shell behavior, task journal path, and `list_driver_tasks`.
- Modify: `skills/multiagent/SKILL.md`
  - Adds recovery guidance.
- Modify: `skills/multiagent/references/driver-tools.md`
  - Documents `list_driver_tasks`.
- Modify: `skills/mcp-acceptance/SKILL.md`
  - Mentions checking task journal after interrupted long calls.

## Task 1: Add TaskJournal Core

**Files:**
- Create: `multi-agent/internal/driver/task_journal_test.go`
- Create: `multi-agent/internal/driver/task_journal.go`

- [ ] **Step 1: Write failing append/read tests**

Create `multi-agent/internal/driver/task_journal_test.go` with these tests:

```go
package driver

import (
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
```

- [ ] **Step 2: Run tests and verify RED**

Run:

```bash
cd multi-agent
go test ./internal/driver -run TestTaskJournal -count=1
```

Expected: FAIL because `NewTaskJournal`, `TaskRecord`, and `Recent` do not exist.

- [ ] **Step 3: Implement TaskJournal**

Create `multi-agent/internal/driver/task_journal.go` with:

```go
package driver

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	taskJournalEvent      = "delegate_task"
	defaultTaskJournalMax = 50
	maxTaskJournalLimit   = 500
)

type TaskRecord struct {
	TS                string `json:"ts"`
	Event             string `json:"event"`
	Tool              string `json:"tool"`
	TaskID            string `json:"task_id"`
	SessionID         string `json:"session_id,omitempty"`
	TargetID          string `json:"target_id,omitempty"`
	TargetDisplayName string `json:"target_display_name,omitempty"`
	Skill             string `json:"skill,omitempty"`
	Status            string `json:"status,omitempty"`
	Wait              bool   `json:"wait"`
	TimeoutSec        int    `json:"timeout_sec,omitempty"`
}

type TaskJournal struct {
	mu   sync.Mutex
	f    *os.File
	path string
}

func NewTaskJournal(path string) (*TaskJournal, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("mkdir task journal dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open task journal: %w", err)
	}
	return &TaskJournal{f: f, path: path}, nil
}
```

Then add `Path`, `Append`, `Recent`, `Close`, and `normalizeTaskJournalLimit` in the same file:

```go
func (j *TaskJournal) Path() string { return j.path }

func (j *TaskJournal) Append(rec TaskRecord) error {
	if rec.TS == "" {
		rec.TS = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if rec.Event == "" {
		rec.Event = taskJournalEvent
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal task journal record: %w", err)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, err := j.f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("write task journal: %w", err)
	}
	if err := j.f.Sync(); err != nil {
		return fmt.Errorf("sync task journal: %w", err)
	}
	return nil
}

func (j *TaskJournal) Recent(limit int, taskID string) ([]TaskRecord, error) {
	limit = normalizeTaskJournalLimit(limit)
	j.mu.Lock()
	defer j.mu.Unlock()

	f, err := os.Open(j.path)
	if os.IsNotExist(err) {
		return []TaskRecord{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open task journal for read: %w", err)
	}
	defer f.Close()

	var records []TaskRecord
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var rec TaskRecord
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			return nil, fmt.Errorf("parse task journal line: %w", err)
		}
		if taskID != "" && rec.TaskID != taskID {
			continue
		}
		records = append(records, rec)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read task journal: %w", err)
	}
	for i, k := 0, len(records)-1; i < k; i, k = i+1, k-1 {
		records[i], records[k] = records[k], records[i]
	}
	if len(records) > limit {
		records = records[:limit]
	}
	return records, nil
}

func normalizeTaskJournalLimit(limit int) int {
	if limit <= 0 {
		return defaultTaskJournalMax
	}
	if limit > maxTaskJournalLimit {
		return maxTaskJournalLimit
	}
	return limit
}

func (j *TaskJournal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.f.Close()
}
```

- [ ] **Step 4: Run tests and verify GREEN**

Run:

```bash
cd multi-agent
go test ./internal/driver -run TestTaskJournal -count=1
```

Expected: PASS.

## Task 2: Wire Journal Into Tools and Add list_driver_tasks

**Files:**
- Modify: `multi-agent/internal/driver/tools.go`
- Modify: `multi-agent/internal/driver/tools_test.go`
- Modify: `multi-agent/internal/driver/task_journal_test.go`

- [ ] **Step 1: Add failing tool tests**

Append to `multi-agent/internal/driver/task_journal_test.go`:

```go
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
```

Also add imports for `context` and `encoding/json` if the test file does not already have them.

- [ ] **Step 2: Run tests and verify RED**

Run:

```bash
cd multi-agent
go test ./internal/driver -run 'TestListDriverTasks|TestTaskJournal' -count=1
```

Expected: FAIL because `Tools.taskJournal`, test helper wiring, and `list_driver_tasks` do not exist.

- [ ] **Step 3: Add Tools wiring and helper**

Modify `multi-agent/internal/driver/tools.go`:

```go
type Tools struct {
	reg            *FileRegistry
	audit          *AuditLog
	taskJournal    *TaskJournal
	sdk            SDKClient
	cfg            *Config
	observer       ObserverSink
	relay          *ObserverRelay
	contractRunner ContractRunner
}

func (t *Tools) SetTaskJournal(j *TaskJournal) {
	t.taskJournal = j
}

type delegatedTaskRecord struct {
	Tool              string
	Response          *agentsdk.DelegateTaskResponse
	TargetID          string
	TargetDisplayName string
	Skill             string
	Wait              bool
	TimeoutSec        int
}

func (t *Tools) recordDelegatedTask(rec delegatedTaskRecord) error {
	if t.taskJournal == nil || rec.Response == nil || rec.Response.TaskID == "" {
		return nil
	}
	err := t.taskJournal.Append(TaskRecord{
		Tool:              rec.Tool,
		TaskID:            rec.Response.TaskID,
		SessionID:         rec.Response.SessionID,
		TargetID:          rec.TargetID,
		TargetDisplayName: rec.TargetDisplayName,
		Skill:             rec.Skill,
		Status:            rec.Response.Status,
		Wait:              rec.Wait,
		TimeoutSec:        rec.TimeoutSec,
	})
	if err != nil {
		return &MCPToolError{Message: fmt.Sprintf("task %s was created but driver failed to record it in driver-tasks.jsonl: %v", rec.Response.TaskID, err)}
	}
	return nil
}
```

Modify `All()` to include:

```go
&listDriverTasksTool{t},
```

Place it after `list_agents`.

- [ ] **Step 4: Add list_driver_tasks implementation**

In `multi-agent/internal/driver/tools.go`, add:

```go
type listDriverTasksTool struct{ t *Tools }

func (l *listDriverTasksTool) Name() string { return "list_driver_tasks" }

func (l *listDriverTasksTool) Description() string {
	return "List locally recorded driver-created delegated task IDs for recovery after MCP client timeouts or interrupts."
}

func (l *listDriverTasksTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"limit":{"type":"integer"},"task_id":{"type":"string"}},"additionalProperties":false}`)
}

func (l *listDriverTasksTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		Limit  int    `json:"limit,omitempty"`
		TaskID string `json:"task_id,omitempty"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, &MCPToolError{Message: "invalid args: " + err.Error()}
		}
	}
	if l.t.taskJournal == nil {
		return json.Marshal(map[string]interface{}{"journal_path": "", "tasks": []TaskRecord{}})
	}
	records, err := l.t.taskJournal.Recent(args.Limit, args.TaskID)
	if err != nil {
		return nil, &MCPToolError{Message: err.Error()}
	}
	return json.Marshal(map[string]interface{}{"journal_path": l.t.taskJournal.Path(), "tasks": records})
}
```

- [ ] **Step 5: Wire test helper**

Modify `newTestToolsWithObserver` in `multi-agent/internal/driver/tools_test.go`:

```go
j, err := NewTaskJournal(filepath.Join(dir, "driver-tasks.jsonl"))
if err != nil {
	t.Fatal(err)
}
t.Cleanup(func() { j.Close() })
tools := NewTools(NewFileRegistry(50000), a, sdk, cfg, obs)
tools.SetTaskJournal(j)
return tools
```

- [ ] **Step 6: Run tests and verify GREEN**

Run:

```bash
cd multi-agent
go test ./internal/driver -run 'TestListDriverTasks|TestTaskJournal' -count=1
```

Expected: PASS.

## Task 3: Record Shell and submit_task Before Waiting or Side Effects

**Files:**
- Modify: `multi-agent/internal/driver/slave_tools_test.go`
- Modify: `multi-agent/internal/driver/tools_test.go`
- Modify: `multi-agent/internal/driver/slave_tools.go`
- Modify: `multi-agent/internal/driver/tools.go`

- [ ] **Step 1: Add failing shell wait ordering test**

Append to `multi-agent/internal/driver/slave_tools_test.go`:

```go
func TestRunSlaveBashWaitTrueRecordsTaskBeforePolling(t *testing.T) {
	getTaskCalled := false
	var tools *Tools
	sdk := &fakeSDK{
		cards: []agentsdk.AgentCard{{
			AgentID:     "agent-1",
			DisplayName: "slave-1",
			Status:      "available",
			Card:        json.RawMessage(`{"skills":["bash"],"command_interfaces":[{"skill":"bash","kind":"bash","command":"/bin/bash","default":true}]}`),
		}},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			return &agentsdk.DelegateTaskResponse{TaskID: "task-wait", Status: "submitted"}, nil
		},
		getTaskFunc: func(taskID string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			getTaskCalled = true
			records, err := tools.taskJournal.Recent(1, "task-wait")
			require.NoError(t, err)
			require.Len(t, records, 1)
			return &agentsdk.TaskInfo{TaskID: taskID, Status: "completed", Output: "ok"}, nil
		},
	}
	tools = newTestTools(t, sdk)

	_, err := toolByName(t, tools, "run_slave_bash").Call(context.Background(), json.RawMessage(`{"target_display_name":"slave-1","script":"sleep 30","wait":true}`))
	require.NoError(t, err)
	require.True(t, getTaskCalled)
}
```

- [ ] **Step 2: Add failing submit_task journal test**

Append to `multi-agent/internal/driver/tools_test.go`:

```go
func TestSubmitTaskRecordsDelegatedTask(t *testing.T) {
	sdk := &fakeSDK{
		cards: []agentsdk.AgentCard{{
			AgentID:     "agent-1",
			DisplayName: "master-1",
			Status:      "available",
			Card:        json.RawMessage(`{"skills":["fanout"]}`),
		}},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			return &agentsdk.DelegateTaskResponse{TaskID: "task-submit", SessionID: "session-1", Status: "submitted"}, nil
		},
	}
	tools := newTestTools(t, sdk)

	_, err := toolByName(t, tools, "submit_task").Call(context.Background(), json.RawMessage(`{"prompt":"do work","skill":"chat"}`))
	require.NoError(t, err)

	records, err := tools.taskJournal.Recent(1, "task-submit")
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "submit_task", records[0].Tool)
	require.Equal(t, "agent-1", records[0].TargetID)
	require.Equal(t, "master-1", records[0].TargetDisplayName)
	require.Equal(t, "chat", records[0].Skill)
	require.Equal(t, "session-1", records[0].SessionID)
	require.False(t, records[0].Wait)
}
```

- [ ] **Step 3: Run tests and verify RED**

Run:

```bash
cd multi-agent
go test ./internal/driver -run 'TestRunSlaveBashWaitTrueRecordsTaskBeforePolling|TestSubmitTaskRecordsDelegatedTask' -count=1
```

Expected: FAIL because delegation call sites do not record yet.

- [ ] **Step 4: Record shell helpers immediately**

Change `delegateShellTask` to receive the tool name:

```go
func (t *Tools) delegateShellTask(ctx context.Context, card agentsdk.AgentCard, toolName, skill string, args shellToolArgs) (json.RawMessage, error)
```

Call it as:

```go
return r.t.delegateShellTask(ctx, card, r.Name(), "bash", args)
return r.t.delegateShellTask(ctx, card, r.Name(), "powershell", args)
return r.t.delegateShellTask(ctx, card, r.Name(), commandInterface.Kind, args)
```

After `DelegateTask` succeeds and after computing `wait`, record before returning or waiting:

```go
if err := t.recordDelegatedTask(delegatedTaskRecord{
	Tool:              toolName,
	Response:          resp,
	TargetID:          card.AgentID,
	TargetDisplayName: card.DisplayName,
	Skill:             skill,
	Wait:              wait,
	TimeoutSec:        args.TimeoutSec,
}); err != nil {
	return nil, err
}
```

- [ ] **Step 5: Record submit_task immediately**

In `submitTaskTool.Call`, after `DelegateTask` succeeds and before `emit`, `RebindWriteTokenTaskID`, observer write updates, or `TrackTask`, add:

```go
if err := s.t.recordDelegatedTask(delegatedTaskRecord{
	Tool:              s.Name(),
	Response:          resp,
	TargetID:          targetID,
	TargetDisplayName: targetName,
	Skill:             skill,
	Wait:              false,
	TimeoutSec:        timeout,
}); err != nil {
	return nil, err
}
```

- [ ] **Step 6: Run tests and verify GREEN**

Run:

```bash
cd multi-agent
go test ./internal/driver -run 'TestRunSlaveBashWaitTrueRecordsTaskBeforePolling|TestSubmitTaskRecordsDelegatedTask|TestListDriverTasks|TestTaskJournal' -count=1
```

Expected: PASS.

## Task 4: Record Remaining DelegateTask Call Sites and Driver Startup Path

**Files:**
- Modify: `multi-agent/internal/driver/slave_tools.go`
- Modify: `multi-agent/internal/driver/register_mcp_tool.go`
- Modify: `multi-agent/internal/driver/unregister_mcp_tool.go`
- Modify: `multi-agent/internal/driver/slave_file_tools.go`
- Modify: `multi-agent/internal/driver/contract_tools.go`
- Modify: `multi-agent/internal/driver/tools.go`
- Modify: `multi-agent/cmd/driver-agent/main.go`
- Modify: `multi-agent/cmd/driver-agent/main_test.go`

- [ ] **Step 1: Add failing representative tests**

Add one test for a wait-only helper and one test for local path resolution:

```go
func TestPermissionTaskRecordsBeforeWaiting(t *testing.T) {
	var tools *Tools
	sdk := &fakeSDK{
		cards: []agentsdk.AgentCard{{
			AgentID:     "agent-1",
			DisplayName: "slave-1",
			Status:      "available",
			Card:        json.RawMessage(`{"skills":["permissions"]}`),
		}},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			return &agentsdk.DelegateTaskResponse{TaskID: "task-perm", Status: "submitted"}, nil
		},
		getTaskFunc: func(taskID string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			records, err := tools.taskJournal.Recent(1, "task-perm")
			require.NoError(t, err)
			require.Len(t, records, 1)
			require.Equal(t, "get_slave_claude_permissions", records[0].Tool)
			return &agentsdk.TaskInfo{TaskID: taskID, Status: "completed", Output: `{"ok":true}`}, nil
		},
	}
	tools = newTestTools(t, sdk)

	_, err := toolByName(t, tools, "get_slave_claude_permissions").Call(context.Background(), json.RawMessage(`{"target_display_name":"slave-1"}`))
	require.NoError(t, err)
}
```

In `multi-agent/cmd/driver-agent/main_test.go`, add:

```go
func TestResolveDriverLocalPathUsesAuditLogDir(t *testing.T) {
	cfg := &driver.Config{}
	cfg.Credentials.ShortID = "drv-001"
	cfg.DriverDefaults.AuditLogDir = t.TempDir()

	auditPath, err := resolveDriverLocalPath(cfg, "audit.log")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(cfg.DriverDefaults.AuditLogDir, "audit.log"), auditPath)

	journalPath, err := resolveDriverLocalPath(cfg, "driver-tasks.jsonl")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(cfg.DriverDefaults.AuditLogDir, "driver-tasks.jsonl"), journalPath)
}
```

Add `path/filepath` to imports.

- [ ] **Step 2: Run tests and verify RED**

Run:

```bash
cd multi-agent
go test ./internal/driver -run TestPermissionTaskRecordsBeforeWaiting -count=1
go test ./cmd/driver-agent -run TestResolveDriverLocalPathUsesAuditLogDir -count=1
```

Expected: first FAILS because permissions do not record, second FAILS because `resolveDriverLocalPath` does not exist.

- [ ] **Step 3: Record remaining driver delegations**

Add `recordDelegatedTask` immediately after successful `DelegateTask` in:

```text
multi-agent/internal/driver/slave_tools.go              delegatePermissionTask
multi-agent/internal/driver/register_mcp_tool.go        register_slave_mcp
multi-agent/internal/driver/unregister_mcp_tool.go      unregister_slave_mcp
multi-agent/internal/driver/slave_file_tools.go         read_slave_file, write_slave_file, stat_slave_file
multi-agent/internal/driver/contract_tools.go           submit_contract_task
multi-agent/internal/driver/tools.go                    resume_task
```

Use these exact records:

```go
// delegatePermissionTask, after changing the helper to accept toolName.
delegatedTaskRecord{Tool: toolName, Response: resp, TargetID: card.AgentID, TargetDisplayName: card.DisplayName, Skill: skill, Wait: true, TimeoutSec: 0}

// register_slave_mcp
delegatedTaskRecord{Tool: r.Name(), Response: resp, TargetID: card.AgentID, TargetDisplayName: card.DisplayName, Skill: "register_mcp", Wait: true, TimeoutSec: args.TimeoutSec}

// unregister_slave_mcp
delegatedTaskRecord{Tool: u.Name(), Response: resp, TargetID: card.AgentID, TargetDisplayName: card.DisplayName, Skill: "unregister_mcp", Wait: true, TimeoutSec: args.TimeoutSec}

// read_slave_file
delegatedTaskRecord{Tool: r.Name(), Response: resp, TargetID: card.AgentID, TargetDisplayName: card.DisplayName, Skill: "file", Wait: true, TimeoutSec: 0}

// write_slave_file
delegatedTaskRecord{Tool: w.Name(), Response: resp, TargetID: card.AgentID, TargetDisplayName: card.DisplayName, Skill: "file", Wait: true, TimeoutSec: 0}

// stat_slave_file
delegatedTaskRecord{Tool: s.Name(), Response: resp, TargetID: card.AgentID, TargetDisplayName: card.DisplayName, Skill: "file", Wait: true, TimeoutSec: 0}

// submit_contract_task
delegatedTaskRecord{Tool: s.Name(), Response: resp, TargetID: targetID, TargetDisplayName: targetName, Skill: skill, Wait: false, TimeoutSec: timeout}

// resume_task. TargetDisplayName is empty because GetTask only guarantees TargetID.
delegatedTaskRecord{Tool: r.Name(), Response: resp, TargetID: info.TargetID, Skill: "chat_resume", Wait: true, TimeoutSec: timeout}
```

- [ ] **Step 4: Wire driver-agent startup**

Replace `resolveAuditPath` with a reusable local path helper in `multi-agent/cmd/driver-agent/main.go`:

```go
func resolveDriverLocalPath(cfg *driver.Config, name string) (string, error) {
	dir := cfg.DriverDefaults.AuditLogDir
	if dir == "" {
		u, err := user.Current()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(u.HomeDir, ".cache", "multi-agent", cfg.Credentials.ShortID)
	}
	return filepath.Join(dir, name), nil
}

func resolveAuditPath(cfg *driver.Config) (string, error) {
	return resolveDriverLocalPath(cfg, "audit.log")
}
```

In `main`, after opening audit:

```go
taskJournalPath, err := resolveDriverLocalPath(cfg, "driver-tasks.jsonl")
if err != nil {
	die(err.Error())
}
taskJournal, err := driver.NewTaskJournal(taskJournalPath)
if err != nil {
	die(err.Error())
}
defer taskJournal.Close()
```

After `tools := driver.NewTools(...)`:

```go
tools.SetTaskJournal(taskJournal)
```

- [ ] **Step 5: Run tests and verify GREEN**

Run:

```bash
cd multi-agent
go test ./internal/driver -run 'TestPermissionTaskRecordsBeforeWaiting|TestRunSlaveBashWaitTrueRecordsTaskBeforePolling|TestSubmitTaskRecordsDelegatedTask|TestListDriverTasks|TestTaskJournal' -count=1
go test ./cmd/driver-agent -run TestResolveDriverLocalPathUsesAuditLogDir -count=1
```

Expected: PASS.

## Task 5: Docs, Full Verification, and Prod Smoke

**Files:**
- Modify: `multi-agent/cmd/driver-agent/README.md`
- Modify: `skills/multiagent/SKILL.md`
- Modify: `skills/multiagent/references/driver-tools.md`
- Modify: `skills/mcp-acceptance/SKILL.md`

- [ ] **Step 1: Update docs**

Document:

```text
driver-tasks.jsonl lives beside audit.log.
list_driver_tasks returns recent locally recorded task IDs.
Use list_driver_tasks after an interrupted or timed-out long tools/call before deciding a task is lost.
The journal records metadata only, not prompts, scripts, env vars, tokens, or file contents.
```

- [ ] **Step 2: Run formatting**

Run:

```bash
cd multi-agent
gofmt -w cmd/driver-agent/main.go cmd/driver-agent/main_test.go internal/driver/task_journal.go internal/driver/task_journal_test.go internal/driver/tools.go internal/driver/tools_test.go internal/driver/slave_tools.go internal/driver/slave_tools_test.go internal/driver/register_mcp_tool.go internal/driver/unregister_mcp_tool.go internal/driver/slave_file_tools.go internal/driver/contract_tools.go
```

Expected: exit 0.

- [ ] **Step 3: Run targeted tests**

Run:

```bash
cd multi-agent
go test ./internal/driver -run 'TaskJournal|ListDriverTasks|RecordsDelegatedTask|RecordsTaskBefore|SubmitTaskRecords|PermissionTaskRecords' -count=1
go test ./cmd/driver-agent -run 'DriverLocalPath|TaskJournal' -count=1
```

Expected: PASS.

- [ ] **Step 4: Run full tests**

Run:

```bash
cd multi-agent
go test ./...
git diff --check
```

Expected: PASS and no whitespace errors.

- [ ] **Step 5: Prod smoke when credentials are available**

From `multi-agent/tests/prod_test`, rebuild and re-register the driver/slave binaries, then verify:

```text
1. list_agents sees the prod slave.
2. run_slave_bash or run_slave_powershell with wait:false returns task_id quickly.
3. run_slave_bash or run_slave_powershell with wait:true records task_id in driver-tasks.jsonl before wait completes.
4. list_driver_tasks returns that task_id while the long task is still running.
```

If login or secret input is required, stop and ask the user rather than printing or storing credentials.

## Self-Review Checklist

- The plan implements every spec goal: durable local task record, immediate write timing, all delegation paths, recovery tool, docs, and tests.
- The known agentserver response-loss gap is documented and not hidden by the implementation.
- No plan step writes prompts, scripts, env values, tokens, or file contents to the journal.
- Every production behavior change has a failing test before implementation.
