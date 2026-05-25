# unregister_mcp Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 给 dynamic-mcp 闭环加上"减法"——slave 侧 `unregister_mcp` skill 与 driver 侧 `unregister_slave_mcp` MCP tool 完全对称 register 路径，能把某个 dynamic MCP server 从 `dynamic_mcp.yaml` 移除、杀掉其 stdio 子进程、刷新 CAPABILITIES、重发卡片。

**Architecture:** 自底向上分四层：(1) `MCPExecutor.UnregisterStdio` 与 `RemoveDynamicYAML` 两个底层 helper；(2) `UnregisterMCPExecutor` 串起 yaml 移除 + 子进程 kill + republish + observer 事件；(3) slave-agent main 在 discovery 开关下挂路由；(4) driver-agent 新增 `unregister_slave_mcp` MCP tool，通过 `DelegateTask` 调到对端 skill。Strict 默认（找不到报错），`if_present:true` 走幂等 no-op。只动 `dynamic_mcp.yaml`，不触碰静态 MCP，不删源码。

**Tech Stack:** Go 1.x；testify/require；`gopkg.in/yaml.v3`；agentserver SDK；现有 `internal/executor`、`internal/driver`、`internal/observer`、`internal/dispatch` 包。

**Working directory for all commands:** `multi-agent/`（Go module 在子目录里；`cd multi-agent` 一次即可）。

**Spec:** `docs/superpowers/specs/2026-05-25-unregister-mcp-design.md`

---

## File Map

**Create:**
- `multi-agent/internal/executor/unregistermcp.go` — `UnregisterMCPExecutor` 与 `UnregisterMCPConfig`
- `multi-agent/internal/executor/unregistermcp_test.go` — executor 单元测试
- `multi-agent/internal/executor/dynamicmcp_test.go` — `RemoveDynamicYAML` 单元测试
- `multi-agent/internal/driver/unregister_mcp_tool.go` — `unregisterSlaveMCPTool`
- `multi-agent/internal/driver/unregister_mcp_tool_test.go` — driver tool 单元测试

**Modify:**
- `multi-agent/internal/observer/event.go` — 新增 `EventMCPServerRemoved` 常量
- `multi-agent/internal/executor/mcp.go` — 新增 `ErrMCPNotRegistered` 与 `UnregisterStdio` 方法
- `multi-agent/internal/executor/dynamicmcp.go` — 新增 `RemoveDynamicYAML`
- `multi-agent/cmd/slave-agent/main.go` — 在 `register_mcp` 路由挂载之后挂 `unregister_mcp`
- `multi-agent/internal/driver/tools.go` — `All()` 列表里加入 `&unregisterSlaveMCPTool{t}`
- `multi-agent/internal/dispatch/dispatch.go` — 改 envelope strip 注释提到 `unregister_mcp`
- `README.md` — slave skills 段加 `unregister_mcp`；driver tools 段加 `unregister_slave_mcp`

**Memory（在仓库外、不入 git）：**
- `/root/.claude/projects/-root-multi-agent/memory/MEMORY.md` 新增一行
- `/root/.claude/projects/-root-multi-agent/memory/unregister_mcp_semantics.md` 新文件

---

## Task 1：observer event 常量

**Files:**
- Modify: `multi-agent/internal/observer/event.go`（在 `EventMCPServerCreated` 紧邻一行加常量）

- [ ] **Step 1：读现有常量段定位**

Run: `grep -n "EventMCPServerCreated\|EventMCPServerBlocked" multi-agent/internal/observer/event.go`
Expected: 输出两行，分别是 `mcp_server_created` 与 `mcp_server_blocked` 常量定义所在行。

- [ ] **Step 2：插入新常量**

在 `EventMCPServerCreated         = "mcp_server_created"` 这一行紧跟着加：

```go
	EventMCPServerRemoved         = "mcp_server_removed"
```

（同段；保持原有对齐风格。）

- [ ] **Step 3：编译**

Run: `cd multi-agent && go build ./internal/observer/...`
Expected: 无输出，退出码 0。

- [ ] **Step 4：commit**

```bash
git add multi-agent/internal/observer/event.go
git commit -m "feat(observer): add EventMCPServerRemoved constant"
```

---

## Task 2：`MCPExecutor.UnregisterStdio` + `ErrMCPNotRegistered`

**Files:**
- Modify: `multi-agent/internal/executor/mcp.go`（在 `RegisterStdio` 之后追加新方法与 sentinel error）

- [ ] **Step 1：写失败测试（追加到 `multi-agent/internal/executor/registermcp_test.go` 末尾，或新建独立测试文件均可；这里追加到 registermcp_test.go 末尾以共用 helper）**

```go
func TestMCPExecutor_UnregisterStdio_Removes(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
	mcpExec := NewMCPExecutor(map[string]MCPServerCfg{})
	t.Cleanup(mcpExec.Close)
	require.NoError(t, mcpExec.RegisterStdio("echo", MCPServerCfg{
		Transport: "stdio", Command: "python3", Args: []string{"-c", "import sys; sys.exit(0)"},
	}))
	require.Contains(t, mcpExec.Servers(), "echo")

	require.NoError(t, mcpExec.UnregisterStdio("echo"))
	require.NotContains(t, mcpExec.Servers(), "echo")
}

func TestMCPExecutor_UnregisterStdio_NotRegistered(t *testing.T) {
	mcpExec := NewMCPExecutor(map[string]MCPServerCfg{})
	t.Cleanup(mcpExec.Close)
	err := mcpExec.UnregisterStdio("nope")
	require.ErrorIs(t, err, ErrMCPNotRegistered)
}
```

- [ ] **Step 2：跑测试确认失败**

Run: `cd multi-agent && go test ./internal/executor/ -run "TestMCPExecutor_UnregisterStdio" -v`
Expected: 编译失败，错误信息提到 `UnregisterStdio undefined` 与 `ErrMCPNotRegistered undefined`。

- [ ] **Step 3：实现 `ErrMCPNotRegistered` + `UnregisterStdio`（mcp.go）**

在 `mcp.go` 顶部 `import` 块确认包含 `"errors"`（若没有则加上）。在 `RegisterStdio` 方法定义之后插入：

```go
// ErrMCPNotRegistered is returned when an unregister target is unknown.
var ErrMCPNotRegistered = errors.New("mcp server not registered")

// UnregisterStdio removes a stdio MCP server entry at runtime, killing any
// running subprocess. Returns ErrMCPNotRegistered if no entry exists for
// name. Symmetric to RegisterStdio.
func (e *MCPExecutor) UnregisterStdio(name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.cfg[name]; !ok {
		return ErrMCPNotRegistered
	}
	if h, ok := e.stdios[name]; ok {
		h.kill()
		delete(e.stdios, name)
	}
	delete(e.cfg, name)
	return nil
}
```

- [ ] **Step 4：跑测试确认通过**

Run: `cd multi-agent && go test ./internal/executor/ -run "TestMCPExecutor_UnregisterStdio" -v`
Expected: 两个 case 均 PASS。

- [ ] **Step 5：跑整包测试，确保未回归**

Run: `cd multi-agent && go test ./internal/executor/... -count=1`
Expected: 全部 PASS。

- [ ] **Step 6：commit**

```bash
git add multi-agent/internal/executor/mcp.go multi-agent/internal/executor/registermcp_test.go
git commit -m "feat(executor): MCPExecutor.UnregisterStdio + ErrMCPNotRegistered"
```

---

## Task 3：`RemoveDynamicYAML`

**Files:**
- Modify: `multi-agent/internal/executor/dynamicmcp.go`
- Create: `multi-agent/internal/executor/dynamicmcp_test.go`

- [ ] **Step 1：写失败测试（新建文件）**

`multi-agent/internal/executor/dynamicmcp_test.go`：

```go
package executor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRemoveDynamicYAML_RemovesExisting(t *testing.T) {
	work := t.TempDir()
	path := DynamicYAMLPath(work)
	require.NoError(t, UpsertDynamicYAML(path, DynamicEntry{
		Name: "echo", Transport: "stdio", Command: "python3", Args: []string{"e.py"}, Version: 1,
	}))

	removed, err := RemoveDynamicYAML(path, "echo")
	require.NoError(t, err)
	require.True(t, removed)

	df, err := ReadDynamicYAML(path)
	require.NoError(t, err)
	require.NotContains(t, df.Servers, "echo")
}

func TestRemoveDynamicYAML_NoOpWhenMissing(t *testing.T) {
	work := t.TempDir()
	path := DynamicYAMLPath(work)
	require.NoError(t, UpsertDynamicYAML(path, DynamicEntry{
		Name: "keep", Transport: "stdio", Command: "python3", Args: []string{"k.py"}, Version: 1,
	}))

	removed, err := RemoveDynamicYAML(path, "absent")
	require.NoError(t, err)
	require.False(t, removed)

	df, err := ReadDynamicYAML(path)
	require.NoError(t, err)
	require.Contains(t, df.Servers, "keep")
}

func TestRemoveDynamicYAML_MissingFile(t *testing.T) {
	work := t.TempDir()
	path := filepath.Join(work, "dynamic_mcp.yaml")
	_, err := os.Stat(path)
	require.True(t, os.IsNotExist(err), "precondition: file must not exist")

	removed, err := RemoveDynamicYAML(path, "anything")
	require.NoError(t, err)
	require.False(t, removed)
}
```

- [ ] **Step 2：跑测试确认失败**

Run: `cd multi-agent && go test ./internal/executor/ -run "TestRemoveDynamicYAML" -v`
Expected: 编译失败，`RemoveDynamicYAML undefined`。

- [ ] **Step 3：实现 `RemoveDynamicYAML`**

在 `multi-agent/internal/executor/dynamicmcp.go` 末尾追加：

```go
// RemoveDynamicYAML deletes the named server entry from dynamic_mcp.yaml at
// path. Returns removed=true if the entry existed and was removed, false if
// the entry was not present (including when the file does not exist).
// Uses an atomic rename for the write.
func RemoveDynamicYAML(path, name string) (bool, error) {
	df, err := ReadDynamicYAML(path)
	if err != nil {
		return false, err
	}
	if _, ok := df.Servers[name]; !ok {
		return false, nil
	}
	delete(df.Servers, name)
	out, err := yaml.Marshal(df)
	if err != nil {
		return false, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return false, err
	}
	return true, nil
}
```

- [ ] **Step 4：跑测试确认通过**

Run: `cd multi-agent && go test ./internal/executor/ -run "TestRemoveDynamicYAML" -v`
Expected: 三个 case 均 PASS。

- [ ] **Step 5：commit**

```bash
git add multi-agent/internal/executor/dynamicmcp.go multi-agent/internal/executor/dynamicmcp_test.go
git commit -m "feat(executor): RemoveDynamicYAML for dynamic_mcp.yaml entries"
```

---

## Task 4：`UnregisterMCPExecutor`

**Files:**
- Create: `multi-agent/internal/executor/unregistermcp.go`
- Create: `multi-agent/internal/executor/unregistermcp_test.go`

- [ ] **Step 1：写失败测试（新建文件）**

`multi-agent/internal/executor/unregistermcp_test.go`：

```go
package executor

import (
	"context"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/observer"
)

// captureObserver records emitted events for assertion.
type captureObserver struct{ events []observer.Event }

func (c *captureObserver) Emit(ev observer.Event) { c.events = append(c.events, ev) }

// registerEchoForUnregister registers a minimal echo MCP via the RegisterMCPExecutor
// so the unregister path has a real entry to operate on.
func registerEchoForUnregister(t *testing.T) (*UnregisterMCPExecutor, *MCPExecutor, string, *captureObserver) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
	work := t.TempDir()
	mcpExec := NewMCPExecutor(map[string]MCPServerCfg{})
	t.Cleanup(mcpExec.Close)
	repub := func(ctx context.Context) error { return nil }
	obs := &captureObserver{}

	reg := NewRegisterMCPExecutor(RegisterMCPConfig{
		WorkDir:   work,
		MCPExec:   mcpExec,
		Republish: repub,
		Observer:  obs,
	})
	writeSource(t, work, "generated_mcp/echo/v1.py", minimalMCPSource)
	regPrompt := `{
		"spec": {
			"name": "echo",
			"description": "Echo tool",
			"version": 1,
			"tools": [{"name":"echo","description":"echo","args_schema":{"type":"object"},"result_description":"r"}],
			"allowed_packages": []
		},
		"source_path": "generated_mcp/echo/v1.py"
	}`
	_, err := reg.Run(context.Background(), Task{ID: "reg", Skill: "register_mcp", Prompt: regPrompt}, &nopSink{})
	require.NoError(t, err)

	obs.events = nil // discard register events so unregister assertions are clean

	unreg := NewUnregisterMCPExecutor(UnregisterMCPConfig{
		WorkDir:   work,
		MCPExec:   mcpExec,
		Republish: repub,
		Observer:  obs,
	})
	return unreg, mcpExec, work, obs
}

func TestUnregisterMCP_HappyPath(t *testing.T) {
	unreg, mcpExec, work, obs := registerEchoForUnregister(t)

	res, err := unreg.Run(context.Background(), Task{ID: "u1", Skill: "unregister_mcp", Prompt: `{"name":"echo"}`}, &nopSink{})
	require.NoError(t, err)
	require.Contains(t, res.Summary, `"type":"mcp_unregistered"`)
	require.Contains(t, res.Summary, `"name":"echo"`)
	require.Contains(t, res.Summary, `"removed":"true"`)

	df, err := ReadDynamicYAML(DynamicYAMLPath(work))
	require.NoError(t, err)
	require.NotContains(t, df.Servers, "echo")

	require.NotContains(t, mcpExec.Servers(), "echo")

	require.Len(t, obs.events, 1)
	require.Equal(t, observer.EventMCPServerRemoved, obs.events[0].Type)
	require.Equal(t, "echo", obs.events[0].MCPServerName)
}

func TestUnregisterMCP_StrictMissingErrors(t *testing.T) {
	work := t.TempDir()
	mcpExec := NewMCPExecutor(map[string]MCPServerCfg{})
	t.Cleanup(mcpExec.Close)
	unreg := NewUnregisterMCPExecutor(UnregisterMCPConfig{
		WorkDir: work, MCPExec: mcpExec,
		Republish: func(ctx context.Context) error { return nil },
	})

	_, err := unreg.Run(context.Background(), Task{ID: "u", Prompt: `{"name":"nope"}`}, &nopSink{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not registered")
}

func TestUnregisterMCP_IfPresentMissingNoOp(t *testing.T) {
	work := t.TempDir()
	mcpExec := NewMCPExecutor(map[string]MCPServerCfg{})
	t.Cleanup(mcpExec.Close)
	obs := &captureObserver{}
	unreg := NewUnregisterMCPExecutor(UnregisterMCPConfig{
		WorkDir: work, MCPExec: mcpExec,
		Republish: func(ctx context.Context) error { return nil },
		Observer:  obs,
	})

	res, err := unreg.Run(context.Background(), Task{ID: "u", Prompt: `{"name":"nope","if_present":true}`}, &nopSink{})
	require.NoError(t, err)
	require.Contains(t, res.Summary, `"type":"mcp_unregistered"`)
	require.Contains(t, res.Summary, `"removed":"false"`)
	require.Empty(t, obs.events, "no observer event when nothing was removed")
}

func TestUnregisterMCP_RejectsNonJSONPrompt(t *testing.T) {
	work := t.TempDir()
	mcpExec := NewMCPExecutor(map[string]MCPServerCfg{})
	t.Cleanup(mcpExec.Close)
	unreg := NewUnregisterMCPExecutor(UnregisterMCPConfig{WorkDir: work, MCPExec: mcpExec})

	_, err := unreg.Run(context.Background(), Task{ID: "u", Prompt: "not json"}, &nopSink{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be JSON")
}

func TestUnregisterMCP_RejectsEmptyName(t *testing.T) {
	work := t.TempDir()
	mcpExec := NewMCPExecutor(map[string]MCPServerCfg{})
	t.Cleanup(mcpExec.Close)
	unreg := NewUnregisterMCPExecutor(UnregisterMCPConfig{WorkDir: work, MCPExec: mcpExec})

	_, err := unreg.Run(context.Background(), Task{ID: "u", Prompt: `{"name":""}`}, &nopSink{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "name is required")
}
```

- [ ] **Step 2：跑测试确认失败**

Run: `cd multi-agent && go test ./internal/executor/ -run "TestUnregisterMCP" -v`
Expected: 编译失败，`UnregisterMCPExecutor undefined`、`NewUnregisterMCPExecutor undefined`、`UnregisterMCPConfig undefined`。

- [ ] **Step 3：实现 executor（新建文件）**

`multi-agent/internal/executor/unregistermcp.go`：

```go
package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/yourorg/multi-agent/internal/observer"
)

// UnregisterMCPConfig wires UnregisterMCPExecutor to its slave-side
// dependencies. Symmetric to RegisterMCPConfig.
type UnregisterMCPConfig struct {
	WorkDir   string
	MCPExec   *MCPExecutor
	Republish func(ctx context.Context) error
	Observer  Observer
}

// UnregisterMCPExecutor removes a dynamic MCP server: drops it from
// dynamic_mcp.yaml, kills its stdio subprocess, refreshes capabilities,
// and emits an observer event. Source files under generated_mcp/ are NOT
// deleted (callers wanting that should use bash explicitly).
type UnregisterMCPExecutor struct {
	cfg UnregisterMCPConfig
}

func NewUnregisterMCPExecutor(cfg UnregisterMCPConfig) *UnregisterMCPExecutor {
	return &UnregisterMCPExecutor{cfg: cfg}
}

func (e *UnregisterMCPExecutor) emit(ev observer.Event) {
	if e.cfg.Observer == nil {
		return
	}
	defer func() { _ = recover() }()
	e.cfg.Observer.Emit(ev)
}

type unregisterMCPPrompt struct {
	Name      string `json:"name"`
	IfPresent bool   `json:"if_present"`
}

// Run executes the unregister_mcp skill. The task prompt must be JSON
// with field "name" (required) and optional "if_present" (default false).
// When if_present is false (strict, the default), the call errors if no
// matching entry exists in dynamic_mcp.yaml.
func (e *UnregisterMCPExecutor) Run(ctx context.Context, t Task, sink Sink) (Result, error) {
	defer sink.Close()

	var p unregisterMCPPrompt
	if err := json.Unmarshal([]byte(t.Prompt), &p); err != nil {
		return Result{}, fmt.Errorf("unregister_mcp prompt must be JSON: %w", err)
	}
	if p.Name == "" {
		return Result{}, fmt.Errorf("unregister_mcp: name is required")
	}

	yamlPath := DynamicYAMLPath(e.cfg.WorkDir)
	_, present := LookupDynamicEntry(yamlPath, p.Name)
	if !present {
		if !p.IfPresent {
			return Result{}, fmt.Errorf("unregister_mcp: not registered: %s", p.Name)
		}
		handle := handleJSON{
			Type: "mcp_unregistered",
			Meta: map[string]string{"name": p.Name, "removed": "false"},
		}
		return Result{Summary: handle.Marshal()}, nil
	}

	if err := e.cfg.MCPExec.UnregisterStdio(p.Name); err != nil && !errors.Is(err, ErrMCPNotRegistered) {
		return Result{}, fmt.Errorf("unregister_mcp: kill stdio: %w", err)
	}

	removed, err := RemoveDynamicYAML(yamlPath, p.Name)
	if err != nil {
		return Result{}, fmt.Errorf("unregister_mcp: persist: %w", err)
	}
	if !removed {
		sink.Write("warn", fmt.Sprintf("unregister_mcp: yaml entry %q vanished between lookup and remove", p.Name))
	}

	if e.cfg.Republish != nil {
		if err := e.cfg.Republish(ctx); err != nil {
			sink.Write("warn", fmt.Sprintf("unregister_mcp: republish: %v", err))
		}
	}

	e.emit(observer.Event{
		Type:          observer.EventMCPServerRemoved,
		TaskID:        t.ID,
		MCPServerName: p.Name,
		Status:        "completed",
	})

	handle := handleJSON{
		Type: "mcp_unregistered",
		Meta: map[string]string{"name": p.Name, "removed": "true"},
	}
	return Result{Summary: handle.Marshal()}, nil
}
```

- [ ] **Step 4：跑测试确认通过**

Run: `cd multi-agent && go test ./internal/executor/ -run "TestUnregisterMCP" -v`
Expected: 五个 case 全部 PASS。

- [ ] **Step 5：跑包内全测，确认无回归**

Run: `cd multi-agent && go test ./internal/executor/... -count=1`
Expected: 全部 PASS。

- [ ] **Step 6：commit**

```bash
git add multi-agent/internal/executor/unregistermcp.go multi-agent/internal/executor/unregistermcp_test.go
git commit -m "feat(executor): UnregisterMCPExecutor for unregister_mcp skill"
```

---

## Task 5：slave-agent 路由接线

**Files:**
- Modify: `multi-agent/cmd/slave-agent/main.go`（在 `register_mcp` block 后追加；当前是 258–268 行附近）

- [ ] **Step 1：定位现有 register_mcp block**

Run: `grep -n 'register_mcp\|hasSkill(cfg.Discovery.Skills,' multi-agent/cmd/slave-agent/main.go`
Expected: 看到 register_mcp 的 `hasSkill` 行与 `routes["register_mcp"] = executor.NewRegisterMCPExecutor(...)`。记下其结束括号（`})`）所在行。

- [ ] **Step 2：在 register_mcp block 闭合 `}` 之后立刻插入 unregister 路由**

新增（紧跟 register block 之后）：

```go
		if hasSkill(cfg.Discovery.Skills, "unregister_mcp") {
			routes["unregister_mcp"] = executor.NewUnregisterMCPExecutor(executor.UnregisterMCPConfig{
				WorkDir: workdir,
				MCPExec: mcpExec,
				Republish: func(ctx context.Context) error {
					refreshCapabilities(ctx, "unregister_mcp removed MCP server")
					return tn.PublishCard(ctx)
				},
				Observer: obs,
			})
		}
```

注意缩进与外层 if/for 对齐（与 register_mcp block 完全一致即可）。

- [ ] **Step 3：编译 + vet**

Run: `cd multi-agent && go build ./cmd/slave-agent/... && go vet ./cmd/slave-agent/...`
Expected: 无错误，退出码 0。

- [ ] **Step 4：跑 slave-agent 单测（如果存在），确认无回归**

Run: `cd multi-agent && go test ./cmd/slave-agent/... -count=1`
Expected: 全部 PASS（或 SKIP，凡是已经 skip 的就让它继续 skip）。

- [ ] **Step 5：commit**

```bash
git add multi-agent/cmd/slave-agent/main.go
git commit -m "feat(slave): wire unregister_mcp skill route via discovery opt-in"
```

---

## Task 6：driver MCP tool `unregister_slave_mcp`

**Files:**
- Create: `multi-agent/internal/driver/unregister_mcp_tool.go`
- Create: `multi-agent/internal/driver/unregister_mcp_tool_test.go`
- Modify: `multi-agent/internal/driver/tools.go`（`All()` 列表里加一项）

- [ ] **Step 1：写失败测试（新建文件）**

`multi-agent/internal/driver/unregister_mcp_tool_test.go`：

```go
package driver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/stretchr/testify/require"
)

func TestUnregisterSlaveMCP_DelegatesAsUnregisterMCPSkill(t *testing.T) {
	var delegated agentsdk.DelegateTaskRequest
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-b", DisplayName: "slave-b", Status: "available", Card: json.RawMessage(`{"skills":["chat","unregister_mcp"],"short_id":"sb"}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			delegated = req
			return &agentsdk.DelegateTaskResponse{TaskID: "task-unreg-1"}, nil
		},
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{
				TaskID: "task-unreg-1",
				Status: "completed",
				Result: json.RawMessage(`"unregistered"`),
			}, nil
		},
	}
	tool := toolByName(t, newTestTools(t, sdk), "unregister_slave_mcp")
	args := `{"target_display_name":"slave-b","name":"echo","if_present":true,"timeout_sec":60}`
	out, err := tool.Call(context.Background(), json.RawMessage(args))
	require.NoError(t, err)
	require.Equal(t, "slave-b", delegated.TargetID)
	require.Equal(t, "unregister_mcp", delegated.Skill)
	require.Contains(t, delegated.Prompt, `"name":"echo"`)
	require.Contains(t, delegated.Prompt, `"if_present":true`)
	require.Contains(t, string(out), "task-unreg-1")
}

func TestUnregisterSlaveMCP_RejectsSlaveWithoutSkill(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-c", DisplayName: "slave-c", Status: "available", Card: json.RawMessage(`{"skills":["chat"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			t.Fatalf("must not delegate when unregister_mcp skill is missing")
			return nil, nil
		},
	}
	tool := toolByName(t, newTestTools(t, sdk), "unregister_slave_mcp")
	args := `{"target_display_name":"slave-c","name":"echo"}`
	_, err := tool.Call(context.Background(), json.RawMessage(args))
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not advertise unregister_mcp")
}

func TestUnregisterSlaveMCP_RejectsEmptyName(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-d", DisplayName: "slave-d", Status: "available", Card: json.RawMessage(`{"skills":["unregister_mcp"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			t.Fatalf("must not delegate with empty name")
			return nil, nil
		},
	}
	tool := toolByName(t, newTestTools(t, sdk), "unregister_slave_mcp")
	args := `{"target_display_name":"slave-d","name":""}`
	_, err := tool.Call(context.Background(), json.RawMessage(args))
	require.Error(t, err)
	require.Contains(t, err.Error(), "name is required")
}
```

- [ ] **Step 2：跑测试确认失败**

Run: `cd multi-agent && go test ./internal/driver/ -run "TestUnregisterSlaveMCP" -v`
Expected: 编译失败，工具未找到（`toolByName` 找不到 `unregister_slave_mcp`），或 `unregisterSlaveMCPTool` 类型不存在。

- [ ] **Step 3：实现 tool（新建文件）**

`multi-agent/internal/driver/unregister_mcp_tool.go`：

```go
package driver

import (
	"context"
	"encoding/json"

	"github.com/agentserver/agentserver/pkg/agentsdk"
)

type unregisterSlaveMCPTool struct{ t *Tools }

func (u *unregisterSlaveMCPTool) Name() string { return "unregister_slave_mcp" }
func (u *unregisterSlaveMCPTool) Description() string {
	return "Unregister a dynamic MCP server on a slave via its unregister_mcp skill. Removes the entry from dynamic_mcp.yaml, kills its stdio subprocess, and republishes the slave card."
}
func (u *unregisterSlaveMCPTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
        "target_agent_id":{"type":"string"},
        "target_display_name":{"type":"string"},
        "name":{"type":"string"},
        "if_present":{"type":"boolean"},
        "timeout_sec":{"type":"integer"}
    },"required":["name"],"additionalProperties":false}`)
}

func (u *unregisterSlaveMCPTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		TargetAgentID     string `json:"target_agent_id"`
		TargetDisplayName string `json:"target_display_name"`
		Name              string `json:"name"`
		IfPresent         bool   `json:"if_present,omitempty"`
		TimeoutSec        int    `json:"timeout_sec,omitempty"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &MCPToolError{Message: "invalid args: " + err.Error()}
	}
	if args.Name == "" {
		return nil, &MCPToolError{Message: "name is required"}
	}
	card, err := u.t.resolveAvailableAgent(ctx, args.TargetAgentID, args.TargetDisplayName)
	if err != nil {
		return nil, err
	}
	if !hasSkill(card, "unregister_mcp") {
		return nil, &MCPToolError{Message: "target " + card.DisplayName + " does not advertise unregister_mcp"}
	}
	prompt, err := json.Marshal(struct {
		Name      string `json:"name"`
		IfPresent bool   `json:"if_present"`
	}{Name: args.Name, IfPresent: args.IfPresent})
	if err != nil {
		return nil, &MCPToolError{Message: err.Error()}
	}
	resp, err := u.t.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID:       card.AgentID,
		Skill:          "unregister_mcp",
		Prompt:         string(prompt),
		TimeoutSeconds: args.TimeoutSec,
	})
	if err != nil {
		return nil, &MCPToolError{Message: "delegate unregister_mcp task: " + err.Error()}
	}
	return u.t.waitDelegatedTask(ctx, resp.TaskID, args.TimeoutSec)
}
```

- [ ] **Step 4：注册到 `Tools.All()`**

修改 `multi-agent/internal/driver/tools.go`，在 `&registerSlaveMCPTool{t},` 行之后插入：

```go
		&unregisterSlaveMCPTool{t},
```

（保持与上一行同样的缩进与逗号风格。）

- [ ] **Step 5：跑测试确认通过**

Run: `cd multi-agent && go test ./internal/driver/ -run "TestUnregisterSlaveMCP" -v`
Expected: 三个 case 全部 PASS。

- [ ] **Step 6：跑包内全测**

Run: `cd multi-agent && go test ./internal/driver/... -count=1`
Expected: 全部 PASS。

- [ ] **Step 7：commit**

```bash
git add multi-agent/internal/driver/unregister_mcp_tool.go multi-agent/internal/driver/unregister_mcp_tool_test.go multi-agent/internal/driver/tools.go
git commit -m "feat(driver): unregister_slave_mcp MCP tool"
```

---

## Task 7：dispatch 注释 + README 文档

**Files:**
- Modify: `multi-agent/internal/dispatch/dispatch.go`
- Modify: `README.md`

- [ ] **Step 1：定位 dispatch 注释**

Run: `grep -n "bash/mcp/register_mcp can json.Unmarshal" multi-agent/internal/dispatch/dispatch.go`
Expected: 命中第 66 行附近的注释。

- [ ] **Step 2：修订注释**

把：

```go
	// the body and bash/mcp/register_mcp can json.Unmarshal it cleanly. Only
```

改为：

```go
	// the body and bash/mcp/register_mcp/unregister_mcp can json.Unmarshal it
	// cleanly. Only
```

（保持原注释剩余行结构；如果原注释只有一行，按你看到的实际内容做相应调整，保证语义不破。）

- [ ] **Step 3：跑 dispatch 包测试**

Run: `cd multi-agent && go test ./internal/dispatch/... -count=1`
Expected: 全部 PASS（仅注释改动，不应影响测试）。

- [ ] **Step 4：更新 README（slave skills）**

定位 README `### Slave 公开的核心 skills` 段，把：

```markdown
- `register_mcp` — 注册一段已经在 slave 上写好并通过烟雾测试的 MCP server 源码
```

紧跟着加一行：

```markdown
- `unregister_mcp` — 解除注册某个 dynamic MCP server（从 `dynamic_mcp.yaml` 移除、杀掉子进程、刷新 CAPABILITIES）；不删源码文件
```

- [ ] **Step 5：更新 README（driver tools）**

定位 `### Driver MCP 工具` 段，把：

```markdown
- `run_slave_bash` / `register_slave_mcp`
```

改为：

```markdown
- `run_slave_bash` / `register_slave_mcp` / `unregister_slave_mcp`
```

- [ ] **Step 6：commit**

```bash
git add multi-agent/internal/dispatch/dispatch.go README.md
git commit -m "docs: dispatch comment + README mention unregister_mcp"
```

---

## Task 8：全量回归 + memory

**Files:**
- 全仓 Go 测试
- Create: `/root/.claude/projects/-root-multi-agent/memory/unregister_mcp_semantics.md`
- Modify: `/root/.claude/projects/-root-multi-agent/memory/MEMORY.md`

- [ ] **Step 1：构建全模块**

Run: `cd multi-agent && go build ./...`
Expected: 无输出，退出码 0。

- [ ] **Step 2：vet 全模块**

Run: `cd multi-agent && go vet ./...`
Expected: 无输出，退出码 0。

- [ ] **Step 3：跑全量测试**

Run: `cd multi-agent && go test ./... -race -count=1`
Expected: 全部 PASS（contract / smoke 受 build tag 保护不会跑）。如果某测试因为本次改动失败，停下排查；与本次无关的预先 flaky 测试可记录但不能掩盖回归。

- [ ] **Step 4：跑 contract 测试**

Run: `cd multi-agent && go test -tags=contract ./tests/contract/... -count=1`
Expected: 全部 PASS。

- [ ] **Step 5：写 memory 文件**

新建 `/root/.claude/projects/-root-multi-agent/memory/unregister_mcp_semantics.md`：

```markdown
---
name: unregister-mcp-semantics
description: unregister_mcp 默认 strict（找不到报错），if_present 才幂等；只动 dynamic_mcp.yaml，不删源码、不动静态 MCP
metadata:
  type: feedback
---

`unregister_mcp` skill / `unregister_slave_mcp` driver tool 的默认契约：

- **strict 默认**：`if_present` 默认 false，目标 name 不在 `dynamic_mcp.yaml` 直接报错；幂等行为需显式 `if_present: true`
- **只动 dynamic**：dynamic_mcp.yaml 里没有的 name 视作"未注册"，即使内存里恰好有同名静态 MCP 也不动；不能用它去裁掉 slave config 静态配置的 MCP
- **不删源码**：`generated_mcp/<name>/v*.py` 由 agent 自己用 bash 显式清理；unregister 只解除注册关系
- **observer 事件**：成功移除时发 `EventMCPServerRemoved`；幂等 no-op 不发事件

**Why:** 与 [[registermcp_reliability]] 对称防回归——任何"看起来什么都没发生"的成功路径必须明确（要么真的改了状态并发事件，要么是 if_present no-op），避免悄悄改动静态配置或删源码这类"魔法"。

**How to apply:** 给 agent 写脚本调 unregister 前，先想清楚要 strict 还是 idempotent；若是 driver-side 批量重置 dynamic server，循环里要带 `if_present:true`，否则第一个找不到的就会中断整个清单。
```

- [ ] **Step 6：在 MEMORY.md 加索引行**

打开 `/root/.claude/projects/-root-multi-agent/memory/MEMORY.md`，在 `registermcp_reliability` 行下方新加一行：

```markdown
- [unregister_mcp 语义](unregister_mcp_semantics.md) — strict 默认；只动 dynamic_mcp.yaml，不删源码、不动静态 MCP
```

- [ ] **Step 7：commit 仓库内最终文档（如有）**

Run: `git status`
Expected: working tree clean（前面每个 Task 都 commit 过了；memory 文件在仓库外不会出现在 status）。如有未提交内容，归到合理的 commit message 里。

---

## Self-Review 已执行的修订记录

- 测试覆盖：spec 列出的所有测试用例（HappyPath / Strict / IfPresent / 非 JSON / 空 name / 拒绝无 skill 的 slave / 拒绝空 name）均映射到 Task 4 与 Task 6 中。
- 名称一致：`UnregisterStdio` / `ErrMCPNotRegistered` / `RemoveDynamicYAML` / `EventMCPServerRemoved` / `UnregisterMCPExecutor` / `unregister_slave_mcp` 在跨任务使用时拼写一致。
- handleJSON.Meta 类型为 `map[string]string`，所以 `removed` 字段用 `"true"`/`"false"` 字符串（test 断言已对齐）。
- e2e_test.go 在 spec 中标为"如已覆盖则追加"——本次没有把它列为必做任务，避免在不确定 e2e 是否触及 register_mcp 的情况下硬塞步骤；如需补，按 register 流程镜像即可，独立 PR 也行。
- README 段落定位指令使用现有内容做锚点（"register_mcp 的描述行"、"run_slave_bash / register_slave_mcp 那一行"），任何 README 微调过后仍可识别。
