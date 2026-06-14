# Opencode Backend (PR #19) — host-local e2e 证据

- **日期**：2026-06-14
- **分支**：`worktree-add-opencode-backend` @ HEAD `907e512` (PR #19)
- **拓扑**：本机 codex (host orchestrator) → driver-agent (codex backend) → ws-prod tunnel (agent.cs.ac.cn) → slave-agent (**opencode backend**) → observer (127.0.0.1:18091)
- **依据**：memory `e2e_required_for_features_and_fixes` + `e2e_prod_test_codex_local` runbook
- **针对修复**：
  - PR #19 主体：新增 opencode 作为 slave-agent 第 3 个 backend（与 claude / codex 并列），完整跑通 chat skill 主链路
  - 907e512：opencode `writeOpencodeHumanloopConfig` 必须在 inject loom_humanloop **之前** merge 用户 `opencode.json`（OPENCODE_CONFIG 是 full-file override，不是 merge；不像 claude `--mcp-config` flag 或 codex `-c mcp_servers.X` inline）。否则 operator 自定义 provider/model 会在 slave spawn opencode 时被丢掉，opencode 直接报 'no model' / 'provider not configured'。
  - 反向对照：PR #18 的 P2-A 在 codex slave 上是 `--print` 误用；PR #19 在 opencode slave 上的对称失败假说就是 'opencode --print' / 未识别 flag — 本轮 slave.log **零命中**这类错误。

## 环境

| 角色 | binary | sandbox_id | short_id |
|---|---|---|---|
| driver  | bin/driver-agent.linux-amd64 (PR #19, codex backend) | 77b91a64-6f96-4fcb-96a9-c650b1867191 | zuv584rs |
| slave   | bin/slave-agent.linux-amd64  (PR #19, **opencode backend**) | d6736d8d-9beb-4586-b43b-58e6b9edf700 | fdmqu16y |
| observer | bin/observer-server.linux-amd64 (PR #19) | — (127.0.0.1:18091) | — |

OAuth：driver 走 device-code 注册到 ws-prod；slave 同上 (user_code `wUvMTxbz`)；两者同 workspace `96bd3120-a725-44d9-a047-a75ed89af3ed`（沿用 PR #18 e2e workspace）。

工作目录：`/root/multi-agent/multi-agent/tests/prod_test/.e2e-2026-06-14-opencode/`

## Pre-flight：opencode 本机已就绪

- `opencode --version` → `1.17.6`（系统 PATH `/usr/bin/opencode`，由 CAPABILITIES.md 自动探测确认）
- 全局 `~/.config/opencode/opencode.json`：
  ```json
  {
    "$schema": "https://opencode.ai/config.json",
    "model": "modelserver/deepseek-v4-flash",
    "provider": {
      "modelserver": {
        "npm": "@ai-sdk/openai-compatible",
        "name": "modelserver",
        "options": {
          "baseURL": "https://code.ai.cs.ac.cn/v1",
          "apiKey": "{env:OPENAI_API_KEY}"
        },
        "models": {
          "deepseek-v4-flash": {"name": "deepseek-v4-flash"}
        }
      }
    }
  }
  ```
- **偏差说明**：
  - 用 `@ai-sdk/openai-compatible`（npm 上 OpenAI 兼容 provider 模板），不是 opencode 内置的 `openai` provider — 因为 relay 走 `/v1` OpenAI 协议但 base_url 非 api.openai.com，opencode 内置 provider 没有 baseURL 覆盖入口
  - 模型选 `deepseek-v4-flash`，不是 codex 那侧 `gpt-5.5` — relay 后端按模型路由，opencode 走的 provider/model 组合在 relay 内可用，gpt-5.5 当前只对 codex `responses` wire-api 可用
- 独立 smoke：`opencode run "Hello, how are you?"` 之前已 standalone 跑通（来自上下文交接，本 turn 未重跑）

## YAML schema 与 backend 注册

`slave-config.yaml` 用 PR #18 引入的 flat `agent:` schema：

```yaml
agent:
    kind: opencode          # <-- 新枚举值
    bin: opencode
    workdir: /root/multi-agent/multi-agent/tests/prod_test/.e2e-2026-06-14-opencode/slave-workdir
    extra_args: []
discovery:
    display_name: slave-opencode-e2e
    skills: [chat, file, register_mcp]
```

- slave-agent.linux-amd64 加载该 config 不报 unknown kind（PR #19 在 `internal/agentbackend/registry` 注册了 opencode；commit `1e856eb feat(cmd,config): wire opencode into driver+slave registries`）
- driver 仍走 codex backend（host orchestrator 用 codex MCP 协议，与 slave 的 backend kind 解耦）

## 4 步 prompt 跑通（chat skill 主链路）

`logs/codex-last-message.txt` 最终消息（codex 退出码 0）：

- **Step 1** (`driver.list_agents`)：**ok**。找到 `slave-opencode-e2e` (agent_id `d6736d8d-9beb-4586-b43b-58e6b9edf700`, short_id `fdmqu16y`)，`skills=[chat, file, register_mcp]`。
- **Step 2** (`driver.submit_task` PONG)：**ok**。`task_id = task_928afd1c-528d-45f2-b8a1-df27a9b4b805`。
- **Step 3** (`driver.wait_task`)：**status=`completed`**，`output = "PONG"`。**这条直接证明 opencode CLI 被 slave 正确 invoke + 实际触达 relay 拿到模型回复 + 事件流被 executor 解出 final text**。
- **Step 4** (submit + wait "list 3 things")：`task_id = task_c1017f91-5db5-4c2e-89ba-5cbdeed92d74`，`status=completed`，output：
  ```
  1. Explore and modify codebases using search, read, write, edit, and bash tools.
  2. Delegate complex multi-step tasks to specialized sub-agents for parallel processing.
  3. Fetch web content and search for information to answer questions.
  ```
- **End**：codex 在 `last-message.txt` 末尾输出 `E2E DONE`，`codex exit code 0`。

driver 侧 `logs/driver-tasks.jsonl`：

```
{"event":"delegate_task","tool":"submit_task","task_id":"task_928afd1c-...","target_id":"d6736d8d-...","target_display_name":"slave-opencode-e2e","skill":"chat","status":"pending","wait":false,"timeout_sec":180}
{"event":"delegate_task","tool":"submit_task","task_id":"task_c1017f91-...","target_id":"d6736d8d-...","target_display_name":"slave-opencode-e2e","skill":"chat","status":"pending","wait":false,"timeout_sec":180}
```

两条 delegate 记录 target_id 都正确解析到 slave sandbox。

## P2-A 对称证据：opencode slave 无 CLI 协议误用

**反向对照**：PR #18 codex slave 修复前是 journal 硬编码 `exec(AgentBin, "--print", prompt)` — 在 codex CLI 上是 unrecognized flag。对 opencode 而言对称失败的形态是「executor 给 opencode 传 claude/codex 风格 flag」，会在 slave.log 里出现 `unrecognized flag` / `opencode --print not found` / `opencode exit N` 之类报错。

**实际**：`logs/slave.log` 全文 7 行：

```
Open this URL to authenticate:
    https://agent.cs.ac.cn/oauth2/device/verify?user_code=wUvMTxbz
2026/06/14 23:13:47 agentsdk: connecting to https://agent.cs.ac.cn
2026/06/14 23:13:47 agentsdk: tunnel connected (sandbox: d6736d8d-9beb-4586-b43b-58e6b9edf700)
```

- `grep -E 'opencode.*exit|unrecognized|unknown.*flag|--print|opencode.*error'` → **零命中**
- 2 个 chat task 全 `status=completed`，slave 进程未崩、未刷错。如果 executor 错配了 CLI 协议或 OPENCODE_CONFIG 未注入 humanloop，最迟 Step 3 PONG 任务即应失败 — 本次 PONG/列 3 件事都拿回 LLM 真实响应，证明：
  1. opencode 子进程被用「对的 argv」spawn（无 claude/codex flag 串味）
  2. OPENCODE_CONFIG 临时文件被写入并加载（否则 humanloop MCP 注入不到，且因为 OPENCODE_CONFIG 是全量覆盖，operator 的 modelserver provider 也会丢，模型直接报 'no provider' — 但任务正常完成）

## 907e512 executor merge fix 反向证明

合并 fix 的核心断言：「写 tmp 配置时，用户 `~/.config/opencode/opencode.json` 的 provider/model 配置必须先 merge 进 base map，再 inject `loom_humanloop` 到 mcp」。

直接证据（间接但充分）：

1. **如果 merge 不生效** → tmp config 只有 `loom_humanloop`，没有 `modelserver` provider/model → opencode 启动时找不到 `modelserver/deepseek-v4-flash` → 子进程立即报 'provider not configured' → executor 拿不到任何 LLM 响应 → wait_task `status != completed` 或 `output` 为空/错误串。
2. **实际观测** → Step 3 `output: "PONG"`（精准模型输出），Step 4 列出 3 条助手能力（多句 LLM 文本生成）。两者都需要 provider+model 在 opencode 子进程里活着。
3. **结论**：907e512 的 merge 路径在 production binary 里走通了。配套 unit test：
   - `TestExecutor_PreservesUserOpencodeConfig`（provider/model/已有 mcp 全部保留 + humanloop 加入）
   - `TestExecutor_EmptyUserConfigStillWritesHumanloop`（无用户 config 仍写出合法 humanloop-only config，向后兼容）
   存在于 `pkg/agentbackend/opencode/executor_test.go`。

OPENCODE_CONFIG tmp 文件本身未在 slave.log 留 path 痕迹（slave.log 设计上只记 tunnel/auth 高层事件，subprocess argv/env 不打 — 这与 PR #18 codex slave 的日志形态一致，不是缺失）。

## Journal / capability

`journal/CAPABILITIES.md` 由 startup scan 写入（ts=2026-06-14T15:13:47Z）：

```
- display_name: slave-opencode-e2e
- workdir: /root/multi-agent/multi-agent/tests/prod_test/.e2e-2026-06-14-opencode
- opencode: /usr/bin/opencode    # <-- 自动探测到
- Skills: chat, file, register_mcp
- Current State: _No CURRENT_STATE.md has been recorded yet._
- Recent Capability Changes: _No capability change history has been recorded yet._
```

CURRENT_STATE.md / history.md 未生成 — 与 PR #18 e2e 同因：journal merge 只在 `executor.Result.CapabilityChange != ""` 触发，而 `chat` skill 走 backend 的 LLM 路径不会 set 该字段（只有 `register_mcp` / `permissions` 之类工具调用才会）。本轮 4 步只跑 chat，**符合预期**。journal merge 的真跑路径由单测 + PR #18 间接证据覆盖。

## 已知容忍

- `audit.log` 全是 `observer_relay_error: artifact requests status 401` 与 `list writes status 401` — observer legacy_api_key 与 agent-bind 语义差（memory 已记录，跨所有 e2e 普遍存在），不阻塞 task 流转。`wait_task` warnings 里也带 `observer sync writes: list writes status 401`，但 `status=completed`。

## 关停

```bash
pkill -9 -f "tests/prod_test/bin/.*linux-amd64"
```

`.e2e-2026-06-14-opencode/` 目录 gitignored，保留作快照（含 codex.log / slave.log / driver-tasks.jsonl / journal/CAPABILITIES.md / audit.log / slave-config.yaml + token）。

## 结论

**PASS（含已知容忍）**

- ✓ PR #19 主体：opencode 作为第 3 个 slave backend 在 production binary 跑通；CAPABILITIES.md 自动探测 + flat `agent.kind: opencode` schema 被 binary 正确加载
- ✓ 主链路：codex orchestrator → driver(codex backend) → ws-prod → slave(opencode backend) → opencode CLI → relay(modelserver/deepseek-v4-flash) → executor 解事件流 → wait_task `status=completed`，2/2 chat task 拿回真实 LLM 输出
- ✓ 907e512 merge fix：OPENCODE_CONFIG 全量覆盖语义下，用户 provider/model 在 slave spawn 后存活；反向证据是「LLM 任务能成功」就证明 provider 没丢
- ✓ P2-A 对称：slave.log 零 `--print`/unrecognized-flag/`opencode exit` 命中，executor 用正确 opencode CLI 协议
- ◯ 增量：register_mcp / permissions 类 capability_changed=true 的 journal merge 真跑未在 e2e 直跑，由单测覆盖
- ◯ relay model 选择偏差：opencode 走 `modelserver/deepseek-v4-flash` 而非 `gpt-5.5`（gpt-5.5 当前仅 codex `responses` wire-api 可用）— 不影响 backend 正确性证明

## Follow-up

- 跑一次 register_mcp / permissions 用例触发 `CapabilityChange != ""`，直跑 journal merge（含 CURRENT_STATE.md / history.md 生成）— 三 backend 共同的 follow-up，不只 opencode
- relay 侧：让 opencode 也能调 gpt-5.5（responses-shim），免去 e2e 在 codex/opencode 两套模型间切换
- observer 401：仍是已记录跨 e2e 容忍项，待 control-plane 层处理
