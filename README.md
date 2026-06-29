# Loom

> 你的织机：一台本地 driver，把多台 self-host slave 提供的能力织进任务；缺线时，就在 slave 上当场纺一根。

**语言**: [简体中文](README.md) · [English](README.en.md)

Loom 是基于 [agentserver](https://github.com/agentserver/agentserver) 平台的一组自定义 agent，共享同一个 Go module 与一套内部包。整套系统的目标是：**用户在 Claude Code、Codex CLI 或 opencode 里跑一台本地 driver，用它统一指挥一组分布在不同机器、不同能力的 self-host slave**；driver 负责澄清意图、检视能力、编排任务；slave 在各自节点本地执行；所有遥测汇集到独立部署的 observer 便于回放与调试。支持**混合机队**部署：每个 driver 和 slave 独立选择后端（`--agent claude|codex|opencode`），来自不同后端的 agent 共享同一个 observer / workspace。

最与众不同的一点：**集群能力不是固定的**。当现有 slave 都满足不了某个任务时，driver 会让一台 slave 在线写、跑、验证一段 Python MCP server，然后通过 `register_mcp` 把它注册进去；下一轮编排时这套新能力就已可调用。集群因此可以从一个最小骨架开始，按用户实际任务慢慢长出真正用得到的工具，而不是先做一大套预制集成。整件事就像织布：经线、纬线来自不同 slave 的现成能力，driver 决定怎么交织成最终任务；如果某根关键的线还不存在，就当场在 slave 上纺出来再织进去。

> 命名说明：项目最初叫 `multi-agent`，现已更名为 **Loom**。Go module 路径、目录名暂时保持 `multi-agent/` 不动，等替换可以集中做一次。

## 快速部署

三种角色（observer → slave → driver）通过 release 上的 bootstrap 脚本部署，
**不需要 clone 仓库**，适用于任何 Linux（amd64/arm64）和 Termux/Android（aarch64）。
默认后端为 **Codex CLI**；也可传 `--agent claude` 或 `--agent opencode` 切换。

**前置要求**：`codex` CLI（`npm i -g @openai/codex`，Node ≥ 22）+ `codex login`
或 `export OPENAI_API_KEY=...`。Codex CLI 可通过 `~/.codex/config.toml` 的
`[model_providers.<name>]` 指向任何 OpenAI 兼容端点，不强制走 api.openai.com。

### 1. Observer（控制面）

```bash
export LOOM_WORKSPACE_ID=WS_ID LOOM_API_KEY='YOUR_API_KEY'   # 两个都可省
bash <(curl -fsSL \
  https://github.com/agentserver/loom/releases/latest/download/bootstrap-observer.sh) \
  --name obs-prod --systemd
```

`LOOM_API_KEY` 不传则自动生成并打印一次（observer 自身的 bootstrap key）。

### 2. Slave（执行器）

```bash
export LOOM_OBSERVER_URL=http://OBSERVER_HOST:8090
bash <(curl -fsSL \
  https://github.com/agentserver/loom/releases/latest/download/bootstrap-slave.sh) \
  --name slave-myhost --systemd          # Termux/Android 上去掉 --systemd
```

首启动 stderr 会打印 device-code URL，浏览器批准后 agentserver 下发
proxy token，slave 凭此 token 直接向 observer 认证——**不需要**手动传
`LOOM_API_KEY`。

### 3. Driver（编排器）

```bash
export LOOM_OBSERVER_URL=http://OBSERVER_HOST:8090
bash <(curl -fsSL \
  https://github.com/agentserver/loom/releases/latest/download/bootstrap-driver.sh) \
  --name driver-myhost
```

部署后执行：

```bash
./driver-agent register --config ./config.yaml   # 一次性 device-code OAuth
cd <项目目录> && codex                              # 首次运行提示 trust 目录
```

Codex 读取 `.codex/config.toml` 中的 `[mcp_servers.driver]` 自动拉起
`driver-agent serve-mcp`；用户在 Codex 内即可调用 `list_agents`、
`submit_task` 等 driver 工具编排 slave。

### 切换后端

传 `--agent claude` 或 `--agent opencode` 给 bootstrap 脚本即可。
Claude Code 模式写 `.mcp.json` + `.claude/skills/`；opencode 模式写
`~/.config/opencode/opencode.json` + `AGENTS.md`。详细对比见
[`multi-agent/deploy/agent-backends.md`](multi-agent/deploy/agent-backends.md)，
完整参数见
[`multi-agent/deploy/README.md`](multi-agent/deploy/README.md)。

## 拓扑

```
                        ┌──────────────────────┐
   Claude Code / Codex  │   driver-agent       │  本地、用户侧、单实例（织工）
        / opencode ────▶│  (stdio MCP server)  │  ── 工作区上下文 + 编排工具
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
- **全员可 self-host**：driver / slave / observer / agentserver 都是普通 Go 二进制或容器；可以全部跑在一台机器上做本地开发，也可以把 slave 散布到不同机房、不同硬件（GPU 节点、抓图节点、压缩节点…），只要它们能注册到同一个 agentserver workspace。
- **Observer 解耦遥测**：driver / slave 都以 best-effort 方式向 observer 推送任务、子任务、artifact 事件；observer 单独部署，提供 HTTP 视图。

## 按需建能力（dynamic-mcp 闭环）

这是 Loom 最想突出的特性，单独拎出来讲：

1. driver 调 `inspect_capabilities` 发现现有 slave 谁都没有所需的工具。
2. driver 用 `bash` 让目标 slave 在线写一段 Python MCP server，并跑通烟雾 / 验收测试。
3. 验收通过后 driver 调 `register_mcp`，slave 把它持久化到 `dynamic_mcp.yaml` 并刷新 `CAPABILITIES.md`。
4. 下一轮 `dry_run_contract` / `submit_contract_task` 就能把这套新能力当作普通 `skill:"mcp"` 节点调度。

效果是：**集群从一个最小骨架（只装 claude + bash）起步，按用户真实任务长出真正用得到的工具**，不需要事先准备一套庞大的预制 MCP 集成。`examples/dynamic-mcp/` 是完整的端到端示例。

> ⚠️ **Historical note — `BuildMCPExecutor` was never implemented.**
> 旧 spec `docs/superpowers/specs/2026-05-09-dynamic-mcp-design.md` 描述了一条 master 端自动构建 MCP 的 `BuildMCPExecutor` / `build_mcp` 路径。该代码**从未落地**（`grep -ri BuildMCPExecutor multi-agent/` 命中为 0）；master path 也已冻结（见 [`docs/intermediate/`](../paper_writing/docs/intermediate/) 的 v3 计划与 [`skills/multiagent/SKILL.md`](skills/multiagent/SKILL.md)）。当前 Loom 走 driver-first 的「user-promoted capability lifecycle」：用户用 `bash` 在 slave 上现场写脚本，acceptance 通过后再 `register_mcp` 持久化，整个闭环由 driver 编排，没有自动 master 构建路径。
>
> 配套的 eval-runner 默认 config 也将 master path 关停：见 [`multi-agent/dev/configs/eval-default.yaml`](multi-agent/dev/configs/eval-default.yaml)（`routing.mode: driver-first`，`allow_master: false`）。

## 其它设计要点

- **Driver-first 编排**：用户在 Claude Code / Codex / opencode 里直接和 driver 对话；driver 既是一个 stdio MCP server，也是 agentserver workspace 里的一个普通 agent，能把用户本地的文件清单透传给 slave。
- **能力可发现**：每个 slave 启动时会把自己的 skills、MCP servers、资源、运行时信息写到 `journal/CAPABILITIES.md`；driver 通过 `inspect_capabilities` 决定路由——这也正是"按需建能力"得以闭环的基础。

## 四个二进制

| Binary | 角色 | 文档 |
|---|---|---|
| `cmd/driver-agent` | 本地 driver，作为 Claude Code / Codex / opencode 的 stdio MCP server，承载工作区上下文与编排工具 | [cmd/driver-agent/README.md](multi-agent/cmd/driver-agent/README.md) |
| `cmd/slave-agent` | 工作 agent，接受任务并通过 claude / codex / MCP 执行，维护能力清单 | [cmd/slave-agent/README.md](multi-agent/cmd/slave-agent/README.md) |
| `cmd/observer-server` | 独立 HTTP observer，存储并展示 driver / slave 遥测；同时托管 userspace 包仓库 | [cmd/observer-server/config.example.yaml](multi-agent/cmd/observer-server/config.example.yaml) |
| `cmd/mcp-userspace` | 命令行客户端，把验证过的 MCP server / skill 打成包推送到 observer 的 userspace，再在另一台机器上 `install` | [cmd/mcp-userspace/](multi-agent/cmd/mcp-userspace/) |

### Slave 公开的核心 skills

- `chat` — 自然语言任务，由 slave 内嵌的 claude（或 codex）执行
- `bash` — 由 slave 原生 Go executor 执行的确定性 shell 任务
- `file` — 无状态的 `read` / `write` / `stat`，直接操作 slave 本地文件
- `register_mcp` — 注册一段已经在 slave 上写好并通过烟雾测试的 MCP server 源码
- `permissions` — 通过原生代码读取 / 修改 slave 上 Claude Code 或 Codex 的权限配置

`chat` skill 内还提供 **humanloop** 工具：slave 端的 claude/codex 可以调
`ask_user` / `request_permission` 主动暂停一轮，driver 把问题透传给坐在
Claude Code / VS Code 前的用户，回答到位再恢复执行。详见
`internal/humanloop/` 与 `docs/superpowers/specs/2026-05-26-humanloop-resumable-chat-design.md`。

### Driver MCP 工具

驱动侧暴露的工具命名空间在 Claude Code 里以 `driver/` 出现，常用的包括：

- `inspect_capabilities` / `list_agents`
- `draft_task_contract` / `dry_run_contract` / `submit_contract_task`
- `get_task` / `wait_task` / `tail_subtasks` / `cancel_task`
- `run_slave_bash` / `register_slave_mcp` / `unregister_slave_mcp`
- `get_slave_claude_permissions` / `update_slave_claude_permissions`

完整 schema 参见 `docs/superpowers/specs/2026-05-09-generic-driver-agent-design.md` 与 `skills/multiagent`。

### Python 客户端：loom-py

如果你想在 Python 脚本 / 笔记本里直接驱动 driver，而不打开 Claude Code，
可以用仓库内的 `multi-agent/python/`（PyPI 名 `loom`）。它把 driver MCP
封装成 fluent 的 workflow API，覆盖 chat / wait / expect_or_ask /
find_slave / 文件 IO 占位符等核心动作：

```python
import loom

with loom.workflow(goal="say HELLO") as wf:
    res = wf.chat("Reply with HELLO and stop.",
                  target="slave-local-prod").wait()
print(res.output)
```

零运行时依赖，唯一外部要求是 `driver-agent` 二进制在 PATH 上。详见
[`multi-agent/python/README.md`](multi-agent/python/README.md) 与
`docs/superpowers/specs/2026-05-27-loom-python-library-design.md`。

### Userspace：跨机器复用自己造的能力

当你在某台 driver 上让 slave 现场造出一个 MCP server（或写了一个新的
skill），可以用 `mcp-userspace` CLI 把它打成包推到 observer 上自己的
个人空间，再在另一台机器 / 另一个 workspace 上 `install`：

```bash
mcp-userspace login --url http://observer:8090 --token $TOKEN
mcp-userspace push  --slug wedding_almanac --bump-patch ./generated_mcp/wedding_almanac
mcp-userspace install --as mcp --workspace ws-work --overwrite wedding_almanac@1.0.0
```

服务端逻辑在 `internal/userspace` + `internal/mcpmarket`；driver 侧配套
的 `userspace-publish` skill 会在用户说"保存到我的空间"时被触发。

## 仓库结构

```
.
├── README.md / README.en.md          顶层文档（你正在看的）
├── skills/                           Claude Code / Codex 侧 skills
│   ├── multiagent/                   driver tools / slave skills / task contract / orchestration
│   ├── scaffold-mcp-server/          spec.json → stdio JSON-RPC 骨架
│   ├── mcp-acceptance/               register 前的语义验真 gate
│   └── userspace-publish/            把验证过的 MCP / skill 推到个人 userspace
├── docs/
│   ├── superpowers/                  设计 spec 与执行计划
│   └── intro/                        项目介绍 HTML 站（零依赖 SVG 图解）
└── multi-agent/                      Go module（项目代号 Loom，路径暂未重命名）
    ├── go.mod                        module github.com/yourorg/multi-agent
    ├── cmd/
    │   ├── driver-agent/             stdio MCP + workspace agent
    │   ├── slave-agent/              工作 agent
    │   ├── observer-server/          遥测后端 + userspace 包仓库
    │   └── mcp-userspace/            userspace 推 / 拉 / install CLI
    ├── internal/
    │   ├── config, store, webui, tunnel, poller          全员共享
    │   ├── executor, journal, dispatch, capability(doc)  slave 侧
    │   ├── driver, contract, claudeperm, progress        driver 侧
    │   ├── humanloop                                     chat 期 ask_user / request_permission
    │   ├── userspace, mcpmarket                          observer 上的个人包仓库
    │   └── buildspec, observer, observerclient,
    │       observerstore, observerweb                    遥测 / 构建规范
    ├── pkg/transport                 对外可复用的传输辅助
    ├── python/                       loom-py：driver 的 Python fluent workflow 客户端
    ├── deploy/                       生产部署模板 + bootstrap 脚本
    ├── examples/
    │   ├── driver-first/             driver-first 编排范例
    │   ├── dynamic-mcp/              bash → register_mcp 闭环
    │   ├── generic-driver/           通用 driver 拼装文件
    │   └── image-pipeline/           多 slave 图像采集 + 压缩流水线
    ├── dev/
    │   ├── agent-runtime/            容器运行时镜像（含 numpy 与 Claude Code 默认权限）
    │   ├── configs/                  driver/slave/observer 示例配置
    │   ├── compose.distributed.yaml  docker compose 一键拉起
    │   └── tmp/                      workspace 辅助脚本与 e2e 临时目录
    ├── testdata/                     fake-claude.sh / fake-planner.sh / fake-mcp-stdio
    └── tests/
        ├── contract/                 build tag: contract
        ├── runtime/                  runtime 镜像与权限文档
        ├── smoke/                    build tag: smoke（手工，需 ANTHROPIC_API_KEY）
        ├── claude_driver/            Claude Code driver 用例 fixtures（matmul 等）
        └── prod_test/                内部 prod 灰度配置（gitignored）
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
go build -o cmd/slave-agent/slave-agent         ./cmd/slave-agent
go build -o bin/observer-server                  ./cmd/observer-server
go build -o bin/mcp-userspace                    ./cmd/mcp-userspace
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

driver 永远只跑在用户本地（要 attach 到 Claude Code / Codex / opencode）；observer 既可以和 driver 在同一台，也可以放到独立的 self-host 节点上。

## Observer

observer 是独立 HTTP 服务，不依赖 agentserver；driver / slave 通过 bearer token 推送遥测，失败不会影响任务执行。

```bash
go build -o bin/observer-server ./cmd/observer-server
cp cmd/observer-server/config.example.yaml observer.yaml
./bin/observer-server --config observer.yaml
```

各 agent 的 `observer:` 配置要互相对得上：

```yaml
observer:
  enabled: true
  url: http://observer.local:8090
  workspace_id: ws-personal          # required; observer lazy-creates
  workspace_name: "Personal Lab"      # optional; recorded on first creation
  agent_id: driver-local
  api_key: REPLACE_ME
  token_state_path: /var/lib/driver/observer-token.json
```

视图：

- `http://localhost:8090/drivers`
- `http://localhost:8090/slaves`

## Skill 与文档

仓库根目录 `skills/` 下现有四个 skill，由 driver 端 Claude Code / Codex 加载：

- `multiagent` —— 主 skill，囊括 driver tools / slave skills / task contract / orchestration patterns 的 reference
- `scaffold-mcp-server` —— 从 `spec.json` 生成 stdio JSON-RPC 骨架（重跑保留 handler）
- `mcp-acceptance` —— `register_mcp` 之前必须过的语义验真 gate（exit 0 才能 register）
- `userspace-publish` —— 把验证过的 MCP / skill 推到 observer 上自己的 userspace

`docs/intro/` 是项目介绍 HTML 站（layered stack / cycle / related-work
等图解，零 JS 依赖），可以直接本地打开 `index.html`。

设计与计划文档在 `docs/superpowers/`，按时间排序，最近的几篇值得先看：

- `specs/2026-05-09-generic-driver-agent-design.md`
- `specs/2026-05-09-dynamic-mcp-design.md`
- `specs/2026-05-13-typed-buildmcp-progress-design.md`
- `specs/2026-05-14-distributed-driver-master-contract-design.md`
- `specs/2026-05-14-observer-artifact-relay-temporary-design.md`
- `specs/2026-05-26-humanloop-resumable-chat-design.md`
- `specs/2026-05-27-loom-python-library-design.md`
- `plans/2026-05-19-bash-driven-mcp-registration.md`

早期设计文档（`2026-04-27`、`2026-04-28`）仍可参考，但其中的目录命名 (`slave_agent/...`) 早于本次重命名，不再随 refactor 自动同步。
