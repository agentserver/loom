# Agent backend 注册抽象 + 配置归一 — 设计文档

- **日期**：2026-06-14
- **来源**：[issue #15](https://github.com/agentserver/loom/issues/15) + 用户请求「未来 driver 和 slave 应该与特定的 agent 解耦」
- **范围**：把 `cfg.Claude.*` / `cfg.Codex.*` 归一到 `cfg.Agent.*`；在 `pkg/agentbackend/` 加 `Register`/`New` 注册中心；驱逐 main.go 的 `switch cfg.Agent.Kind`；顺手修 3 个 bug
- **不在范围**：真接入 opencode（铺路，后续 PR 只加一个 `pkg/agentbackend/opencode/` + 两行 import）；master 路径（[[master_path_frozen]]）；`agentbackend.Backend` 接口方法签名（兼容现有 claude/codex 实现）

## 背景

当前 `pkg/agentbackend/` 那一层 interface 已经干净（`Backend` / `LLMRunner` / `PermissionsStore`），claude 和 codex 都各自实现完整。但**下面**（driver/slave 的 config）和**上面**（CLI 入口 + main.go switch）耦合到具体 backend：

- `internal/{driver,config}/config.go` 各自有 `ClaudeConfig` + `CodexConfig` 并列 struct，互相 mirror workdir，loader 多处 `switch cfg.Agent.Kind`
- `cmd/{driver,slave}-agent/main.go` 直接读 `cfg.Claude.Bin` / `cfg.Codex.Bin` 构造 `agentbackend.Config`
- 加新 backend（如 opencode）要改 12-14 个文件，配置 struct 重复写

上一轮 PR #14 P1 加的 `driverDefaultWorkDir(c)` / config mirror（claude↔codex 互填）是 band-aid——根治在统一字段。同时勘探发现 3 个隐藏 bug 顺手修：

1. `cmd/slave-agent/capabilities.go:30,33` bash/powershell executor 硬编码 `cfg.Claude.WorkDir`——codex agent 配 codex.workdir 时被忽略
2. `cmd/slave-agent/main.go:167` `journal.New({ClaudeBin: cfg.Claude.Bin})` ——codex slave 上调用根本不存在的 claude 二进制
3. issue #15 原本主题（agent.workdir 散乱 + silent fallback to cwd）

## 目标 schema

YAML 顶层 `agent` 段成为**唯一**的 per-backend 字段来源：

```yaml
agent:
  kind: claude           # required, no default
  workdir: /loom/proj    # required, no fallback
  bin: claude            # optional, factory 默认值
  extra_args: []         # optional
```

旧字段 `claude:` / `codex:` 顶层段**删除**。`yaml.DisallowUnknownFields()` 已开，旧 YAML 自动报错；LoadConfig 顶部 peek raw bytes，遇到 `claude:` / `codex:` legacy key 时报错文案明示「移到 agent 段，见 docs/migration/2026-06-agent-config.md」。

## 目标不变量

1. **配置归一**：driver/slave config 里不再有 `Claude` / `Codex` 段；只有 `Agent{Kind, Bin, WorkDir, ExtraArgs}`。
2. **必填**：`agent.kind` 和 `agent.workdir` 必填；缺一报错。`agent.bin` 可空，由 factory 提供默认（claude→"claude", codex→"codex"）。
3. **kind 白名单**：`Agent.Kind` 必须在 `agentbackend.RegisteredKinds()` 里；不在则报错列出已注册集合。
4. **注册中心**：`pkg/agentbackend/registry.go` 提供 `Register(kind, factory)` / `New(cfg, hooks)`；claude 和 codex 在各自包的 `init()` 自注册；CLI 入口 `import _ "...claude"` `import _ "...codex"`。
5. **加新 backend 只动一个包 + 两行 import**：新 backend 只在 `pkg/agentbackend/<name>/` 写代码 + 在两个 main.go 里各加一行 `import _`。无需改 config struct、无需改 main.go 任何 switch。
6. **顺手修 3 bug**：bash/powershell workdir、journal bin、agent.workdir 散乱（PR #14 band-aid）全部用统一字段消除。
7. **Master 冻结**：`cmd/master-agent/` / `internal/orchestrator/` / `internal/orchestration/` 一行不动。

## 变更摘要

### 1. `pkg/agentbackend/registry.go`（新）

```go
// AgentConfig 是 driver/slave config agent 段的镜像，
// pkg/agentbackend 不依赖任何 internal/* 类型，避免循环导入。
type AgentConfig struct {
    Kind      string
    Bin       string
    WorkDir   string
    ExtraArgs []string
}

// Hooks 收容 backend 需要的回调（journal、humanloop 目录等），
// 避免每个 factory 签名扩参数。
type Hooks struct {
    JournalAppend func(JournalEntry)  // backend 决定是否调用；nil = 不记
    HumanloopDir  string              // 可选，仅 claude humanloop IPC 用
}

type Factory func(cfg AgentConfig, hooks Hooks) (Backend, error)

var (
    registryMu sync.Mutex
    registry   = map[Kind]Factory{}
)

// Register 安装 backend factory。常规在 backend 包的 init() 中调用。
// 重复注册同一 kind 直接 panic（init 顺序错乱的早期发现）。
func Register(kind Kind, f Factory) {
    registryMu.Lock()
    defer registryMu.Unlock()
    if _, dup := registry[kind]; dup {
        panic("agentbackend: duplicate Register " + string(kind))
    }
    registry[kind] = f
}

// New 按 cfg.Kind 查表构造 Backend。unknown kind 报错并列出已注册集合，
// 方便操作员看 "我应该 import 哪个包" 而非黑盒 unknown。
func New(cfg AgentConfig, hooks Hooks) (Backend, error) {
    registryMu.Lock()
    f, ok := registry[Kind(cfg.Kind)]
    kinds := registeredKindsLocked()
    registryMu.Unlock()
    if !ok {
        return nil, fmt.Errorf("agentbackend: unknown kind %q; registered: %v", cfg.Kind, kinds)
    }
    return f(cfg, hooks)
}

func RegisteredKinds() []string {
    registryMu.Lock()
    defer registryMu.Unlock()
    return registeredKindsLocked()
}
```

`JournalEntry` 形态：现有 `internal/journal` 包对外暴露的 entry struct——backend 包不依赖 internal，所以这个类型搬进 `pkg/agentbackend/`，`internal/journal` 改为 `pkg/agentbackend` 的消费者（它本身不关心 backend，只是 backend 把日志附加事件回传给 host）。

### 2. `pkg/agentbackend/{claude,codex}/backend.go` 末尾加 `init()`

```go
// claude/backend.go
func init() {
    agentbackend.Register(agentbackend.KindClaude, factory)
}

func factory(cfg agentbackend.AgentConfig, h agentbackend.Hooks) (agentbackend.Backend, error) {
    bin := cfg.Bin
    if bin == "" {
        bin = "claude"
    }
    return New(InternalConfig{
        Bin:          bin,
        WorkDir:      cfg.WorkDir,
        ExtraArgs:    cfg.ExtraArgs,
        HumanloopDir: h.HumanloopDir,
        JournalHook:  h.JournalAppend,
    }), nil
}
```

`codex/backend.go` 类似，默认 bin = `"codex"`，HumanloopDir 不使用。

### 3. `internal/driver/config.go`

**删**：
- `type ClaudeConfig struct{...}` + `type CodexConfig struct{...}`
- `Config.Claude` / `Config.Codex` 字段
- LoadConfig 的 `if c.Codex.WorkDir == "" { c.Codex.WorkDir = c.Claude.WorkDir }` mirror
- LoadConfig 的 `if c.Claude.WorkDir == "" { c.Claude.WorkDir = c.Codex.WorkDir }` reverse mirror（PR #14 加的）
- `driverDefaultWorkDir(c)` helper（PR #14 加的）
- `DriverDefaults.WorkDir` 字段的「fallback claude/codex.workdir」逻辑
- `Planner.Bin` 的 `switch c.Agent.Kind` 默认值（改为统一默认 `c.Agent.Bin`，空则报错）

**加**：
- `type AgentConfig struct { Kind, Bin, WorkDir string; ExtraArgs []string }`
- `Config.Agent AgentConfig` 字段（YAML tag `agent`）
- LoadConfig 必填校验：`Agent.Kind != ""`、`Agent.WorkDir != ""`、`agentbackend.IsRegistered(Agent.Kind)`
- Legacy key 检测（详细见下）
- `DriverDefaults.WorkDir` 仍保留作为 jail 边界字段，默认值改成 `c.Agent.WorkDir`（之前 `driverDefaultWorkDir` 的 cwd fallback 删除——必填强制下无意义）

**Legacy key peek**（顶部 yaml.Decode 之前）：

```go
// Detect pre-unify YAML keys and report friendly migration error.
// DisallowUnknownFields would report them eventually but the message
// "unknown field \"claude\" in type driver.Config" is hard to act on.
type rawProbe struct {
    Claude any `yaml:"claude"`
    Codex  any `yaml:"codex"`
}
var probe rawProbe
if err := yaml.Unmarshal(raw, &probe); err == nil {
    var legacy []string
    if probe.Claude != nil { legacy = append(legacy, "claude") }
    if probe.Codex != nil { legacy = append(legacy, "codex") }
    if len(legacy) > 0 {
        return nil, fmt.Errorf("config %s: legacy top-level keys %v are no longer supported; consolidate into agent: { kind, bin, workdir, extra_args }. See docs/migration/2026-06-agent-config.md", path, legacy)
    }
}
```

### 4. `internal/config/config.go`（slave）

同 driver：删 `Claude` / `Codex` 段、加 `Agent` 段、legacy peek、必填校验。

### 5. `cmd/driver-agent/main.go`

**前**：
```go
backend, err := agentbackend.New(agentbackend.Config{
    Kind:    cfg.Agent.Kind,
    Claude:  agentbackend.ClaudeConfig{Bin: cfg.Claude.Bin, WorkDir: cfg.Claude.WorkDir, Args: cfg.Claude.Args},
    Codex:   agentbackend.CodexConfig{...},
})
```

**后**：
```go
import (
    _ "github.com/yourorg/multi-agent/pkg/agentbackend/claude"
    _ "github.com/yourorg/multi-agent/pkg/agentbackend/codex"
    // 未来加 opencode：_ "github.com/yourorg/multi-agent/pkg/agentbackend/opencode"
)
// ...
backend, err := agentbackend.New(agentbackend.AgentConfig{
    Kind:      cfg.Agent.Kind,
    Bin:       cfg.Agent.Bin,
    WorkDir:   cfg.Agent.WorkDir,
    ExtraArgs: cfg.Agent.ExtraArgs,
}, agentbackend.Hooks{
    HumanloopDir: filepath.Join(cfg.Agent.WorkDir, "humanloop"),
})
```

所有 `cfg.Claude.*` / `cfg.Codex.*` 引用一次性消失。

### 6. `cmd/slave-agent/main.go` + `capabilities.go`

同上 + 顺手修 bug：

- `main.go:167` `journal.New(journal.Config{ClaudeBin: cfg.Claude.Bin})` → `journal.New(journal.Config{AgentBin: cfg.Agent.Bin})`；`internal/journal/journal.go` 的 `Config` 字段重命名 + 内部使用点改名
- `main.go:254-256` `if cfg.Agent.Kind == "codex" { fileWorkDir = cfg.Codex.WorkDir } else { ... }` switch 整段删除——用 `cfg.Agent.WorkDir`
- `capabilities.go:30,33` bash / powershell executor 的 `cfg.Claude.WorkDir` → `cfg.Agent.WorkDir`

### 7. 部署模板 + 安装脚本

| 文件 | 改动 |
|---|---|
| `deploy/{linux,windows}/{driver,slave}/config.yaml.template` | 删 `claude:` / `codex:` 段；加 `agent: { kind: __AGENT_KIND__, bin: __AGENT_BIN__, workdir: __PROJECT_DIR__, extra_args: [] }` |
| `deploy/linux/{driver,slave}/install.sh` | 删 `AGENT_BLOCK` 多分支拼接的 python3 替换；用 sed 一次性渲染 `__AGENT_KIND__` / `__AGENT_BIN__` |
| `deploy/windows/{driver,slave}/install.ps1` | 同 PowerShell Replace 一次性渲染 |
| `deploy/linux/slave/install.sh:141` | `if [[ "$AGENT" == "claude" ]]; then AGENT_BLOCK=...` 整段删除——`AGENT` 和 `BIN` 一起塞模板替换 |

模板/脚本里 `__PROJECT_DIR__` 替换沿用（PR #14 P1-followup 已修正 linux installer 漏的 sed pass）。

### 8. 迁移文档

新文件 `docs/migration/2026-06-agent-config.md`：

```markdown
# Migration: agent config unification (2026-06)

Before:
\`\`\`yaml
agent:
  kind: claude
claude:
  bin: claude
  workdir: /loom/proj
  extra_args: []
\`\`\`

After:
\`\`\`yaml
agent:
  kind: claude
  bin: claude        # optional, defaults per backend
  workdir: /loom/proj
  extra_args: []
\`\`\`

For codex: `agent.kind: codex`, `agent.bin: codex`.

Driver and slave both follow this schema. Master config schema unchanged
(separate workstream; see [[master_path_frozen]]).

Tools that ship pre-baked YAML (deploy/{linux,windows}/{driver,slave}/install.*)
already rendered the new schema. Operator-edited YAMLs must be migrated manually;
the loader reports a friendly error pointing here.
```

## 测试覆盖

新 / 改：

| # | 测试 | 文件 | 覆盖 |
|---|---|---|---|
| 1 | `TestRegister_PanicsOnDuplicateKind` | `pkg/agentbackend/registry_test.go` | 注册中心去重 |
| 2 | `TestNew_UnknownKindLists Registered` | 同 | 错误文案包含已注册集 |
| 3 | `TestNew_DispatchesToFactory` | 同 | factory 真被调，cfg 透传 |
| 4 | `TestClaudeBackend_AutoRegisters` | `pkg/agentbackend/claude/init_test.go` | import 后 `RegisteredKinds()` 含 claude |
| 5 | `TestCodexBackend_AutoRegisters` | `pkg/agentbackend/codex/init_test.go` | 同 codex |
| 6 | `TestLoadConfig_RequiresAgentKind` | `internal/driver/config_test.go` | 缺 kind 报错 |
| 7 | `TestLoadConfig_RequiresAgentWorkDir` | 同 | 缺 workdir 报错 |
| 8 | `TestLoadConfig_RejectsUnknownAgentKind` | 同 | unknown kind 报错文案含已注册集 |
| 9 | `TestLoadConfig_RejectsLegacyClaudeKey` | 同 | 旧 YAML 有 `claude:` → friendly error 指 migration |
| 10 | `TestLoadConfig_RejectsLegacyCodexKey` | 同 | 旧 YAML 有 `codex:` → 同 |
| 11 | `TestLoadConfig_AgentBinDefaultsViaFactory` | 同 | `agent.bin` 留空时 Backend.New 拿到 factory 默认 |
| 12 | `TestSlaveConfig_*`（同 6-11 一套）| `internal/config/config_test.go` | slave 侧 |
| 13 | `TestSlaveBashUsesAgentWorkDir` | `cmd/slave-agent/capabilities_test.go`（新增）| Bug 修正：bash workdir = agent.workdir |
| 14 | `TestSlavePowerShellUsesAgentWorkDir` | 同 | 同 powershell |
| 15 | `TestJournalConfig_AgentBinFieldName` | `internal/journal/journal_test.go` | 字段重命名 + agent.kind=codex 时 bin 是 codex 而非 claude |

回归：`go test ./... -race -count=1` 全过。

### 既有测试调整

所有引用 `cfg.Claude.*` / `cfg.Codex.*` 的测试 fixture / fake 全部改为 `cfg.Agent.*`。PR #14 加的 `TestLoadConfig_DriverDefaultsWorkDir*` 系列要么删要么改写——`driverDefaultWorkDir` 不存在了，校验逻辑由「必填 + factory 默认」承担。

prod_test e2e config（`tests/prod_test/.e2e-*/`）的 YAML 全改——`.gitignore` 内的 e2e 工作目录不算源码，但 runbook 文档里的示例 YAML 改。

## 兼容性

| 变更 | 影响 / 缓解 |
|---|---|
| 删 `claude:` / `codex:` 顶层段 | 旧 YAML 立即失败；loader 报 friendly migration error 指向文档 |
| `agent.kind` / `agent.workdir` 必填 | 老 YAML 即使只用 `claude:` 段也包含这两个（PR #14 模板已渲染）；脱手编辑 YAML 的用户必须显式补 |
| `Planner.Bin` 默认行为变 | 默认 = `Agent.Bin`（如果都为空且 kind 不带默认，报错——这种情况不存在，因 factory 必有 fallback） |
| `DriverDefaults.WorkDir` 不再 fallback cwd | PR #14 P1 加的兜底删除；workdir 必填后这条 fallback 永远不会触发；删了更干净 |
| `journal.Config.ClaudeBin` 字段重命名为 `AgentBin` | 调用方只在 slave-agent 内部，无外部消费者 |
| 模板 + 安装脚本 | 同 PR 改全；生产部署下一次 install 即自然走新 schema |
| Master config | 一行不动；master 自己的 `cmd/master-agent/config.go` 仍有 `Claude` 段，单独 issue（[[master_path_frozen]]）跟进 |

## 决策记录

| 决策 | 备选 | 选择理由 |
|---|---|---|
| `init()` 自注册 | 显式注册表 / 反射构造 | Go 标准模式（database/sql、image/png）；加 backend = 新包 + 两行 import；test 文件可单独验证「import 后注册成功」 |
| Factory 只拿 `AgentConfig + Hooks` | 拿全 Config / 拿 backend-specific extra map | YAGNI——目前 backend 不需要 deploy-specific 配置；未来如果需要再加 `extra map[string]any` 字段，不破坏现有 factory |
| `agent.kind` 强制显式 | 默认 claude | "破坏迁移"口径一致；隐式默认是技术债 |
| `agent.workdir` 必填不 fallback | fallback cwd | 同上；fallback cwd 是安全维度的隐藏行为，PR #14 加它是 band-aid |
| 旧 `claude:` / `codex:` legacy peek + friendly error | 仅依赖 DisallowUnknownFields | DisallowUnknownFields 报错文案太抽象（"unknown field 'claude' in type driver.Config"），操作员看不出迁移路径 |
| `JournalEntry` 类型搬进 `pkg/agentbackend/` | backend 包 import internal/journal | pkg/* 不能依赖 internal/*；类型搬迁是 1-line 改 |
| 不真接入 opencode | 一并接 | 真接入需要调研 opencode CLI 行为（session / humanloop / streaming），范围爆炸；本 PR 把"加 backend"的成本降到「新包 + 两行 import」就够交付价值了 |

## 反目标 / 反范围

- 不真接入 opencode backend
- 不动 master 路径（`cmd/master-agent/`、`internal/orchestrator/`、`internal/orchestration/`）
- 不动 `agentbackend.Backend` interface 方法签名
- 不引入新依赖（全 stdlib + 现有 yaml.v3）
- 不重命名 `Backend` / `LLMRunner` / `PermissionsStore` interface
- 不动 prod_test e2e runbook 流程（只改 runbook 里的示例 YAML）
