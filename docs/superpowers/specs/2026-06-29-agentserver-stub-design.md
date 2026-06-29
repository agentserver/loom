# agentserver-stub design (Phase 0 #04 / WT-0-agentserver-stub)

**Status:** approved-pending-user-review
**Date:** 2026-06-29
**Owner:** worktree `p0-agentserver-stub`
**Tracks:** `docs/intermediate/12_loom_development_tasks_for_v3.md` §C4, `docs/final/todo_list.md` WT-0-agentserver-stub
**Blocks:** Phase 1 WT-1-eval-runner-skeleton (cannot fan out experiments without it)

## Problem

The eval-runner needs to bring driver, slave-a, slave-b, and observer up in batch
(N×runs/day) with **zero human-in-the-loop OAuth device flow**. Prod
`agent.cs.ac.cn` requires a browser consent step every time a sandbox's token
goes `forbidden` (see `tests/prod_test/E2E_RUNBOOK.md` line 220+ "Re-registering
agents"). That step is incompatible with automated experiments.

We need a stripped-down agentserver that **issues credentials directly** and
serves the bare minimum HTTP surface the four existing binaries already speak.

## Non-goals

- Not a prod replacement. Does not implement OAuth device flow, sandbox
  lifecycle, tunnel routing, or proxy traffic.
- Does not maintain real heartbeat state — accepts and discards.
- Does not replace `cmd/whoami-stub` (file-config, whoami-only) or
  `tests/k8s_commander/mock-agentserver` (k8s-scoped fixture). Distinct from
  both because it issues credentials at runtime rather than reading static JSON.

## Surface

### Process

Single Go binary `agentserver-stub` under `multi-agent/tools/eval/agentserver-stub/`.
Listens on `:18080` by default (chosen to avoid 18091–18094 occupied by prod
client agents per E2E_RUNBOOK).

### HTTP endpoints

Each handler is mounted on **both** path schemes (decided in brainstorming):

| Handler | New (spec) | Legacy alias |
|---|---|---|
| Register | `POST /api/v1/agents/register` | `POST /api/agent/register` |
| Whoami | `GET /api/v1/agents/whoami` | `GET /api/agent/whoami` |
| Heartbeat | `POST /api/v1/agents/heartbeat` | `POST /api/agent/heartbeat` |
| Health | `GET /healthz` | — |

The alias exists so unmodified driver/slave/observer (hardcoded to `/api/agent/...`)
can talk to the stub. Eval-runner code may target either; new code prefers the
`/api/v1/agents/` form. Both routes invoke identical handlers — no behavior
divergence.

#### `POST /api/v1/agents/register`

Request body: `{"role": "driver|slave|observer", "short_id": "drv-001", "workspace_id": "optional"}`.

Response: five-tuple JSON
```
{
  "sandbox_id":    "sbx-<hmac>",
  "tunnel_token":  "ttok-<hmac>",
  "proxy_token":   "ptok-<hmac>",
  "workspace_id":  "ws-default",
  "short_id":      "drv-001"
}
```

Idempotent: same `(role, short_id, workspace_id)` ⇒ same five-tuple every call.
Achieved by deriving every token via `HMAC-SHA256(server_secret, "field|role|short_id|workspace_id")`
truncated to 32 hex chars. `server_secret` is generated once at process start
(crypto/rand, 32 bytes, hex), so the same `short_id` is stable for the lifetime
of one stub process; restart yields fresh tokens (acceptable for ephemeral eval
runs — see *Limitations*).

#### `GET /api/v1/agents/whoami`

Reads `Authorization: Bearer <proxy_token>`. Reverse-lookup table keyed on the
token (built lazily as `register`/`issue` is called). Returns the matching
`{user_id, workspace_id, workspace_name, sandbox_id, short_id, role}` shape that
`internal/identity/agentserver` already parses (so observer's startup probe is
satisfied). `user_id` is synthesized as `eval-user`, `workspace_name` is
`workspace_id`.

401 on unknown token, 405 on wrong method. (`forbidden` lifecycle is
deliberately absent — eval runs do not need it.)

#### `POST /api/v1/agents/heartbeat`

Reads bearer, returns `204 No Content` on a known token, `401` on an unknown
one. Body is read and discarded. No state mutation, no expiry.

#### `GET /healthz`

`200 {"ok": true}`. Liveness probe for bootstrap scripts.

### CLI

```
agentserver-stub --listen :18080 [--workspace-id auto|<id>]
                                  starts the HTTP server in the foreground
agentserver-stub issue --server http://127.0.0.1:18080 --role <role> --short-id <id> [--workspace-id <id>]
                                  POSTs to /api/v1/agents/register on a running
                                  stub and prints the five-tuple JSON to stdout
```

`issue` is a thin client over the running server's `register` endpoint, so
clients (eval-runner, examples) need only the binary. `--workspace-id auto` on
the server means "all registrations land in the same `ws-eval-auto`".

## Internal structure

```
multi-agent/tools/eval/agentserver-stub/
├── main.go              CLI dispatch: serve vs issue subcommand
├── server.go            handler funcs + mux wiring (both path schemes)
├── tokens.go            hmac-based deterministic token derivation
├── stub_test.go         the five required tests
├── README.md            usage + limits + eval-runner snippet
└── examples/
    └── eval-bootstrap.sh
```

The whole binary should sit under ~400 LOC. Token registry is an in-memory
`sync.Map[token -> identity]`. No persistence.

## Test plan (TDD order)

`stub_test.go` is written first and must fail before any handler exists:

1. `TestIssue_ReturnsFiveTupleCredentials` — `register` returns all five fields,
   none empty.
2. `TestRegister_ReturnsConsistentTokens` — same `(short_id, role, workspace_id)`
   twice ⇒ byte-identical response. Different `short_id` ⇒ different tokens.
3. `TestWhoami_RoundTrips` — issue a token, call whoami with it, get back the
   same `short_id` / `workspace_id` / `role`.
4. `TestHeartbeat_AlwaysAccepts` — known token ⇒ 204; arbitrary payload OK.
5. `TestHealthz` — `{"ok": true}`.

Plus one alias smoke test asserting `/api/agent/whoami` and
`/api/v1/agents/whoami` return identical bodies for the same token.

`go test ./tools/eval/agentserver-stub/...` is the gate.

## Integration self-check

`examples/eval-bootstrap.sh`:

1. Build binary into a tmpdir, start with `--listen :18080 --workspace-id auto`
   redirecting logs.
2. Wait for `/healthz`.
3. Run `issue` four times (driver, slave-a, slave-b, observer), capture each
   five-tuple to a tmpdir file.
4. `curl` `/api/v1/agents/whoami` with each `proxy_token`; assert short_id
   matches the requested one.
5. Kill stub, clean up.

Script is idempotent and self-contained — Phase 1 eval-runner can crib it.

## Limitations (called out in README + startup banner)

- **NOT FOR PRODUCTION.** Tokens are HMAC over a per-process secret; restarting
  the stub invalidates every previously issued token. Anyone who can reach the
  port can issue tokens.
- No `forbidden` lifecycle, no `revoked` state, no expiry, no rotation.
- Heartbeat is a black hole — no liveness state for downstream consumers.
- Single-workspace default (`ws-eval-auto`). Multi-workspace is allowed by
  passing `--workspace-id` to `issue`, but no isolation guarantees.
- No TLS. Bind to loopback in real use (`--listen 127.0.0.1:18080`).

## Why HMAC instead of random tokens

Determinism inside one process is the only contract eval-runner needs (the
prompt: "deterministic可重放… 同 short_id + 同 workspace_id 应该签出同样的
token"). Random per-call would force eval-runner to checkpoint tokens to disk;
HMAC means "lose them, regenerate them, identical bits." Trade-off accepted:
restart-of-stub ⇒ fresh secret ⇒ tokens change. Eval-runner is expected to
start the stub itself in its own lifecycle, so this is a non-issue.

## Open questions

None blocking implementation.
