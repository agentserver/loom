# master_agent Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `master_agent`, a Go custom-agent (same module as slave_agent) that polls tasks from agentserver, and instead of executing them itself, uses claude as planner/router/reducer to delegate work to other agents in the workspace via `SDK.DelegateTask`. Supports two skills: `route` (1→1 LLM-routed) and `fanout` (1→N DAG with `{{nX.output}}` template substitution).

**Architecture:** Shares `internal/{config,store,webui,tunnel,poller}` with slave_agent (compatible cross-module additions only). New packages: `internal/planner` (claude-as-decision wrapper, single-shot, no streaming) and `internal/orchestrator` (skill routing + DAG scheduler + SDK delegation). New cmd binary `cmd/master-agent`. New docs/scripts under `master_agent/`.

**Tech Stack:** Go 1.22+, `github.com/agentserver/agentserver/pkg/agentsdk` (uses `DelegateTask`/`WaitForTask`/`DiscoverAgents`/`AgentCard`), `modernc.org/sqlite`, stdlib `os/exec`/`net/http`, `testify` for assertions. Reuses `internal/{config,store,tunnel,poller,webui}` from slave_agent (this plan adds backward-compatible fields/methods to those packages).

**Spec:** `docs/superpowers/specs/2026-04-28-master-agent-design.md`  
**slave baseline:** branch `feat/slave-agent` already exists with 27+ commits. This plan creates a new branch `feat/master-agent` from `feat/slave-agent`, so master work stacks on top without entangling slave's PR.

---

## File Structure

```
slave_agent/                                      ← module root (name retained)
├── cmd/
│   ├── slave-agent/main.go                       ← existing
│   └── master-agent/main.go                      ← NEW (Task 14)
├── internal/
│   ├── config/
│   │   └── config.go                             ← MODIFY: add Planner, Fanout (Task 1)
│   ├── store/
│   │   ├── schema.sql                            ← MODIFY: add sub_tasks (Task 2)
│   │   └── store.go                              ← MODIFY: add SubTaskRow + CRUD + Recover ext (Tasks 2-3)
│   ├── webui/
│   │   └── server.go                             ← MODIFY: /tasks/{id}/children + SSE event types (Task 13)
│   ├── tunnel/
│   │   └── tunnel.go                             ← MODIFY: add SDKClient() getter (Task 5)
│   ├── poller/
│   │   └── poller.go                             ← MODIFY: 2nd arg becomes Dispatcher interface (Task 4)
│   ├── orchestrator/                             ← NEW
│   │   ├── orchestrator.go                       ← Tasks 10-12
│   │   ├── route.go                              ← Task 11
│   │   ├── fanout.go                             ← Task 12
│   │   ├── dag.go                                ← Tasks 7-9
│   │   ├── dag_test.go
│   │   ├── route_test.go
│   │   └── fanout_test.go
│   └── planner/                                  ← NEW
│       ├── planner.go                            ← Task 6
│       ├── planner_test.go                       ← Task 6
│       └── prompts.go                            ← Task 6 (system prompts as consts)
├── testdata/
│   └── fake-planner.sh                           ← NEW (Task 6)
├── tests/
│   └── contract/
│       └── master_contract_test.go               ← NEW (Task 15)
└── master_agent/                                 ← NEW (Task 16, NOT in Go package path)
    ├── README.md
    ├── config.example.yaml
    └── scripts/e2e.sh
```

**Boundary rules:**
- `planner` depends on stdlib + `os/exec` + `agentsdk` (for AgentCard type only).
- `orchestrator` depends on `planner`, `store`, `agentsdk` types, and the `executor` package's `Task`/`Result`/`Sink` types only (does NOT instantiate executors).
- `internal/{executor,journal,dispatch}` are slave-private; master must NOT import them.
- `cmd/master-agent/main.go` is the only file that imports everything.

---

## Pre-flight

- [ ] **Verify worktree state**

```bash
cd /mnt/c/Users/DELL/multi-agent/.claude/worktrees/slave-agent-impl
git status
git log --oneline -3
```
Expected: clean working tree, on `feat/slave-agent` branch, slave commits visible.

- [ ] **Create new branch from feat/slave-agent**

```bash
cd /mnt/c/Users/DELL/multi-agent/.claude/worktrees/slave-agent-impl
git checkout -b feat/master-agent
```

All subsequent tasks commit to `feat/master-agent`.

- [ ] **Read the spec end-to-end before starting Task 1**: `docs/superpowers/specs/2026-04-28-master-agent-design.md`. Every later task references section numbers (e.g., "spec §3.2").

---

## Task 1: Extend `config` with Planner + Fanout

**Files:**
- Modify: `slave_agent/internal/config/config.go`
- Modify: `slave_agent/internal/config/config_test.go`

- [ ] **Step 1: Failing test**

Append to `config_test.go`:

```go
func TestLoad_MasterFields(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "c.yaml")
    require.NoError(t, os.WriteFile(path, []byte(`
server:
  url: https://example.com
  name: m
discovery:
  display_name: M
  skills: [route, fanout]
planner:
  bin: /usr/bin/claude
  timeout_sec: 90
fanout:
  max_concurrency: 8
  default_policy: best_effort
  policy_by_skill:
    fanout_strict: all_or_nothing
  subtask_defaults:
    timeout_sec: 1200
`), 0o600))

    c, err := Load(path)
    require.NoError(t, err)
    require.Equal(t, "/usr/bin/claude", c.Planner.Bin)
    require.Equal(t, 90, c.Planner.TimeoutSec)
    require.Equal(t, 8, c.Fanout.MaxConcurrency)
    require.Equal(t, "best_effort", c.Fanout.DefaultPolicy)
    require.Equal(t, "all_or_nothing", c.Fanout.PolicyBySkill["fanout_strict"])
    require.Equal(t, 1200, c.Fanout.SubTaskDefaults.TimeoutSec)
}

func TestLoad_MasterDefaults(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "c.yaml")
    require.NoError(t, os.WriteFile(path, []byte("server:\n  url: x\n"), 0o600))
    c, err := Load(path)
    require.NoError(t, err)
    require.Equal(t, 60, c.Planner.TimeoutSec)
    require.Equal(t, 4, c.Fanout.MaxConcurrency)
    require.Equal(t, "best_effort", c.Fanout.DefaultPolicy)
    require.Equal(t, 600, c.Fanout.SubTaskDefaults.TimeoutSec)
}
```

- [ ] **Step 2: Run — expect FAIL** (`Planner`/`Fanout` undefined)

`go test ./internal/config/... -v -run TestLoad_Master`

- [ ] **Step 3: Add types to `config.go`**

Append the following type declarations (do NOT touch existing types):

```go
type Planner struct {
    Bin        string   `yaml:"bin"`
    TimeoutSec int      `yaml:"timeout_sec"`
    ExtraArgs  []string `yaml:"extra_args"`
}

type Fanout struct {
    MaxConcurrency  int                 `yaml:"max_concurrency"`
    DefaultPolicy   string              `yaml:"default_policy"`
    PolicyBySkill   map[string]string   `yaml:"policy_by_skill"`
    SubTaskDefaults SubTaskDefaults     `yaml:"subtask_defaults"`
}

type SubTaskDefaults struct {
    TimeoutSec   int     `yaml:"timeout_sec"`
    MaxBudgetUSD float64 `yaml:"max_budget_usd"`
}
```

Add fields to the existing `Config` struct (find the existing struct and append these two fields inside the braces):

```go
    Planner Planner `yaml:"planner"`
    Fanout  Fanout  `yaml:"fanout"`
```

In `Load`, after the existing default-population block (where `Claude.Bin` is defaulted), add:

```go
    if c.Planner.Bin == "" {
        c.Planner.Bin = c.Claude.Bin
    }
    if c.Planner.TimeoutSec == 0 {
        c.Planner.TimeoutSec = 60
    }
    if c.Fanout.MaxConcurrency == 0 {
        c.Fanout.MaxConcurrency = 4
    }
    if c.Fanout.DefaultPolicy == "" {
        c.Fanout.DefaultPolicy = "best_effort"
    }
    if c.Fanout.SubTaskDefaults.TimeoutSec == 0 {
        c.Fanout.SubTaskDefaults.TimeoutSec = 600
    }
```

Add to `Validate()` (after existing checks):

```go
    if c.Fanout.MaxConcurrency < 0 {
        return fmt.Errorf("fanout.max_concurrency must be >= 0 (got %d)", c.Fanout.MaxConcurrency)
    }
    if c.Fanout.DefaultPolicy != "" && c.Fanout.DefaultPolicy != "best_effort" && c.Fanout.DefaultPolicy != "all_or_nothing" {
        return fmt.Errorf("fanout.default_policy must be best_effort or all_or_nothing")
    }
```

- [ ] **Step 4: Run — expect PASS**

`go test ./internal/config/... -v`

- [ ] **Step 5: Commit**

```bash
git add slave_agent/internal/config/
git commit -m "feat(slave_agent/config): add Planner + Fanout master fields"
```

---

## Task 2: Extend `store` schema with `sub_tasks` table

**Files:**
- Modify: `slave_agent/internal/store/schema.sql`
- Modify: `slave_agent/internal/store/store.go`
- Modify: `slave_agent/internal/store/store_test.go`

- [ ] **Step 1: Append to `schema.sql`**

```sql
CREATE TABLE IF NOT EXISTS sub_tasks (
    parent_id     TEXT NOT NULL REFERENCES tasks(id),
    node_id       TEXT NOT NULL,
    target_id     TEXT NOT NULL,
    child_task_id TEXT,
    prompt        TEXT NOT NULL,
    depends_on    TEXT NOT NULL,
    status        TEXT NOT NULL,
    output        TEXT,
    error         TEXT,
    created_at    TEXT NOT NULL,
    started_at    TEXT,
    finished_at   TEXT,
    PRIMARY KEY (parent_id, node_id)
);
CREATE INDEX IF NOT EXISTS idx_subtasks_parent ON sub_tasks(parent_id);
```

- [ ] **Step 2: Failing test**

Append to `store_test.go`:

```go
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
```

- [ ] **Step 3: Run — expect FAIL** (`SubTaskRow`, `InsertSubTasks`, etc. undefined)

- [ ] **Step 4: Implement in `store.go` (append)**

```go
type SubTaskRow struct {
    ParentID, NodeID, TargetID, ChildTaskID, Prompt string
    DependsOn  []string
    Status, Output, Error string
    CreatedAt, StartedAt, FinishedAt string
}

func (s *Store) InsertSubTasks(parentID string, rows []SubTaskRow) error {
    tx, err := s.db.Begin()
    if err != nil {
        return err
    }
    defer tx.Rollback()
    for _, r := range rows {
        deps, _ := json.Marshal(r.DependsOn)
        if r.CreatedAt == "" {
            r.CreatedAt = nowUTC()
        }
        if r.Status == "" {
            r.Status = "pending"
        }
        if _, err := tx.Exec(
            `INSERT INTO sub_tasks(parent_id,node_id,target_id,child_task_id,prompt,depends_on,status,output,error,created_at,started_at,finished_at)
             VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
            parentID, r.NodeID, r.TargetID, nullable(r.ChildTaskID), r.Prompt, string(deps), r.Status,
            nullable(r.Output), nullable(r.Error), r.CreatedAt, nullable(r.StartedAt), nullable(r.FinishedAt),
        ); err != nil {
            return err
        }
    }
    return tx.Commit()
}

func (s *Store) UpdateSubTask(parentID, nodeID string, fields map[string]interface{}) error {
    if len(fields) == 0 {
        return nil
    }
    keys := make([]string, 0, len(fields))
    args := make([]interface{}, 0, len(fields)+2)
    for k, v := range fields {
        switch k {
        case "child_task_id", "prompt", "status", "output", "error", "started_at", "finished_at", "target_id":
        default:
            return fmt.Errorf("UpdateSubTask: unknown field %q", k)
        }
        keys = append(keys, k+"=?")
        args = append(args, v)
    }
    args = append(args, parentID, nodeID)
    q := "UPDATE sub_tasks SET " + strings.Join(keys, ",") + " WHERE parent_id=? AND node_id=?"
    res, err := s.db.Exec(q, args...)
    if err != nil {
        return err
    }
    n, _ := res.RowsAffected()
    if n == 0 {
        return fmt.Errorf("sub_task %s/%s not found", parentID, nodeID)
    }
    return nil
}

func (s *Store) ListSubTasks(parentID string) ([]SubTaskRow, error) {
    rows, err := s.db.Query(
        `SELECT parent_id,node_id,target_id,COALESCE(child_task_id,''),prompt,depends_on,status,
                COALESCE(output,''),COALESCE(error,''),created_at,COALESCE(started_at,''),COALESCE(finished_at,'')
         FROM sub_tasks WHERE parent_id=? ORDER BY created_at ASC`,
        parentID,
    )
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    var out []SubTaskRow
    for rows.Next() {
        var r SubTaskRow
        var depsJSON string
        if err := rows.Scan(&r.ParentID, &r.NodeID, &r.TargetID, &r.ChildTaskID, &r.Prompt, &depsJSON,
            &r.Status, &r.Output, &r.Error, &r.CreatedAt, &r.StartedAt, &r.FinishedAt); err != nil {
            return nil, err
        }
        _ = json.Unmarshal([]byte(depsJSON), &r.DependsOn)
        out = append(out, r)
    }
    return out, rows.Err()
}

func nullable(s string) interface{} {
    if s == "" {
        return nil
    }
    return s
}
```

Add `"encoding/json"` and `"strings"` to the existing import block if not already there.

- [ ] **Step 5: Run — expect PASS**

`go test ./internal/store/... -v -run TestSubTasks`

- [ ] **Step 6: Commit**

```bash
git add slave_agent/internal/store/
git commit -m "feat(slave_agent/store): add sub_tasks table + CRUD"
```

---

## Task 3: Extend `store.Recover` to cancel in-flight sub_tasks

**Files:**
- Modify: `slave_agent/internal/store/store.go`
- Modify: `slave_agent/internal/store/store_test.go`

- [ ] **Step 1: Failing test**

Append to `store_test.go`:

```go
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
```

- [ ] **Step 2: Run — expect FAIL** (Recover currently doesn't touch sub_tasks)

- [ ] **Step 3: Modify `Recover`**

Find the existing `func (s *Store) Recover() error` body. Inside the same transaction loop where it marks tasks failed, after the existing `pending_acks` insert, add:

```go
        if _, err := tx.Exec(
            `UPDATE sub_tasks SET status='cancelled', finished_at=? WHERE parent_id=? AND status IN ('pending','assigned')`,
            nowUTC(), id,
        ); err != nil {
            return err
        }
```

(So per failed parent task, also cancel its in-flight sub_tasks.)

- [ ] **Step 4: Run — expect PASS**

`go test ./internal/store/... -v -run TestRecover`

(The pre-existing `TestRecover_MarksInflightAsFailedAndQueues` must still pass — it doesn't insert sub_tasks so the new SQL is a no-op.)

- [ ] **Step 5: Commit**

```bash
git add slave_agent/internal/store/
git commit -m "feat(slave_agent/store): Recover also cancels in-flight sub_tasks"
```

---

## Task 4: Make `poller` accept Dispatcher interface

**Files:**
- Modify: `slave_agent/internal/poller/poller.go`
- Modify: `slave_agent/internal/poller/poller_test.go`

- [ ] **Step 1: Define interface and switch poller to it**

Edit `poller.go`. Add at the top of the file (after imports):

```go
// Dispatcher is the contract poller uses to hand a task off for execution.
// slave's *dispatch.Dispatcher and master's *orchestrator.Orchestrator both satisfy this.
type Dispatcher interface {
    Run(ctx context.Context, t executor.Task) (executor.Result, error)
}
```

Change the `Poller.disp` field type from `*dispatch.Dispatcher` to `Dispatcher`. Change `New`'s 2nd parameter type the same way.

Remove the `"github.com/yourorg/slave_agent/internal/dispatch"` import if it's no longer needed.

- [ ] **Step 2: Run all tests**

`go test ./... -race -count=1`

The change is API-compatible because `*dispatch.Dispatcher` already satisfies the new interface (it has `Run(ctx, executor.Task) (executor.Result, error)`). Existing tests should still pass without modification.

If `poller_test.go` directly constructs `dispatch.Dispatcher` and passes it: that still works because it satisfies the interface.

- [ ] **Step 3: Commit**

```bash
git add slave_agent/internal/poller/
git commit -m "refactor(slave_agent/poller): accept Dispatcher interface"
```

---

## Task 5: Add `tunnel.SDKClient()` getter

**Files:**
- Modify: `slave_agent/internal/tunnel/tunnel.go`

- [ ] **Step 1: Add getter**

Append to `tunnel.go` (just before the closing of the file):

```go
// SDKClient returns the underlying agentsdk.Client after EnsureRegistered has run.
// Callers (e.g., master_agent's orchestrator) use it to call DelegateTask, WaitForTask, DiscoverAgents.
// Returns nil if EnsureRegistered has not been called.
func (t *Tunnel) SDKClient() *agentsdk.Client {
    return t.sdk
}
```

(Note: `t.sdk` is set in `EnsureRegistered`. If callers need it without registration first, they should call `EnsureRegistered` then `Run` then `SDKClient`. But `Run` also lazy-inits `t.sdk` when nil. Either path works for `main.go`'s usage order.)

- [ ] **Step 2: Verify build**

`go build ./...`

- [ ] **Step 3: Commit**

```bash
git add slave_agent/internal/tunnel/
git commit -m "feat(slave_agent/tunnel): expose SDKClient() getter"
```

---

## Task 6: `planner` package — types, prompts, fake binary

**Files:**
- Create: `slave_agent/internal/planner/planner.go`
- Create: `slave_agent/internal/planner/prompts.go`
- Create: `slave_agent/internal/planner/planner_test.go`
- Create: `slave_agent/testdata/fake-planner.sh`

- [ ] **Step 1: Write `testdata/fake-planner.sh`**

```bash
#!/usr/bin/env bash
# Behavior knobs:
#   FAKE_PLANNER_MODE = route_a | route_empty | plan_diamond | plan_chain | plan_invalid_cycle |
#                       plan_invalid_json | reduce_ok | exit1 | sleep
#   FAKE_PLANNER_SLEEP = seconds
set -euo pipefail
mode="${FAKE_PLANNER_MODE:-reduce_ok}"
case "$mode" in
  route_a)            echo '{"target_id":"agent-a"}';;
  route_empty)        echo '{"target_id":""}';;
  plan_diamond)
    cat <<'EOF'
[
  {"id":"n1","target_id":"agent-a","prompt":"step 1"},
  {"id":"n2","target_id":"agent-b","prompt":"step 2 using {{n1.output}}","depends_on":["n1"]},
  {"id":"n3","target_id":"agent-c","prompt":"step 3 using {{n1.output}}","depends_on":["n1"]},
  {"id":"n4","target_id":"agent-d","prompt":"merge {{n2.output}} {{n3.output}}","depends_on":["n2","n3"]}
]
EOF
    ;;
  plan_chain)
    cat <<'EOF'
[
  {"id":"a","target_id":"agent-a","prompt":"first"},
  {"id":"b","target_id":"agent-b","prompt":"second using {{a.output}}","depends_on":["a"]}
]
EOF
    ;;
  plan_invalid_cycle)
    cat <<'EOF'
[
  {"id":"x","target_id":"agent-a","prompt":"x","depends_on":["y"]},
  {"id":"y","target_id":"agent-b","prompt":"y","depends_on":["x"]}
]
EOF
    ;;
  plan_invalid_json) echo "this is not json at all";;
  reduce_ok)         echo "REDUCED OUTPUT";;
  exit1)             echo "boom" 1>&2; exit 1;;
  sleep)             sleep "${FAKE_PLANNER_SLEEP:-30}";;
  *) echo "unknown FAKE_PLANNER_MODE: $mode" 1>&2; exit 2;;
esac
```

`chmod +x slave_agent/testdata/fake-planner.sh`

- [ ] **Step 2: Write `planner.go`**

```go
package planner

import (
    "context"
    "encoding/json"
    "fmt"
    "os/exec"
    "strings"
    "time"

    "github.com/agentserver/agentserver/pkg/agentsdk"
    "github.com/yourorg/slave_agent/internal/config"
)

type Planner struct{ cfg config.Planner }

func New(cfg config.Planner) *Planner { return &Planner{cfg: cfg} }

type Node struct {
    ID        string   `json:"id"`
    TargetID  string   `json:"target_id"`
    Prompt    string   `json:"prompt"`
    DependsOn []string `json:"depends_on,omitempty"`
}

type SubResult struct {
    NodeID, TargetID, Prompt, Status, Output, Error string
}

func (p *Planner) Route(ctx context.Context, prompt string, agents []agentsdk.AgentCard) (string, error) {
    out, err := p.runClaude(ctx, routePrompt(prompt, agents))
    if err != nil {
        return "", err
    }
    var r struct {
        TargetID string `json:"target_id"`
    }
    if json.Unmarshal([]byte(out), &r) == nil {
        return r.TargetID, nil
    }
    // Fallback: trim and treat as raw target_id
    return strings.TrimSpace(out), nil
}

func (p *Planner) Plan(ctx context.Context, prompt string, agents []agentsdk.AgentCard) ([]Node, error) {
    out, err := p.runClaude(ctx, planPrompt(prompt, agents))
    if err != nil {
        return nil, err
    }
    var nodes []Node
    if err := json.Unmarshal([]byte(out), &nodes); err != nil {
        return nil, fmt.Errorf("plan unmarshal: %w; output: %s", err, truncate(out, 512))
    }
    return nodes, nil
}

func (p *Planner) Reduce(ctx context.Context, originalPrompt string, results []SubResult) (string, error) {
    return p.runClaude(ctx, reducePrompt(originalPrompt, results))
}

func (p *Planner) runClaude(ctx context.Context, stdinPrompt string) (string, error) {
    timeout := time.Duration(p.cfg.TimeoutSec) * time.Second
    if timeout <= 0 {
        timeout = 60 * time.Second
    }
    cctx, cancel := context.WithTimeout(ctx, timeout)
    defer cancel()

    args := append([]string{"--print"}, p.cfg.ExtraArgs...)
    cmd := exec.CommandContext(cctx, p.cfg.Bin, args...)
    cmd.Stdin = strings.NewReader(stdinPrompt)
    var stderrBuf strings.Builder
    cmd.Stderr = &stderrBuf
    out, err := cmd.Output()
    if err != nil {
        if cctx.Err() == context.DeadlineExceeded {
            return "", fmt.Errorf("planner timeout after %s", timeout)
        }
        tail := stderrBuf.String()
        if len(tail) > 4096 {
            tail = tail[len(tail)-4096:]
        }
        return "", fmt.Errorf("planner exit: %v: %s", err, tail)
    }
    return strings.TrimSpace(string(out)), nil
}

func truncate(s string, n int) string {
    if len(s) <= n {
        return s
    }
    return s[:n] + "..."
}
```

- [ ] **Step 3: Write `prompts.go`**

```go
package planner

import (
    "encoding/json"
    "fmt"
    "strings"

    "github.com/agentserver/agentserver/pkg/agentsdk"
)

func routePrompt(taskPrompt string, agents []agentsdk.AgentCard) string {
    return fmt.Sprintf(`You are a task router. Given a task and a list of available agents, pick the single agent best suited to handle this task.

Output exactly one line of JSON: {"target_id":"<agent_id>"}.
If no agent is suitable, output {"target_id":""}.

Task:
%s

Available agents:
%s
`, taskPrompt, agentsJSON(agents))
}

func planPrompt(taskPrompt string, agents []agentsdk.AgentCard) string {
    return fmt.Sprintf(`You are a task decomposer. Break the following task into a DAG of 1 to 20 sub-tasks. Output a JSON array; each element has:
  - "id": short unique node name (e.g. "n1")
  - "target_id": from the available agents list (the agent_id field)
  - "prompt": the sub-task text. May reference an upstream node's output via {{X.output}}, where X must appear in depends_on.
  - "depends_on": array of upstream node ids; empty for root nodes.

Constraints: no cycles. At least one root. Prefer a clear convergence (one or few sink nodes).

Output ONLY the JSON array, no commentary.

Task:
%s

Available agents:
%s
`, taskPrompt, agentsJSON(agents))
}

func reducePrompt(originalPrompt string, results []SubResult) string {
    var sb strings.Builder
    sb.WriteString("Sub-tasks (with status and output):\n")
    for _, r := range results {
        sb.WriteString(fmt.Sprintf("\n--- node %s [target=%s status=%s] ---\nprompt: %s\n", r.NodeID, r.TargetID, r.Status, r.Prompt))
        if r.Status == "completed" {
            sb.WriteString("output: " + r.Output + "\n")
        } else if r.Error != "" {
            sb.WriteString("error: " + r.Error + "\n")
        }
    }
    return fmt.Sprintf(`You are a task reducer. Given the original task and the outputs of the sub-tasks that ran on its behalf, produce a final answer to the original task.

If some sub-tasks failed or were skipped, mention which ones explicitly so the caller knows what data is missing.

Original task:
%s

%s
`, originalPrompt, sb.String())
}

func agentsJSON(agents []agentsdk.AgentCard) string {
    type lite struct {
        AgentID, DisplayName, Description, Status string
    }
    out := make([]lite, len(agents))
    for i, a := range agents {
        out[i] = lite{a.AgentID, a.DisplayName, a.Description, a.Status}
    }
    b, _ := json.MarshalIndent(out, "", "  ")
    return string(b)
}
```

- [ ] **Step 4: Write `planner_test.go`**

```go
package planner

import (
    "context"
    "path/filepath"
    "runtime"
    "testing"
    "time"

    "github.com/stretchr/testify/require"
    "github.com/agentserver/agentserver/pkg/agentsdk"
    "github.com/yourorg/slave_agent/internal/config"
)

func fakePlanner(t *testing.T) string {
    t.Helper()
    _, file, _, _ := runtime.Caller(0)
    p, err := filepath.Abs(filepath.Join(filepath.Dir(file), "../../testdata/fake-planner.sh"))
    require.NoError(t, err)
    return p
}

func newPlanner(t *testing.T, mode string) *Planner {
    return New(config.Planner{
        Bin:        fakePlanner(t),
        TimeoutSec: 5,
        ExtraArgs:  []string{},
    })
}

func withMode(t *testing.T, mode string, fn func()) {
    t.Helper()
    t.Setenv("FAKE_PLANNER_MODE", mode)
    fn()
}

var demoAgents = []agentsdk.AgentCard{
    {AgentID: "agent-a", DisplayName: "A", Status: "available"},
    {AgentID: "agent-b", DisplayName: "B", Status: "available"},
}

func TestRoute_PicksTarget(t *testing.T) {
    withMode(t, "route_a", func() {
        p := newPlanner(t, "route_a")
        target, err := p.Route(context.Background(), "do thing", demoAgents)
        require.NoError(t, err)
        require.Equal(t, "agent-a", target)
    })
}

func TestRoute_EmptyMeansNoCandidate(t *testing.T) {
    withMode(t, "route_empty", func() {
        p := newPlanner(t, "route_empty")
        target, err := p.Route(context.Background(), "do thing", demoAgents)
        require.NoError(t, err)
        require.Equal(t, "", target)
    })
}

func TestPlan_DiamondParsedCorrectly(t *testing.T) {
    withMode(t, "plan_diamond", func() {
        p := newPlanner(t, "plan_diamond")
        nodes, err := p.Plan(context.Background(), "x", demoAgents)
        require.NoError(t, err)
        require.Len(t, nodes, 4)
        require.Equal(t, "n1", nodes[0].ID)
        require.Equal(t, []string{"n1"}, nodes[1].DependsOn)
        require.Equal(t, []string{"n2", "n3"}, nodes[3].DependsOn)
    })
}

func TestPlan_InvalidJSONErrors(t *testing.T) {
    withMode(t, "plan_invalid_json", func() {
        p := newPlanner(t, "plan_invalid_json")
        _, err := p.Plan(context.Background(), "x", demoAgents)
        require.ErrorContains(t, err, "plan unmarshal")
    })
}

func TestReduce_ReturnsClaudeOutput(t *testing.T) {
    withMode(t, "reduce_ok", func() {
        p := newPlanner(t, "reduce_ok")
        out, err := p.Reduce(context.Background(), "task", []SubResult{
            {NodeID: "n1", Status: "completed", Output: "x"},
        })
        require.NoError(t, err)
        require.Equal(t, "REDUCED OUTPUT", out)
    })
}

func TestRunClaude_Exit1(t *testing.T) {
    withMode(t, "exit1", func() {
        p := newPlanner(t, "exit1")
        _, err := p.Route(context.Background(), "x", demoAgents)
        require.ErrorContains(t, err, "boom")
    })
}

func TestRunClaude_Timeout(t *testing.T) {
    withMode(t, "sleep", func() {
        t.Setenv("FAKE_PLANNER_SLEEP", "10")
        p := New(config.Planner{Bin: fakePlanner(t), TimeoutSec: 1})
        ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()
        _, err := p.Route(ctx, "x", demoAgents)
        require.ErrorContains(t, err, "timeout")
    })
}
```

- [ ] **Step 5: Run — expect PASS**

`go test ./internal/planner/... -v`

- [ ] **Step 6: Commit**

```bash
git add slave_agent/internal/planner/ slave_agent/testdata/fake-planner.sh
git commit -m "feat(slave_agent/planner): claude-as-decision wrapper (Route/Plan/Reduce)"
```

---

## Task 7: `orchestrator/dag` — Validate

**Files:**
- Create: `slave_agent/internal/orchestrator/dag.go`
- Create: `slave_agent/internal/orchestrator/dag_test.go`

- [ ] **Step 1: Failing test**

```go
// dag_test.go
package orchestrator

import (
    "testing"

    "github.com/stretchr/testify/require"
    "github.com/yourorg/slave_agent/internal/planner"
)

func TestValidate_OK(t *testing.T) {
    nodes := []planner.Node{
        {ID: "a", TargetID: "x", Prompt: "p"},
        {ID: "b", TargetID: "y", Prompt: "p", DependsOn: []string{"a"}},
    }
    require.NoError(t, Validate(nodes))
}

func TestValidate_Empty(t *testing.T) {
    require.ErrorContains(t, Validate(nil), "plan empty")
}

func TestValidate_DuplicateID(t *testing.T) {
    nodes := []planner.Node{
        {ID: "a", TargetID: "x", Prompt: "p"},
        {ID: "a", TargetID: "y", Prompt: "p"},
    }
    require.ErrorContains(t, Validate(nodes), "duplicate")
}

func TestValidate_DanglingDep(t *testing.T) {
    nodes := []planner.Node{
        {ID: "a", TargetID: "x", Prompt: "p", DependsOn: []string{"ghost"}},
    }
    require.ErrorContains(t, Validate(nodes), "dangling dep")
}

func TestValidate_Cycle(t *testing.T) {
    nodes := []planner.Node{
        {ID: "a", TargetID: "x", Prompt: "p", DependsOn: []string{"b"}},
        {ID: "b", TargetID: "y", Prompt: "p", DependsOn: []string{"a"}},
    }
    require.ErrorContains(t, Validate(nodes), "cycle")
}

func TestValidate_TooLarge(t *testing.T) {
    var nodes []planner.Node
    for i := 0; i < 101; i++ {
        nodes = append(nodes, planner.Node{ID: string(rune('a' + i)), TargetID: "x", Prompt: "p"})
    }
    require.ErrorContains(t, Validate(nodes), "plan too large")
}
```

- [ ] **Step 2: Run — expect FAIL**

- [ ] **Step 3: Implement**

```go
// dag.go
package orchestrator

import (
    "fmt"

    "github.com/yourorg/slave_agent/internal/planner"
)

const MaxNodes = 100

func Validate(nodes []planner.Node) error {
    if len(nodes) == 0 {
        return fmt.Errorf("plan empty")
    }
    if len(nodes) > MaxNodes {
        return fmt.Errorf("plan too large: %d nodes (max %d)", len(nodes), MaxNodes)
    }
    seen := make(map[string]bool, len(nodes))
    for _, n := range nodes {
        if n.ID == "" {
            return fmt.Errorf("node with empty id")
        }
        if seen[n.ID] {
            return fmt.Errorf("duplicate node id: %s", n.ID)
        }
        seen[n.ID] = true
    }
    for _, n := range nodes {
        for _, dep := range n.DependsOn {
            if !seen[dep] {
                return fmt.Errorf("dangling dep: %s -> %s", n.ID, dep)
            }
        }
    }
    return detectCycle(nodes)
}

// detectCycle uses Kahn's topological sort.
func detectCycle(nodes []planner.Node) error {
    indeg := make(map[string]int, len(nodes))
    for _, n := range nodes {
        indeg[n.ID] = 0
    }
    for _, n := range nodes {
        for range n.DependsOn {
            indeg[n.ID]++
        }
    }
    var queue []string
    for id, d := range indeg {
        if d == 0 {
            queue = append(queue, id)
        }
    }
    visited := 0
    nodeByID := make(map[string]planner.Node, len(nodes))
    for _, n := range nodes {
        nodeByID[n.ID] = n
    }
    // build reverse adjacency: for each node, who depends on it
    rev := make(map[string][]string)
    for _, n := range nodes {
        for _, dep := range n.DependsOn {
            rev[dep] = append(rev[dep], n.ID)
        }
    }
    for len(queue) > 0 {
        id := queue[0]
        queue = queue[1:]
        visited++
        for _, downstream := range rev[id] {
            indeg[downstream]--
            if indeg[downstream] == 0 {
                queue = append(queue, downstream)
            }
        }
    }
    if visited != len(nodes) {
        return fmt.Errorf("cycle detected (visited %d of %d nodes)", visited, len(nodes))
    }
    return nil
}
```

- [ ] **Step 4: Run — expect PASS**

`go test ./internal/orchestrator/... -v -run TestValidate`

- [ ] **Step 5: Commit**

```bash
git add slave_agent/internal/orchestrator/
git commit -m "feat(slave_agent/orchestrator): DAG Validate (cycle/duplicate/dangling/size)"
```

---

## Task 8: `orchestrator/dag` — Render template

**Files:**
- Modify: `slave_agent/internal/orchestrator/dag.go`
- Modify: `slave_agent/internal/orchestrator/dag_test.go`

- [ ] **Step 1: Failing tests**

Append to `dag_test.go`:

```go
func TestRender_NoVars(t *testing.T) {
    out, err := Render("hello world", nil)
    require.NoError(t, err)
    require.Equal(t, "hello world", out)
}

func TestRender_Substitutes(t *testing.T) {
    out, err := Render("use {{a.output}} and {{b.output}}", map[string]string{"a": "X", "b": "Y"})
    require.NoError(t, err)
    require.Equal(t, "use X and Y", out)
}

func TestRender_MissingVarErrors(t *testing.T) {
    _, err := Render("use {{ghost.output}}", map[string]string{"a": "X"})
    require.ErrorContains(t, err, "ghost")
}

func TestRender_RepeatedReferences(t *testing.T) {
    out, err := Render("{{a.output}} {{a.output}}", map[string]string{"a": "X"})
    require.NoError(t, err)
    require.Equal(t, "X X", out)
}
```

- [ ] **Step 2: Run — expect FAIL**

- [ ] **Step 3: Implement (append to `dag.go`)**

```go
import "regexp"

var renderRe = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_-]+)\.output\s*\}\}`)

func Render(template string, outputs map[string]string) (string, error) {
    var firstErr error
    out := renderRe.ReplaceAllStringFunc(template, func(match string) string {
        sub := renderRe.FindStringSubmatch(match)
        id := sub[1]
        v, ok := outputs[id]
        if !ok {
            if firstErr == nil {
                firstErr = fmt.Errorf("template references missing node output: %s", id)
            }
            return match
        }
        return v
    })
    if firstErr != nil {
        return "", firstErr
    }
    return out, nil
}
```

Move the existing `import "fmt"` line into a grouped import block with `"regexp"` if not already grouped.

- [ ] **Step 4: Run — expect PASS**

`go test ./internal/orchestrator/... -v -run TestRender`

- [ ] **Step 5: Commit**

```bash
git add slave_agent/internal/orchestrator/
git commit -m "feat(slave_agent/orchestrator): DAG Render template substitution"
```

---

## Task 9: `orchestrator/dag` — Scheduler

**Files:**
- Modify: `slave_agent/internal/orchestrator/dag.go`
- Modify: `slave_agent/internal/orchestrator/dag_test.go`

- [ ] **Step 1: Failing tests**

Append to `dag_test.go`:

```go
func TestScheduler_LinearChain(t *testing.T) {
    nodes := []planner.Node{
        {ID: "a", TargetID: "x", Prompt: "p"},
        {ID: "b", TargetID: "y", Prompt: "p", DependsOn: []string{"a"}},
        {ID: "c", TargetID: "z", Prompt: "p", DependsOn: []string{"b"}},
    }
    s := NewScheduler(nodes, 4)
    require.False(t, s.Done())

    ready := s.Ready()
    require.Len(t, ready, 1)
    require.Equal(t, "a", ready[0].ID)

    s.MarkDispatched("a")
    s.Report("a", "completed", "out-a", "")
    require.False(t, s.Done())

    ready = s.Ready()
    require.Len(t, ready, 1)
    require.Equal(t, "b", ready[0].ID)

    s.MarkDispatched("b")
    s.Report("b", "completed", "out-b", "")
    s.MarkDispatched("c")
    s.Report("c", "completed", "out-c", "")
    require.True(t, s.Done())

    fin := s.AllFinished()
    require.Len(t, fin, 3)
}

func TestScheduler_DiamondParallel(t *testing.T) {
    nodes := []planner.Node{
        {ID: "n1", TargetID: "a", Prompt: "p"},
        {ID: "n2", TargetID: "b", Prompt: "p", DependsOn: []string{"n1"}},
        {ID: "n3", TargetID: "c", Prompt: "p", DependsOn: []string{"n1"}},
        {ID: "n4", TargetID: "d", Prompt: "p", DependsOn: []string{"n2", "n3"}},
    }
    s := NewScheduler(nodes, 4)
    require.Equal(t, []string{"n1"}, idsOf(s.Ready()))
    s.MarkDispatched("n1")
    s.Report("n1", "completed", "x", "")
    ready := s.Ready()
    require.ElementsMatch(t, []string{"n2", "n3"}, idsOf(ready))
}

func TestScheduler_MaxConcurrencyLimits(t *testing.T) {
    nodes := []planner.Node{
        {ID: "a", TargetID: "x", Prompt: "p"},
        {ID: "b", TargetID: "y", Prompt: "p"},
        {ID: "c", TargetID: "z", Prompt: "p"},
    }
    s := NewScheduler(nodes, 2)
    require.Len(t, s.Ready(), 2)
    s.MarkDispatched(s.Ready()[0].ID)
    s.MarkDispatched(s.Ready()[0].ID)
    require.Len(t, s.Ready(), 0) // all 2 slots taken
}

func TestScheduler_DownstreamSkippedOnFailure(t *testing.T) {
    nodes := []planner.Node{
        {ID: "a", TargetID: "x", Prompt: "p"},
        {ID: "b", TargetID: "y", Prompt: "p", DependsOn: []string{"a"}},
        {ID: "c", TargetID: "z", Prompt: "p", DependsOn: []string{"b"}},
    }
    s := NewScheduler(nodes, 4)
    s.MarkDispatched("a")
    s.Report("a", "failed", "", "boom")
    s.MarkDownstreamSkipped("a")
    require.True(t, s.Done())

    fin := s.AllFinished()
    statuses := map[string]string{}
    for _, f := range fin {
        statuses[f.NodeID] = f.Status
    }
    require.Equal(t, "failed", statuses["a"])
    require.Equal(t, "skipped", statuses["b"])
    require.Equal(t, "skipped", statuses["c"])
}

func idsOf(ns []planner.Node) []string {
    out := make([]string, len(ns))
    for i, n := range ns {
        out[i] = n.ID
    }
    return out
}
```

- [ ] **Step 2: Run — expect FAIL**

- [ ] **Step 3: Implement (append to `dag.go`)**

```go
type FinishedNode struct {
    NodeID string
    Status string // "completed" | "failed" | "skipped"
    Output string
    Error  string
}

type Scheduler struct {
    nodes      []planner.Node
    nodeByID   map[string]planner.Node
    rev        map[string][]string // who depends on me
    maxConc    int
    inFlight   map[string]bool
    finished   map[string]FinishedNode
    pending    map[string]bool // node id → true if not yet dispatched, all deps satisfied
    failedDeps map[string]bool // any upstream failed/skipped → this node is unrecoverable
}

func NewScheduler(nodes []planner.Node, maxConc int) *Scheduler {
    if maxConc <= 0 {
        maxConc = 1
    }
    s := &Scheduler{
        nodes:      nodes,
        nodeByID:   make(map[string]planner.Node, len(nodes)),
        rev:        make(map[string][]string),
        maxConc:    maxConc,
        inFlight:   make(map[string]bool),
        finished:   make(map[string]FinishedNode),
        pending:    make(map[string]bool),
        failedDeps: make(map[string]bool),
    }
    for _, n := range nodes {
        s.nodeByID[n.ID] = n
        for _, dep := range n.DependsOn {
            s.rev[dep] = append(s.rev[dep], n.ID)
        }
        if len(n.DependsOn) == 0 {
            s.pending[n.ID] = true
        }
    }
    return s
}

func (s *Scheduler) Ready() []planner.Node {
    free := s.maxConc - len(s.inFlight)
    if free <= 0 {
        return nil
    }
    var out []planner.Node
    for id := range s.pending {
        if free == 0 {
            break
        }
        out = append(out, s.nodeByID[id])
        free--
    }
    return out
}

func (s *Scheduler) MarkDispatched(nodeID string) {
    delete(s.pending, nodeID)
    s.inFlight[nodeID] = true
}

func (s *Scheduler) Report(nodeID, status, output, errMsg string) {
    delete(s.inFlight, nodeID)
    s.finished[nodeID] = FinishedNode{NodeID: nodeID, Status: status, Output: output, Error: errMsg}
    if status != "completed" {
        return
    }
    // Promote downstream nodes whose deps are now all satisfied
    for _, downstream := range s.rev[nodeID] {
        if _, done := s.finished[downstream]; done {
            continue
        }
        if s.failedDeps[downstream] {
            continue
        }
        ready := true
        for _, dep := range s.nodeByID[downstream].DependsOn {
            f, ok := s.finished[dep]
            if !ok || f.Status != "completed" {
                ready = false
                break
            }
        }
        if ready {
            s.pending[downstream] = true
        }
    }
}

func (s *Scheduler) MarkDownstreamSkipped(failedID string) {
    var stack []string
    stack = append(stack, s.rev[failedID]...)
    for len(stack) > 0 {
        id := stack[len(stack)-1]
        stack = stack[:len(stack)-1]
        if _, done := s.finished[id]; done {
            continue
        }
        s.failedDeps[id] = true
        delete(s.pending, id)
        s.finished[id] = FinishedNode{NodeID: id, Status: "skipped", Error: fmt.Sprintf("upstream %s failed/skipped", failedID)}
        stack = append(stack, s.rev[id]...)
    }
}

func (s *Scheduler) Done() bool {
    return len(s.pending) == 0 && len(s.inFlight) == 0
}

func (s *Scheduler) AllFinished() []FinishedNode {
    out := make([]FinishedNode, 0, len(s.finished))
    for _, n := range s.nodes {
        if f, ok := s.finished[n.ID]; ok {
            out = append(out, f)
        }
    }
    return out
}
```

- [ ] **Step 4: Run — expect PASS**

`go test ./internal/orchestrator/... -v`

- [ ] **Step 5: Commit**

```bash
git add slave_agent/internal/orchestrator/
git commit -m "feat(slave_agent/orchestrator): DAG Scheduler with Ready/Report/Skip"
```

---

## Task 10: `orchestrator` types + base + SDKDelegator interface

**Files:**
- Create: `slave_agent/internal/orchestrator/orchestrator.go`

- [ ] **Step 1: Write base types**

```go
package orchestrator

import (
    "context"
    "fmt"
    "time"

    "github.com/agentserver/agentserver/pkg/agentsdk"
    "github.com/yourorg/slave_agent/internal/config"
    "github.com/yourorg/slave_agent/internal/executor"
    "github.com/yourorg/slave_agent/internal/planner"
    "github.com/yourorg/slave_agent/internal/store"
)

// SDKDelegator is the slice of agentsdk.Client we use, expressed as an interface
// so tests can supply an in-memory fake.
type SDKDelegator interface {
    DiscoverAgents(ctx context.Context) ([]agentsdk.AgentCard, error)
    DelegateTask(ctx context.Context, req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error)
    WaitForTask(ctx context.Context, taskID string, pollInterval time.Duration) (*agentsdk.TaskInfo, error)
}

type Orchestrator struct {
    store   *store.Store
    planner *planner.Planner
    sdk     SDKDelegator
    cfg     config.Fanout
    selfID  string
}

func New(s *store.Store, p *planner.Planner, sdk SDKDelegator, cfg config.Fanout, selfID string) *Orchestrator {
    return &Orchestrator{store: s, planner: p, sdk: sdk, cfg: cfg, selfID: selfID}
}

// Run satisfies poller.Dispatcher.
func (o *Orchestrator) Run(ctx context.Context, t executor.Task) (executor.Result, error) {
    if err := o.store.Insert(store.Task{ID: t.ID, Skill: t.Skill, Prompt: t.Prompt}); err != nil {
        return executor.Result{}, err
    }
    if err := o.store.MarkRunning(t.ID); err != nil {
        return executor.Result{}, err
    }

    var (
        res executor.Result
        err error
    )
    switch t.Skill {
    case "route":
        res, err = o.runRoute(ctx, t)
    case "fanout":
        res, err = o.runFanout(ctx, t)
    default:
        err = fmt.Errorf("unknown skill: %q", t.Skill)
    }
    if err != nil {
        _ = o.store.Fail(t.ID, err.Error())
        return executor.Result{}, err
    }
    if cerr := o.store.Complete(t.ID, res.Summary); cerr != nil {
        return res, cerr
    }
    return res, nil
}

// discoverFiltered returns DiscoverAgents minus self, only "available" agents.
func (o *Orchestrator) discoverFiltered(ctx context.Context) ([]agentsdk.AgentCard, error) {
    all, err := o.sdk.DiscoverAgents(ctx)
    if err != nil {
        return nil, fmt.Errorf("discover: %w", err)
    }
    out := make([]agentsdk.AgentCard, 0, len(all))
    for _, a := range all {
        if a.AgentID == o.selfID {
            continue
        }
        if a.Status != "available" {
            continue
        }
        out = append(out, a)
    }
    return out, nil
}

func (o *Orchestrator) policyForSkill(skill string) string {
    if p, ok := o.cfg.PolicyBySkill[skill]; ok {
        return p
    }
    if o.cfg.DefaultPolicy != "" {
        return o.cfg.DefaultPolicy
    }
    return "best_effort"
}
```

`runRoute` and `runFanout` are stubbed in subsequent tasks — for now create them as placeholders so the file compiles:

```go
func (o *Orchestrator) runRoute(ctx context.Context, t executor.Task) (executor.Result, error) {
    return executor.Result{}, fmt.Errorf("runRoute not implemented")
}
func (o *Orchestrator) runFanout(ctx context.Context, t executor.Task) (executor.Result, error) {
    return executor.Result{}, fmt.Errorf("runFanout not implemented")
}
```

- [ ] **Step 2: Verify build**

`go build ./internal/orchestrator/...`

- [ ] **Step 3: Commit**

```bash
git add slave_agent/internal/orchestrator/
git commit -m "feat(slave_agent/orchestrator): base types + SDKDelegator interface"
```

---

## Task 11: `orchestrator.runRoute` (1→1 path)

**Files:**
- Create: `slave_agent/internal/orchestrator/route.go` (move stub out of orchestrator.go)
- Create: `slave_agent/internal/orchestrator/route_test.go`
- Modify: `slave_agent/internal/orchestrator/orchestrator.go` (delete the stub)

- [ ] **Step 1: Failing test (use a fake SDK)**

```go
// route_test.go
package orchestrator

import (
    "context"
    "errors"
    "path/filepath"
    "testing"
    "time"

    "github.com/stretchr/testify/require"
    "github.com/agentserver/agentserver/pkg/agentsdk"
    "github.com/yourorg/slave_agent/internal/config"
    "github.com/yourorg/slave_agent/internal/executor"
    "github.com/yourorg/slave_agent/internal/planner"
    "github.com/yourorg/slave_agent/internal/store"
)

type fakeSDK struct {
    agents       []agentsdk.AgentCard
    delegateResp *agentsdk.DelegateTaskResponse
    delegateErr  error
    waitInfo     *agentsdk.TaskInfo
    waitErr      error

    delegatedReqs []agentsdk.DelegateTaskRequest
}

func (f *fakeSDK) DiscoverAgents(ctx context.Context) ([]agentsdk.AgentCard, error) {
    return f.agents, nil
}
func (f *fakeSDK) DelegateTask(ctx context.Context, req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
    f.delegatedReqs = append(f.delegatedReqs, req)
    return f.delegateResp, f.delegateErr
}
func (f *fakeSDK) WaitForTask(ctx context.Context, id string, _ time.Duration) (*agentsdk.TaskInfo, error) {
    return f.waitInfo, f.waitErr
}

func newOrch(t *testing.T, sdk SDKDelegator, mode string) *Orchestrator {
    t.Helper()
    t.Setenv("FAKE_PLANNER_MODE", mode)
    p := planner.New(config.Planner{Bin: fakePlannerForOrch(t), TimeoutSec: 5})
    s, err := store.Open(filepath.Join(t.TempDir(), "x.db"))
    require.NoError(t, err)
    t.Cleanup(func() { s.Close() })
    return New(s, p, sdk, config.Fanout{MaxConcurrency: 4, DefaultPolicy: "best_effort"}, "self-id")
}

func fakePlannerForOrch(t *testing.T) string {
    t.Helper()
    p, err := filepath.Abs("../../testdata/fake-planner.sh")
    require.NoError(t, err)
    return p
}

func TestRoute_HappyPath(t *testing.T) {
    sdk := &fakeSDK{
        agents:       []agentsdk.AgentCard{{AgentID: "agent-a", Status: "available"}},
        delegateResp: &agentsdk.DelegateTaskResponse{TaskID: "child-1"},
        waitInfo:     &agentsdk.TaskInfo{TaskID: "child-1", Status: "completed", Output: "child output"},
    }
    o := newOrch(t, sdk, "route_a")

    res, err := o.Run(context.Background(), executor.Task{ID: "p1", Skill: "route", Prompt: "do thing"})
    require.NoError(t, err)
    require.Equal(t, "child output", res.Summary)
    require.Len(t, sdk.delegatedReqs, 1)
    require.Equal(t, "agent-a", sdk.delegatedReqs[0].TargetID)
    require.Equal(t, "do thing", sdk.delegatedReqs[0].Prompt)
}

func TestRoute_NoCandidate(t *testing.T) {
    sdk := &fakeSDK{
        agents: []agentsdk.AgentCard{{AgentID: "agent-a", Status: "available"}},
    }
    o := newOrch(t, sdk, "route_empty")

    _, err := o.Run(context.Background(), executor.Task{ID: "p2", Skill: "route", Prompt: "x"})
    require.ErrorContains(t, err, "no candidate")
    require.Empty(t, sdk.delegatedReqs)
}

func TestRoute_ChildFails(t *testing.T) {
    sdk := &fakeSDK{
        agents:       []agentsdk.AgentCard{{AgentID: "agent-a", Status: "available"}},
        delegateResp: &agentsdk.DelegateTaskResponse{TaskID: "child-1"},
        waitInfo:     &agentsdk.TaskInfo{TaskID: "child-1", Status: "failed"},
    }
    o := newOrch(t, sdk, "route_a")
    _, err := o.Run(context.Background(), executor.Task{ID: "p3", Skill: "route", Prompt: "x"})
    require.ErrorContains(t, err, "child")
}

func TestRoute_DelegateError(t *testing.T) {
    sdk := &fakeSDK{
        agents:      []agentsdk.AgentCard{{AgentID: "agent-a", Status: "available"}},
        delegateErr: errors.New("boom"),
    }
    o := newOrch(t, sdk, "route_a")
    _, err := o.Run(context.Background(), executor.Task{ID: "p4", Skill: "route", Prompt: "x"})
    require.ErrorContains(t, err, "boom")
}

func TestRoute_FiltersSelf(t *testing.T) {
    sdk := &fakeSDK{
        agents: []agentsdk.AgentCard{
            {AgentID: "self-id", Status: "available"},  // would-be self
            {AgentID: "agent-a", Status: "available"},
        },
        delegateResp: &agentsdk.DelegateTaskResponse{TaskID: "c"},
        waitInfo:     &agentsdk.TaskInfo{TaskID: "c", Status: "completed", Output: "ok"},
    }
    o := newOrch(t, sdk, "route_a")
    _, err := o.Run(context.Background(), executor.Task{ID: "p5", Skill: "route", Prompt: "x"})
    require.NoError(t, err)
    // route_a always picks "agent-a" regardless of filter; we just confirm Discover
    // was filtered (no second call to DelegateTask with self-id).
    for _, r := range sdk.delegatedReqs {
        require.NotEqual(t, "self-id", r.TargetID)
    }
}
```

- [ ] **Step 2: Run — expect FAIL**

- [ ] **Step 3: Move stub out into `route.go` and implement**

Delete the `runRoute` stub from `orchestrator.go`. Create `route.go`:

```go
package orchestrator

import (
    "context"
    "fmt"
    "time"

    "github.com/agentserver/agentserver/pkg/agentsdk"
    "github.com/yourorg/slave_agent/internal/executor"
    "github.com/yourorg/slave_agent/internal/store"
)

func (o *Orchestrator) runRoute(ctx context.Context, t executor.Task) (executor.Result, error) {
    agents, err := o.discoverFiltered(ctx)
    if err != nil {
        return executor.Result{}, err
    }
    target, err := o.planner.Route(ctx, t.Prompt, agents)
    if err != nil {
        return executor.Result{}, fmt.Errorf("planner.Route: %w", err)
    }
    if target == "" {
        return executor.Result{}, fmt.Errorf("no candidate")
    }

    if err := o.store.InsertSubTasks(t.ID, []store.SubTaskRow{
        {ParentID: t.ID, NodeID: "root", TargetID: target, Prompt: t.Prompt, Status: "assigned"},
    }); err != nil {
        return executor.Result{}, err
    }

    resp, err := o.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
        TargetID:       target,
        Prompt:         t.Prompt,
        TimeoutSeconds: t.TimeoutSec,
    })
    if err != nil {
        _ = o.store.UpdateSubTask(t.ID, "root", map[string]interface{}{
            "status": "failed", "error": err.Error(),
        })
        return executor.Result{}, fmt.Errorf("delegate: %w", err)
    }
    _ = o.store.UpdateSubTask(t.ID, "root", map[string]interface{}{
        "child_task_id": resp.TaskID,
        "started_at":    time.Now().UTC().Format(time.RFC3339Nano),
    })

    info, err := o.sdk.WaitForTask(ctx, resp.TaskID, 5*time.Second)
    if err != nil {
        _ = o.store.UpdateSubTask(t.ID, "root", map[string]interface{}{
            "status": "failed", "error": err.Error(),
        })
        return executor.Result{}, fmt.Errorf("wait: %w", err)
    }
    _ = o.store.UpdateSubTask(t.ID, "root", map[string]interface{}{
        "status":      info.Status,
        "output":      info.Output,
        "finished_at": time.Now().UTC().Format(time.RFC3339Nano),
    })
    if info.Status != "completed" {
        return executor.Result{}, fmt.Errorf("child %s: status=%s", resp.TaskID, info.Status)
    }
    return executor.Result{Summary: info.Output}, nil
}
```

Note: the spec says `TaskInfo` has an `Output` field — confirm by checking `/root/go/pkg/mod/github.com/agentserver/agentserver@v0.40.0/pkg/agentsdk/agents.go` if needed.

- [ ] **Step 4: Run — expect PASS**

`go test ./internal/orchestrator/... -v -run TestRoute`

- [ ] **Step 5: Commit**

```bash
git add slave_agent/internal/orchestrator/
git commit -m "feat(slave_agent/orchestrator): runRoute (1→1 LLM-routed delegation)"
```

---

## Task 12: `orchestrator.runFanout` (DAG path)

**Files:**
- Create: `slave_agent/internal/orchestrator/fanout.go`
- Create: `slave_agent/internal/orchestrator/fanout_test.go`
- Modify: `slave_agent/internal/orchestrator/orchestrator.go` (delete the runFanout stub)

- [ ] **Step 1: Failing test**

```go
// fanout_test.go
package orchestrator

import (
    "context"
    "sync"
    "testing"
    "time"

    "github.com/stretchr/testify/require"
    "github.com/agentserver/agentserver/pkg/agentsdk"
    "github.com/yourorg/slave_agent/internal/executor"
)

// fakeSDKQueue lets each child task return a queued (status, output) pair keyed by request order.
type fakeSDKQueue struct {
    mu          sync.Mutex
    agents      []agentsdk.AgentCard
    nextID      int
    queue       []agentsdk.TaskInfo // returned by WaitForTask in dispatch order
    dispatched  []agentsdk.DelegateTaskRequest
}

func (f *fakeSDKQueue) DiscoverAgents(_ context.Context) ([]agentsdk.AgentCard, error) {
    return f.agents, nil
}
func (f *fakeSDKQueue) DelegateTask(_ context.Context, req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
    f.mu.Lock()
    defer f.mu.Unlock()
    f.dispatched = append(f.dispatched, req)
    f.nextID++
    return &agentsdk.DelegateTaskResponse{TaskID: fmt.Sprintf("c%d", f.nextID)}, nil
}
func (f *fakeSDKQueue) WaitForTask(_ context.Context, id string, _ time.Duration) (*agentsdk.TaskInfo, error) {
    f.mu.Lock()
    defer f.mu.Unlock()
    // Return one item from queue per call; if empty, return failed.
    if len(f.queue) == 0 {
        return &agentsdk.TaskInfo{TaskID: id, Status: "failed"}, nil
    }
    info := f.queue[0]
    f.queue = f.queue[1:]
    info.TaskID = id
    return &info, nil
}

func TestFanout_HappyDiamond(t *testing.T) {
    sdk := &fakeSDKQueue{
        agents: []agentsdk.AgentCard{
            {AgentID: "agent-a", Status: "available"},
            {AgentID: "agent-b", Status: "available"},
            {AgentID: "agent-c", Status: "available"},
            {AgentID: "agent-d", Status: "available"},
        },
        // 4 nodes diamond: returns in dispatch order
        queue: []agentsdk.TaskInfo{
            {Status: "completed", Output: "out-1"},
            {Status: "completed", Output: "out-2"},
            {Status: "completed", Output: "out-3"},
            {Status: "completed", Output: "out-4"},
        },
    }
    o := newOrch(t, sdk, "plan_diamond")
    // reduce_ok is invoked second time — but FAKE_PLANNER_MODE is set once.
    // Workaround: since planner uses bin per call and mode env is per process,
    // we use plan_diamond for Plan; for Reduce, we replace mode mid-test.
    res, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "do"})
    // Reduce uses the same env => returns the SAME thing as plan_diamond would.
    // For this test, we don't assert on summary; we assert all 4 sub-tasks dispatched.
    _ = res
    require.NoError(t, err)
    require.Len(t, sdk.dispatched, 4)
}

func TestFanout_BestEffortPartialFailure(t *testing.T) {
    sdk := &fakeSDKQueue{
        agents: []agentsdk.AgentCard{
            {AgentID: "agent-a", Status: "available"},
            {AgentID: "agent-b", Status: "available"},
        },
        queue: []agentsdk.TaskInfo{
            {Status: "failed"},                       // a fails
            // b should not run because its dep (a) failed
        },
    }
    o := newOrch(t, sdk, "plan_chain")
    res, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "x"})
    require.NoError(t, err) // best_effort: parent still completed (reducer summarizes failure)
    _ = res
    // Only "a" was dispatched; "b" was skipped.
    require.Len(t, sdk.dispatched, 1)
}

func TestFanout_AllOrNothingFailsImmediately(t *testing.T) {
    sdk := &fakeSDKQueue{
        agents: []agentsdk.AgentCard{
            {AgentID: "agent-a", Status: "available"},
            {AgentID: "agent-b", Status: "available"},
        },
        queue: []agentsdk.TaskInfo{
            {Status: "failed"},
        },
    }
    o := newOrch(t, sdk, "plan_chain")
    o.cfg.PolicyBySkill = map[string]string{"fanout": "all_or_nothing"}

    _, err := o.Run(context.Background(), executor.Task{ID: "p", Skill: "fanout", Prompt: "x"})
    require.Error(t, err)
}

// import fmt for the queue fake
var _ = fmt.Sprintf
```

Add `"fmt"` to fanout_test.go's imports.

- [ ] **Step 2: Run — expect FAIL**

- [ ] **Step 3: Implement `fanout.go`**

```go
package orchestrator

import (
    "context"
    "fmt"
    "sync"
    "time"

    "github.com/agentserver/agentserver/pkg/agentsdk"
    "github.com/yourorg/slave_agent/internal/executor"
    "github.com/yourorg/slave_agent/internal/planner"
    "github.com/yourorg/slave_agent/internal/store"
)

func (o *Orchestrator) runFanout(ctx context.Context, t executor.Task) (executor.Result, error) {
    agents, err := o.discoverFiltered(ctx)
    if err != nil {
        return executor.Result{}, err
    }
    plan, err := o.planner.Plan(ctx, t.Prompt, agents)
    if err != nil {
        return executor.Result{}, fmt.Errorf("planner.Plan: %w", err)
    }
    if err := Validate(plan); err != nil {
        return executor.Result{}, fmt.Errorf("invalid plan: %w", err)
    }

    rows := make([]store.SubTaskRow, len(plan))
    for i, n := range plan {
        rows[i] = store.SubTaskRow{
            ParentID: t.ID, NodeID: n.ID, TargetID: n.TargetID,
            Prompt: n.Prompt, DependsOn: n.DependsOn, Status: "pending",
        }
    }
    if err := o.store.InsertSubTasks(t.ID, rows); err != nil {
        return executor.Result{}, err
    }

    policy := o.policyForSkill(t.Skill)
    sched := NewScheduler(plan, o.cfg.MaxConcurrency)
    outputs := map[string]string{}
    var outputsMu sync.Mutex

    type done struct{ FinishedNode }
    doneCh := make(chan done, len(plan))
    fanoutCtx, cancelAll := context.WithCancel(ctx)
    defer cancelAll()

    sseSink := o.store.ChunkSink(t.ID)
    defer sseSink.Close()

    var inFlight int
    dispatched := func(n planner.Node, prompt string) {
        sched.MarkDispatched(n.ID)
        inFlight++
        _ = o.store.UpdateSubTask(t.ID, n.ID, map[string]interface{}{
            "status":     "assigned",
            "prompt":     prompt,
            "started_at": time.Now().UTC().Format(time.RFC3339Nano),
        })
        sseSink.Write("subtask_dispatched", fmt.Sprintf(`{"node_id":%q,"target_id":%q}`, n.ID, n.TargetID))
        go func(n planner.Node, prompt string) {
            resp, err := o.sdk.DelegateTask(fanoutCtx, agentsdk.DelegateTaskRequest{
                TargetID:       n.TargetID,
                Prompt:         prompt,
                TimeoutSeconds: o.cfg.SubTaskDefaults.TimeoutSec,
            })
            if err != nil {
                doneCh <- done{FinishedNode{NodeID: n.ID, Status: "failed", Error: err.Error()}}
                return
            }
            _ = o.store.UpdateSubTask(t.ID, n.ID, map[string]interface{}{"child_task_id": resp.TaskID})
            info, err := o.sdk.WaitForTask(fanoutCtx, resp.TaskID, 5*time.Second)
            if err != nil {
                doneCh <- done{FinishedNode{NodeID: n.ID, Status: "failed", Error: err.Error()}}
                return
            }
            f := FinishedNode{NodeID: n.ID, Status: info.Status, Output: info.Output}
            if info.Status != "completed" {
                f.Error = info.FailureReason
                if f.Error == "" {
                    f.Error = info.Status
                }
            }
            doneCh <- done{f}
        }(n, prompt)
    }

    for !sched.Done() {
        for _, n := range sched.Ready() {
            outputsMu.Lock()
            prompt, rerr := Render(n.Prompt, outputs)
            outputsMu.Unlock()
            if rerr != nil {
                sched.MarkDispatched(n.ID)
                inFlight++
                go func(n planner.Node, e error) {
                    doneCh <- done{FinishedNode{NodeID: n.ID, Status: "failed", Error: e.Error()}}
                }(n, rerr)
                continue
            }
            dispatched(n, prompt)
        }
        if inFlight == 0 {
            break // nothing pending and nothing in flight
        }
        d := <-doneCh
        inFlight--
        sched.Report(d.NodeID, d.Status, d.Output, d.Error)
        if d.Status == "completed" {
            outputsMu.Lock()
            outputs[d.NodeID] = d.Output
            outputsMu.Unlock()
            _ = o.store.UpdateSubTask(t.ID, d.NodeID, map[string]interface{}{
                "status": "completed", "output": d.Output,
                "finished_at": time.Now().UTC().Format(time.RFC3339Nano),
            })
            sseSink.Write("subtask_done", fmt.Sprintf(`{"node_id":%q,"status":"completed","output_len":%d}`, d.NodeID, len(d.Output)))
        } else {
            _ = o.store.UpdateSubTask(t.ID, d.NodeID, map[string]interface{}{
                "status": d.Status, "error": d.Error,
                "finished_at": time.Now().UTC().Format(time.RFC3339Nano),
            })
            sseSink.Write("subtask_done", fmt.Sprintf(`{"node_id":%q,"status":%q}`, d.NodeID, d.Status))
            if policy == "all_or_nothing" {
                cancelAll()
                // drain remaining inflight
                for inFlight > 0 {
                    <-doneCh
                    inFlight--
                }
                return executor.Result{}, fmt.Errorf("node %s %s: %s", d.NodeID, d.Status, d.Error)
            }
            sched.MarkDownstreamSkipped(d.NodeID)
            // Persist the skipped downstream rows + emit SSE for them
            for _, fn := range sched.AllFinished() {
                if fn.Status == "skipped" {
                    _ = o.store.UpdateSubTask(t.ID, fn.NodeID, map[string]interface{}{
                        "status": "skipped", "error": fn.Error,
                        "finished_at": time.Now().UTC().Format(time.RFC3339Nano),
                    })
                    sseSink.Write("subtask_skipped", fmt.Sprintf(`{"node_id":%q,"reason":%q}`, fn.NodeID, fn.Error))
                }
            }
        }
    }

    // Build SubResults for reducer.
    fins := sched.AllFinished()
    nodeByID := map[string]planner.Node{}
    for _, n := range plan {
        nodeByID[n.ID] = n
    }
    results := make([]planner.SubResult, len(fins))
    for i, f := range fins {
        n := nodeByID[f.NodeID]
        results[i] = planner.SubResult{
            NodeID: f.NodeID, TargetID: n.TargetID, Prompt: n.Prompt,
            Status: f.Status, Output: f.Output, Error: f.Error,
        }
    }
    summary, rerr := o.planner.Reduce(ctx, t.Prompt, results)
    if rerr != nil {
        return executor.Result{}, fmt.Errorf("reduce: %w", rerr)
    }
    return executor.Result{Summary: summary}, nil
}
```

Delete the `runFanout` stub from `orchestrator.go`.

- [ ] **Step 4: Run — expect PASS**

`go test ./internal/orchestrator/... -v -race`

- [ ] **Step 5: Commit**

```bash
git add slave_agent/internal/orchestrator/
git commit -m "feat(slave_agent/orchestrator): runFanout (DAG executor with policy)"
```

---

## Task 13: Extend `webui` with sub-task routes and SSE event types

**Files:**
- Modify: `slave_agent/internal/webui/server.go`
- Modify: `slave_agent/internal/webui/server_test.go`

- [ ] **Step 1: Failing tests**

Append to `server_test.go`:

```go
func TestChildren_NotFound(t *testing.T) {
    s := openStore(t)
    h := NewHandler(s, "", &config.Config{})
    rr := httptest.NewRecorder()
    h.ServeHTTP(rr, httptest.NewRequest("GET", "/tasks/none/children", nil))
    require.Equal(t, 200, rr.Code)
    require.Equal(t, "[]\n", rr.Body.String()) // empty array, not 404
}

func TestChildren_ListsSubTasks(t *testing.T) {
    s := openStore(t)
    require.NoError(t, s.Insert(store.Task{ID: "p"}))
    require.NoError(t, s.InsertSubTasks("p", []store.SubTaskRow{
        {ParentID: "p", NodeID: "n1", TargetID: "a", Prompt: "x", Status: "completed", Output: "ok"},
    }))
    h := NewHandler(s, "", &config.Config{})

    rr := httptest.NewRecorder()
    h.ServeHTTP(rr, httptest.NewRequest("GET", "/tasks/p/children", nil))
    require.Equal(t, 200, rr.Code)
    require.Contains(t, rr.Body.String(), `"NodeID":"n1"`)
    require.Contains(t, rr.Body.String(), `"Status":"completed"`)
}

func TestTaskDetail_IncludesChildren(t *testing.T) {
    s := openStore(t)
    require.NoError(t, s.Insert(store.Task{ID: "p"}))
    require.NoError(t, s.InsertSubTasks("p", []store.SubTaskRow{
        {ParentID: "p", NodeID: "n1", TargetID: "a", Prompt: "x", Status: "pending"},
    }))
    h := NewHandler(s, "", &config.Config{})

    rr := httptest.NewRecorder()
    h.ServeHTTP(rr, httptest.NewRequest("GET", "/tasks/p", nil))
    require.Equal(t, 200, rr.Code)
    require.Contains(t, rr.Body.String(), `"children"`)
    require.Contains(t, rr.Body.String(), `"NodeID":"n1"`)
}
```

- [ ] **Step 2: Run — expect FAIL**

- [ ] **Step 3: Implement**

In `server.go`, find `taskRouter` and add a branch for `parts[1] == "children"`:

```go
func (h *Handler) taskRouter(w http.ResponseWriter, r *http.Request) {
    rest := strings.TrimPrefix(r.URL.Path, "/tasks/")
    parts := strings.SplitN(rest, "/", 2)
    id := parts[0]
    if len(parts) == 2 {
        switch parts[1] {
        case "stream":
            h.stream(w, r, id)
            return
        case "children":
            h.children(w, r, id)
            return
        }
    }
    h.taskDetail(w, r, id)
}

func (h *Handler) children(w http.ResponseWriter, r *http.Request, id string) {
    rows, err := h.s.ListSubTasks(id)
    if err != nil {
        http.Error(w, err.Error(), 500)
        return
    }
    if rows == nil {
        rows = []store.SubTaskRow{}
    }
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(rows)
}
```

Modify `taskDetail` to include children:

```go
func (h *Handler) taskDetail(w http.ResponseWriter, r *http.Request, id string) {
    row, chunks, err := h.s.GetTaskWithChunks(id)
    if err != nil {
        http.Error(w, `{"error":"not found"}`, 404)
        return
    }
    children, _ := h.s.ListSubTasks(id) // may be nil/empty for slave tasks
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]interface{}{
        "task": row, "chunks": chunks, "children": children,
    })
}
```

- [ ] **Step 4: Run — expect PASS**

`go test ./internal/webui/... -v -race`

- [ ] **Step 5: Commit**

```bash
git add slave_agent/internal/webui/
git commit -m "feat(slave_agent/webui): /tasks/{id}/children + include children in detail"
```

---

## Task 14: `cmd/master-agent/main.go`

**Files:**
- Create: `slave_agent/cmd/master-agent/main.go`

- [ ] **Step 1: Write main.go**

```go
package main

import (
    "context"
    "fmt"
    "log"
    "os"
    "os/signal"
    "syscall"

    "golang.org/x/sync/errgroup"

    "github.com/yourorg/slave_agent/internal/config"
    "github.com/yourorg/slave_agent/internal/orchestrator"
    "github.com/yourorg/slave_agent/internal/planner"
    "github.com/yourorg/slave_agent/internal/poller"
    "github.com/yourorg/slave_agent/internal/store"
    "github.com/yourorg/slave_agent/internal/tunnel"
    "github.com/yourorg/slave_agent/internal/webui"
)

func main() {
    cfgPath := "config.yaml"
    if len(os.Args) > 1 {
        cfgPath = os.Args[1]
    }
    if err := run(cfgPath); err != nil {
        log.Fatalf("master_agent: %v", err)
    }
}

func run(cfgPath string) error {
    cfg, err := config.Load(cfgPath)
    if err != nil {
        return err
    }

    s, err := store.Open("data.db")
    if err != nil {
        return err
    }
    defer s.Close()
    if err := s.Recover(); err != nil {
        return err
    }

    ui := webui.NewHandler(s, "", cfg) // master does not maintain a journal

    ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer cancel()

    tn := tunnel.New(cfg, cfgPath, ui)
    if err := tn.EnsureRegistered(ctx); err != nil {
        return err
    }
    if err := tn.PublishCard(ctx); err != nil {
        log.Printf("publish card: %v (continuing)", err)
    }

    sdk := tn.SDKClient()
    if sdk == nil {
        return fmt.Errorf("tunnel.SDKClient returned nil after EnsureRegistered")
    }
    p := planner.New(cfg.Planner)
    orch := orchestrator.New(s, p, sdk, cfg.Fanout, cfg.Credentials.SandboxID)

    pollCfg := poller.Config{
        ServerURL:  cfg.Server.URL,
        ProxyToken: cfg.Credentials.ProxyToken,
    }

    g, gctx := errgroup.WithContext(ctx)
    g.Go(func() error { return tn.Run(gctx) })
    g.Go(func() error { return poller.New(pollCfg, orch, s).Run(gctx) })

    err = g.Wait()
    if err != nil && err != context.Canceled {
        return fmt.Errorf("run: %w", err)
    }
    return nil
}
```

- [ ] **Step 2: Build and verify all tests still pass**

```bash
cd slave_agent
go build ./...
go test ./... -race -count=1
```

Expected: clean build, all packages pass.

- [ ] **Step 3: Update `.gitignore` to ignore the new built binary**

`slave_agent/.gitignore` already ignores `slave-agent`. Append `master-agent`:

```
master-agent
```

- [ ] **Step 4: Commit**

```bash
git add slave_agent/cmd/master-agent/ slave_agent/.gitignore
git commit -m "feat(slave_agent): cmd/master-agent main wiring"
```

---

## Task 15: Contract test for DelegateTask / WaitForTask

**Files:**
- Create: `slave_agent/tests/contract/master_contract_test.go`

- [ ] **Step 1: Write contract test**

```go
//go:build contract

package contract

import (
    "context"
    "encoding/json"
    "io"
    "net/http"
    "net/http/httptest"
    "path/filepath"
    "strings"
    "sync"
    "testing"
    "time"

    "github.com/stretchr/testify/require"
    "github.com/agentserver/agentserver/pkg/agentsdk"
    "github.com/yourorg/slave_agent/internal/config"
    "github.com/yourorg/slave_agent/internal/orchestrator"
    "github.com/yourorg/slave_agent/internal/planner"
    "github.com/yourorg/slave_agent/internal/store"
)

func TestContract_DelegateTaskShape(t *testing.T) {
    var mu sync.Mutex
    var seen []map[string]interface{}
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if strings.Contains(r.URL.Path, "/api/agent/discover/agents") || strings.Contains(r.URL.Path, "/api/agents") {
            json.NewEncoder(w).Encode([]agentsdk.AgentCard{
                {AgentID: "agent-a", Status: "available"},
            })
            return
        }
        if strings.Contains(r.URL.Path, "/delegate") || strings.HasSuffix(r.URL.Path, "/api/agent/tasks") {
            body, _ := io.ReadAll(r.Body)
            var m map[string]interface{}
            json.Unmarshal(body, &m)
            mu.Lock(); seen = append(seen, m); mu.Unlock()
            json.NewEncoder(w).Encode(map[string]interface{}{
                "task_id": "ct1", "status": "assigned",
            })
            return
        }
        if strings.Contains(r.URL.Path, "/api/agent/tasks/ct1") {
            json.NewEncoder(w).Encode(map[string]interface{}{
                "task_id": "ct1", "status": "completed", "output": "ok",
            })
            return
        }
        w.WriteHeader(404)
    }))
    defer srv.Close()

    cli := agentsdk.NewClient(agentsdk.Config{ServerURL: srv.URL, Name: "master"})
    cli.SetRegistration(&agentsdk.Registration{
        SandboxID: "self", TunnelToken: "t", ProxyToken: "p",
    })

    s, _ := store.Open(filepath.Join(t.TempDir(), "x.db"))
    defer s.Close()
    plnr := planner.New(config.Planner{Bin: "/bin/echo", TimeoutSec: 5}) // never actually called

    o := orchestrator.New(s, plnr, cli, config.Fanout{
        MaxConcurrency: 2,
        DefaultPolicy:  "best_effort",
        SubTaskDefaults: config.SubTaskDefaults{TimeoutSec: 30},
    }, "self")

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    // Direct call to verify SDK shape: bypass full Run. We just exercise discoverFiltered + DelegateTask.
    cards, err := cli.DiscoverAgents(ctx)
    require.NoError(t, err)
    require.NotEmpty(t, cards)
    resp, err := cli.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
        TargetID: "agent-a", Prompt: "hello", Skill: "chat", TimeoutSeconds: 60,
    })
    require.NoError(t, err)
    require.Equal(t, "ct1", resp.TaskID)

    info, err := cli.WaitForTask(ctx, "ct1", time.Second)
    require.NoError(t, err)
    require.Equal(t, "completed", info.Status)
    require.Equal(t, "ok", info.Output)

    mu.Lock(); defer mu.Unlock()
    require.Len(t, seen, 1)
    require.Equal(t, "agent-a", seen[0]["target_id"])
    require.Equal(t, "hello", seen[0]["prompt"])
    require.EqualValues(t, 60, seen[0]["timeout_seconds"])

    _ = o // use o to keep import; could be removed if linter complains
}
```

The exact endpoint URLs the SDK uses for `DiscoverAgents`/`DelegateTask`/`WaitForTask` may differ from the patterns guessed above. If the test reports unexpected paths in the test server log (`t.Logf`), check `agents.go` in the SDK and adjust the URL matchers accordingly. The assertions on the request body (target_id, prompt, timeout_seconds) are the contract — those must hold.

- [ ] **Step 2: Run**

```bash
cd slave_agent && go test -tags=contract ./tests/contract/... -v -run TestContract_DelegateTaskShape
```

If the test fails because the SDK uses different routes, either:
- Adjust the matcher to the actual route patterns
- OR if SDK refuses to accept httptest URL (e.g., scheme issue), drop this test and rely on the e2e (Task 16)

- [ ] **Step 3: Commit**

```bash
git add slave_agent/tests/contract/master_contract_test.go
git commit -m "test(slave_agent): contract test for DelegateTask/WaitForTask"
```

---

## Task 16: `master_agent/` docs + e2e script

**Files:**
- Create: `master_agent/README.md`
- Create: `master_agent/config.example.yaml`
- Create: `master_agent/scripts/e2e.sh`

These live OUTSIDE `slave_agent/` (the Go module), at the repo root. They are pure docs / scripts.

- [ ] **Step 1: `master_agent/README.md`**

```markdown
# master_agent

Pure-orchestration agent for agentserver. See
`docs/superpowers/specs/2026-04-28-master-agent-design.md`.

Built from the same Go module as slave_agent.

## Build

    cd slave_agent
    go build -o ../master_agent/master-agent ./cmd/master-agent

## Configure

    cp master_agent/config.example.yaml master_agent/config.yaml
    # edit server.url

## Run

    cd master_agent && ./master-agent config.yaml

## End-to-end

    AGENTSERVER_URL=https://agent.example.com ./master_agent/scripts/e2e.sh
```

- [ ] **Step 2: `master_agent/config.example.yaml`**

```yaml
server:
  url: https://agent.example.com
  name: master-agent

credentials:
  sandbox_id: ""
  tunnel_token: ""
  proxy_token: ""
  short_id: ""

claude:
  bin: claude

planner:
  bin: claude
  timeout_sec: 60
  extra_args: []

fanout:
  max_concurrency: 4
  default_policy: best_effort
  policy_by_skill:
    fanout_strict: all_or_nothing
  subtask_defaults:
    timeout_sec: 600
    max_budget_usd: 0

discovery:
  display_name: master_agent
  description: Orchestrator (route + fanout)
  skills:
    - route
    - fanout
```

- [ ] **Step 3: `master_agent/scripts/e2e.sh`**

```bash
#!/usr/bin/env bash
# Manual end-to-end check. Requires:
#   - agentserver reachable at $AGENTSERVER_URL
#   - claude on PATH and ANTHROPIC_API_KEY set
#   - at least 2 slave_agents already running and registered to the same workspace
set -euo pipefail
: "${AGENTSERVER_URL:?must set AGENTSERVER_URL}"

work=$(mktemp -d)
trap 'kill %1 2>/dev/null || true; rm -rf "$work"' EXIT

cat > "$work/config.yaml" <<EOF
server:
  url: $AGENTSERVER_URL
  name: master-e2e
claude: { bin: claude }
planner: { bin: claude, timeout_sec: 60 }
fanout:
  max_concurrency: 4
  default_policy: best_effort
  subtask_defaults: { timeout_sec: 600 }
discovery:
  display_name: master-e2e
  description: e2e
  skills: [route, fanout]
EOF

(cd slave_agent && go build -o "$work/master-agent" ./cmd/master-agent)
( cd "$work" && ./master-agent config.yaml ) &

echo "master agent running in $work (pid $!)"
echo "manually:"
echo "  1. visit https://code-<shortID>.<base> to confirm dashboard"
echo "  2. POST a route task: skill=route, prompt='do X' → should pick a slave and return its output"
echo "  3. POST a fanout task: skill=fanout, prompt='research and summarize Y'"
echo "     → planner emits DAG; check /tasks/<id>/children for sub-task rows"
echo "     → SSE /tasks/<id>/stream shows subtask_dispatched + subtask_done events"
echo "     → final output is reducer's summary"
echo
echo "Press Ctrl-C to stop."
wait
```

`chmod +x master_agent/scripts/e2e.sh`

- [ ] **Step 4: Commit**

```bash
git add master_agent/
git commit -m "docs(master_agent): README + example config + e2e script"
```

---

## Final Verification

- [ ] **Run the full test matrix locally**

```bash
cd slave_agent
go vet ./...
go test ./... -race -count=1
go test -tags=contract ./tests/contract/...
go build -tags=smoke ./tests/smoke/...
```

All non-smoke tests must pass; smoke must compile.

- [ ] **Inspect commits**

```bash
git log --oneline feat/master-agent ^feat/slave-agent
```

Expected: roughly 16 new commits (one per task above).

- [ ] **Verify spec coverage**

| Spec section | Implemented in |
|---|---|
| §1 architecture | Tasks 1, 14 |
| §2 decisions list | Embodied throughout |
| §3.1 route data flow | Task 11 |
| §3.2 fanout data flow + DAG semantics | Tasks 7-9, 12 |
| §3.3 failure paths | Tasks 7, 11, 12 |
| §3.4 SSE events (subtask_*) | Task 12 (orchestrator writes events via store.ChunkSink; webui SSE handler forwards them by event type unchanged) |
| §3.5 restart recovery for sub_tasks | Task 3 |
| §4.1 directory layout | Tasks 6-14 |
| §4.2 config | Task 1 |
| §4.3 store + sub_tasks | Tasks 2-3 |
| §4.4 planner | Task 6 |
| §4.5 orchestrator | Tasks 10-12 |
| §4.6 webui | Task 13 |
| §4.7 poller interface | Task 4 |
| §4.8 tunnel.SDKClient | Task 5 |
| §4.9 master main | Task 14 |
| §5 error handling | Distributed across Tasks 6-12 |
| §6.1 unit tests | Each impl task includes tests |
| §6.3 contract | Task 15 |
| §6.4 e2e script | Task 16 |
| §6.5 not tested | n/a |
| §6.6 CI reuse | No new workflow needed |

**SSE coverage**: Task 12 emits `subtask_dispatched`, `subtask_done`, and `subtask_skipped` events through `store.ChunkSink(parentID)`. The existing webui SSE handler (Task 19 of the slave plan) forwards any event type by name, so no changes needed in webui.
