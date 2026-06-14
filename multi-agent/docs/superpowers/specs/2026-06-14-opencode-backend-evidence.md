# opencode Backend — 测试证据

- **日期**：2026-06-14
- **分支**：`worktree-add-opencode-backend` @ HEAD `11a4f63`（Task 7 末提交；Task 8 文档/证据提交将在此之上）
- **范围**：[anomalyco/opencode](https://github.com/sst/opencode) 作为 3rd backend；slave 端 `pkg/agentbackend/opencode/` 完整 Backend 实现 + driver 端 deploy 模板/install 脚本分支
- **不在范围**：opencode `acp` 子命令、auth pass-through、master 路径（冻结）、host-local e2e（留 follow-up — slave 端真起 opencode 任务的 e2e 是单独 issue）

## 为什么 unit + binary smoke 足够

- backend 是纯本地子进程调用（与 codex 同形状，codex backend 已经 e2e 验证过相同形状 — 参见 `2026-06-14-agent-backend-unify-e2e-evidence.md`）
- humanloop MCP 注入由 unit test 验证（assert 临时 `opencode.json` content + `OPENCODE_CONFIG` env 传递）
- 真起 opencode 端到端跑任务需要：装 opencode + 配 provider auth（`opencode auth login`）+ provider 网络可达 — 属于 host-local e2e 工作量，与本 PR 解耦。Issue 跟进。

## 提交序列

```
11a4f63 deploy: opencode driver MCP template + install branches (Task 7)
1e856eb feat(cmd,config): wire opencode into driver+slave registries (Task 6)
0296c73 feat(agentbackend/opencode): Run() + humanloop MCP via OPENCODE_CONFIG (Task 4)
7003ead feat(agentbackend/opencode): detect + permissions Store (Task 3)
d1e0ff4 feat(agentbackend/opencode): llm runner (Task 2)
5ee9935 feat(agentbackend): opencode skeleton + KindOpencode + init registration (Task 1)
647062d docs: implementation plan for opencode backend support
19be4bf docs: spec for opencode backend support
```

（Task 8 的 docs/migration + 本 evidence 文档提交在序列尾部。）

## 任务覆盖

| Task | Commit | 内容 |
|---|---|---|
| 1 | `5ee9935` | `KindOpencode` 常量 + `Backend` skeleton + `init()` 注册到 `agentbackend` 注册表 |
| 2 | `d1e0ff4` | `llmRunner` (spawn `opencode run --dangerously-skip-permissions --format=json -`) + `testhelpers_test.go` 共享 `goBuildFake` |
| 3 | `7003ead` | `detect.go` (PATH lookup) + NoOp `Store` (拒绝 claude-only Allow/Deny 字段) |
| 4 | `0296c73` | `executor.Run()` + humanloop MCP via 临时 `opencode.json` + `OPENCODE_CONFIG` env；同 commit 含 RunResume（执行时合并了 Task 5） |
| 5 | （并入 `0296c73`） | `RunResume()` via `--session <id> --continue`，stdin "User answered: ..." |
| 6 | `1e856eb` | `cmd/{driver,slave}-agent/main.go` 各加一行 `_ "..."`；`internal/{driver,config}/config_test.go` 各一个 `AcceptsOpencodeKind` |
| 7 | `11a4f63` | `deploy/{linux,windows}/driver/opencode.json.template` + install.sh/ps1 `case "$AGENT"` 分支 |
| 8 | （本提交） | migration doc + 证据 + PR |

## 测试

**新 package `pkg/agentbackend/opencode`（15 个测试，全部 race+count=1 通过）**：

| Test | Pins |
|---|---|
| `TestOpencodeBackend_AutoRegisters` | 导入 sub-package 即自动注册到 `agentbackend.RegisteredKinds()`（issue #15 promise） |
| `TestOpencodeNew_DefaultsBin` | `cfg.Bin==""` 时 factory 默认为 `"opencode"` |
| `TestOpencodeNew_KeepsExplicitBin` | operator 显式 bin 路径不被覆盖 |
| `TestOpencodeBackend_RegistryDispatchesNew` | `agentbackend.New(Config{Kind:KindOpencode})` 走 builder 出 `*Backend` |
| `TestDetectFailsWhenBinMissing` | `detect()` 对不存在 bin 报错 |
| `TestDetectPassesWhenBinExists` | 存在的 fake bin → ok（不 shell-out） |
| `TestExecutor_ReplaysCapturedFixture` | 用 `testdata/opencode_run.ndjson` 重放真 opencode event 流；`extractAssistantText` 正确解 `type=="text"` + `part.text` |
| `TestExecutor_InjectsHumanloopMCPViaTempConfig` | fake bin 把 `OPENCODE_CONFIG` 文件内容捕获到 sentinel；断言 `mcp.loom_humanloop.type=="local"` + `command[1]=="humanloop-mcp"` |
| `TestExecutor_RunResume_UsesSessionFlag` | argv 含 `--session sess-abc` + `--continue`；stdin 含 `User answered: yes please proceed` |
| `TestLLMRunnerReturnsTrimmedStdout` | fake bin 回显 `pong\n\n` → LLMRunner 返回 `"pong"` |
| `TestLLMRunner_SurfacesStderrTailOnExit` | 退出非零时 err 包裹 stderr tail（含 marker） |
| `TestLLMRunner_ExtraArgsAppended` | `ExtraArgs` 在默认 flags 之后追加，operator override 优先生效 |
| `TestStore_GetReturnsDefault` | 默认 state：`Backend=KindOpencode`、`Mode="ask"` |
| `TestStore_PatchAcceptsMode` | Patch Mode 回显但 NoOp 不持久化（后续 Get 仍是 "ask"） |
| `TestStore_PatchRejectsClaudeOnlyFields` | `AllowAdd/AllowRemove/DenyAdd/DenyRemove` 四种 Patch 都报错 |

**Acceptance（registry path end-to-end）**：

- `internal/driver/config_test.go::TestLoadConfig_AcceptsOpencodeKind` — driver YAML `agent.kind: opencode` 加载干净，`Agent.Bin` 默认 `"opencode"`
- `internal/config/config_test.go::TestSlaveLoad_AcceptsOpencodeKind` — slave 端镜像测试

**Full repo race**：

```
$ go test ./... -race -count=1
... (58 个 packages ok / 12 个 no test files) ...
ok  	github.com/yourorg/multi-agent/cmd/driver-agent	1.018s
ok  	github.com/yourorg/multi-agent/cmd/slave-agent	1.433s
ok  	github.com/yourorg/multi-agent/internal/config	1.076s
ok  	github.com/yourorg/multi-agent/internal/driver	2.022s
ok  	github.com/yourorg/multi-agent/internal/executor	34.670s
ok  	github.com/yourorg/multi-agent/pkg/agentbackend	1.043s
ok  	github.com/yourorg/multi-agent/pkg/agentbackend/claude	5.737s
ok  	github.com/yourorg/multi-agent/pkg/agentbackend/codex	4.889s
ok  	github.com/yourorg/multi-agent/pkg/agentbackend/opencode	2.213s
... (其余全 ok) ...
```

零 FAIL，所有 race 检查通过。

## 二进制 sanity

```
$ go build -o /tmp/bin-smoke-opencode/driver-agent ./cmd/driver-agent
$ go build -o /tmp/bin-smoke-opencode/slave-agent ./cmd/slave-agent
$ /tmp/bin-smoke-opencode/driver-agent
driver-agent — bridges Claude Code to the multi-agent workspace.

Usage:
  driver-agent register   --config /path/to/driver.yaml
  driver-agent serve-mcp  --config /path/to/driver.yaml
```

`go build ./...` clean，driver/slave 二进制 17 MiB 各一个，help 文本正常。

## Pre-flight：实测的 opencode event schema（Step 4.0 第一项）

**安装**：`npm i -g opencode-ai`；版本 v1.17.6（实测）。

**运行时捕获**：BLOCKED。本机环境的 `OPENAI_API_KEY` / `ANTHROPIC_API_KEY` 都以 `ms-...` 开头（指向 relay-proxy），路由返回 401/404，opencode 在第一帧就 fail。`/tmp/opencode-run.ndjson` 里只拿到一行真实 frame：

```json
{"type":"error","timestamp":1781442127952,"sessionID":"ses_139c65c48ffe53chodpG4RNUgp",
 "error":{"name":"APIError","data":{"message":"Not Found: 404 page not found",
 "statusCode":404,"isRetryable":false}}}
```

**Schema 来源**：直接读 opencode 源码
`packages/opencode/src/cli/cmd/run.ts`
（[github.com/sst/opencode/blob/dev/packages/opencode/src/cli/cmd/run.ts](https://github.com/sst/opencode/blob/dev/packages/opencode/src/cli/cmd/run.ts)）。
其中 `emit()` helper 对每一行都无条件 merge：

```
{ "type": <name>, "timestamp": <ms>, "sessionID": <sess.id>, ...data }
```

`run.ts` 的事件 `type` 取值：`step_start`、`step_finish`、`tool_use`、`text`、`reasoning`、`error`。

**最终 assistant text 事件**：
- `type == "text"`
- 文本路径：`.part.text`
- 仅当 part **finalised**（即 `part.time.end` 已设置）时发射；因此**无需做 delta 拼接** — 每个 finalized part 单帧到位。

**session id**：
- 顶层 `.sessionID`（camelCase — opencode 特有；与 codex 的 snake_case `session_id` 区分）
- 每一行都带，包括 error 帧。

**测试 fixture** (`pkg/agentbackend/opencode/testdata/opencode_run.ndjson`)：
- header comment 完整记录了上述结论 + 真捕获的 error 帧
- 末尾追加 3 个合成 happy-path 帧（`step_start` / `text` / `step_finish`），字节形状与 `run.ts` 的 `emit()` 输出一致（已对照源码验证），用于驱动 `TestExecutor_ReplaysCapturedFixture` 端到端走通解析路径

`pkg/agentbackend/opencode/executor.go` 的 `opencodeEvent` struct + `extractAssistantText` 据此实测 schema 写定（非占位）。

## Pre-flight：opencode desktop 配置路径（Step 4.0 第二项）

**结论：UNVERIFIED**。

本环境为 Arch Linux，无 apt/dpkg，且无 GUI，无法安装 opencode desktop `.deb` 包或 AppImage 并真起来观察。spec 中的「Linux 走 XDG（`~/.config/opencode/opencode.json`）/ Windows 走 `%APPDATA%`」假设**未在真桌面实例上验证**。

**当前处置**：install.sh / install.ps1 按 spec 假设写入 XDG / APPDATA 路径；CLI 一侧实测可消费（opencode CLI 读 XDG 是 `run.ts` 源码可见的，已在 Task 7 模板渲染 smoke 中印证）。Desktop 一侧待 follow-up。

**Follow-up**：声明 desktop 支持 e2e-tested 前，需要在装有 opencode desktop 的机器上 verify。若实测路径与 XDG 不同（例如 `~/.local/share/opencode/`），开 issue 调整 install.sh 并更新 migration 文档。本 PR 不声称 desktop 已端到端测过。

## Master-path freeze

```
$ git diff --stat master..HEAD -- 'cmd/master-agent/**' 'internal/orchestrator/**' 'internal/orchestration/**'
(empty — exit 0, 0 lines)
```

冻结路径零改动；符合 PR #18 / issue #15 约束。

## Follow-up

- **host-local e2e**：slave 端真起一个 opencode 任务（需 provider auth），验证 humanloop pause/resume 完整闭环
- **desktop 路径验证**：在装有 opencode desktop 的机器上 confirm `~/.config/opencode/opencode.json` 真被读取；若否，单独 issue + install.sh 调整
- **opencode acp 子命令**：v2 性能优化（spec 提及），非本 PR 范围
- **auth pass-through**：当前 slave 不传 provider 凭证，要求 operator 提前 `opencode auth login`；如果未来要做 slave-managed auth，单独设计
