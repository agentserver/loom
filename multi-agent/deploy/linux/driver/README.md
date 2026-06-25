# linux-driver

Generic `driver-agent` bring-up for any Linux host. The driver is a
Codex/Claude/opencode-launched MCP server, **not** a long-running daemon —
there's no systemd unit, just a project directory you point `codex` (or
`claude` / `opencode`) at.

For the prod-test driver shipped with this repo (`driver-prod`,
pre-registered), see `../../../tests/prod_test/driver/`.

## What you get

| File | Purpose |
|---|---|
| `install.sh` | Renders templates, drops binary + config + MCP registration into a project dir, optionally copies skill bundles |
| `config.yaml.template` | Driver config with placeholders for name, token dir |
| `.mcp.json.template` | Tells Claude Code how to launch `driver-agent serve-mcp` |
| `codex-mcp.toml.template` | Codex MCP server registration for `driver-agent serve-mcp` |
| `opencode.json.template` | opencode MCP server registration for `driver-agent serve-mcp` |

## Prereqs

1. **Binary** at `../bin/driver-agent.linux-<arch>` (override with `--bin PATH`).
   ```bash
   # Option A — pre-built (replace amd64 with arm64 for aarch64 hosts)
   mkdir -p ../bin && curl -L -o ../bin/driver-agent.linux-amd64 \
     https://github.com/agentserver/loom/releases/latest/download/driver-agent.linux-amd64
   chmod +x ../bin/driver-agent.linux-amd64

   # Option B — build from source (from multi-agent/ )
   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' \
     -o deploy/linux/bin/driver-agent.linux-amd64 ./cmd/driver-agent
   ```
2. **Codex CLI** installed locally — `codex` on `PATH`
   (`npm i -g @openai/codex`, Node ≥ 22). Or `claude` / `opencode` if
   using `--agent claude` / `--agent opencode`.
3. **Observer URL** — the driver authenticates with observer via the proxy
   token issued during device-code OAuth. `--api-key` is accepted for
   legacy setups but not required.
4. A target **project directory** where you'll run `codex` (or `claude`).

## Quick start

```bash
./install.sh \
  --project ~/code/my-driver \
  --name driver-myhost \
  --observer-url http://observer.example.com:8090 \
  --workspace ws-prod \
  --api-key 'de4a8e22…'

# one-time agentserver registration (device-code OAuth)
~/code/my-driver/driver-agent register --config ~/code/my-driver/config.yaml
# → open the printed verification URL; creds get written back into config.yaml

cd ~/code/my-driver
codex                            # first run prompts to trust the project dir
# In the Codex prompt:
#   mcp__driver__list_agents
# expect your slaves to appear (after they've registered too)
```

## Flag reference

| Flag | Default | Notes |
|---|---|---|
| `--project PATH` | (required) | Target dir; created if absent. Holds binary, config, `.mcp.json`, `.claude/`, `logs/`. |
| `--name NAME` | (required) | `discovery.display_name` and `observer.agent_id`. |
| `--observer-url URL` | (required) | `observer.url`, e.g. `http://observer.example.com:8090`. |
| `--workspace ID` | `ws-default` | `observer.workspace_id`. Must match a workspace defined on the observer. |
| `--desc TEXT` | `Linux driver-agent (<NAME>)` | `discovery.description`. |
| `--api-key KEY` | (none) | Optional. Writes `observer.api_key` for legacy observer auth. With device-code OAuth, the proxy token handles observer auth — this flag is not required. |
| `--skill-bundle PATH` | Claude: `../../../tests/prod_test/driver/.claude/skills/multiagent` if present; Codex: repo `skills/` if present | Claude copies under `<project>/.claude/skills/`; Codex copies under `<project>/.agents/skills/`. |
| `--token-dir PATH` | `~/.loom/<NAME>` | Parent dir for `observer.token`. Must be absolute. |
| `--bin PATH` | `../bin/driver-agent.linux-<arch>` | Override the binary path (e.g., point at a downloaded release asset). |
| `--agent CLI` | `codex` | `codex` (default), `claude`, or `opencode`. Codex mode writes `.codex/config.toml` + `AGENTS.md`. Claude mode writes `.mcp.json` + `.claude/skills/`. opencode mode writes `opencode.json` to `~/.config/opencode/` + `AGENTS.md`. |

## Project layout after install

```
<project>/
├── driver-agent            # binary, 0755
├── config.yaml             # 0600 — server, observer creds, driver_defaults
├── .mcp.json               # Claude Code MCP server registration
├── .claude/
│   └── skills/
│       └── multiagent/     # only if --skill-bundle resolved
├── .agents/
│   └── skills/             # Codex skills when --agent codex
└── logs/                   # audit logs (driver_defaults.audit_log_dir)

~/.loom/<NAME>/
└── observer.token          # 0600 — written on first start by observerclient
```

## Codex notes

- Codex loads project-scoped `.codex/config.toml` **only in trusted
  directories**. The first `codex` invocation in the project dir prompts
  to trust it — approve once.
- Codex CLI auth: `codex login` (with a ChatGPT subscription) or
  `export OPENAI_API_KEY=...`. The driver-agent itself still does its
  own observer device-code OAuth via `./driver-agent register`.
- Codex's permissions model is coarser than Claude's. The `permissions`
  skill exposes presets (`file_write`, `full_access`, ...) and the
  three sandbox modes (`ask`, `workspace-write`, `full-access`).
- **Self-hosted endpoint**: codex CLI accepts any OpenAI-compatible URL
  via `[model_providers.<name>]` in `~/.codex/config.toml`. Same pattern as
  pointing Claude at `ANTHROPIC_BASE_URL=...`. Example block + env in
  [`../../agent-backends.md`](../../agent-backends.md).
- **In containers**: the trusted-dir prompt can't fire non-interactively, so
  mount a fully-populated **global** `/root/.codex/config.toml` (both
  `[model_providers.<name>]` and `[mcp_servers.driver]`) instead of relying
  on the project-scoped `.codex/config.toml`. The container itself runs
  with `sleep infinity` as PID 1 and you `docker exec ... codex exec ...`
  per task — codex isn't a daemon.

## opencode notes

- opencode reads MCP server config from `~/.config/opencode/opencode.json`
  (or `$XDG_CONFIG_HOME/opencode/opencode.json`). This file is shared
  between the opencode CLI and desktop app.
- `--agent opencode` writes the driver MCP registration into this global
  config file. If one already exists it is backed up first.
- opencode also reads project-root `AGENTS.md` (same convention as Codex).
- The driver-agent itself still does its own observer device-code OAuth
  via `./driver-agent register`, regardless of the opencode backend.

## Why no systemd unit?

The driver process is owned by the coding agent (Codex / Claude Code /
opencode) via the project's MCP config (`.codex/config.toml`,
`.mcp.json`, or `opencode.json`). The coding agent starts it on session
open, talks to it over stdio, and tears it down on exit. Running it under
systemd would create a second copy that fights for the same observer
agent_id.

If you need the driver MCP server up independent of any Claude session
(e.g., for testing), launch it manually:

```bash
cd <project>
./driver-agent serve-mcp --config ./config.yaml
```

## Reset / re-registration

- **Rotate observer per-agent token** — `rm ~/.loom/<NAME>/observer.token`
  and re-launch; agent re-registers and the old token is invalidated.
- **Rotate agentserver sandbox** — blank out `credentials.sandbox_id` and
  `credentials.tunnel_token` in `config.yaml`, then re-run `driver-agent
  register`.
- **Full cleanup** — `rm -rf <project> ~/.loom/<NAME>`.
