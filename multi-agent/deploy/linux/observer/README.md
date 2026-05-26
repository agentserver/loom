# linux-observer

Generic `observer-server` bring-up for any Linux host. Observer is a single
HTTP daemon backed by SQLite — drivers and slaves POST `/api/agents/register`
with a workspace `api_key` to mint a per-agent token, then push telemetry to
`/api/events`.

For the prod-test observer instance at `39.104.86.73`, see the operator
notes in `../../../tests/prod_test/README.md` (not version-controlled).

## What you get

| File | Purpose |
|---|---|
| `install.sh` | Detects arch, renders templates, drops binary + config into `~/.loom/<name>/`, optional systemd unit |
| `config.yaml.template` | Observer config — listen addr, db path, one workspace + one bootstrap api-key |
| `observer-server.service.template` | Systemd unit with hardening (`NoNewPrivileges`, `ProtectSystem=full`, `ReadWritePaths=<install-dir>`) |

## Prereqs

1. **Binary** at `../bin/observer-server.linux-<arch>` (override with `--bin PATH`).
   ```bash
   # Option A — pre-built (replace amd64 with arm64)
   mkdir -p ../bin && curl -L -o ../bin/observer-server.linux-amd64 \
     https://github.com/agentserver/loom/releases/latest/download/observer-server.linux-amd64
   chmod +x ../bin/observer-server.linux-amd64

   # Option B — build from source (from multi-agent/ )
   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' \
     -o deploy/linux/bin/observer-server.linux-amd64 ./cmd/observer-server
   ```
2. A free TCP port (default `:8090`). Adjust with `--listen`.
3. (Optional) sudo, if you want systemd to manage the service.

## Quick start

```bash
# foreground smoke test (current user, no systemd)
./install.sh --name obs-dev
# → prints a generated bootstrap api-key. Save it.
# → tells you the exact foreground launch command.

# production: systemd-managed, dedicated user, fixed workspace and api-key
sudo useradd -m -s /bin/bash loom            # if it doesn't exist
./install.sh \
  --name obs-prod \
  --user loom \
  --systemd \
  --workspace ws-prod \
  --workspace-name "Production" \
  --api-key "$(head -c 32 /dev/urandom | xxd -p -c 64)"

# verify it's up
curl -sS -o /dev/null -w 'http=%{http_code}\n' http://127.0.0.1:8090/
sudo journalctl -u observer-server-obs-prod.service -f --since '1 min ago'
```

## Flag reference

| Flag | Default | Notes |
|---|---|---|
| `--name NAME` | (required) | Instance name; becomes unit name (`observer-server-<NAME>.service`) and install dir suffix. |
| `--user USER` | `$USER` | Service user. Must already exist; `$HOME` is read from `/etc/passwd`. |
| `--loom-home PATH` | `<home>/.loom/<NAME>` | Install dir. Holds binary, `observer.yaml`, `observer.db`, `observer.log`. |
| `--systemd` | off | Install `/etc/systemd/system/observer-server-<NAME>.service` (sudo). Without this, you start the binary yourself. |
| `--listen ADDR` | `:8090` | `listen_addr` in observer.yaml. Use `:port` for all interfaces, `127.0.0.1:port` for loopback only. |
| `--workspace ID` | `ws-default` | First workspace's `id`. |
| `--workspace-name TEXT` | same as `--workspace` | Workspace `name` (display). |
| `--api-key KEY` | (random hex) | Bootstrap api-key for that workspace. If omitted, a 32-byte hex key is generated and printed once. |
| `--bin PATH` | `../bin/observer-server.linux-<arch>` | Override the binary path. |

## Layout after install

```
~/.loom/<NAME>/
├── observer-server     # binary, 0755
├── observer.yaml       # 0600 — listen addr, db path, workspaces + api_keys
├── observer.db         # SQLite — created on first start
├── observer.db-wal     # WAL, journal
├── observer.db-shm     # shared mem
└── observer.log        # service stdout+stderr (if --systemd)

/etc/systemd/system/observer-server-<NAME>.service     # if --systemd, 0644
```

## Wiring slaves and drivers

Each slave / driver `config.yaml` references the observer through:

```yaml
observer:
  enabled: true
  url: http://<observer-host>:8090
  workspace_id: <YOUR WORKSPACE>
  agent_id: <UNIQUE PER AGENT>
  api_key: <ONE OF THE WORKSPACE BOOTSTRAP api_keys>
  token_state_path: /absolute/path/to/<agent>.token
```

On first start the agent POSTs `/api/agents/register` with the bootstrap
`api_key` as Bearer; the server mints a per-agent token and the slave writes
it to `token_state_path` (mode 0600). Subsequent `/api/events` calls use
that per-agent token automatically.

## Multiple workspaces / multiple keys

The `install.sh` only writes a single workspace × single api-key for
simplicity. To add more, edit `~/.loom/<NAME>/observer.yaml`:

```yaml
workspaces:
  - id: ws-prod
    name: "Production"
    api_keys:
      - id: bootstrap
        key: "..."
      - id: ci-bootstrap        # additional key for CI to register agents
        key: "..."
  - id: ws-dev
    name: "Development"
    api_keys:
      - id: bootstrap
        key: "..."
```

Then `sudo systemctl restart observer-server-<NAME>.service`. Per-agent
tokens already issued under the old config remain valid — `api_keys` are
only used at registration time.

## Reset / re-issue

- **Rotate a bootstrap api-key** — edit `api_keys[*].key`, restart the
  service. Existing per-agent tokens are unaffected.
- **Wipe an agent's per-agent token** — easier to do on the agent side
  (`rm <token_state_path>` then restart) than via the API.
- **Full reset** — `sudo systemctl disable --now observer-server-<NAME>.service`
  (if used), `sudo rm /etc/systemd/system/observer-server-<NAME>.service`,
  `rm -rf ~/.loom/<NAME>/`. Note this wipes the SQLite DB → all per-agent
  tokens, events, and audit history are lost.
