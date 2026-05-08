# slave_agent Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `slave_agent`, a Go custom-agent for agentserver that polls tasks, routes them by `skill` to ClaudeExecutor or MCPExecutor, persists history in SQLite, exposes a Web UI with SSE streaming, and maintains a `CURRENT_STATE.md` capability journal.

**Architecture:** Single Go binary, serial task processing, layered packages with one-way dependencies (`config` → `store` → `executor` → `journal` → `dispatch` → `poller` + `tunnel` + `webui` → `main`). External I/O: WebSocket via `agentsdk`, HTTP for task API, subprocess for `claude`, MCP transports stdio/HTTP, SQLite via pure-Go `modernc.org/sqlite`.

**Tech Stack:** Go 1.22+, `github.com/agentserver/agentserver/pkg/agentsdk`, `modernc.org/sqlite`, `gopkg.in/yaml.v3`, stdlib `net/http`, `os/exec`, `text/template`. Tests use `testify/require` (or stdlib) and `httptest`.

**Spec:** `docs/superpowers/specs/2026-04-27-slave-agent-design.md`

---

## File Structure

```
slave_agent/
├── go.mod
├── go.sum
├── README.md
├── config.example.yaml
├── cmd/slave-agent/
│   └── main.go                            # Wires components, handles signals
├── internal/
│   ├── config/
│   │   ├── config.go                      # Config struct + Load + Save + Validate
│   │   └── config_test.go
│   ├── store/
│   │   ├── schema.sql                     # Embedded via go:embed
│   │   ├── store.go                       # Open, task CRUD, ChunkSink, Subscribe, Recover
│   │   ├── events.go                      # Event type
│   │   └── store_test.go
│   ├── executor/
│   │   ├── executor.go                    # Task, Result, ChunkSink, Executor interface
│   │   ├── claude.go                      # ClaudeExecutor (subprocess + stream-json)
│   │   ├── claude_test.go
│   │   ├── mcp.go                         # MCPExecutor (stdio + HTTP transports)
│   │   └── mcp_test.go
│   ├── journal/
│   │   ├── journal.go                     # CURRENT_STATE.md merge + history.md append
│   │   └── journal_test.go
│   ├── dispatch/
│   │   ├── dispatch.go                    # skill → executor route + lifecycle
│   │   └── dispatch_test.go
│   ├── poller/
│   │   ├── poller.go                      # 5s polling loop + status PUTs
│   │   └── poller_test.go
│   ├── tunnel/
│   │   ├── tunnel.go                      # agentsdk wrapper + device flow + register
│   │   └── tunnel_test.go
│   └── webui/
│       ├── server.go                      # HTTP routes (/, /tasks, /tasks/{id}/stream, /state, /healthz)
│       ├── server_test.go
│       └── templates/
│           └── dashboard.html
├── testdata/
│   ├── fake-claude.sh                     # Bash script emulating claude --print --output-format=stream-json
│   ├── fake-claude-text.sh                # Bash script emulating plain claude --print (for journal)
│   └── fake-mcp-stdio/
│       └── main.go                        # Tiny Go MCP-stdio server for tests
├── tests/
│   ├── contract/
│   │   ├── agentserver_contract_test.go
│   │   └── doc.go                         # build tag: contract
│   └── smoke/
│       ├── claude_smoke_test.go
│       ├── mcp_smoke_test.go
│       └── doc.go                         # build tag: smoke
└── scripts/
    └── e2e.sh
```

**Boundary rules:**
- `config` depends on nothing project-internal.
- `store` depends only on stdlib + sqlite driver.
- `executor` depends on `store` only via the `ChunkSink` interface defined in `executor`.
- `journal` depends on stdlib + `os/exec`. Knows about `executor.Task` and `executor.Result` shapes.
- `dispatch` depends on `executor`, `journal`, `store`.
- `poller`, `tunnel`, `webui` depend on the layers below them.
- `main` is the only file that imports everything.

---

## Pre-flight

- [ ] **Read the spec end-to-end before starting Task 1**: `docs/superpowers/specs/2026-04-27-slave-agent-design.md`. Every later task references section numbers (e.g., "spec §4.6").

---

## Task 1: Project skeleton

**Files:**
- Create: `slave_agent/go.mod`
- Create: `slave_agent/.gitignore`
- Create: `slave_agent/README.md`
- Create: `slave_agent/config.example.yaml`
- Create: `slave_agent/cmd/slave-agent/main.go` (placeholder)

- [ ] **Step 1: Init module**

```bash
cd slave_agent
go mod init github.com/yourorg/slave_agent
go get github.com/agentserver/agentserver/pkg/agentsdk@latest
go get modernc.org/sqlite@latest
go get gopkg.in/yaml.v3@latest
go get github.com/stretchr/testify@latest
```

If `github.com/agentserver/agentserver/pkg/agentsdk` is unavailable in the user's environment, vendor it from the local clone at `../agentserver/pkg/agentsdk` via a `replace` directive in `go.mod`:

```
replace github.com/agentserver/agentserver => ../agentserver
```

- [ ] **Step 2: Create `.gitignore`**

```
data.db
data.db-*
journal/CURRENT_STATE.md
journal/history.md
config.yaml
*.tmp
```

(Keep `config.example.yaml` tracked, ignore the user-populated `config.yaml`.)

- [ ] **Step 3: Create `config.example.yaml`**

```yaml
server:
  url: https://agent.example.com
  name: slave-agent

credentials:
  sandbox_id: ""
  tunnel_token: ""
  proxy_token: ""
  short_id: ""

claude:
  bin: claude
  workdir: ""
  extra_args: []

mcp_servers:
  echo_stdio:
    transport: stdio
    command: /usr/bin/env
    args: ["python3", "-m", "echo_mcp"]
    env:
      LOG_LEVEL: info
  remote_http:
    transport: http
    url: https://mcp.example.com
    headers:
      Authorization: Bearer XXX

discovery:
  display_name: slave_agent
  description: General-purpose task executor with claude + MCP
  skills:
    - chat
    - mcp
```

- [ ] **Step 4: Create placeholder `cmd/slave-agent/main.go`**

```go
package main

import "fmt"

func main() {
    fmt.Println("slave_agent: not yet implemented")
}
```

- [ ] **Step 5: Verify build**

Run: `go build ./...`
Expected: no errors.

- [ ] **Step 6: Create README.md skeleton**

```markdown
# slave_agent

Custom agent for agentserver. See `docs/superpowers/specs/2026-04-27-slave-agent-design.md`.

## Build

    go build -o slave-agent ./cmd/slave-agent

## Configure

    cp config.example.yaml config.yaml
    # edit server.url

## Run

    ./slave-agent
```

- [ ] **Step 7: Commit**

```bash
git add slave_agent/
git commit -m "feat(slave_agent): project skeleton"
```

---

## Task 2: `config` package — Load

**Files:**
- Create: `slave_agent/internal/config/config.go`
- Create: `slave_agent/internal/config/config_test.go`

- [ ] **Step 1: Write failing test for Load**

```go
// internal/config/config_test.go
package config

import (
    "os"
    "path/filepath"
    "testing"

    "github.com/stretchr/testify/require"
)

func TestLoad_ValidYAML(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "c.yaml")
    require.NoError(t, os.WriteFile(path, []byte(`
server:
  url: https://example.com
  name: agent-1
claude:
  bin: claude
mcp_servers:
  echo:
    transport: stdio
    command: /bin/echo
discovery:
  display_name: A
  skills: [chat, mcp]
`), 0o600))

    c, err := Load(path)
    require.NoError(t, err)
    require.Equal(t, "https://example.com", c.Server.URL)
    require.Equal(t, "agent-1", c.Server.Name)
    require.Equal(t, "stdio", c.MCPServers["echo"].Transport)
    require.ElementsMatch(t, []string{"chat", "mcp"}, c.Discovery.Skills)
}

func TestLoad_MissingURL(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "c.yaml")
    require.NoError(t, os.WriteFile(path, []byte("server:\n  name: x\n"), 0o600))

    _, err := Load(path)
    require.ErrorContains(t, err, "server.url")
}
```

- [ ] **Step 2: Run — expect FAIL**

`go test ./internal/config/...`
Expected: undefined `Load`.

- [ ] **Step 3: Implement `config.go`**

```go
package config

import (
    "fmt"
    "os"

    "gopkg.in/yaml.v3"
)

type Config struct {
    Server      Server               `yaml:"server"`
    Credentials Credentials          `yaml:"credentials"`
    Claude      Claude               `yaml:"claude"`
    MCPServers  map[string]MCPServer `yaml:"mcp_servers"`
    Discovery   Discovery            `yaml:"discovery"`
}

type Server struct {
    URL  string `yaml:"url"`
    Name string `yaml:"name"`
}

type Credentials struct {
    SandboxID   string `yaml:"sandbox_id"`
    TunnelToken string `yaml:"tunnel_token"`
    ProxyToken  string `yaml:"proxy_token"`
    ShortID     string `yaml:"short_id"`
}

type Claude struct {
    Bin     string   `yaml:"bin"`
    WorkDir string   `yaml:"workdir"`
    Args    []string `yaml:"extra_args"`
}

type MCPServer struct {
    Transport string            `yaml:"transport"` // "stdio" | "http"
    Command   string            `yaml:"command,omitempty"`
    Args      []string          `yaml:"args,omitempty"`
    Env       map[string]string `yaml:"env,omitempty"`
    URL       string            `yaml:"url,omitempty"`
    Headers   map[string]string `yaml:"headers,omitempty"`
}

type Discovery struct {
    DisplayName string   `yaml:"display_name"`
    Description string   `yaml:"description"`
    Skills      []string `yaml:"skills"`
}

func Load(path string) (*Config, error) {
    data, err := os.ReadFile(path)
    if err != nil {
        return nil, fmt.Errorf("read %s: %w", path, err)
    }
    var c Config
    if err := yaml.Unmarshal(data, &c); err != nil {
        return nil, fmt.Errorf("parse %s: %w", path, err)
    }
    if err := c.Validate(); err != nil {
        return nil, err
    }
    if c.Claude.Bin == "" {
        c.Claude.Bin = "claude"
    }
    return &c, nil
}

func (c *Config) Validate() error {
    if c.Server.URL == "" {
        return fmt.Errorf("server.url is required")
    }
    return nil
}
```

- [ ] **Step 4: Run — expect PASS**

`go test ./internal/config/... -v`

- [ ] **Step 5: Commit**

```bash
git add slave_agent/internal/config/
git commit -m "feat(slave_agent/config): Load + Validate"
```

---

## Task 3: `config.Save` — round-trip

**Files:**
- Modify: `slave_agent/internal/config/config.go`
- Modify: `slave_agent/internal/config/config_test.go`

- [ ] **Step 1: Failing test**

Append to `config_test.go`:

```go
func TestSaveLoad_Roundtrip(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "c.yaml")
    c := &Config{
        Server:    Server{URL: "https://x", Name: "n"},
        Discovery: Discovery{DisplayName: "D", Skills: []string{"chat"}},
    }
    c.Credentials.SandboxID = "sb-1"
    c.Credentials.TunnelToken = "tt"
    c.Credentials.ProxyToken = "pt"
    c.Credentials.ShortID = "sid"

    require.NoError(t, c.Save(path))
    got, err := Load(path)
    require.NoError(t, err)
    require.Equal(t, c.Credentials, got.Credentials)
    require.Equal(t, c.Server, got.Server)

    info, err := os.Stat(path)
    require.NoError(t, err)
    require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}
```

- [ ] **Step 2: Run — expect FAIL** (`Save` undefined)

- [ ] **Step 3: Implement `Save`**

Append to `config.go`:

```go
func (c *Config) Save(path string) error {
    data, err := yaml.Marshal(c)
    if err != nil {
        return fmt.Errorf("marshal: %w", err)
    }
    tmp := path + ".tmp"
    if err := os.WriteFile(tmp, data, 0o600); err != nil {
        return err
    }
    return os.Rename(tmp, path)
}
```

- [ ] **Step 4: Run — expect PASS**

`go test ./internal/config/... -v`

- [ ] **Step 5: Commit**

```bash
git add slave_agent/internal/config/
git commit -m "feat(slave_agent/config): atomic Save with 0600 perms"
```

---

## Task 4: `store` schema + Open

**Files:**
- Create: `slave_agent/internal/store/schema.sql`
- Create: `slave_agent/internal/store/events.go`
- Create: `slave_agent/internal/store/store.go`
- Create: `slave_agent/internal/store/store_test.go`

- [ ] **Step 1: Write schema.sql**

```sql
CREATE TABLE IF NOT EXISTS tasks (
    id           TEXT PRIMARY KEY,
    skill        TEXT NOT NULL,
    prompt       TEXT NOT NULL,
    status       TEXT NOT NULL,
    output       TEXT,
    error        TEXT,
    created_at   TEXT NOT NULL,
    started_at   TEXT,
    finished_at  TEXT
);

CREATE TABLE IF NOT EXISTS task_chunks (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id     TEXT NOT NULL REFERENCES tasks(id),
    ts          TEXT NOT NULL,
    type        TEXT NOT NULL,
    data        TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_chunks_task ON task_chunks(task_id, id);

CREATE TABLE IF NOT EXISTS pending_acks (
    task_id      TEXT PRIMARY KEY REFERENCES tasks(id),
    status       TEXT NOT NULL,
    enqueued_at  TEXT NOT NULL
);
```

- [ ] **Step 2: Write `events.go`**

```go
package store

type EventType string

const (
    EventChunk      EventType = "chunk"
    EventCapability EventType = "capability"
    EventDone       EventType = "done"
    EventError      EventType = "error"
)

type Event struct {
    Type EventType
    Data string
}
```

- [ ] **Step 3: Failing test for Open**

```go
// store_test.go
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
```

- [ ] **Step 4: Run — expect FAIL**

- [ ] **Step 5: Implement Open**

```go
// store.go
package store

import (
    "database/sql"
    _ "embed"
    "fmt"
    "sync"

    _ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

type Store struct {
    db   *sql.DB
    mu   sync.Mutex
    subs map[string][]chan Event
}

func Open(path string) (*Store, error) {
    db, err := sql.Open("sqlite", path)
    if err != nil {
        return nil, fmt.Errorf("open: %w", err)
    }
    if _, err := db.Exec(schemaSQL); err != nil {
        db.Close()
        return nil, fmt.Errorf("migrate: %w", err)
    }
    return &Store{db: db, subs: make(map[string][]chan Event)}, nil
}

func (s *Store) Close() error      { return s.db.Close() }
func (s *Store) DB() *sql.DB       { return s.db } // test-only accessor
```

- [ ] **Step 6: Run — expect PASS**

`go test ./internal/store/... -v`

- [ ] **Step 7: Commit**

```bash
git add slave_agent/internal/store/
git commit -m "feat(slave_agent/store): Open + embedded schema"
```

---

## Task 5: `store` task CRUD + state machine

**Files:**
- Modify: `slave_agent/internal/store/store.go`
- Modify: `slave_agent/internal/store/store_test.go`

- [ ] **Step 1: Failing tests**

Append to `store_test.go`:

```go
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
```

- [ ] **Step 2: Run — expect FAIL** (undefined Task / methods)

- [ ] **Step 3: Implement task types and CRUD**

Append to `store.go`:

```go
import (
    "context"
    "time"
)

type Task struct {
    ID            string
    Skill         string
    Prompt        string
    SystemContext string
    TimeoutSec    int
}

type TaskRow struct {
    ID         string
    Skill      string
    Prompt     string
    Status     string
    Output     string
    Error      string
    CreatedAt  string
    StartedAt  string
    FinishedAt string
}

type Chunk struct {
    Type EventType
    Data string
    TS   string
}

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func (s *Store) Insert(t Task) error {
    _, err := s.db.Exec(
        `INSERT INTO tasks(id,skill,prompt,status,created_at) VALUES(?,?,?,?,?)`,
        t.ID, t.Skill, t.Prompt, "assigned", nowUTC(),
    )
    return err
}

func (s *Store) MarkRunning(id string) error {
    res, err := s.db.Exec(
        `UPDATE tasks SET status='running', started_at=? WHERE id=? AND status='assigned'`,
        nowUTC(), id,
    )
    if err != nil {
        return err
    }
    n, _ := res.RowsAffected()
    if n == 0 {
        return fmt.Errorf("task %s not in assigned state", id)
    }
    return nil
}

func (s *Store) Complete(id, output string) error {
    res, err := s.db.Exec(
        `UPDATE tasks SET status='completed', output=?, finished_at=? WHERE id=? AND status IN ('assigned','running')`,
        output, nowUTC(), id,
    )
    if err != nil {
        return err
    }
    n, _ := res.RowsAffected()
    if n == 0 {
        return fmt.Errorf("task %s not in active state", id)
    }
    return nil
}

func (s *Store) Fail(id, reason string) error {
    res, err := s.db.Exec(
        `UPDATE tasks SET status='failed', error=?, finished_at=? WHERE id=? AND status IN ('assigned','running')`,
        reason, nowUTC(), id,
    )
    if err != nil {
        return err
    }
    n, _ := res.RowsAffected()
    if n == 0 {
        return fmt.Errorf("task %s not in active state", id)
    }
    return nil
}

func (s *Store) GetTaskWithChunks(id string) (TaskRow, []Chunk, error) {
    var r TaskRow
    var output, errStr, started, finished sql.NullString
    err := s.db.QueryRow(
        `SELECT id,skill,prompt,status,output,error,created_at,started_at,finished_at FROM tasks WHERE id=?`, id,
    ).Scan(&r.ID, &r.Skill, &r.Prompt, &r.Status, &output, &errStr, &r.CreatedAt, &started, &finished)
    if err != nil {
        return r, nil, err
    }
    r.Output, r.Error, r.StartedAt, r.FinishedAt = output.String, errStr.String, started.String, finished.String

    rows, err := s.db.Query(`SELECT type, data, ts FROM task_chunks WHERE task_id=? ORDER BY id ASC`, id)
    if err != nil {
        return r, nil, err
    }
    defer rows.Close()
    var chunks []Chunk
    for rows.Next() {
        var c Chunk
        var t string
        if err := rows.Scan(&t, &c.Data, &c.TS); err != nil {
            return r, nil, err
        }
        c.Type = EventType(t)
        chunks = append(chunks, c)
    }
    return r, chunks, rows.Err()
}

func (s *Store) ListTasks(limit, offset int) ([]TaskRow, error) {
    rows, err := s.db.Query(
        `SELECT id,skill,prompt,status,output,error,created_at,started_at,finished_at FROM tasks ORDER BY created_at DESC LIMIT ? OFFSET ?`,
        limit, offset,
    )
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    var out []TaskRow
    for rows.Next() {
        var r TaskRow
        var output, errStr, started, finished sql.NullString
        if err := rows.Scan(&r.ID, &r.Skill, &r.Prompt, &r.Status, &output, &errStr, &r.CreatedAt, &started, &finished); err != nil {
            return nil, err
        }
        r.Output, r.Error, r.StartedAt, r.FinishedAt = output.String, errStr.String, started.String, finished.String
        out = append(out, r)
    }
    return out, rows.Err()
}

// Used to silence unused import in case context isn't referenced directly above.
var _ = context.Background
```

- [ ] **Step 4: Run — expect PASS**

`go test ./internal/store/... -v -run "TestInsert|TestComplete|TestFail"`

- [ ] **Step 5: Commit**

```bash
git add slave_agent/internal/store/
git commit -m "feat(slave_agent/store): task CRUD + state transitions"
```

---

## Task 6: `store.ChunkSink` + SSE pubsub

**Files:**
- Modify: `slave_agent/internal/store/store.go`
- Modify: `slave_agent/internal/store/store_test.go`

- [ ] **Step 1: Failing tests**

Append to `store_test.go`:

```go
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
```

- [ ] **Step 2: Run — expect FAIL**

- [ ] **Step 3: Implement ChunkSink + Subscribe**

Append to `store.go`:

```go
type sink struct {
    s      *Store
    taskID string
}

type Sink interface {
    Write(eventType, data string)
    Close()
}

func (s *Store) ChunkSink(taskID string) Sink { return &sink{s: s, taskID: taskID} }

func (sk *sink) Write(eventType, data string) {
    _, err := sk.s.db.Exec(
        `INSERT INTO task_chunks(task_id,ts,type,data) VALUES(?,?,?,?)`,
        sk.taskID, nowUTC(), eventType, data,
    )
    if err != nil {
        // store failure is best-effort for chunks; don't abort task.
        return
    }
    sk.s.publish(sk.taskID, Event{Type: EventType(eventType), Data: data})
}

func (sk *sink) Close() {
    sk.s.publish(sk.taskID, Event{Type: EventDone})
    sk.s.unsubscribeAll(sk.taskID)
}

func (s *Store) Subscribe(taskID string) (<-chan Event, func()) {
    ch := make(chan Event, 32)
    s.mu.Lock()
    s.subs[taskID] = append(s.subs[taskID], ch)
    s.mu.Unlock()

    cancel := func() {
        s.mu.Lock()
        defer s.mu.Unlock()
        for i, c := range s.subs[taskID] {
            if c == ch {
                s.subs[taskID] = append(s.subs[taskID][:i], s.subs[taskID][i+1:]...)
                close(ch)
                return
            }
        }
    }
    return ch, cancel
}

func (s *Store) publish(taskID string, e Event) {
    s.mu.Lock()
    subs := append([]chan Event(nil), s.subs[taskID]...)
    s.mu.Unlock()
    for _, c := range subs {
        select {
        case c <- e:
        default:
            // slow consumer — drop rather than block writer
        }
    }
}

func (s *Store) unsubscribeAll(taskID string) {
    s.mu.Lock()
    defer s.mu.Unlock()
    for _, c := range s.subs[taskID] {
        close(c)
    }
    delete(s.subs, taskID)
}
```

- [ ] **Step 4: Run — expect PASS**

`go test ./internal/store/... -race -v`

- [ ] **Step 5: Commit**

```bash
git add slave_agent/internal/store/
git commit -m "feat(slave_agent/store): ChunkSink + SSE pubsub with non-blocking writer"
```

---

## Task 7: `store.Recover` + pending_acks

**Files:**
- Modify: `slave_agent/internal/store/store.go`
- Modify: `slave_agent/internal/store/store_test.go`

- [ ] **Step 1: Failing tests**

Append to `store_test.go`:

```go
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
```

- [ ] **Step 2: Run — expect FAIL**

- [ ] **Step 3: Implement Recover + pending_acks**

Append to `store.go`:

```go
type PendingAck struct {
    TaskID string
    Status string // "completed" | "failed"
    Reason string // optional
}

func (s *Store) Recover() error {
    tx, err := s.db.Begin()
    if err != nil {
        return err
    }
    defer tx.Rollback()
    rows, err := tx.Query(`SELECT id FROM tasks WHERE status IN ('assigned','running')`)
    if err != nil {
        return err
    }
    var ids []string
    for rows.Next() {
        var id string
        if err := rows.Scan(&id); err != nil {
            rows.Close()
            return err
        }
        ids = append(ids, id)
    }
    rows.Close()

    for _, id := range ids {
        if _, err := tx.Exec(
            `UPDATE tasks SET status='failed', error=?, finished_at=? WHERE id=?`,
            "agent restarted", nowUTC(), id,
        ); err != nil {
            return err
        }
        if _, err := tx.Exec(
            `INSERT OR REPLACE INTO pending_acks(task_id,status,enqueued_at) VALUES(?,?,?)`,
            id, "failed", nowUTC(),
        ); err != nil {
            return err
        }
    }
    return tx.Commit()
}

func (s *Store) EnqueuePendingAck(id, status string) error {
    _, err := s.db.Exec(
        `INSERT OR REPLACE INTO pending_acks(task_id,status,enqueued_at) VALUES(?,?,?)`,
        id, status, nowUTC(),
    )
    return err
}

func (s *Store) PopPendingAcks() ([]PendingAck, error) {
    rows, err := s.db.Query(`SELECT pa.task_id, pa.status, COALESCE(t.error,''), COALESCE(t.output,'')
                             FROM pending_acks pa JOIN tasks t ON t.id=pa.task_id
                             ORDER BY pa.enqueued_at ASC`)
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    var out []PendingAck
    for rows.Next() {
        var p PendingAck
        var output string
        if err := rows.Scan(&p.TaskID, &p.Status, &p.Reason, &output); err != nil {
            return nil, err
        }
        if p.Status == "completed" {
            p.Reason = output
        }
        out = append(out, p)
    }
    return out, rows.Err()
}

func (s *Store) DeletePendingAck(id string) error {
    _, err := s.db.Exec(`DELETE FROM pending_acks WHERE task_id=?`, id)
    return err
}
```

- [ ] **Step 4: Run — expect PASS**

`go test ./internal/store/... -race -v`

- [ ] **Step 5: Commit**

```bash
git add slave_agent/internal/store/
git commit -m "feat(slave_agent/store): Recover + pending_acks queue"
```

---

## Task 8: `executor` types

**Files:**
- Create: `slave_agent/internal/executor/executor.go`

- [ ] **Step 1: Write executor.go**

```go
package executor

import "context"

type Task struct {
    ID            string
    Skill         string
    Prompt        string
    SystemContext string
    TimeoutSec    int
}

type Result struct {
    Summary          string
    CapabilityChange string // empty = no change
}

type Sink interface {
    Write(eventType, data string)
    Close()
}

type Executor interface {
    Run(ctx context.Context, t Task, sink Sink) (Result, error)
}
```

- [ ] **Step 2: Verify compiles**

`go build ./internal/executor/...`

- [ ] **Step 3: Commit**

```bash
git add slave_agent/internal/executor/
git commit -m "feat(slave_agent/executor): core types + interface"
```

---

## Task 9: `testdata/fake-claude.sh`

**Files:**
- Create: `slave_agent/testdata/fake-claude.sh`
- Create: `slave_agent/testdata/fake-claude-text.sh`

- [ ] **Step 1: Write `fake-claude.sh`**

This script reads the prompt from stdin and emits stream-json. Behavior is controlled by env vars set by tests.

```bash
#!/usr/bin/env bash
# Behavior knobs (env):
#   FAKE_CLAUDE_MODE=normal|capability|nochange|exit1|sleep|garbage
#   FAKE_CLAUDE_SLEEP=seconds
set -euo pipefail
mode="${FAKE_CLAUDE_MODE:-nochange}"

emit() {
  printf '%s\n' "$1"
}

case "$mode" in
  normal)
    emit '{"type":"assistant","message":{"content":[{"type":"text","text":"hello "}]}}'
    emit '{"type":"assistant","message":{"content":[{"type":"text","text":"world"}]}}'
    emit '{"type":"result","subtype":"success"}'
    ;;
  capability)
    emit '{"type":"assistant","message":{"content":[{"type":"text","text":"installed foo\n=== CAPABILITY ===\nfoo CLI now available"}]}}'
    emit '{"type":"result","subtype":"success"}'
    ;;
  nochange)
    emit '{"type":"assistant","message":{"content":[{"type":"text","text":"answer\n=== CAPABILITY ===\nNO_CAPABILITY_CHANGE"}]}}'
    emit '{"type":"result","subtype":"success"}'
    ;;
  exit1)
    echo "boom" 1>&2
    exit 1
    ;;
  sleep)
    sleep "${FAKE_CLAUDE_SLEEP:-30}"
    ;;
  garbage)
    emit 'not json at all'
    emit '{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}]}}'
    emit '{"type":"result","subtype":"success"}'
    ;;
  *)
    echo "unknown FAKE_CLAUDE_MODE: $mode" 1>&2
    exit 2
    ;;
esac
```

`chmod +x slave_agent/testdata/fake-claude.sh`

- [ ] **Step 2: Write `fake-claude-text.sh`** (used by journal merge calls)

```bash
#!/usr/bin/env bash
# Reads merge prompt on stdin, writes the new CURRENT_STATE.md to stdout.
# In normal mode, just echoes a deterministic block so tests can assert.
set -euo pipefail
mode="${FAKE_CLAUDE_TEXT_MODE:-ok}"
case "$mode" in
  ok)
    cat <<'EOF'
## Tools
- updated by fake merge

## MCP Servers
- (none)
EOF
    ;;
  fail)
    echo "merge failed" 1>&2
    exit 1
    ;;
  *)
    echo "unknown FAKE_CLAUDE_TEXT_MODE" 1>&2
    exit 2
    ;;
esac
```

`chmod +x slave_agent/testdata/fake-claude-text.sh`

- [ ] **Step 3: Commit**

```bash
git add slave_agent/testdata/
git commit -m "test(slave_agent): fake claude scripts for unit tests"
```

---

## Task 10: `ClaudeExecutor` — happy path streaming

**Files:**
- Create: `slave_agent/internal/executor/claude.go`
- Create: `slave_agent/internal/executor/claude_test.go`

- [ ] **Step 1: Failing test**

```go
// claude_test.go
package executor

import (
    "context"
    "path/filepath"
    "runtime"
    "sync"
    "testing"
    "time"

    "github.com/stretchr/testify/require"
)

type captureSink struct {
    mu     sync.Mutex
    events []struct{ Type, Data string }
    closed bool
}

func (c *captureSink) Write(t, d string) {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.events = append(c.events, struct{ Type, Data string }{t, d})
}
func (c *captureSink) Close() {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.closed = true
}

func fakeClaudePath(t *testing.T) string {
    t.Helper()
    _, file, _, _ := runtime.Caller(0)
    p, err := filepath.Abs(filepath.Join(filepath.Dir(file), "../../testdata/fake-claude.sh"))
    require.NoError(t, err)
    return p
}

func TestClaude_NormalStreaming(t *testing.T) {
    e := NewClaudeExecutor(ClaudeConfig{Bin: fakeClaudePath(t), Env: []string{"FAKE_CLAUDE_MODE=normal"}})
    sink := &captureSink{}

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    res, err := e.Run(ctx, Task{ID: "t1", Prompt: "hi"}, sink)
    require.NoError(t, err)
    require.Equal(t, "hello world", res.Summary)
    require.Equal(t, "", res.CapabilityChange)
    require.True(t, sink.closed)

    var got string
    for _, ev := range sink.events {
        if ev.Type == "chunk" {
            got += ev.Data
        }
    }
    require.Equal(t, "hello world", got)
}
```

- [ ] **Step 2: Run — expect FAIL** (NewClaudeExecutor undefined)

- [ ] **Step 3: Implement claude.go (minimal: streaming + parsing)**

```go
package executor

import (
    "bufio"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "os/exec"
    "strings"
)

type ClaudeConfig struct {
    Bin     string
    WorkDir string
    Args    []string
    Env     []string // extra env (KEY=VAL)
}

type ClaudeExecutor struct{ cfg ClaudeConfig }

func NewClaudeExecutor(cfg ClaudeConfig) *ClaudeExecutor { return &ClaudeExecutor{cfg: cfg} }

const capEpilogue = "\n\nWhen you finish, append a line `=== CAPABILITY ===` then 1-3 lines describing any persistent capability change to yourself. If none, write `NO_CAPABILITY_CHANGE`."

func (e *ClaudeExecutor) Run(ctx context.Context, t Task, sink Sink) (Result, error) {
    args := append([]string{"--print", "--output-format=stream-json", "--append-system-prompt", capEpilogue}, e.cfg.Args...)
    cmd := exec.CommandContext(ctx, e.cfg.Bin, args...)
    cmd.Dir = e.cfg.WorkDir
    cmd.Env = append(cmd.Environ(), e.cfg.Env...)

    stdin, err := cmd.StdinPipe()
    if err != nil {
        return Result{}, err
    }
    stdout, err := cmd.StdoutPipe()
    if err != nil {
        return Result{}, err
    }
    var stderrBuf strings.Builder
    cmd.Stderr = &stderrBuf

    if err := cmd.Start(); err != nil {
        return Result{}, err
    }

    go func() {
        defer stdin.Close()
        if t.SystemContext != "" {
            io.WriteString(stdin, t.SystemContext+"\n\n")
        }
        io.WriteString(stdin, t.Prompt)
    }()

    var lastText strings.Builder
    sc := bufio.NewScanner(stdout)
    sc.Buffer(make([]byte, 0, 1<<16), 1<<24)
    for sc.Scan() {
        line := sc.Bytes()
        var msg struct {
            Type    string `json:"type"`
            Message struct {
                Content []struct {
                    Type string `json:"type"`
                    Text string `json:"text"`
                } `json:"content"`
            } `json:"message"`
        }
        if err := json.Unmarshal(line, &msg); err != nil {
            continue // garbage line: skip per spec §5.5
        }
        if msg.Type != "assistant" {
            continue
        }
        for _, c := range msg.Message.Content {
            if c.Type != "text" {
                continue
            }
            sink.Write("chunk", c.Text)
            lastText.WriteString(c.Text)
        }
    }

    if err := cmd.Wait(); err != nil {
        defer sink.Close()
        if ctx.Err() == context.DeadlineExceeded {
            return Result{}, fmt.Errorf("timeout")
        }
        tail := stderrBuf.String()
        if len(tail) > 4096 {
            tail = tail[len(tail)-4096:]
        }
        return Result{}, fmt.Errorf("claude exit: %v: %s", err, tail)
    }

    full := lastText.String()
    summary, change := splitCapability(full)
    if change != "" {
        sink.Write("capability", change)
    }
    sink.Close()
    return Result{Summary: summary, CapabilityChange: change}, nil
}

func splitCapability(s string) (summary, change string) {
    const sep = "=== CAPABILITY ==="
    i := strings.LastIndex(s, sep)
    if i < 0 {
        return strings.TrimSpace(s), ""
    }
    summary = strings.TrimSpace(s[:i])
    change = strings.TrimSpace(s[i+len(sep):])
    if change == "NO_CAPABILITY_CHANGE" {
        change = ""
    }
    return
}
```

- [ ] **Step 4: Run — expect PASS**

`go test ./internal/executor/... -v -run TestClaude_NormalStreaming`

- [ ] **Step 5: Commit**

```bash
git add slave_agent/internal/executor/claude.go slave_agent/internal/executor/claude_test.go
git commit -m "feat(slave_agent/executor): ClaudeExecutor streaming happy path"
```

---

## Task 11: `ClaudeExecutor` — capability parsing + edge cases

**Files:**
- Modify: `slave_agent/internal/executor/claude_test.go`

- [ ] **Step 1: Add tests**

```go
func TestClaude_CapabilityParsed(t *testing.T) {
    e := NewClaudeExecutor(ClaudeConfig{Bin: fakeClaudePath(t), Env: []string{"FAKE_CLAUDE_MODE=capability"}})
    sink := &captureSink{}
    res, err := e.Run(context.Background(), Task{ID: "t"}, sink)
    require.NoError(t, err)
    require.Equal(t, "installed foo", res.Summary)
    require.Equal(t, "foo CLI now available", res.CapabilityChange)

    var sawCap bool
    for _, ev := range sink.events {
        if ev.Type == "capability" && ev.Data == "foo CLI now available" {
            sawCap = true
        }
    }
    require.True(t, sawCap, "expected capability event before close")
}

func TestClaude_NoCapabilityChange(t *testing.T) {
    e := NewClaudeExecutor(ClaudeConfig{Bin: fakeClaudePath(t), Env: []string{"FAKE_CLAUDE_MODE=nochange"}})
    sink := &captureSink{}
    res, err := e.Run(context.Background(), Task{ID: "t"}, sink)
    require.NoError(t, err)
    require.Equal(t, "answer", res.Summary)
    require.Equal(t, "", res.CapabilityChange)
}

func TestClaude_Exit1(t *testing.T) {
    e := NewClaudeExecutor(ClaudeConfig{Bin: fakeClaudePath(t), Env: []string{"FAKE_CLAUDE_MODE=exit1"}})
    sink := &captureSink{}
    _, err := e.Run(context.Background(), Task{ID: "t"}, sink)
    require.Error(t, err)
    require.Contains(t, err.Error(), "boom")
}

func TestClaude_Timeout(t *testing.T) {
    e := NewClaudeExecutor(ClaudeConfig{Bin: fakeClaudePath(t), Env: []string{"FAKE_CLAUDE_MODE=sleep", "FAKE_CLAUDE_SLEEP=10"}})
    sink := &captureSink{}
    ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
    defer cancel()
    _, err := e.Run(ctx, Task{ID: "t"}, sink)
    require.Error(t, err)
    require.Contains(t, err.Error(), "timeout")
}

func TestClaude_GarbageLines_StillCompletes(t *testing.T) {
    e := NewClaudeExecutor(ClaudeConfig{Bin: fakeClaudePath(t), Env: []string{"FAKE_CLAUDE_MODE=garbage"}})
    sink := &captureSink{}
    res, err := e.Run(context.Background(), Task{ID: "t"}, sink)
    require.NoError(t, err)
    require.Equal(t, "ok", res.Summary)
}
```

- [ ] **Step 2: Run — expect PASS** (implementation already handles these)

`go test ./internal/executor/... -v`

If `TestClaude_Exit1` fails because `sink.Close` wasn't called on error, ensure the `defer sink.Close()` in the error branch covers it (already in implementation).

- [ ] **Step 3: Commit**

```bash
git add slave_agent/internal/executor/claude_test.go
git commit -m "test(slave_agent/executor): capability parsing + timeout + exit1 + garbage"
```

---

## Task 12: `testdata/fake-mcp-stdio` — minimal stdio MCP server

**Files:**
- Create: `slave_agent/testdata/fake-mcp-stdio/main.go`
- Create: `slave_agent/testdata/fake-mcp-stdio/go.mod`

This server speaks a tiny JSON-RPC subset over stdio: it reads one request line, writes one response line, and exits.

- [ ] **Step 1: Write `go.mod`**

```
module fakemcp

go 1.22
```

- [ ] **Step 2: Write `main.go`**

```go
package main

import (
    "bufio"
    "encoding/json"
    "fmt"
    "os"
)

type req struct {
    JSONRPC string          `json:"jsonrpc"`
    ID      int             `json:"id"`
    Method  string          `json:"method"`
    Params  json.RawMessage `json:"params"`
}

type toolCallParams struct {
    Name      string                 `json:"name"`
    Arguments map[string]interface{} `json:"arguments"`
}

type resp struct {
    JSONRPC string      `json:"jsonrpc"`
    ID      int         `json:"id"`
    Result  interface{} `json:"result,omitempty"`
    Error   interface{} `json:"error,omitempty"`
}

type toolResult struct {
    Result            interface{} `json:"result"`
    CapabilityChanged bool        `json:"capability_changed"`
    ChangeHint        string      `json:"change_hint,omitempty"`
}

func main() {
    sc := bufio.NewScanner(os.Stdin)
    sc.Buffer(make([]byte, 0, 1<<16), 1<<22)
    for sc.Scan() {
        var r req
        if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
            continue
        }
        var p toolCallParams
        _ = json.Unmarshal(r.Params, &p)
        out := resp{JSONRPC: "2.0", ID: r.ID}
        switch p.Name {
        case "echo":
            out.Result = toolResult{Result: p.Arguments, CapabilityChanged: false}
        case "raise":
            out.Result = toolResult{Result: "raised", CapabilityChanged: true, ChangeHint: "did the thing"}
        case "boom":
            out.Error = map[string]string{"message": "intentional failure"}
        default:
            out.Error = map[string]string{"message": "unknown tool"}
        }
        b, _ := json.Marshal(out)
        fmt.Println(string(b))
    }
}
```

- [ ] **Step 3: Verify it builds**

```bash
cd slave_agent/testdata/fake-mcp-stdio && go build -o fake-mcp-stdio . && ./fake-mcp-stdio < /dev/null
```
Expected: exits cleanly (no input → no output).

- [ ] **Step 4: Commit**

```bash
git add slave_agent/testdata/fake-mcp-stdio/
git commit -m "test(slave_agent): minimal fake stdio MCP server"
```

---

## Task 13: `MCPExecutor` — stdio transport

**Files:**
- Create: `slave_agent/internal/executor/mcp.go`
- Create: `slave_agent/internal/executor/mcp_test.go`

- [ ] **Step 1: Failing test**

```go
// mcp_test.go
package executor

import (
    "context"
    "encoding/json"
    "os/exec"
    "path/filepath"
    "runtime"
    "strings"
    "testing"

    "github.com/stretchr/testify/require"
)

func buildFakeMCP(t *testing.T) string {
    t.Helper()
    _, file, _, _ := runtime.Caller(0)
    src, _ := filepath.Abs(filepath.Join(filepath.Dir(file), "../../testdata/fake-mcp-stdio"))
    out := filepath.Join(t.TempDir(), "fake-mcp")
    cmd := exec.Command("go", "build", "-o", out, ".")
    cmd.Dir = src
    require.NoError(t, cmd.Run())
    return out
}

func TestMCP_Stdio_EchoNoCapability(t *testing.T) {
    bin := buildFakeMCP(t)
    e := NewMCPExecutor(map[string]MCPServerCfg{
        "demo": {Transport: "stdio", Command: bin},
    })
    defer e.Close()

    body, _ := json.Marshal(map[string]interface{}{
        "server": "demo",
        "tool":   "echo",
        "args":   map[string]string{"msg": "hi"},
    })
    res, err := e.Run(context.Background(), Task{ID: "t", Prompt: string(body)}, &captureSink{})
    require.NoError(t, err)
    require.True(t, strings.Contains(res.Summary, "hi"))
    require.Equal(t, "", res.CapabilityChange)
}

func TestMCP_Stdio_RaiseCapability(t *testing.T) {
    bin := buildFakeMCP(t)
    e := NewMCPExecutor(map[string]MCPServerCfg{
        "demo": {Transport: "stdio", Command: bin},
    })
    defer e.Close()

    body, _ := json.Marshal(map[string]interface{}{"server": "demo", "tool": "raise", "args": map[string]string{}})
    res, err := e.Run(context.Background(), Task{Prompt: string(body)}, &captureSink{})
    require.NoError(t, err)
    require.Equal(t, "did the thing", res.CapabilityChange)
}

func TestMCP_BadPromptJSON(t *testing.T) {
    e := NewMCPExecutor(nil)
    defer e.Close()
    _, err := e.Run(context.Background(), Task{Prompt: "not json"}, &captureSink{})
    require.ErrorContains(t, err, "mcp prompt must be JSON")
}

func TestMCP_UnknownServer(t *testing.T) {
    e := NewMCPExecutor(nil)
    defer e.Close()
    body, _ := json.Marshal(map[string]string{"server": "ghost", "tool": "x"})
    _, err := e.Run(context.Background(), Task{Prompt: string(body)}, &captureSink{})
    require.ErrorContains(t, err, "unknown mcp server")
}

func TestMCP_BoomReturnsError(t *testing.T) {
    bin := buildFakeMCP(t)
    e := NewMCPExecutor(map[string]MCPServerCfg{"demo": {Transport: "stdio", Command: bin}})
    defer e.Close()
    body, _ := json.Marshal(map[string]string{"server": "demo", "tool": "boom"})
    _, err := e.Run(context.Background(), Task{Prompt: string(body)}, &captureSink{})
    require.ErrorContains(t, err, "intentional failure")
}
```

- [ ] **Step 2: Run — expect FAIL**

- [ ] **Step 3: Implement `mcp.go` (stdio only first)**

```go
package executor

import (
    "bufio"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "os/exec"
    "strings"
    "sync"
    "sync/atomic"
)

type MCPServerCfg struct {
    Transport string
    // stdio
    Command string
    Args    []string
    Env     map[string]string
    // http
    URL     string
    Headers map[string]string
}

type MCPExecutor struct {
    cfg     map[string]MCPServerCfg
    mu      sync.Mutex
    stdios  map[string]*stdioConn
    httpCli *http.Client
}

func NewMCPExecutor(cfg map[string]MCPServerCfg) *MCPExecutor {
    return &MCPExecutor{
        cfg:     cfg,
        stdios:  make(map[string]*stdioConn),
        httpCli: &http.Client{},
    }
}

func (e *MCPExecutor) Close() {
    e.mu.Lock()
    defer e.mu.Unlock()
    for name, s := range e.stdios {
        s.kill()
        delete(e.stdios, name)
    }
}

type mcpPrompt struct {
    Server string                 `json:"server"`
    Tool   string                 `json:"tool"`
    Args   map[string]interface{} `json:"args"`
}

type mcpToolResult struct {
    Result            json.RawMessage `json:"result"`
    CapabilityChanged bool            `json:"capability_changed"`
    ChangeHint        string          `json:"change_hint"`
}

func (e *MCPExecutor) Run(ctx context.Context, t Task, sink Sink) (Result, error) {
    defer sink.Close()
    var p mcpPrompt
    if err := json.Unmarshal([]byte(t.Prompt), &p); err != nil {
        return Result{}, fmt.Errorf("mcp prompt must be JSON: %w", err)
    }
    if p.Server == "" || p.Tool == "" {
        return Result{}, fmt.Errorf("missing server or tool")
    }
    cfg, ok := e.cfg[p.Server]
    if !ok {
        return Result{}, fmt.Errorf("unknown mcp server: %s", p.Server)
    }

    var raw json.RawMessage
    var err error
    switch cfg.Transport {
    case "stdio":
        raw, err = e.callStdio(ctx, p.Server, cfg, p.Tool, p.Args)
    case "http":
        raw, err = e.callHTTP(ctx, cfg, p.Tool, p.Args)
    default:
        return Result{}, fmt.Errorf("unsupported transport: %s", cfg.Transport)
    }
    if err != nil {
        return Result{}, err
    }

    var tr mcpToolResult
    if err := json.Unmarshal(raw, &tr); err != nil || len(tr.Result) == 0 {
        return Result{}, fmt.Errorf("malformed mcp response")
    }

    summary := stringifyResult(tr.Result)
    change := ""
    if tr.CapabilityChanged {
        change = tr.ChangeHint
        if change == "" {
            change = "unspecified"
        }
    }
    return Result{Summary: summary, CapabilityChange: change}, nil
}

func stringifyResult(r json.RawMessage) string {
    var s string
    if json.Unmarshal(r, &s) == nil {
        return s
    }
    return string(r)
}

// ---------- stdio ----------

type stdioConn struct {
    cmd    *exec.Cmd
    stdin  io.WriteCloser
    stdout *bufio.Reader
    nextID int64
    mu     sync.Mutex
}

func (s *stdioConn) kill() {
    if s.cmd != nil && s.cmd.Process != nil {
        _ = s.cmd.Process.Kill()
        _ = s.cmd.Wait()
    }
}

func (e *MCPExecutor) connStdio(name string, cfg MCPServerCfg) (*stdioConn, error) {
    e.mu.Lock()
    defer e.mu.Unlock()
    if c, ok := e.stdios[name]; ok && c.cmd.ProcessState == nil {
        return c, nil
    }
    cmd := exec.Command(cfg.Command, cfg.Args...)
    for k, v := range cfg.Env {
        cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
    }
    in, err := cmd.StdinPipe()
    if err != nil {
        return nil, err
    }
    out, err := cmd.StdoutPipe()
    if err != nil {
        return nil, err
    }
    if err := cmd.Start(); err != nil {
        return nil, err
    }
    c := &stdioConn{cmd: cmd, stdin: in, stdout: bufio.NewReaderSize(out, 1<<20)}
    e.stdios[name] = c
    return c, nil
}

func (e *MCPExecutor) callStdio(ctx context.Context, name string, cfg MCPServerCfg, tool string, args map[string]interface{}) (json.RawMessage, error) {
    c, err := e.connStdio(name, cfg)
    if err != nil {
        return nil, err
    }
    c.mu.Lock()
    defer c.mu.Unlock()

    id := atomic.AddInt64(&c.nextID, 1)
    req := map[string]interface{}{
        "jsonrpc": "2.0", "id": id, "method": "tools/call",
        "params": map[string]interface{}{"name": tool, "arguments": args},
    }
    line, _ := json.Marshal(req)
    if _, err := c.stdin.Write(append(line, '\n')); err != nil {
        c.kill()
        e.mu.Lock(); delete(e.stdios, name); e.mu.Unlock()
        return nil, fmt.Errorf("mcp stdio write: %w", err)
    }

    type doneT struct {
        b   []byte
        err error
    }
    ch := make(chan doneT, 1)
    go func() {
        b, err := c.stdout.ReadBytes('\n')
        ch <- doneT{b, err}
    }()

    select {
    case <-ctx.Done():
        c.kill()
        e.mu.Lock(); delete(e.stdios, name); e.mu.Unlock()
        return nil, fmt.Errorf("timeout")
    case d := <-ch:
        if d.err != nil {
            c.kill()
            e.mu.Lock(); delete(e.stdios, name); e.mu.Unlock()
            return nil, fmt.Errorf("mcp stdio read: %w", d.err)
        }
        var resp struct {
            Result json.RawMessage `json:"result"`
            Error  *struct {
                Message string `json:"message"`
            } `json:"error"`
        }
        if err := json.Unmarshal(d.b, &resp); err != nil {
            return nil, fmt.Errorf("mcp parse: %w", err)
        }
        if resp.Error != nil {
            return nil, fmt.Errorf("%s", resp.Error.Message)
        }
        return resp.Result, nil
    }
}

// ---------- http (placeholder, fleshed out in Task 14) ----------

func (e *MCPExecutor) callHTTP(ctx context.Context, cfg MCPServerCfg, tool string, args map[string]interface{}) (json.RawMessage, error) {
    return nil, fmt.Errorf("http transport not yet implemented")
}

// suppress unused import warnings if any
var _ = strings.NewReader
```

- [ ] **Step 4: Run — expect PASS**

`go test ./internal/executor/... -v -run TestMCP`

- [ ] **Step 5: Commit**

```bash
git add slave_agent/internal/executor/
git commit -m "feat(slave_agent/executor): MCPExecutor stdio transport + routing"
```

---

## Task 14: `MCPExecutor` — HTTP transport

**Files:**
- Modify: `slave_agent/internal/executor/mcp.go`
- Modify: `slave_agent/internal/executor/mcp_test.go`

- [ ] **Step 1: Failing test using httptest**

```go
import (
    "io"
    "net/http"
    "net/http/httptest"
)

func TestMCP_HTTP_EchoCapability(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        body, _ := io.ReadAll(r.Body)
        var req struct{ Method string; Params struct{ Name string } }
        _ = json.Unmarshal(body, &req)
        var result string
        cap := false
        hint := ""
        switch req.Params.Name {
        case "echo":
            result = `"hello"`
        case "raise":
            result = `"raised"`
            cap = true
            hint = "http-cap"
        }
        resp := map[string]interface{}{"jsonrpc": "2.0", "id": 1,
            "result": map[string]interface{}{
                "result":             json.RawMessage(result),
                "capability_changed": cap,
                "change_hint":        hint,
            }}
        b, _ := json.Marshal(resp)
        w.Write(b)
    }))
    defer srv.Close()

    e := NewMCPExecutor(map[string]MCPServerCfg{"web": {Transport: "http", URL: srv.URL}})
    defer e.Close()

    body, _ := json.Marshal(map[string]string{"server": "web", "tool": "raise"})
    res, err := e.Run(context.Background(), Task{Prompt: string(body)}, &captureSink{})
    require.NoError(t, err)
    require.Equal(t, "http-cap", res.CapabilityChange)
    require.Equal(t, "raised", res.Summary)
}
```

- [ ] **Step 2: Run — expect FAIL**

- [ ] **Step 3: Implement `callHTTP`**

Replace the placeholder `callHTTP`:

```go
func (e *MCPExecutor) callHTTP(ctx context.Context, cfg MCPServerCfg, tool string, args map[string]interface{}) (json.RawMessage, error) {
    body, _ := json.Marshal(map[string]interface{}{
        "jsonrpc": "2.0", "id": 1, "method": "tools/call",
        "params": map[string]interface{}{"name": tool, "arguments": args},
    })
    req, err := http.NewRequestWithContext(ctx, "POST", cfg.URL, strings.NewReader(string(body)))
    if err != nil {
        return nil, err
    }
    req.Header.Set("Content-Type", "application/json")
    for k, v := range cfg.Headers {
        req.Header.Set(k, v)
    }
    resp, err := e.httpCli.Do(req)
    if err != nil {
        return nil, fmt.Errorf("mcp http: %w", err)
    }
    defer resp.Body.Close()
    raw, err := io.ReadAll(resp.Body)
    if err != nil {
        return nil, err
    }
    if resp.StatusCode/100 != 2 {
        return nil, fmt.Errorf("mcp http %d: %s", resp.StatusCode, string(raw))
    }
    var rpc struct {
        Result json.RawMessage `json:"result"`
        Error  *struct{ Message string } `json:"error"`
    }
    if err := json.Unmarshal(raw, &rpc); err != nil {
        return nil, fmt.Errorf("mcp http parse: %w", err)
    }
    if rpc.Error != nil {
        return nil, fmt.Errorf("%s", rpc.Error.Message)
    }
    return rpc.Result, nil
}
```

- [ ] **Step 4: Run — expect PASS**

`go test ./internal/executor/... -v`

- [ ] **Step 5: Commit**

```bash
git add slave_agent/internal/executor/
git commit -m "feat(slave_agent/executor): MCPExecutor HTTP transport"
```

---

## Task 15: `journal` package

**Files:**
- Create: `slave_agent/internal/journal/journal.go`
- Create: `slave_agent/internal/journal/journal_test.go`

- [ ] **Step 1: Failing tests**

```go
// journal_test.go
package journal

import (
    "context"
    "os"
    "path/filepath"
    "runtime"
    "strings"
    "testing"

    "github.com/stretchr/testify/require"
    "github.com/yourorg/slave_agent/internal/executor"
)

func fakeTextClaude(t *testing.T) string {
    t.Helper()
    _, file, _, _ := runtime.Caller(0)
    p, err := filepath.Abs(filepath.Join(filepath.Dir(file), "../../testdata/fake-claude-text.sh"))
    require.NoError(t, err)
    return p
}

func TestRecord_FirstWriteCreatesFile(t *testing.T) {
    dir := t.TempDir()
    j, err := New(Config{Dir: dir, ClaudeBin: fakeTextClaude(t)})
    require.NoError(t, err)
    err = j.Record(context.Background(), executor.Task{ID: "t1", Skill: "mcp"}, executor.Result{CapabilityChange: "did x"})
    require.NoError(t, err)

    body, err := os.ReadFile(filepath.Join(dir, "CURRENT_STATE.md"))
    require.NoError(t, err)
    require.Contains(t, string(body), "updated by fake merge")

    hist, err := os.ReadFile(filepath.Join(dir, "history.md"))
    require.NoError(t, err)
    require.Contains(t, string(hist), "t1")
    require.Contains(t, string(hist), "did x")
}

func TestRecord_MergeFailureStillAppendsHistory(t *testing.T) {
    dir := t.TempDir()
    j, _ := New(Config{Dir: dir, ClaudeBin: fakeTextClaude(t), Env: []string{"FAKE_CLAUDE_TEXT_MODE=fail"}})
    err := j.Record(context.Background(), executor.Task{ID: "t2", Skill: "chat"}, executor.Result{CapabilityChange: "tried"})
    require.NoError(t, err) // journal.Record never errors out: it logs and degrades

    _, err = os.Stat(filepath.Join(dir, "CURRENT_STATE.md"))
    require.True(t, os.IsNotExist(err), "CURRENT_STATE.md must NOT be created on merge failure")

    hist, _ := os.ReadFile(filepath.Join(dir, "history.md"))
    require.Contains(t, string(hist), "[merge failed:")
    require.Contains(t, string(hist), "tried")
}

// helper to silence imports
var _ = strings.HasPrefix
```

- [ ] **Step 2: Run — expect FAIL**

- [ ] **Step 3: Implement `journal.go`**

```go
package journal

import (
    "context"
    "fmt"
    "io"
    "os"
    "os/exec"
    "path/filepath"
    "strings"
    "sync"
    "time"

    "github.com/yourorg/slave_agent/internal/executor"
)

type Config struct {
    Dir       string
    ClaudeBin string
    Env       []string
}

type Journal struct {
    cfg Config
    mu  sync.Mutex
}

func New(cfg Config) (*Journal, error) {
    if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
        return nil, err
    }
    return &Journal{cfg: cfg}, nil
}

func (j *Journal) Record(ctx context.Context, t executor.Task, r executor.Result) error {
    if r.CapabilityChange == "" {
        return nil
    }
    j.mu.Lock()
    defer j.mu.Unlock()

    csPath := filepath.Join(j.cfg.Dir, "CURRENT_STATE.md")
    current, _ := os.ReadFile(csPath)

    merged, mergeErr := j.callClaude(ctx, string(current), t, r.CapabilityChange)
    histLine := j.histLine(t, r.CapabilityChange, mergeErr)
    j.appendHistory(histLine)

    if mergeErr != nil {
        return nil // history.md already records the failure
    }
    return atomicWrite(csPath, []byte(merged))
}

func (j *Journal) callClaude(ctx context.Context, currentDoc string, t executor.Task, change string) (string, error) {
    prompt := fmt.Sprintf(
        "Current CURRENT_STATE.md:\n%s\n\nJust executed task %s (skill=%s) with capability impact:\n%s\n\nOutput the updated CURRENT_STATE.md in full. Group with H2 (## Tools, ## MCP Servers, ## Mounted Resources, ## Credentials). Only modify affected sections. Be terse.",
        currentDoc, t.ID, t.Skill, change,
    )
    cmd := exec.CommandContext(ctx, j.cfg.ClaudeBin, "--print")
    cmd.Env = append(cmd.Environ(), j.cfg.Env...)
    cmd.Stdin = strings.NewReader(prompt)
    out, err := cmd.Output()
    if err != nil {
        return "", err
    }
    return string(out), nil
}

func (j *Journal) histLine(t executor.Task, change string, mergeErr error) string {
    ts := time.Now().UTC().Format(time.RFC3339)
    if mergeErr != nil {
        return fmt.Sprintf("| %s | %s | %s | [merge failed: %v] %s |\n", ts, t.ID, t.Skill, mergeErr, change)
    }
    return fmt.Sprintf("| %s | %s | %s | %s |\n", ts, t.ID, t.Skill, change)
}

func (j *Journal) appendHistory(line string) {
    p := filepath.Join(j.cfg.Dir, "history.md")
    f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
    if err != nil {
        return
    }
    defer f.Close()
    io.WriteString(f, line)
}

func atomicWrite(path string, data []byte) error {
    tmp := path + ".tmp"
    if err := os.WriteFile(tmp, data, 0o644); err != nil {
        return err
    }
    return os.Rename(tmp, path)
}
```

- [ ] **Step 4: Run — expect PASS**

`go test ./internal/journal/... -v`

- [ ] **Step 5: Commit**

```bash
git add slave_agent/internal/journal/
git commit -m "feat(slave_agent/journal): CURRENT_STATE merge + history append"
```

---

## Task 16: `dispatch` package

**Files:**
- Create: `slave_agent/internal/dispatch/dispatch.go`
- Create: `slave_agent/internal/dispatch/dispatch_test.go`

- [ ] **Step 1: Failing tests**

```go
// dispatch_test.go
package dispatch

import (
    "context"
    "errors"
    "path/filepath"
    "testing"

    "github.com/stretchr/testify/require"
    "github.com/yourorg/slave_agent/internal/executor"
    "github.com/yourorg/slave_agent/internal/store"
)

type stubExec struct {
    res executor.Result
    err error
    called bool
}

func (s *stubExec) Run(ctx context.Context, t executor.Task, sink executor.Sink) (executor.Result, error) {
    s.called = true
    sink.Close()
    return s.res, s.err
}

type stubJournal struct{ calls int; lastChange string }

func (j *stubJournal) Record(ctx context.Context, t executor.Task, r executor.Result) error {
    j.calls++
    j.lastChange = r.CapabilityChange
    return nil
}

func newStore(t *testing.T) *store.Store {
    s, err := store.Open(filepath.Join(t.TempDir(), "x.db"))
    require.NoError(t, err)
    t.Cleanup(func() { s.Close() })
    return s
}

func TestRoute_DefaultExecutor(t *testing.T) {
    def := &stubExec{res: executor.Result{Summary: "ok"}}
    mcp := &stubExec{}
    j := &stubJournal{}
    d := New(map[string]executor.Executor{"mcp": mcp, "": def}, j, newStore(t))
    _, err := d.Run(context.Background(), executor.Task{ID: "t", Skill: "chat"})
    require.NoError(t, err)
    require.True(t, def.called)
    require.False(t, mcp.called)
}

func TestRoute_MCPSkill(t *testing.T) {
    def := &stubExec{}
    mcp := &stubExec{res: executor.Result{Summary: "m"}}
    d := New(map[string]executor.Executor{"mcp": mcp, "": def}, &stubJournal{}, newStore(t))
    _, err := d.Run(context.Background(), executor.Task{ID: "t", Skill: "mcp"})
    require.NoError(t, err)
    require.True(t, mcp.called)
    require.False(t, def.called)
}

func TestFailed_SkipsJournal(t *testing.T) {
    def := &stubExec{err: errors.New("bad")}
    j := &stubJournal{}
    d := New(map[string]executor.Executor{"": def}, j, newStore(t))
    _, err := d.Run(context.Background(), executor.Task{ID: "t"})
    require.Error(t, err)
    require.Equal(t, 0, j.calls)
}

func TestNoCapabilityChange_SkipsJournal(t *testing.T) {
    def := &stubExec{res: executor.Result{Summary: "ok"}}
    j := &stubJournal{}
    d := New(map[string]executor.Executor{"": def}, j, newStore(t))
    _, err := d.Run(context.Background(), executor.Task{ID: "t"})
    require.NoError(t, err)
    require.Equal(t, 0, j.calls)
}

func TestCapabilityChange_CallsJournal(t *testing.T) {
    def := &stubExec{res: executor.Result{Summary: "ok", CapabilityChange: "x"}}
    j := &stubJournal{}
    d := New(map[string]executor.Executor{"": def}, j, newStore(t))
    _, err := d.Run(context.Background(), executor.Task{ID: "t"})
    require.NoError(t, err)
    require.Equal(t, 1, j.calls)
    require.Equal(t, "x", j.lastChange)
}
```

- [ ] **Step 2: Run — expect FAIL**

- [ ] **Step 3: Implement `dispatch.go`**

```go
package dispatch

import (
    "context"
    "fmt"

    "github.com/yourorg/slave_agent/internal/executor"
    "github.com/yourorg/slave_agent/internal/store"
)

type JournalRecorder interface {
    Record(ctx context.Context, t executor.Task, r executor.Result) error
}

type Dispatcher struct {
    routes  map[string]executor.Executor
    journal JournalRecorder
    store   *store.Store
}

func New(routes map[string]executor.Executor, j JournalRecorder, s *store.Store) *Dispatcher {
    return &Dispatcher{routes: routes, journal: j, store: s}
}

func (d *Dispatcher) Run(ctx context.Context, t executor.Task) (executor.Result, error) {
    if err := d.store.Insert(store.Task{ID: t.ID, Skill: t.Skill, Prompt: t.Prompt}); err != nil {
        return executor.Result{}, err
    }
    if err := d.store.MarkRunning(t.ID); err != nil {
        return executor.Result{}, err
    }

    exec, ok := d.routes[t.Skill]
    if !ok {
        exec = d.routes[""]
    }
    if exec == nil {
        err := fmt.Errorf("no executor for skill %q", t.Skill)
        _ = d.store.Fail(t.ID, err.Error())
        return executor.Result{}, err
    }

    sink := d.store.ChunkSink(t.ID)
    res, err := exec.Run(ctx, t, sink)
    if err != nil {
        _ = d.store.Fail(t.ID, err.Error())
        return executor.Result{}, err
    }
    if err := d.store.Complete(t.ID, res.Summary); err != nil {
        return res, err
    }

    if res.CapabilityChange != "" {
        if jerr := d.journal.Record(ctx, t, res); jerr != nil {
            // logged, but does not fail the task
            _ = jerr
        }
    }
    return res, nil
}
```

- [ ] **Step 4: Run — expect PASS**

`go test ./internal/dispatch/... -v`

- [ ] **Step 5: Commit**

```bash
git add slave_agent/internal/dispatch/
git commit -m "feat(slave_agent/dispatch): skill routing + lifecycle"
```

---

## Task 17: `poller` — happy path with mocked agentserver

**Files:**
- Create: `slave_agent/internal/poller/poller.go`
- Create: `slave_agent/internal/poller/poller_test.go`

- [ ] **Step 1: Failing test**

```go
// poller_test.go
package poller

import (
    "context"
    "encoding/json"
    "io"
    "net/http"
    "net/http/httptest"
    "path/filepath"
    "sync/atomic"
    "testing"
    "time"

    "github.com/stretchr/testify/require"
    "github.com/yourorg/slave_agent/internal/dispatch"
    "github.com/yourorg/slave_agent/internal/executor"
    "github.com/yourorg/slave_agent/internal/store"
)

type echoExec struct{}

func (echoExec) Run(ctx context.Context, t executor.Task, sink executor.Sink) (executor.Result, error) {
    sink.Close()
    return executor.Result{Summary: "echo: " + t.Prompt}, nil
}

type stubJ struct{}
func (stubJ) Record(context.Context, executor.Task, executor.Result) error { return nil }

func TestPoller_PollsAndCompletes(t *testing.T) {
    var taskHandedOut atomic.Bool
    var statuses []string
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/api/agent/tasks/poll" {
            if !taskHandedOut.CompareAndSwap(false, true) {
                w.WriteHeader(204)
                return
            }
            w.Header().Set("Content-Type", "application/json")
            w.Write([]byte(`{"task_id":"t1","skill":"chat","prompt":"hi","timeout_seconds":30}`))
            return
        }
        // /api/agent/tasks/{id}/status
        body, _ := io.ReadAll(r.Body)
        var msg struct{ Status string }
        _ = json.Unmarshal(body, &msg)
        statuses = append(statuses, msg.Status)
        w.WriteHeader(200)
    }))
    defer srv.Close()

    s, _ := store.Open(filepath.Join(t.TempDir(), "x.db"))
    defer s.Close()
    d := dispatch.New(map[string]executor.Executor{"": echoExec{}}, stubJ{}, s)

    p := New(Config{ServerURL: srv.URL, ProxyToken: "ptoken", IdlePoll: 50 * time.Millisecond, ActivePoll: 10 * time.Millisecond}, d, s)

    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()
    go p.Run(ctx)

    require.Eventually(t, func() bool {
        return len(statuses) >= 2 // running + completed
    }, time.Second, 20*time.Millisecond)
    require.Equal(t, "running", statuses[0])
    require.Equal(t, "completed", statuses[1])
}
```

- [ ] **Step 2: Run — expect FAIL**

- [ ] **Step 3: Implement `poller.go`**

```go
package poller

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "time"

    "github.com/yourorg/slave_agent/internal/dispatch"
    "github.com/yourorg/slave_agent/internal/executor"
    "github.com/yourorg/slave_agent/internal/store"
)

type Config struct {
    ServerURL  string
    ProxyToken string
    IdlePoll   time.Duration // default 5s, escalates to 30s after 5 min idle
    ActivePoll time.Duration // default 0 (poll immediately after task completes)
}

type Poller struct {
    cfg  Config
    cli  *http.Client
    disp *dispatch.Dispatcher
    s    *store.Store
}

func New(cfg Config, d *dispatch.Dispatcher, s *store.Store) *Poller {
    if cfg.IdlePoll == 0 {
        cfg.IdlePoll = 5 * time.Second
    }
    return &Poller{cfg: cfg, cli: &http.Client{Timeout: 10 * time.Second}, disp: d, s: s}
}

type pollTask struct {
    TaskID         string `json:"task_id"`
    Skill          string `json:"skill"`
    Prompt         string `json:"prompt"`
    SystemContext  string `json:"system_context"`
    TimeoutSeconds int    `json:"timeout_seconds"`
}

func (p *Poller) Run(ctx context.Context) error {
    var idleSince time.Time // zero = not currently idle
    for {
        if err := ctx.Err(); err != nil {
            return nil
        }
        p.drainPendingAcks(ctx)

        t, ok, err := p.poll(ctx)
        if err != nil {
            select {
            case <-ctx.Done():
                return nil
            case <-time.After(p.cfg.IdlePoll):
            }
            continue
        }
        if !ok {
            if idleSince.IsZero() {
                idleSince = time.Now()
            }
            backoff := p.cfg.IdlePoll
            if time.Since(idleSince) > 5*time.Minute {
                backoff = 30 * time.Second
            }
            select {
            case <-ctx.Done():
                return nil
            case <-time.After(backoff):
            }
            continue
        }
        idleSince = time.Time{}
        p.execute(ctx, t)
    }
}

func (p *Poller) poll(ctx context.Context) (pollTask, bool, error) {
    req, _ := http.NewRequestWithContext(ctx, "GET", p.cfg.ServerURL+"/api/agent/tasks/poll", nil)
    req.Header.Set("Authorization", "Bearer "+p.cfg.ProxyToken)
    resp, err := p.cli.Do(req)
    if err != nil {
        return pollTask{}, false, err
    }
    defer resp.Body.Close()
    if resp.StatusCode == 204 {
        return pollTask{}, false, nil
    }
    if resp.StatusCode != 200 {
        return pollTask{}, false, fmt.Errorf("poll status %d", resp.StatusCode)
    }
    var t pollTask
    body, _ := io.ReadAll(resp.Body)
    if err := json.Unmarshal(body, &t); err != nil {
        return pollTask{}, false, err
    }
    return t, true, nil
}

func (p *Poller) execute(ctx context.Context, t pollTask) {
    p.putStatus(ctx, t.TaskID, map[string]interface{}{"status": "running"})
    res, err := p.disp.Run(ctx, executor.Task{
        ID: t.TaskID, Skill: t.Skill, Prompt: t.Prompt,
        SystemContext: t.SystemContext, TimeoutSec: t.TimeoutSeconds,
    })
    if err != nil {
        if !p.putStatusRetry(ctx, t.TaskID, map[string]interface{}{
            "status": "failed", "failure_reason": err.Error(),
        }) {
            _ = p.s.EnqueuePendingAck(t.TaskID, "failed")
        }
        return
    }
    if !p.putStatusRetry(ctx, t.TaskID, map[string]interface{}{
        "status": "completed", "output": res.Summary,
    }) {
        _ = p.s.EnqueuePendingAck(t.TaskID, "completed")
    }
}

func (p *Poller) putStatus(ctx context.Context, id string, body map[string]interface{}) error {
    b, _ := json.Marshal(body)
    req, _ := http.NewRequestWithContext(ctx, "PUT", p.cfg.ServerURL+"/api/agent/tasks/"+id+"/status", bytes.NewReader(b))
    req.Header.Set("Authorization", "Bearer "+p.cfg.ProxyToken)
    req.Header.Set("Content-Type", "application/json")
    resp, err := p.cli.Do(req)
    if err != nil {
        return err
    }
    resp.Body.Close()
    if resp.StatusCode/100 != 2 {
        return fmt.Errorf("status update %d", resp.StatusCode)
    }
    return nil
}

func (p *Poller) putStatusRetry(ctx context.Context, id string, body map[string]interface{}) bool {
    backoffs := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}
    for i := 0; i <= len(backoffs); i++ {
        if err := p.putStatus(ctx, id, body); err == nil {
            return true
        }
        if i == len(backoffs) {
            return false
        }
        select {
        case <-ctx.Done():
            return false
        case <-time.After(backoffs[i]):
        }
    }
    return false
}

func (p *Poller) drainPendingAcks(ctx context.Context) {
    pa, err := p.s.PopPendingAcks()
    if err != nil {
        return
    }
    for _, a := range pa {
        body := map[string]interface{}{"status": a.Status}
        if a.Status == "failed" {
            body["failure_reason"] = a.Reason
        } else {
            body["output"] = a.Reason
        }
        if p.putStatus(ctx, a.TaskID, body) == nil {
            _ = p.s.DeletePendingAck(a.TaskID)
        }
    }
}
```

- [ ] **Step 4: Run — expect PASS**

`go test ./internal/poller/... -v -race`

- [ ] **Step 5: Commit**

```bash
git add slave_agent/internal/poller/
git commit -m "feat(slave_agent/poller): polling loop + retries + pending acks"
```

---

## Task 18: `tunnel` — agentsdk wrapper + device flow

**Files:**
- Create: `slave_agent/internal/tunnel/tunnel.go`
- Create: `slave_agent/internal/tunnel/tunnel_test.go`

The agentsdk gives us `RequestDeviceCode`, `PollForToken`, `NewClient`, `Register`, `SetRegistration`, `Connect`. We thinly wrap so we can swap them in tests.

- [ ] **Step 1: Test for register-when-creds-missing**

```go
// tunnel_test.go
package tunnel

import (
    "context"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "path/filepath"
    "testing"

    "github.com/stretchr/testify/require"
    "github.com/yourorg/slave_agent/internal/config"
)

func TestEnsureRegistered_PersistsCredentials(t *testing.T) {
    // Mock device flow + register
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        switch r.URL.Path {
        case "/api/oauth2/device/auth":
            json.NewEncoder(w).Encode(map[string]interface{}{
                "device_code": "dc", "user_code": "UC",
                "verification_uri":         srv_url(r) + "/device",
                "verification_uri_complete": srv_url(r) + "/device?user_code=UC",
                "expires_in": 60, "interval": 1,
            })
        case "/api/oauth2/token":
            json.NewEncoder(w).Encode(map[string]interface{}{
                "access_token": "atk", "token_type": "Bearer", "expires_in": 3600,
            })
        case "/api/agent/register":
            json.NewEncoder(w).Encode(map[string]interface{}{
                "sandbox_id": "sb-1", "tunnel_token": "tt", "proxy_token": "pt",
                "short_id": "sid", "workspace_id": "ws",
            })
        }
    }))
    defer srv.Close()

    cfgPath := filepath.Join(t.TempDir(), "c.yaml")
    c := &config.Config{Server: config.Server{URL: srv.URL, Name: "n"}}
    require.NoError(t, c.Save(cfgPath))

    tn := NewWithDeps(c, cfgPath, http.DefaultServeMux, Deps{
        Open: func(string) error { return nil },
    })
    require.NoError(t, tn.EnsureRegistered(context.Background()))

    reloaded, _ := config.Load(cfgPath)
    require.Equal(t, "sb-1", reloaded.Credentials.SandboxID)
    require.Equal(t, "pt", reloaded.Credentials.ProxyToken)
    require.Equal(t, "sid", reloaded.Credentials.ShortID)
}

func srv_url(r *http.Request) string {
    if r.TLS != nil {
        return "https://" + r.Host
    }
    return "http://" + r.Host
}
```

- [ ] **Step 2: Run — expect FAIL**

- [ ] **Step 3: Implement `tunnel.go`**

```go
package tunnel

import (
    "context"
    "fmt"
    "net/http"
    "time"

    "github.com/agentserver/agentserver/pkg/agentsdk"
    "github.com/yourorg/slave_agent/internal/config"
)

type Deps struct {
    // Open is called to display the device verification URL to the user.
    // In production this prints to stdout; tests inject a no-op.
    Open func(url string) error
}

type Tunnel struct {
    cfg     *config.Config
    cfgPath string
    http    http.Handler
    deps    Deps
    sdk     *agentsdk.Client
}

func New(cfg *config.Config, cfgPath string, h http.Handler) *Tunnel {
    return NewWithDeps(cfg, cfgPath, h, Deps{
        Open: func(u string) error {
            fmt.Printf("\nOpen this URL to authenticate:\n\n    %s\n\n", u)
            return nil
        },
    })
}

func NewWithDeps(cfg *config.Config, cfgPath string, h http.Handler, deps Deps) *Tunnel {
    return &Tunnel{cfg: cfg, cfgPath: cfgPath, http: h, deps: deps}
}

func (t *Tunnel) EnsureRegistered(ctx context.Context) error {
    if t.cfg.Credentials.SandboxID != "" && t.cfg.Credentials.TunnelToken != "" {
        return nil
    }
    dc, err := agentsdk.RequestDeviceCode(ctx, t.cfg.Server.URL)
    if err != nil {
        return fmt.Errorf("device code: %w", err)
    }
    if err := t.deps.Open(dc.VerificationURIComplete); err != nil {
        return err
    }
    tok, err := agentsdk.PollForToken(ctx, t.cfg.Server.URL, dc)
    if err != nil {
        return fmt.Errorf("token: %w", err)
    }
    cli := agentsdk.NewClient(agentsdk.Config{ServerURL: t.cfg.Server.URL, Name: t.cfg.Server.Name})
    reg, err := cli.Register(ctx, tok.AccessToken)
    if err != nil {
        return fmt.Errorf("register: %w", err)
    }
    t.cfg.Credentials.SandboxID = reg.SandboxID
    t.cfg.Credentials.TunnelToken = reg.TunnelToken
    t.cfg.Credentials.ProxyToken = reg.ProxyToken
    t.cfg.Credentials.ShortID = reg.ShortID
    t.sdk = cli
    return t.cfg.Save(t.cfgPath)
}

func (t *Tunnel) PublishCard(ctx context.Context) error {
    // Best effort: non-fatal per spec §5.2
    if t.sdk == nil {
        t.sdk = agentsdk.NewClient(agentsdk.Config{ServerURL: t.cfg.Server.URL, Name: t.cfg.Server.Name})
        t.sdk.SetRegistration(t.cfg.Credentials.SandboxID, t.cfg.Credentials.TunnelToken, t.cfg.Credentials.ProxyToken, t.cfg.Credentials.ShortID)
    }
    return t.sdk.PublishDiscoveryCard(ctx, agentsdk.DiscoveryCard{
        DisplayName: t.cfg.Discovery.DisplayName,
        Description: t.cfg.Discovery.Description,
        AgentType:   "custom",
        Card: map[string]interface{}{
            "skills":        t.cfg.Discovery.Skills,
            "accepts_tasks": true,
            "has_web_ui":    true,
            "version":       "0.1.0",
        },
    })
}

func (t *Tunnel) Run(ctx context.Context) error {
    if t.sdk == nil {
        t.sdk = agentsdk.NewClient(agentsdk.Config{ServerURL: t.cfg.Server.URL, Name: t.cfg.Server.Name})
        t.sdk.SetRegistration(t.cfg.Credentials.SandboxID, t.cfg.Credentials.TunnelToken, t.cfg.Credentials.ProxyToken, t.cfg.Credentials.ShortID)
    }
    return t.sdk.Connect(ctx, agentsdk.Handlers{
        HTTP:         t.http,
        OnConnect:    func() {},
        OnDisconnect: func(error) {},
    })
}

// silence
var _ = time.Second
```

> If the agentsdk's exact method names differ (e.g. `PublishDiscoveryCard` may be called `RegisterCard`), check `agentserver/pkg/agentsdk` and adjust. The spec §3 + §8 describe the wire calls; the SDK helpers around them may have any name.

- [ ] **Step 4: Run — expect PASS**

`go test ./internal/tunnel/... -v`

If the SDK refuses to point at `httptest` (e.g., hardcodes `https://`), wrap the SDK functions behind a small interface in `Deps` and inject an in-test fake that hits the test server directly.

- [ ] **Step 5: Commit**

```bash
git add slave_agent/internal/tunnel/
git commit -m "feat(slave_agent/tunnel): device flow + register + publish card"
```

---

## Task 19: `webui` — handlers

**Files:**
- Create: `slave_agent/internal/webui/server.go`
- Create: `slave_agent/internal/webui/templates/dashboard.html`
- Create: `slave_agent/internal/webui/server_test.go`

- [ ] **Step 1: Failing tests**

```go
// server_test.go
package webui

import (
    "bufio"
    "context"
    "net/http"
    "net/http/httptest"
    "os"
    "path/filepath"
    "strings"
    "testing"
    "time"

    "github.com/stretchr/testify/require"
    "github.com/yourorg/slave_agent/internal/config"
    "github.com/yourorg/slave_agent/internal/store"
)

func openStore(t *testing.T) *store.Store {
    s, err := store.Open(filepath.Join(t.TempDir(), "x.db"))
    require.NoError(t, err)
    t.Cleanup(func() { s.Close() })
    return s
}

func TestHealthz(t *testing.T) {
    s := openStore(t)
    h := NewHandler(s, "", &config.Config{})
    rr := httptest.NewRecorder()
    h.ServeHTTP(rr, httptest.NewRequest("GET", "/healthz", nil))
    require.Equal(t, 200, rr.Code)
    require.Contains(t, rr.Body.String(), `"ok":true`)
}

func TestTasks_404(t *testing.T) {
    s := openStore(t)
    h := NewHandler(s, "", &config.Config{})
    rr := httptest.NewRecorder()
    h.ServeHTTP(rr, httptest.NewRequest("GET", "/tasks/nonexistent", nil))
    require.Equal(t, 404, rr.Code)
}

func TestState_RendersJournal(t *testing.T) {
    s := openStore(t)
    dir := t.TempDir()
    require.NoError(t, os.WriteFile(filepath.Join(dir, "CURRENT_STATE.md"), []byte("## Tools\n- a"), 0o644))
    h := NewHandler(s, dir, &config.Config{})
    rr := httptest.NewRecorder()
    h.ServeHTTP(rr, httptest.NewRequest("GET", "/state", nil))
    require.Equal(t, 200, rr.Code)
    require.Contains(t, rr.Body.String(), "## Tools")
    require.Equal(t, "text/markdown; charset=utf-8", rr.Header().Get("Content-Type"))
}

func TestStream_LiveAndDone(t *testing.T) {
    s := openStore(t)
    require.NoError(t, s.Insert(store.Task{ID: "live"}))
    h := NewHandler(s, "", &config.Config{})

    srv := httptest.NewServer(h)
    defer srv.Close()

    // Open SSE in background.
    req, _ := http.NewRequest("GET", srv.URL+"/tasks/live/stream", nil)
    resp, err := http.DefaultClient.Do(req)
    require.NoError(t, err)
    defer resp.Body.Close()

    // Push events.
    sink := s.ChunkSink("live")
    sink.Write("chunk", "hello")
    sink.Close()

    sc := bufio.NewScanner(resp.Body)
    seen := []string{}
    deadline := time.Now().Add(2 * time.Second)
    for sc.Scan() && time.Now().Before(deadline) {
        line := sc.Text()
        if strings.HasPrefix(line, "event: ") {
            seen = append(seen, strings.TrimPrefix(line, "event: "))
            if seen[len(seen)-1] == "done" {
                break
            }
        }
    }
    require.Contains(t, seen, "chunk")
    require.Contains(t, seen, "done")
}

var _ = context.Background
```

- [ ] **Step 2: Run — expect FAIL**

- [ ] **Step 3: Implement `server.go`**

```go
package webui

import (
    "encoding/json"
    "fmt"
    "net/http"
    "os"
    "path"
    "path/filepath"
    "strings"
    "time"

    "github.com/yourorg/slave_agent/internal/config"
    "github.com/yourorg/slave_agent/internal/store"
)

type Handler struct {
    s          *store.Store
    journalDir string
    cfg        *config.Config
    started    time.Time
}

func NewHandler(s *store.Store, journalDir string, cfg *config.Config) http.Handler {
    h := &Handler{s: s, journalDir: journalDir, cfg: cfg, started: time.Now()}
    mux := http.NewServeMux()
    mux.HandleFunc("/healthz", h.healthz)
    mux.HandleFunc("/state", h.state)
    mux.HandleFunc("/tasks", h.listTasks)
    mux.HandleFunc("/tasks/", h.taskRouter)
    mux.HandleFunc("/", h.dashboard)
    return mux
}

func (h *Handler) healthz(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    fmt.Fprintf(w, `{"ok":true,"uptime_s":%d}`, int(time.Since(h.started).Seconds()))
}

func (h *Handler) state(w http.ResponseWriter, r *http.Request) {
    if h.journalDir == "" {
        w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
        return
    }
    data, err := os.ReadFile(filepath.Join(h.journalDir, "CURRENT_STATE.md"))
    if err != nil && !os.IsNotExist(err) {
        http.Error(w, err.Error(), 500)
        return
    }
    w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
    w.Write(data)
}

func (h *Handler) listTasks(w http.ResponseWriter, r *http.Request) {
    tasks, err := h.s.ListTasks(50, 0)
    if err != nil {
        http.Error(w, err.Error(), 500)
        return
    }
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(tasks)
}

func (h *Handler) taskRouter(w http.ResponseWriter, r *http.Request) {
    rest := strings.TrimPrefix(r.URL.Path, "/tasks/")
    parts := strings.SplitN(rest, "/", 2)
    id := parts[0]
    if len(parts) == 2 && parts[1] == "stream" {
        h.stream(w, r, id)
        return
    }
    h.taskDetail(w, r, id)
}

func (h *Handler) taskDetail(w http.ResponseWriter, r *http.Request, id string) {
    row, chunks, err := h.s.GetTaskWithChunks(id)
    if err != nil {
        http.Error(w, `{"error":"not found"}`, 404)
        return
    }
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]interface{}{"task": row, "chunks": chunks})
}

func (h *Handler) stream(w http.ResponseWriter, r *http.Request, id string) {
    flusher, ok := w.(http.Flusher)
    if !ok {
        http.Error(w, "stream unsupported", 500)
        return
    }
    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")
    w.Header().Set("Connection", "keep-alive")

    row, chunks, err := h.s.GetTaskWithChunks(id)
    if err != nil {
        http.Error(w, `{"error":"not found"}`, 404)
        return
    }
    for _, c := range chunks {
        writeSSE(w, string(c.Type), c.Data)
    }
    flusher.Flush()

    if row.Status == "completed" || row.Status == "failed" {
        writeSSE(w, "done", "")
        flusher.Flush()
        return
    }

    ch, cancel := h.s.Subscribe(id)
    defer cancel()
    for {
        select {
        case <-r.Context().Done():
            return
        case ev, ok := <-ch:
            if !ok {
                writeSSE(w, "done", "")
                flusher.Flush()
                return
            }
            writeSSE(w, string(ev.Type), ev.Data)
            flusher.Flush()
            if ev.Type == store.EventDone {
                return
            }
        }
    }
}

func writeSSE(w http.ResponseWriter, ev, data string) {
    fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev, strings.ReplaceAll(data, "\n", "\\n"))
}

func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request) {
    if r.URL.Path != "/" {
        http.Error(w, `{"error":"not found"}`, 404)
        return
    }
    tasks, _ := h.s.ListTasks(20, 0)
    state := ""
    if h.journalDir != "" {
        b, _ := os.ReadFile(filepath.Join(h.journalDir, "CURRENT_STATE.md"))
        state = string(b)
    }
    w.Header().Set("Content-Type", "text/html; charset=utf-8")
    fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>%s</title><h1>%s</h1>", h.cfg.Discovery.DisplayName, h.cfg.Discovery.DisplayName)
    fmt.Fprintf(w, "<h2>Tasks</h2><ul>")
    for _, t := range tasks {
        fmt.Fprintf(w, "<li><a href=\"%s\">%s</a> [%s] %s</li>",
            path.Join("/tasks", t.ID), t.ID, t.Status, htmlEscape(t.Skill))
    }
    fmt.Fprintf(w, "</ul><h2>State</h2><pre>%s</pre>", htmlEscape(state))
}

func htmlEscape(s string) string {
    return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
```

(`templates/dashboard.html` is intentionally omitted: dashboard is rendered inline. Create the empty file only if you want to add a template later.)

- [ ] **Step 4: Run — expect PASS**

`go test ./internal/webui/... -v`

- [ ] **Step 5: Commit**

```bash
git add slave_agent/internal/webui/
git commit -m "feat(slave_agent/webui): handlers + SSE stream"
```

---

## Task 20: `cmd/slave-agent/main.go` — wire it all up

**Files:**
- Modify: `slave_agent/cmd/slave-agent/main.go`

- [ ] **Step 1: Replace placeholder**

```go
package main

import (
    "context"
    "fmt"
    "log"
    "os"
    "os/signal"
    "path/filepath"
    "syscall"

    "golang.org/x/sync/errgroup"

    "github.com/yourorg/slave_agent/internal/config"
    "github.com/yourorg/slave_agent/internal/dispatch"
    "github.com/yourorg/slave_agent/internal/executor"
    "github.com/yourorg/slave_agent/internal/journal"
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
        log.Fatalf("slave_agent: %v", err)
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

    journalDir, _ := filepath.Abs("journal")
    j, err := journal.New(journal.Config{Dir: journalDir, ClaudeBin: cfg.Claude.Bin})
    if err != nil {
        return err
    }

    mcpCfg := map[string]executor.MCPServerCfg{}
    for name, m := range cfg.MCPServers {
        mcpCfg[name] = executor.MCPServerCfg{
            Transport: m.Transport, Command: m.Command, Args: m.Args, Env: m.Env,
            URL: m.URL, Headers: m.Headers,
        }
    }
    mcpExec := executor.NewMCPExecutor(mcpCfg)
    defer mcpExec.Close()
    claudeExec := executor.NewClaudeExecutor(executor.ClaudeConfig{
        Bin: cfg.Claude.Bin, WorkDir: cfg.Claude.WorkDir, Args: cfg.Claude.Args,
    })

    routes := map[string]executor.Executor{
        "mcp": mcpExec,
        "":    claudeExec,
    }
    d := dispatch.New(routes, j, s)

    ui := webui.NewHandler(s, journalDir, cfg)

    ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer cancel()

    tn := tunnel.New(cfg, cfgPath, ui)
    if err := tn.EnsureRegistered(ctx); err != nil {
        return err
    }
    if err := tn.PublishCard(ctx); err != nil {
        log.Printf("publish card: %v (continuing)", err)
    }

    p := poller.New(poller.Config{
        ServerURL: cfg.Server.URL, ProxyToken: cfg.Credentials.ProxyToken,
    }, d, s)

    g, gctx := errgroup.WithContext(ctx)
    g.Go(func() error { return tn.Run(gctx) })
    g.Go(func() error { return p.Run(gctx) })

    err = g.Wait()
    if err != nil && err != context.Canceled {
        return fmt.Errorf("run: %w", err)
    }
    return nil
}
```

- [ ] **Step 2: Add dependency**

```bash
cd slave_agent && go get golang.org/x/sync@latest
```

- [ ] **Step 3: Build**

```bash
go build ./...
```
Expected: clean build.

- [ ] **Step 4: Commit**

```bash
git add slave_agent/cmd slave_agent/go.mod slave_agent/go.sum
git commit -m "feat(slave_agent): wire main"
```

---

## Task 21: Smoke test — real claude

**Files:**
- Create: `slave_agent/tests/smoke/doc.go`
- Create: `slave_agent/tests/smoke/claude_smoke_test.go`

- [ ] **Step 1: Build tag file**

```go
// doc.go
//go:build smoke

package smoke
```

- [ ] **Step 2: Smoke test**

```go
//go:build smoke

package smoke

import (
    "context"
    "os"
    "os/exec"
    "strings"
    "testing"
    "time"

    "github.com/stretchr/testify/require"
    "github.com/yourorg/slave_agent/internal/executor"
)

type captureSink struct{ data strings.Builder; closed bool }

func (c *captureSink) Write(_, d string) { c.data.WriteString(d) }
func (c *captureSink) Close()            { c.closed = true }

func TestSmoke_RealClaude(t *testing.T) {
    if os.Getenv("ANTHROPIC_API_KEY") == "" {
        t.Skip("ANTHROPIC_API_KEY not set")
    }
    if _, err := exec.LookPath("claude"); err != nil {
        t.Skip("claude binary not on PATH")
    }
    e := executor.NewClaudeExecutor(executor.ClaudeConfig{Bin: "claude"})
    sink := &captureSink{}
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    res, err := e.Run(ctx, executor.Task{
        ID: "smoke", Prompt: "Reply with only the digit 2 (no words).",
    }, sink)
    require.NoError(t, err)
    require.Contains(t, res.Summary, "2")
    require.True(t, sink.closed)
}
```

- [ ] **Step 3: Run (manual)**

```bash
ANTHROPIC_API_KEY=... go test -tags=smoke ./tests/smoke/... -run TestSmoke_RealClaude -v
```

- [ ] **Step 4: Commit**

```bash
git add slave_agent/tests/smoke/
git commit -m "test(slave_agent): smoke test for real claude"
```

---

## Task 22: Smoke test — real MCP

**Files:**
- Create: `slave_agent/tests/smoke/mcp_smoke_test.go`

- [ ] **Step 1: Test using `@modelcontextprotocol/server-everything`**

```go
//go:build smoke

package smoke

import (
    "context"
    "encoding/json"
    "os/exec"
    "strings"
    "testing"
    "time"

    "github.com/stretchr/testify/require"
    "github.com/yourorg/slave_agent/internal/executor"
)

func TestSmoke_RealMCPStdio(t *testing.T) {
    if _, err := exec.LookPath("npx"); err != nil {
        t.Skip("npx not on PATH")
    }
    e := executor.NewMCPExecutor(map[string]executor.MCPServerCfg{
        "everything": {Transport: "stdio", Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-everything"}},
    })
    defer e.Close()

    body, _ := json.Marshal(map[string]interface{}{
        "server": "everything",
        "tool":   "echo",
        "args":   map[string]string{"message": "hello-from-smoke"},
    })
    ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
    defer cancel()
    res, err := e.Run(ctx, executor.Task{ID: "s", Prompt: string(body)}, &captureSink{})
    require.NoError(t, err)
    require.True(t, strings.Contains(res.Summary, "hello-from-smoke"))
}
```

If `@modelcontextprotocol/server-everything` doesn't expose an `echo` tool by that exact name, swap to whatever simple tool it provides (its README documents the available tools). The smoke test only needs *some* call that round-trips a string.

- [ ] **Step 2: Run (manual)**

```bash
go test -tags=smoke ./tests/smoke/... -run TestSmoke_RealMCPStdio -v
```

- [ ] **Step 3: Commit**

```bash
git add slave_agent/tests/smoke/mcp_smoke_test.go
git commit -m "test(slave_agent): smoke test for real stdio MCP"
```

---

## Task 23: Contract tests — agentserver protocol

**Files:**
- Create: `slave_agent/tests/contract/doc.go`
- Create: `slave_agent/tests/contract/agentserver_contract_test.go`

- [ ] **Step 1: Build tag**

```go
// doc.go
//go:build contract

package contract
```

- [ ] **Step 2: Contract assertions**

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
    "sync/atomic"
    "testing"
    "time"

    "github.com/stretchr/testify/require"
    "github.com/yourorg/slave_agent/internal/dispatch"
    "github.com/yourorg/slave_agent/internal/executor"
    "github.com/yourorg/slave_agent/internal/poller"
    "github.com/yourorg/slave_agent/internal/store"
)

type noopExec struct{}
func (noopExec) Run(ctx context.Context, t executor.Task, sink executor.Sink) (executor.Result, error) {
    sink.Close()
    return executor.Result{Summary: "ok"}, nil
}
type noopJ struct{}
func (noopJ) Record(context.Context, executor.Task, executor.Result) error { return nil }

func TestContract_PollUsesProxyTokenAndStatusUpdateShape(t *testing.T) {
    var pollAuth string
    var seenStatuses []map[string]interface{}
    var taskHandedOut atomic.Bool

    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        switch {
        case r.URL.Path == "/api/agent/tasks/poll":
            pollAuth = r.Header.Get("Authorization")
            if !taskHandedOut.CompareAndSwap(false, true) {
                w.WriteHeader(204)
                return
            }
            json.NewEncoder(w).Encode(map[string]interface{}{
                "task_id": "tc1", "skill": "chat", "prompt": "p",
            })
        case strings.HasPrefix(r.URL.Path, "/api/agent/tasks/") && strings.HasSuffix(r.URL.Path, "/status"):
            require.Equal(t, "PUT", r.Method)
            require.Equal(t, "Bearer ptoken", r.Header.Get("Authorization"))
            require.Equal(t, "application/json", r.Header.Get("Content-Type"))
            body, _ := io.ReadAll(r.Body)
            var m map[string]interface{}
            require.NoError(t, json.Unmarshal(body, &m))
            seenStatuses = append(seenStatuses, m)
            w.WriteHeader(200)
        default:
            t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
        }
    }))
    defer srv.Close()

    s, _ := store.Open(filepath.Join(t.TempDir(), "x.db"))
    defer s.Close()
    d := dispatch.New(map[string]executor.Executor{"": noopExec{}}, noopJ{}, s)
    p := poller.New(poller.Config{
        ServerURL: srv.URL, ProxyToken: "ptoken",
        IdlePoll: 50 * time.Millisecond,
    }, d, s)

    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()
    go p.Run(ctx)

    require.Eventually(t, func() bool { return len(seenStatuses) >= 2 }, time.Second, 20*time.Millisecond)

    require.Equal(t, "Bearer ptoken", pollAuth)
    require.Equal(t, "running", seenStatuses[0]["status"])
    require.Equal(t, "completed", seenStatuses[1]["status"])
    require.Equal(t, "ok", seenStatuses[1]["output"])
}

func TestContract_FailedHasFailureReason(t *testing.T) {
    var failureSeen map[string]interface{}
    var hand atomic.Bool
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/api/agent/tasks/poll" {
            if !hand.CompareAndSwap(false, true) {
                w.WriteHeader(204)
                return
            }
            json.NewEncoder(w).Encode(map[string]interface{}{"task_id": "x", "skill": "chat", "prompt": "p"})
            return
        }
        body, _ := io.ReadAll(r.Body)
        var m map[string]interface{}
        json.Unmarshal(body, &m)
        if m["status"] == "failed" {
            failureSeen = m
        }
        w.WriteHeader(200)
    }))
    defer srv.Close()

    s, _ := store.Open(filepath.Join(t.TempDir(), "x.db"))
    defer s.Close()
    d := dispatch.New(map[string]executor.Executor{"": failingExec{}}, noopJ{}, s)
    p := poller.New(poller.Config{ServerURL: srv.URL, ProxyToken: "p", IdlePoll: 50 * time.Millisecond}, d, s)

    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()
    go p.Run(ctx)

    require.Eventually(t, func() bool { return failureSeen != nil }, time.Second, 20*time.Millisecond)
    require.NotEmpty(t, failureSeen["failure_reason"])
}

type failingExec struct{}
func (failingExec) Run(ctx context.Context, t executor.Task, sink executor.Sink) (executor.Result, error) {
    sink.Close()
    return executor.Result{}, errBoom
}

var errBoom = newErr("boom")

func newErr(s string) error { return &simpleErr{s} }

type simpleErr struct{ s string }
func (e *simpleErr) Error() string { return e.s }
```

- [ ] **Step 3: Run**

```bash
go test -tags=contract ./tests/contract/... -v
```

- [ ] **Step 4: Commit**

```bash
git add slave_agent/tests/contract/
git commit -m "test(slave_agent): contract tests for poller/status protocol"
```

---

## Task 24: E2E script

**Files:**
- Create: `slave_agent/scripts/e2e.sh`
- Modify: `slave_agent/README.md`

- [ ] **Step 1: Write `scripts/e2e.sh`**

```bash
#!/usr/bin/env bash
# Manual end-to-end check. Requires:
#   - agentserver reachable at $AGENTSERVER_URL
#   - claude on PATH and ANTHROPIC_API_KEY set
#   - npx for the everything MCP server
set -euo pipefail
: "${AGENTSERVER_URL:?must set AGENTSERVER_URL}"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

cat > "$work/config.yaml" <<EOF
server:
  url: $AGENTSERVER_URL
  name: slave-e2e
claude:
  bin: claude
mcp_servers:
  everything:
    transport: stdio
    command: npx
    args: ["-y", "@modelcontextprotocol/server-everything"]
discovery:
  display_name: slave-e2e
  description: e2e
  skills: [chat, mcp]
EOF

go build -o "$work/slave-agent" ./cmd/slave-agent
( cd "$work" && ./slave-agent config.yaml ) &
agent_pid=$!
trap 'kill "$agent_pid" 2>/dev/null || true; rm -rf "$work"' EXIT

# Wait until /healthz reachable through tunnel — assumes you set up code-{shortID} hostname.
echo "agent running as pid $agent_pid in $work"
echo "manually:"
echo "  1. visit https://code-<shortID>.<base> to confirm dashboard"
echo "  2. POST a task to /api/workspaces/<wid>/tasks with target_id=<sandbox_id>, skill=chat"
echo "  3. POST a task with skill=mcp, prompt='{\"server\":\"everything\",\"tool\":\"echo\",\"args\":{\"message\":\"hi\"}}'"
echo "  4. POST a task with skill=chat, prompt that triggers a failure (e.g., empty)"
echo "  5. confirm data.db has 3 rows; journal/CURRENT_STATE.md updated; SSE stream produces chunks"
echo
echo "Press Ctrl-C to stop the agent."
wait "$agent_pid"
```

`chmod +x slave_agent/scripts/e2e.sh`

- [ ] **Step 2: Append to README**

Append to `slave_agent/README.md`:

```markdown
## Tests

    go test ./...                                  # unit
    go test -tags=contract ./tests/contract/...    # contract
    go test -tags=smoke ./tests/smoke/...          # real claude + MCP (manual)

## End-to-end

    AGENTSERVER_URL=https://agent.example.com ./scripts/e2e.sh
```

- [ ] **Step 3: Commit**

```bash
git add slave_agent/scripts slave_agent/README.md
git commit -m "docs(slave_agent): e2e script + tests doc"
```

---

## Task 25: CI workflow (optional but recommended)

**Files:**
- Create: `.github/workflows/slave_agent.yml`

- [ ] **Step 1: Workflow**

```yaml
name: slave_agent CI
on:
  push:
    paths: [slave_agent/**]
  pull_request:
    paths: [slave_agent/**]

jobs:
  test:
    runs-on: ubuntu-latest
    defaults:
      run:
        working-directory: slave_agent
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.22'
      - run: go vet ./...
      - run: go test ./... -race -count=1 -coverprofile=cover.out
      - run: go test -tags=contract ./tests/contract/...
```

- [ ] **Step 2: Commit**

```bash
git add .github/workflows/slave_agent.yml
git commit -m "ci: slave_agent vet + race tests + contract tests"
```

---

## Final Verification

- [ ] **Run the full test matrix locally**

```bash
cd slave_agent
go vet ./...
go test ./... -race -count=1
go test -tags=contract ./tests/contract/...
# Smoke (manual, only if real env available):
ANTHROPIC_API_KEY=... go test -tags=smoke ./tests/smoke/... -v
```

All non-smoke tests must pass.

- [ ] **Manual smoke (one-time)**

Run a real agent against staging agentserver per Task 24 e2e script.

- [ ] **Verify spec coverage**

Skim each section of `docs/superpowers/specs/2026-04-27-slave-agent-design.md` and confirm a task implements it. Specifically:

| Spec section | Implemented in |
|---|---|
| §1 architecture | Tasks 1, 20 |
| §2 data flow claude | Tasks 10, 11, 16 |
| §2 data flow MCP | Tasks 12, 13, 14, 16 |
| §2.4 SSE | Tasks 6, 19 |
| §2.5 restart recovery | Task 7, 17 (drain pending acks), 20 (Recover at boot) |
| §3 components | Tasks 2-19 |
| §4.1 directory layout | Task 1 |
| §4.2 config | Tasks 2, 3 |
| §4.3 tunnel | Task 18 |
| §4.4 poller | Task 17 |
| §4.5 dispatch | Task 16 |
| §4.6 executor | Tasks 8, 10-14 |
| §4.7 journal | Task 15 |
| §4.8 store + schema | Tasks 4-7 |
| §4.9 webui | Task 19 |
| §4.10 main | Task 20 |
| §5 error handling | Distributed across tests in 5-19 |
| §6.1 unit tests | Each impl task includes tests |
| §6.3 contract | Task 23 |
| §6.5 smoke | Tasks 21, 22 |
| §6.7 CI | Task 25 |
