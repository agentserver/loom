# unregister_mcp — slave skill + driver tool 对称设计

**Date:** 2026-05-25
**Status:** Draft, pending user review

## 动机

`register_mcp` 让 dynamic-mcp 闭环只能加不能减。长跑的 slave 上 `dynamic_mcp.yaml` 会越积越多：

- agent 重写了一版替换原有 server（虽然 register 会覆盖同名 entry，但若改名重发就只是叠加）
- 实验失败 / 不再需要的旧 server 没法清退
- driver 想精简 CAPABILITIES 给规划用，没有手段裁掉冗余项

`unregister_mcp` 是 register 的逆操作：把 `dynamic_mcp.yaml` 里某个条目移除、杀掉对应 stdio 子进程、刷新 CAPABILITIES、重发卡片。整套调用面与 register 对称（slave skill + driver tool + observer event）。

## 范围 / 非范围

**范围**

- 新增 slave 侧 built-in skill `unregister_mcp`（discovery 开关与 `register_mcp` 对称）
- 新增 driver 侧 MCP tool `unregister_slave_mcp`
- `MCPExecutor` 新增 `UnregisterStdio(name) error`
- `dynamicmcp.go` 新增 `RemoveDynamicYAML(path, name) (removed bool, err error)`
- 新增 observer 事件常量 `EventMCPServerRemoved = "mcp_server_removed"`

**非范围（YAGNI）**

- 不批量 unregister（一次一个 name；agent 可循环调用）
- 不版本回滚 / 历史保留（git 是事实源）
- 不级联删源码文件 `generated_mcp/<name>/*.py`（源码是 agent 自己写的产物；如需清理由 bash 显式做）
- driver 不做 dry-run 预览（agent 调用前可自己 `inspect_capabilities`）
- 不能 unregister 静态配置的 MCP（只动 `dynamic_mcp.yaml`，不动 slave config）

## 接口

### Slave skill `unregister_mcp`

Prompt（JSON）：

```json
{
  "name": "echo",
  "if_present": false
}
```

| 字段 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `name` | string | 必填 | 要解除注册的 MCP server 名 |
| `if_present` | bool | `false` | `true` 时若 `dynamic_mcp.yaml` 里不存在该 name，返回成功 no-op；`false`（strict）时报错 |

返回 `Result.Summary` 形如：

```json
{"type":"mcp_unregistered","meta":{"name":"echo"}}
```

错误模式：

- prompt 非 JSON / `name` 为空 → error
- name 不在 `dynamic_mcp.yaml` 且 `if_present=false` → error `unregister_mcp: not registered: <name>`
- name 不在 `dynamic_mcp.yaml` 且 `if_present=true` → 返回成功，meta 含 `"removed":"false"`
- 写 yaml / kill 子进程失败 → error，向上抛

### Driver MCP tool `unregister_slave_mcp`

```json
{
  "type": "object",
  "properties": {
    "target_agent_id":     {"type": "string"},
    "target_display_name": {"type": "string"},
    "name":                {"type": "string"},
    "if_present":          {"type": "boolean"},
    "timeout_sec":         {"type": "integer"}
  },
  "required": ["name"],
  "additionalProperties": false
}
```

行为与 `register_slave_mcp` 对称：解析 target → 校验对端 advertise 了 `unregister_mcp` skill → `DelegateTask` skill=`unregister_mcp` → `waitDelegatedTask`。

## 实现要点

### 1. `MCPExecutor.UnregisterStdio`（`internal/executor/mcp.go`）

```go
var ErrMCPNotRegistered = errors.New("mcp server not registered")

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

- 与 `RegisterStdio` 对称、同一把 `mu`
- 不区分动态 / 静态来源：cfg 里有就删；约束「不能动静态 MCP」由上层 executor 通过「先查 dynamic_mcp.yaml」实现

### 2. `RemoveDynamicYAML`（`internal/executor/dynamicmcp.go`）

```go
func RemoveDynamicYAML(path, name string) (bool, error) {
    df, err := ReadDynamicYAML(path)
    if err != nil { return false, err }
    if _, ok := df.Servers[name]; !ok {
        return false, nil
    }
    delete(df.Servers, name)
    out, err := yaml.Marshal(df)
    if err != nil { return false, err }
    tmp := path + ".tmp"
    if err := os.WriteFile(tmp, out, 0o600); err != nil { return false, err }
    if err := os.Rename(tmp, path); err != nil { return false, err }
    return true, nil
}
```

- 原子 rename，复用 Upsert 的写盘风格
- `removed=false` 表示文件里本就没有该 name，区分于「写盘失败」

### 3. Executor `UnregisterMCPExecutor`（`internal/executor/unregistermcp.go`）

```go
type UnregisterMCPConfig struct {
    WorkDir   string
    MCPExec   *MCPExecutor
    Republish func(ctx context.Context) error
    Observer  Observer
}

type unregisterMCPPrompt struct {
    Name      string `json:"name"`
    IfPresent bool   `json:"if_present"`
}
```

Run 顺序：

1. JSON 解码 prompt；`Name` 为空 → error
2. `LookupDynamicEntry(DynamicYAMLPath(workDir), name)`
   - 不存在且 `!IfPresent` → error
   - 不存在且 `IfPresent` → return 成功 no-op（summary meta `removed=false`）
3. `MCPExec.UnregisterStdio(name)`：拿到 `ErrMCPNotRegistered` 时容忍（dynamic_mcp.yaml 有但内存里没建过 cfg —— 不该发生，但容忍以保证 yaml 单调），其它 error 上抛
4. `RemoveDynamicYAML(path, name)`：若返回 `removed=false` 说明步骤 2 与 3 之间有并发，按成功处理但 log warn
5. `Republish(ctx)`：失败 → sink warn 不上抛（与 register 风格对称）
6. emit `EventMCPServerRemoved`，payload 带 `name`
7. 返回 `Result{Summary: handleJSON{Type: "mcp_unregistered", Meta: {"name": name, "removed": "true|false"}}.Marshal()}`

### 4. Observer event（`internal/observer/event.go`）

```go
EventMCPServerRemoved = "mcp_server_removed"
```

事件 payload 字段沿用现有 `MCPServerName`；不再需要 `MCPTools`。

### 5. Slave 主入口接线（`cmd/slave-agent/main.go`）

紧跟 `register_mcp` 那段：

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

### 6. Driver tool（`internal/driver/unregister_mcp_tool.go`）

新文件，结构对称 `register_mcp_tool.go`：

```go
type unregisterSlaveMCPTool struct{ t *Tools }

func (u *unregisterSlaveMCPTool) Name() string { return "unregister_slave_mcp" }
func (u *unregisterSlaveMCPTool) Description() string {
    return "Unregister a dynamic MCP server on a slave via its unregister_mcp skill. Removes the entry from dynamic_mcp.yaml, kills its stdio subprocess, and republishes the slave card."
}
```

InputSchema 见上节。Call 中：

- 验证 `name` 非空
- `resolveAvailableAgent`
- `hasSkill(card, "unregister_mcp")` 检查
- `DelegateTask` → skill `unregister_mcp`，prompt 为 `{name, if_present}` JSON
- `waitDelegatedTask` 复用

在 `internal/driver/tools.go` 的 `All()` 中紧跟 `&registerSlaveMCPTool{t}` 插入 `&unregisterSlaveMCPTool{t}`。

### 7. Dispatch 注释

`internal/dispatch/dispatch.go` 第 66 行那条注释把 `register_mcp` 改成 `register_mcp/unregister_mcp`（仅注释；envelope strip 逻辑无变）。

## 测试

### `internal/executor/unregistermcp_test.go`

- `HappyPath`：先用 `NewRegisterMCPExecutor` 注册 echo → 调 unregister → 断言 `dynamic_mcp.yaml` 不含 echo、`MCPExec.Servers()` 不含 echo、observer 收到 `mcp_server_removed`
- `Strict_Missing_Errors`：空 workdir 直接 unregister，`if_present=false` → error，msg 含 `not registered`
- `IfPresent_Missing_NoOp`：同上但 `if_present=true` → 成功，summary meta `removed=false`
- `RejectsNonJSONPrompt`
- `RejectsEmptyName`

### `internal/executor/dynamicmcp_test.go`（如已存在则追加，否则新建）

- `RemoveDynamicYAML_RemovesExisting`
- `RemoveDynamicYAML_NoOpWhenMissing`：返回 `false, nil`
- `RemoveDynamicYAML_MissingFile`：文件不存在时返回 `false, nil`（与 Read 对齐：缺文件视为空表）

### `internal/driver/unregister_mcp_tool_test.go`

参照 `register_mcp_tool_test.go`：

- schema 含 `name` required；不含 spec/source_path
- target 不 advertise `unregister_mcp` → error
- delegate path：fake SDK 校验 skill=`unregister_mcp` 且 prompt JSON 反序列化得到正确 `name` 与 `if_present`

### `cmd/slave-agent/e2e_test.go`

如该文件已覆盖 register_mcp e2e，则追加一个 unregister 步骤（register → unregister → 再 `inspect`，确认 dynamic server 消失）。

## 兼容性 / 回滚

- 纯加法：现有 slave 不打开 `unregister_mcp` skill 行为不变
- driver tool 总是注册到 `Tools.All()`；若目标 slave 没 advertise 该 skill，driver 直接返回 `not advertised` 错误而不会破坏 register 流程
- 出问题回滚只需移除 driver tool 注册行和 slave 路由注册行

## 文档

- 更新 `README.md` slave skills 段（在 `register_mcp` 项后面加一行 `unregister_mcp` 简介）
- 更新 driver tools 列表（README 中那一段「Driver MCP 工具」加一项 `unregister_slave_mcp`）
- `examples/dynamic-mcp/`：保持不动（端到端示例仍以注册为主）；如有 README 描述长跑场景可补一句「冗余的可用 unregister 清退」
- 长跑结论与陷阱记入 memory：在 `memory/registermcp_reliability.md` 旁新增一条「unregister 默认 strict + 不删源码」的 feedback memory
