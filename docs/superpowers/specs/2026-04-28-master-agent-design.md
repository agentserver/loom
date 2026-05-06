# master_agent 设计文档

- **日期**：2026-04-28
- **状态**：Draft（待实现）
- **语言**：Go（与 salve_agent 同模块）
- **协议参考**：`agentserver/docs/developer/protocol.md`、agentsdk 的 `DelegateTask` / `WaitForTask` / `DiscoverAgents` 方法
- **关联 spec**：`docs/superpowers/specs/2026-04-27-salve-agent-design.md`

## 0. 问题陈述

master_agent 是接入 agentserver 的另一个 custom agent，定位「纯调度」：

1. 通过同一套协议从 agentserver 拉任务、回报状态；
2. 不亲自执行任务本身，但允许调 claude 作为 planner / router / reducer 做决策；
3. 把工作通过 `SDK.DelegateTask` 派给 workspace 内其它 agent（如 salve_agent），并等待结果；
4. 支持两种顶层 skill：
   - `route` — 1→1，把整条任务转给一个最合适的下属
   - `fanout` — 1→N（DAG），把任务拆成有依赖关系的子任务图，并发执行下游可执行节点，最后把所有节点输出合成一个总输出
5. 与 salve_agent 同 Go 模块、共享 `internal/{config,store,webui,tunnel,poller}`，新增 `internal/{orchestrator,planner}`，新增 `cmd/master-agent` 二进制。

不在范围：跨 workspace 调度、agent 间消息推送（SDK.SendMessage）、人工编辑 DAG 工具。

## 1. 关键决策一览

| 维度 | 决策 |
|---|---|
| 角色 | 纯调度（不执行任务本身），允许调 claude 做决策 |
| Skill | `route`（1→1）、`fanout`（1→N，DAG） |
| 拆分引擎 | claude（LLM planner） |
| 路由引擎 | claude（LLM router） |
| 聚合引擎 | claude（LLM reducer） |
| 1→N 形状 | DAG（节点带 `depends_on`，prompt 用 `{{nX.output}}` 引用上游）|
| 失败策略 | 可配置：`best_effort`（默认）/ `all_or_nothing`，按 skill 覆盖 |
| 下属来源 | 实时 `SDK.DiscoverAgents`，过滤掉自己 |
| 持久化 | 复用 salve 的 SQLite store，加 `sub_tasks` 表 |
| Web UI | 复用 salve 的 webui，新增 children 路由 + SSE 子任务事件 |
| 并发 | 顶层任务串行；fanout 内部 DAG 节点并发（`max_concurrency` 受限） |
| Module 布局 | 同 salve_agent 模块，新增 cmd/binary，共享 `internal/` |
| Journal | master 不维护 CURRENT_STATE.md（无能力变化概念） |

## 2. 架构总览

```
                ┌────────────────────── master_agent ──────────────────────┐
                │                                                          │
agentserver ◄───┤ tunnel    : agentsdk.Connect (复用 salve internal/tunnel) │
                │ poller    : 5s 轮询 /api/agent/tasks/poll (串行)         │
                │             ↓ skill 路由                                 │
                │ orchestrator                                             │
                │  ├ "route"  → planner.Route(prompt, agents)              │
                │  │             → SDK.DelegateTask(target_id, prompt)     │
                │  │             → SDK.WaitForTask(child_id)               │
                │  │             → return child.output                     │
                │  │                                                       │
                │  └ "fanout" → planner.Plan(prompt, agents) (DAG)         │
                │                → 拓扑调度: 并发 DelegateTask + WaitForTask│
                │                → 模板替换 {{nX.output}} → 下游 prompt    │
                │                → planner.Reduce(prompt, results)         │
                │                → return reduced output                   │
                │             ↓                                            │
                │ store     : SQLite (复用 salve internal/store) +         │
                │             新增 sub_tasks 表                            │
                │             ↓                                            │
                │ webui     : 复用 salve internal/webui，新增              │
                │             /tasks/{id}/children 路由 + 子任务事件       │
                │                                                          │
                │ 文件:  data.db / config.yaml                             │
                │        (master 不写 journal)                             │
                └──────────────────────────────────────────────────────────┘
                                       ↑
                       agentserver workspace 内其它 agent 经                
                       SDK.DelegateTask 收到子任务，跑完回写 status
```

### 关键不变量

1. master 串行处理顶层任务（与 salve 一致），但 fanout 内部 DAG 节点**并发**派发与等待，受 `cfg.fanout.max_concurrency` 限流。
2. master **绝不出现在自己的可调度列表**：planner 调用前从 `agents` 列表过滤掉 `cfg.Credentials.SandboxID`。
3. 防递归：`SDK.DelegateTask` 已经传 `delegation_chain`，平台层有最大深度保护；master 不重复造。
4. 不维护 `journal`：master 不修改自己的 capability。
5. master 与 salve 复用 `internal/` 包，但**互不 import 对方的专属包**：master 不 import `executor/journal/dispatch`，salve 不 import `orchestrator/planner`。

## 3. 数据流和任务生命周期

### 3.1 `route` 路径（1→1）

```
agentserver         poller        orchestrator       planner          SDK            store
    │                 │                │                │              │              │
    │── task ───────► │                │                │              │              │
    │                 │── PUT running ►                 │              │              │
    │                 │── dispatch(t) ►│                │              │              │
    │                 │                │── insert(t) ───────────────────────────────► │
    │                 │                │── DiscoverAgents ─────────────►              │
    │                 │                │◄── [AgentCard] (filter self) ─               │
    │                 │                │── Route(prompt, agents) ─────►│ claude --print
    │                 │                │◄── target_id ─────────────────│              │
    │                 │                │── insertSubTask(t.id, target) ─────────────► │
    │                 │                │── DelegateTask({target,prompt})───►          │
    │                 │                │◄── child_task_id ─────────────                │
    │                 │                │── WaitForTask(child_id, 5s)──►│              │
    │                 │                │   ... 阻塞直到 completed/failed │              │
    │                 │                │◄── TaskInfo{output, status} ──                │
    │                 │                │── updateSubTask(status, out) ──────────────► │
    │                 │                │── Complete(t, child.output)────────────────► │
    │                 │◄── done(out) ──┤                │              │              │
    │                 │── PUT completed▶                │              │              │
    │◄── done ────────┤                                                               │
```

**Route 透传规则**：master 不重写 prompt，原样转给被选中的下属 agent。Route planner 只决定 target_id。

**Route planner 提示词（节选）**：
> 你是一个调度器。下面是要执行的任务和当前可用的 agent 列表（含 display_name、description、card.skills）。返回一行 JSON `{"target_id": "..."}`，选最合适的 agent。如果没有任何合适候选，返回 `{"target_id": ""}`。

### 3.2 `fanout` 路径（1→N，DAG）

```
poller → orchestrator (skill="fanout"):
    1. agents := SDK.DiscoverAgents() ∖ {self}
    2. dag := planner.Plan(prompt, agents)
       // dag = []Node{ID, TargetID, Prompt, DependsOn []string, TimeoutSec?}
    3. validate(dag):
         - 节点 ID 唯一
         - 所有 DependsOn 都指向已声明的 ID
         - 无环（拓扑排序，发现回边 → fail("cycle: a → b → a"))
         - 至少 1 个节点；不超过 100 个节点
       校验失败 → parent failed("invalid plan: <err>")
    4. store.InsertSubTasks(parent, dag)  // 整张图落库，含 depends_on
    5. 拓扑调度循环：
         outputs := map[NodeID]string{}
         pending := all nodes with len(DependsOn) == 0
         running := worker pool (size = cfg.fanout.max_concurrency, 默认 4)
         while pending or running not empty:
           dispatch all pending nodes that have free worker:
             prompt' := substitute({{n.output}} -> outputs[n] for n in node.DependsOn)
             id, err := SDK.DelegateTask({target, prompt'})
             如果 err: 标 node failed; 见步骤 6
           等任一 running 节点完成:
             outputs[node] := info.Output  (失败则不写入)
             store.UpdateSubTask(node, status, output)
             把所有 DependsOn ⊆ done(success) 的下游节点加入 pending
    6. failure_policy := cfg.fanout.failure_policy[parent.Skill] (默认 best_effort)
       任一节点失败时：
         - all_or_nothing:
             立即 cancel 所有 in-flight DelegateTask（context 取消，让 SDK 杀掉）
             把还未派发的下游节点直接标 skipped("upstream <X> failed")
             parent failed("node <X> failed: <reason>")
             不调 reducer
         - best_effort:
             失败节点的下游节点递归标 skipped("upstream <X> failed")
             已 ready 的旁支节点继续派发
             所有节点跑完（含 skipped）后才进 reducer
    7. summary := planner.Reduce(parent.prompt, dag, outputs+statuses)
    8. store.Complete(parent, summary)
```

**Plan JSON 形状（planner 输出）**：

```json
[
  {"id":"n1","target_id":"agent-a","prompt":"调研 X 的现状"},
  {"id":"n2","target_id":"agent-b","prompt":"基于以下调研写大纲：\n{{n1.output}}","depends_on":["n1"]},
  {"id":"n3","target_id":"agent-c","prompt":"基于以下调研列举风险：\n{{n1.output}}","depends_on":["n1"]},
  {"id":"n4","target_id":"agent-d","prompt":"合并大纲与风险：\n大纲：{{n2.output}}\n风险：{{n3.output}}","depends_on":["n2","n3"]}
]
```

**Plan planner 提示词（节选）**：

> 你是任务拆分器。把下面的总任务拆成一张 DAG（1~20 个节点）。返回 JSON 数组，每项字段：
> - `id`：节点唯一名（短字符串，如 `n1`）
> - `target_id`：从下面可用 agents 中挑一个最合适的（用其 `agent_id` 字段值）
> - `prompt`：该节点的任务文本；可以用 `{{X.output}}` 引用某个上游节点 X 的输出，X 必须出现在 `depends_on` 里
> - `depends_on`：上游节点 id 列表，可空（表示根节点）
>
> 必须无环，至少有一个根节点（depends_on 为空），最好有清晰的汇流结构。

**Prompt 模板替换规则**：

- 唯一占位语法：`{{<node_id>.output}}`
- 在 master 派发节点 X 之前做替换：`render(X.Prompt, outputs)`
- 引用了未完成的节点 → 编程错误，节点 failed
- 引用了 skipped/failed 节点 → 节点 skipped
- `{{` 字面量目前不支持转义（YAGNI；以后真撞了再加 `\{{`）

### 3.3 失败路径（汇总）

| 阶段 | 行为 |
|---|---|
| planner.Route 返回空 | parent failed("no candidate") |
| planner.Plan 返回空 / 解析失败 / 校验失败（含环、悬空依赖、ID 重复、>100 节点） | parent failed("invalid plan: <err>") |
| 单节点 DelegateTask 失败 / WaitForTask 失败 | 节点标 failed；按 policy（all_or_nothing 立刻终止；best_effort 递归 skipped 下游 + 继续旁支） |
| 模板替换引用了 skipped 上游 | 该节点直接标 skipped（不派发）|
| best_effort 全部 skipped/failed | 仍调 reducer（让它说全部失败）；parent completed |
| reducer 调用失败 | parent failed("reduce failed: ...")；sub_tasks 表保留追踪 |

### 3.4 SSE 事件

`/tasks/{parent_id}/stream` 的事件类型新增：

- `subtask_dispatched` `{node_id, target_id}`
- `subtask_done` `{node_id, status, output_len}`（不发完整 output 防爆量；详情走 `/tasks/{id}` REST）
- `subtask_skipped` `{node_id, reason}`
- `done`

route 路径产生 1 个 `subtask_dispatched` + 1 个 `subtask_done` + 1 个 `done`。

### 3.5 重启恢复

- 启动时同 salve：扫 SQLite，把 `running` parent 标 failed("agent restarted")，入队 `pending_acks`。
- **额外**：扫 `sub_tasks` 中 parent 已 failed 的所有 in-flight 子任务行（status ∈ {pending, assigned}），标 `cancelled`。真子任务在远端 agent 上继续跑完，结果会丢——可接受，因为 parent 已 failed。

## 4. 组件细节

### 4.1 目录与新增文件（diff 自 salve_agent 当前布局）

```
salve_agent/                              ← 模块根（名字保留）
├── cmd/
│   ├── salve-agent/main.go               （既有）
│   └── master-agent/main.go              ★ 新
├── internal/
│   ├── config/
│   │   └── config.go                     △ 加 master 字段
│   ├── store/
│   │   ├── schema.sql                    △ 加 sub_tasks 表
│   │   └── store.go                      △ 加 sub-task CRUD
│   ├── webui/
│   │   └── server.go                     △ 加 children 路由 + 子任务 SSE
│   ├── tunnel/                           △ 加 SDKClient() getter
│   ├── poller/                           △ 把 *dispatch.Dispatcher 抽成接口
│   ├── orchestrator/                     ★ 新（master 专属）
│   │   ├── orchestrator.go
│   │   ├── route.go
│   │   ├── fanout.go
│   │   ├── dag.go
│   │   └── orchestrator_test.go
│   └── planner/                          ★ 新（master 专属）
│       ├── planner.go
│       └── planner_test.go
└── master_agent/                         ★ 新（仅文档/脚本，不在 Go 包路径）
    ├── README.md
    ├── config.example.yaml
    └── scripts/e2e.sh
```

`internal/{executor,journal,dispatch}` 为 salve 专属，master 不 import。

### 4.2 `config` 扩展

```go
type Config struct {
    // … 既有 Server/Credentials/Claude/MCPServers/Discovery …
    Planner Planner `yaml:"planner"`  // master 用
    Fanout  Fanout  `yaml:"fanout"`   // master 用
}

type Planner struct {
    Bin        string   `yaml:"bin"`        // 默认 = Claude.Bin
    TimeoutSec int      `yaml:"timeout_sec"` // 单次 planner/reducer 超时，默认 60
    ExtraArgs  []string `yaml:"extra_args"`
}

type Fanout struct {
    MaxConcurrency  int                 `yaml:"max_concurrency"`   // 默认 4
    DefaultPolicy   string              `yaml:"default_policy"`    // best_effort | all_or_nothing
    PolicyBySkill   map[string]string   `yaml:"policy_by_skill"`   // 覆盖默认
    SubTaskDefaults SubTaskDefaults     `yaml:"subtask_defaults"`
}

type SubTaskDefaults struct {
    TimeoutSec   int     `yaml:"timeout_sec"`    // 默认 600
    MaxBudgetUSD float64 `yaml:"max_budget_usd"` // 默认 0（不限）
}
```

`master_agent/config.example.yaml`：

```yaml
server: { url: https://agent.example.com, name: master-agent }
discovery:
  display_name: master_agent
  description: Orchestrator (route + fanout)
  skills: [route, fanout]
claude: { bin: claude }
planner:
  bin: claude
  timeout_sec: 60
fanout:
  max_concurrency: 4
  default_policy: best_effort
  policy_by_skill:
    fanout_strict: all_or_nothing
  subtask_defaults: { timeout_sec: 600 }
```

### 4.3 `store` 扩展

**Schema diff**：

```sql
CREATE TABLE IF NOT EXISTS sub_tasks (
    parent_id     TEXT NOT NULL REFERENCES tasks(id),
    node_id       TEXT NOT NULL,           -- 在 parent 内唯一（如 "n1"）
    target_id     TEXT NOT NULL,
    child_task_id TEXT,                    -- SDK.DelegateTask 返回的 task_id；NULL 表示还没派
    prompt        TEXT NOT NULL,           -- 渲染后的 prompt（已替换 {{...}}）
    depends_on    TEXT NOT NULL,           -- JSON array of node_id
    status        TEXT NOT NULL,           -- pending|assigned|completed|failed|skipped|cancelled
    output        TEXT,
    error         TEXT,
    created_at    TEXT NOT NULL,
    started_at    TEXT,
    finished_at   TEXT,
    PRIMARY KEY (parent_id, node_id)
);
CREATE INDEX IF NOT EXISTS idx_subtasks_parent ON sub_tasks(parent_id);
```

**新方法**：

```go
type SubTaskRow struct {
    ParentID, NodeID, TargetID, ChildTaskID, Prompt string
    DependsOn []string
    Status, Output, Error string
    CreatedAt, StartedAt, FinishedAt string
}

func (s *Store) InsertSubTasks(parentID string, nodes []SubTaskRow) error
func (s *Store) UpdateSubTask(parentID, nodeID string, fields map[string]interface{}) error
func (s *Store) ListSubTasks(parentID string) ([]SubTaskRow, error)
```

`UpdateSubTask` 用 fields map 而不是单独方法（6 种状态转换、10 种字段组合，单独方法爆炸）。

`store.Recover()`（既有方法）被扩展：除把 `tasks` 中 in-flight 行标 failed 外，同事务里把 `sub_tasks` 中 `parent_id` 已 failed 且 `status ∈ {pending, assigned}` 的行标 `cancelled`，并把 `finished_at` 写为 now。salve 不会写 sub_tasks，所以这段对 salve 是 no-op。

### 4.4 `planner`（新）

```go
package planner

type Planner struct{ cfg config.Planner }

func New(cfg config.Planner) *Planner

// Route: 选 1 个 target_id；返回空字符串表示无候选。
func (p *Planner) Route(ctx context.Context, prompt string, agents []agentsdk.AgentCard) (string, error)

// Plan: 输出 DAG。
type Node struct {
    ID         string   `json:"id"`
    TargetID   string   `json:"target_id"`
    Prompt     string   `json:"prompt"`
    DependsOn  []string `json:"depends_on,omitempty"`
}
func (p *Planner) Plan(ctx context.Context, prompt string, agents []agentsdk.AgentCard) ([]Node, error)

// Reduce: 给定原 prompt + 节点元数据 + 每个节点的 status/output → 出最终 summary。
type SubResult struct {
    NodeID, TargetID, Prompt, Status, Output, Error string
}
func (p *Planner) Reduce(ctx context.Context, originalPrompt string, results []SubResult) (string, error)
```

实现：每个方法构造一段固定的 system + user prompt（提示词模板写在 planner.go 内的 const），起 `claude --print` 子进程（无 stream-json，纯文本输出），按 timeout 杀。返回值通过解析 claude 的输出：

- **Route**：claude 被指示只输出一行 JSON `{"target_id":"..."}`（空字符串允许）。Planner 解析这行；解析失败 → fallback 到「整个输出 trim 后当成 target_id」（容错）。
- **Plan**：claude 被指示输出 JSON 数组。Planner 严格 unmarshal；失败 → 返回 err。
- **Reduce**：claude 直接输出文字，原样返回。

### 4.5 `orchestrator`（新）

```go
package orchestrator

type Orchestrator struct {
    routes  map[string]Handler   // "route" → routeHandler; "fanout" → fanoutHandler
    store   *store.Store
    planner *planner.Planner
    sdk     SDKDelegator        // 接口，便于测试 mock
    cfg     config.Fanout
    selfID  string              // 用于过滤 DiscoverAgents
}

type Handler func(ctx context.Context, t executor.Task) (executor.Result, error)

type SDKDelegator interface {
    DiscoverAgents(ctx context.Context) ([]agentsdk.AgentCard, error)
    DelegateTask(ctx context.Context, req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error)
    WaitForTask(ctx context.Context, taskID string, pollInterval time.Duration) (*agentsdk.TaskInfo, error)
}

func New(s *store.Store, p *planner.Planner, sdk SDKDelegator, cfg config.Fanout, selfID string) *Orchestrator

// 实现 poller 期望的 dispatcher 接口
func (o *Orchestrator) Run(ctx context.Context, t executor.Task) (executor.Result, error)
```

`Run` 主循环：
1. `store.Insert(parent)` + `MarkRunning`
2. 按 `t.Skill` 选 handler；找不到 → `Fail("unknown skill")`
3. handler(ctx, t) → 返回 (Result, error)
4. err → `Fail`；ok → `Complete(t.ID, res.Summary)`
5. master 不进 journal。

`route.go`（route handler，pseudo-code）：

```go
agents := o.discoverFiltered(ctx)
target, err := o.planner.Route(ctx, t.Prompt, agents)
if target == "" { return Result{}, errors.New("no candidate") }
o.store.InsertSubTasks(t.ID, []SubTaskRow{{NodeID: "root", TargetID: target, Prompt: t.Prompt, Status: "assigned"}})
resp, _ := o.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{TargetID: target, Prompt: t.Prompt, TimeoutSeconds: t.TimeoutSec})
o.store.UpdateSubTask(t.ID, "root", map[string]interface{}{"child_task_id": resp.TaskID})
info, _ := o.sdk.WaitForTask(ctx, resp.TaskID, 5*time.Second)
o.store.UpdateSubTask(t.ID, "root", map[string]interface{}{"status": info.Status, "output": info.Output})
if info.Status != "completed" { return Result{}, fmt.Errorf("child failed: %s", info.FailureReason) }
return Result{Summary: info.Output}, nil
```

`fanout.go` + `dag.go`（fanout handler，pseudo-code）：

```go
agents := o.discoverFiltered(ctx)
plan, err := o.planner.Plan(ctx, t.Prompt, agents)
if err := dag.Validate(plan); err != nil { return Result{}, err }
o.store.InsertSubTasks(t.ID, dag.ToRows(plan))

policy := o.cfg.policyForSkill(t.Skill)
sched := dag.NewScheduler(plan, o.cfg.MaxConcurrency)
outputs := map[string]string{}
for !sched.Done() {
    for _, n := range sched.Ready() {
        prompt := dag.Render(n.Prompt, outputs)
        go runNode(ctx, n, prompt, sched, o.sdk)
    }
    finished := sched.Wait()
    for _, f := range finished {
        if f.Status == "completed" {
            outputs[f.NodeID] = f.Output
        } else if policy == "all_or_nothing" {
            cancelAll(); return Result{}, fmt.Errorf("node %s failed: %s", f.NodeID, f.Error)
        } else {
            sched.MarkDownstreamSkipped(f.NodeID)
        }
        o.store.UpdateSubTask(t.ID, f.NodeID, /* status, output, error */)
    }
}
results := buildSubResults(plan, outputs, statuses)
summary, _ := o.planner.Reduce(ctx, t.Prompt, results)
return Result{Summary: summary}, nil
```

`dag.go` 提供：

```go
func Validate(nodes []planner.Node) error
func Render(template string, outputs map[string]string) (string, error)
type Scheduler struct{ ... }
func NewScheduler(nodes []planner.Node, maxConc int) *Scheduler
func (s *Scheduler) Ready() []planner.Node
func (s *Scheduler) Report(nodeID string, info *agentsdk.TaskInfo, err error)
func (s *Scheduler) Wait() []FinishedNode
func (s *Scheduler) MarkDownstreamSkipped(failedID string)
func (s *Scheduler) Done() bool
```

### 4.6 `webui` 扩展

| 路径 | 行为 |
|---|---|
| `GET /tasks/{id}/children` | JSON `[]SubTaskRow`（按 created_at）|
| `GET /tasks/{id}` | JSON 任务详情**附带** `children: [...]`（如有）|
| `GET /tasks/{id}/stream` | SSE 事件类型新增：`subtask_dispatched` / `subtask_done` / `subtask_skipped`（事件由 orchestrator 通过 `store.Sink` 写入，复用既有 pubsub）|
| `GET /` 仪表盘 | 任务行右边加「Sub: X/Y completed」摘要 |

无新增 handler 文件——都在 server.go 增改。

### 4.7 `poller` 抽象

`poller.New` 第二个参数从 `*dispatch.Dispatcher` 改为接口：

```go
type Dispatcher interface {
    Run(ctx context.Context, t executor.Task) (executor.Result, error)
}
```

`dispatch.Dispatcher`（salve）和 `orchestrator.Orchestrator`（master）都满足。

### 4.8 `tunnel` 小改

加 getter：

```go
func (t *Tunnel) SDKClient() *agentsdk.Client
```

返回内部 `*agentsdk.Client`，以便 master 用它调 `DelegateTask` / `WaitForTask` / `DiscoverAgents`。salve 不调用，但保留无害。

### 4.9 `cmd/master-agent/main.go` 启动顺序

```
1. config.Load("config.yaml")
2. store.Open("data.db") + Recover()
3. tunnel.New(cfg, cfgPath, webui) + EnsureRegistered + PublishCard
4. sdk := tunnel.SDKClient()
5. p := planner.New(cfg.Planner)
6. orch := orchestrator.New(store, p, sdk, cfg.Fanout, cfg.Credentials.SandboxID)
7. ui := webui.NewHandler(store, "", cfg)   // journalDir="" — master 不写 journal
8. errgroup:
     go tunnel.Run()
     go poller.New(pollerCfg, orch, store).Run()
9. SIGINT → cancel → 等到 graceful 30s 再硬退
```

## 5. 错误处理（master 专属补充）

salve 已有的协议层错误（4.1–4.5、4.8、4.9 of salve spec）继承不变。

### 5.1 planner 调用

| 触发 | 处理 |
|---|---|
| claude 二进制找不到 | parent failed("planner: bin not found at ...") |
| claude exit ≠ 0 | parent failed("planner: exit N: <stderr 末 4KB>") |
| 超时（cfg.Planner.TimeoutSec） | parent failed("planner: timeout") |
| Plan 输出非合法 JSON | parent failed("plan invalid: <unmarshal err>") |
| Route 输出空 / 解析失败 | 兜底：trim 整个 stdout 当 target_id；再失败 → parent failed("no candidate") |
| Reduce 调用失败 | parent failed("reduce: <err>")；sub_tasks 仍保留可追踪 |

### 5.2 DAG 校验

| 触发 | 处理 |
|---|---|
| 空数组 | parent failed("plan empty") |
| 节点 ID 重复 | parent failed("duplicate node id: X") |
| `depends_on` 指向不存在的 ID | parent failed("dangling dep: X → Y") |
| 拓扑发现回边 | parent failed("cycle: a → b → a") |
| 节点数 > 100 | parent failed("plan too large: N nodes") |
| `target_id` 不在当前 DiscoverAgents 列表 | 节点 failed("unknown target: X")；按 policy 处理 |

### 5.3 子任务派发与等待

| 触发 | 处理 |
|---|---|
| `SDK.DelegateTask` 网络错 | 该节点 failed("delegate: <err>")；按 policy |
| `SDK.WaitForTask` 远端 401 | 同 salve 凭证刷新流程；该节点临时 failed |
| `SDK.WaitForTask` 返回 status=failed | 节点 failed(child.failure_reason) |
| 节点 timeout（节点级 TimeoutSec） | ctx cancel → DelegateTask 父级 ctx 已带超时；信任 SDK 在 child 端超时 |
| 模板 `{{X.output}}` 引用 skipped/failed 节点 | 节点 skipped("upstream X skipped/failed") |
| 模板引用未声明在 depends_on 中的节点 | 节点 failed("template references undeclared dep: X") |

### 5.4 调度器并发

| 触发 | 处理 |
|---|---|
| `cfg.fanout.max_concurrency = 0` 或负数 | 启动期 fatal exit（config validate）|
| 整张图全部 skipped/failed（无任何 completed） | best_effort：仍调 reducer，让它说「全部失败」；all_or_nothing：在第一个 failed 时已 fail |
| 调度器死锁（理论上不应发生） | 1 分钟无进展 → fatal log + parent failed("scheduler stuck")；防御性 |

### 5.5 关停（master 专属）

- SIGINT → cancel parent ctx → SDK.WaitForTask 各 goroutine 立刻返回 ctx.Canceled
- 已派出去的子任务**不主动通知远端取消**（SDK 没有 `CancelTask`）；它们在远端继续跑完自然完成或超时
- 本地 `sub_tasks` 行标 `cancelled`
- parent 标 failed("agent restarted") via Recover at next boot

## 6. 测试策略（master 专属补充）

### 6.1 单元测试

| 包 | 关键测试点 |
|---|---|
| `planner` | Plan 输出 JSON 解析（含 garbage、缺字段）；Route 兜底；Reduce 透传；timeout；exit≠0 |
| `orchestrator/dag` | Validate（空、重复、悬空、环、超大）；Render（基本、缺变量、未声明依赖）；Scheduler.Ready/Wait/MarkDownstreamSkipped（含菱形 DAG、链式、单节点、扇形）|
| `orchestrator/route` | DiscoverAgents 过滤 self；Route 空 → fail；DelegateTask 失败 → fail；child failed → parent failed |
| `orchestrator/fanout` | best_effort 部分失败仍调 reducer；all_or_nothing 任一失败立即 cancel + skip 后续；模板替换 |
| `store` (sub_tasks 部分) | InsertSubTasks/Update/List；Recover 把 in-flight sub_tasks 标 cancelled |
| `webui` (children 部分) | `/tasks/{id}/children` 返回顺序；详情含 children；SSE 新事件类型 |
| `config` | master 字段默认值、`policy_by_skill` 解析 |

### 6.2 fake / mock 边界

- **claude planner/reducer**：`testdata/fake-planner.sh`，env 控制输出（`FAKE_PLANNER_MODE=route_a|plan_diamond|plan_invalid_cycle|reduce_ok|exit1|sleep`）
- **agentsdk**：抽 `SDKDelegator` 接口（DiscoverAgents/DelegateTask/WaitForTask 三个方法），orchestrator 持接口而非 `*agentsdk.Client`；测试用 in-memory fake，可控制每个 child 的 status/output/delay。SDK 真客户端在 main.go 装配时注入。

### 6.3 contract 测试

新增：

- `TestContract_DelegateTask` — orchestrator 调用 SDK 时拼出的 HTTP 请求体形状对（target_id, prompt, skill, timeout_seconds 字段就位）
- `TestContract_WaitForTask` — poll 间隔、include_output 参数、200 vs 404 行为

### 6.4 端到端

`master_agent/scripts/e2e.sh`：

1. 起一个 master + 至少 2 个 salve（不同 skill 集合）
2. 投 `skill="route"` 任务到 master，验证 master.completed.output == 某个 salve.completed.output
3. 投 `skill="fanout"` 任务到 master，验证：
   - sub_tasks 表里有 N 行
   - `/tasks/{id}/children` 返回这 N 行
   - DAG 拓扑正确（如果 prompt 引导 planner 出菱形结构）
   - SSE 流里能看到 `subtask_dispatched` / `subtask_done` 事件
   - master.completed.output 是 reducer 输出

### 6.5 不测的东西

- claude 选 agent 的「准确性」——属模型层
- planner 输出的 DAG 是否「合理」——属模型层；只测系统能消化它
- 远端 agent 的执行质量

### 6.6 CI

复用 salve 的 `.github/workflows/salve_agent.yml`（同模块跑测试就涵盖了 master）。无需新 workflow。

## 7. 待办（实现阶段拆解占位）

由 writing-plans skill 负责进一步拆解；本节仅占位列出粗粒度里程碑：

1. config 扩字段 + master defaults
2. store schema + sub_tasks CRUD + Recover 改动
3. poller 抽接口（小改 salve）
4. tunnel SDKClient() getter
5. planner 包 + 假 planner 测试
6. orchestrator/dag 包（Validate / Render / Scheduler）
7. orchestrator/route handler
8. orchestrator/fanout handler（含并发调度、policy、cancel-all）
9. webui 加 children 路由 + 子任务 SSE 事件
10. cmd/master-agent/main.go 装配
11. 契约测试（DelegateTask / WaitForTask）
12. master_agent/ 文档 + e2e 脚本
