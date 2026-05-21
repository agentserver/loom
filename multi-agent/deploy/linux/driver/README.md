# linux-driver

Generic `driver-agent` bring-up for any Linux host. The driver is a
Claude-Code-launched MCP server, **not** a long-running daemon — there's
no systemd unit, just a project directory you point `claude` at.

For the prod-test driver shipped with this repo (`driver-prod`,
pre-registered), see `../../../tests/prod_test/driver/`.

## What you get

| File | Purpose |
|---|---|
| `install.sh` | Renders templates, drops binary + config + `.mcp.json` into a project dir, optionally copies a multiagent skill bundle |
| `config.yaml.template` | Driver config with placeholders for name, token dir |
| `.mcp.json.template` | Tells Claude Code how to launch `driver-agent serve-mcp` |

## Prereqs

1. **Binary** at `../bin/driver-agent.linux-<arch>` (override with `--bin PATH`).
   ```bash
   # Option A — pre-built (replace amd64 with arm64 for aarch64 hosts)
   mkdir -p ../bin && curl -L -o ../bin/driver-agent.linux-amd64 \
     https://github.com/agentserver/loom/releases/download/v0.0.1/driver-agent.linux-amd64
   chmod +x ../bin/driver-agent.linux-amd64

   # Option B — build from source (from multi-agent/ )
   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' \
     -o deploy/linux/bin/driver-agent.linux-amd64 ./cmd/driver-agent
   ```
2. **Claude Code** installed locally — `claude` on `PATH`.
3. **Shared ws-prod observer api-key** — pass via `--api-key` or hand-edit
   `config.yaml` post-install.
4. A target **project directory** where you'll run `claude`.

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
claude
# In the Claude prompt:
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
| `--api-key KEY` | (none) | Writes `observer.api_key`. Without this, edit `config.yaml` by hand. |
| `--skill-bundle PATH` | `../../../tests/prod_test/driver/.claude/skills/multiagent` if present | Skill dir to copy under `<project>/.claude/skills/`. |
| `--token-dir PATH` | `~/.loom/<NAME>` | Parent dir for `observer.token`. Must be absolute. |
| `--bin PATH` | `../bin/driver-agent.linux-<arch>` | Override the binary path (e.g., point at a downloaded release asset). |

## Project layout after install

```
<project>/
├── driver-agent            # binary, 0755
├── config.yaml             # 0600 — server, observer creds, driver_defaults
├── .mcp.json               # Claude Code MCP server registration
├── .claude/
│   └── skills/
│       └── multiagent/     # only if --skill-bundle resolved
└── logs/                   # audit logs (driver_defaults.audit_log_dir)

~/.loom/<NAME>/
└── observer.token          # 0600 — written on first start by observerclient
```

## Why no systemd unit?

The driver process is owned by Claude Code via the project's `.mcp.json`.
Claude starts it on session open, talks to it over stdio, and tears it down
on exit. Running it under systemd would create a second copy that fights
for the same observer agent_id.

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
