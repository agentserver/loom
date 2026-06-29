# agentserver-stub

> ⚠️  **NOT FOR PRODUCTION.** This is a stripped-down agentserver for the
> evaluation harness (Phase 1 `WT-1-eval-runner-skeleton` and friends). It
> bypasses OAuth so batch experiments can spin driver / slave-a / slave-b /
> observer up without a human in the loop. Do not point real users at it.

Design: [`docs/superpowers/specs/2026-06-29-agentserver-stub-design.md`](../../../../docs/superpowers/specs/2026-06-29-agentserver-stub-design.md)

---

## What it does

- Issues the five-tuple credentials that the prod agentserver
  (`agent.cs.ac.cn`) signs after OAuth device flow — but without OAuth.
- Serves the bare minimum HTTP surface that `driver-agent`, `slave-agent` and
  `observer-server` already speak, so they come up with their existing config
  format.
- Tokens are deterministic within one process (HMAC over a per-process secret),
  so eval-runner can re-derive them rather than checkpoint them.

## Build

```bash
cd multi-agent
go build -o bin/agentserver-stub ./tools/eval/agentserver-stub
```

## CLI

```bash
# Start the server (foreground; default 127.0.0.1:18080, default workspace ws-eval-auto)
agentserver-stub --listen 127.0.0.1:18080 --workspace-id auto
# Override --listen 0.0.0.0:18080 only on a sealed eval host (register has no auth).

# Issue credentials for one agent against a running server
agentserver-stub issue --server http://127.0.0.1:18080 \
                       --role driver --short-id drv-001
# → prints {"sandbox_id":"sbx-…","tunnel_token":"ttok-…",
#           "proxy_token":"ptok-…","workspace_id":"ws-eval-auto",
#           "short_id":"drv-001"}
```

Default listen address `127.0.0.1:18080` is loopback-only on purpose — `register`
has no authentication and an all-interfaces bind would let anyone on the network
mint tokens. Port `18080` avoids the `18091–18094` range that
`tests/prod_test/E2E_RUNBOOK.md` reserves for client agent loopback. Pass an
explicit `--listen 0.0.0.0:...` only on a sealed eval host.

## HTTP surface

| Method | Path (spec) | Path (legacy alias) | Behavior |
|---|---|---|---|
| `POST` | `/api/v1/agents/register` | `/api/agent/register` | Body `{"role","short_id","workspace_id?"}`; returns five-tuple JSON; idempotent on `(role, short_id, workspace_id)` |
| `GET` | `/api/v1/agents/whoami` | `/api/agent/whoami` | Bearer `proxy_token`; returns `{user_id, workspace_id, workspace_name, sandbox_id, short_id, role}` (the shape `internal/identity/agentserver/resolver.go` parses) |
| `POST` | `/api/v1/agents/heartbeat` | `/api/agent/heartbeat` | Bearer `proxy_token`; body discarded; `204 No Content` |
| `GET` | `/healthz` | — | `{"ok": true}` |

Both path schemes invoke identical handlers. Unmodified driver/slave/observer
(which hardcode `/api/agent/...`) work as-is; new eval code should prefer
`/api/v1/agents/...`.

## Differences vs prod `agent.cs.ac.cn`

| Concern | Prod | Stub |
|---|---|---|
| OAuth device flow | Required; browser consent | **Not implemented**; `register` signs immediately |
| Token rotation / revocation | Real lifecycle (`forbidden`, refresh) | None; tokens valid for stub process lifetime |
| Tunnel routing | Real WebSocket fan-out | Token issued; no connection state |
| Proxy routing | Real model traffic | Token issued; no traffic plane |
| Heartbeat semantics | Drives sandbox `running/creating/forbidden` | Black hole; always 204 for known tokens |
| Authentication scheme | Per-sandbox OAuth identity | Per-process HMAC secret |
| Persistence | Server-side database | In-memory; restart ⇒ new secret ⇒ tokens change |
| Multi-tenant isolation | Real workspaces | One default workspace; `--workspace-id` to override per-issue |
| TLS | Required | None; loopback only |

## Eval-runner integration

The eval-runner owns the stub's lifecycle (start before driver/slave/observer,
kill after). Minimal Phase 1 bootstrap sketch:

```bash
agentserver-stub --listen 127.0.0.1:18080 --workspace-id auto &
STUB_PID=$!
trap "kill $STUB_PID 2>/dev/null" EXIT

# Wait for liveness.
until curl -fsS http://127.0.0.1:18080/healthz >/dev/null; do sleep 0.1; done

# Sign one set of credentials per agent and inject them into each YAML.
mkdir -p creds
for role in driver slave-a slave-b observer; do
  agentserver-stub issue --server http://127.0.0.1:18080 \
    --role "$role" --short-id "${role}-001" > "creds/${role}.json"
done

# Each config.yaml then reads:
#   credentials:
#     sandbox_id:    <from creds/<role>.json>
#     tunnel_token:  ...
#     proxy_token:   ...
#     workspace_id:  ...
#     short_id:      ...
```

`examples/eval-bootstrap.sh` runs this end-to-end against a real binary and
asserts every whoami round-trips. Phase 1 can crib it.

## Layout

```
agentserver-stub/
├── main.go              CLI: serve (default) + `issue` subcommand
├── server.go            handlers + mux wiring; both path schemes
├── tokens.go            HMAC-based deterministic token derivation
├── stub_test.go         5 required tests + alias smoke + error-code coverage
├── README.md            this file
└── examples/
    └── eval-bootstrap.sh   self-check used in PR validation
```

## Tests

```bash
go test ./tools/eval/agentserver-stub/...
PORT=18180 ./tools/eval/agentserver-stub/examples/eval-bootstrap.sh
```

## Limitations (intentional)

- No OAuth device flow.
- No real tunnel or proxy traffic plane.
- No `forbidden`/`revoked`/expiry/rotation lifecycle.
- Heartbeat does not track agent liveness.
- Single workspace by default; isolation is best-effort.
- No TLS. No auth on `register` itself — anyone who reaches the port can mint
  tokens. Bind to loopback.

If you find yourself wanting one of these in the stub, the answer is "use prod
agentserver instead, or extend `internal/identity/agentserver/` properly."
