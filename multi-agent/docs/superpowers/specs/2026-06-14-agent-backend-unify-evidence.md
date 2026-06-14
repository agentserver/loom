# Agent Backend Unify §issue-15 — 测试证据

- **日期**：2026-06-14
- **分支**：`worktree-unify-agent-backend` @ HEAD `6ec6076` (pre-docs commit; docs commit appended in 8.6)
- **范围**：[issue #15](https://github.com/agentserver/loom/issues/15) — agent config split-brain 统一 + 3 个 surfacing bug
- **不在范围**：
  - 真接入 opencode backend（铺好路即可；真接入需调研 opencode CLI session/streaming）
  - master config schema 自己的归一（master-path 冻结，follow-up issue）
  - `pkg/agentbackend.Backend` 接口方法签名（未变）

## 为什么 unit + race + 二进制 sanity 足够

这次是纯**配置重构 + 注册中心**：
- `pkg/agentbackend` flatten + 注册 — 直接 unit 测试覆盖
- driver/slave config loader 验证 - 7 + 7 个 adversarial unit 测试（必填、未知 kind、legacy key 包括 bare 形式、happy path）
- bash/powershell workdir bug fix — 直接 unit 测试 `bashExecutorWorkDir` / `powerShellExecutorWorkDir`
- journal bug fix — field rename + caller pick by kind，单元测试覆盖
- 注册中心 — `init()` 自注册测试 + `RegisteredKinds()` 测试
- 模板 + installer — 本地 smoke render（claude + codex）

没有跨服务 / transport 变化，所以不需要 host-local e2e。

## 提交序列

`git log --oneline master..HEAD`：

```
6ec6076 deploy: templates + installers emit unified agent: block (issue #15 Task 7)
a18783a fix(slave-agent,capabilitydoc): extract workdir helpers + refresh stale comment (issue #15 Tasks 5+6)
850a34f refactor(slave config): flatten to agent.* + legacy peek (issue #15 Task 4)
96dd981 refactor(driver): flatten config to agent.* + legacy peek (issue #15 Task 3)
03c1cb4 fix(journal): rename ClaudeBin to AgentBin and pick by kind (issue #15 Task 2 + bug)
9bcc7e6 refactor(agentbackend): flatten Config + require explicit kind (issue #15 Task 1)
aa84d39 docs: implementation plan for agent backend unify (issue #15)
9a8cb20 docs: spec for agent backend unify (issue #15)
```

## 任务覆盖

| Task | Commit | 内容 |
|---|---|---|
| 1 | `9bcc7e6` | flatten agentbackend.Config (Kind, Bin, WorkDir, ExtraArgs); RegisteredKinds; deprecated aliases for master/orchestrator |
| 2 | `03c1cb4` | journal.Config.ClaudeBin → AgentBin (bug fix: codex slave silently called missing claude binary) |
| 3 | `96dd981` | internal/driver/config.go flatten + legacy peek (yaml.Node Kind check covers bare claude:) + required validation; delete PR #14 P1 band-aid (driverDefaultWorkDir + bidirectional WorkDir mirror) |
| 4 | `850a34f` | internal/config/config.go (slave) flatten — same pattern; cascades absorbed most of Task 5/6 |
| 5+6 | `a18783a` | bashExecutorWorkDir / powerShellExecutorWorkDir helper extraction + 2 unit tests; capabilitydoc stale comment refresh |
| 7 | `6ec6076` | deploy templates + install.sh + install.ps1 + bootstrap.sh — single agent: block; __AGENT_KIND__/__AGENT_BIN__ substitution |
| 8 | (this) | migration doc + evidence + PR |

## 测试

**关键 packages**：
- `pkg/agentbackend` — factory tests (Required/UnknownKind/DispatchClaude/DispatchCodex/RegisteredKinds)
- `internal/driver` — LoadConfig tests (Requires kind+workdir, RejectsUnknown, RejectsLegacyClaude/Codex/BareClaude, HappyPath)
- `internal/config` — TestSlaveLoad_* tests (同上)
- `internal/journal` — AgentBin field rename, both pre-existing tests cover the bin selection
- `cmd/slave-agent` — TestBashExecutorUsesAgentWorkDir + TestPowerShellExecutorUsesAgentWorkDir

**全仓 race**: `go test ./... -race -count=1` — 全过（含 cmd/driver-agent, cmd/slave-agent, internal/{driver,config,journal,capabilitydoc,orchestrator,orchestration,planner,executor,…}, pkg/agentbackend/{claude,codex,…} 等 50+ 包，无 FAIL/ERROR）。

## 二进制 sanity

```bash
$ go build -o /tmp/bin-smoke-15/driver-agent ./cmd/driver-agent
$ go build -o /tmp/bin-smoke-15/slave-agent  ./cmd/slave-agent
$ /tmp/bin-smoke-15/driver-agent
driver-agent — bridges Claude Code to the multi-agent workspace.

Usage:
  driver-agent register   --config /path/to/driver.yaml
  driver-agent serve-mcp  --config /path/to/driver.yaml
```

两个二进制 clean build，help text 正常输出。

## Master-path freeze

```
$ git diff --stat master..HEAD -- 'cmd/master-agent/**' 'internal/orchestrator/**' 'internal/orchestration/**'
 multi-agent/cmd/master-agent/config.example.yaml |  4 +++-
 multi-agent/cmd/master-agent/main.go             | 10 +++++++---
 2 files changed, 10 insertions(+), 4 deletions(-)
```

Master 的 main.go 是 Task 1 的 interim cascade（compile-required after agentbackend.Config flatten） + Task 4 简化（cfg.Claude/cfg.Codex 删除后 switch 自然消失），shape-only no semantic change。config.example.yaml 是 fixture cascade 让 `TestDistributedComposeExampleConfigsLoad` 继续过。**没有 `internal/orchestrator/**` 或 `internal/orchestration/**` 改动** — 与 memory `master_path_frozen` 一致。

orchestrator/planner 的测试通过 `type ClaudeConfig = Config` / `type CodexConfig = Config` deprecated 别名透明兼容（Task 1），所以 `internal/orchestrator/` 不需要任何编译期修改。

## 3 个 surfacing bug 修复

1. **journal codex slave 调用 missing claude binary** — `journal.ClaudeBin` → `AgentBin` + caller pick by kind（Task 2，pinned by 重命名后既有测试）
2. **bash/powershell workdir 硬编码 cfg.Claude.WorkDir** — codex slave shell 能力在错误目录运行 — 改 `cfg.Agent.WorkDir` + 2 个新测试（Task 4 cascade + Task 5 助手提取）
3. **agent.workdir 散乱 / silent cwd fallback** — PR #14 P1 加的 `driverDefaultWorkDir` band-aid 删除；现在 `agent.workdir` 必填（Task 3 + 4）

## Follow-up

- master config 归一（master-path 解冻后，issue 单开）
- `internal/{driver,config}.isRegisteredKind` 两处重复（hoist 到 `agentbackend.IsRegisteredKind` 是 minor cleanup）
- 真接入 opencode backend（独立 PR；需调研 opencode CLI session / streaming）
