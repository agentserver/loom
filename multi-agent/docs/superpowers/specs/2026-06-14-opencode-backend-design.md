# opencode backend 接入 — 设计文档

- **日期**：2026-06-14
- **范围**：
  - **slave 侧**：新 `pkg/agentbackend/opencode/` backend 包，让 slave-agent 能起 [anomalyco/opencode](https://github.com/sst/opencode)（原 sst/opencode，TypeScript，npm 包 `opencode-ai`）跑被派发的任务
  - **driver 侧**：新 deploy 模板让 driver-agent serve-mcp 能被 opencode CLI **和** opencode desktop（同一份 `opencode.json`）当 MCP server 消费
- **依据**：PR #18（issue #15）已铺好「加 backend = 新包 + 两行 import」的注册机制
- **不在范围**：
  - master 路径（[[master_path_frozen]]，本 PR 不动）
  - opencode `auth login`（provider API key 由 operator 预先走 `opencode auth login` 配，agent 包不管账号）
  - opencode `acp`（agent client protocol）子命令——`opencode run --format json` 已足够，acp 作为后续优化
  - 真接入 opencode desktop 的 MCP discovery 路径（desktop 与 CLI 共用 `opencode.json`，模板写好就同时生效，不需要 desktop-specific 代码）
  - opencode-specific permissions persistence（NoOp store，跟 codex 同款）

## 背景

PR #18 的核心成就是「加 backend 只需 `pkg/agentbackend/<name>/` 新包 + 两行 `import _`」。这是第一次兑现那个承诺。

### opencode CLI 关键事实（已查文档确认）

- 非交互入口：`opencode run [message..]`（**子命令**，不是 flag）
- 事件流：`--format json` 出 nd-json 事件流（含 session_id / message events）
- Session 续：`--session <id> --continue "answer"`
- Bypass approvals: `--dangerously-skip-permissions`（类比 codex 的 `--dangerously-bypass-approvals-and-sandbox`）
- 工作目录：`--dir <path>`
- MCP 配置文件：`opencode.json` / `opencode.jsonc`，路径由 `OPENCODE_CONFIG` env 覆盖；XDG 默认 `~/.config/opencode/opencode.json`，Windows 默认 `%APPDATA%\opencode\opencode.json`
- MCP 格式：
  ```jsonc
  { "mcp": { "<name>": { "type": "local", "command": ["<bin>", "arg1"], "enabled": true } } }
  ```
- Bin 名：`opencode`（npm 包 `opencode-ai`，brew/curl/scoop 都装成可执行 `opencode`）

### opencode desktop 关键事实

- 与 CLI 同源（`anomalyco/opencode`），mac/Windows/Linux 都有独立桌面包
- **MCP 配置文件位置与 CLI 共用**（同一个 `~/.config/opencode/opencode.json` 或 `OPENCODE_CONFIG`）
- 所以 driver 侧只需要写好这一份 `opencode.json` 模板，CLI 和 desktop 自动都消费

## 目标不变量

修复后下列必须始终成立：

1. **配置 `agent.kind: opencode`** loader 接受，写到 `cfg.Agent.Kind` 后 `agentbackend.New` 路由到 opencode backend
2. **`Bin` 默认 `"opencode"`**——operator 不写 `agent.bin` 时 factory 自动填，跟 claude/codex 同款
3. **slave 上 opencode 起来时默认带 `--dangerously-skip-permissions`**——不需要人机交互；任务本身是其他 agent 派发的，反问权限 = deadlock
4. **Humanloop MCP 注入**——临时写 `opencode.json`（带 `loom_humanloop` server，command = binSelf, args = humanloop endpoint），用 `OPENCODE_CONFIG=<tmp>` env 传给子进程；与 claude（用 `--mcp-config` 路径）和 codex（用 `-c mcp_servers.X.command=` 内联 override）形状不同但语义等价
5. **`backend.LLM().Run()`** 走 `opencode run --format json` + 解事件流取 final assistant text；用于 planner 和 P2-A 后的 journal merge
6. **`backend.RunResume(sessionID, answer)`** 走 `opencode run --session <id> --continue ...`
7. **`backend.Detect()`** 验 `opencode --version` 退 0
8. **driver-agent serve-mcp** 能被 opencode CLI 和 desktop 共同消费（同一份 `opencode.json`）
9. **`install.sh` / `install.ps1` 知道 `AGENT=opencode` 分支**：把 driver `opencode.json` 写到 OS-conventional 路径（Linux: `~/.config/opencode/opencode.json`；Windows: `%APPDATA%\opencode\opencode.json`）

## 变更摘要

### 1. `pkg/agentbackend/opencode/`（新包）

照搬 codex 包形状（最像 opencode，因都是 `<bin> <subcmd> --flag` 模式）。5 个文件：

#### `backend.go`

```go
package opencode

import (
	"context"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

type Backend struct {
	cfg  agentbackend.Config
	exec *executor
	perm *Store
	llm  *llmRunner
}

func New(cfg agentbackend.Config, env []string) *Backend {
	if cfg.Bin == "" {
		cfg.Bin = "opencode"
	}
	return &Backend{
		cfg:  cfg,
		exec: newExecutor(cfg, env),
		perm: NewStore(cfg.WorkDir),
		llm:  newLLM(cfg, env),
	}
}

func (b *Backend) Kind() agentbackend.Kind                                  { return agentbackend.KindOpencode }
func (b *Backend) Run(ctx context.Context, t agentbackend.Task, s agentbackend.Sink) (agentbackend.Result, error) { return b.exec.Run(ctx, t, s) }
func (b *Backend) RunResume(ctx context.Context, sessionID, answer string, s agentbackend.Sink) (agentbackend.Result, error) { return b.exec.RunResume(ctx, sessionID, answer, s) }
func (b *Backend) LLM() agentbackend.LLMRunner                              { return b.llm }
func (b *Backend) Permissions() agentbackend.PermissionsStore               { return b.perm }
func (b *Backend) Detect(ctx context.Context) error                         { return detect(ctx, b.cfg.Bin) }

func init() {
	agentbackend.RegisterBuilder(agentbackend.KindOpencode, func(cfg agentbackend.Config, env []string) (agentbackend.Backend, error) {
		return New(cfg, env), nil
	})
}
```

需要在 `pkg/agentbackend/backend.go` 加 `KindOpencode Kind = "opencode"` 常量。

#### `llm.go`

```go
package opencode

func (r *llmRunner) Run(ctx context.Context, stdinPrompt string) (string, error) {
	// opencode run 默认从 args 读 prompt，但也支持 stdin via "-" 占位符；
	// 优先 stdin 避免命令行长度限制 + escape 烦恼
	args := []string{"run", "--dangerously-skip-permissions", "--format=json", "-"}
	if len(r.cfg.ExtraArgs) > 0 {
		args = append(args, r.cfg.ExtraArgs...)
	}
	cmd := exec.CommandContext(ctx, r.cfg.Bin, args...)
	cmd.Env = append(cmd.Environ(), r.env...)
	cmd.Stdin = strings.NewReader(stdinPrompt)
	out, stderr, err := runCapture(cmd)
	if err != nil {
		return "", fmt.Errorf("opencode llm exit: %v: %s", err, lastN(stderr, 4096))
	}
	// LLMRunner 契约：单次同步返字符串
	// opencode --format json 出事件流，要找 last assistant message
	return extractFinalText(out)
}
```

**待 implementer 确认**：`opencode run --format json` 的具体事件 schema。若 implementer 跑 `opencode run --format json "hello"` 发现 event shape 与 codex 不同（codex 用 `{"type":"item.completed","item":{"type":"agent_message","text":"…"}}`），相应调整 `extractFinalText`。

#### `executor.go`

```go
type executor struct {
	cfg              agentbackend.Config
	env              []string
	binSelf          string // slave-agent binary path; default os.Args[0]
	maxQuestions     int    // default 5
	shutdownGraceSec int    // default 10
	socketHookForTest func(string)
}

func (e *executor) Run(ctx context.Context, t agentbackend.Task, sink agentbackend.Sink) (agentbackend.Result, error) {
	prompt := t.Prompt + agentbackend.CapabilityEpilogue
	if t.SystemContext != "" {
		prompt = t.SystemContext + "\n\n" + prompt
	}

	// Humanloop MCP injection: 写临时 opencode.json，OPENCODE_CONFIG 指过去
	sockDir, err := os.MkdirTemp("", "humanloop-")
	if err != nil { return agentbackend.Result{}, err }
	defer os.RemoveAll(sockDir)
	srv, ep, err := humanloop.ListenIPC(sockDir)
	if err != nil { return agentbackend.Result{}, err }
	defer srv.Close()
	if e.socketHookForTest != nil {
		go e.socketHookForTest(humanloop.EndpointArg(ep))
	}

	cfgPath := filepath.Join(sockDir, "opencode.json")
	if err := writeOpencodeHumanloopConfig(cfgPath, e.binSelf, ep, e.maxQuestions); err != nil {
		return agentbackend.Result{}, err
	}

	args := []string{"run", "--dangerously-skip-permissions", "--format=json", "--dir", e.cfg.WorkDir, "-"}
	args = append(args, e.cfg.ExtraArgs...)

	cmd := exec.CommandContext(ctx, e.cfg.Bin, args...)
	cmd.Dir = e.cfg.WorkDir
	cmd.Env = append(cmd.Environ(), e.env...)
	cmd.Env = append(cmd.Env, "OPENCODE_CONFIG="+cfgPath)
	cmd.Stdin = strings.NewReader(prompt)
	// ... stdout pipe + nd-json decode loop + sink.Write per event + graceful shutdown on ctx.Done
}

func (e *executor) RunResume(ctx context.Context, sessionID, answer string, sink agentbackend.Sink) (agentbackend.Result, error) {
	// opencode run --session <id> --continue + prompt "User answered: " + answer
	// 与 codex 的 `exec resume <id> --json` 同义
}

func writeOpencodeHumanloopConfig(path, binSelf string, ep humanloop.Endpoint, max int) error {
	cfg := map[string]any{
		"$schema": "https://opencode.ai/config.json",
		"mcp": map[string]any{
			"loom_humanloop": map[string]any{
				"type":    "local",
				"command": []string{binSelf, "humanloop-mcp", humanloop.EndpointArg(ep), strconv.Itoa(max)},
				"enabled": true,
			},
		},
	}
	b, err := json.Marshal(cfg)
	if err != nil { return err }
	return os.WriteFile(path, b, 0o600)
}
```

**事件流解析**：参考 codex executor.go 的 `codexEvent` struct + nd-json 行循环；用 opencode 实际 event schema 替换字段（implementer 跑一次 `opencode run --format json "hi"` 抓样本）。

#### `permissions.go`

```go
// NoOp store: opencode 没有 claude 的 settings.json 那套权限模型；
// 同 codex Store——内存返默认 State，Patch 透传不持久化。
// 真正的权限走 --dangerously-skip-permissions（agent layer 默认注入）。
```

#### `detect.go`

```go
func detect(ctx context.Context, bin string) error {
	cmd := exec.CommandContext(ctx, bin, "--version")
	return cmd.Run()
}
```

#### `presets.go`（可选，**不实现**）

claude 包有 `presets.go` 做 permission preset 展开。opencode 没那套概念，本 PR 不加。

### 2. CLI 注册

`cmd/driver-agent/main.go` + `cmd/slave-agent/main.go` 各加一行 import：

```go
import (
    _ "github.com/yourorg/multi-agent/pkg/agentbackend/claude"
    _ "github.com/yourorg/multi-agent/pkg/agentbackend/codex"
    _ "github.com/yourorg/multi-agent/pkg/agentbackend/opencode"  // 新
)
```

`agent.kind: opencode` 在 `agentbackend.RegisteredKinds()` 里自动出现，driver/slave loader 的 `isRegisteredKind` 自动通过。

### 3. Driver 侧 deploy 模板

#### 新文件：`deploy/linux/driver/opencode.json.template`

```jsonc
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "driver": {
      "type": "local",
      "command": ["__DRIVER_AGENT__", "serve-mcp", "--config", "__CONFIG__"],
      "enabled": true
    }
  }
}
```

#### 新文件：`deploy/windows/driver/opencode.json.template`

同内容，install.ps1 渲染时 Windows-quote 路径。

#### `deploy/linux/driver/install.sh` 改

现有：
```bash
if [[ "$AGENT" == "claude" ]]; then
  sed -e "s|__PROJECT_DIR__|$PROJECT_ABS|g" "$HERE/.mcp.json.template" > "$PROJECT_ABS/.mcp.json"
else
  mkdir -p "$PROJECT_ABS/.codex"
  sed -e "s|__PROJECT_DIR__|$PROJECT_ABS|g" "$HERE/codex-mcp.toml.template" > "$PROJECT_ABS/.codex/config.toml"
fi
```

改为：
```bash
case "$AGENT" in
  claude)
    sed -e "s|__PROJECT_DIR__|$PROJECT_ABS|g" "$HERE/.mcp.json.template" > "$PROJECT_ABS/.mcp.json"
    ;;
  codex)
    mkdir -p "$PROJECT_ABS/.codex"
    sed -e "s|__PROJECT_DIR__|$PROJECT_ABS|g" "$HERE/codex-mcp.toml.template" > "$PROJECT_ABS/.codex/config.toml"
    ;;
  opencode)
    # CLI + desktop 共用 ~/.config/opencode/opencode.json
    OPENCODE_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/opencode"
    mkdir -p "$OPENCODE_DIR"
    sed \
      -e "s|__DRIVER_AGENT__|$PROJECT_ABS/driver-agent|g" \
      -e "s|__CONFIG__|$PROJECT_ABS/config.yaml|g" \
      "$HERE/opencode.json.template" > "$OPENCODE_DIR/opencode.json"
    echo "==> wrote $OPENCODE_DIR/opencode.json (consumed by both opencode CLI and desktop)"
    ;;
  *)
    echo "unsupported agent: $AGENT" >&2; exit 1 ;;
esac
```

注意：opencode 是**用户级**配置（不是 project-scoped 像 `.mcp.json` / `.codex/config.toml` 那样），因为 desktop 不会进入 project 目录读。**这是 opencode 集成的语义差异，要在 install.sh 输出里讲清。**

#### `deploy/windows/driver/install.ps1` 同上

加 `opencode` 分支，路径用 `[Environment]::GetFolderPath('ApplicationData')`（即 `%APPDATA%`）+ `opencode\opencode.json`。

#### Slave 端 install.sh / install.ps1

Slave 不需要 MCP 客户端模板（slave 上 opencode 是被 spawn 的，不需要给它装 driver MCP）。但 `AGENT=opencode` 时配置生成路径不变——只是 `agent.kind: opencode` 写到 config.yaml。verify 现有脚本是否 hardcoded `claude`/`codex` 分支。

### 4. 测试

| Test | 文件 | 覆盖 |
|---|---|---|
| `TestOpencodeBackend_AutoRegisters` | `pkg/agentbackend/opencode/backend_test.go` | `import _ "...opencode"` 后 `RegisteredKinds()` 含 `opencode` |
| `TestOpencodeNew_DefaultsBin` | 同 | `Bin==""` → `"opencode"` |
| `TestOpencodeNew_KeepsExplicitBin` | 同 | `Bin="/path"` 透传 |
| `TestOpencodeLLM_Run_StdinPrompt` | `pkg/agentbackend/opencode/llm_test.go` | fake `opencode` bash bin: echo stdin 的某 field 出 final text |
| `TestOpencodeLLM_Run_ExitNonZeroSurfacesStderr` | 同 | 失败时报错 tail 含 stderr |
| `TestOpencodeExecutor_Run_BasicFlow` | `pkg/agentbackend/opencode/executor_test.go` | fake bin 出 nd-json 事件流 → sink 收到 events + final Result |
| `TestOpencodeExecutor_Run_InjectsHumanloopMCP` | 同 | tmp opencode.json 含 `loom_humanloop` server，`command[0]==binSelf` |
| `TestOpencodeExecutor_RunResume_UsesSessionFlag` | 同 | argv 含 `--session <id> --continue` |
| `TestOpencodeExecutor_HonorsContextCancel` | 同 | ctx cancel → graceful shutdown，pid 不留 |
| `TestOpencodeDetect_Present` | `pkg/agentbackend/opencode/detect_test.go` | fake bin `--version` exit 0 |
| `TestOpencodeDetect_Missing` | 同 | bin 路径不存在 → err |
| `TestOpencodePermissions_NoOpRoundTrip` | `pkg/agentbackend/opencode/permissions_test.go` | Get/Patch 默认 State，不持久化 |
| `TestLoadConfig_AcceptsOpencodeKind` | `internal/driver/config_test.go` + `internal/config/config_test.go` | `agent.kind: opencode` 不被 unknown-kind 拒（依赖 `_ ".../opencode"` import） |
| `TestInstallScript_OpencodeRendersConfig` | `deploy/linux/driver/install_test.sh`（或新建）| 已渲染 opencode.json 含 `driver` mcp server 且无 `__*__` placeholder |

回归：`go test ./... -race -count=1` 整仓过。

### 5. 文档

- `docs/migration/2026-06-agent-config.md` 加段：「现支持 `agent.kind: opencode`，bin 默认 `opencode`；driver 端 install 自动写 `~/.config/opencode/opencode.json` 让 CLI 和 desktop 共用」
- README（如有相关章节）补一句 opencode 也支持
- `dev/configs/*.example.yaml` 不强加 opencode 示例（保持 claude 默认），但加注释「kind 可选 claude/codex/opencode」

## 兼容性

| 变更 | 影响 |
|---|---|
| 新 `KindOpencode` 常量 | 加常量不破坏现有 kind |
| 新 backend 包 + init 注册 | 不 import 就不激活；driver/slave CLI 加 import 才会暴露 |
| install.sh `case` 替换 `if/else` | claude/codex 分支语义不变 |
| Driver 侧写 `~/.config/opencode/opencode.json` | 全局用户级路径——如果用户已有自己的 opencode.json，**会被覆盖**。install.sh 应：(a) 检查文件存在时备份到 `opencode.json.bak.<ts>`，或 (b) merge mcp 段（更复杂）。**v1 采取 (a)** ——简单 + 可回滚。 |
| Slave 上 opencode 子进程默认带 `--dangerously-skip-permissions` | 与 codex 同款（无人值守）。operator 想去掉可在 `agent.extra_args` 加 `--no-skip` 类（如果 opencode 提供取消选项）—— **不在 v1 范围**。 |

## 决策记录

| 决策 | 备选 | 选择理由 |
|---|---|---|
| 用 `opencode run --format json` | 用 `opencode acp` (stdin/stdout nd-JSON) | `run` 是 documented + stable；`acp` 是新的 agent client protocol，复杂且文档少 |
| Humanloop 走临时 `opencode.json` + `OPENCODE_CONFIG` env | 永久写 `~/.config/opencode/opencode.json` 加 loom_humanloop server | 永久写会污染用户 config + 互相覆盖；临时 file + env 自包含 |
| Driver opencode.json 写 user-level（`~/.config/`） | project-level（`<project>/opencode.json`） | desktop 不进 project 目录；user-level 让 CLI 和 desktop 都能 discover |
| Install 覆盖现有 opencode.json 时备份 | merge | merge JSON + 后续 update 重复 merge 是复杂逻辑；v1 备份够用 |
| `Bin` 默认 `"opencode"` | `"opencode-ai"`（npm 包名） | npm install 后 bin 是 `opencode`；包名只是安装时用 |
| `--dangerously-skip-permissions` 默认注入 | extra_args 留 operator 填 | 与 codex 行为对齐；slave 是无人值守服务 |
| 不实现 acp 子命令 | 一并实现 | YAGNI——run 够用，acp 是后续优化 |
| 不接 opencode auth pass-through | install 加 prompt 走 `opencode auth login` | auth 是 provider-level（Anthropic/OpenAI key），不该 agent layer 管 |
| 不写 opencode-specific permissions store | 复用 claude 的 settings.json 模型 | opencode 没有那套概念；NoOp 跟 codex 一样 |
| 不在 master 路径加 opencode 任何东西 | 顺手 | 冻结，[[master_path_frozen]] |

## 反目标 / 反范围

- 不接 opencode `acp` 子命令
- 不实现 opencode auth flow
- 不动 master 路径
- 不为 opencode desktop 写 desktop-specific MCP discovery 路径（共用 CLI 的 `opencode.json`）
- 不修改 humanloop IPC server 协议（直接复用现有 `binSelf humanloop-mcp <endpoint> <max>` 入口）
- 不新增 yaml/json schema migration——`agent.kind: opencode` 在已统一的 schema 里直接生效

## 风险 / 未知

1. **opencode event schema** 我没在文档里查到完整 nd-json shape。Implementer 第一步：本地装 opencode（`npm i -g opencode-ai`）跑 `opencode run --format json "hi"`，抓样本写进 `executor.go` event struct。如果 schema 大变（比如分 `message.start` / `message.delta` / `message.end` 而非 codex 的 `item.completed` 单事件），executor.go 解析逻辑要重写——**但 backend interface 不变**。
2. **opencode CLI 长期 stability**：项目活跃，CLI 命令可能演化。`run --format json` 是 v1 主线，但 `--dangerously-skip-permissions` 未来可能改名。先按当前文档接，加注释指 doc URL。
3. **Desktop discovery 路径**——文档只说 OPENCODE_CONFIG / XDG default，没明确 desktop 是否也读 XDG。**Implementer 第一步还要 desktop 装一份 + 摆放配置测试**。如果 desktop 走另一个路径（如 `~/Library/Application Support/opencode/` on macOS），install.sh 需要写多份或硬链接——**这种情况下 design 第 3 节的「共用一份」假设打破，要单独 issue 跟进**。

## 兼容性 + 升级

- 现有 claude/codex slave 不受影响
- 已经装好 driver 的用户重新跑 install.sh 选 `--agent opencode` 即可切换；旧的 `.mcp.json` / `.codex/config.toml` 不删（无害留下）
- prod_test e2e 沿用——加 opencode slave 的 e2e 留作 follow-up（issue），本 PR 不阻塞
