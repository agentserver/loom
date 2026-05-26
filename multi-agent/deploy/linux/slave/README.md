# linux-slave

Generic `slave-agent` bring-up for any Linux host (host-native, no docker, no SSH).
For the pre-registered prod-test variants (Jetson host-native, local docker) see
`../../../tests/prod_test/jetson/` and `../../../tests/prod_test/slave/`.

## What you get

| File | Purpose |
|---|---|
| `install.sh` | Validates inputs, renders templates, installs binary + config (and optional systemd unit) into `~/.loom/<name>/` |
| `config.yaml.template` | Slave config with placeholders for name, install dir, host resources |
| `slave-agent.service.template` | Systemd unit template with placeholders for service user and install dir |

## Prereqs

1. **Binary** at `../bin/slave-agent.linux-<arch>` (override with `--bin PATH`).
   ```bash
   # Option A — pre-built (replace amd64 with arm64 for aarch64 hosts)
   mkdir -p ../bin && curl -L -o ../bin/slave-agent.linux-amd64 \
     https://github.com/agentserver/loom/releases/latest/download/slave-agent.linux-amd64
   chmod +x ../bin/slave-agent.linux-amd64

   # Option B — build from source (from multi-agent/ )
   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' \
     -o deploy/linux/bin/slave-agent.linux-amd64 ./cmd/slave-agent
   # or GOARCH=arm64 for aarch64 hosts
   ```
2. **`claude` CLI** installed and on the service user's `PATH` (or edit
   `claude.bin` in the rendered `config.yaml` to its absolute path).
3. **Shared ws-prod observer api-key** — pasted via `--api-key` or hand-edited
   into `~/.loom/<name>/config.yaml` after install.
4. **`ANTHROPIC_API_KEY`** — passed via `--anthropic-key` to land in
   `~/.loom/<name>/slave.env`, or set in the unit's env some other way.

## Quick start

```bash
# foreground smoke test (current user, no systemd, no sudo beyond install steps)
./install.sh \
  --name slave-myhost \
  --observer-url http://observer.example.com:8090 \
  --workspace ws-prod \
  --tag linux --tag prod \
  --api-key 'de4a8e22…'              # the workspace bootstrap api-key

# then run it manually to see the device-code URL:
~/.loom/slave-myhost/slave-agent ~/.loom/slave-myhost/config.yaml
```

```bash
# production-style install as a dedicated user with systemd
sudo useradd -m -s /bin/bash loom            # only if the user doesn't exist
./install.sh \
  --name slave-myhost \
  --observer-url http://observer.example.com:8090 \
  --workspace ws-prod \
  --user loom \
  --systemd \
  --tag linux --tag prod \
  --api-key 'de4a8e22…' \
  --anthropic-key 'sk-ant-…'

# tail logs to grab the FIRST-RUN device-code URL
sudo tail -f /home/loom/.loom/slave-myhost/slave.log
```

After the device-code URL is approved, the slave persists the issued
sandbox/tunnel credentials back into its own `config.yaml`, then registers
with observer using the `api_key`, and starts publishing its capability
card. From the driver host:

```
mcp__driver__list_agents
# expect "slave-myhost" to appear
```

## Flag reference

| Flag | Default | Notes |
|---|---|---|
| `--name NAME` | (required) | Becomes `discovery.display_name`, `observer.agent_id`, install dir suffix, systemd unit name. |
| `--observer-url URL` | (required) | Goes into `observer.url`. Pre-flight: agent will POST `/api/agents/register` here on first start. |
| `--workspace ID` | `ws-default` | `observer.workspace_id`. Must match a workspace defined on the observer. |
| `--user USER` | `$USER` | Service user. The user must already exist; its `$HOME` is read from `/etc/passwd`. |
| `--loom-home PATH` | `<home>/.loom/<NAME>` | Install dir. Holds binary, config, log, `observer.token`, optional `slave.env`. |
| `--systemd` | off | Install `/etc/systemd/system/slave-agent-<NAME>.service` (sudo). Without this, you start the binary yourself. |
| `--desc TEXT` | `Linux slave-agent (<NAME>)` | `discovery.description`. |
| `--tag TAG` | `linux` | Repeatable. Becomes `resources.tags`. |
| `--api-key KEY` | (none) | Writes `observer.api_key`. Without this, edit the rendered config manually. |
| `--anthropic-key KEY` | (none) | Writes `ANTHROPIC_API_KEY=...` to `slave.env` (mode 0600). |
| `--bin PATH` | `../bin/slave-agent.linux-<arch>` | Override the binary path (e.g., point at a downloaded release asset). |
| `--agent CLI` | `claude` | `claude` or `codex`. One slave process = one backend. Under `--agent codex` the `chat` skill spawns `codex exec --json` instead of `claude --print --output-format=stream-json`. Mixed fleets (some slaves claude, others codex) share the same observer / workspace. For codex slaves, export `OPENAI_API_KEY` (and optionally drop a `~/.codex/config.toml` with `[model_providers.<name>]` to point at a self-hosted OpenAI-compatible endpoint — see [`../../agent-backends.md`](../../agent-backends.md)). |

Host CPU cores (`nproc`), arch (`uname -m`), and total memory (`/proc/meminfo`)
are auto-detected and written into the config's `resources` block.

## Skills advertised

The rendered `config.yaml` lists five `discovery.skills`, each backed by a
different code path in `slave-agent`. The driver routes work by skill, so
removing one disables that capability for this slave.

| Skill | What it lets the driver do |
|---|---|
| `chat` | Natural-language Claude Code task in the slave workspace. General-purpose. |
| `bash` | Run an explicit `script` (with `env`, `timeout_sec`) — native Go exec, no Claude. |
| `file` | Stateless `read` / `write` / `stat` on slave-local paths — native Go I/O. |
| `register_mcp` | Register a pre-built stdio MCP server file; tool calls then route via `skill:"mcp"`. Pair with the driver-side `scaffold-mcp-server` and `mcp-acceptance` skills — `register_mcp` only does structural validation. |
| `claude_permissions` | Read / patch this slave's Claude Code `settings.json` permissions through native code (don't ask `chat` to edit its own permissions). |

Drop any of these from `discovery.skills` if you don't want this slave to
accept that workload.

## Layout after install

```
~/.loom/<NAME>/
├── slave-agent           # binary, 0755
├── config.yaml           # 0600 — server, observer creds, discovery, resources
├── slave.env             # 0600 — optional, ANTHROPIC_API_KEY=...
├── observer.token        # 0600 — written on first boot by observerclient
└── slave.log             # service stdout+stderr (if --systemd)

/etc/systemd/system/slave-agent-<NAME>.service     # if --systemd, 0644
```

## Reset / re-registration

- **Rotate observer per-agent token** — `rm ~/.loom/<name>/observer.token` and
  restart; agent re-registers and the old token is invalidated.
- **Rotate agentserver sandbox** — blank out `credentials.sandbox_id` and
  `credentials.tunnel_token` in `config.yaml`, restart; device-code flow runs
  again.
- **Full cleanup** — `sudo systemctl disable --now slave-agent-<NAME>.service`
  (if used), `sudo rm /etc/systemd/system/slave-agent-<NAME>.service`,
  `rm -rf ~/.loom/<name>/`.
