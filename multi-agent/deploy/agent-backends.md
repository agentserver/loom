# Agent backends — Claude Code vs Codex CLI vs opencode

This project hosts a pluggable coding-agent layer at `pkg/agentbackend/`.
Driver and slave processes pick a backend via `agent.kind` in their
`config.yaml` (default `codex`). All backends implement the same three
interfaces: `Run` (chat skill), `LLMRunner` (planner), `PermissionsStore`
(permissions skill).

## Side-by-side

| Aspect | claude | codex | opencode |
|---|---|---|---|
| Binary | `claude` (`npm i -g @anthropic-ai/claude-code`) | `codex` (`npm i -g @openai/codex`, Node ≥ 22) | `opencode` |
| Auth | `claude login` or `ANTHROPIC_API_KEY` | `codex login` (subscription) or `OPENAI_API_KEY` | provider-specific |
| Chat invocation | `claude --print --output-format=stream-json` | `codex exec --json --dangerously-bypass-approvals-and-sandbox` | `opencode` |
| Driver MCP wiring | `.mcp.json` (project root) | `.codex/config.toml` (project root, trusted-dir only) | `~/.config/opencode/opencode.json` (global, CLI + desktop) |
| Skill / prompt bundle | `.claude/skills/{multiagent,...}/` | `AGENTS.md` (+ optional `.codex/prompts/*.md`) | `AGENTS.md` |
| Permissions store | `<workdir>/.claude/settings.local.json` (`allow`/`deny` arrays) | `<workdir>/.codex/config.toml` (`sandbox_mode` + `approval_policy`) | TBD |
| Permissions grain | per-tool strings like `Bash(curl *)` | three sandbox modes: ask / workspace-write / full-access | TBD |

## The `permissions` skill

Both backends accept the same JSON envelope. Patches carry uniform `presets`
plus backend-specific overrides; sending the wrong override returns an error
with the backend name.

### Get current state

Request (any backend):

```json
{"op": "get"}
```

Response (claude):

```json
{
  "backend": "claude",
  "path": "/home/x/.loom/slave-myhost/.claude/settings.local.json",
  "allow": ["Edit", "Read", "Write"],
  "deny": ["Bash(rm *)"]
}
```

Response (codex):

```json
{
  "backend": "codex",
  "path": "/home/x/.loom/slave-myhost/.codex/config.toml",
  "mode": "workspace-write"
}
```

### Patch with a uniform preset

Both backends honor `presets`:

```json
{"op": "patch", "presets": ["file_write"]}
```

- claude → adds `Read`, `Write`, `Edit` to `allow`
- codex → bumps `sandbox_mode` to `workspace-write`

### Backend-specific patches

Claude-native (rejected by codex):

```json
{"op": "patch", "allow_add": ["Bash(curl *)"], "deny_add": ["Bash(rm *)"]}
```

Codex-native (rejected by claude):

```json
{"op": "patch", "mode": "full-access"}
```

## Mixing backends in one fleet

A single observer / workspace hosts agents of any backend. Driver-A on
claude can dispatch to slave-B on codex (and vice versa) — the chat output
is plain text either way, and the permissions skill exposes a uniform
preset vocabulary.

One process is one backend. To run multiple backends on the same host, run
separate slave processes with distinct `--name` and distinct `LOOM_HOME`
directories.

## Codex against a self-hosted OpenAI-compatible endpoint

Codex CLI doesn't require api.openai.com — it accepts any OpenAI-compatible
endpoint via `[model_providers.<name>]` in `~/.codex/config.toml`. This is
the symmetric counterpart of running Claude Code against
`ANTHROPIC_BASE_URL=https://code.ai.cs.ac.cn`.

Example (self-hosted `modelserver` proxy serving `gpt-5.4` at
`https://code.ai.cs.ac.cn/v1`, reading the bearer from `OPENAI_API_KEY`):

```toml
# ~/.codex/config.toml
model_provider = "modelserver"
model = "gpt-5.4"

[model_providers.modelserver]
name = "modelserver"
base_url = "https://code.ai.cs.ac.cn/v1"
env_key = "OPENAI_API_KEY"
wire_api = "responses"
```

Both the slave's chat skill (`codex exec --json`) and the driver's planner
(`codex exec`) pick this up automatically — no app-side wiring needed.

## Codex in containers

Two friction points that aren't obvious until you try:

1. **Trusted-dir prompt can't fire non-interactively.** Project-scoped
   `<project>/.codex/config.toml` only loads after `codex` interactively
   confirms the directory is trusted. In a container, mount the **global**
   `/root/.codex/config.toml` instead, with both your
   `[model_providers.<name>]` block AND any `[mcp_servers.driver]` block.
   Codex applies the global config without a trust prompt.

2. **The driver-side codex is not a daemon.** The codex CLI is on-demand:
   it launches the driver MCP server as a stdio child via `mcp_servers.driver`
   only while a `codex exec` (or interactive `codex`) command is alive.
   Container topologies that need to drive codex from an external orchestrator
   usually keep the container alive with `sleep infinity` and run
   `docker exec <c> codex exec --dangerously-bypass-approvals-and-sandbox '...'`
   per task, rather than running codex as PID 1.

A working pattern for both backends in containers (driver-codex side) is to
mount the binary, the `config.yaml`, and the global `.codex/config.toml`
all read-only, then `docker exec` the codex CLI in as a sidecar.

## See also

- Design spec: `docs/superpowers/specs/2026-05-23-codex-backend-design.md`
- Bootstrap one-liners: `deploy/README.md`

---

## 中文摘要

本项目通过 `pkg/agentbackend/` 抽象支持三种 coding agent 后端：Claude Code（默认）、Codex CLI 与 opencode。
在 slave / driver 的 `config.yaml` 里通过 `agent.kind: codex | claude | opencode` 切换（默认 codex）。一个进程对应一个后端；
同一 observer/workspace 可混部多种 agent。

**关键差异**：
- 二进制：`claude` (`@anthropic-ai/claude-code`) vs `codex` (`@openai/codex`，需 Node ≥ 22) vs `opencode`
- 鉴权：`claude login`/`ANTHROPIC_API_KEY` vs `codex login`/`OPENAI_API_KEY` vs opencode（按 provider 配置）
- driver 注册：`.mcp.json` vs `.codex/config.toml`（项目级，需先 trust 目录）vs `~/.config/opencode/opencode.json`（全局，CLI 与桌面端共用）
- 权限模型：claude 是细粒度 `Bash(curl *)`-style 字符串；codex 是三档 sandbox（ask / workspace-write / full-access）；opencode 待定

**自建 OpenAI-兼容端点**：codex CLI 不强制走 api.openai.com，通过
`[model_providers.<name>]` 可指向任何兼容端点（与 claude 端
`ANTHROPIC_BASE_URL=https://code.ai.cs.ac.cn` 对称）。示例配置参见上面 EN
段的 `Codex against a self-hosted OpenAI-compatible endpoint`。

**容器里两个坑**：
1. 项目级 `.codex/config.toml` 需要交互式 trust，容器里没法弹窗 ——
   改成挂全局 `/root/.codex/config.toml`（同时含 `[model_providers.*]` 与
   `[mcp_servers.driver]`）绕过。
2. driver 容器里 `codex` 不是 PID 1，是按需 `docker exec` 调起来的 ——
   容器主进程通常是 `sleep infinity`，每个任务一次 exec。

详见上面的英文表格与 `permissions` skill JSON 示例；后端选型与一键部署见 `deploy/README.md`。
