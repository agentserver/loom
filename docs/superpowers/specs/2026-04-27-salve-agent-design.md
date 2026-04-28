# salve_agent 设计文档

- **日期**：2026-04-27
- **状态**：Draft（待实现）
- **语言**：Go
- **依赖**：`github.com/agentserver/agentserver/pkg/agentsdk`、`modernc.org/sqlite`、`gopkg.in/yaml.v3`
- **协议参考**：`agentserver/docs/developer/protocol.md`、`agentserver/docs/developer/quickstart.md`

## 0. 问题陈述

salve_agent 是接入 agentserver 的从属（custom）agent，负责：

1. 接受由 agentserver 派发的任务，按 `skill` 路由到对应执行器；
2. 执行结果回传 agentserver；
3. 通过隧道暴露的 Web UI 提供任务列表、实时输出 SSE 流、当前能力状态文档；
4. 维护一份 **CURRENT_STATE.md**，反映 agent 自身能力的累积变化；任何使能力发生变更的任务都触发该文档的合并更新。

不在范围：master_agent 的设计、跨 agent 编排、UI 美化。

## 1. 关键决策一览

| 维度 | 决策 |
|---|---|
| 实现语言 | Go（用官方 `agentsdk`） |
| 模式 | 同时启用 task executor + Web UI |
| 任务路由 | 按 task `skill` 字段：`"mcp"` → MCPExecutor，其他（含空）→ ClaudeExecutor |
| Claude 调用 | `claude --print --output-format=stream-json` 拉起一次性会话，逐行解析 stream-json |
| MCP 调用 | 同时支持 stdio 与 streamable HTTP；服务在 config.yaml 中静态声明 |
| MCP 任务格式 | `skill="mcp"`、`prompt = JSON {"server","tool","args"}` |
| 并发模型 | 串行（同时只跑一个任务） |
| 中间输出暴露 | Web UI `/tasks/{id}/stream` SSE；agentserver task status 只报 running/最终 summary |
| 配置与凭证 | 单个 `config.yaml`，device flow 后凭证写回同文件 |
| 持久化 | SQLite（`modernc.org/sqlite`，纯 Go） |
| 自我状态 | `salve_agent/journal/CURRENT_STATE.md` + `history.md`；只在能力变化时更新 |
| 能力变化判定 | Claude：在末消息内追加 `=== CAPABILITY ===` 段（无变化写 `NO_CAPABILITY_CHANGE`）；MCP：响应需带 `capability_changed: bool` + 可选 `change_hint` |

## 2. 架构总览

```
                ┌──────────────────────── salve_agent ────────────────────────┐
                │                                                             │
agentserver ◄───┤ tunnel    : agentsdk.Connect (yamux + 心跳 + 重连)          │
                │ poller    : 5s 轮询 /api/agent/tasks/poll (串行)            │
                │             ↓ skill 路由                                    │
                │ dispatcher├─ "mcp"  → MCPExecutor   ──┐                     │
                │           └─ 其他   → ClaudeExecutor ─┤                     │
                │                                       ↓                     │
                │ journal   : 判断 capability_changed                         │
                │             - false → 不动                                  │
                │             - true  → 调 claude 合并到 CURRENT_STATE.md    │
                │                      append 一行到 history.md               │
                │             ↓                                               │
                │ store     : SQLite tasks 表 + 内存 SSE pubsub               │
                │             ↓                                               │
                │ webui     : / · /tasks · /tasks/{id}/stream · /healthz      │
                │             · /state  (展示 CURRENT_STATE.md)               │
                │                                                             │
                │ 文件:  salve_agent/journal/CURRENT_STATE.md                 │
                │        salve_agent/journal/history.md                       │
                │        salve_agent/data.db                                  │
                │        salve_agent/config.yaml                              │
                └─────────────────────────────────────────────────────────────┘
                                       ↑
                               浏览器经 code-{shortID}.{baseDomain} 访问
```

### 核心边界

- `tunnel`：管 WebSocket 连接、心跳、重连；用 SDK，零自研。
- `poller`：定时器 + HTTP 调 task API；只负责拉任务、报状态。
- `dispatcher`：接 task → 查 skill → 选 executor → 拿到结果回报 poller。
- `executor`（接口）：两个实现，互不知情，只看入参产出 `Result{Summary, CapabilityChange}`。
- `store`：SQLite + SSE pubsub 一处，因为它们都是「任务的状态变化」的两种 sink。
- `webui`：纯读 store/journal，不写任何任务状态（避免双写源）。
- `journal`：CURRENT_STATE.md 的唯一作者；store 不重复存能力数据。

### 关键不变量

1. 任务状态唯一来源 = `store`；dispatcher 写，poller / webui 读。
2. agentserver 上的 task status 只反映 `assigned → running → completed|failed`，最终 `output` 是结果摘要文本；过程中的逐 token 输出只在 SSE 端点上看得到。
3. 串行：dispatcher 同时只跑一个 task，poller 在当前任务结束前不会拉下一个。
4. "能力" = 「下次执行任务时有用的、可枚举的状态变化」（已装的工具、新拿到的凭证、已挂载的资源、已配置的 MCP server 等）。一次性结果（聊天回答、查询返回值）不算。
5. 失败任务不触发 journal，避免脏状态污染 CURRENT_STATE.md。

## 3. 数据流和任务生命周期

### 3.1 Claude 路径

```
agentserver           poller          dispatcher        ClaudeExecutor       journal           store
    │                   │                 │                  │                  │                │
    │◄── poll ──────────┤                 │                  │                  │                │
    │── task ──────────►│                 │                  │                  │                │
    │                   │── PUT running ──►                  │                  │                │
    │◄──────────────────┤                 │                  │                  │                │
    │                   │── dispatch(t) ─►│                  │                  │                │
    │                   │                 │── insert(t) ──────────────────────────────────────► │
    │                   │                 │── run(t) ───────►│                  │                │
    │                   │                 │                  │ exec: claude --print              │
    │                   │                 │                  │   --output-format=stream-json     │
    │                   │                 │                  │   --append-system-prompt          │
    │                   │                 │                  │   (capability epilog)             │
    │                   │                 │                  │── stream chunk ─────────────────► │ pubsub→SSE
    │                   │                 │                  │── stream chunk ─────────────────► │
    │                   │                 │                  │   ... (N chunks)                  │
    │                   │                 │                  │── final assistant msg ────────────│
    │                   │                 │◄─── Result ──────┤                  │                │
    │                   │                 │     {summary,    │                  │                │
    │                   │                 │      capability_change?}            │                │
    │                   │                 │── record(t,r) ──────────────────────►                │
    │                   │                 │                  │                  │ if change ≠ ∅: │
    │                   │                 │                  │                  │   merge claude │
    │                   │                 │                  │                  │   write CS.md  │
    │                   │                 │                  │                  │   append hist  │
    │                   │                 │── update(done)──────────────────────────────────────►│
    │                   │◄── done(r) ─────┤                  │                  │                │
    │                   │── PUT completed ►                  │                  │                │
    │◄──────────────────┤                 │                  │                  │                │
```

**Capability epilogue**（追加到 system prompt 末尾）：

> 任务结束后，在最后一条 assistant 消息里追加一行 `=== CAPABILITY ===` 分隔，然后用 1-3 行说明本次执行**对你自己的能力或可用资源**带来的持久变化（新装的工具、写入的配置、挂载的目录…）。如果没有任何持久变化，只写 `NO_CAPABILITY_CHANGE`。

ClaudeExecutor 解析最后一条 assistant 消息：

- `=== CAPABILITY ===` 之前 = `Result.Summary`（→ agentserver 的 task `output` 字段）
- 之后 = `Result.CapabilityChange`（→ journal）；若是 `NO_CAPABILITY_CHANGE` 当作空字符串。

### 3.2 MCP 路径

```
poller → dispatcher → MCPExecutor:
    parse prompt JSON  {server, tool, args}
    server := config.MCPServers[server]   // stdio 拉起 / 复用 http client
    resp   := mcp.Call(server, tool, args)
    return Result{
        Summary:           resp.result_text or json.Marshal(resp.result),
        CapabilityChange:  resp.change_hint  if resp.capability_changed else "",
    }
→ journal: 同 3.1 规则
→ poller 上报 completed
```

**MCP 响应结构（salve_agent 期望）**：

```json
{
  "result": <任意 — 工具原生返回>,
  "capability_changed": true,
  "change_hint": "已挂载 /data 为只读卷"
}
```

没有 `capability_changed` 字段时按 false 处理；为 true 但缺 `change_hint` 时退化为占位 `"unspecified"`，让 journal 端自行解析 `result` 推断。

### 3.3 失败路径

任一阶段抛错 → dispatcher 拿到 `error` → `store.Fail(task, err.Error())` → poller `PUT failed{failure_reason: err.Error()}`。**失败任务不触发 journal**。

### 3.4 SSE 流

- `store.Subscribe(taskID) (<-chan Event, cancel func())`。
- 事件：`{type: "chunk"|"capability"|"done"|"error", data: ...}`。
- `webui` 的 `/tasks/{id}/stream`：
  - 任务还在跑：先一次性把 SQLite 已有 chunks 回放，再订阅活跃 channel；
  - 任务已结束：从 SQLite 一次性读完 chunks → 发 `event: done` → 关闭。

### 3.5 重启恢复

启动时扫 SQLite：所有 `running` / `assigned` 状态的任务标 `failed(reason="agent restarted")`，并入队 `pending_acks` 表，启动后第一轮 poller 一次性 `PUT failed` 追平 agentserver。

## 4. 组件细节

### 4.1 目录结构

```
salve_agent/
├── cmd/salve-agent/main.go            # 装配 + 启动
├── internal/
│   ├── config/      config.go         # 读 config.yaml
│   ├── tunnel/      tunnel.go         # 包 agentsdk.Client
│   ├── poller/      poller.go         # 任务轮询 + 状态回报
│   ├── dispatch/    dispatch.go       # skill → executor 路由
│   ├── executor/    executor.go       # 接口
│   │                claude.go         # ClaudeExecutor
│   │                mcp.go            # MCPExecutor (stdio + http)
│   ├── journal/     journal.go        # CURRENT_STATE 维护
│   ├── store/       store.go          # SQLite + SSE pubsub
│   │                schema.sql
│   └── webui/       server.go         # /, /tasks, /tasks/{id}/stream, /state, /healthz
│                    templates/*.html
├── journal/         CURRENT_STATE.md  (运行时生成)
│                    history.md        (运行时生成)
├── data.db                             (运行时生成)
├── config.yaml
├── go.mod
└── README.md
```

### 4.2 `config`

```go
type Config struct {
    Server struct {
        URL  string `yaml:"url"`
        Name string `yaml:"name"`
    } `yaml:"server"`

    Credentials struct {
        SandboxID    string `yaml:"sandbox_id"`
        TunnelToken  string `yaml:"tunnel_token"`
        ProxyToken   string `yaml:"proxy_token"`
        ShortID      string `yaml:"short_id"`
    } `yaml:"credentials"`

    Claude struct {
        Bin     string   `yaml:"bin"`
        WorkDir string   `yaml:"workdir"`
        Args    []string `yaml:"extra_args"`
    } `yaml:"claude"`

    MCPServers map[string]MCPServer `yaml:"mcp_servers"`

    Discovery struct {
        DisplayName string   `yaml:"display_name"`
        Description string   `yaml:"description"`
        Skills      []string `yaml:"skills"`
    } `yaml:"discovery"`
}

type MCPServer struct {
    Transport string            `yaml:"transport"`
    // stdio
    Command string              `yaml:"command,omitempty"`
    Args    []string            `yaml:"args,omitempty"`
    Env     map[string]string   `yaml:"env,omitempty"`
    // http
    URL     string              `yaml:"url,omitempty"`
    Headers map[string]string   `yaml:"headers,omitempty"`
}

func Load(path string) (*Config, error)
func (c *Config) Save(path string) error
```

### 4.3 `tunnel`

```go
type Tunnel struct{ /* agentsdk.Client + 配置 */ }

func New(cfg *config.Config, http http.Handler) *Tunnel
func (t *Tunnel) EnsureRegistered(ctx context.Context) error  // device flow → 写 cfg
func (t *Tunnel) Run(ctx context.Context) error               // Connect 阻塞
func (t *Tunnel) PublishCard(ctx context.Context) error
```

### 4.4 `poller`

```go
type Poller struct {
    proxyToken string
    serverURL  string
    dispatch   *dispatch.Dispatcher
}

func New(cfg *config.Config, d *dispatch.Dispatcher) *Poller
func (p *Poller) Run(ctx context.Context) error
```

行为：拉到 task → `PUT running` → `dispatch.Run(task)`（阻塞到 done 或 err）→ `PUT completed{output, ...}` 或 `PUT failed{failure_reason}`。空闲 5 分钟后退到 30s。

### 4.5 `dispatch`

```go
type Dispatcher struct {
    routes  map[string]executor.Executor   // "mcp" → MCPExecutor; "" 默认 = ClaudeExecutor
    journal *journal.Journal
    store   *store.Store
}

func New(routes map[string]executor.Executor, j *journal.Journal, s *store.Store) *Dispatcher
func (d *Dispatcher) Run(ctx context.Context, t Task) (Result, error)
```

逻辑：

1. `store.Insert(t)`
2. 选 executor（`routes[t.Skill]`，缺省走 default）
3. `r, err := executor.Run(ctx, t, store.ChunkSink(t.ID))`
4. 若 err → `store.Fail(t, err)` → return
5. `store.Complete(t, r.Summary)`
6. 若 `r.CapabilityChange != ""` → `journal.Record(ctx, t, r)`（异步、不阻塞 poller 上报）
7. return `r`

### 4.6 `executor`

```go
package executor

type Task struct {
    ID            string
    Skill         string
    Prompt        string
    SystemContext string
    TimeoutSec    int
}

type Result struct {
    Summary          string  // → agentserver task output
    CapabilityChange string  // 空表示无变化
}

type ChunkSink interface {
    Write(eventType, data string)  // chunk / capability
    Close()
}

type Executor interface {
    Run(ctx context.Context, t Task, sink ChunkSink) (Result, error)
}
```

#### ClaudeExecutor

- `exec.CommandContext(ctx, cfg.Claude.Bin, "--print", "--output-format=stream-json", "--append-system-prompt", epilog, ...cfg.Claude.Args)`
- stdin 写 `t.Prompt`（前缀加 `t.SystemContext` 如非空）
- 逐行解析 stream-json，每条 `assistant` content delta → `sink.Write("chunk", delta)`
- 收尾解析最后 assistant message：用 `=== CAPABILITY ===` 切分 → `Result.Summary`、`Result.CapabilityChange`（值 `NO_CAPABILITY_CHANGE` 归零为 `""`）
- 若 `Result.CapabilityChange != ""`，结束前向 sink 发一条 `sink.Write("capability", change)`，让 SSE 订阅者拿到摘要后再收 `done`
- 进程超时 = `t.TimeoutSec` 秒（无则默认 300）

#### MCPExecutor

- 启动时按 config 拉起 stdio server / 建 http client，按需懒加载并复用
- 解析 `t.Prompt` 为 `{server, tool, args}` JSON
- 调 `client.CallTool(server, tool, args)` → 拿 `{result, capability_changed?, change_hint?}`
- `Result.Summary = stringifyResult(result)`
- `Result.CapabilityChange = ""` 若 `capability_changed != true`，否则 `change_hint`（缺则填 `"unspecified"`）
- 中间过程没有 chunk 流，sink 直接 `Close()`

### 4.7 `journal`

```go
type Journal struct {
    dir         string             // salve_agent/journal/
    claudeBin   string
    mu          sync.Mutex         // 串行化合并
}

func New(dir, claudeBin string) (*Journal, error)
func (j *Journal) Record(ctx context.Context, t Task, r Result) error
```

`Record` 流程：

1. 加锁（保证 CURRENT_STATE.md 单作者）
2. `current := os.ReadFile(CURRENT_STATE.md)`（不存在按空字符串）
3. 拼合并提示给 claude（`exec.CommandContext(... "claude", "--print", ...)`，默认 text 输出，不加 `--output-format`）：
   > 下面是 salve_agent 的当前能力状态文档（CURRENT_STATE.md）：
   > <current>
   >
   > 刚刚执行了任务 `{id}`（skill={skill}），它对 agent 自身能力的影响是：
   > <capability_change>
   >
   > 请输出更新后的 CURRENT_STATE.md 全文。结构：用 H2 分组（如 `## Tools`、`## MCP Servers`、`## Mounted Resources`、`## Credentials`）。只动确实受影响的小节。要简洁。
4. 把 claude 输出原子写回 `CURRENT_STATE.md`（先写 `.tmp` 再 rename）
5. 追加一行到 `history.md`：`| 2026-04-27T15:32:18Z | task_abc | mcp | 已挂载 /data |`

异常：合并 claude 调用失败 → 不更新 CURRENT_STATE.md，但仍 append 一条 `history.md` 标 `[merge failed: ...]`，保留原始 `change_hint`。

### 4.8 `store`

```go
type Store struct{ db *sql.DB; subs map[string][]chan Event; mu sync.Mutex }

func Open(path string) (*Store, error)
func (s *Store) Insert(t Task) error
func (s *Store) Complete(taskID, output string) error
func (s *Store) Fail(taskID, reason string) error
func (s *Store) ChunkSink(taskID string) executor.ChunkSink
func (s *Store) Subscribe(taskID string) (<-chan Event, func())
func (s *Store) Recover(ctx context.Context, p PollerLike) error
func (s *Store) ListTasks(limit, offset int) ([]TaskRow, error)
func (s *Store) GetTaskWithChunks(id string) (TaskRow, []Chunk, error)
```

Schema：

```sql
CREATE TABLE tasks (
  id           TEXT PRIMARY KEY,
  skill        TEXT NOT NULL,
  prompt       TEXT NOT NULL,
  status       TEXT NOT NULL,         -- assigned|running|completed|failed
  output       TEXT,
  error        TEXT,
  created_at   TEXT NOT NULL,
  started_at   TEXT,
  finished_at  TEXT
);
CREATE TABLE task_chunks (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id     TEXT NOT NULL REFERENCES tasks(id),
  ts          TEXT NOT NULL,
  type        TEXT NOT NULL,          -- chunk|capability
  data        TEXT NOT NULL
);
CREATE INDEX idx_chunks_task ON task_chunks(task_id, id);

CREATE TABLE pending_acks (
  task_id      TEXT PRIMARY KEY REFERENCES tasks(id),
  status       TEXT NOT NULL,         -- completed|failed
  enqueued_at  TEXT NOT NULL
);
```

驱动：`modernc.org/sqlite`（纯 Go，无 cgo）。

### 4.9 `webui`

```go
func NewHandler(s *store.Store, j *journal.Journal, cfg *config.Config) http.Handler
```

路由：

| 路径 | 行为 |
|---|---|
| `GET /` | 仪表盘 HTML（最近 20 任务 + CURRENT_STATE.md 渲染 + 健康）|
| `GET /tasks` | `?limit&offset` JSON 列表 |
| `GET /tasks/{id}` | JSON 任务 + chunks |
| `GET /tasks/{id}/stream` | SSE：先回放历史 chunks，再订阅活跃 channel；已结束任务发完 `done` 关闭 |
| `GET /state` | `text/markdown` 直出 CURRENT_STATE.md |
| `GET /healthz` | `{"ok": true, "current_task": "...", "uptime_s": 123}` |

模板用 `text/template`；CURRENT_STATE.md 在仪表盘里用 `<pre>` 显示原文（YAGNI，不引入 markdown 渲染库）。

### 4.10 `cmd/salve-agent/main.go` 启动顺序

```
1. config.Load("config.yaml")
2. store.Open("data.db") + Recover()
3. journal.New("journal/")
4. exec := map[string]Executor{ "mcp": NewMCPExecutor(cfg.MCPServers), "": NewClaudeExecutor(cfg.Claude) }
5. dispatcher.New(exec, journal, store)
6. webuiHandler := webui.NewHandler(store, journal, cfg)
7. tunnel.New(cfg, webuiHandler)
8. tunnel.EnsureRegistered() → 必要时跑 device flow，回写 config.yaml
9. tunnel.PublishCard()
10. errgroup:
      go tunnel.Run()
      go poller.New(cfg, dispatcher).Run()
11. 等 SIGINT → ctx cancel → 等 errgroup → store.Close
```

## 5. 错误处理

### 5.1 启动期错误（致命，进程退出非 0）

| 触发 | 处理 |
|---|---|
| `config.yaml` 不存在 / 解析失败 | 打印路径和具体行号错误 → exit 1 |
| `config.server.url` 缺失 | 打印 `must set server.url` → exit 1 |
| `data.db` 打不开 / schema migrate 失败 | 打印 SQLite 错 → exit 1 |
| `journal/` 目录创建失败（权限） | 打印路径 → exit 1 |
| MCP server 配置存在但语法错 | 打印是哪个 server，标记 disabled，启动继续 |

### 5.2 注册 / Device Flow

| 触发 | 处理 |
|---|---|
| `POST /api/oauth2/device/auth` 网络/5xx | 指数退避重试 1s→60s，无上限 |
| 用户 30 分钟没批准（`expired_token`） | 重新走 device flow，重新打印新链接 |
| `access_denied` | 打印 → exit 1 |
| `POST /api/agent/register` 401 | 重跑 device flow 一次；再 401 → exit 1 |
| 已有凭证的 register 被拒 | 清空 `cfg.Credentials`、重跑 device flow + register；接受新 ShortID 写回 |
| `PublishCard` 失败 | 记 warn，**不阻塞启动** |

### 5.3 Tunnel

SDK 自带指数退避重连（1s→60s，30s 稳定后重置）；不再自研。

| 触发 | 处理 |
|---|---|
| 心跳失败 / 断流 | SDK 自动重连，记 info 日志 |
| 重连用 `tunnel_token` 401 | 同 5.2 末行：清凭证、重注册 |
| `tunnel.Run()` 退出（context done 或不可恢复） | errgroup 触发 cancel，整体退出 |

### 5.4 Poller / Task Lifecycle

| 触发 | 处理 |
|---|---|
| `GET /tasks/poll` 网络错 / 5xx | 退避到 30s 重试 |
| `GET /tasks/poll` 401 | 同 5.2 凭证失效流程 |
| `PUT running` 失败 | 记 warn，**继续执行任务**（最终 PUT completed/failed 会追平） |
| `PUT completed` / `PUT failed` 失败 | 退避重试 1s/2s/4s/8s/16s（最多 5 次）；仍失败 → 落 `pending_acks`，启动恢复时再追平 |
| Poller 自身 panic | recover → 记 error → 5s 后重起轮询 |

### 5.5 Executor

| 触发 | 处理 |
|---|---|
| `t.TimeoutSec` 到点 | `ctx.Cancel` 杀子进程 / MCP 请求 → `error = "timeout"` → failed |
| ClaudeExecutor: `claude` 二进制找不到 | 立刻返回 err `"claude binary not found at <path>"` → failed |
| ClaudeExecutor: stream-json 解析失败 | 已收 chunks 当 summary，CapabilityChange="" → 仍标 completed；记 warn |
| ClaudeExecutor: 子进程 exit ≠ 0 | failed，`reason = "claude exit N: <stderr 末 4KB>"` |
| ClaudeExecutor: 找不到 `=== CAPABILITY ===` 分隔 | 整个末消息当 summary，CapabilityChange="" |
| MCPExecutor: prompt 不是合法 JSON | failed，`reason = "mcp prompt must be JSON: <err>"` |
| MCPExecutor: prompt JSON 缺 server / tool | failed，`reason = "missing server or tool"` |
| MCPExecutor: server 不在 config | failed，`reason = "unknown mcp server: X"` |
| MCPExecutor: stdio server 进程崩溃 | 杀残留 → 下次任务按需重启；本次 failed |
| MCPExecutor: tool 调用返回 error | failed，`reason = tool error message` |
| MCPExecutor: 响应 JSON 缺 result | failed，`reason = "malformed mcp response"` |
| Executor panic | dispatcher recover → failed，`reason = "executor panic: <stack>"` |

### 5.6 Journal 合并

| 触发 | 处理 |
|---|---|
| 任务 failed | **不调 journal** |
| `CapabilityChange == ""` | 跳过合并；**也跳过 history.md 行** |
| 合并 claude 调用失败 / 超时 | 不更新 CURRENT_STATE.md；history.md 仍 append 一行 `[merge failed: <reason>]` 保留原始 `change_hint` |
| Journal 写文件失败（磁盘满 / 权限） | 记 error，**不影响 task 上报** |
| 合并并发（防御性） | `sync.Mutex` 阻塞排队 |

### 5.7 Store

| 触发 | 处理 |
|---|---|
| SQLite write 失败（磁盘满） | err 上抛；dispatcher 试图标 failed 也失败 → 进程 fatal exit |
| 启动 Recover 把 in-flight 标 failed | 同时入队 `pending_acks` |
| SSE 订阅者断开 | drain channel；下次 chunk 写入用 `select default` 不阻塞 |

### 5.8 WebUI

| 触发 | 处理 |
|---|---|
| 路径找不到 | 404 JSON `{"error":"not found"}` |
| `/tasks/{id}` 不存在 | 404 |
| `/tasks/{id}/stream` 任务已完成 | 一次性回放 chunks → `event: done` → 关闭 |
| 模板渲染失败 | 500，记 error |
| Panic | `http.Server` 自带 recover；500 |

### 5.9 关停（SIGINT / SIGTERM）

```
1. ctx cancel
2. poller 拒绝再拉 task；当前 in-flight task 让它跑完（最多等 30s）
3. tunnel.Run 收到 cancel 自然退出
4. webui http.Server.Shutdown(5s timeout)
5. journal 等待 mu 解锁
6. store.Close (SQLite checkpoint)
7. 进程 exit 0
```

30s 没让出 → exit 1，下次启动靠 Recover 收尾。

## 6. 测试策略

### 6.1 单元测试

每个内部包跑 `go test`，目标 ≥ 70% 行覆盖。重点：

| 包 | 关键测试点 |
|---|---|
| `config` | YAML 解析、缺字段报错、`Save` 后 `Load` 等价 |
| `dispatch` | skill 路由表查找；空 skill → default；executor 返 err → store.Fail；CapabilityChange="" 时 journal 不被调 |
| `executor/claude` | stream-json 行解析（含异常行）；`=== CAPABILITY ===` 切分；`NO_CAPABILITY_CHANGE` 归零；超时 ctx 杀进程；exit≠0 提取 stderr |
| `executor/mcp` | prompt JSON 解析失败；缺 server/tool；响应缺 result；`capability_changed` 缺/false/true 三分支；stdio 崩溃后下次复活 |
| `journal` | 首次 CURRENT_STATE.md 不存在时建空；mu 互斥；merge 失败仍写 history.md |
| `store` | 状态机非法转换被拒；Recover 把 running 改 failed 并入队 pending_acks；SSE 订阅多客户端 fanout；订阅者断开不阻塞 writer |
| `webui` | `/tasks/{id}/stream` 未结束 → 实时；已结束 → 历史回放后 done；404 行为 |

### 6.2 fake / mock 边界

- **`claude` 子进程**：用 `testdata/fake-claude`（小脚本/二进制）按 `--print --output-format=stream-json` 输出预录 JSONL。覆盖：正常输出、带 capability、不带 capability、超时、exit 1。
- **MCP server**：进程内 fake stdio + fake http（`httptest.NewServer`），实现 echo 与 raise-capability 两种 tool。
- **agentserver**：`httptest.NewServer` 模拟 `/api/agent/tasks/poll`、`/api/agent/tasks/{id}/status`、device flow、register。
- **SQLite**：每个测试用 `t.TempDir()` 下一份 db 或 `:memory:`。

不滥用 mock：executor 接口、Store 接口的实现都用真的，只 fake 外部进程 / HTTP。

### 6.3 契约测试

`tests/contract/` 用 `httptest.Server` 模拟 agentserver 期望的请求，覆盖：

- Device flow 字段格式与 `Content-Type`
- Register body 与 Authorization Bearer
- Heartbeat 间隔、最小 hostname/os 字段
- Task poll 请求头与 5s/30s 退避
- Status update 路径与必填字段
- Discovery card schema

任一项被服务端协议变更破坏 → 本地立即挂。

### 6.4 端到端

`scripts/e2e.sh`（不进 CI，发布前手动跑）：

1. `docker compose up agentserver`（如有镜像）
2. salve_agent 用临时 config 启动，跑 device flow
3. 经 `code-{shortID}.{baseDomain}` 触发：
   - `skill="chat"` → 等 PUT completed
   - `skill="mcp", prompt='{"server":"echo","tool":"echo","args":{...}}'` → 等 PUT completed
   - 故意失败 → 验证 PUT failed
4. 验证副作用：data.db 三条任务、CURRENT_STATE.md 被更新、history.md 三条新行、SSE 端点拿到 chunks。

### 6.5 真 claude / 真 MCP 冒烟测试

`tests/smoke/`，用 `-tags=smoke` 隔离，**不在普通 CI 跑**（需要本机真 `claude` 二进制 + `ANTHROPIC_API_KEY` + 真 MCP server，慢且要花 token），但每次发版前必须手动过一遍。

两个最小用例：

1. **真 claude 路径** — 起 ClaudeExecutor，喂任务 `prompt="回答 1+1=? 一个数字"`：
   - 断言 `Result.Summary` 含字符 `"2"`
   - 断言 `Result.CapabilityChange == ""`（这种问答不该改能力）
   - 断言 SSE sink 至少收到一条 `chunk`
   - 断言子进程 exit 0，30s 内完成

2. **真 MCP 路径** — 起 MCPExecutor，连一个最小本地 stdio MCP server（用 `npx @modelcontextprotocol/server-everything` 或自写一个 5 行的 echo server），喂 `skill="mcp", prompt='{"server":"smoke","tool":"echo","args":{"text":"hello"}}'`：
   - 断言 `Result.Summary` 含 `"hello"`
   - 断言 stdio server 进程在测试结束后被清理（无僵尸）
   - 断言 `capability_changed=false` 时 journal 不被调

跳过条件：`os.Getenv("ANTHROPIC_API_KEY") == ""` 或 `which claude` 失败 → `t.Skip()` 并打印明确原因。

意图：不测 claude 的回答质量本身（那是模型层），但要保证「我们的 stream-json 解析、子进程管理、MCP transport 接线」对真二进制是兼容的——这是单元测里 fake 永远盖不住的。

### 6.6 不测的东西

- `agentsdk` 自身、`modernc.org/sqlite` 的 SQL 行为
- claude 的回答质量、MCP server 的业务正确性
- HTML 模板视觉

### 6.7 CI

```
go vet ./...
go test ./... -race -count=1 -coverprofile=cover.out
go test -tags=contract ./tests/contract/...
# 不跑：go test -tags=smoke ./tests/smoke/...   ← 本地手动
```

`-race` 必开（journal mutex、SSE pubsub 是并发热点）。

## 7. 待办（实现阶段拆解占位）

由 writing-plans skill 负责进一步拆解；本节仅占位列出粗粒度里程碑：

1. 项目骨架 + config + 启动装配
2. tunnel 集成 + device flow + register + discovery card
3. store + SQLite migration + Recover
4. ClaudeExecutor + 假 claude 测试
5. MCPExecutor + stdio + http 双模式
6. dispatcher + poller + 串行任务流转
7. journal + 合并提示 + 原子写
8. webui + 仪表盘 + SSE
9. 关停/恢复闭环
10. 契约测试 + 端到端脚本
