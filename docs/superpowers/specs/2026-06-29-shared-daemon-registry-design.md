# Shared commanderhub daemon registry across observer instances

**Issue:** [#49](https://github.com/agentserver/loom/issues/49) — commanderhub daemon registry not shared across observer instances; the commander UI shows daemons intermittently when the observer scales horizontally.

> Revision history: v1 (initial), v2 (post-Claude adversarial review — fixes B1–B4, M1–M11, m1–m10), **v3 (post-Codex review — fixes additional 9 BLOCKERs + 14 MAJORs)**.

## Context

The observer deploys with `replicaCount: 2` in dev (`deploy/charts/observer/values.yaml:1`) and `replicaCount: 3` in production (`values-production.example.yaml:1`). The commanderhub `Hub` keeps every live daemon WebSocket in a per-process map (`internal/commanderhub/registry.go:86-93`). A `daemon-link` WS is naturally sticky — it lands on one pod and stays there — but the read paths the commander UI uses (`GET /api/commander/daemons`, `/tree`, `/sessions`, `POST /daemons/{id}/sessions/{sid}/turn`) are plain stateless HTTP requests. The load balancer routes each one to an arbitrary pod, and that pod can only see the daemons whose WS happened to land on it. The result, observed in production at `loom.nj.cs.ac.cn:10062`:

- A user with one driver-agent + one slave-agent sees the daemon list change on every refresh.
- `POST .../turn` returns 404 whenever the request lands on a non-owning pod.
- Daemon TCP connections and stderr stay healthy throughout — the bug is purely on the observer side.

The fix shares enough state between observer pods that any pod can answer any commander HTTP request consistently. The v3 scope **closes every observable read inconsistency** — not just the daemon list, but the per-daemon session list and turn state too. Specifically: daemon registry shared via Postgres, command/turn forwarded to the WS-owning pod over an internal HTTP listener, `turnStateStore` is replaced with a Postgres-backed implementation, `sessionListCache` is disabled in shared mode (it's a 10s in-memory cache whose cross-pod invalidation cost dwarfs its single-pod hit-rate benefit). Multi-pod turn-in-flight dedup falls out of the shared turn-state.

## Approach

Four layers:

1. **Postgres-backed registry of online daemons** (`commander_daemons` table). Owner pod UPSERTs on connect, heartbeats every 15 s with `WHERE owning_instance_url=$pod` ownership guard, DELETEs on graceful disconnect (also guarded), and sweeps rows older than 5 min. Reads (`/daemons`, `/tree`, `/sessions`) consult this table.

2. **Internal pod-to-pod command forwarding** over a **separate dedicated listener** (`:8091` by default) that is **never exposed by Ingress/HTTPRoute**. Auth: HMAC over `(timestamp, nonce, body)` with a 60 s window and a receiver-side nonce LRU (replay-proof within the window). Supports current+previous secret pair for zero-downtime rotation. Wire format: length-prefixed JSON envelopes capped at **3 MiB** per envelope (covers `MaxFilePreviewBytes = 2 MiB` plus JSON overhead — `internal/commander/protocol.go:19`).

3. **Postgres-backed `turnStateStore`** (`commander_turns` table). Owner pod's `routeFrame` is the single writer: it interprets each envelope using a stored `pendingEntry.command` + session id, runs the existing turn-state machine, and UPSERTs the row. Read paths (`tree.go::cachedSessionRows`, etc.) read by `(owner, daemon_id, session_id)`. `turns.begin()` becomes a row-level lock via `INSERT … ON CONFLICT … WHERE state IN ('idle','done','error','awaiting_approval','disconnected')`.

4. **`sessionListCache` disabled when shared mode is active.** The cache exists to spare daemons repeated `list_sessions` traffic when a UI tab refreshes quickly; the cost in shared mode (cross-pod invalidation, stale lists for up to 10s) is worse than just paying the daemon hit. In single-pod mode the cache stays exactly as-is.

All four layers are **fail-closed on partial config**: any mix-up of `cluster.advertise_url{,_env}` set + `cluster.secret_env` empty (or vice versa) is a fatal `validateConfig` error at observer startup, NOT a silent fallback to single-pod mode. The default `cluster.internal_listen_addr=":8091"` is **applied only when `cluster.enabled=true` resolves true**, so it cannot trigger the partial-config error on legitimate single-pod deployments.

### Component map

| Component                                            | File                                                                    | Change                                                                       |
|------------------------------------------------------|-------------------------------------------------------------------------|------------------------------------------------------------------------------|
| Postgres DDL — `commander_daemons` + `commander_turns` + `commander_forward_nonces` | `internal/commanderhub/authstore/schema_postgres.sql`                   | add three tables + indexes                                                   |
| Migration runner                                     | `internal/commanderhub/authstore/migrate.go`                            | unchanged (same `db.Exec(schema)` runs new DDL)                              |
| Test conformance hook                                | `internal/commanderhub/authstore/postgres_test.go`                      | extend existing `OBSERVER_POSTGRES_TEST_DSN`-skip conformance to assert new tables and constraints |
| Registry struct → split                              | `internal/commanderhub/registry.go`                                     | rename `registry` → `localRegistry`; `Hub.reg` field stays named `reg` with the same method surface (callers `hub.reg.add(...)`, `hub.reg.daemons(...)` continue to compile); add a separate `sharedRegistry` type and `Hub.sharedReg` field |
| Heartbeat goroutine                                  | `internal/commanderhub/hub.go` `ServeHTTP`                              | started after `sharedReg.connectUpsert`; tied to `dc.done`; runs ownership-guarded UPSERT every 15 s; `Wait()`s for the goroutine to exit before invoking `sharedReg.remove` in defers |
| Turn-state store (shared)                            | `internal/commanderhub/turn_state.go`, new `turn_state_pg.go`           | extract `turnStateStore` to an interface `turnStateBackend`; in-memory impl unchanged; new Postgres impl |
| Turn-state writer on owning pod                      | `internal/commanderhub/hub.go` `routeFrame`                             | when `pendingEntry.command == "session_turn"` and frame is terminal/status-event, call `hub.turns.updateFromEnvelope(...)` |
| Session-cache gating                                 | `internal/commanderhub/hub.go` `NewHub`, `tree.go`                      | when `sharedReg != nil`, `sessionCache` set to nil; `cachedSessionRows` checks for nil and skips caching |
| Forwarding client                                    | `internal/commanderhub/forward_client.go` (new)                         | called by `proxy.go` `SendCommand`/`SendCommandStream` when local lookup misses and shared lookup returns remote |
| Forwarding HTTP handler                              | `internal/commanderhub/forward_server.go` (new)                         | mounts `/forward` on the INTERNAL mux (separate `http.ServeMux`); calls `sendCommandToLocal` / `sendCommandStreamToLocal` |
| Internal codec (length-prefixed JSON)                | `internal/commanderhub/forward_codec.go` (new)                          | 3 MiB cap per envelope; decimal-ASCII length + `\n` + JSON bytes             |
| `sendCommandToLocal` / `sendCommandStreamToLocal`    | `internal/commanderhub/proxy.go`                                        | factor out the post-lookup body of `SendCommand[Stream]` into local-only helpers; `SendCommand[Stream]` now does lookup → local OR forward |
| Read-path helpers                                    | `internal/commanderhub/hub.go`                                          | `(h *Hub).listDaemons(ctx, o) []DaemonInfo`, `(h *Hub).lookupDaemon(ctx, o, daemonID) (lookupResult, error)`; used by `daemons`, `CommanderTree`, `FanOutSessions`, `ch.turn`'s guard |
| Hub wiring                                           | `internal/commanderhub/wiring.go`, `hub.go`                             | `MountAll(publicMux, internalMux, resolver, agentserverURL, store, cluster ClusterRuntime)`; `internalMux=nil` ⇒ skip forward endpoint; `NewHub(resolver)` keeps signature; in-mode wiring via `Hub.attachSharedRegistry(...)` |
| Observer config schema                               | `cmd/observer-server/main.go`                                           | new `Cluster ClusterConfig` field + `validateConfig` rules                   |
| Observer server lifecycle                            | `cmd/observer-server/main.go`                                           | when cluster enabled: build a second `*http.Server` for the internal listener (no `WriteTimeout` — see streaming-safe section); start both with `errgroup`; coordinated `Shutdown(ctx)` |
| Public listener streaming-safe timeout fix          | `cmd/observer-server/main.go::newHTTPServer`                            | pre-existing bug: `WriteTimeout: 60s` is incompatible with 10-min SSE turns. Split into `newPublicHTTPServer` (no `WriteTimeout`, retains `ReadHeaderTimeout`+`IdleTimeout`) and `newInternalHTTPServer` (same posture). Public-listener change is needed regardless of this PR but folded in to avoid divergent posture |
| Helm chart values                                    | `deploy/charts/observer/values.yaml`                                    | new `cluster:` block; flip dev `replicaCount` 2 → 1                          |
| Helm chart values-production                         | `deploy/charts/observer/values-production.example.yaml`                 | `cluster.enabled: true`; doc `cluster-secret` key in `existingSecret`        |
| Helm chart secret + deployment                       | `deploy/charts/observer/templates/{secret.yaml,deployment.yaml}`        | render `cluster:` into observer.yaml (only inside the `secret.create && !existingSecret` gate, where observer.yaml lives); wire `POD_IP` + `OBSERVER_CLUSTER_SECRET` env vars; internal port |
| Helm chart **validation template** (always rendered) | `deploy/charts/observer/templates/_validate.yaml` (new)                 | top-level `{{- fail }}` guard for `replicaCount > 1 && store.driver=postgres && !cluster.enabled` — runs regardless of `secret.create` / `existingSecret`. Template itself emits no resources (`{{- "" -}}` body). |
| Helm chart pod init container                        | `deploy/charts/observer/templates/deployment.yaml`                      | merge with existing Postgres-wait initContainers (one `initContainers:` block, conditional contents); assert `OBSERVER_CLUSTER_SECRET` non-empty |
| Helm chart internal Service (per-pod headless)       | `deploy/charts/observer/templates/service.yaml`                         | second `Service` named `<release>-observer-headless` with `clusterIP: None, publishNotReadyAddresses: true` so DNS resolves per-pod-IP (the chart's existing ClusterIP load-balances and would break forwarding) |
| Helm chart Ingress/HTTPRoute hardening               | `deploy/charts/observer/templates/{ingress.yaml,httproute.yaml}`        | concrete, supported deny rules (see §"Ingress hardening" for tested syntax)  |
| Chart tests                                          | `deploy/charts/observer/tests/chart_test.sh`                            | render assertions: cluster env + internal Service + fail-fast triggers       |
| CI deploy workflow                                   | `.github/workflows/observer-deploy.yml`                                 | generate `clusterSecret` + `clusterSecretPrev` in smoke; `replicaCount: 2`; smoke probe resolves pod IPs in the GitHub runner (kubectl in CI image) and renders one wget Job per pod IP; release requires `OBSERVER_CLUSTER_SECRET[_PREV]` repo secrets |
| Multi-pod regression test                            | `internal/commanderhub/multi_pod_test.go` (new)                         | two `Hub` instances + Postgres via existing `OBSERVER_POSTGRES_TEST_DSN`-skip pattern (with `t.Skip` fallback); daemon connects to A, B sees it and forwards `list_sessions` + `session_turn` |
| Forwarding-only tests                                | `internal/commanderhub/forward_test.go` (new)                           | `httptest`-driven handler/client round-trip; auth, replay, nonce, cap, cancellation, slow-reader tests |
| `sharedRegistry` SQL tests                           | `internal/commanderhub/registry_shared_test.go` (new)                   | go-sqlmock against `*sql.DB`; assert ownership-guarded UPSERT/DELETE/sweep SQL; assert peer-only `lookupRemote` |
| Local-repro compose                                  | `dev/compose.multi-observer.yaml` (new) + `dev/README.md` (new)         | extends existing `dev/compose.distributed.yaml` patterns: PG + 2 observers + nginx LB |
| Deploy docs                                          | `multi-agent/deploy/README.md`                                          | pre-rollout instructions: set `OBSERVER_CLUSTER_SECRET` in repo secrets + `cluster-secret` key in `existingSecret`; rotation procedure |

### Postgres schema

Added to `internal/commanderhub/authstore/schema_postgres.sql`. Same migration script and same gating as the existing commander tables (`cmd/observer-server/main.go:264-268`), so existing single-pod Postgres deployments pay the DDL cost once at upgrade and otherwise see no behavior change.

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

CREATE TABLE IF NOT EXISTS commander_turns (
    user_id            text        NOT NULL,
    workspace_id       text        NOT NULL,
    daemon_id          text        NOT NULL,
    session_id         text        NOT NULL,
    state              text        NOT NULL, -- 'idle'|'queued'|'answering'|'awaiting_approval'|'done'|'error'|'disconnected'
    awaiting_approval  boolean     NOT NULL DEFAULT false,
    active_worker      boolean     NOT NULL DEFAULT false,
    message            text        NOT NULL DEFAULT '',
    updated_at         timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, workspace_id, daemon_id, session_id),
    CONSTRAINT commander_turns_state_enum CHECK (
        state IN ('idle','queued','answering','awaiting_approval','done','error','disconnected')
    )
);
CREATE INDEX IF NOT EXISTS commander_turns_owner_idx
    ON commander_turns (user_id, workspace_id, daemon_id);
CREATE INDEX IF NOT EXISTS commander_turns_updated_idx
    ON commander_turns (updated_at);

CREATE TABLE IF NOT EXISTS commander_forward_nonces (
    nonce       text        PRIMARY KEY,
    received_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS commander_forward_nonces_received_idx
    ON commander_forward_nonces (received_at);
```

`commander_forward_nonces` lets the cluster reject replays across pods: pod A's accepted nonce blocks pod B from accepting the same nonce within the 60 s window. Sweeper trims rows older than 120 s (2× the window). For a small fleet this table grows to maybe 10k rows steady-state.

Rollback path: `internal/commanderhub/authstore/schema_postgres_rollback.sql` (new) with `DROP TABLE IF EXISTS commander_forward_nonces; DROP TABLE IF EXISTS commander_turns; DROP TABLE IF EXISTS commander_daemons;`. Helm `--migrate-only` does not auto-down; ops run psql manually if rolling back across this PR.

### Hub struct + wiring

`Hub` grows nilable fields; `reg` field name preserved:

```go
type Hub struct {
    resolver     identity.Resolver
    upgrader     websocket.Upgrader
    reg          *localRegistry   // same field name as today; same method surface; type renamed
    sharedReg    *sharedRegistry  // nil in single-pod / legacy mode
    forwardCli   *forwardClient   // nil iff sharedReg == nil
    turns        turnStateBackend // interface; in-memory by default, Postgres-backed in shared mode
    sessionCache *sessionListCache // nil in shared mode (cache disabled cluster-wide)
    cmdSeq       atomic.Int64
    TurnTimeout  time.Duration
}
```

`NewHub(resolver identity.Resolver) *Hub` signature unchanged (preserves all 30+ `hub.reg.*` test sites enumerated by `grep -nE '\bhub\.reg\b' internal/commanderhub/*_test.go`). `MountAll` is what plugs in the shared bits via a new internal method:

```go
func (h *Hub) attachSharedRegistry(sr *sharedRegistry, fc *forwardClient, turns turnStateBackend) {
    h.sharedReg = sr
    h.forwardCli = fc
    h.turns = turns
    h.sessionCache = nil // see §"Session cache gating"
}
```

`MountAll` v3 signature:

```go
// publicMux receives /api/daemon-link + /api/commander/*.
// internalMux receives /forward (nil in single-pod mode → no forwarding endpoint).
func MountAll(
    publicMux *http.ServeMux,
    internalMux *http.ServeMux,
    resolver identity.Resolver,
    agentserverURL string,
    store authstore.Store,
    cluster ClusterRuntime,
)

type ClusterRuntime struct {
    DB                 *sql.DB    // nil → shared mode off
    AdvertiseURL       string     // empty → shared mode off
    Secret             []byte     // current secret
    PrevSecret         []byte     // previous secret accepted during rotation (nil OK)
    InternalListenAddr string     // for log only; main.go is what binds
}
```

`Hub.Close(ctx context.Context) error` (new) shuts down the forward client (`forwardCli.transport.CloseIdleConnections()`), cancels any heartbeat goroutines (already tied to `dc.done`, so this is mostly a no-op except for the forward client). Called by `observerweb` server shutdown chain or by `cmd/observer-server/main.go` when both servers' `Shutdown` returns.

**Caller compat:** `internal/observerweb/server.go:111` currently calls `commanderhub.MountAll(mux, resolver, opts.AgentserverURL, opts.AuthStore)`. The signature change is breaking but only one caller exists. `internal/commanderhub/wiring_test.go:21` is the second caller (test); it gets updated. **Action item:** update both call sites; grep `MountAll\(` confirms only these two.

### Observer server lifecycle (separate listener)

`cmd/observer-server/main.go` currently builds one `http.Server` (`main.go:246`, `srv := newHTTPServer(...)`). v3:

```go
// Build options:
opts := observerWebOptions(cfg, objects)
opts.AuthStore = authStore
clusterRuntime, err := buildClusterRuntime(cfg, st.DB())  // empty if !cluster.enabled
if err != nil { log.Fatal(err) }
opts.Cluster = clusterRuntime

publicHandler, internalHandler := observerweb.NewWithResolverOptions(st, usHandler, resolver, opts)

publicSrv  := newPublicHTTPServer(cfg.ListenAddr, withHealth(publicHandler, dbPing))
var internalSrv *http.Server
if clusterRuntime.AdvertiseURL != "" {
    internalSrv = newInternalHTTPServer(cfg.Cluster.InternalListenAddr, internalHandler)
}

// errgroup: any ListenAndServe error triggers Shutdown of the others.
g, ctx := errgroup.WithContext(rootCtx)
g.Go(func() error { return runServer(ctx, publicSrv) })
if internalSrv != nil { g.Go(func() error { return runServer(ctx, internalSrv) }) }
log.Fatal(g.Wait())
```

`observerweb.NewWithResolverOptions` is updated to return `(publicHandler, internalHandler http.Handler)` where `internalHandler == nil` if cluster disabled. **Caller compat:** the two current callers (`server.go:65, 76`) are in-package convenience constructors using struct-keyed `Options{}`; they get updated to return both handlers (callers in tests already use the multi-return form trivially).

**Streaming-safe timeouts** (also fixes pre-existing pre-PR bug):

```go
func newPublicHTTPServer(addr string, h http.Handler) *http.Server {
    return &http.Server{
        Addr:              addr,
        Handler:           h,
        ReadHeaderTimeout: 5 * time.Second,
        ReadTimeout:       0,                  // SSE turn POSTs can stream
        WriteTimeout:      0,                  // 10-min turn SSE
        IdleTimeout:       120 * time.Second,
    }
}

func newInternalHTTPServer(addr string, h http.Handler) *http.Server {
    return &http.Server{
        Addr:              addr,
        Handler:           h,
        ReadHeaderTimeout: 5 * time.Second,
        ReadTimeout:       0,                  // chunked forward stream
        WriteTimeout:      0,                  // chunked forward stream
        IdleTimeout:       120 * time.Second,
    }
}
```

The old `newHTTPServer` (with 60s read/write timeouts) is retained ONLY for the unrelated `/readyz`/`/healthz` health server if used elsewhere — verify there are no other callers via `grep -nE '\bnewHTTPServer\b' cmd/observer-server`. If it's only used for the listening server, remove it. Per-turn ctx still bounds runaway streams: `Hub.TurnTimeout = 10m` (`hub.go:50`) — no change.

### Registry split

Existing `*registry` → `*localRegistry`, same methods, same behavior. `Hub.reg`'s **method surface stays identical**; only the underlying type is renamed. Tests calling `hub.reg.add(...)` / `hub.reg.daemons(...)` recompile unchanged.

`*sharedRegistry`:

```go
type sharedRegistry struct {
    db                  *sql.DB
    advertiseURL        string
    heartbeatEvery      time.Duration // 15s
    onlineTTL           time.Duration // 45s
    deleteAfter         time.Duration // 5min
    sweepEvery          time.Duration // 30s
}

// connectUpsert claims ownership on a new WS connect. INSERT … ON CONFLICT …
// DO UPDATE without an owning-pod guard — connect is allowed to take ownership
// because the daemon reconnected to us.
func (s *sharedRegistry) connectUpsert(ctx context.Context, dc *daemonConn) error

// heartbeatUpsert refreshes last_seen_at ONLY when this pod still owns the row.
//   INSERT INTO commander_daemons (...) VALUES (...)
//   ON CONFLICT (user_id, workspace_id, daemon_id) DO UPDATE
//     SET last_seen_at = now(),
//         short_id     = EXCLUDED.short_id, … etc
//     WHERE commander_daemons.owning_instance_url = EXCLUDED.owning_instance_url;
// 0 rows affected ⇒ another pod took ownership; heartbeat exits.
func (s *sharedRegistry) heartbeatUpsert(ctx context.Context, dc *daemonConn) (claimed bool, err error)

// remove DELETEs only when owning_instance_url matches this pod (so a daemon
// already reconnected to a sibling pod isn't unlinked).
func (s *sharedRegistry) remove(ctx context.Context, o owner, daemonID string) error

// lookupRemote returns peerURL+info iff a fresh row exists AND its
// owning_instance_url != this pod's advertiseURL. Returns ok=false otherwise.
func (s *sharedRegistry) lookupRemote(ctx context.Context, o owner, daemonID string) (peerURL string, info DaemonInfo, ok bool, err error)

// listAll returns every fresh row for owner. Used by /daemons, /tree, /sessions.
func (s *sharedRegistry) listAll(ctx context.Context, o owner) ([]DaemonInfo, error)

// sweep deletes rows older than deleteAfter (5min). NOT the 45s online-threshold.
// Sized so that a transient PG outage on the owning pod cannot let a peer's
// sweep delete the row.
func (s *sharedRegistry) sweep(ctx context.Context) error

// sweepNonces deletes commander_forward_nonces older than 120s.
func (s *sharedRegistry) sweepNonces(ctx context.Context) error
```

Online-for-reads (`last_seen_at > now() - 45s`) and deletable-by-sweep (`last_seen_at < now() - 5min`) are deliberately separated: a 60s PG hiccup on pod A makes pod A's daemons briefly invisible (within bound) but they are never deleted. When PG recovers, the next heartbeat's UPSERT-with-ownership-guard sees 0 affected rows because the row still exists with the same owning_instance_url — wait, that's a bug: 0 affected rows would mean "another pod took ownership," which is wrong. **The SQL above must be re-read carefully**: the `WHERE` clause runs only when there's a conflict; the row's `owning_instance_url` is compared against `EXCLUDED.owning_instance_url` which is the new (= same pod) value, so the condition `commander_daemons.owning_instance_url = EXCLUDED.owning_instance_url` holds whenever this pod hasn't been displaced. Affected rows = 1 in the normal case; 0 only when another pod has claimed it. Correct.

**Daemon teardown ordering** (`hub.go:130-134` defers):

```go
h.reg.add(dc)
hbCtx, hbCancel := context.WithCancel(context.Background())
hbDone := make(chan struct{})
if h.sharedReg != nil {
    if err := h.sharedReg.connectUpsert(ctx, dc); err != nil { /* log + continue */ }
    go func() {
        defer close(hbDone)
        h.sharedReg.runHeartbeat(hbCtx, dc) // ticks until ctx done OR ownership lost
    }()
}
defer h.reg.remove(o, dc.id)
defer h.invalidateDaemonSessions(o, dc.id)
defer close(dc.done)
defer dc.failAllPending()
defer func() {
    if h.sharedReg != nil {
        hbCancel()
        <-hbDone                            // wait for heartbeat goroutine to exit
        _ = h.sharedReg.remove(removeCtx, o, dc.id) // ownership-guarded DELETE
    }
}()
```

`hbCancel + <-hbDone` ensures the heartbeat goroutine has exited before the DELETE runs, so the heartbeat cannot resurrect the row between the DELETE and the WS goroutine return.

### Forwarding: client, server, codec

#### Internal mux — separate `http.ServeMux`

The forward endpoint is mounted on a **second mux** that is **never** registered on the public ServeMux. The chart exposes the internal mux via a per-pod-addressable Service (see §"Internal Service"), not via Ingress. The public Ingress/HTTPRoute templates also add a hardening rule (§"Ingress hardening") so even if a future change accidentally re-mounts `/forward` on the public mux, the edge will 404 it.

#### Per-pod DNS — headless Service

A standard `ClusterIP` Service load-balances across pods, which would defeat forwarding (a forward request from pod B would round-trip back to pod B sometimes). The chart adds a **headless Service** (`clusterIP: None, publishNotReadyAddresses: true`) so DNS resolves per-pod. The advertised URL stays `http://$(POD_IP):8091` — pod-IP is what each pod sees about itself via the downward API, and the headless Service makes those IPs DNS-discoverable for any non-routing observability needs. Forwarding itself dials the IP directly; it does not depend on DNS.

**Loop prevention:** if `peer URL == advertiseURL` (misconfiguration / single-pod-but-cluster-enabled), forward client refuses with `ErrDaemonNotFound` and logs ERROR. Same applies if peer URL equals `127.0.0.1` / `localhost` against an `advertiseURL` of the form `http://10.x:port`.

#### Auth — HMAC + nonce

The forward request carries three headers:

```
X-Observer-Cluster-Timestamp: <unix-seconds-decimal>
X-Observer-Cluster-Nonce:     <32 random hex chars>
X-Observer-Cluster-Auth:      <hex(hmac_sha256(secret, timestamp || "\n" || nonce || "\n" || body))>
```

Receiver:
1. Reject (403) if `|now - timestamp| > 60s` (replay window).
2. **Atomically insert nonce** into `commander_forward_nonces` (`INSERT … ON CONFLICT DO NOTHING`); reject 403 if conflict (replay within window).
3. Read body (capped at 3 MiB by `io.LimitReader`); reject 413 on overrun.
4. Compute HMAC over `(ts || "\n" || nonce || "\n" || body)`; compare with both `Secret` and (if non-nil) `PrevSecret` using `crypto/subtle.ConstantTimeCompare`. Reject 403 on mismatch with both.
5. Never log auth headers or secret material. Error responses are `{"error":"unauthorized"}` with no detail.

Sender:
- Computes HMAC with `Secret` (current). During rotation, the previous secret is honored by all receivers; rotation procedure: ops sets `PrevSecret = oldSecret; Secret = newSecret` on all pods one rollout, then `PrevSecret = nil` on the next.

#### Request shape

```
POST /forward HTTP/1.1   (on the internal listener)
Headers: as above
Content-Type: application/json
Content-Length: <N>      # capped at 3 MiB; receiver returns 413 if exceeded

{
  "user_id":      "<owner.userID>",
  "workspace_id": "<owner.workspaceID>",
  "daemon_id":    "<daemon-id>",
  "command":      "session_turn" | "list_sessions" | "get_session" | "list_files" | "read_file",
  "args":         {...},
  "streaming":    true | false,
  "timeout_ms":   600000        // bounded by receiver to Hub.TurnTimeout
}
```

#### Response — non-streaming

```
200 OK + Content-Type: application/json + {"result": <raw command_result payload>}
```
or
```
200 OK + {"error": {"code": "<commander.ErrCode*>", "message": "..."}}
```

The forward **client** maps `{"error":...}` back to `*DaemonError` (preserving `commander.ErrCodeSessionNotFound`, `ErrCodeInvalidRequest`, etc.) so `http.go::writeSendCmdError` (`http.go:190-207`) continues to map daemon-originated errors to the correct HTTP status (404 for session_not_found, 400 for invalid_request, etc.). **Test coverage:** `forward_test.go::TestForwardErrorCodeRoundTrip`.

#### Response — streaming

`Transfer-Encoding: chunked`. Body is a sequence of `<decimal-ascii-length>\n<envelope-json-bytes>`. Receiver reads ASCII digits until `\n` (max 8 digits, cap `length ≤ 3 MiB`), then reads exactly that many bytes. Each chunk MUST parse as a single `commander.Envelope`. Stream ends on EOF (terminal frame seen) or upstream cancel (see §"Cancellation propagation").

#### Back-pressure

The forwarding server's drain goroutine wraps the local channel in a **closeable wrapper channel** with buffer 256:

```go
// sendCommandStreamToLocal is the factored-out post-lookup body of
// SendCommandStream. It does NOT depend on hub.reg.lookup — caller has
// the *daemonConn already.
//
// outBuffer chooses the wrapper-channel size; 16 for direct browser SSE
// (existing default), 256 for forwarding receivers (larger pod-to-pod buffer).
func (h *Hub) sendCommandStreamToLocal(ctx context.Context, dc *daemonConn, command string, args json.RawMessage, outBuffer int) (<-chan commander.Envelope, error)
```

The forwarding receiver's drain calls `sendCommandStreamToLocal(ctx, dc, command, args, 256)`. The `out` channel IS closed by `sendCommandStreamToLocal`'s wrapper goroutine on terminal/cancel/disconnect (matching today's `proxy.go:103: defer close(out)`), so the drain loop's `case env, ok := <-out` reliably fires `ok=false` to exit. **`pendingEntry.ch` is still never closed** — the wrapper channel is the only thing closed, identical to today's pattern.

**Drop telemetry:** the forwarding receiver's drain goroutine counts each time it had to drop a non-terminal envelope (when the HTTP body writer was blocked AND the wrapper buffer was full). Counts surface as a structured log line at WARN, rate-limited to once per (daemon_id, command) per second, with format `{"event":"forward.dropped","daemon_id":...,"command":...,"count":N}`. After the first drop in a stream, a synthetic `{type:"event",payload:{event_kind:"truncated",text:"observer-side buffer overflow"}}` envelope is sent at the next opportunity so the UI shows a visible gap.

#### Cancellation propagation

1. Browser closes SSE → Pod B's `ch.turn` `r.Context().Done()` fires.
2. Pod B's forward client cancels its outbound `http.Request` ctx → Go's transport closes the underlying TCP connection.
3. Pod A's forward server: a watcher goroutine selects on `r.Context().Done()` (Go's net/http fires this on TCP close) and cancels the inner ctx passed to `sendCommandStreamToLocal`.
4. `sendCommandStreamToLocal`'s wrapper goroutine selects on `<-ctx.Done()`, calls `dc.removePending(cmdID)` (frees the daemon-side slot, unblocks `routeFrame`'s terminal sends via the per-entry cancel), and closes `out`.
5. Forwarding server's drain loop reads `ok=false` from `out`, exits.

Test: `forward_test.go::TestForwardCallerCancelPropagates` opens a stream that emits one envelope every 50ms, cancels caller ctx at 200ms, asserts `removePending` runs within 1s by mocking the daemon side.

### Forward-aware command path (proxy.go)

`SendCommand` and `SendCommandStream` are restructured:

```go
func (h *Hub) SendCommand(ctx context.Context, o owner, daemonID, command string, args json.RawMessage) (json.RawMessage, error) {
    if dc, ok := h.reg.lookup(o, daemonID); ok {
        return h.sendCommandToLocal(ctx, dc, command, args)
    }
    if h.sharedReg == nil {
        return nil, ErrDaemonNotFound
    }
    peerURL, _, ok, err := h.sharedReg.lookupRemote(ctx, o, daemonID)
    if err != nil { return nil, err }
    if !ok { return nil, ErrDaemonNotFound }
    return h.forwardCli.send(ctx, peerURL, forwardRequest{
        Owner: o, DaemonID: daemonID, Command: command, Args: args, Streaming: false,
    })
}
```

`SendCommandStream` is analogous, but the forward path returns a `<-chan commander.Envelope` fed by the forward client's decoder goroutine. **`FanOutSessions`** (`proxy.go:156`) is updated to call `h.listDaemons(ctx, o)` (which consults shared registry) instead of `h.reg.daemons(o)`, so it asks every online daemon across all pods.

### Read-path helpers

```go
// listDaemons consults shared registry if attached, else local map.
// Used by ch.daemons, CommanderTree, FanOutSessions.
func (h *Hub) listDaemons(ctx context.Context, o owner) ([]DaemonInfo, error)

// lookupDaemon mirrors SendCommand's lookup logic; used by ch.turn's
// existence guard.
type lookupResult struct {
    Local   *daemonConn // non-nil iff owned by this pod
    PeerURL string      // non-empty iff Local == nil and a remote pod has it
    Info    DaemonInfo  // populated for both cases
}
func (h *Hub) lookupDaemon(ctx context.Context, o owner, daemonID string) (lookupResult, bool, error)
```

`ch.turn`'s existence guard (`http.go:226`) changes:

```go
res, ok, err := ch.hub.lookupDaemon(r.Context(), o, daemonID)
if err != nil {
    http.Error(w, err.Error(), http.StatusBadGateway)
    return
}
if !ok {
    http.NotFound(w, r)
    return
}
// Continue regardless of res.Local vs res.PeerURL — SendCommandStream below routes correctly.
```

`CommanderTree` (`tree.go:123-138`) and `FanOutSessions` (`proxy.go:156`) call `h.listDaemons` instead of `h.reg.daemons`.

### Turn state — Postgres-backed in shared mode

`turn_state.go` extracts the existing struct into an interface and reuses it:

```go
type turnStateBackend interface {
    begin(key turnKey) bool
    set(key turnKey, state turnState)
    finish(key turnKey, state turnState)
    fail(key turnKey, msg string)
    rekey(old, new turnKey)
    get(key turnKey) turnSnapshot
}
```

In-memory impl is the existing code, unchanged. New `turn_state_pg.go` provides `*pgTurnStore` implementing the same interface against `commander_turns`. `begin` uses `INSERT … ON CONFLICT (user_id,workspace_id,daemon_id,session_id) DO UPDATE SET state='queued', updated_at=now() WHERE commander_turns.state IN ('idle','done','error','awaiting_approval','disconnected') RETURNING xmax` — `xmax=0` means insert (begin succeeded); `xmax>0` and rows affected = 1 means update (begin succeeded); rows affected = 0 means conflict (turn in flight elsewhere, return false). Result: cross-pod turn-in-flight dedup falls out naturally — a second pod's `begin` blocks the duplicate turn.

The **owning pod is the single writer** for non-`begin` mutations. `routeFrame` (`hub.go:243-260`) is extended:

```go
// pendingEntry gains:
type pendingEntry struct {
    ch        chan commander.Envelope
    cancel    chan struct{}
    streaming bool
    command   string   // NEW: e.g. "session_turn"; set at registerPending time
    sessionID string   // NEW: extracted from args when command == "session_turn"
}
```

After a successful `sendOrDrop` of a terminal/status frame in `routeFrame`, the owning pod calls `dc.hub.turns.updateFromEnvelope(...)` with the envelope and the recorded `(command, sessionID, owner, daemonID)`. The update logic mirrors today's `updateTurnStateFromEnvelope` in `http.go:323-372` — refactored into a method on `turnStateBackend` so both paths share it.

**Unsolicited frames** (env.ID == "") are NOT correlated to a pendingEntry — they take a different path: the receiver looks at `env.Type` and, for known session-mutating types (`event` with `event_kind=session_changed`), invalidates the (now-shared-mode-disabled) session cache and updates turn-state if the payload carries a session_id. Implementation: same `updateFromEnvelope` taking a nil pendingEntry path. Today's code ignores unsolicited frames entirely (`hub.go:244-246`); this remains the default, with the new opt-in handler only firing on whitelisted event_kinds.

**Read paths** (`cachedSessionRows` at `tree.go:168`, `mergeCurrentTurnState` at `tree.go:224`) read from `turns.get(key)` — interface call, so Postgres-backed reads on every list. Acceptable: `commander_turns` reads by PK in jsonb-cache PG are sub-ms; the existing `cachedSessionRows` already does an out-of-process round-trip to the daemon.

### Session cache disabled in shared mode

`NewHub` builds `sessionCache = newSessionListCache(10*time.Second)` today (`hub.go:49`). When `attachSharedRegistry` is called, `h.sessionCache = nil` and `cachedSessionRows` skips the cache:

```go
func (h *Hub) cachedSessionRows(ctx context.Context, o owner, info DaemonInfo) ([]SessionRow, error) {
    if h.sessionCache == nil {
        return h.refreshSessionRows(ctx, o, info)
    }
    // … existing path …
}
```

The cache existed to spare daemons repeated `list_sessions` on quick UI tab refreshes. In shared mode, the per-pod cache + cross-pod invalidation cost dwarfs that benefit. A future optimization (out of scope) could move the cache to Postgres with a generation column bumped by `routeFrame` on owning pod; for now, deleting the cache is cheaper than getting cross-pod invalidation right.

`invalidateDaemonSessions` (today called from `http.go:132, 242, 248, 254, 320, 341, 344, 347, 367, 370` — yes, `http.go:132` is in fact the disconnect path's `MethodGet` check, NOT an invalidation site; the disconnect-invalidation actually lives in `hub.go:132` via `defer h.invalidateDaemonSessions(...)` — line references corrected here) becomes a no-op when `sessionCache == nil`. Callers remain as belt-and-suspenders.

### Cluster config

```yaml
cluster:
  advertise_url: ""             # bare value, OR
  advertise_url_env: ""         # env var name (typical: OBSERVER_ADVERTISE_URL)
  secret_env: ""                # env var name (typical: OBSERVER_CLUSTER_SECRET)
  prev_secret_env: ""           # env var name for previous secret (rotation; optional)
  internal_listen_addr: ""      # default ":8091" applied ONLY when cluster is enabled
```

`validateConfig` rules (fail-closed, runs in `cmd/observer-server/main.go`):
- Resolve `advertise_url` (`advertise_url_env` wins if both set), `secret_env` value, `prev_secret_env` value.
- Define "cluster enabled" = (resolved `advertise_url` non-empty) AND (resolved `secret` non-empty).
- If "cluster enabled" AND `store.driver != "postgres"` → fatal `"cluster mode requires store.driver=postgres"`.
- If exactly one of (resolved `advertise_url`, resolved `secret`) is non-empty → fatal `"cluster: advertise_url and secret_env must both be configured, or both omitted"`.
- If "cluster enabled" AND `internal_listen_addr` empty → apply default `":8091"`.
- If NOT "cluster enabled" → `internal_listen_addr` MUST be empty (catches typo where operator set the listen addr but forgot advertise/secret); fatal otherwise.
- `prev_secret_env` resolves to empty is fine (rotation not in progress).
- Log on startup: `commanderhub: shared registry enabled (advertise=<url>, internal=<addr>)` OR `commanderhub: single-pod mode (registry=local)`.

This makes "store.driver=postgres + cluster.* empty" a legitimate single-pod-Postgres deployment with no validation noise, while a partial cluster.* config aborts startup.

### Helm chart

#### `values.yaml`

```yaml
# v3: flip dev default from 2 → 1 because the chart's new fail-fast refuses
# replicaCount > 1 without cluster config. Multi-pod is opt-in.
replicaCount: 1

cluster:
  enabled: false
  advertiseUrlEnv: OBSERVER_ADVERTISE_URL
  secretEnv: OBSERVER_CLUSTER_SECRET
  prevSecretEnv: OBSERVER_CLUSTER_SECRET_PREV
  secretKey: cluster-secret
  prevSecretKey: cluster-secret-prev
  internalListenAddr: ":8091"
  internalServicePort: 8091
  headlessServiceName: ""   # default "<release>-observer-headless"
```

#### `values-production.example.yaml`

```yaml
replicaCount: 3
cluster:
  enabled: true
  # Ops MUST add `cluster-secret` (and optionally `cluster-secret-prev` during
  # rotation) to existingSecret. The init container at pod startup asserts
  # OBSERVER_CLUSTER_SECRET is non-empty so misconfig is loud, not silent.
```

#### `templates/_validate.yaml` (always-rendered)

```gotemplate
{{- $multiPod  := gt (int .Values.replicaCount) 1 -}}
{{- $isPostgres := eq .Values.config.store.driver "postgres" -}}
{{- if and $multiPod $isPostgres (not .Values.cluster.enabled) -}}
{{- fail "replicaCount > 1 with store.driver=postgres requires cluster.enabled=true (set cluster.enabled=true and provide secret.clusterSecret or an existingSecret with a 'cluster-secret' key)" -}}
{{- end -}}
{{- if and .Values.cluster.enabled .Values.secret.create (not .Values.secret.clusterSecret) -}}
{{- fail "cluster.enabled=true with secret.create=true requires secret.clusterSecret (>=32 chars of random)" -}}
{{- end -}}
```

Helm renders templates in alphabetical order; an underscore-prefixed template is a partial that runs but emits nothing. This **always runs**, regardless of `secret.create` or `existingSecret`, because it's not gated by the secret.yaml top-level `{{- if … }}`.

#### `templates/secret.yaml` additions

(Still inside the existing `{{- if and .Values.secret.create (not .Values.existingSecret) }}` gate, because the secret.yaml file itself is only relevant when the chart is creating the secret. The validation lives in `_validate.yaml` above for the `existingSecret` case.)

Add to `observer.yaml`:

```gotemplate
    {{- if .Values.cluster.enabled }}
    cluster:
      advertise_url_env: {{ .Values.cluster.advertiseUrlEnv | quote }}
      secret_env: {{ .Values.cluster.secretEnv | quote }}
      {{- if .Values.cluster.prevSecretEnv }}
      prev_secret_env: {{ .Values.cluster.prevSecretEnv | quote }}
      {{- end }}
      internal_listen_addr: {{ .Values.cluster.internalListenAddr | quote }}
    {{- end }}
```

Add secret data keys:

```gotemplate
  {{- if .Values.cluster.enabled }}
  {{ default "cluster-secret" .Values.cluster.secretKey }}: {{ required "secret.clusterSecret is required when cluster.enabled=true and secret.create=true" .Values.secret.clusterSecret | quote }}
  {{- if .Values.secret.clusterSecretPrev }}
  {{ default "cluster-secret-prev" .Values.cluster.prevSecretKey }}: {{ .Values.secret.clusterSecretPrev | quote }}
  {{- end }}
  {{- end }}
```

#### `templates/deployment.yaml`

The chart already has a conditional `initContainers:` block (lines 30-74) only when Postgres wait is enabled. v3 refactors into a single `initContainers:` block that includes either or both:

```gotemplate
{{- $needPostgresWait := and (eq .Values.config.store.driver "postgres") .Values.postgresql.wait.enabled }}
{{- if or $needPostgresWait .Values.cluster.enabled }}
initContainers:
  {{- if $needPostgresWait }}
  - name: wait-for-postgresql
    {{- /* … existing … */ -}}
  - name: wait-for-observer-schema
    {{- /* … existing … */ -}}
  {{- end }}
  {{- if .Values.cluster.enabled }}
  - name: assert-cluster-secret
    image: "{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}"
    imagePullPolicy: {{ .Values.image.pullPolicy }}
    command: ["/bin/sh", "-ec"]
    args:
      - |
        test -n "$CHECK_VAL" || (
          echo "{{ .Values.cluster.secretEnv }} env var is empty;"
          echo "check that the Secret (configured or external) has key {{ default "cluster-secret" .Values.cluster.secretKey }}"
          exit 1
        ) >&2
    env:
      - name: CHECK_VAL
        valueFrom:
          secretKeyRef:
            name: {{ default (include "observer.configSecretName" .) .Values.existingSecret }}
            key: {{ default "cluster-secret" .Values.cluster.secretKey }}
  {{- end }}
{{- end }}
```

Container envs:

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
      name: {{ default (include "observer.configSecretName" .) .Values.existingSecret }}
      key: {{ default "cluster-secret" .Values.cluster.secretKey }}
{{- if .Values.cluster.prevSecretEnv }}
- name: {{ .Values.cluster.prevSecretEnv }}
  valueFrom:
    secretKeyRef:
      name: {{ default (include "observer.configSecretName" .) .Values.existingSecret }}
      key: {{ default "cluster-secret-prev" .Values.cluster.prevSecretKey }}
      optional: true
{{- end }}
{{- end }}
```

Container ports:

```gotemplate
- name: http
  containerPort: {{ .Values.service.port }}
{{- if .Values.cluster.enabled }}
- name: internal
  containerPort: {{ .Values.cluster.internalServicePort }}
{{- end }}
```

Rolling-update strategy (new top-level block in deployment.yaml spec):

```gotemplate
{{- if .Values.cluster.enabled }}
strategy:
  type: RollingUpdate
  rollingUpdate:
    maxUnavailable: 0
    maxSurge: 100%
{{- end }}
```

**Honest scope note:** even with `maxUnavailable: 0, maxSurge: 100%`, there is a window where old pods are still serving traffic (and not writing to `commander_daemons`) while new pods are also serving. Old-pod daemons remain invisible to new pods during that window, which is typically 30-120s. The spec does NOT claim this collapses to zero; the goal is to bound it. Production rollout doc (`deploy/README.md`) tells operators to drain daemon WS connections by `kubectl rollout restart` once new pods are all Ready, forcing daemons to reconnect to new pods.

#### Internal Service — headless

```gotemplate
{{- if .Values.cluster.enabled }}
---
apiVersion: v1
kind: Service
metadata:
  name: {{ default (printf "%s-headless" (include "observer.fullname" .)) .Values.cluster.headlessServiceName }}
  labels:
    {{- include "observer.labels" . | nindent 4 }}
spec:
  type: ClusterIP
  clusterIP: None                       # headless: DNS resolves all pod IPs
  publishNotReadyAddresses: true        # forward to terminating pods too
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

Pods discover peer IPs from `commander_daemons.owning_instance_url` (advertised pod IP). The headless Service makes those IPs DNS-queryable by name for any operator debugging. Forwarding itself dials the IP directly — no DNS dependency.

#### Ingress/HTTPRoute hardening

For **`templates/ingress.yaml`** (nginx-ingress):
```gotemplate
{{- if and .Values.ingress.enabled }}
  {{- /* Add a more-specific Ingress rule that returns 404 for the internal path. */ -}}
  {{- /* nginx-ingress merges Ingress rules; a more-specific path wins. */ -}}
spec:
  rules:
    - host: {{ .Values.ingress.host }}
      http:
        paths:
          - path: /api/commander/_internal/
            pathType: Prefix
            backend:
              service:
                # Point at a non-existent in-cluster Service to get 503/404 at the edge.
                name: {{ include "observer.fullname" . }}-deny
                port: { number: 1 }
          - path: /
            pathType: Prefix
            backend: ...   # existing public backend
{{- end }}
```

For **`templates/httproute.yaml`** (Gateway API):
```gotemplate
spec:
  rules:
    - matches:
        - path: { type: PathPrefix, value: /api/commander/_internal/ }
      filters:
        - type: ResponseHeaderModifier
          responseHeaderModifier:
            set:
              - { name: Content-Type, value: application/json }
      # No backendRefs ⇒ Gateway returns 503 (Gateway API spec).
    - matches:
        - path: { type: PathPrefix, value: / }
      backendRefs: [ … existing public … ]
```

A more-specific path with no backend is the canonical Gateway-API deny. Verified against the Gateway API v1 spec.

#### Chart tests (`tests/chart_test.sh`)

Three new assertion blocks added to the existing script:

```bash
# 1. Default (replicaCount=1) renders no cluster env or internal Service.
default="$(helm template observer-test "$CHART_DIR")"
! grep -q 'OBSERVER_CLUSTER_SECRET' <<<"$default"
! grep -q 'observer-test-observer-headless' <<<"$default"
! grep -q 'containerPort: 8091' <<<"$default"

# 2. Multi-pod with cluster.enabled renders envs + internal Service + strategy.
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
grep -q 'observer-test-observer-headless' <<<"$multi"
grep -q 'clusterIP: None' <<<"$multi"
grep -q 'containerPort: 8091' <<<"$multi"
grep -q 'name: assert-cluster-secret' <<<"$multi"
grep -q 'maxUnavailable: 0' <<<"$multi"

# 3. Multi-pod without cluster.enabled fails fast (always-rendered _validate.yaml).
if helm template observer-test "$CHART_DIR" --set replicaCount=2 \
    --set config.store.driver=postgres 2>&1 | grep -q 'cluster.enabled=true'; then
  echo "fail-fast detected as expected"
else
  echo "expected fail-fast on replicaCount=2 without cluster.enabled" >&2
  exit 1
fi
```

### CI workflow changes

**`.github/workflows/observer-deploy.yml`:**

- **Smoke job, `Generate smoke values` step (existing block at lines 85-149):**
  - Add `cluster_secret = "".join(secrets.choice(alphabet) for _ in range(48))` and `cluster_secret_prev = ""` to the secret-generation block at lines 88-96.
  - Add `print(f"::add-mask::{cluster_secret}")` immediately after generation.
  - Change `"replicaCount": 1` (line 99) → `"replicaCount": 2`.
  - In the `values` dict: `"cluster": {"enabled": True}` and `values["secret"]["clusterSecret"] = cluster_secret`.

- **Smoke probe job (existing `Smoke from cluster` step starting line 173, in-cluster wget at lines 204-210):**
  - Resolve pod IPs **in the GitHub runner step** (which has kubectl + kubeconfig), not in the busybox Job:
    ```yaml
    - name: Resolve smoke pod IPs
      run: |
        kubectl --context "$KUBE_CONTEXT" -n "$OBSERVER_NAMESPACE" \
          get pods -l app.kubernetes.io/instance=$SMOKE_RELEASE,app.kubernetes.io/component=observer \
          -o jsonpath='{range .items[*]}{.status.podIP} {end}' > /tmp/observer-pod-ips
        cat /tmp/observer-pod-ips
    - name: Smoke from cluster
      run: |
        ips="$(cat /tmp/observer-pod-ips)"
        # Render per-pod-IP wget commands into the busybox Job manifest:
        cmds=""
        for ip in $ips; do
          cmds="$cmds wget -qO- http://$ip:8090/readyz;"
        done
        cat >/tmp/observer-smoke-job.yaml <<EOF
        … (existing template) …
        args:
          - |
            $cmds
        EOF
    ```
  - Asserts each pod's readiness independent of LB routing without giving the busybox Job kubectl access (which it cannot have anyway).

- **Release job (existing block starting line 233):**
  - Add `"OBSERVER_CLUSTER_SECRET"` to the `required = [...]` list (line 285-291); `"OBSERVER_CLUSTER_SECRET_PREV"` is **not required** (rotation-only).
  - Pull from `${{ secrets.OBSERVER_CLUSTER_SECRET }}` as env at line 273-279; mask via `::add-mask::` immediately.
  - Populate `values["secret"]["clusterSecret"]` and `values["cluster"]={"enabled": True}`.
  - If `${{ secrets.OBSERVER_CLUSTER_SECRET_PREV }}` is set (rotation window), populate `values["secret"]["clusterSecretPrev"]` too.

**`.github/workflows/multi-agent.yml`:** no required changes. `go test ./...` runs every test including the new `multi_pod_test.go`. The `helm` job (line 54) re-runs `chart_test.sh`.

### Data flow walkthroughs

**1. UI lists daemons:**
1. UI → LB → Pod B → `GET /api/commander/daemons`.
2. `ch.daemons` calls `ch.hub.listDaemons(r.Context(), o)`.
3. Shared mode: `sharedReg.listAll(ctx, o)` runs `SELECT … WHERE last_seen_at > now() - 45s`. Returns full list across pods.
4. PG unreachable: returns empty + `X-Observer-Registry-Degraded: true`; HTTP 200; UI shows "no daemons" instead of 500.

**2. UI runs a turn on a daemon owned by Pod A, request lands on Pod B:**
1. UI → LB → Pod B → `POST /api/commander/daemons/<id>/sessions/<sid>/turn`.
2. `ch.turn` calls `hub.lookupDaemon(r.Context(), o, daemonID)` → `{PeerURL: "http://10.0.1.42:8091", …}`.
3. `ch.hub.turns.begin(key)` — Postgres-backed in shared mode, ATOMIC across pods: Pod B's INSERT-on-conflict returns true; a duplicate from Pod C (or even Pod B's second tab) returns false → 409 "turn already in flight". This is the multi-pod dedup that v2 explicitly left out and v3 fixes.
4. `SendCommandStream(ctx, o, daemonID, "session_turn", args)`. Local lookup misses → shared lookup returns peer → forward client opens POST to `http://10.0.1.42:8091/forward` with streaming=true.
5. Pod A's `/forward` handler:
   - Validates HMAC + timestamp + nonce-insert.
   - Reads body (3 MiB cap).
   - Validates daemon is in Pod A's `localReg` (404 otherwise).
   - Calls `hub.sendCommandStreamToLocal(ctx, dc, "session_turn", args, outBuffer=256)`.
   - Drains the returned channel and writes length-prefixed JSON to the chunked HTTP body.
   - Each frame routed by Pod A's `routeFrame`. Because Pod A's `pendingEntry.command="session_turn"` and sessionID is known, terminal/status frames trigger `hub.turns.updateFromEnvelope(...)` ON POD A — turn state in `commander_turns` reflects Pod A as source-of-truth.
6. Pod B's forward client decodes envelopes and emits on the `<-chan` returned from `SendCommandStream`. `ch.turn` writes SSE to the browser.
7. Terminal frame → forward client closes its read, drain exits on `ok=false` from `out`.

**3. UI on Pod C polls `/tree` mid-turn:**
1. `ch.tree` → `CommanderTree(ctx, o)` → `listDaemons(...)` returns daemons from all pods.
2. For each, `daemonTree(ctx, o, info)` calls `cachedSessionRows` — in shared mode, cache is nil, so always refresh: `SendCommand("list_sessions")` either local or forwarded.
3. Per-row turn state read from `commander_turns` via `hub.turns.get(key)` — which is the Postgres-backed read. Sees the in-flight turn updated by Pod A's routeFrame in step 5/6.

**4. Pod A crashes mid-turn:**
1. Pod B's forward client `io.EOF` → synthesize error envelope → close chan.
2. `ch.turn` emits SSE error.
3. `commander_turns` row for that key has `state='queued'|'answering'` and isn't being updated. `hub.turns.cleanupOrphans()` (new background sweep) flips rows older than `Hub.TurnTimeout` (10min) to `state='disconnected'`. **Caveat:** this is the worst surviving inconsistency — a daemon row could show `state='answering'` for up to 10 minutes after a crash. Acceptable for the user-visible bug fix; tracked.
4. `commander_daemons` row for Pod A's daemons gets cleaned up by sweep at the 5-min boundary.

**5. Daemon fast reconnect Pod A → Pod B:**
1. Daemon's WS dies, reconnects, lands on Pod B (LB choice).
2. Pod B `localReg.add(dc)` + `sharedReg.connectUpsert` → row's `owning_instance_url` is now Pod B.
3. Pod A's deferred `sharedReg.remove(o, daemonID)` runs but the DELETE's `WHERE owning_instance_url=podA` filter affects 0 rows. Safe.
4. Pod A's heartbeat goroutine: cancelled by `hbCancel`, exits before the deferred DELETE; the last UPSERT attempt (if mid-flight) returns 0 affected rows under the `WHERE owning_instance_url=EXCLUDED.owning_instance_url` ownership guard → heartbeat treats 0 as "ownership lost" and exits without log spam.

**6. Secret rotation:**
1. Ops sets `cluster-secret-prev` key in `existingSecret` to the old secret value; `cluster-secret` to the new value. Trigger rollout.
2. New pods come up with `Secret=new, PrevSecret=old`. They accept HMAC from old-secret-only senders (the not-yet-rolled pods).
3. Old pods are being terminated; they send with their `Secret=old`. New pods accept under `PrevSecret`.
4. After rollout completes, ops removes `cluster-secret-prev`; next rollout pods have `PrevSecret=nil`.

**7. PG outage during heartbeat:**
1. Heartbeat fails for 60s. Counter `forward.heartbeat_errors` increments per failed UPSERT. WARN log rate-limited to 1/sec/pod.
2. `listAll` from any pod stops returning the affected daemons after `last_seen_at > now() - 45s`.
3. **Sweep does NOT delete** (>5min threshold). Rows preserved.
4. PG recovers, next heartbeat UPSERTs `last_seen_at = now()`. Daemons reappear immediately.

### Error mapping (forwarding)

| Receiver state                                               | HTTP status | Caller behavior                                                       |
|--------------------------------------------------------------|-------------|-----------------------------------------------------------------------|
| HMAC/timestamp invalid                                       | 403         | Caller logs (WARN, no secret) + returns `ErrDaemonGone`               |
| Nonce already seen within 60 s window                        | 403         | Same                                                                  |
| Receiver not in shared mode                                  | 503         | Caller logs + returns `ErrDaemonGone`                                 |
| Body > 3 MiB                                                 | 413         | Caller logs + returns `ErrDaemonGone`                                 |
| Daemon not in receiver's local registry                      | 404         | Caller returns `ErrDaemonNotFound` (UI 404); next sweep cleans row    |
| Daemon present, daemon-originated error                      | 200         | Caller wraps `{"error":{code,message}}` back into `*DaemonError`; preserves `commander.ErrCodeSessionNotFound`/`ErrCodeInvalidRequest`/etc. |
| Daemon present, command OK                                   | 200         | Normal path                                                           |
| Daemon present, mid-stream disconnect                        | partial 200 | Caller injects synthetic error envelope on the wrapper channel        |
| Receiver returns 5xx unexpected                              | 500/502     | Caller logs + returns `ErrDaemonGone`                                 |
| Peer URL == this pod's advertise URL (loop)                  | n/a         | Caller refuses without dialing; returns `ErrDaemonNotFound` + ERROR log |

### Testing

**Unit (no Postgres):**
- `registry_shared_test.go` — `go-sqlmock` against `*sql.DB`: assert ownership-guarded UPSERT/heartbeat/DELETE/sweep SQL; assert `lookupRemote` returns false for self-owned rows.
- `forward_test.go` — `httptest`-driven round-trip; HMAC valid/invalid; timestamp drift > 60s → 403; nonce replay → 403; body > 3 MiB → 413; receiver not in shared mode → 503; caller cancel propagates; slow reader triggers drop counter + synthetic `truncated` envelope; daemon-error code preserved across the wire.
- `turn_state_pg_test.go` — `go-sqlmock`: begin returns true on first call, false on conflict; rekey moves key atomically; cleanupOrphans flips stale rows.

**Integration (Postgres via env-skip pattern; mirrors `authstore/postgres_test.go:15-23`):**
- `multi_pod_test.go` — boot two `Hub` instances against shared PG + shared `clusterSecret`. Mock daemon connects to Hub A. Assert:
  - Hub B `listDaemons(o)` returns 1 row.
  - Hub B `SendCommand("list_sessions")` succeeds via forwarding.
  - Hub B `SendCommandStream("session_turn")` receives all envelopes; turn-state in `commander_turns` updated by Hub A.
  - Concurrent `turns.begin(same key)` on Hub A and Hub B — only one returns true.
  - Kill Hub A; sweep on Hub B removes row after `deleteAfter` (use injected `time.Now` faker).
  - Reconnect daemon to Hub B; ownership flipped; Hub A (relaunched) lookups now hit Hub B.
- `multi_pod_files_test.go` — forward a 2 MiB `read_file` response; assert success (3 MiB cap covers it).

**Local repro:** `dev/compose.multi-observer.yaml` boots PG + 2 observers + nginx LB; `dev/README.md` documents `make multi-observer-up`.

**Existing tests:** unchanged. `*_test.go` calls to `hub.reg.{add,daemons,lookup,remove}` still compile because the method surface is preserved on `*localRegistry`.

### Verification

CI:
- `helm` job's `chart_test.sh` covers cluster env + internal Service + fail-fast rendering.
- `go` job's `go test ./...` covers unit; integration tests gated on `OBSERVER_POSTGRES_TEST_DSN` env (skipped on PRs without; run on smoke/release jobs).

Smoke cluster:
```sh
kubectl -n dev-yuzishu get pods -l app.kubernetes.io/instance=observer-ci-<run> \
  -l app.kubernetes.io/component=observer    # 2 pods Running
kubectl -n dev-yuzishu get svc | grep observer-headless    # headless Service exists
curl -sf https://<public-host>/api/commander/_internal/forward    # 404 (Ingress hardened)
kubectl -n dev-yuzishu exec <pg-pod> -- psql "$DSN" -c '\d commander_daemons commander_turns commander_forward_nonces'

# Connect a driver-agent at the public host; 30 GETs → length stable.
for i in {1..30}; do
  curl -s -H "Authorization: Bearer $TOKEN" "https://<public-host>/api/commander/daemons" \
    | jq '.daemons | length'
done | sort -u | wc -l    # → 1

# Run 10 turns; none should 404.
for i in {1..10}; do
  curl -sf -X POST -H "Authorization: Bearer $TOKEN" \
    "https://<public-host>/api/commander/daemons/<id>/sessions/<sid>/turn" \
    -d '{"prompt":"hello"}' >/dev/null || echo "FAIL on iter $i"
done

# Re-do above with two daemons + concurrent two-tab turn POST → exactly one
# should 409 ("turn already in flight"). Other should succeed.
```

Local:
```sh
docker compose -f dev/compose.multi-observer.yaml up -d
for i in {1..30}; do
  curl -s http://localhost:8090/api/commander/daemons | jq '.daemons | length'
done | sort -u | wc -l    # → 1
```

Automated regression:
```sh
go test ./internal/commanderhub/... -race -count=1
OBSERVER_POSTGRES_TEST_DSN=... go test -run TestMultiPod -race ./internal/commanderhub/...
```

### Out of scope (follow-up issues)

- **mTLS between pods** — HMAC + nonce + non-public Service is adequate for cluster-internal traffic; mTLS via cert-manager is a separate sprint.
- **Headless-DNS-based addressing for forwarding** — pod IPs via downward API + headless Service for discovery is simpler; revisit if pod IP churn becomes a real problem.
- **`cleanupOrphans` for `commander_turns`** — basic implementation in v3 (flip to `disconnected` after `TurnTimeout`); a follow-up could improve UX by linking the orphan to its `commander_daemons` row and flipping when the daemon row disappears.
- **PG-backed session-list cache** — v3 simply disables the cache in shared mode. A follow-up could add a generation column for shared invalidation if `list_sessions` traffic becomes hot.

### Rollout sequence

1. **Pre-merge ops work:**
   - Add `OBSERVER_CLUSTER_SECRET` to GitHub repo secrets (smoke + release).
   - Add `cluster-secret` key to production `existingSecret` (`observer-production-secret`).
2. **Merge.** CI builds image and runs smoke at `replicaCount=2` with auto-generated secret.
3. **Production release deploy** (`workflow_dispatch` with `target: release`): Helm `upgrade --install` with `maxUnavailable: 0, maxSurge: 100%` (set in chart when `cluster.enabled=true`). New pods come up alongside old, drain, then old pods terminate.
4. **Post-deploy verification:** the curl loops above; check `commander_daemons` row count matches connected daemon count; spot-check that turns succeed regardless of which pod the POST lands on.
5. **Honest caveat:** during the rolling-update window (typically 30-120s), old pods serve requests using the in-memory map; UI may briefly show fewer daemons (those connected to old pods) when requests land on new pods. To collapse the window, ops run `kubectl rollout restart deployment/observer-observer` once all new pods are Ready, forcing daemons to reconnect to new pods.

Rollback: `helm rollback observer <prev>`. New tables (`commander_daemons`, `commander_turns`, `commander_forward_nonces`) are left behind (no down migration in the chart); rows become stale, irrelevant. A subsequent re-roll-forward consumes them harmlessly. Manual down migration (`schema_postgres_rollback.sql`) is documented in `deploy/README.md`.

Secret rotation: documented in `deploy/README.md` and walkthrough §"Secret rotation" above.
