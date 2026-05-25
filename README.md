# Loom

> 你的织机：一台本地 driver，把多台 self-host slave 提供的能力织进任务；缺线时，就在 slave 上当场纺一根。

**语言**: [简体中文](README.md) · [English](README.en.md)

Loom 是基于 [agentserver](https://github.com/agentserver/agentserver) 平台的一组自定义 agent，共享同一个 Go module 与一套内部包。整套系统的目标是：**用户在 Claude Code 或 Codex CLI 里跑一台本地 driver，用它统一指挥一组分布在不同机器、不同能力的 self-host slave**；driver 负责澄清意图、检视能力、编排任务；slave 在各自节点本地执行；所有遥测汇集到独立部署的 observer 便于回放与调试。支持**混合机队**部署：每个 driver 和 slave 独立选择后端（`--agent claude|codex`），来自不同后端的 agent 共享同一个 observer / workspace。

最与众不同的一点：**集群能力不是固定的**。当现有 slave 都满足不了某个任务时，driver 会让一台 slave 在线写、跑、验证一段 Python MCP server，然后通过 `register_mcp` 把它注册进去；下一轮编排时这套新能力就已可调用。集群因此可以从一个最小骨架开始，按用户实际任务慢慢长出真正用得到的工具，而不是先做一大套预制集成。整件事就像织布：经线、纬线来自不同 slave 的现成能力，driver 决定怎么交织成最终任务；如果某根关键的线还不存在，就当场在 slave 上纺出来再织进去。

> 命名说明：项目最初叫 `multi-agent`，现已更名为 **Loom**。Go module 路径、目录名暂时保持 `multi-agent/` 不动，等替换可以集中做一次。

## 一行命令部署

三种角色都用 release 上托管的 bootstrap 脚本起，**不需要 clone 仓库**，
适用于任何 Linux（amd64/arm64）和 Termux/Android（aarch64）。占位符
`OBSERVER_HOST` / `WS_ID` / `YOUR_API_KEY` / 角色名换成自己的值。
`--systemd` 在 slave/observer 上可选（需要 sudo；Termux 上去掉）。

```bash
# observer（控制面）—— LOOM_API_KEY 不传则自动生成并打印一次
export LOOM_WORKSPACE_ID=WS_ID LOOM_API_KEY='YOUR_API_KEY'
bash <(curl -fsSL \
  https://github.com/agentserver/loom/releases/download/v0.0.1/bootstrap-observer.sh) \
  --name obs-prod --systemd

# driver — Claude Code 版（默认）
export LOOM_OBSERVER_URL=http://OBSERVER_HOST:8090 LOOM_WORKSPACE_ID=WS_ID LOOM_API_KEY='YOUR_API_KEY'
bash <(curl -fsSL \
  https://github.com/agentserver/loom/releases/download/v0.0.1/bootstrap-driver.sh) \
  --name driver-myhost

# driver — Codex 版（写 .codex/config.toml + AGENTS.md）
export LOOM_OBSERVER_URL=http://OBSERVER_HOST:8090 LOOM_WORKSPACE_ID=WS_ID LOOM_API_KEY='YOUR_API_KEY'
bash <(curl -fsSL \
  https://github.com/agentserver/loom/releases/download/v0.0.1/bootstrap-driver.sh) \
  --name driver-myhost --agent codex

# slave — Claude Code 版（执行器，Termux 上去掉 --systemd 改前台）
export LOOM_OBSERVER_URL=http://OBSERVER_HOST:8090 LOOM_WORKSPACE_ID=WS_ID LOOM_API_KEY='YOUR_API_KEY'
bash <(curl -fsSL \
  https://github.com/agentserver/loom/releases/download/v0.0.1/bootstrap-slave.sh) \
  --name slave-myhost --systemd

# slave — Codex 版（chat skill 改用 codex exec --json 驱动，可与 Claude Code slave 混用）
export LOOM_OBSERVER_URL=http://OBSERVER_HOST:8090 LOOM_WORKSPACE_ID=WS_ID LOOM_API_KEY='YOUR_API_KEY'
bash <(curl -fsSL \
  https://github.com/agentserver/loom/releases/download/v0.0.1/bootstrap-slave.sh) \
  --name slave-myhost --agent codex --systemd
```

driver 跑完后一次性 `./driver-agent register --config ./config.yaml` 完成
agentserver device-code OAuth；Codex driver 还需提前 `codex login` 或
`export OPENAI_API_KEY=...`，并在项目目录首次运行 `codex` 以建立 trust。
slave 首启动 stderr 会打印 device-code URL，浏览器批准后凭证自动回写
`config.yaml` 并向 observer 注册。

Codex CLI 不强制走 api.openai.com —— 通过 `~/.codex/config.toml` 的
`[model_providers.<name>]` 可指向任何 OpenAI 兼容端点（与 Claude Code 端
`ANTHROPIC_BASE_URL=...` 对称），常用于自建网关。示例配置、容器部署的两个
坑（项目级 `.codex/config.toml` 需要交互式 trust，容器里改挂全局；driver 容器里
codex 是 `docker exec` 按需调起，不是 PID 1）以及 `permissions` skill 在两种
后端下的 JSON 例子，统一见
[`multi-agent/deploy/agent-backends.md`](multi-agent/deploy/agent-backends.md)。
详细参数与非 bootstrap 部署路径见
[`multi-agent/deploy/README.md`](multi-agent/deploy/README.md)。

## 拓扑

```
                       ┌──────────────────────┐
   Claude Code / VS Code│   driver-agent       │  本地、用户侧、单实例（织工）
            ───────────▶│  (stdio MCP server)  │  ── 工作区上下文 + 编排工具
                       └──────────┬───────────┘
                                  │  agentserver workspace
              ┌───────────────────┼───────────────────┐
              ▼                   ▼                   ▼
        ┌──────────┐        ┌──────────┐        ┌──────────┐
        │ slave-a  │        │ slave-b  │  …     │ slave-N  │   各节点独立 self-host
        │ skills:  │        │ skills:  │        │ skills:  │   能力 / 资源不同
        │ chat,mcp │◀──┐    │ chat,bash│        │ register │
        │ bash,…   │   │    │ mcp,…    │        │ _mcp,…   │
        └────┬─────┘   │    └────┬─────┘        └────┬─────┘
             │     bash 写 + register_mcp 注册的新 MCP server
             │         （driver 触发的 dynamic-mcp 闭环）
             └────────── observer-server ────────────┘
                       (HTTP 遥测，可独立部署)
```

- **一个 driver，多个 slave**：driver 直接看到 workspace 里所有 slave 的能力，可以 1→1 直派，也可以构建 DAG 1→N 扇出；slave 之间没有隐式连接，由 driver 控制。
- **全员可 self-host**：driver / master / slave / observer / agentserver 都是普通 Go 二进制或容器；可以全部跑在一台机器上做本地开发，也可以把 slave 散布到不同机房、不同硬件（GPU 节点、抓图节点、压缩节点…），只要它们能注册到同一个 agentserver workspace。
- **Master 作为兼容路径**：master 提供 `route` / `fanout` 两种基于 claude planner 的编排技能，在不便直连 slave 时仍可使用。
- **Observer 解耦遥测**：driver / master / slave 都以 best-effort 方式向 observer 推送任务、子任务、artifact 事件；observer 单独部署，提供 HTTP 视图。

## 按需建能力（dynamic-mcp 闭环）

这是 Loom 最想突出的特性，单独拎出来讲：

1. driver 调 `inspect_capabilities` 发现现有 slave 谁都没有所需的工具。
2. driver 用 `bash` 让目标 slave 在线写一段 Python MCP server，并跑通烟雾 / 验收测试。
3. 验收通过后 driver 调 `register_mcp`，slave 把它持久化到 `dynamic_mcp.yaml` 并刷新 `CAPABILITIES.md`。
4. 下一轮 `dry_run_contract` / `submit_contract_task` 就能把这套新能力当作普通 `skill:"mcp"` 节点调度。

效果是：**集群从一个最小骨架（只装 claude + bash）起步，按用户真实任务长出真正用得到的工具**，不需要事先准备一套庞大的预制 MCP 集成。`examples/dynamic-mcp/` 是完整的端到端示例。

## 其它设计要点

- **Driver-first 编排**：用户在 Claude Code / VS Code 扩展里直接和 driver 对话；driver 既是一个 stdio MCP server，也是 agentserver workspace 里的一个普通 agent，能把用户本地的文件清单透传给 master/slave。
- **能力可发现**：每个 slave 启动时会把自己的 skills、MCP servers、资源、运行时信息写到 `journal/CAPABILITIES.md`；driver 通过 `inspect_capabilities` 决定路由——这也正是"按需建能力"得以闭环的基础。

## 四个二进制

| Binary | 角色 | 文档 |
|---|---|---|
| `cmd/driver-agent` | 本地 driver，作为 Claude Code 的 stdio MCP server，承载工作区上下文与编排工具 | [cmd/driver-agent/README.md](multi-agent/cmd/driver-agent/README.md) |
| `cmd/master-agent` | 编排器，使用 claude 作为 planner / router / reducer，把任务委派给 workspace 中其它 agent | [cmd/master-agent/README.md](multi-agent/cmd/master-agent/README.md) |
| `cmd/slave-agent` | 工作 agent，接受任务并通过 claude 或 MCP 执行，维护能力清单 | [cmd/slave-agent/README.md](multi-agent/cmd/slave-agent/README.md) |
| `cmd/observer-server` | 独立 HTTP observer，存储并展示 driver / master / slave 遥测 | [cmd/observer-server/config.example.yaml](multi-agent/cmd/observer-server/config.example.yaml) |

### Slave 公开的核心 skills

- `chat` — 自然语言任务，由 slave 内嵌的 claude 执行
- `mcp` — JSON 调用 `{server, tool, args}`，直接打到某个 MCP server
- `register_mcp` — 注册一段已经在 slave 上写好并通过烟雾测试的 MCP server 源码
- `unregister_mcp` — 解除注册某个 dynamic MCP server（从 `dynamic_mcp.yaml` 移除、杀掉子进程、刷新 CAPABILITIES）；不删源码文件
- `bash` — 由 slave 原生 Go executor 执行的确定性 shell 任务
- `claude_permissions` — 通过任务通道读取 / 修改 slave 上 Claude Code 的 project 权限（过渡方案）

### Driver MCP 工具

驱动侧暴露的工具命名空间在 Claude Code 里以 `driver/` 出现，常用的包括：

- `inspect_capabilities` / `list_agents`
- `draft_task_contract` / `dry_run_contract` / `submit_contract_task`
- `get_task` / `wait_task` / `tail_subtasks` / `cancel_task`
- `run_slave_bash` / `register_slave_mcp` / `unregister_slave_mcp`
- `get_slave_claude_permissions` / `update_slave_claude_permissions`

完整 schema 参见 `docs/superpowers/specs/2026-05-09-generic-driver-agent-design.md` 与 `skills/multiagent`。

## 仓库结构

```
.
├── README.md / README.en.md          顶层文档（你正在看的）
├── skills/multiagent/                Claude Code / Codex 侧的 multiagent skill
├── docs/superpowers/                 设计 spec 与执行计划
└── multi-agent/                      Go module（项目代号 Loom，路径暂未重命名）
    ├── go.mod                        module github.com/yourorg/multi-agent
    ├── cmd/
    │   ├── driver-agent/             stdio MCP + workspace agent
    │   ├── master-agent/             编排 agent
    │   ├── slave-agent/              工作 agent
    │   └── observer-server/          遥测后端
    ├── internal/
    │   ├── config, store, webui, tunnel, poller          全员共享
    │   ├── executor, journal, dispatch, capability(doc)  slave 侧
    │   ├── orchestrator, orchestration, planner          master 侧
    │   ├── driver, contract, claudeperm, progress        driver 侧
    │   └── buildspec, observer, observerclient,
    │       observerstore, observerweb                    遥测 / 构建规范
    ├── pkg/transport                 对外可复用的传输辅助
    ├── examples/
    │   ├── driver-first/             driver-first 编排范例
    │   ├── dynamic-mcp/              bash → register_mcp 闭环
    │   ├── generic-driver/           通用 driver 拼装文件
    │   └── image-pipeline/           多 slave 图像采集 + 压缩流水线
    ├── dev/
    │   ├── agent-runtime/            容器运行时镜像（含 numpy 与 Claude Code 默认权限）
    │   ├── configs/                  driver/master/slave/observer 示例配置
    │   ├── compose.distributed.yaml  docker compose 一键拉起
    │   └── tmp/                      workspace 辅助脚本与 e2e 临时目录
    ├── testdata/                     fake-claude.sh / fake-planner.sh / fake-mcp-stdio
    └── tests/
        ├── contract/                 build tag: contract
        ├── runtime/                  runtime 镜像与权限文档
        ├── smoke/                    build tag: smoke（手工，需 ANTHROPIC_API_KEY）
        └── claude_driver/            Claude Code driver 用例 fixtures（matmul 等）
```

## 构建与测试

所有 Go 命令都在 `multi-agent/` 目录下执行：

```bash
cd multi-agent
go build ./...
go vet ./...
go test ./... -race -count=1
go test -tags=contract ./tests/contract/...
go test -tags=smoke ./tests/smoke/...        # 手工执行
```

单独构建某个 binary：

```bash
go build -o cmd/driver-agent/driver-agent       ./cmd/driver-agent
go build -o cmd/master-agent/master-agent       ./cmd/master-agent
go build -o cmd/slave-agent/slave-agent         ./cmd/slave-agent
go build -o bin/observer-server                  ./cmd/observer-server
```

## Self-host 一套

最简单的方式是 docker compose（自带 postgres + agentserver + 一对 slave）：

```bash
cd multi-agent/dev
ANTHROPIC_API_KEY=... docker compose -f compose.distributed.yaml up --build
```

`dev/agent-runtime/Dockerfile` 已内置 `python3-numpy` 以及一份 `/root/.claude/settings.json` 默认权限白名单，避免 slave 上 Claude Code 每次都被权限提示打断。手工跑容器或本地裸跑时，可参照 [`multi-agent/tests/runtime/README.md`](multi-agent/tests/runtime/README.md) 在每个 slave workdir 放一份 `.claude/settings.local.json`。

要把 slave 放到不同节点上自托管，只需：

1. 在每个目标机器上 `go build ./cmd/slave-agent`，或拉 `dev/agent-runtime` 镜像。
2. 复制 `dev/configs/slave-*.example.yaml` 改 `server.url`、`discovery.display_name`、`discovery.skills`、需要暴露的 `resources` / `mcp_servers`。
3. 首次启动会打印 device-flow URL，浏览器走完授权后凭据回写到 yaml；之后 driver 端就能在 `inspect_capabilities` 里看到这台新 slave。

driver 永远只跑在用户本地（要 attach 到 Claude Code）；master / observer 既可以和 driver 在同一台，也可以放到独立的 self-host 节点上。

## Observer

observer 是独立 HTTP 服务，不依赖 agentserver；driver / master / slave 通过 bearer token 推送遥测，失败不会影响任务执行。

```bash
go build -o bin/observer-server ./cmd/observer-server
cp cmd/observer-server/config.example.yaml observer.yaml
./bin/observer-server --config observer.yaml
```

各 agent 的 `observer:` 配置要互相对得上：

```yaml
observer:
  enabled: true
  url: https://observer.example.com
  workspace_id: ws-local
  agent_id: driver-local
  token: driver-token
```

视图：

- `http://localhost:8090/drivers`
- `http://localhost:8090/masters`
- `http://localhost:8090/slaves`

## Skill 与文档

Claude Code / Codex 侧用的 multiagent skill 在仓库根目录的 `skills/multiagent/`，配套 reference 文档覆盖 driver tools / slave skills / task contract / orchestration patterns。

设计与计划文档在 `docs/superpowers/`，按时间排序，最近的几篇值得先看：

- `specs/2026-05-09-generic-driver-agent-design.md`
- `specs/2026-05-09-dynamic-mcp-design.md`
- `specs/2026-05-13-typed-buildmcp-progress-design.md`
- `specs/2026-05-14-distributed-driver-master-contract-design.md`
- `specs/2026-05-14-observer-artifact-relay-temporary-design.md`
- `plans/2026-05-19-bash-driven-mcp-registration.md`

早期的 slave / master 设计文档（`2026-04-27`、`2026-04-28`）仍可参考，但其中的目录命名 (`slave_agent/...`) 早于本次重命名，不再随 refactor 自动同步。
