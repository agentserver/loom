# dev — local development stacks

This directory contains Docker Compose files and example configs for running
multi-agent components locally.

## compose.distributed.yaml

Brings up a full distributed stack: Postgres + agentserver + observer + master
+ driver + two slaves (all built from source via the dev/agent-runtime image).

```bash
cd multi-agent
docker compose -f dev/compose.distributed.yaml up
```

## compose.multi-observer.yaml — two-pod observer cluster

Boots 1 Postgres + 2 observer-server containers + 1 nginx load balancer on
port 8090. Use this to reproduce and test the shared-daemon-registry cluster
mode locally.

### Prerequisites

- Docker with Compose v2 (`docker compose version` shows >= 2.x).
- A built `observer-server:dev` image, or set `OBSERVER_IMAGE` to an existing
  image ref (e.g. `registry.nj.cs.ac.cn/loom/observer:master-latest`).
- A cluster secret: a 64-hex-char (32-byte) secret generated with
  `openssl rand -hex 32`. The observer validates the secret is valid hex and
  at least 64 characters (32 bytes); alphanumeric or shorter secrets are
  rejected at startup.

Build the local image if needed:

```bash
cd multi-agent
docker build -f cmd/observer-server/Dockerfile -t observer-server:dev .
```

### Quick start

```bash
# Generate a cluster secret (must be 64 hex chars / 32 bytes):
export OBSERVER_CLUSTER_SECRET="$(openssl rand -hex 32)"

cd multi-agent
docker compose -f dev/compose.multi-observer.yaml up -d
```

Or use a `.env` file next to `compose.multi-observer.yaml`:

```
OBSERVER_CLUSTER_SECRET=<64-hex-char secret from openssl rand -hex 32>
OBSERVER_IMAGE=observer-server:dev
```

Then:

```bash
docker compose -f dev/compose.multi-observer.yaml up -d
```

A Makefile target is also available:

```bash
make multi-observer-up     # docker compose -f dev/compose.multi-observer.yaml up -d
make multi-observer-down   # docker compose -f dev/compose.multi-observer.yaml down -v
```

### Commander routes and the stub agentserver URL

`dev/configs/observer.multi-pod.yaml` sets
`identity.agentserver.url: "http://agentserver-stub:9999/dev-only"` with
`enabled: true`. This is a **dev-only stub** — the URL is intentionally
unreachable. The observer mounts `/api/commander/*` routes only when
`AgentserverURL` is non-empty; pointing at an unreachable stub is enough to
exercise the full commander surface in the multi-pod repro without a real
agentserver running.

Dev requests authenticate via `legacy_api_keys` (pre-shared `ak_dev_shared_secret`).
Agentserver is never contacted for these requests. `startup_probe: false` ensures
the process starts even though the stub URL is unreachable.

### Verify both pods serve the same daemon list

```bash
# Bring up and wait for both observers to be healthy:
docker compose -f dev/compose.multi-observer.yaml ps

# 30 round-robin requests through the nginx LB — daemon count must be stable:
for i in $(seq 1 30); do
  curl -s http://localhost:8090/api/commander/daemons | jq '.daemons | length'
done | sort -u | wc -l   # should print 1

# Hit each pod directly:
curl -s http://localhost:18091/readyz   # observer-1 direct
curl -s http://localhost:18092/readyz   # observer-2 direct
```

### Point a driver-agent at the LB

Set `observer_url: http://localhost:8090` in the driver's `config.yaml` (or
`LOOM_OBSERVER_URL=http://localhost:8090` in the environment). The driver dials
the LB; any pod that receives the WebSocket registers the daemon in shared
Postgres and forwards commands to whichever pod holds the active connection.

### Troubleshooting

**Postgres not migrated yet**

If you see `ERROR: relation "commander_daemons" does not exist`, the schema
migration has not run. Either:

- Start observer-1 or observer-2 once with `--migrate-only` to apply
  migrations before starting both:

  ```bash
  docker compose -f dev/compose.multi-observer.yaml run --rm observer-1 \
    --config /etc/observer/observer.yaml --migrate-only
  ```

- Or let the observer start normally — it auto-migrates on first boot if the
  schema is absent.

**Config file not found**

The compose file mounts `./configs/observer.example.yaml` from the `dev/`
directory. Copy or symlink it:

```bash
cp dev/configs/observer.example.yaml dev/configs/observer.yaml
# Then adjust compose volume mount to observer.yaml if desired.
```

**OBSERVER_CLUSTER_SECRET not set**

Both observers require `OBSERVER_CLUSTER_SECRET` to start. Check that the env
var is exported or present in the `.env` file alongside
`compose.multi-observer.yaml`.

**426 Upgrade Required on internal port**

During rolling upgrades of mixed binary versions, pods may return 426 on the
internal port 8091. This self-resolves once all pods run the same version. The
public port 8090 (via nginx) is not affected.
