# Mid-chat human-in-the-loop — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a slave's chat backend (Claude Code / Codex CLI) pause mid-conversation via an `ask_user` / `request_permission` MCP tool call, surface the question to the human at the driver, and resume the same conversation when the human answers — using backend session_id continuation as the cross-task glue.

**Architecture:**
1. New slave-internal stdio MCP server `humanloop` (Go, run as `slave-agent humanloop-mcp <socket>` subcommand) injected into the chat backend's MCP list; on tool call it forwards payload to executor over a unix socket and returns a fixed "submitted" tool_result so the model stops cleanly.
2. Slave's chat executor captures `session_id` from the first stream-json frame, watches the humanloop IPC, and on a pause signal closes backend stdin (graceful), then writes `result.kind="awaiting_user"` into the agentserver task output.
3. New slave skill `chat_resume` takes `{session_id, answer, kind}` and re-runs the backend with `--resume`/`resume`; per-session flock prevents concurrent resume races.
4. Driver `submit_task` surfaces `session_id`; `wait_task` / `get_task` recognise the `awaiting_user` marker and present it as a non-terminal status; new `resume_task(last_task_id, answer)` is stateless (it re-reads the prior task via `GetTask` to recover session_id + target_id).
5. agentserver upstream is **not** modified; the "non-terminal awaiting_user" is a driver-side surface convention layered on top of a real `completed` task.

**Tech Stack:** Go 1.22, agentserver SDK v0.48.1 (`github.com/agentserver/agentserver/pkg/agentsdk`), Claude Code CLI (`claude --print --output-format=stream-json --mcp-config --resume`), Codex CLI (`codex exec --json`, `codex resume`), MCP stdio JSON-RPC (already in tree via `internal/executor/mcp.go`), unix domain sockets (`net.Listen("unix", ...)`), `syscall.Flock`, existing dispatch pipeline.

**Spec:** [docs/superpowers/specs/2026-05-26-humanloop-resumable-chat-design.md](../specs/2026-05-26-humanloop-resumable-chat-design.md)

---

## File Structure

**Created:**
- `multi-agent/internal/humanloop/server.go` — stdio MCP server impl (handles `initialize`, `tools/list`, `tools/call`), per-task quota counter
- `multi-agent/internal/humanloop/tools.go` — `ask_user` and `request_permission` tool schemas + handler bodies
- `multi-agent/internal/humanloop/ipc.go` — unix-socket client (executor side) and server (humanloop subcommand side) for payload forwarding
- `multi-agent/internal/humanloop/ipc_test.go` — IPC round-trip test
- `multi-agent/internal/humanloop/server_test.go` — full MCP-protocol test against the in-process server
- `multi-agent/internal/humanloop/quota_test.go` — quota counter behaviour
- `multi-agent/cmd/slave-agent/humanloop_subcmd.go` — `humanloop-mcp <socket>` subcommand dispatch
- `multi-agent/tests/prod_test/scripts/e2e_humanloop_happy.sh` — e2e cases 1+2 (regression + single ask_user)
- `multi-agent/tests/prod_test/scripts/e2e_humanloop_multi.sh` — e2e cases 3+4 (multi-round + request_permission)
- `multi-agent/tests/prod_test/scripts/e2e_humanloop_failures.sh` — e2e cases 5+6+7 (jetson + offline + session-not-found)
- `multi-agent/tests/prod_test/scripts/e2e_humanloop_defensive.sh` — e2e cases 8+9+10 (flock + quota + grace-shutdown)
- `multi-agent/tests/prod_test/scripts/humanloop_helpers.sh` — shared bash helpers used by the 4 scripts (submit, wait, resume via the driver MCP)
- `multi-agent/docs/spike-0-resume-tool_use.md` — Spike-0 findings (claude + codex resume behaviour when last assistant turn ends on a dangling tool_use)

**Modified:**
- `multi-agent/cmd/slave-agent/main.go` — subcommand dispatch (humanloop-mcp); always register `chat_resume` route
- `multi-agent/internal/config/config.go` — add `Humanloop { ShutdownGraceSec, MaxQuestionsPerTask }` struct + defaults
- `multi-agent/internal/executor/executor.go` — extend `Result` with `SessionID`, `AwaitingUser`; new `AskUserPayload` type
- `multi-agent/internal/dispatch/dispatch.go` — wrap chat / chat_resume results in `kind:"final"|"awaiting_user"` markers; other skills unchanged
- `multi-agent/pkg/agentbackend/backend.go` — extend `Backend` interface with `RunResume(ctx, sessionID, answer, sink) (Result, error)`
- `multi-agent/pkg/agentbackend/claude/executor.go` — humanloop injection, session_id capture from `type:"system"` frame, AwaitingUser path, graceful shutdown, `RunResume`
- `multi-agent/pkg/agentbackend/claude/executor_test.go` — table-driven tests for the new paths
- `multi-agent/pkg/agentbackend/codex/executor.go` — same shape, codex variant (`thread.started` event, `codex resume`)
- `multi-agent/pkg/agentbackend/codex/executor_test.go` — same
- `multi-agent/internal/driver/slave_tools.go` — add `chatResumeExecutor` route helper used by slave dispatch (or fold into `cmd/slave-agent/main.go` directly)
- `multi-agent/internal/driver/tools.go` — `submit_task` returns `session_id`; `wait_task` / `get_task` parse `kind`; register `resume_task` in `Tools.All()`
- `multi-agent/internal/driver/tools.go` — define `resumeTaskTool` struct + `Call`
- `multi-agent/internal/driver/tools_test.go` — fakes for `GetTask` returning awaiting_user marker; resume_task happy + error paths
- `multi-agent/pkg/agentbackend/capability.go` — append abuse-guard paragraph to `CapabilityEpilogue`
- `multi-agent/skills/multiagent/SKILL.md` — new "Mid-chat human-in-the-loop" section + advisory-only doctrine
- `multi-agent/skills/multiagent/references/driver-tools.md` — `resume_task` entry + extend `wait_task` / `get_task` schemas
- `multi-agent/skills/multiagent/references/slave-skills.md` — `chat_resume` entry + `humanloop` MCP description; note that chat may emit `kind:"awaiting_user"`
- `multi-agent/skills/multiagent/references/task-contract.md` — add `chat_resume` to JSON-prompt-skill list

**No changes:**
- `agentserver` upstream (per spec non-goal)
- `internal/observerstore/*` / `internal/observer/*` (per spec §5)
- Any deploy scripts under `multi-agent/deploy/` (humanloop ships in-binary; no deploy-side change)

---

## Task 0: Spike-0 — verify backend resume with dangling tool_use

**Why:** The whole design rests on the assumption that `claude --resume <S>` (and `codex resume <S>`) cleanly handles a session whose last assistant turn ended on a `tool_use` for our `ask_user` / `request_permission` whose `tool_result` was never sent. If they choke, we'll need a "patch the session jsonl with a synthetic tool_result before resume" step in humanloop. This task is **investigation only** — no production code touched. Output is a markdown findings file.

**Files:**
- Create: `multi-agent/docs/spike-0-resume-tool_use.md`

**Steps:**

- [ ] **Step 1: Craft a minimal session that ends on a dangling tool_use (claude)**

Reach for a slave or a local install with `claude` on PATH. Use a throwaway working dir. Configure an MCP server that defines one tool whose handler *exits before returning* (simulates our humanloop close-stdin behaviour):

```bash
mkdir -p /tmp/spike0-claude && cd /tmp/spike0-claude

# Minimal MCP server in python that errors out / never returns for the test tool.
cat > /tmp/spike0-mcp.py <<'PY'
import sys, json, time
def reply(obj): sys.stdout.write(json.dumps(obj)+"\n"); sys.stdout.flush()
for line in sys.stdin:
    msg = json.loads(line)
    if msg.get("method") == "initialize":
        reply({"jsonrpc":"2.0","id":msg["id"],"result":{
            "protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"spike","version":"0"}}})
    elif msg.get("method") == "notifications/initialized":
        pass
    elif msg.get("method") == "tools/list":
        reply({"jsonrpc":"2.0","id":msg["id"],"result":{"tools":[{
            "name":"hang","description":"Returns nothing; the client must give up",
            "inputSchema":{"type":"object","properties":{},"additionalProperties":False}}]}})
    elif msg.get("method") == "tools/call":
        # Sleep forever to mimic "model called our tool, executor will kill backend".
        time.sleep(3600)
PY

cat > /tmp/spike0-mcp.json <<JSON
{"mcpServers":{"spike":{"command":"python3","args":["/tmp/spike0-mcp.py"]}}}
JSON

# Drive claude in --print mode that almost certainly invokes the hang tool.
( echo 'Call the spike__hang tool now without arguments. Stop after you call it.';
  sleep 5
) | timeout 30 claude --print --output-format=stream-json --verbose \
      --mcp-config /tmp/spike0-mcp.json 2>&1 | tee /tmp/spike0-claude-run1.log

# Capture the session id (printed in the first stream-json frame).
SESSION=$(grep -m1 '"session_id"' /tmp/spike0-claude-run1.log | python3 -c 'import sys,json,re; print(json.loads(sys.stdin.read().split("\n")[0])["session_id"])')
echo "Session: $SESSION"
```

- [ ] **Step 2: Try to resume with a fresh user message**

```bash
# Resume the same session, feed a new user message claiming the answer.
echo 'I am the user; my answer to your previous question is: 42. Continue.' \
  | timeout 30 claude --resume "$SESSION" --print --output-format=stream-json --verbose \
      --mcp-config /tmp/spike0-mcp.json 2>&1 | tee /tmp/spike0-claude-resume.log
```

Observe:
- Exit code 0? Or non-zero?
- Any error like `tool_use without tool_result` in the log?
- Does the assistant emit text in the resume turn? Does it acknowledge the answer?

- [ ] **Step 3: Repeat for codex**

Same shape: throwaway dir, MCP server in `~/.codex/config.toml`, drive `codex exec --json`, capture `thread_id` from `thread.started`, then `codex resume <thread_id>` with a new user message.

```bash
mkdir -p /tmp/spike0-codex && cd /tmp/spike0-codex
mkdir -p .codex
cat > .codex/config.toml <<'TOML'
[mcp_servers.spike]
command = "python3"
args = ["/tmp/spike0-mcp.py"]
TOML

echo 'Call the spike__hang tool now without arguments. Stop after you call it.' \
  | timeout 30 codex exec --json --cd /tmp/spike0-codex 2>&1 | tee /tmp/spike0-codex-run1.log

THREAD=$(grep -m1 'thread.started' /tmp/spike0-codex-run1.log | python3 -c 'import sys,json; print(json.loads(sys.stdin.read().split("\n")[0])["thread_id"])')

echo 'I am the user; my answer to your previous question is: 42. Continue.' \
  | timeout 30 codex resume "$THREAD" --json 2>&1 | tee /tmp/spike0-codex-resume.log
```

- [ ] **Step 4: Write the findings**

Create `multi-agent/docs/spike-0-resume-tool_use.md` and record, for each backend:

- Did `--resume` accept the session with a dangling tool_use? (yes/no)
- Exit code, observable errors
- Whether the assistant turn in the resume saw the user message clearly
- **If `no` for either backend:** propose a fallback — most likely "before resume, append a synthetic `tool_result` for the dangling `tool_use` into the session jsonl (`~/.claude/projects/<dir>/<session>.jsonl` for claude; codex equivalent TBD)". Sketch the JSON patch and which line offset it inserts at.

Findings file headers:

```markdown
# Spike-0: backend resume with dangling tool_use

**Date:** YYYY-MM-DD
**Goal:** Verify `claude --resume <S>` and `codex resume <T>` handle sessions whose last assistant turn called a tool that never received a tool_result.

## Claude Code

- Version: <claude --version>
- Result: <PASS or FAIL>
- Evidence: <key log excerpts>
- Fallback (if FAIL): <patch shape>

## Codex CLI

- Version: <codex --version>
- Result: <PASS or FAIL>
- Evidence: <key log excerpts>
- Fallback (if FAIL): <patch shape>

## Decision for humanloop server design

- <one paragraph: do we patch session jsonl before resume, or just resume cleanly?>
```

- [ ] **Step 5: Commit the findings**

```bash
git add multi-agent/docs/spike-0-resume-tool_use.md
git commit -m "docs(spike): backend resume behaviour with dangling tool_use"
```

---

## Task 1: Config — `humanloop` block

**Files:**
- Modify: `multi-agent/internal/config/config.go`
- Modify: `multi-agent/internal/config/config_test.go`

- [ ] **Step 1: Write the failing test**

Append to `config_test.go`:

```go
func TestLoadHumanloopDefaults(t *testing.T) {
    dir := t.TempDir()
    p := filepath.Join(dir, "config.yaml")
    if err := os.WriteFile(p, []byte("server:\n  url: \"http://x\"\n"), 0o600); err != nil {
        t.Fatal(err)
    }
    cfg, err := Load(p)
    if err != nil {
        t.Fatal(err)
    }
    if cfg.Humanloop.ShutdownGraceSec != 10 {
        t.Errorf("ShutdownGraceSec default = %d, want 10", cfg.Humanloop.ShutdownGraceSec)
    }
    if cfg.Humanloop.MaxQuestionsPerTask != 5 {
        t.Errorf("MaxQuestionsPerTask default = %d, want 5", cfg.Humanloop.MaxQuestionsPerTask)
    }
}

func TestLoadHumanloopOverrides(t *testing.T) {
    dir := t.TempDir()
    p := filepath.Join(dir, "config.yaml")
    yaml := "server:\n  url: \"http://x\"\nhumanloop:\n  shutdown_grace_sec: 3\n  max_questions_per_task: 99\n"
    if err := os.WriteFile(p, []byte(yaml), 0o600); err != nil {
        t.Fatal(err)
    }
    cfg, err := Load(p)
    if err != nil {
        t.Fatal(err)
    }
    if cfg.Humanloop.ShutdownGraceSec != 3 || cfg.Humanloop.MaxQuestionsPerTask != 99 {
        t.Errorf("override failed: got %+v", cfg.Humanloop)
    }
}
```

- [ ] **Step 2: Run the test, expect fail**

```bash
cd multi-agent && go test ./internal/config/ -run TestLoadHumanloop -v
```

Expected: compile error or undefined `cfg.Humanloop`.

- [ ] **Step 3: Add the struct + defaults**

In `multi-agent/internal/config/config.go`, locate the `Config` struct and add:

```go
type Config struct {
    // ... existing fields ...
    Humanloop HumanloopConfig `yaml:"humanloop"`
}

type HumanloopConfig struct {
    ShutdownGraceSec    int `yaml:"shutdown_grace_sec"`
    MaxQuestionsPerTask int `yaml:"max_questions_per_task"`
}
```

In the `Load` function, after `yaml.Unmarshal(...)` and before the return, add defaults:

```go
if cfg.Humanloop.ShutdownGraceSec == 0 {
    cfg.Humanloop.ShutdownGraceSec = 10
}
if cfg.Humanloop.MaxQuestionsPerTask == 0 {
    cfg.Humanloop.MaxQuestionsPerTask = 5
}
```

- [ ] **Step 4: Run the test, expect pass**

```bash
cd multi-agent && go test ./internal/config/ -run TestLoadHumanloop -v
```

Expected: PASS on both subtests.

- [ ] **Step 5: Commit**

```bash
git add multi-agent/internal/config/config.go multi-agent/internal/config/config_test.go
git commit -m "feat(config): humanloop block (shutdown_grace_sec, max_questions_per_task)"
```

---

## Task 2: `internal/humanloop/` — IPC primitives

**Files:**
- Create: `multi-agent/internal/humanloop/ipc.go`
- Create: `multi-agent/internal/humanloop/ipc_test.go`

- [ ] **Step 1: Write the failing test**

`multi-agent/internal/humanloop/ipc_test.go`:

```go
package humanloop

import (
    "encoding/json"
    "path/filepath"
    "testing"
    "time"
)

func TestIPCRoundTrip(t *testing.T) {
    sock := filepath.Join(t.TempDir(), "hl.sock")

    srv, err := ListenIPC(sock)
    if err != nil {
        t.Fatalf("ListenIPC: %v", err)
    }
    defer srv.Close()

    received := make(chan Payload, 1)
    go func() {
        p, err := srv.Receive()
        if err != nil {
            t.Errorf("Receive: %v", err)
            return
        }
        received <- p
    }()

    client, err := DialIPC(sock)
    if err != nil {
        t.Fatalf("DialIPC: %v", err)
    }
    defer client.Close()

    in := Payload{Kind: "ask_user", Question: "are we good?", Options: []string{"yes", "no"}}
    if err := client.Send(in); err != nil {
        t.Fatalf("Send: %v", err)
    }

    select {
    case got := <-received:
        gj, _ := json.Marshal(got)
        ij, _ := json.Marshal(in)
        if string(gj) != string(ij) {
            t.Errorf("payload mismatch:\nwant %s\ngot  %s", ij, gj)
        }
    case <-time.After(2 * time.Second):
        t.Fatal("timed out waiting for IPC payload")
    }
}
```

- [ ] **Step 2: Run the test, expect fail**

```bash
cd multi-agent && go test ./internal/humanloop/ -run TestIPCRoundTrip -v
```

Expected: compile error (package not found).

- [ ] **Step 3: Implement `ipc.go`**

```go
// Package humanloop wires the slave's chat backend to the user at the driver:
// when the backend's stdio MCP server "humanloop" receives an ask_user /
// request_permission tool call, it forwards the payload over a unix socket
// to the slave's chat executor, which pauses the conversation.
package humanloop

import (
    "bufio"
    "encoding/json"
    "fmt"
    "net"
    "os"
)

// Payload is what humanloop server sends to the executor when the model calls
// ask_user or request_permission. It mirrors AskUserPayload in
// internal/executor; keeping them as two types avoids a cyclic import.
type Payload struct {
    Kind     string   `json:"kind"`              // "ask_user" | "request_permission"
    Question string   `json:"question,omitempty"`
    Options  []string `json:"options,omitempty"`
    Context  string   `json:"context,omitempty"`
    Intent   string   `json:"intent,omitempty"`
    Target   string   `json:"target,omitempty"`
    Reason   string   `json:"reason,omitempty"`
}

// IPCServer listens on a unix socket for a single Payload from the humanloop
// MCP subcommand and then closes.
type IPCServer struct {
    ln   net.Listener
    path string
}

func ListenIPC(path string) (*IPCServer, error) {
    if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
        return nil, err
    }
    _ = os.Remove(path) // best-effort: drop stale socket
    ln, err := net.Listen("unix", path)
    if err != nil {
        return nil, fmt.Errorf("humanloop listen %s: %w", path, err)
    }
    return &IPCServer{ln: ln, path: path}, nil
}

func (s *IPCServer) Receive() (Payload, error) {
    conn, err := s.ln.Accept()
    if err != nil {
        return Payload{}, fmt.Errorf("humanloop accept: %w", err)
    }
    defer conn.Close()
    line, err := bufio.NewReader(conn).ReadBytes('\n')
    if err != nil {
        return Payload{}, fmt.Errorf("humanloop read: %w", err)
    }
    var p Payload
    if err := json.Unmarshal(line, &p); err != nil {
        return Payload{}, fmt.Errorf("humanloop unmarshal: %w", err)
    }
    return p, nil
}

func (s *IPCServer) Close() error {
    err := s.ln.Close()
    _ = os.Remove(s.path)
    return err
}

// IPCClient dials the executor's socket and sends one Payload.
type IPCClient struct {
    conn net.Conn
}

func DialIPC(path string) (*IPCClient, error) {
    c, err := net.Dial("unix", path)
    if err != nil {
        return nil, fmt.Errorf("humanloop dial %s: %w", path, err)
    }
    return &IPCClient{conn: c}, nil
}

func (c *IPCClient) Send(p Payload) error {
    b, err := json.Marshal(p)
    if err != nil {
        return err
    }
    b = append(b, '\n')
    if _, err := c.conn.Write(b); err != nil {
        return fmt.Errorf("humanloop send: %w", err)
    }
    return nil
}

func (c *IPCClient) Close() error { return c.conn.Close() }
```

Also need the import for `filepath`:

```go
import (
    "bufio"
    "encoding/json"
    "fmt"
    "net"
    "os"
    "path/filepath"
)
```

- [ ] **Step 4: Run the test, expect pass**

```bash
cd multi-agent && go test ./internal/humanloop/ -run TestIPCRoundTrip -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add multi-agent/internal/humanloop/
git commit -m "feat(humanloop): unix-socket IPC between executor and MCP server"
```

---

## Task 3: `internal/humanloop/` — MCP server (tools + quota)

**Files:**
- Create: `multi-agent/internal/humanloop/server.go`
- Create: `multi-agent/internal/humanloop/tools.go`
- Create: `multi-agent/internal/humanloop/server_test.go`
- Create: `multi-agent/internal/humanloop/quota_test.go`

- [ ] **Step 1: Write the failing protocol-level test**

`multi-agent/internal/humanloop/server_test.go`:

```go
package humanloop

import (
    "bufio"
    "bytes"
    "encoding/json"
    "io"
    "path/filepath"
    "strings"
    "testing"
)

// drive feeds a sequence of JSON-RPC requests through ServeStdio and returns
// the response lines. ServeStdio reads stdin until EOF and writes responses
// to stdout, one JSON object per line.
func drive(t *testing.T, sock string, max int, lines ...string) []string {
    t.Helper()
    in := strings.NewReader(strings.Join(lines, "\n") + "\n")
    var out bytes.Buffer
    if err := ServeStdio(in, &out, sock, max); err != nil && err != io.EOF {
        t.Fatalf("ServeStdio: %v", err)
    }
    var got []string
    sc := bufio.NewScanner(&out)
    sc.Buffer(make([]byte, 0, 1<<16), 1<<22)
    for sc.Scan() {
        got = append(got, sc.Text())
    }
    return got
}

func TestServerInitializeAndToolsList(t *testing.T) {
    sock := filepath.Join(t.TempDir(), "hl.sock")
    srv, err := ListenIPC(sock)
    if err != nil { t.Fatal(err) }
    defer srv.Close()

    resps := drive(t, sock, 5,
        `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
        `{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
        `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
    )
    if len(resps) < 2 {
        t.Fatalf("expected ≥2 responses, got %d: %v", len(resps), resps)
    }
    // tools/list response must mention both tool names
    if !strings.Contains(resps[1], `"ask_user"`) || !strings.Contains(resps[1], `"request_permission"`) {
        t.Errorf("tools/list missing tools: %s", resps[1])
    }
}

func TestServerAskUserForwardsAndReturnsSubmitted(t *testing.T) {
    sock := filepath.Join(t.TempDir(), "hl.sock")
    srv, err := ListenIPC(sock)
    if err != nil { t.Fatal(err) }
    defer srv.Close()

    got := make(chan Payload, 1)
    go func() {
        p, err := srv.Receive()
        if err != nil { t.Errorf("Receive: %v", err); return }
        got <- p
    }()

    resps := drive(t, sock, 5,
        `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
        `{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
        `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ask_user","arguments":{"question":"q?","options":["a","b"]}}}`,
    )
    if len(resps) < 2 { t.Fatalf("got %d responses", len(resps)) }
    last := resps[len(resps)-1]
    if !strings.Contains(last, `"submitted"`) {
        t.Errorf("expected submitted in tool_result, got: %s", last)
    }
    p := <-got
    if p.Kind != "ask_user" || p.Question != "q?" || len(p.Options) != 2 {
        t.Errorf("unexpected payload: %+v", p)
    }
}

// Helper used by quota_test.
func mustGetText(t *testing.T, line string) string {
    t.Helper()
    var msg struct {
        Result struct {
            Content []struct {
                Type string `json:"type"`
                Text string `json:"text"`
            } `json:"content"`
        } `json:"result"`
    }
    if err := json.Unmarshal([]byte(line), &msg); err != nil {
        t.Fatalf("unmarshal %q: %v", line, err)
    }
    if len(msg.Result.Content) == 0 { return "" }
    return msg.Result.Content[0].Text
}
```

`multi-agent/internal/humanloop/quota_test.go`:

```go
package humanloop

import (
    "path/filepath"
    "strings"
    "testing"
)

func TestServerQuotaRefusesAfterMax(t *testing.T) {
    sock := filepath.Join(t.TempDir(), "hl.sock")
    srv, err := ListenIPC(sock)
    if err != nil { t.Fatal(err) }
    defer srv.Close()

    // Drain payloads in background so srv.Receive doesn't block forever.
    go func() { for { if _, err := srv.Receive(); err != nil { return } } }()

    lines := []string{
        `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
        `{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
    }
    for i := 0; i < 3; i++ {
        lines = append(lines, `{"jsonrpc":"2.0","id":99,"method":"tools/call","params":{"name":"ask_user","arguments":{"question":"q?"}}}`)
    }

    resps := drive(t, sock, 2 /* MAX */, lines...)
    if len(resps) < 4 { t.Fatalf("expected ≥4 responses, got %d", len(resps)) }
    // First two ask_user calls: "submitted"
    if !strings.Contains(resps[1], "submitted") || !strings.Contains(resps[2], "submitted") {
        t.Errorf("expected first two submitted, got %v", resps[1:3])
    }
    // Third one: refused
    if !strings.Contains(resps[3], "refused") {
        t.Errorf("expected third refused, got %s", resps[3])
    }
}
```

- [ ] **Step 2: Run the tests, expect compile fail**

```bash
cd multi-agent && go test ./internal/humanloop/ -run TestServer -v
```

Expected: undefined `ServeStdio`, `Payload` etc.

- [ ] **Step 3: Implement the server**

`multi-agent/internal/humanloop/server.go`:

```go
package humanloop

import (
    "bufio"
    "encoding/json"
    "fmt"
    "io"
)

// ServeStdio reads JSON-RPC 2.0 requests from r line-by-line and writes
// responses to w. The server implements the MCP subset we need:
// initialize / notifications/initialized / tools/list / tools/call.
// ipcSocket is the path of the executor's IPC server; max is the per-process
// quota for tools/call invocations of ask_user|request_permission.
func ServeStdio(r io.Reader, w io.Writer, ipcSocket string, max int) error {
    sc := bufio.NewScanner(r)
    sc.Buffer(make([]byte, 0, 1<<16), 1<<22)
    used := 0
    for sc.Scan() {
        var req struct {
            JSONRPC string          `json:"jsonrpc"`
            ID      json.RawMessage `json:"id,omitempty"`
            Method  string          `json:"method"`
            Params  json.RawMessage `json:"params,omitempty"`
        }
        if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
            continue
        }
        switch req.Method {
        case "initialize":
            writeResult(w, req.ID, map[string]interface{}{
                "protocolVersion": "2024-11-05",
                "capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
                "serverInfo":      map[string]interface{}{"name": "humanloop", "version": "1"},
            })
        case "notifications/initialized":
            // no reply for notifications
        case "tools/list":
            writeResult(w, req.ID, map[string]interface{}{"tools": toolList()})
        case "tools/call":
            text := handleCall(req.Params, ipcSocket, &used, max)
            writeResult(w, req.ID, map[string]interface{}{
                "content": []map[string]interface{}{
                    {"type": "text", "text": text},
                },
            })
        default:
            writeError(w, req.ID, -32601, "method not found: "+req.Method)
        }
    }
    return sc.Err()
}

func writeResult(w io.Writer, id json.RawMessage, result interface{}) {
    out := map[string]interface{}{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result}
    if len(id) == 0 {
        out["id"] = nil
    }
    b, _ := json.Marshal(out)
    fmt.Fprintln(w, string(b))
}

func writeError(w io.Writer, id json.RawMessage, code int, message string) {
    out := map[string]interface{}{"jsonrpc": "2.0", "id": json.RawMessage(id),
        "error": map[string]interface{}{"code": code, "message": message}}
    if len(id) == 0 { out["id"] = nil }
    b, _ := json.Marshal(out)
    fmt.Fprintln(w, string(b))
}
```

`multi-agent/internal/humanloop/tools.go`:

```go
package humanloop

import (
    "encoding/json"
    "fmt"
)

const submittedText = `{"status":"submitted","note":"Your question was dispatched to the user. The backend will now pause; the user's answer will arrive as your next user turn after resume."}`

const refusedText = `{"status":"refused","reason":"max questions reached for this task; decide yourself and explain in summary"}`

func toolList() []map[string]interface{} {
    return []map[string]interface{}{
        {
            "name":        "ask_user",
            "description": "Pause the conversation and ask the human (sitting at the driver) for a judgement or clarification. Use only when guessing wrong would be costly and the answer fits in one or two sentences.",
            "inputSchema": map[string]interface{}{
                "type": "object",
                "properties": map[string]interface{}{
                    "question": map[string]interface{}{"type": "string"},
                    "options":  map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
                    "context":  map[string]interface{}{"type": "string"},
                },
                "required":             []string{"question"},
                "additionalProperties": false,
            },
        },
        {
            "name":        "request_permission",
            "description": "Pause the conversation and ask the human to approve a sensitive operation. Advisory only: an 'approve' answer does NOT grant new abilities; the user must use update_slave_claude_permissions / register_slave_mcp separately.",
            "inputSchema": map[string]interface{}{
                "type": "object",
                "properties": map[string]interface{}{
                    "intent": map[string]interface{}{"type": "string",
                        "enum": []string{"run_bash", "write_path", "install_mcp", "other"}},
                    "target": map[string]interface{}{"type": "string"},
                    "reason": map[string]interface{}{"type": "string"},
                },
                "required":             []string{"intent", "target"},
                "additionalProperties": false,
            },
        },
    }
}

func handleCall(rawParams json.RawMessage, ipcSocket string, used *int, max int) string {
    var p struct {
        Name      string          `json:"name"`
        Arguments json.RawMessage `json:"arguments"`
    }
    if err := json.Unmarshal(rawParams, &p); err != nil {
        return fmt.Sprintf(`{"status":"error","reason":"bad params: %s"}`, err.Error())
    }
    if p.Name != "ask_user" && p.Name != "request_permission" {
        return fmt.Sprintf(`{"status":"error","reason":"unknown tool: %s"}`, p.Name)
    }
    if *used >= max {
        return refusedText
    }
    *used++

    var args struct {
        Question string   `json:"question"`
        Options  []string `json:"options"`
        Context  string   `json:"context"`
        Intent   string   `json:"intent"`
        Target   string   `json:"target"`
        Reason   string   `json:"reason"`
    }
    _ = json.Unmarshal(p.Arguments, &args)

    payload := Payload{
        Kind:     p.Name,
        Question: args.Question,
        Options:  args.Options,
        Context:  args.Context,
        Intent:   args.Intent,
        Target:   args.Target,
        Reason:   args.Reason,
    }

    client, err := DialIPC(ipcSocket)
    if err != nil {
        return fmt.Sprintf(`{"status":"error","reason":"ipc dial: %s"}`, err.Error())
    }
    defer client.Close()
    if err := client.Send(payload); err != nil {
        return fmt.Sprintf(`{"status":"error","reason":"ipc send: %s"}`, err.Error())
    }
    return submittedText
}
```

- [ ] **Step 4: Run the tests, expect pass**

```bash
cd multi-agent && go test ./internal/humanloop/ -v
```

Expected: 3 tests PASS (`TestIPCRoundTrip`, `TestServerInitializeAndToolsList`, `TestServerAskUserForwardsAndReturnsSubmitted`, `TestServerQuotaRefusesAfterMax`).

- [ ] **Step 5: Commit**

```bash
git add multi-agent/internal/humanloop/
git commit -m "feat(humanloop): stdio MCP server with ask_user + request_permission + quota"
```

---

## Task 4: Wire `humanloop-mcp` subcommand into slave-agent

**Files:**
- Create: `multi-agent/cmd/slave-agent/humanloop_subcmd.go`
- Modify: `multi-agent/cmd/slave-agent/main.go`

- [ ] **Step 1: Create the subcommand entry**

`multi-agent/cmd/slave-agent/humanloop_subcmd.go`:

```go
package main

import (
    "fmt"
    "os"
    "strconv"

    "github.com/yourorg/multi-agent/internal/humanloop"
)

// runHumanloopMCP runs the in-binary humanloop MCP server.
// Usage: slave-agent humanloop-mcp <ipc-socket-path> <max-questions>
// Stdin/stdout = the backend's MCP transport; the IPC socket is how this
// subcommand reports user-question payloads back to the chat executor.
func runHumanloopMCP(args []string) error {
    if len(args) < 2 {
        return fmt.Errorf("usage: slave-agent humanloop-mcp <socket-path> <max-questions>")
    }
    sock := args[0]
    max, err := strconv.Atoi(args[1])
    if err != nil || max <= 0 {
        return fmt.Errorf("max-questions must be a positive integer, got %q", args[1])
    }
    return humanloop.ServeStdio(os.Stdin, os.Stdout, sock, max)
}
```

- [ ] **Step 2: Dispatch the subcommand in `main`**

In `multi-agent/cmd/slave-agent/main.go`, replace:

```go
func main() {
    cfgPath := "config.yaml"
    if len(os.Args) > 1 {
        cfgPath = os.Args[1]
    }
    if err := run(cfgPath); err != nil {
        log.Fatalf("slave_agent: %v", err)
    }
}
```

with:

```go
func main() {
    if len(os.Args) > 1 && os.Args[1] == "humanloop-mcp" {
        if err := runHumanloopMCP(os.Args[2:]); err != nil {
            log.Fatalf("slave_agent humanloop-mcp: %v", err)
        }
        return
    }
    cfgPath := "config.yaml"
    if len(os.Args) > 1 {
        cfgPath = os.Args[1]
    }
    if err := run(cfgPath); err != nil {
        log.Fatalf("slave_agent: %v", err)
    }
}
```

- [ ] **Step 3: Build to verify**

```bash
cd multi-agent && go build ./cmd/slave-agent/
```

Expected: no error.

- [ ] **Step 4: Smoke-run the subcommand by hand**

```bash
cd multi-agent
SOCK=$(mktemp -u /tmp/hl.XXXXXX.sock)
( printf '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}\n{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}\n' \
   | ./slave-agent humanloop-mcp "$SOCK" 5 ) | head -5
```

Expected: two JSON response lines on stdout. (Socket may already be cleaned up; that's fine for this smoke.)

- [ ] **Step 5: Commit**

```bash
git add multi-agent/cmd/slave-agent/humanloop_subcmd.go multi-agent/cmd/slave-agent/main.go
git commit -m "feat(slave-agent): humanloop-mcp subcommand entry"
```

---

## Task 5: Extend `executor.Result` + `AskUserPayload`

**Files:**
- Modify: `multi-agent/internal/executor/executor.go`

- [ ] **Step 1: Update the types**

Replace the contents of `multi-agent/internal/executor/executor.go` with:

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
    SessionID        string // backend session/thread id (chat / chat_resume only)
    AwaitingUser     *AskUserPayload
}

// AskUserPayload mirrors humanloop.Payload but lives here so chat-skill
// callers in this package can build a Result without importing humanloop.
type AskUserPayload struct {
    Kind     string   `json:"kind"`              // "ask_user" | "request_permission"
    Question string   `json:"question,omitempty"`
    Options  []string `json:"options,omitempty"`
    Context  string   `json:"context,omitempty"`
    Intent   string   `json:"intent,omitempty"`
    Target   string   `json:"target,omitempty"`
    Reason   string   `json:"reason,omitempty"`
}

type Sink interface {
    Write(eventType, data string)
    Close()
}

type Executor interface {
    Run(ctx context.Context, t Task, sink Sink) (Result, error)
}
```

- [ ] **Step 2: Build the whole tree**

```bash
cd multi-agent && go build ./...
```

Expected: no error (we added fields, no callsite removed anything).

- [ ] **Step 3: Commit**

```bash
git add multi-agent/internal/executor/executor.go
git commit -m "feat(executor): Result.SessionID + Result.AwaitingUser + AskUserPayload"
```

---

## Task 6: Dispatch — wrap chat / chat_resume results in `kind:` marker

**Files:**
- Modify: `multi-agent/internal/dispatch/dispatch.go`
- Modify: `multi-agent/internal/dispatch/dispatch_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `multi-agent/internal/dispatch/dispatch_test.go`:

```go
func TestRunChatWrapsFinalResult(t *testing.T) {
    s := setupStore(t)
    defer s.Close()
    exec := &fakeExec{result: executor.Result{Summary: "done", SessionID: "S-1"}}
    d := New(map[string]executor.Executor{"": exec}, fakeRecorder{}, s, nil)

    res, err := d.Run(context.Background(), executor.Task{ID: "T1", Skill: "", Prompt: "hi"})
    if err != nil { t.Fatal(err) }

    // After dispatch, the stored task's summary should be the wrapped JSON.
    info, err := s.Get("T1")
    if err != nil { t.Fatal(err) }
    if !strings.Contains(info.Summary, `"kind":"final"`) {
        t.Errorf("expected kind:final in stored summary, got %q", info.Summary)
    }
    if !strings.Contains(info.Summary, `"session_id":"S-1"`) {
        t.Errorf("expected session_id in stored summary, got %q", info.Summary)
    }
    if res.Summary != "done" {
        t.Errorf("res.Summary should remain unwrapped, got %q", res.Summary)
    }
}

func TestRunChatWrapsAwaitingUser(t *testing.T) {
    s := setupStore(t)
    defer s.Close()
    exec := &fakeExec{result: executor.Result{
        SessionID: "S-2",
        AwaitingUser: &executor.AskUserPayload{
            Kind: "ask_user", Question: "pick one?", Options: []string{"a", "b"},
        },
    }}
    d := New(map[string]executor.Executor{"": exec}, fakeRecorder{}, s, nil)

    if _, err := d.Run(context.Background(), executor.Task{ID: "T2", Skill: "", Prompt: "hi"}); err != nil {
        t.Fatal(err)
    }
    info, _ := s.Get("T2")
    for _, want := range []string{`"kind":"awaiting_user"`, `"session_id":"S-2"`, `"question":"pick one?"`} {
        if !strings.Contains(info.Summary, want) {
            t.Errorf("missing %s in %s", want, info.Summary)
        }
    }
}

func TestRunBashLeavesResultUnwrapped(t *testing.T) {
    s := setupStore(t)
    defer s.Close()
    exec := &fakeExec{result: executor.Result{Summary: "raw bash output"}}
    d := New(map[string]executor.Executor{"bash": exec}, fakeRecorder{}, s, nil)

    if _, err := d.Run(context.Background(), executor.Task{ID: "T3", Skill: "bash", Prompt: "ls"}); err != nil {
        t.Fatal(err)
    }
    info, _ := s.Get("T3")
    if strings.Contains(info.Summary, `"kind"`) {
        t.Errorf("non-chat skill should not be wrapped; got %s", info.Summary)
    }
    if info.Summary != "raw bash output" {
        t.Errorf("expected raw summary, got %q", info.Summary)
    }
}
```

(`setupStore`, `fakeExec`, and `fakeRecorder` must already exist in `dispatch_test.go`; if their signatures differ, adapt the new tests accordingly — keep the assertions the same.)

- [ ] **Step 2: Run the tests, expect fail**

```bash
cd multi-agent && go test ./internal/dispatch/ -run "TestRun(Chat|Bash)" -v
```

Expected: FAIL — stored summary is the raw `res.Summary` without wrapping.

- [ ] **Step 3: Implement the marker wrapping**

In `multi-agent/internal/dispatch/dispatch.go`, immediately after `res, err := exec.Run(...)` but before `d.store.Complete(...)`, insert:

```go
// For chat / chat_resume only, wrap the result in a structured marker so
// the driver can distinguish "final" from "awaiting_user" without parsing
// the summary text. See spec §3.4.
stored := res.Summary
if t.Skill == "" || t.Skill == "chat" || t.Skill == "chat_resume" {
    var wrapper any
    if res.AwaitingUser != nil {
        wrapper = map[string]any{
            "kind":       "awaiting_user",
            "session_id": res.SessionID,
            "question":   res.AwaitingUser,
        }
    } else {
        wrapper = map[string]any{
            "kind":       "final",
            "summary":    res.Summary,
            "session_id": res.SessionID,
        }
    }
    if b, err := json.Marshal(wrapper); err == nil {
        stored = string(b)
    }
}
if err := d.store.Complete(t.ID, stored); err != nil {
    return res, err
}
```

…and remove the existing call to `d.store.Complete(t.ID, res.Summary)` it replaces. Also update the observer-emit `output` field if it uses `res.Summary` directly — keep it on `stored` for the marker case, so observer sees the same string the driver will read back.

- [ ] **Step 4: Run the tests, expect pass**

```bash
cd multi-agent && go test ./internal/dispatch/ -v
```

Expected: all dispatch tests PASS.

- [ ] **Step 5: Commit**

```bash
git add multi-agent/internal/dispatch/
git commit -m "feat(dispatch): wrap chat/chat_resume results in kind:final|awaiting_user"
```

---

## Task 7: Extend `agentbackend.Backend` interface with `RunResume`

**Files:**
- Modify: `multi-agent/pkg/agentbackend/backend.go`
- Modify: `multi-agent/pkg/agentbackend/backend_test.go` (or add fake-backend updates)

- [ ] **Step 1: Add the method to the interface**

In `multi-agent/pkg/agentbackend/backend.go`, replace the `Backend` interface with:

```go
type Backend interface {
    Kind() Kind
    Run(ctx context.Context, t Task, sink Sink) (Result, error)
    RunResume(ctx context.Context, sessionID, answer string, sink Sink) (Result, error)
    LLM() LLMRunner
    Permissions() PermissionsStore
    Detect(ctx context.Context) error
}
```

- [ ] **Step 2: Add a stub `RunResume` to claude + codex backends so the build still compiles**

In `multi-agent/pkg/agentbackend/claude/backend.go` (or wherever the `Backend` impl lives), add:

```go
func (b *Backend) RunResume(ctx context.Context, sessionID, answer string, sink Sink) (Result, error) {
    return Result{}, fmt.Errorf("RunResume not yet wired (claude); see Task 9")
}
```

(Adjust receiver name to whatever the existing file uses; add the `fmt` import if missing.)

Same shape for `multi-agent/pkg/agentbackend/codex/backend.go`.

- [ ] **Step 3: Build to verify the interface change compiles**

```bash
cd multi-agent && go build ./...
```

Expected: no error.

- [ ] **Step 4: Run all existing tests to ensure nothing regressed**

```bash
cd multi-agent && go test ./...
```

Expected: PASS (the stubs aren't called yet).

- [ ] **Step 5: Commit**

```bash
git add multi-agent/pkg/agentbackend/
git commit -m "feat(agentbackend): RunResume on Backend interface (stub impls)"
```

---

## Task 8: claude backend — session_id capture + humanloop injection + AwaitingUser

**Files:**
- Modify: `multi-agent/pkg/agentbackend/claude/executor.go`
- Modify: `multi-agent/pkg/agentbackend/claude/executor_test.go`

This is the largest single task. Steps stay TDD-shaped but the implementation step is bigger.

- [ ] **Step 1: Write failing tests against a stubbed `claude` binary**

The existing executor test pattern uses a small shell-script `claude` binary on PATH that emits canned stream-json. Extend that pattern.

Append to `multi-agent/pkg/agentbackend/claude/executor_test.go`:

```go
// TestExecutorCapturesSessionID checks that the first {"type":"system","session_id":...}
// frame in the stream-json transcript is stored on Result.SessionID.
func TestExecutorCapturesSessionID(t *testing.T) {
    bin := writeFakeClaude(t, []string{
        `{"type":"system","session_id":"sess-abc"}`,
        `{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`,
    })
    ex := newExecutor(agentbackend.ClaudeConfig{Bin: bin, WorkDir: t.TempDir()}, nil)
    res, err := ex.Run(context.Background(), agentbackend.Task{Prompt: "hi"}, &captureSink{})
    if err != nil { t.Fatal(err) }
    if res.SessionID != "sess-abc" {
        t.Errorf("SessionID = %q, want sess-abc", res.SessionID)
    }
    if res.AwaitingUser != nil { t.Errorf("AwaitingUser should be nil") }
}

// TestExecutorPausesOnHumanloopIPC simulates the humanloop subprocess sending
// an ask_user payload while the chat is running; the executor should close
// stdin, wait for the fake claude to exit, and return Result.AwaitingUser set.
func TestExecutorPausesOnHumanloopIPC(t *testing.T) {
    // fake claude reads stdin until EOF, then prints a system frame + finishes.
    bin := writeFakeClaudeReadsStdinThenExits(t, "sess-pause")
    sockHook := func(path string) {
        // Connect to the executor's IPC socket and send an ask_user payload.
        time.Sleep(200 * time.Millisecond) // let executor start the listener
        c, err := humanloop.DialIPC(path)
        if err != nil { t.Logf("DialIPC: %v", err); return }
        defer c.Close()
        _ = c.Send(humanloop.Payload{Kind: "ask_user", Question: "approve?"})
    }
    ex := newExecutorWithSocketHook(agentbackend.ClaudeConfig{Bin: bin, WorkDir: t.TempDir()}, nil, sockHook)
    res, err := ex.Run(context.Background(), agentbackend.Task{Prompt: "hi"}, &captureSink{})
    if err != nil { t.Fatal(err) }
    if res.AwaitingUser == nil { t.Fatal("AwaitingUser nil") }
    if res.AwaitingUser.Question != "approve?" {
        t.Errorf("question = %q", res.AwaitingUser.Question)
    }
    if res.SessionID != "sess-pause" { t.Errorf("SessionID = %q", res.SessionID) }
}
```

Test helpers `writeFakeClaude`, `writeFakeClaudeReadsStdinThenExits`, `captureSink`, and `newExecutorWithSocketHook` may need to be created in the test file. Pattern:

```go
func writeFakeClaude(t *testing.T, frames []string) string {
    t.Helper()
    dir := t.TempDir()
    script := filepath.Join(dir, "claude")
    body := "#!/bin/bash\n"
    for _, f := range frames {
        body += fmt.Sprintf("echo '%s'\n", f)
    }
    if err := os.WriteFile(script, []byte(body), 0o755); err != nil { t.Fatal(err) }
    return script
}

func writeFakeClaudeReadsStdinThenExits(t *testing.T, sessionID string) string {
    t.Helper()
    dir := t.TempDir()
    script := filepath.Join(dir, "claude")
    body := fmt.Sprintf(`#!/bin/bash
echo '{"type":"system","session_id":"%s"}'
# block on stdin; exit when stdin closes
cat > /dev/null
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"bye"}]}}'
`, sessionID)
    if err := os.WriteFile(script, []byte(body), 0o755); err != nil { t.Fatal(err) }
    return script
}

type captureSink struct{ writes []string }
func (s *captureSink) Write(t, d string) { s.writes = append(s.writes, t+":"+d) }
func (s *captureSink) Close() {}

// newExecutorWithSocketHook is a constructor variant exposed for tests that
// invokes hook(socketPath) as soon as the listener is up.
```

- [ ] **Step 2: Run, expect FAIL**

```bash
cd multi-agent && go test ./pkg/agentbackend/claude/ -run "TestExecutor(CapturesSessionID|PausesOnHumanloop)" -v
```

Expected: FAIL or build error (SessionID not populated; helpers undefined).

- [ ] **Step 3: Implement the changes to `executor.go`**

Replace `Run` in `multi-agent/pkg/agentbackend/claude/executor.go` with the version below. Key changes vs current:
1. Compute a per-task IPC socket path (`os.MkdirTemp` based).
2. Spawn `slave-agent humanloop-mcp <sock> <max>` as a subprocess (the path of `slave-agent` is taken from `os.Args[0]` when available — for tests we accept an override).
3. Write a temp MCP config and pass `--mcp-config <path>`.
4. Start an `IPCServer` listener on the socket.
5. In the stream-json loop, parse `type:"system"` for `session_id`.
6. A goroutine `srv.Receive()` — on payload: stash + `stdin.Close()`.
7. Wait for claude to exit; honour `cfg.ShutdownGraceSec` (passed in from caller) and SIGTERM/SIGKILL as documented in spec §3.1.

Sketch:

```go
type Executor struct {
    cfg                  agentbackend.ClaudeConfig
    env                  []string
    binSelf              string  // slave-agent binary path; default os.Args[0]
    maxQuestions         int
    shutdownGraceSec     int
    socketHookForTest    func(string)  // nil in prod
}

func (e *Executor) Run(ctx context.Context, t agentbackend.Task, sink agentbackend.Sink) (agentbackend.Result, error) {
    sockDir, err := os.MkdirTemp("", "humanloop-")
    if err != nil { return agentbackend.Result{}, err }
    defer os.RemoveAll(sockDir)
    sockPath := filepath.Join(sockDir, "hl.sock")

    srv, err := humanloop.ListenIPC(sockPath)
    if err != nil { return agentbackend.Result{}, err }
    defer srv.Close()
    if e.socketHookForTest != nil { go e.socketHookForTest(sockPath) }

    mcpConfigPath := filepath.Join(sockDir, "mcp.json")
    if err := writeHumanloopMCPConfig(mcpConfigPath, e.binSelf, sockPath, e.maxQuestions); err != nil {
        return agentbackend.Result{}, err
    }

    args := append([]string{
        "--print",
        "--output-format=stream-json",
        "--verbose",
        "--append-system-prompt", agentbackend.CapabilityEpilogue,
        "--mcp-config", mcpConfigPath,
    }, e.cfg.ExtraArgs...)
    cmd := exec.CommandContext(ctx, e.cfg.Bin, args...)
    cmd.Dir = e.cfg.WorkDir
    cmd.Env = append(cmd.Environ(), e.env...)

    stdin, _ := cmd.StdinPipe()
    stdout, _ := cmd.StdoutPipe()
    var stderrBuf strings.Builder
    cmd.Stderr = &stderrBuf

    if err := cmd.Start(); err != nil { return agentbackend.Result{}, err }

    // Send prompt then close stdin … but we may close earlier on pause.
    promptDone := make(chan struct{})
    go func() {
        defer close(promptDone)
        if t.SystemContext != "" { io.WriteString(stdin, t.SystemContext+"\n\n") }
        io.WriteString(stdin, t.Prompt)
    }()

    var awaiting *executor.AskUserPayload
    pauseCh := make(chan struct{})
    go func() {
        p, err := srv.Receive()
        if err != nil { close(pauseCh); return }
        awaiting = &executor.AskUserPayload{Kind: p.Kind, Question: p.Question,
            Options: p.Options, Context: p.Context, Intent: p.Intent,
            Target: p.Target, Reason: p.Reason}
        <-promptDone
        stdin.Close()
        close(pauseCh)
    }()

    var lastText strings.Builder
    var sessionID string
    sc := bufio.NewScanner(stdout)
    sc.Buffer(make([]byte, 0, 1<<16), 1<<24)
    for sc.Scan() {
        line := sc.Bytes()
        var meta struct {
            Type      string `json:"type"`
            SessionID string `json:"session_id"`
            Message   struct {
                Content []struct {
                    Type string `json:"type"`
                    Text string `json:"text"`
                } `json:"content"`
            } `json:"message"`
        }
        if err := json.Unmarshal(line, &meta); err != nil { continue }
        if meta.Type == "system" && sessionID == "" && meta.SessionID != "" {
            sessionID = meta.SessionID
        }
        if meta.Type == "assistant" {
            for _, c := range meta.Message.Content {
                if c.Type == "text" {
                    sink.Write("chunk", c.Text)
                    lastText.WriteString(c.Text)
                }
            }
        }
    }

    // Wait with shutdown grace.
    done := make(chan error, 1)
    go func() { done <- cmd.Wait() }()
    select {
    case err := <-done:
        if awaiting == nil && err != nil {
            sink.Close()
            tail := stderrBuf.String()
            if len(tail) > 4096 { tail = tail[len(tail)-4096:] }
            return agentbackend.Result{}, fmt.Errorf("claude exit: %v: %s", err, tail)
        }
    case <-time.After(time.Duration(e.shutdownGraceSec) * time.Second):
        _ = cmd.Process.Signal(syscall.SIGTERM)
        select {
        case <-done:
        case <-time.After(5 * time.Second):
            _ = cmd.Process.Kill()
            <-done
        }
    }

    full := lastText.String()
    summary, change := agentbackend.SplitCapability(full)
    if change != "" { sink.Write("capability", change) }
    sink.Close()
    return agentbackend.Result{
        Summary: summary, CapabilityChange: change,
        SessionID: sessionID, AwaitingUser: awaiting,
    }, nil
}
```

Add `writeHumanloopMCPConfig`:

```go
func writeHumanloopMCPConfig(path, binSelf, sockPath string, max int) error {
    cfg := map[string]any{
        "mcpServers": map[string]any{
            "loom_humanloop": map[string]any{
                "command": binSelf,
                "args":    []string{"humanloop-mcp", sockPath, strconv.Itoa(max)},
            },
        },
    }
    b, err := json.Marshal(cfg)
    if err != nil { return err }
    return os.WriteFile(path, b, 0o600)
}
```

And update the constructor `newExecutor` to default `binSelf=os.Args[0]`, `maxQuestions=5`, `shutdownGraceSec=10`. The caller (Task 12) will plumb cfg values in.

- [ ] **Step 4: Run the tests, expect pass**

```bash
cd multi-agent && go test ./pkg/agentbackend/claude/ -v
```

Expected: PASS. If `TestExecutorPausesOnHumanloopIPC` times out, increase the test-side `time.Sleep` for hook before connecting, or expose a `srvReady` channel.

- [ ] **Step 5: Commit**

```bash
git add multi-agent/pkg/agentbackend/claude/
git commit -m "feat(backend/claude): humanloop MCP injection + session_id + pause path"
```

---

## Task 9: claude backend — `RunResume`

**Files:**
- Modify: `multi-agent/pkg/agentbackend/claude/executor.go`
- Modify: `multi-agent/pkg/agentbackend/claude/backend.go` (remove stub)
- Modify: `multi-agent/pkg/agentbackend/claude/executor_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestExecutorRunResumeFeedsAnswer(t *testing.T) {
    // The fake claude --resume binary should echo back the stdin it received
    // as an assistant text frame so we can assert it.
    bin := writeFakeClaudeResume(t)
    ex := newExecutor(agentbackend.ClaudeConfig{Bin: bin, WorkDir: t.TempDir()}, nil)
    res, err := ex.RunResume(context.Background(), "sess-1", "the user's answer", &captureSink{})
    if err != nil { t.Fatal(err) }
    if !strings.Contains(res.Summary, "User answered: the user's answer") {
        t.Errorf("expected answer prefix in summary, got %q", res.Summary)
    }
}

// writeFakeClaudeResume: a script that asserts $1 == "--resume", reads
// stdin, and emits stream-json with the stdin verbatim as the assistant
// text frame.
func writeFakeClaudeResume(t *testing.T) string { /* ... */ }
```

- [ ] **Step 2: Run, expect fail**

- [ ] **Step 3: Implement `RunResume`**

Add a method on the same `Executor`:

```go
func (e *Executor) RunResume(ctx context.Context, sessionID, answer string, sink agentbackend.Sink) (agentbackend.Result, error) {
    // RunResume reuses Run with a different argv head (--resume) and a
    // prompt that signals to the model that this is the user's answer to
    // its prior ask_user / request_permission tool call.
    promptPrefix := "User answered: " + answer
    // Delegate to Run by injecting --resume into ExtraArgs and the prompt.
    cfg := e.cfg
    cfg.ExtraArgs = append([]string{"--resume", sessionID}, cfg.ExtraArgs...)
    sub := *e
    sub.cfg = cfg
    return sub.Run(ctx, agentbackend.Task{Prompt: promptPrefix}, sink)
}
```

Update `Backend.RunResume` in `backend.go` to delegate to `e.RunResume`.

- [ ] **Step 4: Run, expect pass**

```bash
cd multi-agent && go test ./pkg/agentbackend/claude/ -v
```

- [ ] **Step 5: Commit**

```bash
git add multi-agent/pkg/agentbackend/claude/
git commit -m "feat(backend/claude): RunResume via --resume + 'User answered:' prefix"
```

---

## Task 10: codex backend — session capture + humanloop injection + AwaitingUser

**Files:**
- Modify: `multi-agent/pkg/agentbackend/codex/executor.go`
- Modify: `multi-agent/pkg/agentbackend/codex/executor_test.go`

Mirror Task 8 for codex. Three deltas vs claude.

| Concern | Claude | Codex |
|---|---|---|
| Session id parse | first `{"type":"system","session_id":"..."}` stream-json frame | first `thread.started` event in `codex exec --json` stream — extract `thread_id` |
| MCP injection | `--mcp-config <json-file>` flag | write a per-task `<scratch>/.codex/config.toml` with `[mcp_servers.loom_humanloop]` and invoke with `--cd <scratch>` so codex picks it up; copy any existing `~/.codex/config.toml` into the scratch dir first so user model-provider settings still apply |
| Resume invocation | `claude --resume <sessionID> --print …` | `codex resume <threadID> --json …` |

- [ ] **Step 1: Write tests with a fake `codex` script**

Pattern identical to Task 8's `writeFakeClaude` / `writeFakeClaudeReadsStdinThenExits` but emitting codex-shaped NDJSON. Sample first frame:

```
{"type":"thread.started","thread_id":"thr-1"}
{"type":"item.completed","item":{"type":"assistant_message","text":"hi"}}
```

- [ ] **Step 2: Run, expect fail**

```bash
cd multi-agent && go test ./pkg/agentbackend/codex/ -run "TestExecutor(CapturesThreadID|PausesOnHumanloop)" -v
```

- [ ] **Step 3: Implement `Run` (codex variant)**

Key snippets specific to codex (the rest mirrors Task 8 line-for-line):

```go
// MCP config: write a TOML in a scratch dir, then --cd into it.
func writeHumanloopCodexConfig(scratchDir, binSelf, sockPath string, max int) error {
    if err := os.MkdirAll(filepath.Join(scratchDir, ".codex"), 0o700); err != nil { return err }
    // Preserve existing user config if any.
    if home, err := os.UserHomeDir(); err == nil {
        if src, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml")); err == nil {
            _ = os.WriteFile(filepath.Join(scratchDir, ".codex", "config.toml"), src, 0o600)
        }
    }
    f, err := os.OpenFile(filepath.Join(scratchDir, ".codex", "config.toml"),
        os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
    if err != nil { return err }
    defer f.Close()
    fmt.Fprintf(f, "\n[mcp_servers.loom_humanloop]\ncommand = %q\nargs = [%q, %q, %q]\n",
        binSelf, "humanloop-mcp", sockPath, strconv.Itoa(max))
    return nil
}

// In Run:
scratchDir := filepath.Join(sockDir, "scratch")
if err := writeHumanloopCodexConfig(scratchDir, e.binSelf, sockPath, e.maxQuestions); err != nil {
    return agentbackend.Result{}, err
}
args := append([]string{"exec", "--json", "--cd", scratchDir}, e.cfg.ExtraArgs...)
// session-id parse in the stream-json loop:
var meta struct {
    Type     string `json:"type"`
    ThreadID string `json:"thread_id"`
    Item     struct {
        Type string `json:"type"`
        Text string `json:"text"`
    } `json:"item"`
}
// ...
if meta.Type == "thread.started" && sessionID == "" { sessionID = meta.ThreadID }
if meta.Type == "item.completed" && meta.Item.Type == "assistant_message" {
    sink.Write("chunk", meta.Item.Text)
    lastText.WriteString(meta.Item.Text)
}
```

`RunResume` codex variant:

```go
func (e *Executor) RunResume(ctx context.Context, sessionID, answer string, sink agentbackend.Sink) (agentbackend.Result, error) {
    cfg := e.cfg
    cfg.ExtraArgs = append([]string{"resume", sessionID, "--json"}, cfg.ExtraArgs...)
    // NB: codex resume's primary positional differs from `exec`; the runner
    // below must NOT prepend "exec" again. Simplest: introduce a small flag
    // path in Run() that, when called from RunResume, uses cfg.ExtraArgs as-is
    // instead of prepending the exec head. Or: implement RunResume as its own
    // self-contained spawn — only ~15 lines.
    sub := *e; sub.cfg = cfg
    return sub.runResumeInner(ctx, "User answered: "+answer, sink)
}
```

(`runResumeInner` is a near-duplicate of `Run` that does NOT prepend `exec --json --cd …`; uses the same humanloop + stdin/stdout plumbing. The mild duplication is acceptable per the spec's "don't add abstractions until you need them" — only 2 callers.)

- [ ] **Step 4: Run, expect pass**

```bash
cd multi-agent && go test ./pkg/agentbackend/codex/ -v
```

- [ ] **Step 5: Commit**

```bash
git add multi-agent/pkg/agentbackend/codex/
git commit -m "feat(backend/codex): humanloop MCP injection + thread_id + pause path + RunResume"
```

---

## Task 11: Slave — `chat_resume` skill route + per-session flock

**Files:**
- Modify: `multi-agent/cmd/slave-agent/main.go`
- Create: `multi-agent/internal/executor/chat_resume.go`
- Create: `multi-agent/internal/executor/chat_resume_test.go`

- [ ] **Step 1: Write the failing test**

`multi-agent/internal/executor/chat_resume_test.go`:

```go
package executor

import (
    "context"
    "path/filepath"
    "sync"
    "testing"
)

type fakeResumeBackend struct{ calls int }

func (f *fakeResumeBackend) Run(ctx context.Context, t Task, s Sink) (Result, error) { return Result{}, nil }
func (f *fakeResumeBackend) RunResume(ctx context.Context, sid, ans string, s Sink) (Result, error) {
    f.calls++
    return Result{Summary: "ok: " + ans, SessionID: sid}, nil
}

func TestChatResumeHappyPath(t *testing.T) {
    be := &fakeResumeBackend{}
    ex := NewChatResume(ChatResumeConfig{Backend: be, FlockDir: t.TempDir()})
    res, err := ex.Run(context.Background(),
        Task{Prompt: `{"session_id":"S","answer":"yes","kind":"ask_user"}`},
        nullSink{})
    if err != nil { t.Fatal(err) }
    if be.calls != 1 || res.Summary != "ok: yes" { t.Errorf("unexpected: %+v", res) }
}

func TestChatResumeRejectsConcurrent(t *testing.T) {
    be := &fakeResumeBackend{}
    flockDir := t.TempDir()
    ex := NewChatResume(ChatResumeConfig{Backend: &slowBackend{}, FlockDir: flockDir})
    var wg sync.WaitGroup
    errs := make(chan error, 2)
    body := `{"session_id":"S-shared","answer":"x","kind":"ask_user"}`
    for i := 0; i < 2; i++ {
        wg.Add(1)
        go func() { defer wg.Done()
            _, err := ex.Run(context.Background(), Task{Prompt: body}, nullSink{})
            errs <- err
        }()
    }
    wg.Wait(); close(errs)
    _ = be // suppress unused
    var busy int
    for err := range errs { if err != nil && strings.Contains(err.Error(), "session busy") { busy++ } }
    if busy != 1 { t.Errorf("expected exactly 1 'session busy' error, got %d", busy) }

    // flock file must exist for the session
    if _, err := os.Stat(filepath.Join(flockDir, "S-shared.lock")); err != nil { t.Error(err) }
}

type slowBackend struct{}
func (s *slowBackend) Run(ctx context.Context, t Task, _ Sink) (Result, error) { return Result{}, nil }
func (s *slowBackend) RunResume(ctx context.Context, _, _ string, _ Sink) (Result, error) {
    time.Sleep(200 * time.Millisecond); return Result{}, nil
}

type nullSink struct{}
func (nullSink) Write(string, string) {}
func (nullSink) Close()                {}
```

- [ ] **Step 2: Run, expect fail**

- [ ] **Step 3: Implement `chat_resume.go`**

```go
package executor

import (
    "context"
    "encoding/json"
    "fmt"
    "os"
    "path/filepath"
    "syscall"
)

type ResumeBackend interface {
    Run(ctx context.Context, t Task, sink Sink) (Result, error)
    RunResume(ctx context.Context, sessionID, answer string, sink Sink) (Result, error)
}

type ChatResumeConfig struct {
    Backend  ResumeBackend
    FlockDir string // typically $LOOM_HOME/<agent>/humanloop/
}

type ChatResumeExecutor struct{ cfg ChatResumeConfig }

func NewChatResume(c ChatResumeConfig) *ChatResumeExecutor { return &ChatResumeExecutor{cfg: c} }

func (e *ChatResumeExecutor) Run(ctx context.Context, t Task, sink Sink) (Result, error) {
    var body struct {
        SessionID string `json:"session_id"`
        Answer    string `json:"answer"`
        Kind      string `json:"kind"`
    }
    if err := json.Unmarshal([]byte(t.Prompt), &body); err != nil {
        return Result{}, fmt.Errorf("chat_resume: bad prompt JSON: %w", err)
    }
    if body.SessionID == "" || body.Answer == "" {
        return Result{}, fmt.Errorf("chat_resume: session_id and answer required")
    }

    if err := os.MkdirAll(e.cfg.FlockDir, 0o700); err != nil {
        return Result{}, fmt.Errorf("chat_resume: mkdir %s: %w", e.cfg.FlockDir, err)
    }
    lockPath := filepath.Join(e.cfg.FlockDir, body.SessionID+".lock")
    f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
    if err != nil {
        return Result{}, fmt.Errorf("chat_resume: open lock: %w", err)
    }
    defer f.Close()
    if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
        return Result{}, fmt.Errorf("chat_resume: session busy (flock=%s)", lockPath)
    }
    defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

    return e.cfg.Backend.RunResume(ctx, body.SessionID, body.Answer, sink)
}
```

- [ ] **Step 4: Wire the route in `cmd/slave-agent/main.go`**

Locate the `routes := map[string]executor.Executor{...}` block and add unconditionally:

```go
routes["chat_resume"] = executor.NewChatResume(executor.ChatResumeConfig{
    Backend:  resumeAdapter{backend},
    FlockDir: filepath.Join(workdir, "humanloop"),
})
```

…where `resumeAdapter` is a tiny adapter:

```go
type resumeAdapter struct{ b agentbackend.Backend }
func (a resumeAdapter) Run(ctx context.Context, t executor.Task, s executor.Sink) (executor.Result, error) {
    return a.b.Run(ctx, t, s)
}
func (a resumeAdapter) RunResume(ctx context.Context, sid, ans string, s executor.Sink) (executor.Result, error) {
    return a.b.RunResume(ctx, sid, ans, s)
}
```

Also add `"chat_resume"` to the `jsonPromptSkill` allowlist in driver-side code — wait, that's a *driver* concern (Task 12). Note here: dispatch's contract-envelope strip in `internal/dispatch/dispatch.go` should already be benign because `chat_resume` prompt is pure JSON object, the envelope-decoder will fail decode and pass through. **Verify in step 5.**

- [ ] **Step 5: Run the tests, expect pass + smoke build**

```bash
cd multi-agent && go test ./internal/executor/ ./cmd/slave-agent/ -v && go build ./...
```

- [ ] **Step 6: Commit**

```bash
git add multi-agent/internal/executor/ multi-agent/cmd/slave-agent/
git commit -m "feat(slave): chat_resume skill route + per-session flock"
```

---

## Task 12: Driver — `submit_task` surfaces `session_id`

**Files:**
- Modify: `multi-agent/internal/driver/tools.go`
- Modify: `multi-agent/internal/driver/tools_test.go`

- [ ] **Step 1: Write the failing test**

Append to `tools_test.go`:

```go
func TestSubmitTaskReturnsSessionID(t *testing.T) {
    sdk := &fakeSDK{
        delegateResp: &agentsdk.DelegateTaskResponse{
            TaskID: "T-1", SessionID: "S-abc", Status: "assigned",
        },
        cards: []agentsdk.AgentCard{ /* … one target with fanout … */ },
    }
    tools := NewTools(/* reg, audit, sdk, cfg, obs */)
    raw, err := tools.All()[indexOf(tools.All(), "submit_task")].Call(context.Background(),
        json.RawMessage(`{"prompt":"hi","target_display_name":"slave-A"}`))
    if err != nil { t.Fatal(err) }
    var got struct {
        TaskID    string `json:"task_id"`
        SessionID string `json:"session_id"`
    }
    if err := json.Unmarshal(raw, &got); err != nil { t.Fatal(err) }
    if got.TaskID != "T-1" || got.SessionID != "S-abc" {
        t.Errorf("got %+v", got)
    }
}
```

(Adapt to whatever helpers exist in `tools_test.go`; if `indexOf` doesn't exist, add a tiny local helper.)

- [ ] **Step 2: Run, expect fail**

- [ ] **Step 3: Add `session_id` to the submit_task return**

In `multi-agent/internal/driver/tools.go::submitTaskTool.Call`, change the final `json.Marshal` to:

```go
return json.Marshal(map[string]interface{}{
    "task_id":             resp.TaskID,
    "session_id":          resp.SessionID, // NEW
    "target_id":           targetID,
    "target_display_name": targetName,
    "manifest":            manifest,
})
```

- [ ] **Step 4: Run, expect pass**

- [ ] **Step 5: Commit**

```bash
git add multi-agent/internal/driver/
git commit -m "feat(driver): submit_task returns session_id from agentserver"
```

---

## Task 13: Driver — `wait_task` / `get_task` recognise `kind:"awaiting_user"`

**Files:**
- Modify: `multi-agent/internal/driver/tools.go`
- Modify: `multi-agent/internal/driver/tools_test.go`

- [ ] **Step 1: Write the failing tests**

Append:

```go
func TestWaitTaskReturnsAwaitingUserWhenResultMarker(t *testing.T) {
    sdk := &fakeSDK{
        taskInfos: map[string]*agentsdk.TaskInfo{
            "T-1": {
                TaskID: "T-1", Status: "completed", SessionID: "S-abc", TargetID: "ag-X",
                Result: json.RawMessage(`{"kind":"awaiting_user","session_id":"S-abc",
                    "question":{"kind":"ask_user","question":"pick?","options":["a","b"]}}`),
            },
        },
    }
    tools := NewTools(/* … */)
    raw, err := tools.All()[indexOf(tools.All(), "wait_task")].Call(context.Background(),
        json.RawMessage(`{"task_id":"T-1","timeout_sec":2}`))
    if err != nil { t.Fatal(err) }
    var got struct {
        Status         string `json:"status"`
        IsFinal        bool   `json:"is_final"`
        SessionID      string `json:"session_id"`
        CurrentTaskID  string `json:"current_task_id"`
        Question       struct {
            Kind     string   `json:"kind"`
            Question string   `json:"question"`
            Options  []string `json:"options"`
        } `json:"question"`
    }
    if err := json.Unmarshal(raw, &got); err != nil { t.Fatal(err) }
    if got.Status != "awaiting_user" || got.IsFinal != false ||
        got.SessionID != "S-abc" || got.CurrentTaskID != "T-1" ||
        got.Question.Question != "pick?" || len(got.Question.Options) != 2 {
        t.Errorf("unexpected awaiting_user shape: %+v", got)
    }
}

func TestWaitTaskUnwrapsKindFinal(t *testing.T) {
    sdk := &fakeSDK{
        taskInfos: map[string]*agentsdk.TaskInfo{
            "T-2": {
                TaskID: "T-2", Status: "completed", SessionID: "S-z",
                Result: json.RawMessage(`{"kind":"final","summary":"all done","session_id":"S-z"}`),
            },
        },
    }
    tools := NewTools(/* … */)
    raw, _ := tools.All()[indexOf(tools.All(), "wait_task")].Call(context.Background(),
        json.RawMessage(`{"task_id":"T-2","timeout_sec":2}`))
    var got struct {
        Status string `json:"status"`
        Output string `json:"output"`
    }
    _ = json.Unmarshal(raw, &got)
    if got.Status != "completed" || got.Output != "all done" {
        t.Errorf("expected completed/all done, got %+v", got)
    }
}

func TestWaitTaskPassesThroughLegacyResultUnwrapped(t *testing.T) {
    // Non-chat skill: result is a raw string, no kind wrapper.
    sdk := &fakeSDK{
        taskInfos: map[string]*agentsdk.TaskInfo{
            "T-3": {TaskID: "T-3", Status: "completed", Output: "bash ran"},
        },
    }
    tools := NewTools(/* … */)
    raw, _ := tools.All()[indexOf(tools.All(), "wait_task")].Call(context.Background(),
        json.RawMessage(`{"task_id":"T-3","timeout_sec":2}`))
    var got struct {
        Status string `json:"status"`
        Output string `json:"output"`
    }
    _ = json.Unmarshal(raw, &got)
    if got.Status != "completed" || got.Output != "bash ran" {
        t.Errorf("expected legacy passthrough, got %+v", got)
    }
}
```

- [ ] **Step 2: Run, expect fail**

- [ ] **Step 3: Implement**

In `wait_task` (and the symmetric `get_task`), after fetching `info`, before the final `json.Marshal`, parse `info.Result`:

```go
type kindWrapper struct {
    Kind      string          `json:"kind"`
    Summary   string          `json:"summary"`
    SessionID string          `json:"session_id"`
    Question  json.RawMessage `json:"question"`
}
var kw kindWrapper
if len(info.Result) > 0 {
    _ = json.Unmarshal(info.Result, &kw)
}
switch kw.Kind {
case "awaiting_user":
    return json.Marshal(struct {
        Status         string          `json:"status"`
        IsFinal        bool            `json:"is_final"`
        SessionID      string          `json:"session_id"`
        CurrentTaskID  string          `json:"current_task_id"`
        TargetID       string          `json:"target_id"`
        Question       json.RawMessage `json:"question"`
    }{
        Status: "awaiting_user", IsFinal: false,
        SessionID: firstNonEmpty(info.SessionID, kw.SessionID),
        CurrentTaskID: firstNonEmpty(info.TaskID, args.TaskID),
        TargetID: info.TargetID, Question: kw.Question,
    })
case "final":
    // Drop into the existing "completed" branch with kw.Summary used in place of output.
    output := kw.Summary
    // … existing fields …
default:
    // Existing legacy path — output = sdkTaskOutput(info)
}
```

Refactor existing `wait_task` final return to share the `output` computation. Same edit in `get_task`.

- [ ] **Step 4: Run, expect pass**

```bash
cd multi-agent && go test ./internal/driver/ -v
```

- [ ] **Step 5: Commit**

```bash
git add multi-agent/internal/driver/
git commit -m "feat(driver): wait_task/get_task recognise kind:awaiting_user|final markers"
```

---

## Task 14: Driver — new `resume_task` tool

**Files:**
- Modify: `multi-agent/internal/driver/tools.go`
- Modify: `multi-agent/internal/driver/tools_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestResumeTaskHappy(t *testing.T) {
    delegated := false
    sdk := &fakeSDK{
        taskInfos: map[string]*agentsdk.TaskInfo{
            "T-1": {TaskID: "T-1", Status: "completed", SessionID: "S-abc", TargetID: "ag-X",
                Result: json.RawMessage(`{"kind":"awaiting_user","session_id":"S-abc",
                    "question":{"kind":"ask_user","question":"q?"}}`)},
            "T-2": {TaskID: "T-2", Status: "completed", SessionID: "S-abc",
                Result: json.RawMessage(`{"kind":"final","summary":"finalised","session_id":"S-abc"}`)},
        },
        delegateResp: &agentsdk.DelegateTaskResponse{TaskID: "T-2", SessionID: "S-abc"},
        onDelegate: func(req agentsdk.DelegateTaskRequest) {
            delegated = true
            if req.Skill != "chat_resume" || req.TargetID != "ag-X" {
                t.Errorf("unexpected delegate: %+v", req)
            }
            var body struct {
                SessionID string `json:"session_id"`
                Answer    string `json:"answer"`
                Kind      string `json:"kind"`
            }
            _ = json.Unmarshal([]byte(req.Prompt), &body)
            if body.SessionID != "S-abc" || body.Answer != "yes" || body.Kind != "ask_user" {
                t.Errorf("bad resume body: %+v", body)
            }
        },
    }
    tools := NewTools(/* … */)
    raw, err := tools.All()[indexOf(tools.All(), "resume_task")].Call(context.Background(),
        json.RawMessage(`{"last_task_id":"T-1","answer":"yes","timeout_sec":2}`))
    if err != nil { t.Fatal(err) }
    if !delegated { t.Fatal("did not call DelegateTask") }
    var got struct{ Status, Output string }
    _ = json.Unmarshal(raw, &got)
    if got.Status != "completed" || got.Output != "finalised" {
        t.Errorf("expected completed/finalised, got %+v", got)
    }
}

func TestResumeTaskRejectsWhenNotAwaitingUser(t *testing.T) {
    sdk := &fakeSDK{
        taskInfos: map[string]*agentsdk.TaskInfo{
            "T-1": {TaskID: "T-1", Status: "completed",
                Result: json.RawMessage(`{"kind":"final","summary":"x"}`)},
        },
    }
    tools := NewTools(/* … */)
    _, err := tools.All()[indexOf(tools.All(), "resume_task")].Call(context.Background(),
        json.RawMessage(`{"last_task_id":"T-1","answer":"a"}`))
    if err == nil || !strings.Contains(err.Error(), "not awaiting_user") {
        t.Errorf("expected not-awaiting-user error, got %v", err)
    }
}
```

- [ ] **Step 2: Run, expect fail**

- [ ] **Step 3: Implement `resumeTaskTool`**

In `multi-agent/internal/driver/tools.go`, append:

```go
type resumeTaskTool struct{ t *Tools }

func (r *resumeTaskTool) Name() string { return "resume_task" }
func (r *resumeTaskTool) Description() string {
    return "Resume a paused chat: pass the last_task_id (from wait_task's awaiting_user) and the user's answer; returns the next wait_task-shaped result."
}
func (r *resumeTaskTool) InputSchema() json.RawMessage {
    return json.RawMessage(`{"type":"object","properties":{
        "last_task_id":{"type":"string"},
        "answer":{"type":"string"},
        "timeout_sec":{"type":"integer"}
    },"required":["last_task_id","answer"]}`)
}
func (r *resumeTaskTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
    var args struct {
        LastTaskID string `json:"last_task_id"`
        Answer     string `json:"answer"`
        TimeoutSec int    `json:"timeout_sec"`
    }
    if err := json.Unmarshal(raw, &args); err != nil {
        return nil, &MCPToolError{Message: "invalid args: " + err.Error()}
    }
    if args.LastTaskID == "" || args.Answer == "" {
        return nil, &MCPToolError{Message: "last_task_id and answer are required"}
    }
    info, err := r.t.sdk.GetTask(ctx, args.LastTaskID, true)
    if err != nil {
        return nil, &MCPToolError{Message: "get_task: " + err.Error()}
    }
    var kw struct {
        Kind     string `json:"kind"`
        Question struct {
            Kind string `json:"kind"`
        } `json:"question"`
        SessionID string `json:"session_id"`
    }
    if len(info.Result) > 0 { _ = json.Unmarshal(info.Result, &kw) }
    if info.Status != "completed" || kw.Kind != "awaiting_user" {
        return nil, &MCPToolError{Message: fmt.Sprintf(
            "not awaiting_user; status=%s, kind=%s", info.Status, kw.Kind)}
    }
    sessionID := firstNonEmpty(info.SessionID, kw.SessionID)
    if sessionID == "" {
        return nil, &MCPToolError{Message: "missing session_id; cannot resume"}
    }

    body, _ := json.Marshal(map[string]string{
        "session_id": sessionID, "answer": args.Answer, "kind": kw.Question.Kind,
    })
    timeout := args.TimeoutSec
    if timeout == 0 { timeout = r.t.cfg.DriverDefaults.TaskTimeoutSec }
    if timeout == 0 { timeout = 600 }

    resp, err := r.t.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
        TargetID: info.TargetID, Skill: "chat_resume",
        Prompt: string(body), TimeoutSeconds: timeout,
    })
    if err != nil {
        return nil, &MCPToolError{Message: "delegate chat_resume: " + err.Error()}
    }

    // Block until the new task reaches a recognised status (reuse wait loop).
    return r.t.waitDelegatedTask(ctx, resp.TaskID, timeout)
}
```

Add `&resumeTaskTool{t}` to `Tools.All()` right after `&waitTaskTool{t}`.

Also extend `waitDelegatedTask` (already exists for permissions) to use the same `kind`-aware unwrap logic as `wait_task`, so the response shape matches. The cleanest refactor: extract `marshalDelegatedTaskOutput` to share the "awaiting_user vs final vs legacy" branching with `wait_task` / `get_task` from Task 13.

- [ ] **Step 4: Run, expect pass**

```bash
cd multi-agent && go test ./internal/driver/ -v
```

- [ ] **Step 5: Commit**

```bash
git add multi-agent/internal/driver/
git commit -m "feat(driver): resume_task tool (stateless via GetTask)"
```

---

## Task 15: System-prompt abuse guard

**Files:**
- Modify: `multi-agent/pkg/agentbackend/capability.go`
- Modify: `multi-agent/pkg/agentbackend/capability_test.go` (if exists)

- [ ] **Step 1: Append to `CapabilityEpilogue` constant**

Locate the const and append (preserve the existing text):

```go
const CapabilityEpilogue = existingText + `

You have two tools, ask_user and request_permission, that pause the chat to ask the human. Only call them when (a) you are genuinely uncertain how to proceed, (b) guessing wrong has a non-trivial cost (loss of work, irreversible side-effects, or wasted spend), and (c) the human can answer in one or two sentences. Otherwise: decide yourself and explain the assumption in your final summary. You have a small budget of questions per task; spend them carefully.

request_permission is advisory only — answering "approve" does NOT grant new abilities; the user must run update_slave_claude_permissions or register_slave_mcp separately to actually elevate.
`
```

(If `CapabilityEpilogue` is currently a single literal, just edit the literal in place — don't introduce `existingText` aliasing.)

- [ ] **Step 2: Build to verify**

```bash
cd multi-agent && go build ./...
```

- [ ] **Step 3: Commit**

```bash
git add multi-agent/pkg/agentbackend/
git commit -m "feat(backend): system-prompt guard for ask_user/request_permission abuse"
```

---

## Task 16: `skills/multiagent` documentation updates

**Files:**
- Modify: `multi-agent/skills/multiagent/SKILL.md`
- Modify: `multi-agent/skills/multiagent/references/driver-tools.md`
- Modify: `multi-agent/skills/multiagent/references/slave-skills.md`
- Modify: `multi-agent/skills/multiagent/references/task-contract.md`

- [ ] **Step 1: `SKILL.md` — append a section**

After the "Common Mistakes" section, add:

```markdown
## Mid-chat human-in-the-loop

Chat tasks (skill="chat") can now pause mid-conversation. wait_task may return
status:"awaiting_user" instead of completed. When that happens:

- The question field carries `kind` (ask_user | request_permission), `question`
  text, optional `options`, optional `context`.
- **Default behaviour: surface the question to the human via `AskUserQuestion`.
  Do not invent an answer.** Only auto-answer when the question text itself says
  e.g. *"do not ask user, decide based on X"*.
- Call resume_task(last_task_id=<current_task_id>, answer=<user's reply>) to
  continue. resume_task returns the same shape as wait_task — keep looping
  until you see status:"completed".

`request_permission` is **advisory**, not enforcement. Answering "approve" does
NOT grant the slave new abilities; if the user wants to actually elevate, call
update_slave_claude_permissions / register_slave_mcp separately.
```

Also extend the "Common Mistakes" list with two items:

```markdown
- Auto-answering an awaiting_user question without surfacing it to the real
  human (you are the proxy, not the decider).
- Treating "approve" answers to request_permission as if they granted new
  permissions — they don't.
```

- [ ] **Step 2: `references/driver-tools.md` — add resume_task**

After the wait_task subsection, add:

```markdown
### `resume_task`

Input:

\`\`\`json
{
  "last_task_id": "T-1",
  "answer": "yes, use option a",
  "timeout_sec": 600
}
\`\`\`

Continues a chat that wait_task returned as status:"awaiting_user". Stateless:
the driver re-reads last_task_id via GetTask to recover session_id and
target_id. Returns the same shape as wait_task — may itself be another
awaiting_user (multi-round questions) or a final completed.
```

Also extend wait_task / get_task documentation to mention the new
`status:"awaiting_user"` return.

- [ ] **Step 3: `references/slave-skills.md` — add chat_resume + humanloop**

Add to the skills list:

```markdown
### `chat_resume`

JSON-prompt skill. The driver's resume_task tool delegates to this — do not
call it directly via submit_task unless you know what you're doing. Prompt
shape:

\`\`\`json
{ "session_id": "S-...", "answer": "...", "kind": "ask_user|request_permission" }
\`\`\`

Slave re-runs the chat backend with `claude --resume <S>` (or `codex resume`),
feeding "User answered: <answer>" as the next user turn.
```

Also add a "humanloop MCP server" note explaining that every chat skill has
two extra tools (`ask_user`, `request_permission`) injected, and that the
backend may pause and return `result.kind:"awaiting_user"`.

- [ ] **Step 4: `references/task-contract.md` — add chat_resume to allowlist**

In the JSON-prompt skills list, append `chat_resume`.

- [ ] **Step 5: Commit**

```bash
git add multi-agent/skills/multiagent/
git commit -m "docs(skill): mid-chat human-in-the-loop section + resume_task + chat_resume"
```

---

## Task 17: E2E scripts framework + Case 1 + Case 2

**Files:**
- Create: `multi-agent/tests/prod_test/scripts/humanloop_helpers.sh`
- Create: `multi-agent/tests/prod_test/scripts/e2e_humanloop_happy.sh`

**Pre-req:** A working `tests/prod_test` deployment per its README — driver-prod, slave-local-prod running and registered with the prod observer.

- [ ] **Step 1: Shared helpers**

`humanloop_helpers.sh`:

```bash
#!/usr/bin/env bash
# Helpers for prod_test humanloop e2e scripts. Source me, don't exec.
# We drive the real driver MCP server (driver-agent serve-mcp) over stdio
# the same way Claude Code does: initialize → tools/call → read response,
# then close stdin. The server exits when stdin closes.
set -euo pipefail

: "${PROD_TEST_DIR:=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
DRIVER_BIN="${PROD_TEST_DIR}/bin/driver-agent.linux-amd64"
DRIVER_CFG="${PROD_TEST_DIR}/driver/config.yaml"

# call_tool TOOL JSON_ARGS → prints the tool's response payload (the JSON
# string inside .result.content[0].text). Spawns a fresh driver-agent
# serve-mcp for each call, which is fine for e2e (cold-start ≈ 200ms).
call_tool() {
  local tool="$1" args="$2"
  {
    printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"e2e","version":"0"}}}'
    printf '%s\n' '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}'
    jq -cn --arg t "$tool" --argjson a "$args" \
      '{jsonrpc:"2.0",id:2,method:"tools/call",params:{name:$t,arguments:$a}}'
  } | "$DRIVER_BIN" serve-mcp --config "$DRIVER_CFG" 2>/dev/null \
    | awk 'NR>1' \
    | jq -r 'select(.id==2) | .result.content[0].text'
}

submit_chat() {
  local prompt="$1" target="${2:-slave-local-prod}"
  call_tool submit_task "$(jq -cn --arg p "$prompt" --arg tn "$target" \
    '{prompt:$p,target_display_name:$tn,skill:"chat"}')"
}
wait_for()    { call_tool wait_task    "$(jq -cn --arg t "$1" '{task_id:$t,timeout_sec:120}')"; }
get_task()    { call_tool get_task     "$(jq -cn --arg t "$1" '{task_id:$t}')"; }
resume_with() { call_tool resume_task  "$(jq -cn --arg t "$1" --arg a "$2" \
                                          '{last_task_id:$t,answer:$a,timeout_sec:120}')"; }

# field FIELD < tool_response → print the value at .<FIELD> in the inner JSON.
# Usage:  wait_for "$task" | tee /tmp/x ; status=$(field status </tmp/x)
field() { jq -r ".$1"; }
```

Notes on the helper:
- `awk 'NR>1'` skips the initialize response so jq only sees the tools/call response.
- If `driver-agent serve-mcp` does not return on stdin-close, the e2e helper hangs forever; verify with `echo {} | $DRIVER_BIN serve-mcp --config $DRIVER_CFG` during impl. If it doesn't, add `--exit-on-eof` (or a similar one-shot mode) to `cmd/driver-agent/main.go` as a tiny prereq commit, **and update this helper accordingly.**

- [ ] **Step 2: Case 1+2 script**

`e2e_humanloop_happy.sh`:

```bash
#!/usr/bin/env bash
source "$(dirname "$0")/humanloop_helpers.sh"

echo "=== Case 1: happy chat with humanloop MCP injected, no ask_user called ==="
submit=$(submit_chat 'Reply with the single word "HELLO" and stop.' slave-local-prod)
task_id=$(echo "$submit" | field task_id)
wait=$(wait_for "$task_id")
status=$(echo "$wait" | field status)
output=$(echo "$wait" | field output)
[[ "$status" == "completed" ]] || { echo "FAIL: case 1 status=$status"; exit 1; }
[[ "$output" == *HELLO* ]]      || { echo "FAIL: case 1 output=$output"; exit 1; }
echo "PASS case 1"

echo "=== Case 2: single ask_user → resume → final ==="
submit=$(submit_chat 'Call the ask_user tool with question="What color?" options=["red","blue"]. After you receive the answer in your next user turn, reply with exactly that color word and stop.' slave-local-prod)
task_id=$(echo "$submit" | field task_id)
wait=$(wait_for "$task_id")
status=$(echo "$wait" | field status)
[[ "$status" == "awaiting_user" ]] || { echo "FAIL: case 2 not awaiting; got $status"; exit 1; }
current=$(echo "$wait" | field current_task_id)
resume=$(resume_with "$current" "blue")
status=$(echo "$resume" | field status)
output=$(echo "$resume" | field output)
[[ "$status" == "completed" ]] || { echo "FAIL: case 2 resume status=$status"; exit 1; }
[[ "$output" == *blue* ]]       || { echo "FAIL: case 2 output=$output"; exit 1; }
echo "PASS case 2"
```

- [ ] **Step 3: Make scripts executable + run**

```bash
chmod +x multi-agent/tests/prod_test/scripts/*.sh
# (After deploying the new slave-agent binary to slave-local-prod first!)
multi-agent/tests/prod_test/scripts/e2e_humanloop_happy.sh
```

Expected: `PASS case 1` then `PASS case 2`.

- [ ] **Step 4: Commit (regardless of pass/fail; fix iteratively if needed)**

```bash
git add multi-agent/tests/prod_test/scripts/
git commit -m "test(e2e): humanloop happy paths (cases 1+2)"
```

If the run failed, iterate on impl + commit fixes; do **not** mark this task complete until the script passes against the real prod_test deployment.

---

## Task 18: E2E Case 3 + Case 4 (multi-round + request_permission)

**Files:**
- Create: `multi-agent/tests/prod_test/scripts/e2e_humanloop_multi.sh`

- [ ] **Step 1: Write the script**

```bash
#!/usr/bin/env bash
source "$(dirname "$0")/humanloop_helpers.sh"

echo "=== Case 3: two rounds of ask_user before final ==="
submit=$(submit_chat 'Call ask_user with question="First step?" options=["A","B"]. After the answer, call ask_user again with question="Second step?" options=["X","Y"]. After that answer, reply with both answers separated by "+" and stop.' slave-local-prod)
task=$(echo "$submit" | field task_id)
w1=$(wait_for "$task")
[[ "$(echo "$w1" | field status)" == "awaiting_user" ]] || { echo "FAIL c3 round 1"; exit 1; }
cur=$(echo "$w1" | field current_task_id)
r1=$(resume_with "$cur" "A")
[[ "$(echo "$r1" | field status)" == "awaiting_user" ]] || { echo "FAIL c3 round 2 expected awaiting"; exit 1; }
cur=$(echo "$r1" | field current_task_id)
r2=$(resume_with "$cur" "Y")
status=$(echo "$r2" | field status)
output=$(echo "$r2" | field output)
[[ "$status" == "completed" ]] || { echo "FAIL c3 final status=$status"; exit 1; }
[[ "$output" == *"A+Y"* ]]      || { echo "FAIL c3 final output=$output"; exit 1; }
echo "PASS case 3"

echo "=== Case 4: request_permission marker distinct from ask_user ==="
submit=$(submit_chat 'Call request_permission with intent="run_bash" target="rm -rf /tmp/some_dir" reason="cleanup". After the answer, reply with the answer text verbatim.' slave-local-prod)
task=$(echo "$submit" | field task_id)
w=$(wait_for "$task")
kind=$(echo "$w" | jq -r '.result.content[0].text | fromjson | .question.kind')
intent=$(echo "$w" | jq -r '.result.content[0].text | fromjson | .question.intent')
target=$(echo "$w" | jq -r '.result.content[0].text | fromjson | .question.target')
[[ "$kind" == "request_permission" ]] || { echo "FAIL c4 kind=$kind"; exit 1; }
[[ "$intent" == "run_bash" ]]         || { echo "FAIL c4 intent=$intent"; exit 1; }
[[ "$target" == "rm -rf /tmp/some_dir" ]] || { echo "FAIL c4 target=$target"; exit 1; }
cur=$(echo "$w" | field current_task_id)
r=$(resume_with "$cur" "denied")
[[ "$(echo "$r" | field status)" == "completed" ]] || { echo "FAIL c4 final"; exit 1; }
echo "PASS case 4"
```

- [ ] **Step 2: Run**

```bash
multi-agent/tests/prod_test/scripts/e2e_humanloop_multi.sh
```

- [ ] **Step 3: Commit**

```bash
git add multi-agent/tests/prod_test/scripts/e2e_humanloop_multi.sh
git commit -m "test(e2e): humanloop multi-round + request_permission (cases 3+4)"
```

---

## Task 19: E2E Case 5 + Case 6 + Case 7 (jetson + offline + session-not-found)

**Files:**
- Create: `multi-agent/tests/prod_test/scripts/e2e_humanloop_failures.sh`

- [ ] **Step 1: Write the script**

```bash
#!/usr/bin/env bash
source "$(dirname "$0")/humanloop_helpers.sh"

echo "=== Case 5: chat with ask_user on Jetson (arm64, cross-node) ==="
submit=$(submit_chat 'Call ask_user with question="ready?" options=["yes","no"]. After the answer, reply with the answer verbatim and stop.' slave-jetson-prod)
task=$(echo "$submit" | field task_id)
w=$(wait_for "$task")
[[ "$(echo "$w" | field status)" == "awaiting_user" ]] || { echo "FAIL c5 wait"; exit 1; }
cur=$(echo "$w" | field current_task_id)
r=$(resume_with "$cur" "yes")
[[ "$(echo "$r" | field status)" == "completed" ]] || { echo "FAIL c5 resume"; exit 1; }
[[ "$(echo "$r" | field output)" == *yes* ]] || { echo "FAIL c5 output"; exit 1; }
echo "PASS case 5"

echo "=== Case 6: slave offline mid-thread → resume errors → restart → succeeds ==="
submit=$(submit_chat 'Call ask_user with question="continue?" and wait.' slave-local-prod)
task=$(echo "$submit" | field task_id)
w=$(wait_for "$task")
[[ "$(echo "$w" | field status)" == "awaiting_user" ]] || { echo "FAIL c6 wait"; exit 1; }
cur=$(echo "$w" | field current_task_id)

# Stop the slave container.
docker stop slave-local-prod >/dev/null
# resume_task should error cleanly (DelegateTask fails or task fails fast).
set +e
r_err=$(resume_with "$cur" "go ahead" 2>&1)
rc=$?
set -e
[[ $rc -ne 0 || "$r_err" == *error* || "$r_err" == *failed* ]] \
  || { echo "FAIL c6 resume should have errored, got: $r_err"; exit 1; }

# Bring slave back and retry resume.
docker start slave-local-prod >/dev/null
# Give agentserver a moment to re-mark it available.
sleep 8
r=$(resume_with "$cur" "go ahead")
[[ "$(echo "$r" | field status)" == "completed" ]] \
  || { echo "FAIL c6 resume after restart: $(echo "$r" | field status)"; exit 1; }
echo "PASS case 6"

echo "=== Case 7: session jsonl deleted → resume fails with clear reason ==="
submit=$(submit_chat 'Call ask_user with question="proceed?" and wait.' slave-local-prod)
task=$(echo "$submit" | field task_id)
w=$(wait_for "$task")
session=$(echo "$w" | field session_id)
cur=$(echo "$w" | field current_task_id)
[[ -n "$session" ]] || { echo "FAIL c7 no session_id"; exit 1; }

# Wipe the session jsonl inside the slave container.
docker exec slave-local-prod sh -c "rm -f /root/.claude/projects/*/${session}*.jsonl"

set +e
r=$(resume_with "$cur" "yes")
set -e
status=$(echo "$r" | field status)
reason=$(echo "$r" | jq -r '.failure_reason // .output // ""')
[[ "$status" == "failed" || "$status" == "error" ]] \
  || { echo "FAIL c7 expected failed status, got $status"; exit 1; }
[[ "$reason" == *session* || "$reason" == *resume* ]] \
  || { echo "FAIL c7 unclear failure_reason: $reason"; exit 1; }
echo "PASS case 7"
```

Notes:
- Case 6 assumes the local docker slave container is named `slave-local-prod` (per `tests/prod_test/slave/docker-compose.yml`; adjust if different).
- Case 7's session jsonl path assumes Claude Code's default `~/.claude/projects/<workdir-hash>/<session>.jsonl` layout. Spike-0 should have confirmed this; adjust the `rm` glob if the actual layout differs (or for codex slaves, target the codex thread store).

- [ ] **Step 2: Run**

- [ ] **Step 3: Commit**

```bash
git add multi-agent/tests/prod_test/scripts/e2e_humanloop_failures.sh
git commit -m "test(e2e): humanloop failure cases (jetson + offline + session-lost)"
```

---

## Task 20: E2E Case 8 + Case 9 + Case 10 (flock + quota + grace-shutdown)

**Files:**
- Create: `multi-agent/tests/prod_test/scripts/e2e_humanloop_defensive.sh`

- [ ] **Step 1: Write the script**

```bash
#!/usr/bin/env bash
source "$(dirname "$0")/humanloop_helpers.sh"

echo "=== Case 8: per-session flock — concurrent resume rejects one ==="
submit=$(submit_chat 'Call ask_user with question="continue?" and wait.' slave-local-prod)
task=$(echo "$submit" | field task_id)
w=$(wait_for "$task")
cur=$(echo "$w" | field current_task_id)

resume_with "$cur" "first"  > /tmp/c8a.json &
resume_with "$cur" "second" > /tmp/c8b.json &
wait

ok_count=0; busy_count=0
for f in /tmp/c8a.json /tmp/c8b.json; do
  s=$(field status < "$f")
  reason=$(jq -r '.failure_reason // .output // ""' < "$f")
  if [[ "$s" == "completed" ]]; then ok_count=$((ok_count+1));
  elif [[ "$reason" == *"session busy"* ]]; then busy_count=$((busy_count+1));
  else echo "FAIL c8 unexpected status=$s reason=$reason in $f"; exit 1; fi
done
[[ $ok_count -eq 1 && $busy_count -eq 1 ]] \
  || { echo "FAIL c8 ok=$ok_count busy=$busy_count"; exit 1; }
echo "PASS case 8"

echo "=== Case 9: question quota — 6th ask_user is refused, model proceeds ==="
# Default max_questions_per_task=5. Prompt explicitly tries 6 calls.
submit=$(submit_chat 'Call ask_user 6 times in a row, each with question="round N" (N=1..6). If a tool call returns {"status":"refused"}, stop calling ask_user, reply with the literal text "QUOTA HIT" and stop.' slave-local-prod)
task=$(echo "$submit" | field task_id)
# We expect awaiting_user up to 5 times if the model serialises them, but the
# guard kicks in even within a single tool call sequence. Easiest assertion:
# wait_for returns completed (not awaiting_user) once the quota refuses, and
# output contains "QUOTA HIT".
# The model may have produced ≥1 pause; resume each.
status="awaiting_user"; current="$task"
loops=0
while [[ "$status" == "awaiting_user" && $loops -lt 6 ]]; do
  r=$(if [[ $loops -eq 0 ]]; then wait_for "$current"; else resume_with "$current" "round-$loops"; fi)
  status=$(echo "$r" | field status)
  current=$(echo "$r" | field current_task_id 2>/dev/null || echo "")
  loops=$((loops+1))
done
[[ "$status" == "completed" ]] || { echo "FAIL c9 status=$status after $loops loops"; exit 1; }
output=$(echo "$r" | field output)
[[ "$output" == *"QUOTA HIT"* ]] || { echo "FAIL c9 no QUOTA HIT marker; got: $output"; exit 1; }
echo "PASS case 9"

echo "=== Case 10: graceful-shutdown timeout (synthetic backend stub) ==="
# We install a backend stub that:
#   - emits one stream-json system frame with a session_id
#   - calls into humanloop MCP to trigger pause
#   - then ignores stdin close and sleeps forever
# Verify the task ends "failed" with a SIGTERM/SIGKILL hint in failure_reason.
docker exec slave-local-prod bash -c '
cat > /usr/local/bin/claude-stub <<'"'"'BASH'"'"'
#!/bin/bash
echo '"'"'{"type":"system","session_id":"stub-sess"}'"'"'
# Pretend to call ask_user via humanloop, but since we cannot easily, just
# sleep forever after closing nothing.
sleep 999
BASH
chmod +x /usr/local/bin/claude-stub
'
# Tell the slave to use the stub for the next chat. Easiest: write a small
# override config and HUP / restart. We use the existing config edit pattern
# (do NOT scp-overwrite the full config — see prod_test_config_scp_trap memory).
docker exec slave-local-prod sh -c "sed -i 's|^  bin:.*|  bin: /usr/local/bin/claude-stub|' /root/.loom/slave-local/config.yaml"
docker exec slave-local-prod sh -c "sed -i 's|shutdown_grace_sec:.*|shutdown_grace_sec: 1|' /root/.loom/slave-local/config.yaml || echo 'humanloop:\n  shutdown_grace_sec: 1' >> /root/.loom/slave-local/config.yaml"
docker restart slave-local-prod >/dev/null
sleep 10

submit=$(submit_chat 'anything' slave-local-prod)
task=$(echo "$submit" | field task_id)
# wait with a short timeout — the stub will be SIGTERM'd within grace+5s.
r=$(wait_for "$task")
status=$(echo "$r" | field status)
reason=$(echo "$r" | jq -r '.failure_reason // .output // ""')
[[ "$status" == "failed" ]] || { echo "FAIL c10 status=$status"; exit 1; }
[[ "$reason" == *SIG* || "$reason" == *killed* || "$reason" == *timeout* ]] \
  || { echo "FAIL c10 reason missing signal hint: $reason"; exit 1; }
echo "PASS case 10"

# Restore the real claude bin and grace_sec.
docker exec slave-local-prod sh -c "sed -i 's|^  bin: /usr/local/bin/claude-stub|  bin: claude|' /root/.loom/slave-local/config.yaml"
docker exec slave-local-prod sh -c "sed -i 's|shutdown_grace_sec: 1|shutdown_grace_sec: 10|' /root/.loom/slave-local/config.yaml"
docker restart slave-local-prod >/dev/null
```

Notes:
- Case 10 mutates the slave config in-place (per `prod_test_config_scp_trap` memory: never `scp` the whole config; only sed individual fields). Restore at the end so subsequent runs aren't broken.
- If `shutdown_grace_sec: 10` line isn't present, the second sed appends a `humanloop:` block — adjust the heredoc shape if your config already has one.

- [ ] **Step 2: Run**

- [ ] **Step 3: Commit**

```bash
git add multi-agent/tests/prod_test/scripts/e2e_humanloop_defensive.sh
git commit -m "test(e2e): humanloop defensive cases (flock + quota + grace-shutdown)"
```

---

## Task 21: Final integration smoke + go vet + go test ./...

**Files:** None (verification only)

- [ ] **Step 1: Run full unit suite**

```bash
cd multi-agent && go vet ./... && go test ./...
```

Expected: PASS.

- [ ] **Step 2: Re-run all four e2e scripts against prod_test**

```bash
for s in multi-agent/tests/prod_test/scripts/e2e_humanloop_*.sh; do
  echo "== $s =="
  "$s" || { echo "FAIL: $s"; exit 1; }
done
```

Expected: every `PASS case N` line, exit 0.

- [ ] **Step 3: Document any TBD-prototype follow-ups discovered during e2e**

If Spike-0's findings revealed a need to patch session jsonl before resume, and that landed elsewhere in the plan, double-check the impl ships it. If new issues surfaced (codex resume quirks, timing flakes), note them in the spec's "Open questions" section or open a follow-up plan file.

- [ ] **Step 4: Commit any final adjustments (no-op if none)**

```bash
# only if anything changed:
git status
git add ...
git commit -m "chore: integration smoke + follow-up notes"
```

---

## Done definition

- All 21 tasks above commit-by-commit complete.
- `go test ./...` green.
- All four e2e scripts green against `tests/prod_test`.
- `docs/superpowers/specs/2026-05-26-humanloop-resumable-chat-design.md` and this plan committed.
- Spike-0 findings landed at `docs/spike-0-resume-tool_use.md`.
- `skills/multiagent` reflects the new tool / new status / new doctrine.

Hand off to `superpowers:finishing-a-development-branch` to decide on merge / PR.
