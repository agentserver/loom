# Shared commanderhub daemon registry across observer instances

**Issue:** [#49](https://github.com/agentserver/loom/issues/49) — commanderhub daemon registry not shared across observer instances; the commander UI shows daemons intermittently when the observer scales horizontally.

## Context

The observer deploys with `replicaCount: 2` in dev (`deploy/charts/observer/values.yaml:1`) and `replicaCount: 3` in production (`values-production.example.yaml:1`). The commanderhub `Hub` keeps every live daemon WebSocket in a per-process map (`internal/commanderhub/registry.go:86-93`). A `daemon-link` WS is naturally sticky — it lands on one pod and stays there — but the read paths the commander UI uses (`GET /api/commander/daemons`, `/tree`, `/sessions`, `POST /daemons/{id}/sessions/{sid}/turn`) are plain stateless HTTP requests. The load balancer routes each one to an arbitrary pod, and that pod can only see the daemons whose WS happened to land on it. The result, observed in production at `loom.nj.cs.ac.cn:10062`:

- A user with one driver-agent + one slave-agent sees the daemon list change on every refresh.
- `POST .../turn` returns 404 whenever the request lands on a non-owning pod.
- Daemon TCP connections and stderr stay healthy throughout — the bug is purely on the observer side.

The fix shares enough state between observer pods that any pod can answer any commander HTTP request consistently. We pick the smallest scope that closes the user-visible symptom: share the registry list and route command/turn requests to the owning pod via internal HTTP forwarding. We deliberately leave the per-pod `turnStateStore` and `sessionListCache` for a follow-up — they degrade gracefully (one stale UI refresh; turn-in-flight guard scoped to one pod).

## Approach

Two layers:

1. **Postgres-backed registry of online daemons.** Each daemon WS owner pod writes a row when the daemon connects, heartbeats every 15 s, deletes the row on disconnect, and a sweeper removes orphan rows after 45 s. The row carries the pod's `owning_instance_url` (its own reachable address). Reads (`/api/commander/daemons`, `/tree`, `/sessions`) query this table and see all daemons regardless of which pod owns them.

2. **Internal pod-to-pod command forwarding.** When `SendCommand` / `SendCommandStream` is called on a non-owning pod, it POSTs to the owning pod's `/api/commander/_internal/forward` endpoint authenticated by a shared cluster secret. The owning pod runs the original local-registry path and streams replies back as length-prefixed JSON envelopes. The streaming wire format mirrors the existing envelope shape — no change to the SSE the browser sees.

Both layers are gated by config: if `store.driver != "postgres"` OR `cluster.advertise_url` empty OR `cluster.secret` empty, the hub keeps using the in-memory registry exclusively — no DB writes, no forwarding endpoint mounted, current single-pod behavior unchanged.

### Component map

| Component                              | File                                                              | Change       |
|----------------------------------------|-------------------------------------------------------------------|--------------|
| Postgres DDL                           | `internal/commanderhub/authstore/schema_postgres.sql`             | add table    |
| Migration runner                       | `internal/commanderhub/authstore/migrate.go`                      | unchanged (same `db.Exec(schema)` runs new DDL) |
| Registry interface                     | `internal/commanderhub/registry.go`                               | extract iface, keep `localRegistry`, add `sharedRegistry` |
| Heartbeat goroutine                    | `internal/commanderhub/hub.go` `ServeHTTP`                        | start in defer-bounded goroutine after `reg.add` |
| Forwarding client (`SendCommand[Stream]` remote case) | `internal/commanderhub/proxy.go`                  | branch on `lookup` result |
| Forwarding HTTP endpoint               | `internal/commanderhub/forward.go` (new)                          | mount under `/api/commander/_internal/forward` |
| Length-prefixed JSON envelope codec    | `internal/commanderhub/forward.go` (new)                          | one helper, used both sides |
| Hub options + wiring                   | `internal/commanderhub/wiring.go`, `hub.go`                       | thread `ClusterConfig` through `MountAll`/`NewHub` |
| Observer config schema                 | `cmd/observer-server/main.go`                                     | new `Cluster ClusterConfig` field + `validateConfig` |
| Helm chart                             | `deploy/charts/observer/values.yaml`, `templates/secret.yaml`, `templates/deployment.yaml`, `templates/configmap.yaml` | new `cluster:` block, env wiring (downward API), secret data key, fail-fast on multi-pod without secret |
| Chart tests                            | `deploy/charts/observer/tests/chart_test.sh`                      | render assertions for cluster env + fail-fast |
| CI deploy workflow                     | `.github/workflows/observer-deploy.yml`                           | generate `clusterSecret` in smoke; bump smoke `replicaCount` to 2; require `OBSERVER_CLUSTER_SECRET` repo secret in release |
| Multi-pod regression test              | `internal/commanderhub/multi_pod_test.go` (new)                   | two `Hub` instances + dockertest Postgres; daemon connects to A, B sees it and forwards `list_sessions` |
| Optional local-repro compose           | `dev/compose.multi-observer.yaml` (new)                           | 2 observers + 1 Postgres for manual repro |

The new `commanderhub/forward.go` file isolates the pod-to-pod transport (client + handler + codec) from the existing `proxy.go` daemon-side proxy. `proxy.go` only changes by branching on the registry lookup result: local → existing code, remote → call the forward client. This keeps the daemon-facing protocol unchanged.

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

The PK is `(user_id, workspace_id, daemon_id)`. `daemon_id` is already a random 16-hex-char string (`hub.go:newDaemonID()`), so no collisions across pods. `owning_instance_url` is the advertised URL of the pod the WS is currently on. If a daemon reconnects to a different pod after a network blip, `INSERT ... ON CONFLICT (...) DO UPDATE` overwrites the URL.

### Registry interface

Existing `*registry` becomes `localRegistry` implementing `daemonRegistry`:

```go
type daemonRegistry interface {
    add(dc *daemonConn)
    remove(o owner, daemonID string)
    lookup(o owner, daemonID string) lookupResult
    daemons(o owner) []DaemonInfo
}

type lookupResult struct {
    local   *daemonConn // non-nil iff owned by this pod
    remote  bool        // true iff DB row exists but pod is a peer
    peerURL string      // set when remote
    info    DaemonInfo  // populated for remote; used by FanOutSessions
}
```

`sharedRegistry` wraps a `localRegistry` (for daemons owned by this pod — `SendCommand`'s read loop and pending map must access the real `*daemonConn`), plus a `*sql.DB` and the pod's own `advertiseURL`. Its methods:

- `add(dc)` — `localRegistry.add(dc)` then `INSERT ... ON CONFLICT (user_id, workspace_id, daemon_id) DO UPDATE SET owning_instance_url=$N, last_seen_at=now(), ...`. Failure is logged + counted but does not refuse the WS (network partitions shouldn't drop healthy daemons). The heartbeat goroutine retries on the next tick.
- `remove(o, daemonID)` — `localRegistry.remove(o, daemonID)` then `DELETE ... WHERE user_id=$1 AND workspace_id=$2 AND daemon_id=$3 AND owning_instance_url=$4` (the `owning_instance_url` guard prevents deleting a row that a sibling pod has just claimed after a fast reconnect).
- `lookup(o, daemonID)` — first ask the embedded `localRegistry`. If hit, return `{local: dc}`. Otherwise `SELECT owning_instance_url, short_id, display_name, kind, driver_version, capabilities, last_seen_at FROM commander_daemons WHERE ...`. If row exists AND `last_seen_at > now() - 45s`, return `{remote: true, peerURL: ..., info: ...}`. Otherwise return zero (caller maps to `ErrDaemonNotFound`).
- `daemons(o)` — `SELECT ... WHERE user_id=$1 AND workspace_id=$2 AND last_seen_at > now() - interval '45 seconds' ORDER BY display_name`. Returns all visible daemons across all pods.

### Heartbeat & sweep

- **Heartbeat:** when a `daemonConn` is added, `ServeHTTP` spawns a goroutine that ticks every 15 s and runs `UPDATE commander_daemons SET last_seen_at = now() WHERE user_id=$1 AND workspace_id=$2 AND daemon_id=$3 AND owning_instance_url=$4`. It exits when `<-dc.done` fires (mirrors how `readLoop` exit triggers cleanup). On Postgres unavailable, log and continue; the next tick retries. The 3× TTL ratio absorbs one missed heartbeat.
- **Sweep:** one goroutine per pod, started by `MountAll` when shared mode is active, ticks every 30 s and runs `DELETE FROM commander_daemons WHERE last_seen_at < now() - interval '45 seconds'`. Pod crashes leave rows; sweep cleans them within ~30 s.
- **Graceful disconnect:** the existing `defer h.reg.remove(o, dc.id)` in `ServeHTTP` already removes the row instantly when the WS closes cleanly.

### Internal forwarding endpoint

Mounted at `/api/commander/_internal/forward` only when shared mode is active. Path prefix `_internal/` so any future operator running an Ingress with path-based ACLs has an obvious deny target (the path SHOULD never be reachable from outside the cluster, but defense in depth).

Request:

```
POST /api/commander/_internal/forward
X-Observer-Cluster-Secret: <bytes from OBSERVER_CLUSTER_SECRET>
Content-Type: application/json

{
  "user_id":      "<owner.userID>",
  "workspace_id": "<owner.workspaceID>",
  "daemon_id":    "<daemon-id>",
  "command":      "session_turn",
  "args":         {...},        // raw JSON, forwarded to daemon as-is
  "streaming":    true,
  "timeout_ms":   600000        // observer-side safety bound; matches Hub.TurnTimeout
}
```

Auth:
- Compare `X-Observer-Cluster-Secret` against the configured secret in constant time (`crypto/subtle.ConstantTimeCompare`).
- Mismatch → 403, no body.
- Missing secret config on the receiver → endpoint returns 503 (means the receiver isn't in shared mode either; caller should re-resolve registry).

Response — non-streaming:

```
200 OK
Content-Type: application/json

{"result": <raw command_result payload>}
```

or

```
200 OK
{"error": {"code": "...", "message": "..."}}
```

(404 is reserved for "daemon not in MY local registry either" — i.e., the DB row is stale; caller can decide to retry the registry resolution or surface 404 to the user.)

Response — streaming: `Transfer-Encoding: chunked`, body is a sequence of length-prefixed JSON envelopes:

```
<len>\n<envelope-json-bytes>
<len>\n<envelope-json-bytes>
...
```

The stream ends when the daemon's response stream ends (terminal frame seen, ctx cancelled, daemon gone). The forwarding receiver re-injects each envelope into the channel returned from its local `SendCommandStream`, which `ch.turn` in `http.go` then writes out as SSE to the browser.

Choosing length-prefixed JSON over SSE for the pod-to-pod hop: SSE is browser-oriented (event/data framing for `EventSource`); for a Go-to-Go hop, length-prefixing is one allocation and one read per frame, matches what `commander.Envelope` already serializes to. Reuses no third-party codec.

### Cluster config

New observer config block (added to `cmd/observer-server/main.go` `Config`):

```yaml
cluster:
  advertise_url: ""           # bare value, OR
  advertise_url_env: OBSERVER_ADVERTISE_URL
  secret_env: OBSERVER_CLUSTER_SECRET
```

`advertise_url` is the pod's own reachable base URL — for k8s, `http://$(POD_IP):8090` rendered via the downward API. For docker-compose, the service name (e.g. `http://observer-2:8090`). `advertise_url_env` (the typical case) makes the chart wire `POD_IP` into the env without baking the IP into the configmap. Either is fine; if both set, `advertise_url_env` wins.

`secret_env` names the env var holding the cluster secret. The value SHOULD be ≥ 32 random bytes; chart auto-generates if not provided.

`validateConfig` rules (in `cmd/observer-server/main.go`):
- If `cluster.advertise_url` empty AND `cluster.advertise_url_env` resolves to empty → shared mode disabled.
- If `cluster.secret_env` resolves to empty → shared mode disabled.
- If `store.driver != "postgres"` → shared mode disabled (with log line; SQLite is single-pod by definition).
- Otherwise → shared mode enabled. Log `commanderhub: shared registry (instance=<url>)` at startup.

This auto-detect approach means existing single-pod deployments (smoke env, docker-compose, dev) need no config change. Multi-pod deployments must opt in by setting both env vars.

### Hub wiring change

`MountAll` signature today:
```go
func MountAll(mux *http.ServeMux, resolver identity.Resolver, agentserverURL string, store authstore.Store)
```

Becomes:
```go
type ClusterConfig struct {
    DB           *sql.DB  // nil → shared mode off
    AdvertiseURL string   // empty → shared mode off
    Secret       []byte   // empty → shared mode off
}

func MountAll(mux *http.ServeMux, resolver identity.Resolver, agentserverURL string, store authstore.Store, cluster ClusterConfig)
```

`MountAll` decides the registry implementation based on `cluster`, builds the right one, and passes it to a new `NewHubWithRegistry(resolver, reg daemonRegistry) *Hub`. The existing `NewHub(resolver)` convenience constructor stays unchanged — it calls `NewHubWithRegistry(resolver, newLocalRegistry())`. All existing tests keep using `NewHub`. In shared mode, `MountAll` also mounts `/api/commander/_internal/forward` and starts the sweep goroutine. Single-pod (legacy) mode: `MountAll` builds a `localRegistry` and skips the forward endpoint/sweep.

`observerweb.NewWithResolverOptions` (the caller of `MountAll`) gains a `Cluster ClusterConfig` field on `Options`, which `cmd/observer-server/main.go` populates from the resolved config. Backward-compat: zero-value `ClusterConfig` ⇒ legacy single-pod.

### Helm chart changes

**`values.yaml`** — new top-level `cluster:` block:

```yaml
cluster:
  # When replicaCount > 1, enable=true requires secret. Default behavior:
  # if replicaCount > 1 and store.driver=postgres, the chart auto-enables
  # this block and refuses to render without secret.clusterSecret.
  enabled: false
  advertiseUrlEnv: OBSERVER_ADVERTISE_URL
  secretEnv: OBSERVER_CLUSTER_SECRET
  secretKey: cluster-secret
```

**`secret.yaml`** — add a fail-fast block near the top:

```gotemplate
{{- if and (gt (int .Values.replicaCount) 1) (eq .Values.config.store.driver "postgres") }}
  {{- if and (not .Values.cluster.enabled) (not .Values.existingSecret) }}
    {{- fail "replicaCount > 1 with store.driver=postgres requires cluster.enabled=true and secret.clusterSecret (or existingSecret with cluster-secret key)" }}
  {{- end }}
{{- end }}
{{- if and .Values.cluster.enabled .Values.secret.create (not .Values.secret.clusterSecret) }}
  {{- fail "cluster.enabled=true with secret.create=true requires secret.clusterSecret (≥32 chars random)" }}
{{- end }}
```

Add to `observer.yaml` rendered into the secret:

```gotemplate
    {{- if .Values.cluster.enabled }}
    cluster:
      advertise_url_env: {{ .Values.cluster.advertiseUrlEnv | quote }}
      secret_env: {{ .Values.cluster.secretEnv | quote }}
    {{- end }}
```

Add the secret data key:

```gotemplate
  {{- if .Values.cluster.enabled }}
  {{ default "cluster-secret" .Values.cluster.secretKey }}: {{ required "secret.clusterSecret is required when cluster.enabled=true" .Values.secret.clusterSecret | quote }}
  {{- end }}
```

**`deployment.yaml`** — add to the `env:` block on the observer container:

```gotemplate
{{- if .Values.cluster.enabled }}
- name: POD_IP
  valueFrom:
    fieldRef:
      fieldPath: status.podIP
- name: {{ .Values.cluster.advertiseUrlEnv }}
  value: "http://$(POD_IP):{{ .Values.service.port }}"
- name: {{ .Values.cluster.secretEnv }}
  valueFrom:
    secretKeyRef:
      name: {{ include "observer.configSecretName" . }}
      key: {{ default "cluster-secret" .Values.cluster.secretKey }}
{{- end }}
```

**`tests/chart_test.sh`** — assertions:
1. `helm template ... --set replicaCount=1` renders without any `OBSERVER_CLUSTER_SECRET` env (regression: single-pod unaffected).
2. `helm template ... --set replicaCount=2 --set cluster.enabled=true --set secret.create=true --set secret.clusterSecret=xxxx... --set ...` renders `OBSERVER_CLUSTER_SECRET` and `POD_IP` env entries on the observer deployment.
3. `helm template ... --set replicaCount=2 --set store.driver=postgres` (no cluster.enabled, no existingSecret) → exit 1 with the expected fail message.

**`values-production.example.yaml`** — set `cluster.enabled: true` (matches `replicaCount: 3`). Document `secret.clusterSecret` is provided via `existingSecret: observer-production-secret`; ops must add a `cluster-secret` key to that secret before the chart's pre-rollout validation passes.

### CI workflow changes

**`.github/workflows/observer-deploy.yml`:**

- `smoke` job (line 60 onwards): bump `replicaCount` from 1 → 2; generate `cluster_secret = "".join(secrets.choice(alphabet) for _ in range(48))` alongside the existing password/key generation (lines 89-95); include in values:
  ```python
  "cluster": {"enabled": True},
  "secret": {..., "clusterSecret": cluster_secret},
  ```
- Smoke probe (line 173): extend the in-cluster smoke job to hit `kubectl get pod -l ... -o jsonpath='{.items[0].status.podIP}'` for each pod and wget `/readyz` per-pod. Asserts each pod started cleanly (one might have failed validation if env wiring is wrong).
- `release` job (line 233): add `OBSERVER_CLUSTER_SECRET` to the `required = [...]` list (line 285), pull from `${{ secrets.OBSERVER_CLUSTER_SECRET }}`, populate `secret.clusterSecret` and `cluster.enabled = True`.
- **Pre-rollout coordination note** (added to the workflow comments): the repo secret `OBSERVER_CLUSTER_SECRET` MUST exist before the first release deploy after this change merges, otherwise the chart fail-fast will block the rollout. Document in `deploy/README.md`.

**`.github/workflows/multi-agent.yml`:** no change. Existing `go test ./... -race -count=1` already runs every test including any new `multi_pod_test.go`. The `helm` job (line 54) already runs `chart_test.sh` which will be extended.

### Data flow walkthroughs

**1. UI lists daemons (read path):**
1. UI → LB → Pod B → `GET /api/commander/daemons`.
2. `ch.daemons` (`http.go:44`) calls `ch.hub.reg.daemons(o)`.
3. In shared mode, `sharedRegistry.daemons` runs the `SELECT ... WHERE last_seen_at > now() - 45s`. Returns full list across pods.
4. UI sees consistent daemon set on every refresh, regardless of LB routing.

**2. UI runs a turn on a daemon owned by Pod A, request lands on Pod B:**
1. UI → LB → Pod B → `POST /api/commander/daemons/<id>/sessions/<sid>/turn`.
2. `ch.turn` (`http.go:209`) first calls `ch.hub.reg.lookup(o, daemonID)` (line 226 today; the check stays). `sharedRegistry.lookup` returns `{remote: true, peerURL: "http://10.0.1.42:8090"}`.
3. `turn` calls `ch.hub.turns.begin(key)` locally — succeeds because Pod B has no entry for this key. (Cross-pod turn dedup is a non-goal: the same turn issued concurrently to Pod A and Pod B both proceed, and the daemon's session_turn handler is the final dedup. This is acceptable for the user-visible symptom; tracked as a follow-up issue.) It proceeds to `SendCommandStream`.
4. `SendCommandStream` (`proxy.go:84`) sees `lookupResult.remote == true` and routes to the forward client. Forward client opens an HTTP POST to `peerURL/api/commander/_internal/forward`, streaming=true, with the cluster secret header.
5. Pod A's `/api/commander/_internal/forward` handler authenticates, validates the requested `daemon_id` is in **its local registry only** (refuses with 404 otherwise — prevents infinite peer loops). The handler does NOT call `turns.begin` (turn-state remains owned by the caller Pod B). It calls `hub.sendCommandToLocal(...)` — a refactored internal helper extracted from today's `SendCommand[Stream]` body that bypasses the registry-lookup branch and operates directly on the local `*daemonConn`. Pod A owns `nextCmdID`, registers the pending entry, drains replies.
6. Each envelope Pod A emits is written to Pod B as `<len>\n<json>`. Pod B's forward client reads them, sends them on the returned `<-chan commander.Envelope`. Pod B's `ch.turn` writes them out as SSE to the browser — exact same path as a local turn.
7. Terminal frame closes the stream; Pod B finalizes turn state locally (per-pod is fine for the in-flight pod; cross-pod state divergence is the documented non-goal).

**3. Pod A crashes mid-turn:**
1. Pod B's forward client gets `io.EOF` or connection-reset on the chunked body read.
2. Forward client closes the returned channel with a synthetic `{Type:"error", Payload:{code:"backend_unavailable", message:"daemon disconnected"}}` envelope.
3. `ch.turn` handles this via the existing `case <-chunkCh:` path → `finishTurnWithoutTerminal` → SSE `error` event to browser.
4. Sweep (running on Pod B and any other surviving pod) deletes the orphan rows for daemons that were on Pod A after 45 s.
5. On Pod A restart, daemons reconnect (existing wsclient reconnect loop), `add` runs `INSERT ... ON CONFLICT DO UPDATE` with the new (or same) IP.

**4. Postgres unreachable on a read:**
1. `sharedRegistry.daemons` returns `nil, err`.
2. `ch.daemons` returns `{daemons: []}` with `X-Observer-Registry-Degraded: true` header (new), HTTP 200. UI shows "no daemons" (rather than 500 / hang). Metric `observer.commanderhub.registry.errors{op="daemons"}` increments.
3. Operator visibility: log line at `WARN` level on every DB error, rate-limited to one per second per pod (use existing `logutil` if available; otherwise simple `atomic.Int64` counter).

### Error mapping (forwarding)

| Receiver state                                     | HTTP status | Caller behavior |
|----------------------------------------------------|-------------|-----------------|
| Secret mismatch                                    | 403         | Caller logs + treats as `ErrDaemonGone` (peer untrusted) |
| Receiver not in shared mode                        | 503         | Caller logs + treats as `ErrDaemonGone` |
| Daemon not in receiver's local registry            | 404         | Caller returns `ErrDaemonNotFound` (UI 404) — sweep will clean stale row |
| Daemon present, command sent OK, terminal returned | 200         | Normal path |
| Daemon present, mid-stream connection drop         | partial 200 | Caller injects synthetic error envelope on the channel |
| Receiver returns 5xx unexpected                    | 500/502     | Caller logs + returns `ErrDaemonGone` |

### Testing

**Unit (no Postgres required):**
- `registry_shared_test.go` — `sharedRegistry` against `sqlmock` / `pgxmock`: `add` → INSERT/UPDATE SQL shape; `lookup` returns `local` when in-memory hit, `remote` when DB hit, zero when stale.
- `forward_test.go` — round-trip test using `httptest.Server`: client POSTs JSON; handler validates secret; non-streaming returns 200 with result; streaming sends N envelopes ending in terminal frame.
- `forward_auth_test.go` — wrong secret → 403; missing config on receiver → 503.

**Integration (Postgres via dockertest, mirrors `authstore/postgres_test.go` pattern):**
- `multi_pod_test.go` —
  - Boot two `Hub` instances against one Postgres.
  - Boot one mock daemon connecting to Hub A.
  - Assert Hub B `daemons(o)` returns 1 row with `owning_instance_url` pointing at A.
  - Hub B `SendCommand(..., "list_sessions", nil)` succeeds, payload matches what the daemon returned to Hub A.
  - Kill Hub A; assert sweep on Hub B removes the row within `2*sweepInterval`.
  - Reconnect daemon to Hub B; assert Hub A (re-launched) sees it via `daemons(o)`.

**Local manual repro (new compose file):**
- `dev/compose.multi-observer.yaml` brings up Postgres + 2 observers + nginx LB.
- `make multi-observer-up` documented in `dev/README.md`.

**Existing tests:** all current commanderhub tests (`hub_test.go`, `proxy_test.go`, `e2e_test.go`, `registry_test.go`, etc.) keep working — they build a single `Hub` with a `localRegistry` and exercise the unchanged in-memory code path. `NewHub` keeps a single-argument convenience signature for these tests (registry defaults to `localRegistry`).

### Verification

End-to-end on the deployed smoke cluster after CI rolls the chart change:

```
# 1. Verify both pods are running.
kubectl -n dev-yuzishu get pods -l app.kubernetes.io/instance=observer-ci-<run> \
  -l app.kubernetes.io/component=observer

# 2. Each pod must carry POD_IP + cluster envs.
kubectl -n dev-yuzishu describe pod <pod-name> | grep -E 'POD_IP|OBSERVER_ADVERTISE_URL|OBSERVER_CLUSTER_SECRET'

# 3. Migration must have created the table.
kubectl -n dev-yuzishu exec <pg-pod> -- \
  psql "$OBSERVER_DATABASE_URL" -c '\d commander_daemons'

# 4. Connect a driver-agent locally, point at the smoke observer.
#    Run 30 consecutive /api/commander/daemons GETs — daemon count must be stable.
for i in {1..30}; do
  curl -s -H "Authorization: Bearer $TOKEN" \
    "https://<smoke-host>/api/commander/daemons" | jq '.daemons | length'
done | sort -u   # → expect a single line "1"

# 5. POST a turn against the daemon; repeat 10×. None should 404.
```

Local repro via `dev/compose.multi-observer.yaml`:

```
docker compose -f dev/compose.multi-observer.yaml up -d
# connect driver-agent at http://localhost:8090 (the nginx LB)
# repeatedly curl http://localhost:8090/api/commander/daemons; daemon count stable
```

Automated regression: `go test ./internal/commanderhub/... -run TestMultiPod -race`.

### Out of scope (follow-up issues)

- Multi-pod `turnStateStore` (turn-in-flight guard remains per-pod) — file follow-up issue.
- Multi-pod `sessionListCache` invalidation — one stale UI refresh after a turn finishes on a sibling pod. File follow-up issue.
- mTLS between pods (current: shared cluster secret).
- K8s headless-service-based addressing instead of pod IP (pod IP is fine for the current pod-restart frequency).
