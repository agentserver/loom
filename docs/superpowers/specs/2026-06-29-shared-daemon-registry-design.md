# Shared commanderhub daemon registry across observer instances

**Issue:** [#49](https://github.com/agentserver/loom/issues/49) — commanderhub daemon registry not shared across observer instances; the commander UI shows daemons intermittently when the observer scales horizontally.

> Revision history: v1 (initial), v2 (post-adversarial-review — fixes blockers B1-B4, majors M1-M11, minors m1-m10).

## Context

The observer deploys with `replicaCount: 2` in dev (`deploy/charts/observer/values.yaml:1`) and `replicaCount: 3` in production (`values-production.example.yaml:1`). The commanderhub `Hub` keeps every live daemon WebSocket in a per-process map (`internal/commanderhub/registry.go:86-93`). A `daemon-link` WS is naturally sticky — it lands on one pod and stays there — but the read paths the commander UI uses (`GET /api/commander/daemons`, `/tree`, `/sessions`, `POST /daemons/{id}/sessions/{sid}/turn`) are plain stateless HTTP requests. The load balancer routes each one to an arbitrary pod, and that pod can only see the daemons whose WS happened to land on it. The result, observed in production at `loom.nj.cs.ac.cn:10062`:

- A user with one driver-agent + one slave-agent sees the daemon list change on every refresh.
- `POST .../turn` returns 404 whenever the request lands on a non-owning pod.
- Daemon TCP connections and stderr stay healthy throughout — the bug is purely on the observer side.

The fix shares enough state between observer pods that any pod can answer any commander HTTP request consistently. We pick the smallest scope that closes the user-visible symptom: share the registry list and route command/turn requests to the owning pod via internal HTTP forwarding. Stale-session-cache divergence (currently an explicit non-goal in v1) is addressed by relocating the invalidation hook so it fires on the WS-owning pod — see §"Session cache invalidation on owning pod" — closing one of the largest user-visible holes without expanding the storage contract.

## Approach

Two layers and a small relocation:

1. **Postgres-backed registry of online daemons.** Each daemon WS owner pod writes a row when the daemon connects, heartbeats every 15 s with an UPSERT (self-healing against sweep races), deletes the row on disconnect, and a sweeper removes orphan rows older than 5 minutes. The row carries the pod's `owning_instance_url` (its own reachable address). Reads (`/api/commander/daemons`, `/tree`, `/sessions`) query this table and see all daemons regardless of which pod owns them.

2. **Internal pod-to-pod command forwarding** on a **separate dedicated listener** (`:8091` by default, never bound to the public ingress). When `SendCommand`/`SendCommandStream` is called on a non-owning pod, it POSTs to the owning pod's `<peer_internal_url>/forward` endpoint, authenticated by an **HMAC-of-body** header with a timestamp window (replay defense). The owning pod runs the original local-registry path and streams replies back as length-prefixed JSON envelopes capped at 1 MiB each. The streaming wire format mirrors the existing `commander.Envelope` shape — no change to the SSE the browser sees.

3. **Move `invalidateDaemonSessions` into the WS-owning pod's `routeFrame`** so the session cache stays consistent across pods without any new RPC.

All three are gated by config. The gate is **fail-closed on partial config**: any mix-up of `cluster.advertise_url{,_env}` set + `cluster.secret_env` empty (or vice versa) is a fatal `validateConfig` error at observer startup — silent fallback to single-pod mode would re-introduce issue #49.

### Component map

| Component                                            | File                                                                    | Change                                                                       |
|------------------------------------------------------|-------------------------------------------------------------------------|------------------------------------------------------------------------------|
| Postgres DDL                                         | `internal/commanderhub/authstore/schema_postgres.sql`                   | add `commander_daemons` table                                                |
| Migration runner                                     | `internal/commanderhub/authstore/migrate.go`                            | unchanged (same `db.Exec(schema)` runs new DDL)                              |
| Test conformance hook                                | `internal/commanderhub/authstore/postgres_test.go`                      | extend existing `OBSERVER_POSTGRES_TEST_DSN`-skip conformance to assert new table created |
| Registry struct → split                              | `internal/commanderhub/registry.go`                                     | rename current `registry` → `localRegistry`; **keep `Hub.reg *localRegistry` field** for test compat; add separate `sharedRegistry` type owning a *`localRegistry`* and a `*sql.DB` |
| Heartbeat goroutine                                  | `internal/commanderhub/hub.go` `ServeHTTP`                              | start in defer-bounded goroutine after `sharedReg.upsert`; exits on `<-dc.done`; UPSERT, not UPDATE |
| Session-cache invalidation relocation                | `internal/commanderhub/hub.go` `routeFrame`, `tree.go`                  | invalidate on owning pod when daemon emits a session-mutating frame (terminal `command_result`, terminal `status` events) |
| Forwarding client (used by `SendCommand[Stream]`)    | `internal/commanderhub/forward_client.go` (new)                         | called by `proxy.go` when `sharedReg.lookup` returns remote                  |
| Forwarding HTTP endpoint                             | `internal/commanderhub/forward_server.go` (new)                         | mounts `/forward` on the internal listener (NOT on the public mux)           |
| Internal HTTP listener                               | `cmd/observer-server/main.go`, `internal/observerweb/server.go`         | new `cluster.internal_listen_addr` (defaults `:8091`); separate `http.Server` started alongside the public one |
| Length-prefixed JSON envelope codec (1 MiB cap)      | `internal/commanderhub/forward_codec.go` (new)                          | one helper, used both sides; decimal-ASCII length + `\n` + JSON bytes        |
| Hub options + wiring                                 | `internal/commanderhub/wiring.go`, `hub.go`                             | `NewHub(resolver)` keeps signature; add `func (h *Hub) attachSharedRegistry(sr *sharedRegistry)` called by `MountAll` only in shared mode |
| Observer config schema                               | `cmd/observer-server/main.go`                                           | new `Cluster ClusterConfig` field + `validateConfig` rules                   |
| Helm chart values                                    | `deploy/charts/observer/values.yaml`                                    | new `cluster:` block (default `enabled: false`); **flip dev `replicaCount` from 2 → 1** so the chart's new fail-fast doesn't break dev defaults (operators set `replicaCount: 2` + cluster.enabled to opt in) |
| Helm chart secret + deployment                       | `deploy/charts/observer/templates/{secret.yaml,deployment.yaml}`        | render `cluster:` into observer.yaml, wire `POD_IP` + `OBSERVER_CLUSTER_SECRET` envs, internal-listener port |
| Helm chart pod init container                        | `deploy/charts/observer/templates/deployment.yaml`                      | when `cluster.enabled=true`, add init container that asserts env `OBSERVER_CLUSTER_SECRET` non-empty (catches `existingSecret` users who forgot the key) |
| Helm chart internal service                          | `deploy/charts/observer/templates/service.yaml` (new internal Service)  | second `Service` named `<release>-observer-internal` on port 8091, NOT exposed by Ingress/HTTPRoute |
| Helm chart Ingress/HTTPRoute hardening               | `deploy/charts/observer/templates/{ingress.yaml,httproute.yaml}`        | explicit deny rule for `/api/commander/_internal/` paths even on the public Service, as belt-and-suspenders if operator later re-mounts |
| Helm chart fail-fast                                 | `deploy/charts/observer/templates/secret.yaml`                          | hard error when `replicaCount>1 && store.driver=postgres && (cluster.enabled!=true OR (secret.create && !secret.clusterSecret))` |
| Helm chart values-production                         | `deploy/charts/observer/values-production.example.yaml`                 | `cluster.enabled: true`; doc `cluster-secret` key must exist in `existingSecret` |
| Chart tests                                          | `deploy/charts/observer/tests/chart_test.sh`                            | render assertions for cluster env, internal Service, fail-fast               |
| CI deploy workflow                                   | `.github/workflows/observer-deploy.yml`                                 | generate `clusterSecret` in smoke (alongside lines 88-96); set smoke `replicaCount: 2`; smoke probe (lines 204-210) hits each pod IP; release requires `OBSERVER_CLUSTER_SECRET` repo secret (line 285 `required` list); `::add-mask::` the secret |
| Multi-pod regression test                            | `internal/commanderhub/multi_pod_test.go` (new)                         | two `Hub` instances + Postgres via existing `OBSERVER_POSTGRES_TEST_DSN`-skip pattern; daemon connects to A, B sees it and forwards `list_sessions` |
| Forwarding-only tests                                | `internal/commanderhub/forward_test.go` (new)                           | sqlmock-driven shared registry; httptest server for forward handler; auth, replay, cap, cancellation, slow-reader tests |
| Local-repro compose                                  | `dev/compose.multi-observer.yaml` (new) + `dev/README.md` (new)         | 2 observers + 1 Postgres + nginx LB, `make multi-observer-up`                |
| Deploy docs                                          | `multi-agent/deploy/README.md`                                          | pre-rollout instruction: set `OBSERVER_CLUSTER_SECRET` repo secret before this PR's first release; `existingSecret` users add `cluster-secret` key |

### Postgres schema

Added to `internal/commanderhub/authstore/schema_postgres.sql`. Lives in the same migration script as `commander_logins`/`commander_sessions` because that migration is already gated on commander being enabled (`cmd/observer-server/main.go:264-268`, the `--migrate-only` path), and we want a single observer-server migration step, not two.

```sql
CREATE TABLE IF NOT EXISTS commander_daemons (
    user_id              text        NOT NULL,
    workspace_id         text        NOT NULL,
    daemon_id            text        NOT NULL,
    short_id             text        NOT NULL DEFAULT '',
    display_name         text        NOT NULL DEFAULT '',
    kind                 text        NOT NULL DEFAULT '',
    driver_version       text        NOT NULL DEFAULT '',
    capabilities         jsonb       NOT NULL DEFAULT '[]'::jsonb,
    owning_instance_url  text        NOT NULL,
    last_seen_at         timestamptz NOT NULL DEFAULT now(),
    created_at           timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, workspace_id, daemon_id),
    CONSTRAINT commander_daemons_user_id_nonempty       CHECK (length(user_id) > 0),
    CONSTRAINT commander_daemons_workspace_id_nonempty  CHECK (length(workspace_id) > 0),
    CONSTRAINT commander_daemons_daemon_id_nonempty     CHECK (length(daemon_id) > 0),
    CONSTRAINT commander_daemons_owning_url_nonempty    CHECK (length(owning_instance_url) > 0)
);
CREATE INDEX IF NOT EXISTS commander_daemons_owner_idx
    ON commander_daemons (user_id, workspace_id);
CREATE INDEX IF NOT EXISTS commander_daemons_last_seen_idx
    ON commander_daemons (last_seen_at);
```

`daemon_id` is a random 16-hex-char string (`hub.go:newDaemonID()`). At 64 bits with O(10) daemons per workspace, birthday collision is ~2⁻⁵⁸ and inconsequential per individual deployment, but flagged here for completeness: a collision shows as an UPSERT overwriting the wrong row's `owning_instance_url`; the next heartbeat from the losing daemon's pod fails the `WHERE owning_instance_url=$pod` filter and the daemon's WS reconnect re-asserts ownership. No corruption, brief invisibility.

Rollback path (down migration): `DROP TABLE IF EXISTS commander_daemons;` documented in `internal/commanderhub/authstore/schema_postgres_rollback.sql` (new). Helm `--migrate-only` does not auto-down; ops run psql manually.

### Registry split

Today's `*registry` (the in-memory map at `registry.go:86-93`) is renamed `*localRegistry` with identical methods (`add`, `remove`, `lookup`, `daemons`) and behavior. **The `Hub.reg *localRegistry` field type stays the same**, which preserves the 30+ test sites that call `hub.reg.add(...)` and `hub.reg.daemons(...)` (enumerated by `grep -nE '\bhub\.reg\b' internal/commanderhub/*_test.go` — all in `hub_test.go`, `proxy_test.go`, `http_test.go`, `tree_test.go`, `race_test.go`, `e2e_test.go`, `livelock_test.go`).

A new `*sharedRegistry` type holds `*localRegistry` + `*sql.DB` + `advertiseURL string` + `secret []byte` + `ttl, sweepEvery time.Duration`. `Hub` gains a separate `sharedReg *sharedRegistry` field (nilable; nil ⇒ legacy single-pod mode).

`sharedRegistry` methods:

```go
// upsert is called from ServeHTTP after localReg.add. Self-healing against
// sweep races: ON CONFLICT DO UPDATE rewrites owning_instance_url and
// resets last_seen_at, so a sweep that deleted the row reappears on the
// next heartbeat.
func (s *sharedRegistry) upsert(ctx context.Context, dc *daemonConn) error

// heartbeat is the 15s tick body. UPSERT (not UPDATE) so it re-creates
// the row if a sweep deleted it during a PG hiccup. 0 affected rows is
// benign and not logged.
func (s *sharedRegistry) heartbeat(ctx context.Context, dc *daemonConn) error

// remove DELETEs only when owning_instance_url matches this pod, so a
// daemon that has already reconnected to another pod isn't unlinked.
func (s *sharedRegistry) remove(ctx context.Context, o owner, daemonID string) error

// lookupRemote returns a peerURL when the DB row exists, last_seen is
// fresh, AND the row is NOT owned by this pod. Returns (zero, false) for
// any other case. Callers ALWAYS check localReg.lookup first; lookupRemote
// is only consulted on local miss.
func (s *sharedRegistry) lookupRemote(ctx context.Context, o owner, daemonID string) (peerURL string, info DaemonInfo, ok bool, err error)

// listAll returns every fresh row for the owner across all pods. Used by
// the read endpoints (/daemons, /tree, /sessions).
func (s *sharedRegistry) listAll(ctx context.Context, o owner) ([]DaemonInfo, error)

// sweep deletes ONLY rows older than 5 minutes (configurable). This is
// much longer than the heartbeat TTL so a transient PG outage on one pod
// cannot let another pod's sweep delete the row. The 5-minute floor is
// "dead long enough that the WS is definitely gone."
func (s *sharedRegistry) sweep(ctx context.Context) error
```

Where v1 conflated "fresh-enough to count as online" with "old-enough to delete," v2 separates them:
- **Online for reads:** `last_seen_at > now() - 45s` (3× heartbeat interval; one missed tick is OK)
- **Deletable by sweep:** `last_seen_at < now() - 5min` (rules out any plausible PG hiccup)

So a daemon whose owning pod has a 30-second PG stall is "stale" (`listAll` filters it out — UI shows it briefly missing) but **not deleted**. When PG recovers and the next heartbeat upserts, the daemon reappears in the list. No row loss, no need for the connecting daemon to reconnect.

The heartbeat goroutine surfaces failures: a counter `observer.commanderhub.registry.heartbeat_errors{pod=<advertise>}` increments per failed UPSERT; per-pod ratelimited WARN log at one-per-second.

### Hub field changes — explicit compat

The `Hub` struct grows one nilable field:

```go
type Hub struct {
    resolver     identity.Resolver
    upgrader     websocket.Upgrader
    reg          *localRegistry           // unchanged field type — preserves *_test.go callers
    sharedReg    *sharedRegistry          // nil in single-pod / legacy mode
    forwardCli   *forwardClient           // nil when sharedReg == nil
    turns        *turnStateStore
    sessionCache *sessionListCache
    cmdSeq       atomic.Int64
    TurnTimeout  time.Duration
}
```

`NewHub(resolver identity.Resolver) *Hub` signature is unchanged. Tests continue working unmodified. `MountAll`, in shared mode, calls a new `(h *Hub).attachSharedRegistry(sr *sharedRegistry, fc *forwardClient)` to plug in the cluster pieces. In legacy mode that method is never called and `hub.sharedReg == nil`.

`observerweb.Options` (currently fields `AgentserverURL` + `AuthStore` per `internal/observerweb/server.go:53-59`) gains one field `Cluster ClusterConfig`. Existing callers using struct-keyed init (the cmd/observer-server `opts := observerWebOptions(...)` path) are unaffected; zero-value `Cluster{}` ⇒ legacy mode. **Verified:** the two-arg constructors `NewWithResolver`/`NewWithResolverOptions` use struct-keyed init at `server.go:65, 76`, so a new optional field is backward-compat.

`MountAll` signature today is `MountAll(mux, resolver, agentserverURL, store)`. It becomes `MountAll(mux, resolver, agentserverURL, store, cluster ClusterRuntime)` where `ClusterRuntime` is the **resolved** view (DB handle + parsed secret + listener addr + advertise URL). A zero-value `ClusterRuntime{}` means single-pod. `observerweb.NewWithResolverOptions` builds the `ClusterRuntime` from `Options.Cluster` and passes it through.

### Session cache invalidation on owning pod

V1 acknowledged session-cache divergence as a non-goal, but inspection showed it's worse than "one stale UI refresh" because the cache TTL is 10 s (`hub.go:49`) and only the *requesting* pod invalidates after a turn. V2 fixes this without new RPCs:

Today's invalidation is called from `http.go` at six post-turn sites (lines 132, 242, 248, 254, 320, 341, 344, 347, 367, 370). Move the policy into `(dc *daemonConn).routeFrame` (`hub.go:243-260`): when a routed envelope is a terminal `command_result`, terminal status (`Done`/`AwaitingApproval`/`Error`), or `error` for a `session_turn`/`session_changed` command, call `dc.hub.invalidateDaemonSessions(dc.owner, dc.id)` directly. Because `routeFrame` runs on the WS-owning pod, the invalidation now happens on the pod whose cache could be stale.

Keep the existing call sites in `http.go` as belt-and-suspenders — calling invalidate twice on the same key is idempotent (a generation-counter bump + map delete).

Caveat: the relocation requires `routeFrame` to look at the *command type*, which isn't currently on the `pendingEntry`. We add one field: `pendingEntry.command string` set at `registerPending` time. Marginal allocation cost.

This still leaves cross-pod *turn-in-flight dedup* per-pod (a user double-clicking from two tabs on two pods both succeed) — explicitly out of scope; tracked as follow-up issue.

### Internal forwarding endpoint — separate listener

V1 mounted `/api/commander/_internal/forward` on the same mux as the public commander API. Verified that `templates/{ingress.yaml,httproute.yaml}` bind path `/` to the observer Service, so any external client could POST to the internal endpoint and the only defense was the static cluster secret in a header — a captured secret would replay forever, and the payload contains `user_id` + `workspace_id` plaintext, so leak ⇒ cross-tenant compromise.

V2 mounts the forwarding endpoint on a **separate `http.Server` bound to a different port** (`cluster.internal_listen_addr`, default `:8091`). The chart exposes this via a second Kubernetes `Service` (`<release>-observer-internal`) without any Ingress/HTTPRoute. Pod-to-pod traffic goes Service-to-Service inside the cluster; external network traffic cannot reach `:8091` unless an operator explicitly adds an Ingress for it (in which case the chart's hardening grep below catches the regression).

Additionally, the public Ingress/HTTPRoute templates add an explicit deny rule for `/api/commander/_internal/` paths as belt-and-suspenders. Even though the internal endpoint is no longer mounted there, the deny rule defeats any future regression where someone re-adds it to the public mux.

#### Auth — HMAC of (timestamp + body)

The forwarding request carries two headers:

```
X-Observer-Cluster-Timestamp: <unix-seconds-decimal>
X-Observer-Cluster-Auth:      <hex(hmac_sha256(secret, timestamp || "\n" || body))>
```

The receiver:
1. Rejects (403) if `|now - timestamp| > 60s` (replay window).
2. Computes the expected HMAC over the actual received body (post-read) and compares with `crypto/subtle.ConstantTimeCompare`. Reject (403) on mismatch.
3. Never logs the auth header or secret material; error responses contain only `{"error":"unauthorized"}` with no detail.

A static-header capture is unusable after 60 s. A leaked secret still lets an attacker forge requests until rotated, which is unavoidable for any symmetric scheme — the cluster secret is a Kubernetes Secret rotated by ops just like the Postgres DSN.

#### Request shape

```
POST /forward HTTP/1.1   (on the internal listener — NOT under /api/commander/)
X-Observer-Cluster-Timestamp: 1751155200
X-Observer-Cluster-Auth: <hex>
Content-Type: application/json
Content-Length: <N>          # capped at 1 MiB; receiver returns 413 if exceeded

{
  "user_id":      "<owner.userID>",
  "workspace_id": "<owner.workspaceID>",
  "daemon_id":    "<daemon-id>",
  "command":      "session_turn",
  "args":         {...},        // raw JSON, forwarded to daemon as-is
  "streaming":    true,
  "timeout_ms":   600000        // bounded by receiver to Hub.TurnTimeout
}
```

The HTTP body is the canonical bytes the HMAC was computed over. The receiver must read the body in full into a `[]byte` (subject to the 1 MiB cap) before HMAC verification.

#### Response — non-streaming

```
HTTP/1.1 200 OK
Content-Type: application/json

{"result": <raw command_result payload>}
```

or

```
HTTP/1.1 200 OK
{"error": {"code": "...", "message": "..."}}
```

#### Response — streaming

`Transfer-Encoding: chunked`. Body is a sequence of length-prefixed JSON envelopes:

```
<decimal-ascii-byte-length>\n<envelope-json-bytes>
<decimal-ascii-byte-length>\n<envelope-json-bytes>
...
```

The grammar is unambiguous: the receiver reads ASCII digits until `\n`, parses the length N (must be ≤ 1 MiB, else terminate stream + log), then reads exactly N bytes which must parse as a single JSON value. The stream ends when the daemon's response stream ends (terminal frame seen, ctx canceled, daemon gone) or when the receiver detects the request body has been closed by the caller (cancellation propagation; see below).

Choosing length-prefixed JSON over SSE for the pod-to-pod hop: SSE framing (`event:` + `data:` lines) is browser-oriented and ambiguous for binary-safe bytes; length-prefixed JSON is one read+one parse per frame and matches `commander.Envelope` exactly.

#### Back-pressure — bounded buffer + drop telemetry

The local `SendCommandStream` returns a channel of buffer 16 (`proxy.go:101`); the existing `sendOrDrop` drops non-terminal envelopes when the channel is full (`hub.go:270-287`). With the forwarding hop, drops would be far more likely (slower consumer through one extra TCP buffer). Two changes:

1. **Forwarding receiver's drain goroutine uses buffer 256** for the local `SendCommandStream`-fed channel (override at `proxy.go:101` only on the forward path), sized for a typical turn's event count without back-pressuring the daemon read loop.
2. **Drop counter:** `observer.commanderhub.forward.dropped{daemon_id,command}` increments each time `sendOrDrop` drops on the forward path. After any drops, emit a synthetic `{"type":"event","payload":{"event_kind":"truncated","text":"observer-side buffer overflow"}}` envelope at the next opportunity so the UI can visibly hint at the gap. Drop counters also surface as a WARN log line at most once per second per (daemon, command).

The forward client (Pod B side) reads the chunked body without buffering ahead of the consumer — `bufio.Reader` with the default 4 KiB buffer. The HTTP/1.1 chunked path is what `net/http` defaults to; HTTP/2 is fine too — `net/http` handles either transparently. Client uses `http.Transport{ResponseHeaderTimeout: 10s, IdleConnTimeout: 60s}`.

#### Cancellation propagation

The forwarding client opens the POST with a `context.Context` derived from the caller's ctx. When the caller cancels (browser closes SSE on Pod B → `r.Context().Done()` fires in `ch.turn`):
1. Pod B's forward client `Cancel()`s the inner ctx → Go's `http.Client` closes the underlying TCP connection.
2. On Pod A, the forward server detects connection close via `r.Context().Done()` in a goroutine watching the request context. That goroutine `Cancel()`s the inner ctx passed to `hub.sendCommandToLocal(...)`.
3. `sendCommandToLocal` (the existing `SendCommandStream` body factored out) selects on `ctx.Done()` and calls `dc.removePending(cmdID)` to free the daemon slot.
4. The forward server's drain loop exits when the local channel closes (which happens because removePending closes the per-entry cancel that unblocks the daemon read).

Spec'd test: `forward_test.go::TestForwardCallerCancelPropagates` — start a forwarding stream that sends one envelope every 50ms, cancel caller ctx after 200ms, assert the local pending entry is removed within 1s.

### Cluster config

New observer config block (added to `cmd/observer-server/main.go` `Config`):

```yaml
cluster:
  advertise_url: ""             # bare value, OR
  advertise_url_env: ""         # env var name to resolve (typical: OBSERVER_ADVERTISE_URL)
  secret_env: ""                # env var name (typical: OBSERVER_CLUSTER_SECRET)
  internal_listen_addr: ":8091" # separate from listen_addr
```

`advertise_url` is the pod's own reachable base URL of the **internal** listener (e.g., `http://10.0.0.42:8091`). For k8s, rendered via the downward API into `OBSERVER_ADVERTISE_URL`. For docker-compose, the service name. If both `advertise_url` and `advertise_url_env` are set, `advertise_url_env` wins (so chart-rendered envs override hardcoded YAML).

`validateConfig` rules (fail-closed):
- If `store.driver != "postgres"` AND any `cluster.*` field is set → reject (`"cluster.* is only supported with store.driver=postgres"`).
- Cluster fields are coupled: `(advertise_url || advertise_url_env)` and `secret_env` must either ALL be empty (single-pod mode) or ALL be non-empty AND resolve to non-empty values at startup. Partial config → fatal `"cluster: advertise_url and secret_env must both be configured, or both omitted"`.
- If shared mode is enabled: `internal_listen_addr` must be non-empty (default `:8091` applies if unset).
- Log on startup: `commanderhub: shared registry enabled (advertise=<url>, internal=<addr>)` OR `commanderhub: single-pod mode (registry=local)`.

This kills the silent-fallback footgun: a misconfigured multi-pod deployment refuses to start instead of running as broken single-pod.

#### Cross-check at runtime

The shared registry on each pod periodically (every 30s) emits a metric `observer.commanderhub.peers_seen` counting distinct `owning_instance_url` values currently in the table. If `peers_seen == 1` for >5min on a pod that has `sharedReg != nil`, log a WARN: "shared mode enabled but no peer daemons visible — verify other pods are healthy."

### Hub wiring change

`MountAll` becomes:
```go
type ClusterRuntime struct {
    DB                 *sql.DB    // nil → shared mode off
    AdvertiseURL       string     // empty → shared mode off
    Secret             []byte     // empty → shared mode off
    InternalListenAddr string     // separate listener for /forward
}

func MountAll(
    publicMux *http.ServeMux,
    internalMux *http.ServeMux,    // nil in single-pod mode
    resolver identity.Resolver,
    agentserverURL string,
    store authstore.Store,
    cluster ClusterRuntime,
)
```

`observerweb.NewWithResolverOptions` builds both muxes (when cluster enabled), constructs the internal `http.Server`, and starts them both. The chart's `deployment.yaml` exposes both `containerPort: 8090` (public) and `containerPort: 8091` (internal).

### Helm chart changes

#### `values.yaml`

```yaml
# Flip default from 2 → 1 because the chart's new fail-fast block refuses
# replicaCount > 1 without cluster config. Operators opting into multi-pod
# must set both replicaCount and cluster.enabled.
replicaCount: 1

cluster:
  enabled: false
  advertiseUrlEnv: OBSERVER_ADVERTISE_URL
  secretEnv: OBSERVER_CLUSTER_SECRET
  secretKey: cluster-secret
  internalListenAddr: ":8091"
  internalServicePort: 8091
```

#### `values-production.example.yaml`

```yaml
replicaCount: 3
cluster:
  enabled: true
  # Operator MUST add a `cluster-secret` key to existingSecret. The chart
  # cannot verify this; the init container in the pod template asserts the
  # env is non-empty at pod startup.
```

#### `templates/secret.yaml` fail-fast (added near the top)

```gotemplate
{{- $multiPod := gt (int .Values.replicaCount) 1 }}
{{- $isPostgres := eq .Values.config.store.driver "postgres" }}
{{- if and $multiPod $isPostgres (not .Values.cluster.enabled) }}
{{- fail "replicaCount > 1 with store.driver=postgres requires cluster.enabled=true (set cluster.enabled=true and provide secret.clusterSecret or an existingSecret with a 'cluster-secret' key)" }}
{{- end }}
{{- if and .Values.cluster.enabled .Values.secret.create (not .Values.secret.clusterSecret) }}
{{- fail "cluster.enabled=true with secret.create=true requires secret.clusterSecret (≥32 chars of random)" }}
{{- end }}
```

Add to `observer.yaml` rendered into the secret:

```gotemplate
    {{- if .Values.cluster.enabled }}
    cluster:
      advertise_url_env: {{ .Values.cluster.advertiseUrlEnv | quote }}
      secret_env: {{ .Values.cluster.secretEnv | quote }}
      internal_listen_addr: {{ .Values.cluster.internalListenAddr | quote }}
    {{- end }}
```

Add secret data key (only when `secret.create=true`):

```gotemplate
  {{- if and .Values.cluster.enabled .Values.secret.create }}
  {{ default "cluster-secret" .Values.cluster.secretKey }}: {{ required "secret.clusterSecret is required when cluster.enabled=true and secret.create=true" .Values.secret.clusterSecret | quote }}
  {{- end }}
```

#### `templates/deployment.yaml`

Add to the observer container's `env`:

```gotemplate
{{- if .Values.cluster.enabled }}
- name: POD_IP
  valueFrom:
    fieldRef:
      fieldPath: status.podIP
- name: {{ .Values.cluster.advertiseUrlEnv }}
  value: "http://$(POD_IP):{{ .Values.cluster.internalServicePort }}"
- name: {{ .Values.cluster.secretEnv }}
  valueFrom:
    secretKeyRef:
      name: {{ include "observer.configSecretName" . }}
      key: {{ default "cluster-secret" .Values.cluster.secretKey }}
{{- end }}
```

Add the internal-listener port to `ports`:

```gotemplate
- name: http
  containerPort: {{ .Values.service.port }}
{{- if .Values.cluster.enabled }}
- name: internal
  containerPort: {{ .Values.cluster.internalServicePort }}
{{- end }}
```

Add an init container to assert the env is populated (catches `existingSecret` users who forgot the key):

```gotemplate
{{- if .Values.cluster.enabled }}
- name: assert-cluster-secret
  image: "{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}"
  imagePullPolicy: {{ .Values.image.pullPolicy }}
  command: ["/bin/sh", "-ec"]
  args:
    - 'test -n "${{ .Values.cluster.secretEnv }}" || (echo "{{ .Values.cluster.secretEnv }} env var is empty; check your Secret has key {{ default "cluster-secret" .Values.cluster.secretKey }}" >&2; exit 1)'
  env:
    - name: {{ .Values.cluster.secretEnv }}
      valueFrom:
        secretKeyRef:
          name: {{ default (include "observer.configSecretName" .) .Values.existingSecret }}
          key: {{ default "cluster-secret" .Values.cluster.secretKey }}
{{- end }}
```

#### `templates/service.yaml` — new internal Service

```gotemplate
{{- if .Values.cluster.enabled }}
---
apiVersion: v1
kind: Service
metadata:
  name: {{ include "observer.fullname" . }}-internal
  labels:
    {{- include "observer.labels" . | nindent 4 }}
spec:
  type: ClusterIP
  ports:
    - name: internal
      port: {{ .Values.cluster.internalServicePort }}
      targetPort: internal
      protocol: TCP
  selector:
    {{- include "observer.selectorLabels" . | nindent 4 }}
    app.kubernetes.io/component: observer
{{- end }}
```

#### Public Ingress/HTTPRoute hardening

Add explicit deny path-prefix `/api/commander/_internal/` (still belt-and-suspenders even though the endpoint is no longer mounted on the public mux). For nginx-style annotations:
```yaml
nginx.ingress.kubernetes.io/configuration-snippet: |
  location ~* ^/api/commander/_internal/ { return 404; }
```
For HTTPRoute, add a `Filter: RequestRedirect` to 404 that path prefix.

#### `tests/chart_test.sh` — new assertions

```bash
# 1. Default (replicaCount=1) renders no cluster env.
default="$(helm template observer-test "$CHART_DIR")"
! grep -q 'OBSERVER_CLUSTER_SECRET' <<<"$default"
! grep -q 'observer-test-observer-internal' <<<"$default"

# 2. Multi-pod with cluster.enabled renders envs + internal Service.
multi="$(helm template observer-test "$CHART_DIR" \
  --set replicaCount=2 \
  --set cluster.enabled=true \
  --set secret.create=true \
  --set secret.clusterSecret=$(head -c 48 /dev/urandom | base64 | tr -d '+/=' | head -c 48) \
  --set secret.databaseUrl='postgres://x' \
  --set secret.s3AccessKey=x --set secret.s3SecretKey=x \
  --set secret.telemetryKeys.telemetry-global-key=x \
  --set config.identity.legacyAPIKeys.enabled=true \
  --set config.apiKeys[0].id=test --set config.apiKeys[0].key=test)"
grep -q 'OBSERVER_CLUSTER_SECRET' <<<"$multi"
grep -q 'POD_IP' <<<"$multi"
grep -q 'observer-test-observer-internal' <<<"$multi"
grep -q 'containerPort: 8091' <<<"$multi"
grep -q 'name: assert-cluster-secret' <<<"$multi"

# 3. Multi-pod without cluster.enabled fails fast.
if helm template observer-test "$CHART_DIR" --set replicaCount=2 \
    --set config.store.driver=postgres --set secret.create=true \
    --set secret.databaseUrl=x 2>&1 | tee /tmp/out | grep -q 'cluster.enabled=true'; then
  echo "fail-fast detected as expected"
else
  echo "expected fail-fast on replicaCount=2 without cluster.enabled" >&2; exit 1
fi
```

### CI workflow changes

**`.github/workflows/observer-deploy.yml`** verified against current file:

- **Smoke job (`smoke:` at line 60), inside the `Generate smoke values` step:**
  - Add `cluster_secret = "".join(secrets.choice(alphabet) for _ in range(48))` to the secret-generation block at lines 88-96.
  - At line 99 change `"replicaCount": 1` → `"replicaCount": 2`.
  - In the `values` dict add:
    ```python
    "cluster": {"enabled": True},
    "secret": {..., "clusterSecret": cluster_secret},  # merge into existing secret block
    ```
  - Mask the secret at generation: prepend the Python block with `print(f"::add-mask::{cluster_secret}")`.

- **Smoke probe (`Smoke from cluster` step at line 173, in-cluster wget at lines 204-210):** extend the busybox script to iterate over both pod IPs:
  ```sh
  for ip in $(kubectl -n ... get pods -l app.kubernetes.io/instance=$SMOKE_RELEASE,app.kubernetes.io/component=observer -o jsonpath='{.items[*].status.podIP}'); do
    wget -qO- "http://$ip:8090/readyz"
  done
  ```
  Asserts each pod's readiness independent of LB routing.

- **Release job (`release:` at line 233):**
  - Add `"OBSERVER_CLUSTER_SECRET"` to the `required = [...]` list at lines 285-291.
  - Pull from `${{ secrets.OBSERVER_CLUSTER_SECRET }}` as env at line 273-279.
  - Populate `values["secret"]["clusterSecret"]` and `values["cluster"]={"enabled": True}`.
  - Mask via `::add-mask::` immediately after read.

**`.github/workflows/multi-agent.yml`:** no required changes. Existing `go test ./... -race` (line 36) runs every test; `go.work` includes the new tests automatically. The `helm` job (line 54) runs the extended `chart_test.sh`.

### Data flow walkthroughs

**1. UI lists daemons:**
1. UI → LB → Pod B → `GET /api/commander/daemons`.
2. `ch.daemons` (`http.go:44`) calls `ch.hub.listDaemons(o)` — a new internal helper that consults `hub.sharedReg.listAll` when non-nil, else `hub.reg.daemons`.
3. `sharedReg.listAll` runs `SELECT ... WHERE last_seen_at > now() - 45s`. Returns full list across pods.

**2. UI runs a turn on a daemon owned by Pod A, request lands on Pod B:**
1. UI → LB → Pod B → `POST /api/commander/daemons/<id>/sessions/<sid>/turn`.
2. `ch.turn` (`http.go:209`) calls `hub.lookupDaemon(o, daemonID)` (new helper). First checks `hub.reg.lookup` (local hit → use existing code path). On miss, calls `hub.sharedReg.lookupRemote`. Returns `lookupResult{remote: true, peerURL: "http://10.0.1.42:8091"}`.
3. `turn` calls `hub.turns.begin(key)` locally; OK because Pod B has no entry. Cross-pod turn-in-flight dedup is a non-goal.
4. `SendCommandStream` (`proxy.go:84`) routes the remote case to `hub.forwardCli.streamCommand(ctx, peerURL, payload)`.
5. Pod A's `/forward` handler:
   - Validates HMAC + timestamp window.
   - Reads body (1 MiB cap).
   - Validates `daemon_id` is in Pod A's local registry (404 if not — sweep will clean stale row).
   - Calls `hub.sendCommandToLocal(ctx, o, daemonID, command, args, streaming=true)` — the new internal helper extracted from `SendCommand[Stream]`'s body that bypasses registry lookup.
   - Streams each emitted envelope back as `<len>\n<json>` via `http.Flusher`.
6. Pod B's forward client decodes and emits each envelope on the returned `<-chan commander.Envelope`. `ch.turn` writes them as SSE to the browser. The terminal frame routes through `routeFrame` on Pod A → triggers `invalidateDaemonSessions` on Pod A locally. The same terminal frame, after forwarding, also triggers `ch.turn`'s post-write `invalidateDaemonSessions` on Pod B. Net result: both pods have invalidated.

**3. Pod A crashes mid-turn:**
1. Pod B's forward client gets `io.EOF` mid-stream.
2. Synthesizes an `{type:"error", payload:{code:"backend_unavailable"}}` envelope, sends on the channel, closes it.
3. `ch.turn` handles via the existing `case <-chunkCh:` path → `finishTurnWithoutTerminal` → SSE error.
4. Sweep on Pod B (and other surviving pods) eventually deletes the orphan rows (>5min old). Meanwhile, `listAll` filters by 45-second `last_seen_at`, so the UI stops listing those daemons within a minute.
5. On Pod A restart, daemons reconnect; UPSERT (not blind INSERT) re-establishes the row with the new pod address.

**4. Pod A transient Postgres outage (heartbeat fails 60s):**
1. Heartbeat goroutine logs WARN + increments counter, continues.
2. `listAll` from any pod filters out Pod A's daemons after 45s (UI shows fewer daemons).
3. **Sweep does NOT delete** (sweep filter: >5min). Rows preserved.
4. PG recovers, Pod A's next heartbeat UPSERTs `last_seen_at = now()`. Daemons reappear in `listAll` immediately.

**5. Daemon fast reconnect — Pod A → Pod B:**
1. WS dies; daemon's wsclient reconnects within 1s; LB routes to Pod B.
2. Pod B `localReg.add(dc)` + `sharedReg.upsert(...)` → `INSERT ON CONFLICT DO UPDATE SET owning_instance_url='podB', last_seen_at=now()`.
3. Pod A's `ServeHTTP` deferred `sharedReg.remove(o, daemonID)` runs after the WS read loop exits. The DELETE's `WHERE owning_instance_url='podA'` filter affects 0 rows because the row now belongs to Pod B. Safe.
4. Pod A's heartbeat goroutine ticks once more, UPDATE affects 0 rows (filtered out by `owning_instance_url='podA'`). Heartbeat detects 0 rows + logs at DEBUG (not WARN — this is normal during reconnects), exits when `<-dc.done` fires.

**6. Postgres unreachable on a read:**
1. `sharedReg.listAll` returns `(nil, err)`.
2. `ch.daemons` returns HTTP 200 with body `{"daemons": []}` and header `X-Observer-Registry-Degraded: true`. UI shows "no daemons" rather than 500. Counter `observer.commanderhub.registry.errors{op="list"}` increments. Rate-limited WARN log.

### Error mapping (forwarding)

| Receiver state                                              | HTTP status | Caller behavior                                                       |
|-------------------------------------------------------------|-------------|-----------------------------------------------------------------------|
| HMAC/timestamp invalid                                      | 403         | Caller logs (WARN, no secret material) + returns `ErrDaemonGone`      |
| Receiver not in shared mode (got request anyway)            | 503         | Caller logs + returns `ErrDaemonGone`                                 |
| Daemon not in receiver's local registry                     | 404         | Caller returns `ErrDaemonNotFound` (UI 404); next sweep cleans row    |
| Body > 1 MiB                                                | 413         | Caller logs + returns `ErrDaemonGone`                                 |
| Daemon present, command sent OK, terminal returned          | 200         | Normal path                                                           |
| Daemon present, mid-stream connection drop                  | partial 200 | Caller injects synthetic error envelope on the channel                |
| Receiver returns 5xx unexpected                             | 500/502     | Caller logs + returns `ErrDaemonGone`                                 |

### Testing

**Unit (no Postgres):**
- `registry_shared_test.go` — `sharedRegistry` against `pgxmock`: `upsert` SQL shape; `lookupRemote` returns remote only when row fresh AND owned by a different URL; `remove` SQL includes `owning_instance_url` filter; `sweep` deletes only `>5min` rows.
- `forward_test.go` —
  - Round-trip via `httptest.Server`: client POSTs JSON; handler validates HMAC; non-streaming returns 200 with result; streaming sends N envelopes ending in terminal frame.
  - Wrong secret → 403; expired timestamp (>60s drift) → 403; body > 1 MiB → 413; receiver not in shared mode → 503.
  - `TestForwardCallerCancelPropagates` — slow stream, caller cancel, assert pending entry removed within 1s and TCP closed.
  - `TestForwardSlowReaderTriggersDropCounter` — 1000 envelopes vs throttled reader, assert drop counter > 0 + synthetic `truncated` envelope delivered.
  - Cap test: client sending `length=2^40` to receiver → receiver terminates with 4xx; sym test other direction.

**Integration (Postgres via `OBSERVER_POSTGRES_TEST_DSN` env-skip pattern, mirroring `authstore/postgres_test.go:15-23`):**
- `multi_pod_test.go` —
  - Boot two `Hub` instances against one Postgres + shared `clusterSecret`.
  - Boot one mock daemon connecting to Hub A.
  - Assert Hub B `listAll(o)` returns 1 row with `owning_instance_url` pointing at A.
  - Hub B `SendCommand("list_sessions")` succeeds via forwarding; payload matches.
  - Kill Hub A; assert sweep on Hub B removes the row after >5min (use injected `time.Now`-faker to avoid waiting).
  - Reconnect daemon to Hub B; assert subsequent `listAll` from Hub A (relaunched) sees correct `owning_instance_url=hub-B`.
  - Rolling-update simulation: start Hub A (new code), Hub B (legacy code = `sharedReg=nil`). Assert daemons on Hub B remain invisible to Hub A's `listAll` (documented limitation), and daemons on Hub A correctly listed by Hub A.

**Local manual repro:**
- `dev/compose.multi-observer.yaml` boots Postgres + 2 observers + nginx LB.
- New `dev/README.md` documents `docker compose -f dev/compose.multi-observer.yaml up -d`.

**Existing tests:** all `*_test.go` callers of `hub.reg.add(...)` / `hub.reg.daemons(...)` (enumerated above) continue working because the `Hub.reg *localRegistry` field type is preserved and `localRegistry` has the same method set as the old `*registry`.

### Verification

**Smoke (CI, automated):**
- `chart_test.sh` asserts cluster env + internal Service rendered (or fail-fast triggered) for the matrix of `replicaCount` × `cluster.enabled`.
- `helm` job + `observer-deploy.yml smoke` (post-change) — 2 pods come up, both pass `/readyz` via per-pod IP probe.

**Manual against smoke cluster:**
```sh
# 1. Both pods running with cluster envs.
kubectl -n dev-yuzishu get pods -l app.kubernetes.io/instance=observer-ci-<run> \
  -l app.kubernetes.io/component=observer
kubectl -n dev-yuzishu describe pod <pod-name> | grep -E 'POD_IP|OBSERVER_ADVERTISE_URL|OBSERVER_CLUSTER_SECRET'

# 2. Internal Service exists, not exposed externally.
kubectl -n dev-yuzishu get svc | grep observer-internal   # should exist
curl -sf https://<public-host>/api/commander/_internal/forward   # should 404

# 3. Table created.
kubectl -n dev-yuzishu exec <pg-pod> -- psql "$OBSERVER_DATABASE_URL" -c '\d commander_daemons'

# 4. Connect driver-agent at the public host. 30 GETs → daemon count stable.
for i in {1..30}; do
  curl -s -H "Authorization: Bearer $TOKEN" "https://<public-host>/api/commander/daemons" \
    | jq '.daemons | length'
done | sort -u | wc -l   # → expect 1

# 5. POST a turn against the daemon, 10x. None should 404.
for i in {1..10}; do
  curl -sf -X POST -H "Authorization: Bearer $TOKEN" \
    "https://<public-host>/api/commander/daemons/<id>/sessions/<sid>/turn" \
    -d '{"prompt":"hello"}' >/dev/null || echo "FAIL on iter $i"
done
```

**Local:**
```sh
docker compose -f dev/compose.multi-observer.yaml up -d
# Connect driver-agent at http://localhost:8090 (nginx LB).
for i in {1..30}; do
  curl -s http://localhost:8090/api/commander/daemons | jq '.daemons | length'
done | sort -u | wc -l   # → 1
```

**Automated regression:**
```sh
go test ./internal/commanderhub/... -race -count=1
OBSERVER_POSTGRES_TEST_DSN=... go test -run TestMultiPod ./internal/commanderhub/... -race
```

### Out of scope (follow-up issues)

- **Multi-pod `turnStateStore`** — turn-in-flight guard remains per-pod. Two tabs against two pods both POSTing the same `/turn` both succeed; daemon's session_turn is the final dedup. Open follow-up.
- **mTLS between pods** — current: shared cluster secret + HMAC. Adequate for the threat model (cluster-internal traffic + non-public Service). mTLS via cert-manager is a separate sprint.
- **Headless-service-based addressing** — pod IP via downward API is simpler and adequate. Migrate to pod-hostname.headless-service DNS if pod IP churn ever becomes a problem.

### Rollout sequence

Strict ordering to avoid the mixed-version inconsistency window:

1. **Pre-merge:** ops adds `OBSERVER_CLUSTER_SECRET` to GitHub repo secrets and to the production `existingSecret` (`observer-production-secret`) under key `cluster-secret`.
2. **Merge PR.** CI builds the image and runs smoke at `replicaCount=2` with auto-generated secret.
3. **Production release deploy (`workflow_dispatch` with `target: release`):** Helm `upgrade --install` with rolling-update strategy `maxUnavailable: 0, maxSurge: 100%` (set in chart) — all old pods stay alive until all new pods are Ready. This collapses the mixed-version window. Once all new pods are up, old pods drain; daemon WS reconnects re-land on new pods.
4. **Post-deploy verification (manual against production):** the curl loops above.

Rollback: `helm rollback observer <prev>`. The new `commander_daemons` table is left behind (no down migration in the chart); rows become stale and irrelevant. A subsequent re-roll-forward consumes them harmlessly.
