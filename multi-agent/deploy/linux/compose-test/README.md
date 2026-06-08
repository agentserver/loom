# compose-test

End-to-end smoke test for the three `deploy/linux/` templates
(observer / driver / slave). Spins up a local observer in one container,
then exercises the slave and driver installers against it in two more
containers, and surfaces the device-code "join workspace" URL each agent
prints on first start.

## What it verifies

1. `observer/install.sh` renders `observer.yaml`, stages the binary, and
   the daemon opens a TCP listener on `:8090`.
2. `slave/install.sh` renders config (with `--observer-url` /
   `--workspace`), stages the binary, and `slave-agent` boots and reaches
   the device-code OAuth step.
3. `driver/install.sh` renders the project (`config.yaml` + `.mcp.json`)
   and `driver-agent register` reaches the device-code step.
4. The same `api-key` flows: observer's workspace bootstrap key ↔
   slave / driver `observer.api_key` ↔ per-agent token mint.

Steps that require human interaction (clicking the device-code URLs) are
left as the operator's job — the test surfaces the URLs prominently.

## What it does NOT cover

- `claude` CLI inside the slave container — `chat`-skill tasks won't run.
  Add `claude` (and `ANTHROPIC_API_KEY` via `--anthropic-key`) if you want
  to exercise that path.
- The driver's `serve-mcp` step. Driver stops at `register` because
  `serve-mcp` is invoked by Claude Code's `.mcp.json`, not directly.
- Reachability to `agent.cs.ac.cn` — the tunnel registration is a hard
  external dependency. If your sandbox blocks that host, both driver and
  slave will stall at the device-code step.

## Prereqs

1. Docker + `docker compose` v2 (or the legacy `docker-compose` binary).
2. The three `linux-amd64` binaries dropped into `../bin/`:
   ```bash
   cd ../bin
   for n in observer-server driver-agent slave-agent; do
     curl -L -o "$n.linux-amd64" \
       "https://github.com/agentserver/loom/releases/download/v0.0.2/$n.linux-amd64"
     chmod +x "$n.linux-amd64"
   done
   ```
   Or build them with `make` / `go build` (see the per-role install.sh
   error messages for the exact `go build` commands).

## Run

```bash
cd deploy/linux/compose-test
docker compose up --build
```

Expected output (interleaved across services):

```
loom-observer  | ==> creating /var/lib/loom/compose-observer
loom-observer  | ==> done.
loom-observer  | ==============================================
loom-observer  |  Observer is up. Wire other agents with:
loom-observer  |    observer.url:          http://observer:8090
loom-observer  |    observer.workspace_id: ws-test
loom-observer  |    observer.api_key:      COMPOSE_TESTKEY_dont_use_in_prod
loom-observer  | ==============================================
loom-observer  | 2026/05/21 17:30:00 observer-server listening on :8090
loom-slave     | ==> creating /var/lib/loom/compose-slave
loom-slave     | ==============================================
loom-slave     |  slave: deploy succeeded. Starting slave-agent.
loom-slave     | ==============================================
loom-slave     |
loom-slave     |   Open this URL to authenticate:
loom-slave     |
loom-slave     |       https://agent.cs.ac.cn/oauth2/device/verify?user_code=VhspjQLp
loom-slave     |
loom-driver    |   Open this URL to register "compose-driver":
loom-driver    |       https://agent.cs.ac.cn/oauth2/device/verify?user_code=7AUTGKNs
```

Visit each URL in a browser, approve. After approval:

- driver's `register` will exit 0 and the `loom-driver` container exits.
- slave's `slave-agent` continues running, mints an observer token, and
  publishes its capability card. You can verify with:
  ```bash
  curl -sS -H "Authorization: Bearer COMPOSE_TESTKEY_dont_use_in_prod" \
    http://127.0.0.1:18190/api/agents | python3 -m json.tool
  ```

## Codex driver (optional)

The `driver-codex` service is gated behind the `codex` profile. To run it:

    export OPENAI_API_KEY=sk-...      # or set up codex login in a mounted volume
    docker compose --profile codex up driver-codex

Without auth, codex exec returns an error; the service will crash-loop. That's
expected — the goal of this profile is to verify the deploy templates lay the
right files, not to make a paid API call in CI.

## Tear down

```bash
docker compose down -v        # -v wipes the per-instance dirs in named volumes
docker image rm loom-deploy-test:latest
```

## Files

| Path | Purpose |
|---|---|
| `Dockerfile` | debian:bookworm-slim + bash/sudo/curl/ca-certs/python3/xxd |
| `docker-compose.yml` | three services bind-mounting `../` and per-role entrypoints |
| `entrypoint-observer.sh` | runs `observer/install.sh` then execs `observer-server` |
| `entrypoint-driver.sh`   | runs `driver/install.sh` then execs `driver-agent register` |
| `entrypoint-slave.sh`    | runs `slave/install.sh` then execs `slave-agent` |

## Tweaking the test

Change `API_KEY` in `Dockerfile` and rebuild. Add a second slave by
duplicating the `slave:` service block with a different `container_name`
and `INSTANCE` (edit `entrypoint-slave.sh` to read INSTANCE from env, or
copy it to `entrypoint-slave-2.sh`).
