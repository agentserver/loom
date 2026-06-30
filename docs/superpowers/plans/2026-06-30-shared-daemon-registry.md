# Shared commanderhub Daemon Registry Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close all five cross-pod consistency bugs in the observer surface when `replicaCount > 1` — the daemon registry (issue #49), turn-state (Finding A), session-cache (Finding B), identity-cache TTL skew (Finding D), and telemetry rate limiter (Finding E). Plus debug-correlation polish (cmdID pod prefix).

**Architecture:** Eight layers gated on `cluster.enabled`. Postgres-backed: `commander_daemons` (online set + ownership), `commander_turns` (cross-pod begin/get/finish), `commander_forward_nonces` (HMAC replay defense), `commander_telemetry_buckets` (atomic token bucket). Pod-to-pod HTTP forwarding on dedicated `:8091` listener with HMAC + nonce auth; receiver pod-IP via downward API; per-pod headless Service for discovery. `sessionListCache` disabled in cluster mode (per-pod cost > benefit). Identity cache: shared-mode `FreshTTL = 30s` default; opt-in PG `LISTEN/NOTIFY` revocation channel. Fail-closed on partial config; chart-rendered `validate.yaml` rejects misconfig at `helm install`.

**Tech Stack:** Go 1.26.x, gorilla/websocket, `jackc/pgx/v5` (via `database/sql`) for pool, dedicated `*pgx.Conn` for LISTEN, encoding/json, crypto/hmac, Postgres 14+, Kubernetes 1.27+ (Helm chart, NetworkPolicy v1, downward API), HTTP/1.1 chunked, length-prefixed JSON envelopes.

## Global Constraints

- **Source spec (clean after 10 codex rounds):** `docs/superpowers/specs/2026-06-29-shared-daemon-registry-design.md` (v19).
- **No regression to single-pod mode.** Every change must preserve current behavior when `cluster.enabled=false` AND the cluster-config env vars are unset. All 30+ existing test sites that call `hub.reg.add(...)` / `hub.reg.daemons(...)` MUST continue to compile.
- **Fail-closed on partial config.** `validateConfig` rejects partial cluster.* config or `cluster.enabled AND store.driver != "postgres"`. Chart `templates/validate.yaml` rejects `replicaCount > 1 AND (!cluster.enabled OR store.driver != "postgres")`.
- **Wire caps (immutable across plan):** forward request body ≤ 1.5 MiB (`(1<<20)+(1<<19)`); each length-prefixed envelope ≤ 1 MiB (`1<<20`); observer-side `wsReadLimit` STAYS at 1 MiB; daemon-side `commander/files.go::Handler.ReadFile` enforces JSON-encoded size ≤ 768 KiB.
- **Auth on internal listener:** HMAC-SHA256 over `timestamp || "\n" || nonce || "\n" || body`; compared via `hmac.Equal` on fixed `[32]byte`. 60s timestamp window. Nonce: 32 random hex chars from `crypto/rand`, atomic INSERT into `commander_forward_nonces` AFTER HMAC verify (NOT before — otherwise unauth attacker DoSes the table). Receiver fails CLOSED if nonce INSERT errors (PG unavailable → 503, never accept). Three-phase secret rotation via `cluster.secret_env` + `cluster.prev_secret_env`. Sender retries ONCE on 403 with `PrevSecret`.
- **Loopback bypass restricted to `/api/commander/_internal/drain` only**, NEVER `/forward`. Bypass triggers when `RemoteAddr` resolves to a loopback IP via `net.IP.IsLoopback`.
- **Bug-for-bug parity in single-pod cmdID:** `nextCmdID()` in single-pod (`h.sharedReg == nil`) MUST emit `strconv.FormatInt(seq, 36)` byte-for-byte unchanged (no prefix, no dash). Shared mode emits `<podHash>-<base36>` where `podHash = hex(sha256(advertiseURL))[:4]`.
- **TDD discipline.** Every task starts with a failing test, then minimal code, then a passing test, then commit. Race detector mandatory: `go test -race -count=1`.
- **Postgres integration tests are env-skipped** on `OBSERVER_POSTGRES_TEST_DSN`; CI does not require these. Unit tests on `*sql.DB` use `github.com/DATA-DOG/go-sqlmock` (new dependency added by Task A3).
- **Commit prefixes:** Go in `commanderhub` → `feat(commanderhub): …` or `fix(commanderhub): …`. Go in `commander` (shared) → `feat(commander): …`. observer-server → `feat(observer-server): …`. identity → `feat(identity): …`. observerweb → `feat(observerweb): …`. Chart → `chore(chart): …`. CI → `ci(observer-deploy): …`. Docs → `docs(…): …`. All commits MUST end with the existing `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>` line per CLAUDE.md.
- **No `go.work`.** Run all `go` commands from `multi-agent/`.

---

## Source Spec

Implement: `docs/superpowers/specs/2026-06-29-shared-daemon-registry-design.md` (v19).

## Phase plan

The plan is broken into **5 phases of 5–6 tasks each (27 tasks total)**. Each phase compiles & tests cleanly on its own; phase boundaries are good review checkpoints.

- **Phase A (Foundation, 6 tasks):** Constants, error codes, PG schema (3 tables), daemon-side `ReadFile` encoded-size cap, `localRegistry` rename + `removeIf`, `turnKey.shortID` rename + `turnStateBackend` interface, `telemetryAllower` interface. No behavior change yet.
- **Phase B (Shared registry + heartbeat, 5 tasks):** `sharedRegistry` Go type + SQL UPSERT/heartbeat/DELETE/lookupRemote/listAll, heartbeat goroutine with ownership-loss force-close, `dc.confirmOwnership`, `ServeHTTP` admission gating (connectUpsert before localReg.add), sweep goroutine (commander_daemons + commander_forward_nonces + commander_telemetry_buckets).
- **Phase C (Forwarding + drain + cmdID, 6 tasks):** Length-prefixed envelope codec, HMAC + nonce auth + nonces table, `forwardClient.send`/`stream`, `forwardServer` handler + audit log, `drainServer` endpoint with loopback/HMAC auth, `Hub.nextCmdID` pod-prefix.
- **Phase D (Wiring, read-path migration, observer-server lifecycle, 5 tasks):** `Hub.attachSharedRegistry` + `listDaemons` + `lookupDaemon` + caller migration, `pgTurnStore` (cross-pod begin/get/updateFromEnvelope), `pgTelemetryLimiter`, identity revocation channel (functional-options NewCache + WithRevocationChannel + revocation_pg.go), observer-server `Cluster ClusterConfig` + `loadConfig` merge + `validateConfig` + dual-listener lifecycle (errgroup + `Shutdown`).
- **Phase E (Chart + CI + docs, 5 tasks):** `values.yaml` + `values-production.example.yaml`, `templates/validate.yaml`, `templates/{configmap,secret,deployment}.yaml` renders + init container + preStop, `templates/{service,networkpolicy,ingress,httproute}.yaml`, `chart_test.sh` + `observer-deploy.yml` + `deploy/README.md` + `dev/compose.multi-observer.yaml`.

A reasonable execution pace is **1 phase per day** for a focused worker, with codex review at each phase boundary.

---

## File Structure

### commanderhub (`multi-agent/internal/commanderhub/`)

- Modify: `registry.go` — rename `registry` → `localRegistry`; add `removeIf(o, shortID, connectionID)`; key by `shortID` (was per-connection daemon_id); keep `add`/`lookup`/`daemons` method surface. `daemonConn` already has `id` (per-conn) and `shortID` (set in `hub.go:111`); add `ownershipLost atomic.Bool` for Phase B's confirmOwnership.
- Create: `registry_shared.go` — `*sharedRegistry`: `connectUpsert`, `heartbeatUpsert`, `remove`, `lookupRemote`, `listAll`, `runHeartbeat`, `sweep`, `sweepNonces`, `sweepTelemetryBuckets`.
- Create: `registry_shared_test.go` — `go-sqlmock` driven SQL-shape assertions.
- Modify: `hub.go` — `Hub` gains `sharedReg`, `forwardCli`. `NewHub(resolver)` signature unchanged; new `(h *Hub).attachSharedRegistry(sr, fc, turns, sessionsCache=nil)`. `newDaemonID` → 128-bit + returns error. `ServeHTTP` admission order: `sharedReg.connectUpsert` (under 3s ctx) → `localReg.add`. Heartbeat goroutine via `runHeartbeat(ctx, dc)`. Deferred teardown: `localReg.removeIf(o, dc.shortID, dc.id)` + `sharedReg.remove(ctx, o, dc.shortID, dc.id)` after `hbCancel + <-hbDone`. `(h *Hub).listDaemons(ctx, o) ([]DaemonInfo, error)` + `(h *Hub).lookupDaemon(ctx, o, shortID) (lookupResult, bool, error)` + `(h *Hub).nextCmdID()` pod-prefix in shared mode.
- Modify: `proxy.go` — `SendCommand`/`SendCommandStream` branch: localReg hit → `sendCommandToLocal`/`sendCommandStreamToLocal`; miss → `sharedReg.lookupRemote` → `forwardCli.send`/`forwardCli.stream`. Both local helpers call `dc.confirmOwnership(ctx)` before `writeEnvelope`. `FanOutSessions` uses `listDaemons`. `pendingEntry` gains `command string` + `sessionID string`.
- Modify: `http.go` — `ch.daemons`/`ch.tree`/`ch.sessionsFanout` use `hub.listDaemons`. `ch.turn` existence guard uses `hub.lookupDaemon`. `writeSendCmdError` adds case for `commander.ErrCodeDaemonUpgradeRequired` → HTTP 426.
- Modify: `tree.go` — `CommanderTree` calls `listDaemons`. `cachedSessionRows` skips cache when `h.sessionCache == nil`. `invalidateDaemonSessions` no-op when nil.
- Modify: `turn_state.go` — extract `turnStateBackend` interface (`begin`/`set`/`finish`/`fail`/`rekey`/`get`/`updateFromEnvelope`/`cleanupOrphans` all take `context.Context`). Rename `turnKey.daemonID` → `shortID`. Rename in-memory impl `*turnStateStore` → `*memTurnStore`.
- Create: `turn_state_pg.go` — `*pgTurnStore` against `commander_turns`. `begin` uses `INSERT … ON CONFLICT … WHERE state IN (terminal-states) RETURNING (xmax = 0)`.
- Create: `turn_state_pg_test.go` — `go-sqlmock`.
- Create: `forward_codec.go` — `writeEnvelopeFrame(w io.Writer, env commander.Envelope) error` + `readEnvelopeFrame(r *bufio.Reader) (commander.Envelope, error)`. 1 MiB cap per envelope, decimal-ASCII length + `\n` + JSON bytes.
- Create: `forward_codec_test.go`.
- Create: `forward_client.go` — `*forwardClient`: `send(ctx, peerURL, req) (json.RawMessage, error)`, `stream(ctx, peerURL, req) (<-chan commander.Envelope, error)`. HMAC signing, 32-hex nonce, retry-once-on-403 with `PrevSecret`, audit log line per send.
- Create: `forward_client_test.go` — `httptest.Server`-driven: signing OK, signing wrong → 403 + retry path, body cap, response error mapping to `*DaemonError`.
- Create: `forward_server.go` — `(h *Hub).forwardHandler` on internal mux. Receiver flow: length check → header parse → timestamp window → body LimitReader → HMAC verify → nonce INSERT atomic → audit log → local-registry lookup → `sendCommandToLocal`/`sendCommandStreamToLocal`. Streaming via codec.
- Create: `forward_server_test.go`.
- Create: `drain_server.go` — `(h *Hub).drainHandler` on internal mux. Loopback bypass via `net.IP.IsLoopback`; else HMAC verify. Iterates `localReg`, sends `observer_draining` event, closes WS.
- Create: `drain_server_test.go`.
- Modify: `wiring.go` — `MountAll(publicMux, internalMux *http.ServeMux, resolver, agentserverURL, store, cluster ClusterRuntime)`. Builds `*sharedRegistry`/`*forwardClient`/`*pgTurnStore` (+ for telemetry: returns `telemetryAllower` selection) when `cluster.AdvertiseURL != ""`. Mounts `/forward` + `/drain` on internalMux. Starts sweeper goroutine.
- Modify: `wiring_test.go` — update for new signature.
- Modify: existing `*_test.go` (`hub_test.go`, `proxy_test.go`, `http_test.go`, `tree_test.go`, `race_test.go`, `livelock_test.go`, `e2e_test.go`, `integration_test.go`) — `daemonConn{}` literals get `shortID:` field (sentinel = existing `id` value for parity).
- Create: `multi_pod_test.go` — `OBSERVER_POSTGRES_TEST_DSN`-skipped; two `Hub` instances + shared PG. Cross-pod daemon visibility + forwarding + turn dedup + sweep.
- Create: `multi_pod_files_test.go` — forward pathological 2 MiB-of-`\x01` file; assert `TooLarge=true`, envelope < 1 MiB.

### commanderhub authstore (`internal/commanderhub/authstore/`)

- Modify: `schema_postgres.sql` — append `commander_daemons` + `commander_turns` + `commander_forward_nonces` + `commander_telemetry_buckets`.
- Create: `schema_postgres_rollback.sql` — `DROP TABLE IF EXISTS …` for all four.
- Modify: `postgres_test.go` — extend `TestPostgresStore_Conformance` (env-skipped) with assertions: tables exist, PKs correct, CHECK constraints work.
- Modify: `migrate.go` — unchanged (still `db.Exec(schema)`).

### commander shared package (`internal/commander/`)

- Modify: `protocol.go` — add `ErrCodeDaemonUpgradeRequired` and `CapabilityFilePreviewEncodedCap` constants.
- Modify: `files.go::Handler.ReadFile` — JSON-encoded-size guard ≤ 768 KiB.
- Modify: `files_test.go` — test for pathological 2 MiB `\x01` file → `TooLarge=true`.

### Daemon binaries (`cmd/{driver-agent,slave-agent}/main.go`)

- Modify: both `RegisterPayload` literals to include `commander.CapabilityFilePreviewEncodedCap`.

### observer-server (`cmd/observer-server/`)

- Modify: `main.go`:
  - New `Cluster ClusterConfig` field on `Config`.
  - `AgentserverIdentityConfig.FreshTTL` → `*durationConfig yaml:"fresh_ttl"` (pointer-nullable).
  - `AgentserverIdentityConfig.RevocationChannel *string yaml:"revocation_channel"` (pointer-nullable).
  - `loadConfig`: merge sibling `nonsecret/observer.nonsecret.yaml` if present (extends the v3 spec contract).
  - `validateConfig`: partial-cluster rule + `cluster.enabled AND store.driver != "postgres"` reject + `cluster.internal_listen_addr` loopback-coverage check.
  - Post-merge defaulting (replaces 180s pre-seed): `FreshTTL = 30s if cluster.enabled else 180s` when nil; same shape for `RevocationChannel`.
  - `buildClusterRuntime(cfg, db)` factory.
  - `--drain-local` flag + subcommand → `cmd/observer-server/drain_local.go`.
  - `newPublicHTTPServer` + `newInternalHTTPServer` (no `WriteTimeout`; preserves SSE turns). Existing `newHTTPServer` removed (only caller switches to `newPublicHTTPServer`).
  - When cluster enabled: build a second `*http.Server` for internal mux. Both servers under `errgroup`; coordinated `Shutdown` on signal.
  - Migration gate: `MigratePostgres` runs when `agentserverURL != ""` OR (`telemetry.enabled && cluster.enabled`).
  - Telemetry limiter selection: `cluster.enabled && store.driver=="postgres"` → `*pgTelemetryLimiter`, else `*telemetryLimiter` (in-memory; unchanged).
- Create: `cluster_runtime.go` — `buildClusterRuntime(cfg *Config, db *sql.DB) (commanderhub.ClusterRuntime, error)`.
- Create: `drain_local.go` — `runDrainLocal(cfg *Config) int`. Validates `internal_listen_addr` is loopback-reachable. Exits 1 on config-read error; exits 0 (with WARN) on connect error after valid config.
- Modify: `main_test.go` — matrix tests for `validateConfig` partial cluster + identity-cache pointer-nullable defaulting.

### observerweb (`internal/observerweb/`)

- Modify: `rate_limit.go` — extract `telemetryAllower` interface; existing `*telemetryLimiter` becomes one impl.
- Create: `rate_limit_pg.go` — `*pgTelemetryLimiter` against `commander_telemetry_buckets`. Atomic UPSERT (`SET LOCAL lock_timeout = '100ms'` in transaction).
- Modify: `server.go` — `Handler.telemetryLimiter telemetryAllower` (was `*telemetryLimiter`); call-site at line 203-207 adapts to `(bool, error)` return: `(true,nil)→proceed, (false,nil)→429, (_,err)→503`. `Options.Cluster commanderhub.ClusterRuntime` field; `NewWithResolverOptions(...) (publicHandler, internalHandler http.Handler)` (two returns).
- Modify: `server_test.go` — update for dual-return + new Cluster field.
- Create: `rate_limit_pg_test.go` — env-skipped PG integration test.

### identity (`internal/identity/`)

- Modify: `cache.go` — `NewCache(delegate, cfg, opts ...CacheOption) Resolver` (variadic functional options preserve existing callers). New `WithRevocationChannel(listener *pgx.Conn, publisher *sql.DB, channel string)`. `evict(key)` method (private; only the revocation listener calls it).
- Create: `revocation_pg.go` — LISTEN goroutine on dedicated `*pgx.Conn`; NOTIFY publish on `*sql.DB` (separate connections required by pgx single-conn semantics). Publish policy: ALWAYS on `ErrRevoked`; on `ErrInvalid` ONLY if `tokenKey(token)` is in `c.entries` AND publish rate < 100/s (per-pod token bucket).
- Create: `cache_pg_test.go` — env-skipped: two `cacheResolver` against shared PG; NOTIFY-driven eviction propagates within 100ms.

### Helm chart (`deploy/charts/observer/`)

- Modify: `values.yaml`:
  - `replicaCount: 2 → 1`.
  - `config.identity.agentserver.freshTTL: "180s" → ""` (so binary's nil default fires).
  - `config.identity.agentserver.revocationChannel: "auto"` (new enum: `auto`|`enabled`|`disabled`).
  - New top-level `cluster:` block with `enabled: false`, `advertiseUrlEnv: OBSERVER_ADVERTISE_URL`, `secretEnv: OBSERVER_CLUSTER_SECRET`, `prevSecretEnv: OBSERVER_CLUSTER_SECRET_PREV`, `secretKey: cluster-secret`, `prevSecretKey: cluster-secret-prev`, `internalListenAddr: ":8091"`, `internalServicePort: 8091`, `headlessServiceName: ""`, `networkPolicy: { enabled: true }`.
- Modify: `values-production.example.yaml` — `cluster.enabled: true`, `config.identity.agentserver.freshTTL: "30s"`, `revocationChannel: "enabled"`.
- Modify: `templates/secret.yaml`:
  - Inside the secret.create gate: emit `fresh_ttl` and `revocation_channel` (Helm enum mapped to observer-config value) ONLY when explicitly set (conditional render replacing today's hard-coded `default "180s"`).
  - Add `cluster-secret`/`cluster-secret-prev` data keys (only when `cluster.enabled && secret.create`).
- Modify: `templates/configmap.yaml::observer.nonsecret.yaml`:
  - Add `identity.agentserver.fresh_ttl` conditional emission.
  - Add `identity.agentserver.revocation_channel` enum mapping (`auto`→omit, `enabled`→`"postgres"`, `disabled`→`""`, anything else → `fail`).
  - Add `cluster:` block (advertise_url_env, secret_env, prev_secret_env, internal_listen_addr) only when `cluster.enabled`.
- Modify: `templates/deployment.yaml`:
  - Merge today's conditional `initContainers` (Postgres-wait) with new cluster `assert-cluster-secret` init (env existence + length ≥ 32).
  - Container envs: `POD_IP` (downward API) + `OBSERVER_ADVERTISE_URL` + `OBSERVER_CLUSTER_SECRET` (+ optional `OBSERVER_CLUSTER_SECRET_PREV`) when cluster enabled.
  - Container ports: add `internal` (8091) when cluster enabled.
  - `lifecycle.preStop.exec`: `/usr/local/bin/observer-server --config /etc/observer/observer.yaml --drain-local --internal-port=8091` when cluster enabled.
  - `spec.strategy` block: `RollingUpdate { maxUnavailable: 0, maxSurge: 100% }` when cluster enabled.
- Create: `templates/validate.yaml` (no underscore) — comment-only output with four `fail` guards.
- Modify: `templates/service.yaml` — second headless Service (`<release>-observer-headless`, clusterIP None, publishNotReadyAddresses true) when cluster enabled.
- Create: `templates/networkpolicy.yaml` — two-rule NP: allow `service.port` from anywhere; restrict `cluster.internalServicePort` to observer peers only.
- Modify: `templates/ingress.yaml` + `templates/httproute.yaml` — deny `/api/commander/_internal/*` paths.
- Modify: `tests/chart_test.sh` — 7 new assertion blocks (per spec §"Chart tests").

### CI (`.github/workflows/`)

- Modify: `observer-deploy.yml`:
  - Smoke job: generate `cluster_secret` (48 chars) + `::add-mask::`; bump `replicaCount: 2`; render `cluster.enabled=true`. Resolve pod IPs in GitHub runner step (kubectl/kubeconfig present), render one wget Job per pod IP.
  - Release job: require `OBSERVER_CLUSTER_SECRET` (and optional `OBSERVER_CLUSTER_SECRET_PREV`) in the secret list.

### Docs

- Modify: `deploy/README.md` — pre-rollout coordination, three-phase rotation, mixed-version window caveat, `DaemonInfo.DaemonID` clients treat as opaque.
- Create: `dev/compose.multi-observer.yaml` + `dev/README.md` — 2 observers + 1 PG + nginx LB for local repro.

---

## Phase A — Foundation (6 tasks)

Each Phase A task is independent of the others except where noted; you can parallelize A1+A2+A3+A4+A6.

### Task A1: Add `commander.ErrCodeDaemonUpgradeRequired` + `CapabilityFilePreviewEncodedCap`

**Files:**
- Modify: `multi-agent/internal/commander/protocol.go:14-18` (CapabilityFiles block); `:124-128` (ErrCode block)
- Modify: `multi-agent/internal/commander/protocol_test.go` (append 2 tests)

**Interfaces:**
- Produces: `commander.ErrCodeDaemonUpgradeRequired = "daemon_upgrade_required"`; `commander.CapabilityFilePreviewEncodedCap = "file_preview_encoded_cap"`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/commander/protocol_test.go`:

```go
func TestErrCodeDaemonUpgradeRequiredDefined(t *testing.T) {
	if ErrCodeDaemonUpgradeRequired != "daemon_upgrade_required" {
		t.Fatalf("ErrCodeDaemonUpgradeRequired=%q want %q",
			ErrCodeDaemonUpgradeRequired, "daemon_upgrade_required")
	}
}

func TestCapabilityFilePreviewEncodedCapDefined(t *testing.T) {
	if CapabilityFilePreviewEncodedCap != "file_preview_encoded_cap" {
		t.Fatalf("CapabilityFilePreviewEncodedCap=%q want %q",
			CapabilityFilePreviewEncodedCap, "file_preview_encoded_cap")
	}
}
```

- [ ] **Step 2: Run; expect compile failure**

```sh
cd multi-agent
go test ./internal/commander -run 'TestErrCodeDaemonUpgradeRequiredDefined|TestCapabilityFilePreviewEncodedCapDefined' -count=1
```

Expected: `undefined: ErrCodeDaemonUpgradeRequired` and `undefined: CapabilityFilePreviewEncodedCap`.

- [ ] **Step 3: Add constants**

In `internal/commander/protocol.go`, find the capabilities block at lines 14-18:

```go
const (
	CapabilitySessions = "sessions"
	CapabilityTurn     = "turn"
	CapabilityFiles    = "files"
)
```

Replace with:

```go
const (
	CapabilitySessions              = "sessions"
	CapabilityTurn                  = "turn"
	CapabilityFiles                 = "files"
	// CapabilityFilePreviewEncodedCap signals the daemon enforces a JSON-
	// encoded size cap on read_file responses (see Handler.ReadFile).
	// Observer shared-mode gates read_file forwarding on this capability.
	CapabilityFilePreviewEncodedCap = "file_preview_encoded_cap"
)
```

Find the error-code block at lines 124-128:

```go
const (
	ErrCodeSessionNotFound       = "session_not_found"
	ErrCodeBackendUnavailable    = "backend_unavailable"
	ErrCodeSchemaVersionMismatch = "schema_version_mismatch"
	ErrCodeInvalidRequest        = "invalid_request"
	ErrCodeInternal              = "internal"
)
```

Replace with:

```go
const (
	ErrCodeSessionNotFound        = "session_not_found"
	ErrCodeBackendUnavailable     = "backend_unavailable"
	ErrCodeSchemaVersionMismatch  = "schema_version_mismatch"
	ErrCodeInvalidRequest         = "invalid_request"
	ErrCodeInternal               = "internal"
	// ErrCodeDaemonUpgradeRequired signals the daemon binary lacks a
	// capability the observer requires in shared mode. Observer maps this
	// to HTTP 426 Upgrade Required so the client can surface an actionable
	// "update your daemon" message.
	ErrCodeDaemonUpgradeRequired  = "daemon_upgrade_required"
)
```

- [ ] **Step 4: Re-run; expect pass**

```sh
go test ./internal/commander -count=1 -race
```

- [ ] **Step 5: Commit**

```sh
git add internal/commander/protocol.go internal/commander/protocol_test.go
git commit -m "feat(commander): add ErrCodeDaemonUpgradeRequired + CapabilityFilePreviewEncodedCap

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task A2: Daemon-side `Handler.ReadFile` JSON-encoded size cap + advertise capability

**Files:**
- Modify: `multi-agent/internal/commander/files.go:17-22` (consts) and `:76-132` (ReadFile body)
- Modify: `multi-agent/internal/commander/files_test.go` (append 1 test)
- Modify: `multi-agent/cmd/driver-agent/main.go::commander.RegisterPayload{...}.Capabilities`
- Modify: `multi-agent/cmd/slave-agent/main.go::commander.RegisterPayload{...}.Capabilities`

**Interfaces:**
- Consumes: `commander.CapabilityFilePreviewEncodedCap` (A1).
- Produces: `Handler.ReadFile` returns `TooLarge=true, Content=""` when `len(json.Marshal(res)) > 768 KiB`. Both daemons advertise the new capability.

- [ ] **Step 1: Write the failing test**

Append to `internal/commander/files_test.go`. Use the existing test helper pattern from a sibling `TestReadFile_*` test (grep the file for `newReadFileTestHandler` or whatever the existing fixture builder is called; if no helper exists, follow the pattern of the closest existing test):

```go
func TestReadFile_EncodedSizeCapPreventsControlByteBlowup(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "tricky.txt")
	// 1 MiB of 0x01 bytes: valid UTF-8, not binary, but each byte JSON-
	// escapes as \uXXXX (6 bytes), so naive serialization would be ~6 MiB.
	tricky := bytes.Repeat([]byte{0x01}, 1024*1024)
	require.NoError(t, os.WriteFile(path, tricky, 0o644))

	h, sessID := newReadFileTestHandler(t, root) // adapt to whatever the existing fixture is
	res, err := h.ReadFile(context.Background(), sessID, "tricky.txt")
	require.NoError(t, err)
	require.True(t, res.TooLarge, "expected TooLarge=true")
	require.Empty(t, res.Content, "expected Content empty when TooLarge")

	out, err := json.Marshal(res)
	require.NoError(t, err)
	require.LessOrEqual(t, int64(len(out)), int64(1<<20),
		"encoded FileReadResult must stay under wsReadLimit (1 MiB)")
}
```

If the existing tests use a different fixture pattern, copy that pattern exactly. Add `"encoding/json"` and `"bytes"` to the test file imports if missing.

- [ ] **Step 2: Run; expect failure**

```sh
go test ./internal/commander -run TestReadFile_EncodedSizeCapPreventsControlByteBlowup -count=1
```

Expected: `expected TooLarge=true` (today's code returns full 1 MiB content; marshal would be ~6 MiB).

- [ ] **Step 3: Add `maxEncodedFileResponse` + encoded-size guard**

In `internal/commander/files.go`, add `"encoding/json"` to the imports (currently absent — verify with `grep '"encoding/json"' internal/commander/files.go`).

After the existing `var (... errFileRequest ... errPathOutsideRoot ...)` block (around line 22), add:

```go
// maxEncodedFileResponse bounds the JSON-encoded FileReadResult so the
// wire payload stays under observer wsReadLimit (1 MiB) and forwarding
// envelope cap (1 MiB). The cap leaves ~256 KiB headroom for the
// commander.Envelope wrapper (type, id, payload field framing).
//
// Defends against pathological all-low-ASCII-control text files where
// each byte JSON-escapes as \uXXXX (6 bytes), turning a 1 MiB raw file
// into a 6 MiB JSON string.
const maxEncodedFileResponse = 768 * 1024
```

In `Handler.ReadFile` (currently ends at line 132), find the final block:

```go
	res.MIME = http.DetectContentType(body)
	if bytes.IndexByte(body, 0) >= 0 || !utf8.Valid(body) {
		res.Binary = true
		return res, nil
	}
	res.Content = string(body)
	return res, nil
}
```

Replace with:

```go
	res.MIME = http.DetectContentType(body)
	if bytes.IndexByte(body, 0) >= 0 || !utf8.Valid(body) {
		res.Binary = true
		return res, nil
	}
	res.Content = string(body)

	// Encoded-size guard: marshalling can balloon valid-but-control-heavy
	// text up to 6x. If encoded form exceeds maxEncodedFileResponse,
	// surface TooLarge with empty content so the wire never carries a
	// payload that would breach wsReadLimit / forward cap.
	encoded, err := json.Marshal(res)
	if err != nil {
		return FileReadResult{}, fileRequestError(err)
	}
	if int64(len(encoded)) > maxEncodedFileResponse {
		over := FileReadResult{Path: res.Path, Size: res.Size, TooLarge: true}
		if over.Size < MaxFilePreviewBytes+1 {
			over.Size = MaxFilePreviewBytes + 1
		}
		return over, nil
	}
	return res, nil
}
```

- [ ] **Step 4: Run; expect pass**

```sh
go test ./internal/commander -count=1 -race
```

- [ ] **Step 5: Advertise capability in both daemon binaries**

Open `cmd/driver-agent/main.go`. Locate the `commander.RegisterPayload{...}` literal (around line 361 — search for `Capabilities:`). Add `commander.CapabilityFilePreviewEncodedCap` to the slice. Example transform: if the existing literal is

```go
Capabilities: []string{
    commander.CapabilitySessions,
    commander.CapabilityTurn,
    commander.CapabilityFiles,
},
```

change to

```go
Capabilities: []string{
    commander.CapabilitySessions,
    commander.CapabilityTurn,
    commander.CapabilityFiles,
    commander.CapabilityFilePreviewEncodedCap,
},
```

Apply the same change in `cmd/slave-agent/main.go` (around line 453).

- [ ] **Step 6: Run daemon binary tests**

```sh
go test ./cmd/driver-agent ./cmd/slave-agent ./internal/commander -count=1 -race
```

- [ ] **Step 7: Commit**

```sh
git add internal/commander/files.go internal/commander/files_test.go cmd/driver-agent/main.go cmd/slave-agent/main.go
git commit -m "feat(commander): bound ReadFile JSON-encoded size; advertise file_preview_encoded_cap

Pathological all-control-byte text files JSON-escape each byte as \\uXXXX,
producing payloads that exceed wsReadLimit (1 MiB) and the forwarding cap.
ReadFile now marshals the result and returns TooLarge=true (with empty
content) when the encoded size exceeds 768 KiB. driver-agent and
slave-agent advertise CapabilityFilePreviewEncodedCap so the observer can
gate read_file forwarding on this guarantee.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task A3: Add Postgres schema for `commander_daemons`, `commander_turns`, `commander_forward_nonces`, `commander_telemetry_buckets`

**Files:**
- Modify: `multi-agent/internal/commanderhub/authstore/schema_postgres.sql` (append 4 CREATE TABLE blocks)
- Create: `multi-agent/internal/commanderhub/authstore/schema_postgres_rollback.sql`
- Modify: `multi-agent/internal/commanderhub/authstore/postgres_test.go` (append 1 env-skipped test)
- Modify: `multi-agent/go.mod` + `multi-agent/go.sum` — add `github.com/DATA-DOG/go-sqlmock` for upcoming sqlmock tests in Phase B/D

**Interfaces:**
- Produces: four PG tables visible to phases B/C/D (`commander_daemons`, `commander_turns`, `commander_forward_nonces`, `commander_telemetry_buckets`). All idempotent (`CREATE TABLE IF NOT EXISTS`). All created by `MigratePostgres(db)`.

- [ ] **Step 1: Add `go-sqlmock` dependency**

```sh
cd multi-agent
go get github.com/DATA-DOG/go-sqlmock@v1.5.2
go mod tidy
```

- [ ] **Step 2: Write the failing test**

Append to `internal/commanderhub/authstore/postgres_test.go` (below `TestPostgresStore_Conformance`):

```go
func TestPostgresStore_ClusterTablesCreated(t *testing.T) {
	dsn := os.Getenv("OBSERVER_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("set OBSERVER_POSTGRES_TEST_DSN to run")
	}
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, MigratePostgres(db))

	for _, name := range []string{
		"commander_daemons",
		"commander_turns",
		"commander_forward_nonces",
		"commander_telemetry_buckets",
	} {
		var exists bool
		require.NoError(t, db.QueryRow(
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)`,
			name,
		).Scan(&exists))
		require.True(t, exists, "table %s not created", name)
	}

	// commander_daemons PK must include short_id (NOT a per-connection
	// daemon_id; that would lose ownership across reconnect).
	var pkCols string
	require.NoError(t, db.QueryRow(`
		SELECT string_agg(a.attname, ',' ORDER BY array_position(i.indkey, a.attnum))
		FROM pg_index i
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
		WHERE i.indrelid = 'commander_daemons'::regclass AND i.indisprimary
	`).Scan(&pkCols))
	require.Equal(t, "user_id,workspace_id,short_id", pkCols)

	// commander_turns CHECK constraint enforces the state enum.
	_, err = db.Exec(`
		INSERT INTO commander_turns (user_id, workspace_id, short_id, session_id, state)
		VALUES ('u', 'w', 's', 'sess', 'not_a_valid_state')
	`)
	require.Error(t, err, "expected CHECK constraint violation")

	// commander_telemetry_buckets composite PK (no NUL bytes in PG text).
	var btPK string
	require.NoError(t, db.QueryRow(`
		SELECT string_agg(a.attname, ',' ORDER BY array_position(i.indkey, a.attnum))
		FROM pg_index i
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
		WHERE i.indrelid = 'commander_telemetry_buckets'::regclass AND i.indisprimary
	`).Scan(&btPK))
	require.Equal(t, "workspace_id,agent_id,telemetry_key_id", btPK)
}
```

- [ ] **Step 3: Run; expect skip (no DSN) or fail (DSN set)**

```sh
# Without DSN (typical CI):
go test ./internal/commanderhub/authstore -run TestPostgresStore_ClusterTablesCreated -count=1
# → SKIP

# With local PG (recommended for human dev):
OBSERVER_POSTGRES_TEST_DSN="postgres://user:pass@localhost:5432/test?sslmode=disable" \
  go test ./internal/commanderhub/authstore -run TestPostgresStore_ClusterTablesCreated -count=1
# → FAIL: table commander_daemons not created
```

- [ ] **Step 4: Append schema blocks**

Append to `internal/commanderhub/authstore/schema_postgres.sql`:

```sql

-- Issue #49 / Findings A/B/D/E: cluster-mode tables.
-- See docs/superpowers/specs/2026-06-29-shared-daemon-registry-design.md (v19).

CREATE TABLE IF NOT EXISTS commander_daemons (
    user_id              text        NOT NULL,
    workspace_id         text        NOT NULL,
    short_id             text        NOT NULL,
    connection_id        text        NOT NULL,
    display_name         text        NOT NULL DEFAULT '',
    kind                 text        NOT NULL DEFAULT '',
    driver_version       text        NOT NULL DEFAULT '',
    capabilities         jsonb       NOT NULL DEFAULT '[]'::jsonb,
    owning_instance_url  text        NOT NULL,
    last_seen_at         timestamptz NOT NULL DEFAULT now(),
    created_at           timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, workspace_id, short_id),
    CONSTRAINT commander_daemons_user_id_nonempty       CHECK (length(user_id) > 0),
    CONSTRAINT commander_daemons_workspace_id_nonempty  CHECK (length(workspace_id) > 0),
    CONSTRAINT commander_daemons_short_id_nonempty      CHECK (length(short_id) > 0),
    CONSTRAINT commander_daemons_conn_id_nonempty       CHECK (length(connection_id) > 0),
    CONSTRAINT commander_daemons_owning_url_nonempty    CHECK (length(owning_instance_url) > 0)
);
CREATE INDEX IF NOT EXISTS commander_daemons_owner_idx
    ON commander_daemons (user_id, workspace_id);
CREATE INDEX IF NOT EXISTS commander_daemons_last_seen_idx
    ON commander_daemons (last_seen_at);

CREATE TABLE IF NOT EXISTS commander_turns (
    user_id            text        NOT NULL,
    workspace_id       text        NOT NULL,
    short_id           text        NOT NULL,
    session_id         text        NOT NULL,
    state              text        NOT NULL,
    awaiting_approval  boolean     NOT NULL DEFAULT false,
    active_worker      boolean     NOT NULL DEFAULT false,
    message            text        NOT NULL DEFAULT '',
    updated_at         timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, workspace_id, short_id, session_id),
    CONSTRAINT commander_turns_state_enum CHECK (
        state IN ('idle','queued','answering','awaiting_approval','done','error','disconnected')
    )
);
CREATE INDEX IF NOT EXISTS commander_turns_owner_idx
    ON commander_turns (user_id, workspace_id, short_id);
CREATE INDEX IF NOT EXISTS commander_turns_updated_idx
    ON commander_turns (updated_at);

CREATE TABLE IF NOT EXISTS commander_forward_nonces (
    nonce       text        PRIMARY KEY,
    received_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS commander_forward_nonces_received_idx
    ON commander_forward_nonces (received_at);

-- v13/v14: Finding E. Shared token bucket for telemetry rate limiter.
-- Composite PK because PG text cannot contain NUL bytes (the in-memory
-- limiter used "\x00"-separated string key).
CREATE TABLE IF NOT EXISTS commander_telemetry_buckets (
    workspace_id      text             NOT NULL,
    agent_id          text             NOT NULL,
    telemetry_key_id  text             NOT NULL,
    tokens            double precision NOT NULL,
    last_refilled     timestamptz      NOT NULL DEFAULT now(),
    updated_at        timestamptz      NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, agent_id, telemetry_key_id)
);
CREATE INDEX IF NOT EXISTS commander_telemetry_buckets_updated_idx
    ON commander_telemetry_buckets (updated_at);
```

- [ ] **Step 5: Create rollback file**

Create `internal/commanderhub/authstore/schema_postgres_rollback.sql`:

```sql
-- Manual down migration for the issue-#49 / Findings A/B/D/E cluster-mode tables.
-- Run with `psql "$OBSERVER_DATABASE_URL" -f schema_postgres_rollback.sql`
-- BEFORE rolling back observer-server to a pre-issue-#49 image.
DROP TABLE IF EXISTS commander_telemetry_buckets;
DROP TABLE IF EXISTS commander_forward_nonces;
DROP TABLE IF EXISTS commander_turns;
DROP TABLE IF EXISTS commander_daemons;
```

- [ ] **Step 6: Re-run; expect pass (or skip without DSN)**

```sh
go test ./internal/commanderhub/authstore -count=1 -race
# With DSN:
OBSERVER_POSTGRES_TEST_DSN="..." go test ./internal/commanderhub/authstore -count=1 -race
```

- [ ] **Step 7: Commit**

```sh
git add multi-agent/go.mod multi-agent/go.sum \
        internal/commanderhub/authstore/schema_postgres.sql \
        internal/commanderhub/authstore/schema_postgres_rollback.sql \
        internal/commanderhub/authstore/postgres_test.go
git commit -m "feat(commanderhub/authstore): commander_daemons + commander_turns + commander_forward_nonces + commander_telemetry_buckets

Four Postgres tables for the issue-#49 + Findings A/B/D/E cluster-mode
fixes. Idempotent DDL appended to MigratePostgres script. Down migration
in a separate manual rollback script (no auto-down via Helm).
Conformance test asserts tables, PK shapes (short_id keyed; composite
telemetry PK), and the CHECK enum on commander_turns.state.

Also adds go-sqlmock dependency for upcoming SQL-shape unit tests.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task A4: Rename `registry` → `localRegistry`; add `removeIf`; key by `short_id`

**Files:**
- Modify: `multi-agent/internal/commanderhub/registry.go:85-141` (type + constructor + methods)
- Modify: `multi-agent/internal/commanderhub/registry.go:39-57` (`daemonConn` adds `ownershipLost atomic.Bool`)
- Modify: `multi-agent/internal/commanderhub/registry_test.go` (append 2 tests)
- Modify: `multi-agent/internal/commanderhub/hub.go:30,47` (Hub.reg field type + constructor call)
- Modify: existing `*_test.go` literals that construct `daemonConn{}` — add `shortID:` field (verified rare; grep + sed)

**Interfaces:**
- Produces:
  - `*localRegistry` (renamed from `*registry`); `newLocalRegistry()` (renamed from `newRegistry`).
  - `(r *localRegistry).add(dc *daemonConn)` — indexes by `dc.shortID`, NOT `dc.id`.
  - `(r *localRegistry).lookup(o owner, shortID string) (*daemonConn, bool)` — keyed by shortID.
  - `(r *localRegistry).remove(o owner, shortID string)` — unconditional delete; kept for tests + non-shared paths.
  - `(r *localRegistry).removeIf(o owner, shortID, connectionID string)` — NEW: only deletes when the stored `dc.id` matches `connectionID`.
  - `(r *localRegistry).daemons(o owner) []DaemonInfo` — unchanged.
  - `daemonConn` gains: `ownershipLost atomic.Bool` (zero-value false; Phase B's confirmOwnership flips to true).

This task is a pure rename + field add. `Hub.ServeHTTP` admission/teardown is NOT touched here; Phase B Task B4 does that.

- [ ] **Step 1: Write the failing tests**

Append to `internal/commanderhub/registry_test.go`:

```go
func TestLocalRegistry_RemoveIfMatchesConnectionID(t *testing.T) {
	r := newLocalRegistry()
	o := owner{userID: "alice", workspaceID: "W1"}
	dc1 := &daemonConn{id: "conn-1", shortID: "agent-A", owner: o, displayName: "alice-mac"}
	r.add(dc1)
	if _, ok := r.lookup(o, "agent-A"); !ok {
		t.Fatal("expected agent-A present after add")
	}

	r.removeIf(o, "agent-A", "conn-different")
	if _, ok := r.lookup(o, "agent-A"); !ok {
		t.Fatal("removeIf with non-matching connection_id wrongly deleted entry")
	}

	r.removeIf(o, "agent-A", "conn-1")
	if _, ok := r.lookup(o, "agent-A"); ok {
		t.Fatal("removeIf with matching connection_id failed to delete")
	}
}

func TestLocalRegistry_LookupByShortID(t *testing.T) {
	r := newLocalRegistry()
	o := owner{userID: "alice", workspaceID: "W1"}
	dc := &daemonConn{id: "conn-xyz", shortID: "stable-agent-A", owner: o}
	r.add(dc)
	got, ok := r.lookup(o, "stable-agent-A")
	if !ok || got != dc {
		t.Fatalf("lookup(stable-agent-A) = (%v, %v); want (dc, true)", got, ok)
	}
	if _, ok := r.lookup(o, "conn-xyz"); ok {
		t.Fatal("lookup must key by shortID, not connection id")
	}
}
```

- [ ] **Step 2: Run; expect compile failure**

```sh
go test ./internal/commanderhub -run 'TestLocalRegistry_(RemoveIf|LookupByShort)' -count=1
```

Expected: `newLocalRegistry`/`removeIf` undefined.

- [ ] **Step 3: Replace registry.go (lines 85-141)**

In `internal/commanderhub/registry.go`, replace the existing `registry` type + `newRegistry` + `add` + `remove` + `lookup` + `daemons` block (lines 85-141) with:

```go
// localRegistry maps owner → shortID → *daemonConn. Externally keyed by
// stable short_id (so cluster-mode SQL rows align with in-memory state);
// removeIf uses the per-connection daemonConn.id as a connection_id
// generation guard so a same-pod fast reconnect's old WS goroutine
// doesn't delete the newer entry. All methods are goroutine-safe.
type localRegistry struct {
	mu    sync.Mutex
	conns map[owner]map[string]*daemonConn // owner → shortID → dc
}

func newLocalRegistry() *localRegistry {
	return &localRegistry{conns: make(map[owner]map[string]*daemonConn)}
}

// add indexes dc by its owner + shortID. dc.shortID, dc.id, dc.owner must be set.
func (r *localRegistry) add(dc *daemonConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.conns[dc.owner]
	if m == nil {
		m = make(map[string]*daemonConn)
		r.conns[dc.owner] = m
	}
	m[dc.shortID] = dc
}

// remove unconditionally deletes the entry. Kept for tests and code paths
// where the caller is certain no concurrent reconnect can have placed a
// newer entry. Production WS-teardown uses removeIf.
func (r *localRegistry) remove(o owner, shortID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.conns[o]
	if m == nil {
		return
	}
	delete(m, shortID)
	if len(m) == 0 {
		delete(r.conns, o)
	}
}

// removeIf deletes only when the stored conn's per-connection id matches
// connectionID. Defends same-pod fast reconnect: old WS's deferred remove
// must NOT delete the newly-placed entry.
func (r *localRegistry) removeIf(o owner, shortID, connectionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.conns[o]
	if m == nil {
		return
	}
	dc := m[shortID]
	if dc == nil || dc.id != connectionID {
		return
	}
	delete(m, shortID)
	if len(m) == 0 {
		delete(r.conns, o)
	}
}

func (r *localRegistry) lookup(o owner, shortID string) (*daemonConn, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	dc := r.conns[o][shortID]
	return dc, dc != nil
}

func (r *localRegistry) daemons(o owner) []DaemonInfo {
	r.mu.Lock()
	m := r.conns[o]
	conns := make([]*daemonConn, 0, len(m))
	for _, dc := range m {
		conns = append(conns, dc)
	}
	r.mu.Unlock()

	out := make([]DaemonInfo, 0, len(conns))
	for _, dc := range conns {
		out = append(out, dc.info())
	}
	return out
}
```

- [ ] **Step 4: Add `ownershipLost` to `daemonConn`**

In the same file, find the `daemonConn` struct (lines 39-57). Add the field. Replace:

```go
type daemonConn struct {
	id            string
	owner         owner
	shortID       string
	displayName   string
	kind          string
	driverVersion string

	metaMu       sync.Mutex
	capabilities map[string]bool
	lastSeenAt   time.Time

	conn      *websocket.Conn
	writeMu   sync.Mutex // serializes conn.WriteJSON / WriteControl
	pendingMu sync.Mutex // guards pending map
	pending   map[string]*pendingEntry
	done      chan struct{} // closed when the read loop exits
	hub       *Hub
}
```

with:

```go
type daemonConn struct {
	id            string // per-connection random hex; serves as the shared-registry connection_id
	owner         owner
	shortID       string // stable agentserver-assigned id; cluster registry PK column
	displayName   string
	kind          string
	driverVersion string

	metaMu       sync.Mutex
	capabilities map[string]bool
	lastSeenAt   time.Time

	conn      *websocket.Conn
	writeMu   sync.Mutex // serializes conn.WriteJSON / WriteControl
	pendingMu sync.Mutex // guards pending map
	pending   map[string]*pendingEntry
	done      chan struct{} // closed when the read loop exits
	hub       *Hub

	// ownershipLost: sticky-true once a shared-mode ownership check
	// observes that this connection is no longer the owner (sibling
	// pod claimed). Read by SendCommand[Stream] before write; set by
	// Phase B's confirmOwnership. Zero value is false (no extra init).
	ownershipLost atomic.Bool
}
```

Add `"sync/atomic"` to imports if missing (`grep '"sync/atomic"' internal/commanderhub/registry.go` — if absent, add it).

- [ ] **Step 5: Update Hub.reg field type + constructor**

In `internal/commanderhub/hub.go`, find:

```go
	reg          *registry
```

Replace with:

```go
	reg          *localRegistry
```

Find:

```go
		reg:          newRegistry(),
```

Replace with:

```go
		reg:          newLocalRegistry(),
```

- [ ] **Step 6: Fix existing test fixtures**

```sh
grep -nE '\bdaemonConn\{' internal/commanderhub/*_test.go > /tmp/dc-literals.txt
cat /tmp/dc-literals.txt
```

For every line: if the literal sets `id:` but not `shortID:`, add `shortID:` with the same string value. Example:

Before:
```go
hub.reg.add(&daemonConn{id: "a1", owner: owner{"alice", "W1"}, displayName: "alice-mac", kind: "claude"})
```

After:
```go
hub.reg.add(&daemonConn{id: "a1", shortID: "a1", owner: owner{"alice", "W1"}, displayName: "alice-mac", kind: "claude"})
```

Files to scan (from spec component map): `hub_test.go`, `proxy_test.go`, `http_test.go`, `tree_test.go`, `race_test.go`, `livelock_test.go`, `e2e_test.go`, `integration_test.go`. Tests that go through real WS handshake (`hub.ServeHTTP`) get `shortID` populated by hub.go:111 from `rp.ShortID`; only fixtures that construct daemonConn manually need the parity edit.

- [ ] **Step 7: Run; expect pass**

```sh
go vet ./internal/commanderhub/...
go test ./internal/commanderhub -count=1 -race
```

- [ ] **Step 8: Commit**

```sh
git add internal/commanderhub/registry.go \
        internal/commanderhub/registry_test.go \
        internal/commanderhub/hub.go \
        internal/commanderhub/*_test.go
git commit -m "refactor(commanderhub): rename registry to localRegistry; key by short_id; add removeIf

In-memory registry renamed to localRegistry and keyed externally by
stable short_id (matches the upcoming shared-registry PK). Per-connection
daemonConn.id serves as the connection generation; new removeIf()
compares it before deleting so a same-pod fast reconnect can't evict
the newer entry. daemonConn gains a sticky ownershipLost atomic.Bool
that Phase B's confirmOwnership flips when a sibling pod takes
ownership. Existing test fixtures gain shortID field set to the
existing id value for behavior parity.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task A5: Rename `turnKey.daemonID` → `shortID`; extract `turnStateBackend` interface

**Files:**
- Modify: `multi-agent/internal/commanderhub/turn_state.go` (rename `turnKey.daemonID` field; extract interface; rename `*turnStateStore` → `*memTurnStore`; ctx-ify methods)
- Modify: `multi-agent/internal/commanderhub/turn_state_test.go` (existing fixtures: `daemonID:` → `shortID:`; method calls: add ctx + handle (bool, error))
- Modify: `multi-agent/internal/commanderhub/hub.go` (`turns *turnStateStore` → `turns turnStateBackend`; `newTurnStateStore()` → `newMemTurnStore()`)
- Modify: `multi-agent/internal/commanderhub/http.go` (10 caller sites for `turnKey{owner:..., daemonID:..., sessionID:...}` and `hub.turns.*` calls)
- Modify: `multi-agent/internal/commanderhub/tree.go` (`mergeCurrentTurnState`, `refreshSessionRows` — update key construction + add ctx threading)

**Interfaces:**
- Produces:
  - `turnKey struct { owner owner; shortID string; sessionID string }` (was `daemonID`).
  - `turnStateBackend` interface (NEW):
    ```go
    type turnStateBackend interface {
        begin(ctx context.Context, key turnKey) (bool, error)
        set(ctx context.Context, key turnKey, state turnState) error
        finish(ctx context.Context, key turnKey, state turnState) error
        fail(ctx context.Context, key turnKey, msg string) error
        rekey(ctx context.Context, oldKey, newKey turnKey) error
        get(ctx context.Context, key turnKey) (turnSnapshot, error)
        // updateFromEnvelope is called by routeFrame on the WS-owning pod
        // to translate a daemon envelope into a state mutation. Will be
        // wired in Phase D when *pgTurnStore lands.
        updateFromEnvelope(ctx context.Context, key turnKey, command string, env commander.Envelope) error
        // cleanupOrphans flips any in-flight turn rows older than `older`
        // to 'disconnected'. Run by the per-pod sweep goroutine.
        cleanupOrphans(ctx context.Context, older time.Duration) error
    }
    ```
  - `*memTurnStore` (renamed from `*turnStateStore`) implements `turnStateBackend`. In-memory impl ignores `ctx`; returns `nil` error always. `updateFromEnvelope` and `cleanupOrphans` are no-ops on memTurnStore (single-pod doesn't need cross-pod sync; the existing http.go updateTurnStateFromEnvelope still runs).

This is a pure refactor — no observable behavior change. Postgres impl arrives in Phase D Task D2.

- [ ] **Step 1: Write the failing tests**

Append to `internal/commanderhub/turn_state_test.go`:

```go
func TestMemTurnStoreSatisfiesBackend(t *testing.T) {
	var _ turnStateBackend = newMemTurnStore()
}

func TestTurnKey_FieldRenamed(t *testing.T) {
	k := turnKey{owner: owner{userID: "u", workspaceID: "w"}, shortID: "agent-A", sessionID: "sess-1"}
	if k.shortID != "agent-A" {
		t.Fatalf("turnKey.shortID = %q; want agent-A", k.shortID)
	}
}
```

- [ ] **Step 2: Run; expect compile failure**

```sh
go test ./internal/commanderhub -run 'TestMemTurnStoreSatisfiesBackend|TestTurnKey_FieldRenamed' -count=1
```

- [ ] **Step 3: Rename field + extract interface + ctx-ify methods**

Edit `internal/commanderhub/turn_state.go`. Add `"context"` and `"github.com/yourorg/multi-agent/internal/commander"` to imports.

Find:

```go
type turnKey struct {
	owner     owner
	daemonID  string
	sessionID string
}
```

Replace with:

```go
type turnKey struct {
	owner     owner
	shortID   string
	sessionID string
}
```

After the `turnState` consts and `turnKey`/`turnSnapshot`, add the new interface:

```go
// turnStateBackend is the cross-pod-compatible abstraction over the
// per-pod in-memory turn store. Single-pod mode uses *memTurnStore;
// shared mode swaps in *pgTurnStore (Phase D).
//
// All methods take a ctx so PG-backed implementations can honor
// per-call timeouts. The in-memory impl ignores ctx; all errors are nil.
type turnStateBackend interface {
	begin(ctx context.Context, key turnKey) (bool, error)
	set(ctx context.Context, key turnKey, state turnState) error
	finish(ctx context.Context, key turnKey, state turnState) error
	fail(ctx context.Context, key turnKey, msg string) error
	rekey(ctx context.Context, oldKey, newKey turnKey) error
	get(ctx context.Context, key turnKey) (turnSnapshot, error)
	// updateFromEnvelope is the single-writer hook for the WS-owning pod
	// (called from routeFrame in Phase B); mirrors today's
	// http.go::updateTurnStateFromEnvelope. memTurnStore implementation
	// is a no-op (single-pod path still updates via http.go).
	updateFromEnvelope(ctx context.Context, key turnKey, command string, env commander.Envelope) error
	// cleanupOrphans flips in-flight turn rows older than `older` to
	// 'disconnected'. Run by the per-pod sweep goroutine. memTurnStore
	// no-op (in-memory store doesn't persist past process exit).
	cleanupOrphans(ctx context.Context, older time.Duration) error
}
```

Rename the struct + constructor:

```go
type memTurnStore struct {
	mu sync.Mutex
	m  map[turnKey]turnSnapshot
}

func newMemTurnStore() *memTurnStore {
	return &memTurnStore{m: make(map[turnKey]turnSnapshot)}
}
```

Update every method receiver from `*turnStateStore` to `*memTurnStore` AND make each method accept ctx + return error. In-memory bodies stay essentially unchanged. Example for `begin`:

```go
func (s *memTurnStore) begin(_ context.Context, key turnKey) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.m[key]
	if cur.InFlight {
		return false, nil
	}
	s.m[key] = turnSnapshot{State: turnStateQueued, InFlight: true, updatedAt: time.Now()}
	s.pruneLocked()
	return true, nil
}
```

Apply the same pattern to `set`, `finish`, `fail`, `rekey`, `get`. For `pruneLocked` — unchanged (unexported helper).

Add the two new no-op methods:

```go
func (s *memTurnStore) updateFromEnvelope(_ context.Context, _ turnKey, _ string, _ commander.Envelope) error {
	// Single-pod path: http.go::updateTurnStateFromEnvelope still drives state.
	// This method is the cross-pod hook only used by *pgTurnStore in shared mode.
	return nil
}

func (s *memTurnStore) cleanupOrphans(_ context.Context, _ time.Duration) error {
	// In-memory store doesn't persist past process exit; nothing to sweep.
	return nil
}
```

- [ ] **Step 4: Update Hub.turns field + constructor in hub.go**

Find:

```go
	turns        *turnStateStore
```

Replace with:

```go
	turns        turnStateBackend
```

Find:

```go
		turns:        newTurnStateStore(),
```

Replace with:

```go
		turns:        newMemTurnStore(),
```

- [ ] **Step 5: Update call sites in http.go and tree.go**

Grep:

```sh
grep -nE 'turnKey\{|hub\.turns\.|ch\.hub\.turns\.|\.turns\.' internal/commanderhub/*.go
```

For every literal `turnKey{owner: ..., daemonID: ..., sessionID: ...}`, change `daemonID:` → `shortID:`. The string value passed is still `daemonID` for now (the value happens to be the same string under v1 protocol since http.go gets it from URL path).

For every method call on `Hub.turns.{begin,set,finish,fail,rekey,get}`, add `ctx` as first arg and handle the new `(bool, error)` / `error` returns. Use `r.Context()` in `http.go::ch.turn`. In `tree.go::cachedSessionRows` and below, use the `ctx` already in scope (or add it to function signatures where missing — `mergeCurrentTurnState` needs a new ctx parameter).

Example transform for `ch.turn` at `http.go:231`:

Before:
```go
key := turnKey{owner: o, daemonID: daemonID, sessionID: sid}
if !ch.hub.turns.begin(key) {
    http.Error(w, "turn already in flight", http.StatusConflict)
    return
}
```

After:
```go
key := turnKey{owner: o, shortID: daemonID, sessionID: sid}
ok, err := ch.hub.turns.begin(r.Context(), key)
if err != nil {
    http.Error(w, err.Error(), http.StatusBadGateway)
    return
}
if !ok {
    http.Error(w, "turn already in flight", http.StatusConflict)
    return
}
```

Apply analogous transforms to the other 9 `.turns.{finish,fail,rekey,set,get}` call sites in `http.go`. Most non-`begin` calls don't have a Boolean return; just add ctx and discard the error or `_ = ` it for now (Phase D will tighten the error handling once `*pgTurnStore` lands; for the in-memory impl, error is always nil).

In `tree.go::mergeCurrentTurnState`, signature must change. Today:

```go
func (h *Hub) mergeCurrentTurnState(o owner, daemonID string, rows []SessionRow) {
	for i := range rows {
		snap := h.turns.get(turnKey{owner: o, daemonID: daemonID, sessionID: rows[i].SessionID})
```

After:

```go
func (h *Hub) mergeCurrentTurnState(ctx context.Context, o owner, daemonID string, rows []SessionRow) {
	for i := range rows {
		snap, _ := h.turns.get(ctx, turnKey{owner: o, shortID: daemonID, sessionID: rows[i].SessionID})
```

(`_, _ =` the error from `get` for in-memory; Phase D's `*pgTurnStore` integration tightens this.) Update the single caller of `mergeCurrentTurnState` in `tree.go::cachedSessionRows` to pass `ctx`.

Same pattern for `tree.go::refreshSessionRows` use of `turns.get(turnKey{...daemonID: ..., sessionID: ...})`.

- [ ] **Step 6: Update turn_state_test.go**

```sh
grep -nE 'turnKey\{|turnStateStore|newTurnStateStore' internal/commanderhub/turn_state_test.go
```

For each `turnKey{...daemonID: ...}`, change to `shortID:`. For each `newTurnStateStore()`, change to `newMemTurnStore()`. For each `.begin(key)`, change to `.begin(context.Background(), key)` and adapt return. Add `"context"` import.

- [ ] **Step 7: Run; expect pass**

```sh
go build ./internal/commanderhub/...
go test ./internal/commanderhub -count=1 -race
```

- [ ] **Step 8: Commit**

```sh
git add internal/commanderhub/turn_state.go \
        internal/commanderhub/turn_state_test.go \
        internal/commanderhub/hub.go \
        internal/commanderhub/http.go \
        internal/commanderhub/tree.go
git commit -m "refactor(commanderhub): turnKey.daemonID → shortID; extract turnStateBackend interface

In-memory turnStateStore becomes *memTurnStore implementing a new
turnStateBackend interface, with context-aware methods. turnKey field
renamed to match the upcoming PG-backed PK (user, workspace, short_id,
session). Pure refactor; no observable behavior change yet — Phase D
adds *pgTurnStore implementation.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task A6: Extract `telemetryAllower` interface

**Files:**
- Modify: `multi-agent/internal/observerweb/rate_limit.go` (extract interface; rename impl)
- Modify: `multi-agent/internal/observerweb/server.go:120-125` (Handler field type) and `:203-207` (call-site adapts to `(bool, error)` return)
- Modify: `multi-agent/internal/observerweb/rate_limit_test.go` (existing — update for `(bool, error)` if any tests call `.allow` directly)
- Modify: `multi-agent/internal/observerweb/server_test.go` (if any tests use `Handler.telemetryLimiter` directly)

**Interfaces:**
- Produces:
  - `telemetryKey struct { WorkspaceID, AgentID, TelemetryKeyID string }` — typed key replaces NUL-separated string.
  - `telemetryAllower interface { allow(ctx context.Context, key telemetryKey, now time.Time) (bool, error) }`.
  - `*telemetryLimiter` (in-memory, unchanged behavior) implements `telemetryAllower`. Returns `(_, nil)` always (no error path).
  - `(*Handler).telemetryLimiter` becomes `telemetryAllower` (was `*telemetryLimiter`).
  - Call-site at `server.go:204` maps `(true,nil)→proceed; (false,nil)→429; (_,err)→503` (with the same WARN log + ratelimit pattern).

In-memory call-site behavior is preserved exactly (always `nil` error → same 429-on-deny path). Phase D Task D3 adds `*pgTelemetryLimiter`.

- [ ] **Step 1: Write the failing test**

Append to `internal/observerweb/rate_limit_test.go`:

```go
func TestTelemetryLimiterSatisfiesAllower(t *testing.T) {
	var _ telemetryAllower = newTelemetryLimiter(60, 120)
}

func TestTelemetryLimiter_AllowSignatureBoolError(t *testing.T) {
	l := newTelemetryLimiter(60, 120)
	ok, err := l.allow(context.Background(), telemetryKey{WorkspaceID: "w", AgentID: "a", TelemetryKeyID: "k"}, time.Now())
	if err != nil {
		t.Fatalf("in-memory limiter must never error: %v", err)
	}
	if !ok {
		t.Fatal("first call should be allowed with default burst")
	}
}
```

- [ ] **Step 2: Run; expect compile failure**

```sh
go test ./internal/observerweb -run 'TestTelemetryLimiterSatisfiesAllower|TestTelemetryLimiter_AllowSignatureBoolError' -count=1
```

- [ ] **Step 3: Extract the interface + adapt `*telemetryLimiter`**

Edit `internal/observerweb/rate_limit.go`. Add `"context"` to imports if missing.

At the top of the file (after package + imports), add:

```go
// telemetryKey is the rate-limiter key (workspace, agent, telemetry key
// id) split into explicit fields. The in-memory limiter previously
// concatenated these with "\x00" separators; Postgres text columns
// cannot contain NUL bytes, so the shared-mode *pgTelemetryLimiter
// (Phase D) needs structured fields and the in-memory variant is
// converted in this task for symmetry.
type telemetryKey struct {
	WorkspaceID    string
	AgentID        string
	TelemetryKeyID string
}

// telemetryAllower abstracts the per-pod and PG-backed rate limiters
// behind a single interface. In-memory variant always returns nil error.
// Shared-mode variant (Phase D) can return err when PG is unreachable
// or lock_timeout fires.
type telemetryAllower interface {
	allow(ctx context.Context, key telemetryKey, now time.Time) (bool, error)
}
```

Change the `(l *telemetryLimiter).allow` method signature. Today:

```go
func (l *telemetryLimiter) allow(key string, now time.Time) bool {
```

After:

```go
func (l *telemetryLimiter) allow(_ context.Context, key telemetryKey, now time.Time) (bool, error) {
```

Inside the method body, change the bucket-map key from `key` (string) to a composite local string (or use a map keyed by `telemetryKey` directly — simpler):

Today's body uses `l.buckets[key]`. Change `buckets` field type from `map[string]telemetryBucket` to `map[telemetryKey]telemetryBucket`. Add `"context"` import. Update the return statements: replace `return false` with `return false, nil` and `return true` with `return true, nil`.

- [ ] **Step 4: Adapt the call-site in server.go**

In `internal/observerweb/server.go`, find the rate-limit block (`server.go:203-207`):

```go
	rateKey := agent.WorkspaceID + "\x00" + agent.ID + "\x00" + telemetryKeyID
	if h.telemetryLimiter != nil && !h.telemetryLimiter.allow(rateKey, time.Now()) {
		http.Error(w, "telemetry rate limit exceeded", http.StatusTooManyRequests)
		return
	}
```

Replace with:

```go
	if h.telemetryLimiter != nil {
		allowed, err := h.telemetryLimiter.allow(r.Context(), telemetryKey{
			WorkspaceID:    agent.WorkspaceID,
			AgentID:        agent.ID,
			TelemetryKeyID: telemetryKeyID,
		}, time.Now())
		switch {
		case err != nil:
			http.Error(w, "telemetry rate limit unavailable", http.StatusServiceUnavailable)
			log.Printf("observerweb: telemetry rate limit error: %v", err)
			return
		case !allowed:
			http.Error(w, "telemetry rate limit exceeded", http.StatusTooManyRequests)
			return
		}
	}
```

In the same file, change the `Handler.telemetryLimiter` field type from `*telemetryLimiter` to `telemetryAllower`. Confirm with `grep telemetryLimiter` that no other call sites break.

- [ ] **Step 5: Update any tests that touch the limiter directly**

```sh
grep -nE '\.allow\(' internal/observerweb/*_test.go
```

For each call site, update to `(ctx, telemetryKey{...}, time.Now())` form and adapt the return. Same for any test constructing the field directly.

- [ ] **Step 6: Run; expect pass**

```sh
go build ./internal/observerweb/...
go test ./internal/observerweb -count=1 -race
```

- [ ] **Step 7: Commit**

```sh
git add internal/observerweb/rate_limit.go \
        internal/observerweb/server.go \
        internal/observerweb/rate_limit_test.go \
        internal/observerweb/server_test.go
git commit -m "refactor(observerweb): extract telemetryAllower interface; (bool, error) return

telemetryLimiter becomes one impl of the new telemetryAllower interface,
keyed by typed telemetryKey{WorkspaceID, AgentID, TelemetryKeyID}
instead of NUL-joined string (Postgres text cannot contain NUL bytes;
Phase D adds the *pgTelemetryLimiter variant which needs structured
keys). allow() now returns (bool, error); in-memory variant returns
nil error always so behavior is preserved. Handler maps err→503 and
!allowed,nil→429.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Phase A Gate

After all 6 tasks, run:

```sh
cd multi-agent
go vet ./...
go test ./... -race -count=1
```

All tests should pass. No behavior change should be observable — Phase A is pure scaffolding.

**Dispatch to codex for Phase A review** before starting Phase B.

---

