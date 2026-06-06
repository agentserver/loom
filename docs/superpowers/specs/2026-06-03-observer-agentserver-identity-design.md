# observer ↔ agentserver Identity Alignment

**Status:** Draft (2026-06-03)
**Scope:** observer-server only; agentserver side requires one new public endpoint (spec'd in §3.2).
**Depends on:** removal of unauthenticated dashboard (merged 2026-06-03).
**Implements:** "observer trusts agentserver as its user/workspace authority; observer stores no user table."

## Background

observer-server currently runs its own root-credential system (`api_keys` in
yaml, lazy `workspaces`, per-agent tokens issued via
`POST /api/agents/register`). agentserver has its own complete identity
system: `users` (with OIDC binding), `workspace_members` (N:N user↔workspace),
`sandboxes` (each agent instance is one sandbox with its own `proxy_token`).
The two never met.

For multi-user deployment we want **one source of truth for identity** —
agentserver — without giving observer the operational burden of its own user
management. driver/slave agents are already provisioned by agentserver and
hold a `proxy_token`; observer should accept that same token and translate
it to `(user_id, workspace_id, sandbox_id)` on demand.

Hard constraints from the brainstorming session:
1. agentserver is **only ever called**; it never reaches into observer.
2. observer stores **no user table**.
3. Isolation granularity is **workspace** — same workspace agents are
   mutually visible, matching agentserver's collaboration boundary.
4. The existing `api_keys` path **stays available** as a dev / offline
   escape hatch.
5. userspace MCP marketplace gets a forward-compatible **user-scoped
   visibility** field (default behaviour preserved).

## Architecture

```
┌─────────────────────┐                ┌─────────────────────────────────┐
│  agentserver        │                │   observer-server (one process) │
│                     │                │                                 │
│  GET /api/agent/    │ ◀──introspect──┤  identity.Chain                 │
│       whoami        │   (HTTP+Bearer)│   ├─ static (legacy api_keys)   │
│  (NEW, public)      │                │   └─ agentserver (whoami + LRU) │
│  Bearer proxy_token │                │       fresh TTL 180s ±20% jitter│
└─────────────────────┘                │       stale grace 15m           │
                                       │       singleflight per token    │
                                       │       capacity 65k entries      │
                                       │                                 │
                                       │  observerweb.AgentFromRequest   │
                                       │   → Identity in request ctx     │
                                       │                                 │
                                       │  observerweb endpoints unchanged│
                                       │   (all filter by workspace_id)  │
                                       └─────────────────────────────────┘
```

Three new internal packages, plus minimal edits to `observerweb`:

| Package | Role | Knows about |
| --- | --- | --- |
| `internal/identity` | `Resolver` interface, in-mem LRU cache with TTL + stale-grace, `singleflight` dedup, `Chain` combinator | no HTTP, no DB |
| `internal/identity/agentserver` | `Resolver` impl that calls agentserver `/api/agent/whoami` | HTTP, JSON shape, timeout |
| `internal/identity/static` | `Resolver` impl wrapping `observerstore.ValidateToken` + `LookupAPIKey` (the legacy path) | observerstore |

Edits in `internal/observerweb/server.go`:
- `AgentFromRequest(s, r)` and `handler.authenticate` switch from
  `s.ValidateToken(...)` to `resolver.Resolve(ctx, token)`. Return type
  unchanged for `AgentFromRequest`; `authenticate` returns the new
  `Identity` value so userspace can read `UserID`.
- `Store.ValidateToken` stays (still used by the static resolver).

No new HTTP routes on observer. agentserver side adds **one** endpoint
(§3.2).

## Components in Detail

### identity.Identity

```go
type Identity struct {
    UserID        string  // agentserver users.id; "" for legacy api_keys path
    WorkspaceID   string  // observer's workspace_id (== agentserver workspaces.id when source=agentserver)
    WorkspaceName string  // optional; first-writer-wins on observerstore lazy upsert
    AgentID       string  // observer agents.id (legacy) or sandboxes.short_id (agentserver)
    SandboxID     string  // agentserver sandboxes.id; "" for legacy
    Role          string  // workspace_members.role for agentserver-sourced; observer agent role for legacy
    Source        string  // "agentserver" | "local"
}
```

`Source` is for logging/audit only; downstream business logic must not
branch on it.

### identity.Resolver

```go
type Resolver interface {
    Resolve(ctx context.Context, token string) (Identity, error)
}

var (
    ErrInvalid   = errors.New("identity: invalid token")
    ErrRevoked   = errors.New("identity: token revoked")
    ErrUpstream  = errors.New("identity: upstream unavailable")
)
```

Implementations:

- **`identity.Chain{Resolvers...}`** — tries each in order; `ErrInvalid` from
  one resolver advances to the next; any other error is fatal and bubbles.
  Empty chain is a programming error (panic at construction).
- **`identity/static.Resolver`** — wraps `observerstore.ValidateToken`
  (legacy api_keys path). Returns `ErrInvalid` for unknown tokens.
- **`identity/agentserver.Resolver`** — HTTP client to
  `<agentserver_url>/api/agent/whoami`. Returns `ErrInvalid` on 401,
  `ErrRevoked` on 403, `ErrUpstream` on 5xx/timeout/network.

### identity.Cache

Process-local LRU keyed by `sha256(token)` (hex). Token plaintext never
stored. Entry:

```go
type entry struct {
    Identity  Identity
    FetchedAt time.Time
    ExpiresAt time.Time
}
```

Two thresholds (config, defaults shown):
- `freshTTL = 180s` — `now ≤ ExpiresAt` ⇒ direct hit, no upstream call.
- `staleGrace = 15m` — `ExpiresAt < now ≤ ExpiresAt + staleGrace` ⇒ entry
  is _stale_ but eligible for **fail-open** if upstream returns
  `ErrUpstream`. `now > ExpiresAt + staleGrace` ⇒ entry is discarded.

Per-entry **TTL jitter ±20%**: each `Put` sets
`ExpiresAt = FetchedAt + freshTTL * uniform(0.8, 1.2)`. Without this, a
crowd of agents that registered around the same time would all hit
`ExpiresAt` in the same second, producing a synchronized stampede of
introspection calls every `freshTTL` seconds. Combined with
`singleflight` (which dedups concurrent misses for the **same** token,
not across tokens), jitter spreads the per-token re-fetches over a
~70-second window per nominal TTL cycle. `staleGrace` is **not**
jittered — it's a tail bound, not a refresh trigger.

Capacity: 65536 entries (entry ≈ 200B → ~16 MB max).

A cache **wraps** a delegate resolver; on miss/expired it calls the
delegate, populates the entry, and dedups concurrent misses for the same
token via `golang.org/x/sync/singleflight`. This is the only place
`singleflight` is used.

### Data Flow

```
agent ──Bearer T──▶ POST /api/events
                         │
                         ▼
                 AgentFromRequest(resolver, r)
                         │
                         ▼
                 resolver.Resolve(ctx, T)
                         │
                         ▼  Chain: static, then agentserver-with-cache
                  ┌──────┴──────┐
                  ▼             ▼
              static       agentserver-cache:
              hit          1. cache GET — fresh hit → return
              return       2. miss/expired → singleflight:
                              a. agentserver.Resolve(ctx, T):
                                 GET /api/agent/whoami
                                 Authorization: Bearer T
                                 timeout=2s
                              b. 2xx  → cache.Put(now+freshTTL), return
                                 401 → cache.Evict(T), return ErrInvalid
                                 403 → cache.Evict(T), return ErrRevoked
                                 5xx/timeout → if stale entry exists,
                                                 log "stale fallback",
                                                 return stale identity
                                               else
                                                 return ErrUpstream
```

A `Resolve` call is the only authentication step. Once the handler has an
`Identity`, all downstream code uses `identity.WorkspaceID` as the
isolation key, exactly like today's `agent.WorkspaceID`.

## Error Handling

| Inner state | HTTP response | Header / log |
| --- | --- | --- |
| no bearer / malformed | 401 `missing or invalid bearer token` | (none) |
| chain returns `ErrInvalid` | 401 `unauthorized` | log `identity: rejected token_sha=… status=401` |
| chain returns `ErrRevoked` | 403 `token revoked` | log `identity: rejected token_sha=… status=403` |
| chain returns `ErrUpstream` (no stale fallback available) | 503 `identity upstream unavailable` | `Retry-After: 5`; log `identity: upstream unavailable reason=…` |
| identity OK but event.workspace_id ≠ identity.WorkspaceID | 403 `workspace mismatch` | unchanged from current behaviour |
| identity OK but cross-workspace read | 404 | unchanged (no existence leak) |
| cache fail-open hit | 200 (normal) | stderr `identity: stale fallback ws=ws_y age=42s reason=upstream_5xx` |
| introspection success | 200 (normal) | (cache hits not logged; misses log `identity: introspect ok user=u_x ws=ws_y src=agentserver took=23ms`) |

Notes:
- introspection has **no internal retry**. Retry storms turn an
  agentserver wobble into an observer outage.
- ctx cancellation by the upstream client (`r.Context().Done()`) is
  honoured — no 503 body, no log spam.
- `cache.Evict` on `ErrInvalid`/`ErrRevoked` ensures revoked tokens
  reach observer at most after the in-flight request completes.

## agentserver-side Contract

agentserver adds one new public endpoint. Spec:

```
GET /api/agent/whoami
Authorization: Bearer <proxy_token>

Success — 200 OK, application/json
{
  "user_id":        "u_abc123",            // users.id
  "workspace_id":   "ws_xyz789",           // workspaces.id
  "workspace_name": "Alice's Workspace",   // workspaces.name (optional, "" if unset)
  "sandbox_id":     "sbx_456",             // sandboxes.id
  "short_id":       "alice-driver-01",     // sandboxes.short_id (optional)
  "role":           "developer"            // workspace_members.role
}

401 Unauthorized — invalid or unknown token
403 Forbidden    — token valid but sandbox suspended / workspace removed
5xx              — internal error; observer treats as upstream unavailable
```

Auth: reuse the existing `extractProxyTokenSandbox` middleware.
Side effects: none — pure read across `proxy_tokens`, `sandboxes`,
`workspaces`, `workspace_members`. Idempotent.

The response intentionally carries **no expiry**; observer enforces TTL
locally so the two services don't couple their cache lifecycles.

### Hard prerequisite: `whoami` must exist before observer ships

Earlier drafts of this design proposed a transitional fallback to
agentserver's existing `POST /internal/validate-proxy-token`. That path
is now ruled out:

- `/internal/validate-proxy-token` carries **no authentication**
  (registered outside the auth middleware; agentserver's own
  `docs/audit-report-2026-03-13.md` flags this as **H-19** and explicitly
  recommends "shared-secret auth, or restrict to internal network").
- agentserver's Helm chart exposes the main Service via a single
  `HTTPRoute` rule that matches `/` — there is no path-level filter that
  hides `/internal/*` from the ingress. Whether the endpoint is
  externally reachable depends entirely on whether the operator added an
  ingress-level deny rule.
- The intended deployment of this design is **cross-trust-domain**
  (observer is not necessarily a pod next to agentserver). In that
  topology either the operator has correctly blocked `/internal/*` at
  the gateway (so observer can't reach it either) or they haven't (in
  which case the token-introspection endpoint is enumerable from the
  internet — unacceptable to depend on).

Therefore: **agentserver must ship `GET /api/agent/whoami` before
observer's agentserver identity source can be enabled in any
multi-user deployment.** No `fallback_to_internal_validate` flag
exists. observer's `identity.agentserver.enabled: true` will refuse to
boot if `whoami` returns 404 on a startup probe (one-shot, retry-once,
fail-fast).

Operators who need to run observer in the multi-user shape **before**
agentserver ships `whoami` have one supported escape: keep
`identity.legacy_api_keys.enabled: true`, distribute observer-owned
api_keys, and provision one workspace-scoped api_key per user out of
band. This is the same path as today, just used as a stopgap rather
than the primary identity source.

## observer schema changes

Minimal additive migrations on the existing tables; no new tables.

```sql
ALTER TABLE workspaces ADD COLUMN external_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE agents     ADD COLUMN external_sandbox_id TEXT NOT NULL DEFAULT '';
ALTER TABLE agents     ADD COLUMN external_user_id    TEXT NOT NULL DEFAULT '';
```

- `external_*` columns are **for audit only**, never used as a query key.
- First-writer-wins on `external_user_id` per workspace (matches the
  existing first-writer rule on `workspaces.name`).
- `UpsertWorkspaceLazy` and `UpsertAgent` get optional parameters to
  populate the new columns when the source is agentserver; legacy path
  passes empty strings.

`workspace_id` convention: observer reuses agentserver's `workspace_id`
string verbatim. agentserver-issued ids satisfy the existing
`[A-Za-z0-9_-]{1,64}` regex, so the schema constraint does not change.

### userspace (MCP marketplace) changes

```sql
ALTER TABLE userspace_package_versions
  ADD COLUMN visibility TEXT NOT NULL DEFAULT 'workspace';
ALTER TABLE userspace_package_versions
  ADD COLUMN created_by_user_id TEXT NOT NULL DEFAULT '';
```

Behaviour by `visibility`:
- `workspace` (default; backwards compatible) — same as today, filtered by
  the package's workspace.
- `user` — visible/installable only when caller's `Identity.UserID`
  matches `created_by_user_id`. Works across workspaces.
- `public` — visible/installable to everyone.

`userspace.Handler.Resolver` signature changes from
`func(r *http.Request) (workspaceID, agentID string, ok bool)` to
`func(r *http.Request) (Identity, bool)`. observerweb provides the
adapter; existing call sites keep working.

v1 of this design ships only the **schema** + filter logic; no UX or API
to set `visibility=user`/`public` (push always defaults to
`workspace`). Surfacing the choice to push clients is a follow-up.

## Configuration

`observer.yaml` adds an `identity:` section:

```yaml
identity:
  agentserver:
    enabled: true
    url: https://agentserver.example.com   # must expose GET /api/agent/whoami
    fresh_ttl: 180s         # per-entry TTL; ±20% jitter applied automatically
    stale_grace: 15m        # fail-open window when upstream is unavailable
    request_timeout: 2s
    cache_capacity: 65536   # LRU entry count; ~16 MB at ~200B/entry
    startup_probe: true     # one-shot GET /api/agent/whoami at boot;
                            # exit(1) if it returns 404 (whoami not deployed)
  legacy_api_keys:
    enabled: true
```

`api_keys:` block (existing) is unchanged — only consumed when
`legacy_api_keys.enabled: true`. Both `agentserver.enabled` and
`legacy_api_keys.enabled` may be true simultaneously; resolution order
is `static` then `agentserver` (static first so the small space of
locally-issued 32B-hex tokens is matched without an upstream call).

If neither is enabled the server refuses to boot.

## Testing

Five-layer plan; each layer covers what the layers above can't.

### 5.1 `internal/identity` — unit tests (the heaviest layer)

Subject to test: cache TTL/grace edges, LRU eviction, `singleflight`
dedup, Chain ordering, fail-open behaviour. Delegate is a fake
`Resolver` injected from the test; no HTTP.

Cases:
- Cache: put/get; TTL boundary (`age==freshTTL` → hot; `age==freshTTL+1ns` → stale-eligible; `age==freshTTL+staleGrace+1ns` → evicted); capacity eviction (65537th entry pushes oldest out).
- TTL jitter: with a seeded RNG inject 10 000 `Put`s at the same `FetchedAt`; assert the resulting `ExpiresAt` values are distributed across `[FetchedAt+0.8·freshTTL, FetchedAt+1.2·freshTTL]` with no spike density above 5% per second window (sanity, not strict statistics).
- `singleflight`: 100 concurrent `Resolve` for the same token triggers exactly 1 delegate call (assert via atomic counter).
- `Chain`: first returns `ErrInvalid`, second returns OK → OK; first returns 500-class → Chain bubbles; empty chain panics.
- Fail-open: delegate returns `ErrUpstream` + stale entry exists → returns stale; delegate `ErrUpstream` + no entry → returns `ErrUpstream`; delegate `ErrInvalid`/`ErrRevoked` → evicts and returns the error (no fail-open).

### 5.2 `internal/identity/agentserver` — unit tests

`httptest.NewServer` mocks `/api/agent/whoami`. Cases:
- 200 with valid body → parses every field; missing optional fields tolerated.
- 401 → `ErrInvalid`.
- 403 → `ErrRevoked`.
- 5xx → `ErrUpstream`.
- `server.Close()` mid-flight → `ErrUpstream`.
- `time.Sleep(request_timeout+1s)` → `ErrUpstream`.
- Body not JSON / required field missing → `ErrUpstream` + one stderr line.
- `Authorization` header passed through verbatim (request inspection in mock).

### 5.3 `internal/observerweb` — handler tests

Existing `seedWorkspaceAndAgents` tests (legacy api_keys path) **remain
unchanged**, asserting no regression. New helper
`seedAgentserverIdentity(t, mockResolver, ...)` enables:

- POST `/api/events` with agentserver-sourced token → 202.
- GET `/api/tasks/{id}/progress` cross-workspace agentserver token → 404.
- Mock resolver returns `ErrRevoked` → 403.
- Mock resolver returns `ErrUpstream`, no stale cache → 503 with `Retry-After: 5`.
- Mixed legacy + agentserver tokens both accepted under the chain.

### 5.4 `internal/userspace` — visibility tests

- `visibility=workspace` (default): existing behaviour, no change.
- `visibility=user`: alice pushes a user-scoped package; bob in the same workspace can't search/install; alice from a different workspace can.
- `visibility=public`: every authenticated identity can see/install.

### 5.5 End-to-end script `tests/scripts/observer_agentserver_e2e_test.go`

A standalone `cmd/whoami-stub` (small Go binary in the repo, not shipped
to production) implements `/api/agent/whoami` against a JSON config file
mapping token → identity. The script test:

1. Starts `whoami-stub` on a random port with a JSON file mapping two
   workspaces × three agents.
2. Starts `observer-server` with `identity.agentserver.url` pointing at
   the stub.
3. Runs curl matrix asserting: dashboard routes 404 (regression),
   cross-workspace 404, revoked → 403 (stub returns 403), stub down →
   503 with `Retry-After`, legacy api_key path still works when both
   sources are enabled.

`cmd/whoami-stub` is also useful for local driver/slave grayscale runs
that don't have a real agentserver handy.

### 5.6 Out of scope for this spec's tests

- No real agentserver binary in the test loop — that belongs to
  agentserver's own CI.
- No OIDC / Hydra interaction — this design intentionally does not use
  OAuth introspection on observer's side.

## Migration & Rollout

Phased, each phase ships independently:

1. **Phase 0 — schema** (low-risk, no behaviour change):
   migrations add `external_*` columns and userspace `visibility` /
   `created_by_user_id`. All defaults preserve current behaviour.
2. **Phase 1 — `internal/identity`** packages with **legacy resolver
   only**, wired in as a `Chain{static}`. observerweb switched to use
   `Resolver`. Zero behavioural change; all existing tests pass.
3. **Phase 2 — agentserver resolver** code merged but inert (gated by
   `identity.agentserver.enabled: false`). This phase is mergeable
   independently of agentserver's roadmap; the only behaviour change
   is that turning the flag on now requires `whoami` to exist
   (startup probe).
4. **Phase 3 — flip on** in production *after* agentserver ships
   `whoami`. observer's startup probe enforces this — flipping
   `enabled: true` while `whoami` returns 404 will refuse to boot,
   not silently fail. Once on, observer accepts both legacy
   api_keys-issued tokens and agentserver proxy_tokens.
5. **Phase 4 (optional, future)** — userspace visibility UI / API
   surface; operator can deprecate the legacy api_keys path per
   deployment by setting `legacy_api_keys.enabled: false`.

Each phase can be merged and shipped independently; rollback is "flip
the flag back".

## Appendix A — Issue to file against agentserver

Copy this verbatim into agentserver's issue tracker. It is the only
work item this spec creates outside the observer repo.

---

**Title:** Add `GET /api/agent/whoami` for downstream identity introspection

**Labels:** `area/api`, `area/auth`, `consumer/observer`

**Context.** observer-server (multi-agent project,
`docs/superpowers/specs/2026-06-03-observer-agentserver-identity-design.md`)
is moving to "agentserver is the single source of truth for
user / workspace / sandbox identity." driver and slave agents already
hold an agentserver-issued `proxy_token`; observer needs a public,
authenticated endpoint that maps that token to
`(user_id, workspace_id, sandbox_id, role)` so observer never has to
keep its own user table.

`POST /internal/validate-proxy-token` is unsuitable for this purpose
(audit H-19 in `docs/audit-report-2026-03-13.md`: no authentication,
listed for restriction to internal network; ingress HTTPRoute does not
filter `/internal/*`). observer's deployment is cross-trust-domain, so
it cannot depend on that endpoint.

**Request.** Add a new public endpoint:

```
GET /api/agent/whoami
Authorization: Bearer <proxy_token>

200 OK, application/json
{
  "user_id":        "u_abc123",            // users.id
  "workspace_id":   "ws_xyz789",           // workspaces.id
  "workspace_name": "Alice's Workspace",   // workspaces.name (may be "")
  "sandbox_id":     "sbx_456",             // sandboxes.id
  "short_id":       "alice-driver-01",     // sandboxes.short_id (may be "")
  "role":           "developer"            // workspace_members.role
}

401 — token unknown / format invalid
403 — token valid but sandbox suspended or workspace removed
5xx — server-side; consumer treats as upstream-unavailable
```

**Implementation notes (suggested, non-binding):**

- Reuse the existing `proxy_token` middleware (`extractProxyTokenSandbox`
  in `internal/server/agent_proxy_routes.go`). The middleware already
  resolves `sandbox_id` and `workspace_id` from `proxy_tokens` + checks
  sandbox status.
- The handler is a single SELECT joining `sandboxes` → `workspaces` →
  `workspace_members` (filter by the token's `user_id` lineage; for
  proxy tokens that means the user who owns the sandbox via membership).
- Pure read, idempotent, no side effects. Safe to deploy behind any
  caching layer.
- **Do not** include an expiry/TTL in the response body. Consumer
  enforces TTL locally; coupling the two services' cache lifetimes
  here creates a multi-service invalidation problem nobody wants.
- The response intentionally omits secrets (no token echo, no
  workspace credentials). The 6 fields above are the full contract.

**Why not extend `validate-proxy-token` instead?** It has no auth and
the same shape change would require simultaneously adding auth +
expanding the response — two breaking changes for existing callers
(llmproxy, credentialproxy). A new endpoint at `/api/agent/*`
inherits the existing proxy-token middleware and lets the
`/internal/*` family stay scoped to in-cluster callers.

**Acceptance criteria:**

1. Endpoint returns 200 + the documented 6 fields for any valid
   `proxy_token`.
2. Returns 401 on missing/unknown bearer (matches existing proxy-token
   middleware behaviour).
3. Returns 403 on suspended sandbox / removed workspace.
4. No information leak: 401 message is constant; does not distinguish
   "unknown" from "malformed".
5. Documented in agentserver's API surface (Swagger / `docs/api/*`).

**Non-goals for this issue:**

- Adding `whoami` for cookie-authenticated users — out of scope; the
  consumer is service-to-service.
- Refreshing the proxy_token. observer assumes proxy_tokens are
  long-lived per agentserver's existing model.
- Push notifications on revocation. observer polls (with a local
  TTL of 180s ±20%); push is a separate, larger workstream.

**Coordination:**

- Until this issue ships, observer's `identity.agentserver.enabled`
  must be `false`. Operators deploying observer in the multi-user
  shape today fall back to the legacy `api_keys`-issued tokens.
- observer's startup probe will hit `GET /api/agent/whoami` with an
  invalid bearer token on boot; expecting 401. A 404 means `whoami`
  isn't deployed and observer refuses to start. Please make sure the
  endpoint is mounted before agents start authenticating against it.

---

## Out of Scope

- Cross-workspace data sharing inside observer (e.g., alice grants bob
  read on one task) — not in this design; tracked separately.
- Workspace-scoped quotas / rate limiting on observer.
- Replacing observer's agent token concept with sandbox UUIDs in tables;
  the agent table stays as is.
- Pushing tokens **from** observer **to** agentserver (e.g., revocation
  callback). agentserver is read-only from observer's POV.
- Multi-tenant admin UI on top of `/api/workspaces`.
