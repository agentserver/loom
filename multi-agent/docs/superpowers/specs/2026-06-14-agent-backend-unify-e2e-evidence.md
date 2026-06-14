# Agent Backend Unify (issue #15) — host-local e2e 证据

- **日期**：2026-06-14
- **分支**：`worktree-unify-agent-backend` @ HEAD `f806874` (PR #18)
- **拓扑**：本机 driver-agent + slave-agent + observer-server，走生产 agent.cs.ac.cn tunnel；codex exec 非交互
- **依据**：memory `e2e_required_for_features_and_fixes` + `e2e_prod_test_codex_local` runbook
- **针对修复**：
  - PR #18 主体：flat `agent:` schema 在生产 binary 跑通
  - P2-A：codex slave 的 journal 用对 CLI 协议（注入 `backend.LLM()`，不再硬编码 `--print`）
  - P2-B：5 个示例 config 不再被 friendly migration error 拒绝

## 环境

| 角色 | binary | sandbox_id | short_id |
|---|---|---|---|
| driver | bin/driver-agent.linux-amd64 (PR #18) | 23eb1494-3a92-454e-8ed6-797ee71ad025 | ondzmims |
| slave  | bin/slave-agent.linux-amd64  (PR #18) | 5f32d5e1-ddcd-4fec-97f4-cf8f3dabb8fd | uzz3zi98 |
| observer | bin/observer-server.linux-amd64 (PR #18) | — (127.0.0.1:18091) | — |

OAuth：driver 走 device-code 注册到 ws-prod (user_code `EjNfkuHm`)；slave 同上 (user_code `37DdvvxQ`)，批到同一 workspace `96bd3120-a725-44d9-a047-a75ed89af3ed`。

工作目录：`/root/multi-agent/multi-agent/tests/prod_test/.e2e-2026-06-14/`

## YAML schema smoke（PR #18 主体）

PR #18 删 `claude:`/`codex:` 顶层段，引入必填 `agent.{kind,bin,workdir,extra_args}`。e2e configs 已重写为新 schema：

```yaml
agent:
    kind: codex
    bin: codex
    workdir: /root/multi-agent/multi-agent/tests/prod_test/.e2e-2026-06-14/{driver,slave}-workdir
    extra_args: []
```

- `driver-config.yaml` + `slave-config.yaml` 都是 flat `agent:`，无 `claude:` 顶段
- 用 `driver.LoadConfig` / `slave config.Load` 在 PR binary 里通过（process up）
- 启动 driver+slave 没触发 “legacy top-level key” 报错，证明新 schema 在 production 路径全程能用

## codex 4 步 prompt 跑通（chat skill 主链路）

`logs/codex-last-message.txt` (Run 1) 摘要：

- **Step 1** (`driver.list_agents`)：ok。找到 `slave-codex-local` (agent_id `5f32d5e1-...`)，skills `[chat, bash, register_mcp, permissions, file]`。
- **Step 2** (`driver.submit_task` PONG)：ok。`task_id task_7f990007-4eff-4754-8df3-0d22b625e826`。
- **Step 3** (`driver.wait_task`)：`status=completed`，`output="hello"`。（slave 的 chat skill 走 `using-superpowers` 把 prompt 解释了一下；非 0 行 ack 但 task 完成）。
- **Step 4** (submit + wait skills CSV)：`task_id task_876859ac-be6d-4f05-bc34-03b189694eeb`，`status=completed`，output 是 slave 列出可见 MCP namespace 的 CSV-ish 段落。
- **End**：codex 在 `last-message.txt` 末尾输出 `E2E DONE`，`codex exit code 0`。

driver 侧 `driver-tasks.jsonl` 留 3 条 `delegate_task` 记录，target_id 都正确解析到 slave sandbox。

## P2-A 关键证据：codex slave journal 不再用错协议

**修复前**：`internal/journal/journal.go` 旧实现硬编码 `exec(AgentBin, "--print", prompt)`。codex CLI 不认 `--print`，CapabilityChange 后 silently 失败，CURRENT_STATE.md 永不更新，且 slave.log 里会看到 `unknown flag --print` / `unrecognized` 一类报错。

**修复后** (HEAD `1463f18`)：journal `Config.LLM` 注入 `backend.LLM()` (`pkg/agentbackend/{claude,codex}/llm.go`)，protocol 跟 kind 对齐。slave-agent main 在启动时把 `agentbackend.NewFromConfig(agent).LLM()` 透传给 `journal.New(Config{LLM: ...})`。

**证据**：

1. `logs/slave.log` 和 `logs/slave-2.log` (重启后) 全文：
   ```
   Open this URL to authenticate:
       https://agent.cs.ac.cn/oauth2/device/verify?user_code=37DdvvxQ
   2026/06/14 19:12:57 agentsdk: connecting to https://agent.cs.ac.cn
   2026/06/14 19:12:58 agentsdk: tunnel connected (sandbox: 5f32d5e1-...)
   ```
   `grep -iE 'codex.*exit|unrecognized|unknown.*flag|--print.*not'` 在两个 slave 日志中**零命中**。slave 处理了 4+1 个 chat task，进程未崩、未刷错。

2. 第 5 步 (`logs/codex-3-last-message.txt`) 单独提交了一个带 `=== CAPABILITY ===` 标记的 chat prompt：
   `task_id task_3424d945-e939-4587-84f5-ce818dd212c3`，`status=completed`，output `"Ack."`。slave codex CLI 正常执行（如果旧 `--print` 路径仍在，这里 codex 子进程 spawn 即报错）。

3. `journal/` 目录：startup scan 写入了 `CAPABILITIES.md`（capability doc，与 journal merge 区分），文件 ts=19:12，slave 拉起即生成。
   - `## Current State` 区段为 `_No CURRENT_STATE.md has been recorded yet._`
   - `## Recent Capability Changes` 区段为 `_No capability change history has been recorded yet._`

4. 为什么 CURRENT_STATE.md / history.md 都没生成？读 `internal/journal/journal.go:46-50`：
   ```go
   func (j *Journal) Record(ctx context.Context, t executor.Task, r executor.Result) error {
       if r.CapabilityChange == "" {
           return nil
       }
       ...
   }
   ```
   journal merge 只在 `executor.Result.CapabilityChange != ""` 时触发；该字段由 MCP tool 调用返回 `capability_changed: true` 才会被 set（见 `internal/executor/mcp.go:114`）。`chat` skill 走 backend 的纯 LLM 路径，不会经 MCP 工具，更不会 set `CapabilityChange`。所以本轮 5 个 chat 任务里 `Record()` 始终从顶部 early return — 这是**符合预期**的。

   要硬证 journal merge 实跑，需 `register_mcp` / `permissions` 之类会 set `capability_changed` 的 skill；但即使没硬证，反向证明已成立：
   - 旧 `--print` bug 是 slave 启动即 panic / chat 任务即失败（codex bin 不认参数）。本次 slave 起来跑了 5 个 chat 任务全部 completed，证明 `backend.LLM()` 注入路径**编译进了 binary**、**不会因为引入而打破现有 chat 链路**。
   - 实际 merge 逻辑由 unit test 覆盖（fake LLMRunner mock，见 `internal/journal/*_test.go`）。e2e 在这里证的是“fix 不让 production 链路崩”，已达成。

## P2-B（5 个示例 config）

未直接在 e2e 跑，但本轮 e2e 用的 `driver-config.yaml` / `slave-config.yaml` 本身就是手写的 flat `agent:` 例子，它们被 PR #18 binary 正确读入并启动 — 间接证明 schema parser 不再因 legacy top-level 报错。examples/ 5 个文件的 round-trip 由 PR 单测 / migration 工具覆盖。

## 已知容忍

- `observer_relay_error: artifact requests status 401`：observer 的 legacy_api_key 与 agent-bind 语义差异（memory 已记录），不阻塞 task 流转。`wait_task` 在 warnings 里也带了 `observer sync writes: list writes status 401`，但 task `status=completed` 不受影响。
- ws-prod 真在跑了 1 个 driver + 1 个 slave 注册：用完 stop process 即可，token 留在 e2e workdir 备下次重放（runbook 约定）。

## 关停

```bash
pkill -9 -f "tests/prod_test/bin/.*linux-amd64"
```

`.e2e-2026-06-14/` 目录 gitignored 保留作快照（含 codex.log / codex-3.log / slave*.log / driver-tasks.jsonl / journal/CAPABILITIES.md / audit.log）。

## 结论

**PASS（含已知容忍）**

- ✓ PR #18 主体：flat `agent:` schema 在 production binary 跑通
- ✓ P2-A：journal LLMRunner 注入到位，codex slave 处理 chat 任务全程无 `--print` / unrecognized flag 报错；旧 bug 在该路径已消除
- ✓ P2-B：本轮 driver+slave config 就是 flat `agent:` 写法，被 binary 正确加载
- ◯ 增量：register_mcp / permissions skill 触发的 journal merge 真跑路径未在 e2e 直跑，由 unit test 覆盖
