# Agent backends — Claude Code vs Codex CLI

This project hosts a pluggable coding-agent layer at `pkg/agentbackend/`.
Driver and slave processes pick a backend via `agent.kind` in their
`config.yaml` (default `claude`). Both backends implement the same three
interfaces: `Run` (chat skill), `LLMRunner` (planner), `PermissionsStore`
(permissions skill).

## Side-by-side

| Aspect | claude | codex |
|---|---|---|
| Binary | `claude` (`npm i -g @anthropic-ai/claude-code`) | `codex` (`npm i -g @openai/codex`, Node ≥ 22) |
| Auth | `claude login` or `ANTHROPIC_API_KEY` | `codex login` (subscription) or `OPENAI_API_KEY` |
| Chat invocation | `claude --print --output-format=stream-json` | `codex exec --json --dangerously-bypass-approvals-and-sandbox` |
| Driver MCP wiring | `.mcp.json` (project root) | `.codex/config.toml` (project root, trusted-dir only) |
| Skill / prompt bundle | `.claude/skills/{multiagent,...}/` | `AGENTS.md` (+ optional `.codex/prompts/*.md`) |
| Permissions store | `<workdir>/.claude/settings.local.json` (`allow`/`deny` arrays) | `<workdir>/.codex/config.toml` (`sandbox_mode` + `approval_policy`) |
| Permissions grain | per-tool strings like `Bash(curl *)` | three sandbox modes: ask / workspace-write / full-access |

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

A single observer / workspace hosts agents of either backend. Driver-A on
claude can dispatch to slave-B on codex (and vice versa) — the chat output
is plain text either way, and the permissions skill exposes a uniform
preset vocabulary.

One process is one backend. To run both on the same host, run two slave
processes with distinct `--name` and distinct `LOOM_HOME` directories.

## See also

- Design spec: `docs/superpowers/specs/2026-05-23-codex-backend-design.md`
- Bootstrap one-liners: `deploy/README.md`

---

## 中文摘要

本项目通过 `pkg/agentbackend/` 抽象支持两种 coding agent 后端：Claude Code（默认）与 Codex CLI。
在 slave / driver 的 `config.yaml` 里通过 `agent.kind: claude | codex` 切换。一个进程对应一个后端；
同一 observer/workspace 可混部两类 agent。

**关键差异**：
- 二进制：`claude` (`@anthropic-ai/claude-code`) vs `codex` (`@openai/codex`，需 Node ≥ 22)
- 鉴权：`claude login`/`ANTHROPIC_API_KEY` vs `codex login`/`OPENAI_API_KEY`
- driver 注册：`.mcp.json` vs `.codex/config.toml`（项目级，需先 trust 目录）
- 权限模型：claude 是细粒度 `Bash(curl *)`-style 字符串；codex 是三档 sandbox（ask / workspace-write / full-access）

详见上面的英文表格与 `permissions` skill JSON 示例；后端选型与一键部署见 `deploy/README.md`。
