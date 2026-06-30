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
- **Postgres integration tests are env-skipped** on `OBSERVER_POSTGRES_TEST_DSN`; CI does not require these. Unit tests on `*sql.DB` use `github.com/DATA-DOG/go-sqlmock` (new dependency added by Task B1 — its first importer).
- **Commit prefixes:** Go in `commanderhub` → `feat(commanderhub): …` or `fix(commanderhub): …`. Go in `commander` (shared) → `feat(commander): …`. observer-server → `feat(observer-server): …`. identity → `feat(identity): …`. observerweb → `feat(observerweb): …`. Chart → `chore(chart): …`. CI → `ci(observer-deploy): …`. Docs → `docs(…): …`. All commits MUST end with the existing `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>` line per CLAUDE.md.
- **No `go.work`.** Run all `go` commands from `multi-agent/`.

---

## Source Spec

Implement: `docs/superpowers/specs/2026-06-29-shared-daemon-registry-design.md` (v19).

## Phase plan

The plan is broken into **5 phases of 5–6 tasks each (28 tasks total)**. Each phase compiles & tests cleanly on its own; phase boundaries are good review checkpoints.

- **Phase A (Foundation, 6 tasks):** Constants, error codes, PG schema (3 tables), daemon-side `ReadFile` encoded-size cap, `localRegistry` rename + `removeIf`, `turnKey.shortID` rename + `turnStateBackend` interface, `telemetryAllower` interface. No behavior change yet.
- **Phase B (Shared registry + heartbeat, 5 tasks):** `sharedRegistry` Go type + SQL UPSERT/heartbeat/DELETE/lookupRemote/listAll, heartbeat goroutine with ownership-loss force-close, `dc.confirmOwnership`, `ServeHTTP` admission gating (connectUpsert before localReg.add), sweep goroutine (commander_daemons + commander_forward_nonces + commander_telemetry_buckets).
- **Phase C (Forwarding + drain + cmdID, 6 tasks):** Length-prefixed envelope codec, HMAC + nonce auth + nonces table, `forwardClient.send`/`stream`, `forwardServer` handler + audit log, `drainServer` endpoint with loopback/HMAC auth, `Hub.nextCmdID` pod-prefix.
- **Phase D (Wiring, read-path migration, observer-server lifecycle, 6 tasks):** `Hub.attachSharedRegistry` + `listDaemons` + `lookupDaemon` + caller migration + `Hub.Close`, `pgTurnStore` (cross-pod begin/get/updateFromEnvelope), `pgTelemetryLimiter`, identity revocation channel (functional-options NewCache + WithRevocationChannel + revocation_pg.go), observer-server `Cluster ClusterConfig` + `loadConfig` merge + `validateConfig` + dual-listener lifecycle (errgroup + `Shutdown`), multi-pod regression tests.
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

The existing test helper at `internal/commander/files_test.go:16-22` is `handlerForFileRoot(root)` (returns a `*Handler` for session `"s1"` rooted at `root`). **The file does NOT currently import `testify/require` — use stdlib assertions.** Append to `internal/commander/files_test.go`:

```go
func TestReadFile_EncodedSizeCapPreventsControlByteBlowup(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "tricky.txt")
	// 1 MiB of 0x01 bytes: valid UTF-8, not binary, but each byte JSON-
	// escapes as \uXXXX (6 bytes), so naive serialization would be ~6 MiB.
	tricky := bytes.Repeat([]byte{0x01}, 1024*1024)
	if err := os.WriteFile(path, tricky, 0o644); err != nil {
		t.Fatal(err)
	}

	h := handlerForFileRoot(root)
	res, err := h.ReadFile(context.Background(), "s1", "tricky.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !res.TooLarge {
		t.Fatalf("expected TooLarge=true; got Content len=%d, Binary=%v", len(res.Content), res.Binary)
	}
	if res.Content != "" {
		t.Fatalf("expected Content empty when TooLarge; got len=%d", len(res.Content))
	}

	out, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if int64(len(out)) > 1<<20 {
		t.Fatalf("encoded FileReadResult = %d bytes exceeds 1 MiB cap", len(out))
	}
}
```

Add `"encoding/json"` to the test file imports if missing (`grep '"encoding/json"' internal/commander/files_test.go` — likely absent; `bytes` is already imported).

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

- [ ] **Step 5: ADD `Capabilities` field to both daemon binaries' RegisterPayload**

NOTE (codex plan round-1 MAJOR #7): NEITHER `cmd/driver-agent/main.go` NOR `cmd/slave-agent/main.go` currently has a `Capabilities:` field in their `RegisterPayload` literal. The field exists on the struct (`commander.RegisterPayload.Capabilities []string`) but is omitted (so the slice is nil; the hub code at `hub.go:115-124` then merges-in defaults `CapabilitySessions` + `CapabilityTurn`). **Phase A2 ADDS the field explicitly** so both daemons advertise the new file-preview capability and any future ones.

Open `cmd/driver-agent/main.go`. Locate the `commander.RegisterPayload{...}` literal at line 361:

```go
Register: commander.RegisterPayload{
    SchemaVersion: commander.SchemaVersion,
    Kind:          cfg.Agent.Kind,
    AgentBin:      cfg.Agent.Bin,
    AgentWorkDir:  cfg.Agent.WorkDir,
    DisplayName:   cfg.Discovery.DisplayName,
    DriverVersion: driverVersion,
    ShortID:       cfg.Credentials.ShortID,
},
```

Add a `Capabilities` field at the end of the literal:

```go
Register: commander.RegisterPayload{
    SchemaVersion: commander.SchemaVersion,
    Kind:          cfg.Agent.Kind,
    AgentBin:      cfg.Agent.Bin,
    AgentWorkDir:  cfg.Agent.WorkDir,
    DisplayName:   cfg.Discovery.DisplayName,
    DriverVersion: driverVersion,
    ShortID:       cfg.Credentials.ShortID,
    Capabilities: []string{
        commander.CapabilitySessions,
        commander.CapabilityTurn,
        commander.CapabilityFiles,
        commander.CapabilityFilePreviewEncodedCap,
    },
},
```

Apply the equivalent change in `cmd/slave-agent/main.go` at line 453 (after the `ShortID` line).

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
- (go-sqlmock dependency is added in Phase B Task B1, its first importer; A3 doesn't need it.)

**Interfaces:**
- Produces: four PG tables visible to phases B/C/D (`commander_daemons`, `commander_turns`, `commander_forward_nonces`, `commander_telemetry_buckets`). All idempotent (`CREATE TABLE IF NOT EXISTS`). All created by `MigratePostgres(db)`.

- [ ] **Step 1: Add `go-sqlmock` dependency (deferred to first task that actually imports it)**

`go-sqlmock` is FIRST imported by Task B1's `registry_shared_test.go`. Running `go get … && go mod tidy` here in A3 (before any import exists) would have `go mod tidy` immediately strip the dep as unused. Add the dep in B1 instead. A3 only needs the schema + rollback file + conformance test (which doesn't use sqlmock — it uses real PG via `OBSERVER_POSTGRES_TEST_DSN`).

(This step is intentionally a no-op for A3; left here as a reminder that the dep lives with B1.)

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
git add internal/commanderhub/authstore/schema_postgres.sql \
        internal/commanderhub/authstore/schema_postgres_rollback.sql \
        internal/commanderhub/authstore/postgres_test.go
git commit -m "feat(commanderhub/authstore): commander_daemons + commander_turns + commander_forward_nonces + commander_telemetry_buckets

Four Postgres tables for the issue-#49 + Findings A/B/D/E cluster-mode
fixes. Idempotent DDL appended to MigratePostgres script. Down migration
in a separate manual rollback script (no auto-down via Helm).
Conformance test asserts tables, PK shapes (short_id keyed; composite
telemetry PK), and the CHECK enum on commander_turns.state.

(go-sqlmock dependency is added in Phase B Task B1 — its first importer.)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task A4: Rename `registry` → `localRegistry`; add `removeIf`; key by routing-id; routing-id fallback for empty short_id; update `ServeHTTP` teardown to use the new key

**Files:**
- Modify: `multi-agent/internal/commanderhub/registry.go:59-83` (`daemonConn.info()` — emit `routingID()` as `DaemonInfo.DaemonID`)
- Modify: `multi-agent/internal/commanderhub/registry.go:85-141` (type + constructor + methods)
- Modify: `multi-agent/internal/commanderhub/registry.go:39-57` (`daemonConn` adds `ownershipLost atomic.Bool`; add `routingID() string` method)
- Modify: `multi-agent/internal/commanderhub/registry_test.go` (append 4 tests: `TestLocalRegistry_RemoveIfMatchesConnectionID`, `TestLocalRegistry_LookupByShortID`, `TestDaemonConn_Info_ExposesShortIDAsDaemonID`, `TestDaemonConn_LegacyEmptyShortID_FallsBackToDcID`)
- Modify: `multi-agent/internal/commanderhub/hub.go:27-40` (Hub.reg field type only — `*registry` → `*localRegistry`. NOT adding sharedReg/forwardCli/turns here; those land in the tasks that define their types.)
- Modify: `multi-agent/internal/commanderhub/hub.go:47` (`newRegistry()` → `newLocalRegistry()`)
- Modify: `multi-agent/internal/commanderhub/hub.go::ServeHTTP` (UPDATE today's `defer h.reg.remove(o, dc.id)` and `defer h.invalidateDaemonSessions(o, dc.id)` to use `dc.routingID()` — without this, A4 leaks stale entries until B4 rewrites the teardown)
- Modify: existing `*_test.go` literals that construct `daemonConn{}` — add `shortID:` field where parity test fixtures need it; old fixtures with only `id:` continue to work via the routingID fallback

**Routing-ID fallback (codex round-2 BLOCKER #3):** `RegisterPayload.ShortID` is documented as optional in `commander/protocol.go:43` and spec v19 keeps it optional in single-pod mode (only cluster mode requires it; B4 rejects empty there). To preserve old-daemon single-pod behavior, add a method:

```go
// routingID returns the key used by localRegistry.{add,lookup,remove}
// AND by DaemonInfo.DaemonID. In cluster mode shortID is mandatory;
// for single-pod legacy daemons connecting with empty shortID it falls
// back to the per-connection id (today's behavior, byte-exact).
func (dc *daemonConn) routingID() string {
	if dc.shortID != "" {
		return dc.shortID
	}
	return dc.id
}
```

This guarantees:
- New cluster daemons (with shortID): keyed/displayed as `shortID`. UI URLs survive reconnect.
- Old single-pod daemons (no shortID): keyed/displayed as `dc.id` (per-connection hex) — **bit-exact preservation of v0.0.9 behavior**.
- Cluster-mode admission (B4) still rejects empty `shortID` so the fallback only fires for single-pod.

**Single-pod regression invariant:** existing single-pod deployments running v0.0.9 daemons see `DaemonInfo.DaemonID = dc.id` — UNCHANGED post-A4 because their `shortID` is empty and `routingID()` falls back to `dc.id`. **Verification:** Step 7 runs the full test suite; any test that constructs `daemonConn{id: "x"}` without `shortID` continues to see `DaemonID: "x"` via fallback.

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

// DaemonInfo.DaemonID must round-trip with the same key that lookup uses
// (the URL pattern /api/commander/daemons/{id}/... feeds it back into
// lookup). v5/v6 spec switched this from per-connection id to stable
// short_id so bookmarks survive daemon reconnect.
func TestDaemonConn_Info_ExposesShortIDAsDaemonID(t *testing.T) {
	dc := &daemonConn{id: "conn-xyz", shortID: "stable-agent-A", owner: owner{userID: "u", workspaceID: "w"}, displayName: "name", kind: "claude", driverVersion: "0.0.10"}
	di := dc.info()
	if di.DaemonID != "stable-agent-A" {
		t.Fatalf("DaemonInfo.DaemonID = %q; want stable-agent-A (short_id)", di.DaemonID)
	}
	if di.ShortID != "stable-agent-A" {
		t.Fatalf("DaemonInfo.ShortID = %q; want stable-agent-A", di.ShortID)
	}
}

// Single-pod legacy fallback (codex round-2 BLOCKER #3 + round-5 MAJOR #2):
// a daemon connecting with EMPTY shortID (v0.0.9 behavior) must continue
// to be addressable. routingID() falls back to dc.id; DaemonInfo.DaemonID
// exposes that id; lookup/remove round-trip via the id; legacy
// single-pod behavior is preserved bit-exactly.
func TestDaemonConn_LegacyEmptyShortID_FallsBackToDcID(t *testing.T) {
	r := newLocalRegistry()
	o := owner{userID: "alice", workspaceID: "W1"}
	// Legacy v0.0.9 daemon: shortID empty.
	dc := &daemonConn{id: "legacy-conn-abc", shortID: "", owner: o, displayName: "alice-mac"}

	if got := dc.routingID(); got != "legacy-conn-abc" {
		t.Fatalf("routingID with empty shortID = %q; want fallback to dc.id (%q)", got, dc.id)
	}
	if di := dc.info(); di.DaemonID != "legacy-conn-abc" {
		t.Fatalf("DaemonInfo.DaemonID for legacy daemon = %q; want %q", di.DaemonID, dc.id)
	}

	r.add(dc)
	got, ok := r.lookup(o, "legacy-conn-abc")
	if !ok || got != dc {
		t.Fatalf("legacy lookup by dc.id failed: ok=%v dc=%v", ok, got)
	}

	r.removeIf(o, "legacy-conn-abc", "legacy-conn-abc")
	if _, ok := r.lookup(o, "legacy-conn-abc"); ok {
		t.Fatal("legacy removeIf failed to delete")
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

// add indexes dc by its owner + routingID(). dc.id (always set) and either
// dc.shortID (cluster mode) or fallback to dc.id (single-pod legacy)
// determine the key. dc.owner must be set.
func (r *localRegistry) add(dc *daemonConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.conns[dc.owner]
	if m == nil {
		m = make(map[string]*daemonConn)
		r.conns[dc.owner] = m
	}
	m[dc.routingID()] = dc
}

// remove unconditionally deletes the entry. Kept for tests and code paths
// where the caller is certain no concurrent reconnect can have placed a
// newer entry. Production WS-teardown uses removeIf. The string arg is
// the routingID (shortID OR fallback dc.id; see daemonConn.routingID).
func (r *localRegistry) remove(o owner, routingID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.conns[o]
	if m == nil {
		return
	}
	delete(m, routingID)
	if len(m) == 0 {
		delete(r.conns, o)
	}
}

// removeIf deletes only when the stored conn's per-connection id matches
// connectionID. Defends same-pod fast reconnect: old WS's deferred remove
// must NOT delete the newly-placed entry. The routingID arg is shortID
// (cluster mode) OR fallback dc.id (single-pod legacy).
func (r *localRegistry) removeIf(o owner, routingID, connectionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.conns[o]
	if m == nil {
		return
	}
	dc := m[routingID]
	if dc == nil || dc.id != connectionID {
		return
	}
	delete(m, routingID)
	if len(m) == 0 {
		delete(r.conns, o)
	}
}

// lookup keys by routingID (shortID in cluster mode; fallback dc.id in
// single-pod legacy). Callers pass whatever they got from the URL or
// from DaemonInfo.DaemonID; the registry's add() used routingID() too,
// so the round-trip closes.
func (r *localRegistry) lookup(o owner, routingID string) (*daemonConn, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	dc := r.conns[o][routingID]
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

	// heartbeatErrCount: rate-limit counter for transient PG errors in
	// runHeartbeat (see Phase B Task B2). Atomic so the heartbeat
	// goroutine and any future observer don't race.
	heartbeatErrCount int64
}
```

Add `"sync/atomic"` to imports if missing (`grep '"sync/atomic"' internal/commanderhub/registry.go` — if absent, add it).

- [ ] **Step 5a: Update `daemonConn.info()` to expose routingID() as DaemonInfo.DaemonID**

In `internal/commanderhub/registry.go`, find `(dc *daemonConn) info()` (currently around lines 59-83):

```go
return DaemonInfo{
    DaemonID:      dc.id,
    ShortID:       dc.shortID,
    DisplayName:   dc.displayName,
    Kind:          dc.kind,
    DriverVersion: dc.driverVersion,
    Capabilities:  capabilities,
    LastSeenAt:    lastSeenAt,
}
```

Replace `DaemonID: dc.id` with `DaemonID: dc.routingID()`. The full block becomes:

```go
return DaemonInfo{
    DaemonID:      dc.routingID(), // cluster: stable short_id (UI bookmarks survive reconnect); single-pod legacy: dc.id (preserved bit-exactly)
    ShortID:       dc.shortID,
    DisplayName:   dc.displayName,
    Kind:          dc.kind,
    DriverVersion: dc.driverVersion,
    Capabilities:  capabilities,
    LastSeenAt:    lastSeenAt,
}
```

- [ ] **Step 5b: Update Hub.reg field type and constructor (registry rename only)**

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

**Do NOT add `sharedReg`/`forwardCli` here.** Those fields land in Task B1 (`sharedReg *sharedRegistry` after the sharedRegistry struct is declared in that task) and Task C3 (`forwardCli *forwardClient`). The `turns` field rewires to interface type in Task A5 (which adds the `turnStateBackend` declaration). Go has no forward declarations — A4 only changes what types exist already.

**Coordination with A5:** if A4 and A5 are executed in the same commit batch, the Hub constructor change (`newRegistry()` → `newLocalRegistry()`) and the `newTurnStateStore()` → `newMemTurnStore()` change land together. If A4 lands first, A5's `newMemTurnStore` rename is a separate small follow-up edit to the same constructor.

- [ ] **Step 5c: Update `ServeHTTP` teardown to use routingID() (codex round-2 BLOCKER #2)**

Today's teardown in `hub.go::ServeHTTP` (around lines 130-134):

```go
h.reg.add(dc)
defer h.reg.remove(o, dc.id)
defer h.invalidateDaemonSessions(o, dc.id)
defer close(dc.done)
defer dc.failAllPending()
```

Replace the two `dc.id` references with `dc.routingID()` so the teardown key matches the add key (otherwise `add` indexes by `routingID()` but `remove` tries to delete by `dc.id`; in the cluster case those differ and the entry leaks):

```go
h.reg.add(dc)
defer h.reg.remove(o, dc.routingID())
defer h.invalidateDaemonSessions(o, dc.routingID())
defer close(dc.done)
defer dc.failAllPending()
```

This is a minimal change that B4 will later supersede with the full `removeIf` + `sharedReg.remove` defer chain. A4 must do it because A4 changes the `add` key.

- [ ] **Step 6: Fix existing test fixtures (daemonConn literals + register payloads in WS tests)**

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

Files to scan (from spec component map): `hub_test.go`, `proxy_test.go`, `http_test.go`, `tree_test.go`, `race_test.go`, `livelock_test.go`, `e2e_test.go`, `integration_test.go`.

WS-handshake tests: `shortID` is populated by `hub.go:111` from `rp.ShortID`. **Do NOT blindly force all WS tests to use non-empty `ShortID`** — that masks the single-pod legacy regression we explicitly preserve. Instead:

- Tests that go through `hub.ServeHTTP` with `RegisterPayload.ShortID: ""` represent the legacy v0.0.9 case. Keep at least one such test (the one that's simplest to assert against) and add an assertion that `DaemonInfo.DaemonID` equals the per-connection `dc.id` (the routingID fallback). This locks in the legacy contract.
- For tests where `DaemonInfo.DaemonID` value is asserted explicitly against a literal string, either (a) supply a non-empty `ShortID` and assert against THAT, or (b) capture the daemonConn (via `hub.reg.daemons(o)[0].DaemonID` after admission) and use the returned value in subsequent assertions. Don't hardcode the per-connection hex.

- [ ] **Step 7: Run; expect pass**

```sh
go vet ./internal/commanderhub/...
go test ./internal/commanderhub -count=1 -race
```

(The `hub.turns.{mu,m}` direct-field test sites are addressed in Task A5 Step 5, which is the task that actually changes `Hub.turns` to interface type. A4 leaves `hub.turns` as the concrete `*turnStateStore` type; A4's tests still compile against today's field access.)

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
- Modify: `multi-agent/internal/commanderhub/http_test.go` (DIRECT field access: `hub.turns.mu` / `hub.turns.m[key]` at lines 255-262, 376, 385, 391, 399, 408, 418, 430 — replace with interface calls or `(s.turns).(*memTurnStore)` cast)
- Modify: `multi-agent/internal/commanderhub/tree.go` (`mergeCurrentTurnState`, `refreshSessionRows` — update key construction + add ctx threading)
- Modify: `multi-agent/internal/commanderhub/race_test.go`, `livelock_test.go`, `e2e_test.go`, `integration_test.go` — grep for any other `hub.turns.{mu,m,begin,set,finish,fail,rekey,get}` direct calls; update for interface signature.

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

- [ ] **Step 5: Update call sites in http.go, tree.go, and ALL `*_test.go`**

```sh
# Production call sites
grep -nE 'turnKey\{|hub\.turns\.|ch\.hub\.turns\.|\.turns\.' internal/commanderhub/*.go
# Test call sites (CRITICAL — includes direct field access to hub.turns.mu, hub.turns.m)
grep -nE 'turnKey\{|hub\.turns\.|\.turns\.(mu|m\[|begin|set|finish|fail|rekey|get)' internal/commanderhub/*_test.go
```

For every literal `turnKey{owner: ..., daemonID: ..., sessionID: ...}`, change `daemonID:` → `shortID:`. The string value passed is still `daemonID` for now (the value happens to be the same string under v1 protocol since http.go gets it from URL path).

For every method call on `Hub.turns.{begin,set,finish,fail,rekey,get}`, add `ctx` as first arg and handle the new `(bool, error)` / `error` returns. Use `r.Context()` in `http.go::ch.turn`. In `tree.go::cachedSessionRows` and below, use the `ctx` already in scope (or add it to function signatures where missing — `mergeCurrentTurnState` needs a new ctx parameter).

**Test-only direct field access (codex plan round-1 BLOCKER #4 — `http_test.go:255-262` writes to `hub.turns.mu` and `hub.turns.m[key]` directly):** these no longer compile after Hub.turns becomes interface type. Two options:

(a) Replace direct map writes with interface calls — e.g. instead of `hub.turns.m[key] = turnSnapshot{State: turnStateAnswering, InFlight: true}`, use `hub.turns.begin(context.Background(), key); hub.turns.set(context.Background(), key, turnStateAnswering)`.

(b) Add a test-only accessor on `*memTurnStore`. Append to `turn_state.go` (NOT test file — needs to be reachable from `http_test.go` in the same package):
```go
// snapshotForTest is exported for in-package tests that need to assert
// against the internal map. Not part of the turnStateBackend contract.
// Only valid on *memTurnStore (single-pod tests).
func (s *memTurnStore) snapshotForTest(key turnKey) (turnSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap, ok := s.m[key]
	return snap, ok
}

// setForTest seeds an arbitrary snapshot for test fixtures that need to
// install non-default state. Only valid on *memTurnStore.
func (s *memTurnStore) setForTest(key turnKey, snap turnSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = snap
}
```

Then in `http_test.go` and other test files, replace:
```go
hub.turns.mu.Lock()
hub.turns.m[key] = turnSnapshot{State: turnStateAnswering, InFlight: true, updatedAt: time.Now()}
hub.turns.mu.Unlock()
```
with:
```go
hub.turns.(*memTurnStore).setForTest(key, turnSnapshot{State: turnStateAnswering, InFlight: true, updatedAt: time.Now()})
```

Grep all hits and apply.

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
        internal/commanderhub/http_test.go \
        internal/commanderhub/tree.go \
        internal/commanderhub/tree_test.go \
        internal/commanderhub/race_test.go \
        internal/commanderhub/livelock_test.go \
        internal/commanderhub/e2e_test.go \
        internal/commanderhub/integration_test.go
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

## Phase B — Shared registry + heartbeat (5 tasks)

Builds the Postgres-backed registry layer. Tasks B1–B5 are sequential (B2 needs B1's type; B3 needs `daemonConn` ownershipLost from A4 + sharedReg from B1; B4 wires it all into `ServeHTTP`; B5 adds the per-pod sweep goroutine).

### Task B1: `*sharedRegistry` Go type + SQL (`connectUpsert`, `heartbeatUpsert`, `remove`, `lookupRemote`, `listAll`)

**Files:**
- Modify: `multi-agent/go.mod`, `multi-agent/go.sum` (add `github.com/DATA-DOG/go-sqlmock`)
- Create: `multi-agent/internal/commanderhub/registry_shared.go`
- Create: `multi-agent/internal/commanderhub/registry_shared_test.go` (sqlmock-driven)
- Modify: `multi-agent/internal/commanderhub/hub.go` (ADD `sharedReg *sharedRegistry` field to `Hub` struct now that the type exists)

- [ ] **Step 0: Add the `go-sqlmock` dependency**

```sh
cd multi-agent
go get github.com/DATA-DOG/go-sqlmock@v1.5.2
# Don't run `go mod tidy` yet — the test file added in Step 2 must exist
# first, otherwise tidy will treat the new dep as unused and strip it.
```

After Step 2 lands (test file imports `sqlmock`), commit the `go.mod`/`go.sum` changes with the test:

```sh
go mod tidy
```

**Interfaces:**
- Produces (in package `commanderhub`):

```go
type sharedRegistry struct {
    db             *sql.DB
    advertiseURL   string
    onlineTTL      time.Duration // 45s; cells fresher than this are "online" to readers
    deleteAfter    time.Duration // 5m; sweep deletes rows older than this (NOT 45s)
    heartbeatEvery time.Duration // 15s
    sweepEvery     time.Duration // 30s
    nonceTTL       time.Duration // 120s; sweepNonces threshold (= 2× HMAC timestamp window)
}

func newSharedRegistry(db *sql.DB, advertiseURL string) *sharedRegistry

// connectUpsert: INSERT … ON CONFLICT (user_id, workspace_id, short_id) DO
// UPDATE … WITHOUT ownership guard (a new WS connect is allowed to claim
// ownership; previous owner's heartbeat will see 0 rows and exit).
// Returns error on PG failure; caller MUST refuse the WS to prevent
// split-brain (cluster invisibility).
func (s *sharedRegistry) connectUpsert(ctx context.Context, dc *daemonConn) error

// heartbeatUpsert: ownership-guarded UPSERT. Returns:
//   stillOwn = true  ⇒ row exists with our (advertiseURL, connection_id); refreshed last_seen_at.
//   stillOwn = false ⇒ another pod or a newer same-pod connection claimed; caller MUST close WS.
//   err              ⇒ transient PG; caller continues (next tick may succeed).
func (s *sharedRegistry) heartbeatUpsert(ctx context.Context, dc *daemonConn) (stillOwn bool, err error)

// remove: ownership-guarded DELETE. Only deletes when both
// owning_instance_url AND connection_id match this pod+connection. Safe
// during same-pod fast reconnect.
func (s *sharedRegistry) remove(ctx context.Context, o owner, shortID, connectionID string) error

// lookupRemote: returns peerURL+info iff a fresh (last_seen_at > now() -
// onlineTTL) row exists AND owning_instance_url != s.advertiseURL.
// Returns ok=false on stale row or self-owned row. Returns err on PG.
func (s *sharedRegistry) lookupRemote(ctx context.Context, o owner, shortID string) (peerURL string, info DaemonInfo, ok bool, err error)

// listAll: returns every fresh DaemonInfo for owner (this pod + peers).
// Used by /api/commander/daemons, /tree, FanOutSessions.
func (s *sharedRegistry) listAll(ctx context.Context, o owner) ([]DaemonInfo, error)
```

- [ ] **Step 1: Write the failing tests (sqlmock-driven)**

Create `internal/commanderhub/registry_shared_test.go`:

```go
package commanderhub

import (
	"context"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestSharedRegistry_ConnectUpsertSQL(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newSharedRegistry(db, "http://10.0.0.42:8091")
	dc := &daemonConn{
		id:            "conn-1",
		shortID:       "agent-A",
		owner:         owner{userID: "alice", workspaceID: "W1"},
		displayName:   "alice-mac",
		kind:          "claude",
		driverVersion: "0.0.10",
	}

	mock.ExpectExec(connectUpsertSQL).
		WithArgs("alice", "W1", "agent-A", "conn-1", "alice-mac", "claude", "0.0.10", sqlmock.AnyArg(), "http://10.0.0.42:8091").
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, s.connectUpsert(context.Background(), dc))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSharedRegistry_HeartbeatStillOwn(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newSharedRegistry(db, "http://10.0.0.42:8091")
	dc := &daemonConn{
		id: "conn-1", shortID: "agent-A",
		owner:         owner{userID: "alice", workspaceID: "W1"},
		displayName:   "alice-mac", kind: "claude", driverVersion: "0.0.10",
	}

	// 9 args: user, workspace, short_id, conn_id, display, kind, driver, caps_json, owning_url
	mock.ExpectExec(heartbeatUpsertSQL).
		WithArgs("alice", "W1", "agent-A", "conn-1", "alice-mac", "claude", "0.0.10", sqlmock.AnyArg(), "http://10.0.0.42:8091").
		WillReturnResult(sqlmock.NewResult(0, 1))

	stillOwn, err := s.heartbeatUpsert(context.Background(), dc)
	require.NoError(t, err)
	require.True(t, stillOwn)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSharedRegistry_HeartbeatOwnershipLost(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newSharedRegistry(db, "http://10.0.0.42:8091")
	dc := &daemonConn{
		id: "conn-1", shortID: "agent-A",
		owner:         owner{userID: "alice", workspaceID: "W1"},
		displayName:   "alice-mac", kind: "claude", driverVersion: "0.0.10",
	}

	// 0 rows affected ⇒ sibling owns the row (ownership-guarded WHERE blocked SET).
	mock.ExpectExec(heartbeatUpsertSQL).
		WithArgs("alice", "W1", "agent-A", "conn-1", "alice-mac", "claude", "0.0.10", sqlmock.AnyArg(), "http://10.0.0.42:8091").
		WillReturnResult(sqlmock.NewResult(0, 0))

	stillOwn, err := s.heartbeatUpsert(context.Background(), dc)
	require.NoError(t, err)
	require.False(t, stillOwn)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSharedRegistry_RemoveGuardsConnectionID(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newSharedRegistry(db, "http://10.0.0.42:8091")
	o := owner{userID: "alice", workspaceID: "W1"}

	mock.ExpectExec(removeSQL).
		WithArgs("alice", "W1", "agent-A", "http://10.0.0.42:8091", "conn-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, s.remove(context.Background(), o, "agent-A", "conn-1"))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSharedRegistry_LookupRemoteSkipsSelfOwned(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newSharedRegistry(db, "http://10.0.0.42:8091")
	o := owner{userID: "alice", workspaceID: "W1"}

	// Row exists, owned by THIS pod → ok=false (no peer URL).
	rows := sqlmock.NewRows([]string{"owning_instance_url", "short_id", "display_name", "kind", "driver_version", "capabilities", "last_seen_at"}).
		AddRow("http://10.0.0.42:8091", "agent-A", "alice-mac", "claude", "0.0.10", `[]`, time.Now())
	mock.ExpectQuery(lookupRemoteSQL).
		WithArgs("alice", "W1", "agent-A", sqlmock.AnyArg()).
		WillReturnRows(rows)

	_, _, ok, err := s.lookupRemote(context.Background(), o, "agent-A")
	require.NoError(t, err)
	require.False(t, ok, "self-owned row must not be returned as remote")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSharedRegistry_LookupRemotePeerOwned(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newSharedRegistry(db, "http://10.0.0.42:8091")
	o := owner{userID: "alice", workspaceID: "W1"}

	rows := sqlmock.NewRows([]string{"owning_instance_url", "short_id", "display_name", "kind", "driver_version", "capabilities", "last_seen_at"}).
		AddRow("http://10.0.1.99:8091", "agent-A", "alice-mac", "claude", "0.0.10", `["sessions","turn"]`, time.Now())
	mock.ExpectQuery(lookupRemoteSQL).
		WithArgs("alice", "W1", "agent-A", sqlmock.AnyArg()).
		WillReturnRows(rows)

	peer, info, ok, err := s.lookupRemote(context.Background(), o, "agent-A")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "http://10.0.1.99:8091", peer)
	require.Equal(t, "agent-A", info.DaemonID)
	require.Equal(t, "alice-mac", info.DisplayName)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSharedRegistry_ListAllFreshOnly(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newSharedRegistry(db, "http://10.0.0.42:8091")
	o := owner{userID: "alice", workspaceID: "W1"}

	rows := sqlmock.NewRows([]string{"short_id", "display_name", "kind", "driver_version", "capabilities", "last_seen_at", "owning_instance_url"}).
		AddRow("agent-A", "alice-mac", "claude", "0.0.10", `["sessions"]`, time.Now(), "http://10.0.0.42:8091").
		AddRow("agent-B", "alice-laptop", "codex", "0.0.10", `["sessions"]`, time.Now(), "http://10.0.1.99:8091")
	mock.ExpectQuery(listAllSQL).
		WithArgs("alice", "W1", sqlmock.AnyArg()).
		WillReturnRows(rows)

	got, err := s.listAll(context.Background(), o)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "agent-A", got[0].DaemonID)
	require.Equal(t, "agent-B", got[1].DaemonID)
	require.NoError(t, mock.ExpectationsWereMet())
}
```

- [ ] **Step 2: Run; expect compile failure**

```sh
go test ./internal/commanderhub -run TestSharedRegistry_ -count=1
```

Expected: `undefined: newSharedRegistry`, `undefined: connectUpsertSQL`, etc.

- [ ] **Step 3: Create `registry_shared.go`**

Create `internal/commanderhub/registry_shared.go`:

```go
package commanderhub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"
)

// SQL statements as package-level consts so unit tests can assert exact
// shape via sqlmock.QueryMatcherEqual. Indentation/whitespace must match
// what the production code passes to db.ExecContext/QueryRowContext.

const connectUpsertSQL = `INSERT INTO commander_daemons (user_id, workspace_id, short_id, connection_id, display_name, kind, driver_version, capabilities, owning_instance_url, last_seen_at, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, now(), now()) ON CONFLICT (user_id, workspace_id, short_id) DO UPDATE SET connection_id = EXCLUDED.connection_id, display_name = EXCLUDED.display_name, kind = EXCLUDED.kind, driver_version = EXCLUDED.driver_version, capabilities = EXCLUDED.capabilities, owning_instance_url = EXCLUDED.owning_instance_url, last_seen_at = now()`

const heartbeatUpsertSQL = `INSERT INTO commander_daemons (user_id, workspace_id, short_id, connection_id, display_name, kind, driver_version, capabilities, owning_instance_url, last_seen_at, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, now(), now()) ON CONFLICT (user_id, workspace_id, short_id) DO UPDATE SET last_seen_at = now(), display_name = EXCLUDED.display_name, kind = EXCLUDED.kind, driver_version = EXCLUDED.driver_version, capabilities = EXCLUDED.capabilities WHERE commander_daemons.owning_instance_url = EXCLUDED.owning_instance_url AND commander_daemons.connection_id = EXCLUDED.connection_id`

const removeSQL = `DELETE FROM commander_daemons WHERE user_id = $1 AND workspace_id = $2 AND short_id = $3 AND owning_instance_url = $4 AND connection_id = $5`

const lookupRemoteSQL = `SELECT owning_instance_url, short_id, display_name, kind, driver_version, capabilities, last_seen_at FROM commander_daemons WHERE user_id = $1 AND workspace_id = $2 AND short_id = $3 AND last_seen_at > $4`

const listAllSQL = `SELECT short_id, display_name, kind, driver_version, capabilities, last_seen_at, owning_instance_url FROM commander_daemons WHERE user_id = $1 AND workspace_id = $2 AND last_seen_at > $3 ORDER BY display_name`

const sweepDaemonsSQL = `DELETE FROM commander_daemons WHERE last_seen_at < $1`

const sweepNoncesSQL = `DELETE FROM commander_forward_nonces WHERE received_at < $1`

const sweepTelemetryBucketsSQL = `DELETE FROM commander_telemetry_buckets WHERE updated_at < $1`

const (
	defaultOnlineTTL      = 45 * time.Second
	defaultDeleteAfter    = 5 * time.Minute
	defaultHeartbeatEvery = 15 * time.Second
	defaultSweepEvery     = 30 * time.Second
	defaultNonceTTL       = 120 * time.Second
)

type sharedRegistry struct {
	db             *sql.DB
	advertiseURL   string
	onlineTTL      time.Duration
	deleteAfter    time.Duration
	heartbeatEvery time.Duration
	sweepEvery     time.Duration
	nonceTTL       time.Duration
}

func newSharedRegistry(db *sql.DB, advertiseURL string) *sharedRegistry {
	return &sharedRegistry{
		db:             db,
		advertiseURL:   advertiseURL,
		onlineTTL:      defaultOnlineTTL,
		deleteAfter:    defaultDeleteAfter,
		heartbeatEvery: defaultHeartbeatEvery,
		sweepEvery:     defaultSweepEvery,
		nonceTTL:       defaultNonceTTL,
	}
}

// connectUpsert: claim ownership on new WS connect. INSERT ... ON CONFLICT
// DO UPDATE without ownership guard — the new connect is allowed to take
// ownership. Previous owner's heartbeat will see 0 rows (its WHERE
// includes connection_id) and exit.
func (s *sharedRegistry) connectUpsert(ctx context.Context, dc *daemonConn) error {
	dc.metaMu.Lock()
	capsList := make([]string, 0, len(dc.capabilities))
	for cap, on := range dc.capabilities {
		if on {
			capsList = append(capsList, cap)
		}
	}
	dc.metaMu.Unlock()
	sort.Strings(capsList)
	capsJSON, _ := json.Marshal(capsList)
	_, err := s.db.ExecContext(ctx, connectUpsertSQL,
		dc.owner.userID, dc.owner.workspaceID, dc.shortID, dc.id,
		dc.displayName, dc.kind, dc.driverVersion, string(capsJSON),
		s.advertiseURL)
	return err
}

// heartbeatUpsert: refresh last_seen_at ONLY when this pod + this exact
// connection still owns the row. 0 rows ⇒ ownership lost (sibling pod or
// newer same-pod connection took over).
//
// Implemented per spec v19 §"sharedRegistry methods" as an UPSERT with
// ownership-guarded WHERE clause (NOT a plain UPDATE). Two distinct
// behaviors arise from the WHERE:
//   - Row exists AND we still own it → SET fires → RowsAffected=1.
//   - Row exists AND sibling owns it → SET skipped (WHERE false) → RowsAffected=0.
//   - Row missing (sweep deleted it during a long PG hiccup) → INSERT
//     path fires → RowsAffected=1 → we re-claim ownership. This is
//     intentional self-healing (see spec §"Daemon admission + teardown
//     ordering" and the sweep TTL discussion: deleteAfter=5min >>
//     onlineTTL=45s so this case is rare).
func (s *sharedRegistry) heartbeatUpsert(ctx context.Context, dc *daemonConn) (stillOwn bool, err error) {
	dc.metaMu.Lock()
	capsList := make([]string, 0, len(dc.capabilities))
	for cap, on := range dc.capabilities {
		if on {
			capsList = append(capsList, cap)
		}
	}
	dc.metaMu.Unlock()
	sort.Strings(capsList)
	capsJSON, _ := json.Marshal(capsList)
	res, err := s.db.ExecContext(ctx, heartbeatUpsertSQL,
		dc.owner.userID, dc.owner.workspaceID, dc.shortID, dc.id,
		dc.displayName, dc.kind, dc.driverVersion, string(capsJSON),
		s.advertiseURL)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// remove: ownership + connection-id-guarded DELETE.
func (s *sharedRegistry) remove(ctx context.Context, o owner, shortID, connectionID string) error {
	_, err := s.db.ExecContext(ctx, removeSQL,
		o.userID, o.workspaceID, shortID, s.advertiseURL, connectionID)
	return err
}

// lookupRemote: peerURL+info iff fresh AND peer-owned.
func (s *sharedRegistry) lookupRemote(ctx context.Context, o owner, shortID string) (string, DaemonInfo, bool, error) {
	row := s.db.QueryRowContext(ctx, lookupRemoteSQL,
		o.userID, o.workspaceID, shortID, time.Now().Add(-s.onlineTTL))
	var ownerURL, displayName, kind, driverVersion, capabilitiesJSON string
	var sid string
	var lastSeen time.Time
	if err := row.Scan(&ownerURL, &sid, &displayName, &kind, &driverVersion, &capabilitiesJSON, &lastSeen); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", DaemonInfo{}, false, nil
		}
		return "", DaemonInfo{}, false, err
	}
	if ownerURL == s.advertiseURL {
		return "", DaemonInfo{}, false, nil
	}
	var capabilities []string
	_ = json.Unmarshal([]byte(capabilitiesJSON), &capabilities)
	return ownerURL, DaemonInfo{
		DaemonID:      sid,
		ShortID:       sid,
		DisplayName:   displayName,
		Kind:          kind,
		DriverVersion: driverVersion,
		Capabilities:  capabilities,
		LastSeenAt:    lastSeen.UTC().Format(time.RFC3339Nano),
	}, true, nil
}

// listAll: every fresh row for owner (this pod + peers).
func (s *sharedRegistry) listAll(ctx context.Context, o owner) ([]DaemonInfo, error) {
	rows, err := s.db.QueryContext(ctx, listAllSQL,
		o.userID, o.workspaceID, time.Now().Add(-s.onlineTTL))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]DaemonInfo, 0, 8)
	for rows.Next() {
		var sid, displayName, kind, driverVersion, capsJSON, ownerURL string
		var lastSeen time.Time
		if err := rows.Scan(&sid, &displayName, &kind, &driverVersion, &capsJSON, &lastSeen, &ownerURL); err != nil {
			return nil, err
		}
		var caps []string
		_ = json.Unmarshal([]byte(capsJSON), &caps)
		out = append(out, DaemonInfo{
			DaemonID:      sid,
			ShortID:       sid,
			DisplayName:   displayName,
			Kind:          kind,
			DriverVersion: driverVersion,
			Capabilities:  caps,
			LastSeenAt:    lastSeen.UTC().Format(time.RFC3339Nano),
		})
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: Add `sharedReg` field to Hub struct**

In `internal/commanderhub/hub.go`, find the Hub struct (post-A4 shape):

```go
type Hub struct {
	resolver     identity.Resolver
	upgrader     websocket.Upgrader
	reg          *localRegistry
	turns        turnStateBackend   // (added by A5)
	sessionCache *sessionListCache
	cmdSeq       atomic.Int64

	TurnTimeout time.Duration
}
```

Add `sharedReg *sharedRegistry` after `reg`:

```go
type Hub struct {
	resolver     identity.Resolver
	upgrader     websocket.Upgrader
	reg          *localRegistry
	sharedReg    *sharedRegistry // B1: nil in single-pod; populated by attachSharedRegistry (Phase B B4)
	turns        turnStateBackend
	sessionCache *sessionListCache
	cmdSeq       atomic.Int64

	TurnTimeout time.Duration
}
```

`NewHub` constructor remains unchanged; `sharedReg` defaults to nil.

- [ ] **Step 5: Run; expect pass**

```sh
go test ./internal/commanderhub -run TestSharedRegistry_ -count=1 -race
```

- [ ] **Step 6: Commit**

```sh
git add go.mod go.sum \
        internal/commanderhub/registry_shared.go \
        internal/commanderhub/registry_shared_test.go \
        internal/commanderhub/hub.go
git commit -m "feat(commanderhub): add sharedRegistry SQL layer (connectUpsert, heartbeat, remove, lookupRemote, listAll)

Postgres-backed registry of online daemons. connectUpsert claims
ownership on new WS connect; heartbeatUpsert is ownership-guarded (0
rows ⇒ sibling claimed); remove is connection_id-guarded against
same-pod fast reconnect; lookupRemote returns peer URL only when the
row is owned by another advertiseURL; listAll returns fresh rows for
all pods. SQL statements live as exported consts so sqlmock tests can
assert exact shape via QueryMatcherEqual.

Heartbeat is an UPSERT with ownership-guarded WHERE clause (per spec
v19): SET fires only when commander_daemons.owning_instance_url AND
connection_id match the heartbeat's intent. 0 rows ⇒ sibling/newer
connection took over (caller's runHeartbeatOnce force-closes WS).
INSERT path fires when the row is missing (long PG outage + sweep) so
the heartbeat self-heals by re-claiming.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task B2: heartbeat goroutine + `runHeartbeat` (ownership-loss → force-close WS)

**Files:**
- Modify: `multi-agent/internal/commanderhub/registry_shared.go` (add `runHeartbeat`)
- Modify: `multi-agent/internal/commanderhub/registry_shared_test.go` (add 2 tests)

**Interfaces:**
- Produces: `(s *sharedRegistry).runHeartbeat(ctx context.Context, dc *daemonConn)`. Loops every `heartbeatEvery` (15s) calling `heartbeatUpsert`. On `stillOwn=false`: marks `dc.ownershipLost.Store(true)`, **calls `dc.conn.Close()`** to force the WS read loop to exit (so ServeHTTP defers run + sibling's claim is honored), logs WARN, and returns. On `stillOwn=true`: logs nothing. On err: logs WARN at most once per 5 ticks per dc (avoid spam), continues. Exits when ctx cancelled.

- [ ] **Step 1: Append failing tests**

Append to `internal/commanderhub/registry_shared_test.go`:

```go
// To avoid timer-based race conditions, the production runHeartbeat is
// factored to expose runHeartbeatOnce(ctx, dc) which executes EXACTLY
// one tick body. Tests call it directly; runHeartbeat is just the for-
// loop wrapper.

func TestSharedRegistry_HeartbeatOnce_StillOwn(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newSharedRegistry(db, "http://10.0.0.42:8091")
	dc := &daemonConn{
		id: "conn-1", shortID: "agent-A",
		owner:         owner{userID: "alice", workspaceID: "W1"},
		displayName:   "alice-mac", kind: "claude", driverVersion: "0.0.10",
	}

	mock.ExpectExec(heartbeatUpsertSQL).
		WithArgs("alice", "W1", "agent-A", "conn-1", "alice-mac", "claude", "0.0.10", sqlmock.AnyArg(), "http://10.0.0.42:8091").
		WillReturnResult(sqlmock.NewResult(0, 1))

	keepRunning := s.runHeartbeatOnce(context.Background(), dc)
	require.True(t, keepRunning, "stillOwn should let the loop continue")
	require.False(t, dc.ownershipLost.Load())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSharedRegistry_HeartbeatOnce_ForceClosesOnOwnershipLoss(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newSharedRegistry(db, "http://10.0.0.42:8091")
	dc := newOwnershipTestDaemonConn(t, "conn-1", "agent-A", owner{userID: "alice", workspaceID: "W1"})

	mock.ExpectExec(heartbeatUpsertSQL).
		WithArgs("alice", "W1", "agent-A", "conn-1", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), "http://10.0.0.42:8091").
		WillReturnResult(sqlmock.NewResult(0, 0))

	keepRunning := s.runHeartbeatOnce(context.Background(), dc)
	require.False(t, keepRunning, "ownership loss must signal stop")
	require.True(t, dc.ownershipLost.Load(), "ownershipLost must be sticky-true")
	require.True(t, ownershipTestConnIsClosed(dc), "WS conn must be force-closed on ownership loss")
	require.NoError(t, mock.ExpectationsWereMet())
}
```

Add the helper to a new file `internal/commanderhub/registry_shared_helpers_test.go` (kept separate from `registry_shared_test.go` for clarity):

```go
package commanderhub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// newOwnershipTestDaemonConn returns a daemonConn whose `conn` is a
// real server-side *websocket.Conn over a localhost loopback connection,
// so dc.conn.Close() is observable via ownershipTestConnIsClosed.
//
// The server-side conn is what runHeartbeat will Close(); the client-side
// conn is held by the cleanup so it doesn't get GC'd mid-test.
func newOwnershipTestDaemonConn(t *testing.T, connID, shortID string, o owner) *daemonConn {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	serverCh := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("server upgrade: %v", err)
			return
		}
		serverCh <- c
	}))
	t.Cleanup(srv.Close)

	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	clientConn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = clientConn.Close() })

	select {
	case sc := <-serverCh:
		return &daemonConn{
			id: connID, shortID: shortID, owner: o, conn: sc,
			pending: make(map[string]*pendingEntry),
			done:    make(chan struct{}),
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server upgrade timeout")
		return nil
	}
}

func ownershipTestConnIsClosed(dc *daemonConn) bool {
	// Probe with a 100ms write deadline; gorilla returns websocket.ErrCloseSent
	// or net.OpError on closed conn.
	_ = dc.conn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
	err := dc.conn.WriteMessage(websocket.PingMessage, nil)
	return err != nil
}
```

- [ ] **Step 2: Run; expect compile failure**

- [ ] **Step 3: Add `runHeartbeat` to `registry_shared.go`**

```go
import (
	"log"
	"sync/atomic"
)

// runHeartbeatOnce executes one tick body: heartbeatUpsert + handle
// result. Returns false when the loop must stop (ownership lost OR
// ctx canceled). Returns true otherwise (still own, or transient PG
// error — caller continues looping).
//
// Exposed as a method (not a closure) so tests can call it directly
// without relying on timer races.
func (s *sharedRegistry) runHeartbeatOnce(ctx context.Context, dc *daemonConn) bool {
	hbCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	stillOwn, err := s.heartbeatUpsert(hbCtx, dc)
	switch {
	case err != nil:
		// Transient PG error — rate-limited log; caller continues looping.
		n := atomic.AddInt64(&dc.heartbeatErrCount, 1)
		if n%5 == 1 {
			log.Printf("commanderhub: heartbeatUpsert short_id=%s conn_id=%s pod=%s err=%v",
				dc.shortID, dc.id, s.advertiseURL, err)
		}
		return true
	case !stillOwn:
		log.Printf("commanderhub: heartbeat ownership lost short_id=%s conn_id=%s pod=%s; force-closing WS",
			dc.shortID, dc.id, s.advertiseURL)
		dc.ownershipLost.Store(true)
		// Force-close so the read loop wakes with io.EOF; ServeHTTP
		// defers then run localReg.removeIf + sharedReg.remove,
		// neither of which delete the new owner's state (both are
		// connection_id-guarded).
		_ = dc.conn.Close()
		return false
	default:
		atomic.StoreInt64(&dc.heartbeatErrCount, 0)
		return true
	}
}

// runHeartbeat ticks every s.heartbeatEvery, calling runHeartbeatOnce.
// Exits on ctx cancel OR when runHeartbeatOnce returns false (ownership
// loss).
func (s *sharedRegistry) runHeartbeat(ctx context.Context, dc *daemonConn) {
	ticker := time.NewTicker(s.heartbeatEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if !s.runHeartbeatOnce(ctx, dc) {
			return
		}
	}
}
```

This requires `daemonConn` to gain a `heartbeatErrCount int64` field (Task A4 should also add it alongside `ownershipLost`). Append to A4 Step 4 the field; if A4 has shipped without it, add it as a separate small edit in B2.

- [ ] **Step 4: Run; expect pass**

```sh
go test ./internal/commanderhub -run TestSharedRegistry_ -count=1 -race
```

- [ ] **Step 5: Commit**

```sh
git add internal/commanderhub/registry_shared.go \
        internal/commanderhub/registry_shared_test.go \
        internal/commanderhub/registry_shared_helpers_test.go
git commit -m "feat(commanderhub): runHeartbeat goroutine with ownership-loss force-close

Periodically refreshes commander_daemons.last_seen_at; on stillOwn=false
(sibling pod claimed via newer connection_id or different advertiseURL),
the goroutine force-closes the WS conn so the read loop wakes with EOF
and ServeHTTP's defers run. Both removeIf (local) and remove (shared)
are connection_id-guarded so neither deletes the new owner's state.

PG transient errors are rate-limited to 1 log per 5 consecutive
failures.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task B3: `(dc *daemonConn).confirmOwnership` — per-send PG ownership check

**Files:**
- Modify: `multi-agent/internal/commanderhub/registry_shared.go` (add `confirmOwnershipSQL` const)
- Modify: `multi-agent/internal/commanderhub/registry.go` (add `confirmOwnership` method to `daemonConn`)
- Create: `multi-agent/internal/commanderhub/registry_ownership_test.go`

**Prereq:** Task A4 added `Hub.sharedReg` field (so `dc.hub.sharedReg` compiles). Task B1 defined the `sharedRegistry` type itself. B3 wires per-send ownership confirmation between them.

**Interfaces:**
- Produces: `(dc *daemonConn) confirmOwnership(ctx context.Context) bool`. **Single-pod safe (codex round-8 MAJOR #2):** returns `true` immediately when `dc.hub == nil || dc.hub.sharedReg == nil` (single-pod mode has no PG to confirm against; callers MAY call this method unconditionally). Otherwise: returns false if `dc.ownershipLost.Load()` is already true (sticky negative cache); else issues a 500ms-bounded PG SELECT against `commander_daemons` and checks (owning_instance_url, connection_id) match. On any deviation OR PG error, sets `ownershipLost.Store(true)` and returns false. On match, returns true. **No positive cache** — every shared-mode SendCommand call pays one PG round-trip. Eliminates the v6/v7/v8 race window.

- [ ] **Step 1: Add `confirmOwnershipSQL` const to production code**

Append to `internal/commanderhub/registry_shared.go` (alongside the other SQL consts):

```go
const confirmOwnershipSQL = `SELECT owning_instance_url, connection_id FROM commander_daemons WHERE user_id = $1 AND workspace_id = $2 AND short_id = $3`
```

- [ ] **Step 2: Write the failing tests**

Create `internal/commanderhub/registry_ownership_test.go`:

```go
package commanderhub

import (
	"context"
	"database/sql"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestDaemonConn_ConfirmOwnership_StillOwn(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newSharedRegistry(db, "http://10.0.0.42:8091")
	dc := &daemonConn{id: "conn-1", shortID: "agent-A", owner: owner{userID: "alice", workspaceID: "W1"}, hub: &Hub{sharedReg: s}}

	rows := sqlmock.NewRows([]string{"owning_instance_url", "connection_id"}).
		AddRow("http://10.0.0.42:8091", "conn-1")
	mock.ExpectQuery(confirmOwnershipSQL).
		WithArgs("alice", "W1", "agent-A").
		WillReturnRows(rows)

	require.True(t, dc.confirmOwnership(context.Background()))
	require.False(t, dc.ownershipLost.Load())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDaemonConn_ConfirmOwnership_DifferentPod(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newSharedRegistry(db, "http://10.0.0.42:8091")
	dc := &daemonConn{id: "conn-1", shortID: "agent-A", owner: owner{userID: "alice", workspaceID: "W1"}, hub: &Hub{sharedReg: s}}

	rows := sqlmock.NewRows([]string{"owning_instance_url", "connection_id"}).
		AddRow("http://10.0.1.99:8091", "conn-other")
	mock.ExpectQuery(confirmOwnershipSQL).
		WithArgs("alice", "W1", "agent-A").
		WillReturnRows(rows)

	require.False(t, dc.confirmOwnership(context.Background()))
	require.True(t, dc.ownershipLost.Load(), "ownershipLost must be sticky-true")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDaemonConn_ConfirmOwnership_RowMissing(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newSharedRegistry(db, "http://10.0.0.42:8091")
	dc := &daemonConn{id: "conn-1", shortID: "agent-A", owner: owner{userID: "alice", workspaceID: "W1"}, hub: &Hub{sharedReg: s}}

	mock.ExpectQuery(confirmOwnershipSQL).
		WithArgs("alice", "W1", "agent-A").
		WillReturnRows(sqlmock.NewRows([]string{"owning_instance_url", "connection_id"}))

	require.False(t, dc.confirmOwnership(context.Background()))
	require.True(t, dc.ownershipLost.Load())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDaemonConn_ConfirmOwnership_StickyNegativeNoQuery(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newSharedRegistry(db, "http://10.0.0.42:8091")
	dc := &daemonConn{id: "conn-1", shortID: "agent-A", owner: owner{userID: "alice", workspaceID: "W1"}, hub: &Hub{sharedReg: s}}
	dc.ownershipLost.Store(true)

	// No mock.ExpectQuery — sticky negative cache must NOT touch PG.
	require.False(t, dc.confirmOwnership(context.Background()))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDaemonConn_ConfirmOwnership_PGError(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newSharedRegistry(db, "http://10.0.0.42:8091")
	dc := &daemonConn{id: "conn-1", shortID: "agent-A", owner: owner{userID: "alice", workspaceID: "W1"}, hub: &Hub{sharedReg: s}}

	mock.ExpectQuery(confirmOwnershipSQL).
		WithArgs("alice", "W1", "agent-A").
		WillReturnError(sql.ErrConnDone)

	require.False(t, dc.confirmOwnership(context.Background()))
	require.True(t, dc.ownershipLost.Load(), "PG error must be fail-closed (treat as lost)")
	require.NoError(t, mock.ExpectationsWereMet())
}

// Single-pod regression: confirmOwnership must NOT touch PG when sharedReg
// is nil. SendCommand[Stream] in single-pod mode calls confirmOwnership
// unconditionally (after the proxy.go refactor); without this early-return
// it would nil-deref.
func TestDaemonConn_ConfirmOwnership_SinglePodReturnsTrue(t *testing.T) {
	// Hub with no sharedReg (single-pod mode).
	dc := &daemonConn{id: "conn-1", shortID: "agent-A", owner: owner{userID: "alice", workspaceID: "W1"}, hub: &Hub{ /* sharedReg nil */ }}
	require.True(t, dc.confirmOwnership(context.Background()))
	require.False(t, dc.ownershipLost.Load(), "single-pod must not flip ownershipLost")

	// dc.hub == nil also safe.
	dc2 := &daemonConn{id: "conn-2", shortID: "agent-B", owner: owner{userID: "u", workspaceID: "w"}, hub: nil}
	require.True(t, dc2.confirmOwnership(context.Background()))
}
```

- [ ] **Step 3: Run; expect compile failure**

- [ ] **Step 4: Add `confirmOwnership` to registry.go**

Add to `internal/commanderhub/registry.go` (near the bottom):

```go
// confirmOwnership: pre-send check that this conn is still the cluster's
// authoritative owner. Sticky-negative cache: once ownershipLost is true,
// short-circuits all future calls without touching PG. Otherwise issues
// a 500ms-bounded SELECT against commander_daemons.
//
// Single-pod safe: when dc.hub == nil OR dc.hub.sharedReg == nil,
// returns true immediately (no cluster state to confirm against;
// callers MAY call this unconditionally without branching on
// sharedReg).
//
// On any deviation (different owning_instance_url, different
// connection_id, missing row, or PG error), sets ownershipLost=true
// and returns false. Fail-closed semantics.
//
// Called by SendCommand[Stream] before dc.writeEnvelope.
func (dc *daemonConn) confirmOwnership(ctx context.Context) bool {
	if dc.hub == nil || dc.hub.sharedReg == nil {
		return true
	}
	if dc.ownershipLost.Load() {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	var ownerURL, connID string
	row := dc.hub.sharedReg.db.QueryRowContext(cctx, confirmOwnershipSQL,
		dc.owner.userID, dc.owner.workspaceID, dc.shortID)
	if err := row.Scan(&ownerURL, &connID); err != nil ||
		ownerURL != dc.hub.sharedReg.advertiseURL ||
		connID != dc.id {
		dc.ownershipLost.Store(true)
		return false
	}
	return true
}
```

Add `"context"` import if missing.

- [ ] **Step 5: Run; expect pass**

```sh
go test ./internal/commanderhub -run TestDaemonConn_ConfirmOwnership -count=1 -race
```

- [ ] **Step 6: Commit**

```sh
git add internal/commanderhub/registry.go internal/commanderhub/registry_shared.go internal/commanderhub/registry_ownership_test.go
git commit -m "feat(commanderhub): daemonConn.confirmOwnership pre-send PG check

Per-send fresh ownership check against commander_daemons in shared mode.
Sticky-negative cache (atomic.Bool) avoids re-querying for the brief
remaining lifetime of a displaced conn. PG error or any deviation in
(owning_instance_url, connection_id) marks ownership lost (fail-closed),
so SendCommand[Stream] returns ErrDaemonGone instead of writing to a
stale WS that times out at TurnTimeout.

Costs +1 sub-ms PG SELECT per SendCommand in cluster mode. Eliminates
the v6/v7/v8 race window between sibling-claim and heartbeat-driven
force-close.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task B4: `ServeHTTP` admission gating (shared-mode requires successful upsert before local admit) + minimal `attachSharedRegistry`

**Files:**
- Modify: `multi-agent/internal/commanderhub/hub.go::ServeHTTP` (admission + teardown rewrite)
- Modify: `multi-agent/internal/commanderhub/hub.go::newDaemonID` (128-bit + error return)
- Modify: `multi-agent/internal/commanderhub/hub.go` (add minimal `attachSharedRegistry`; Phase D Task D1 expands it)
- Modify: existing tests if any assert specific newDaemonID behavior (grep)

**Minimal `attachSharedRegistry` for Phase B:**

Phase D Task D1 expands this method to also accept `forwardClient`, `turnStateBackend`, and disable `sessionCache`. For Phase B we only need the `sharedReg` field set so B4's tests can construct a Hub with cluster mode enabled. Add to `internal/commanderhub/hub.go` (after `NewHub`):

```go
// attachSharedRegistry plugs in the cluster-mode runtime. Phase B
// minimal version: only sets sharedReg. Phase D Task D1 extends to set
// forwardCli, turns, sessionCache.
//
// Callers must hold no Hub mutex (no Hub-wide lock today; fields are
// nilable-by-design and read by goroutines spawned after this returns).
func (h *Hub) attachSharedRegistry(sr *sharedRegistry) {
	h.sharedReg = sr
}
```

**Interfaces:**
- Produces:
  - `newDaemonID() (string, error)` — was `func() string` ignoring rand errors; now 16 bytes (128-bit) + propagates `crypto/rand` failure.
  - ServeHTTP admission order in shared mode: validate `RegisterPayload.ShortID` non-empty → `sharedReg.connectUpsert(3s ctx)` → on error refuse WS with `ErrCodeBackendUnavailable`; on success → `localReg.add(dc)` → start heartbeat goroutine.
  - ServeHTTP teardown defers (reverse-order): close `done`; `hbCancel + <-hbDone`; ownership-guarded `sharedReg.remove`; `localReg.removeIf(o, shortID, dc.id)`; `invalidateDaemonSessions`; `failAllPending`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/commanderhub/hub_test.go`:

```go
func TestNewDaemonID_128BitHexLength(t *testing.T) {
	id, err := newDaemonID()
	require.NoError(t, err)
	// 16 bytes hex-encoded = 32 chars (v5: was 8 bytes / 16 chars).
	require.Len(t, id, 32, "newDaemonID must return 32-char (128-bit) hex string")
}

func TestNewDaemonID_DistinctAcrossCalls(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		id, err := newDaemonID()
		require.NoError(t, err)
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate ID in 1000-call sample: %s", id)
		}
		seen[id] = struct{}{}
	}
}
```

For ServeHTTP admission gating, the test requires a working sharedRegistry. Use sqlmock to drive both connectUpsert and the WS dial path. Add to a new `hub_admission_test.go`:

```go
package commanderhub

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/internal/commander"
	"github.com/yourorg/multi-agent/internal/identity"
)

// fakeResolver is duplicated from wiring_test.go (same package); if you'd
// rather not duplicate, hoist it into a shared `*_test_helpers.go` file
// in this same task.

func TestServeHTTP_ClusterMode_RefusesWSOnUpsertFailure(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	hub := NewHub(&fakeResolver{mu: map[string]identity.Identity{"tok-alice": {UserID: "alice", WorkspaceID: "W1"}}})
	hub.attachSharedRegistry(newSharedRegistry(db, "http://10.0.0.42:8091"))

	mock.ExpectExec(connectUpsertSQL).
		WithArgs("alice", "W1", "agent-A", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), "http://10.0.0.42:8091").
		WillReturnError(errors.New("simulated PG unavailable"))

	srv := httptest.NewServer(hub)
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/daemon-link"
	hdr := map[string][]string{"Authorization": {"Bearer tok-alice"}}
	conn, _, err := websocket.DefaultDialer.Dial(url, hdr)
	require.NoError(t, err)
	defer conn.Close()

	// Send register payload with non-empty ShortID.
	rp := commander.RegisterPayload{SchemaVersion: commander.SchemaVersion, ShortID: "agent-A", DisplayName: "alice-mac", Kind: "claude"}
	payload, _ := json.Marshal(rp)
	require.NoError(t, conn.WriteJSON(commander.Envelope{Type: "register", Payload: payload}))

	// Expect an error envelope back (backend_unavailable), then close.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var env commander.Envelope
	require.NoError(t, conn.ReadJSON(&env))
	require.Equal(t, "error", env.Type)
	var ep commander.ErrorPayload
	require.NoError(t, json.Unmarshal(env.Payload, &ep))
	require.Equal(t, commander.ErrCodeBackendUnavailable, ep.Code)

	require.NoError(t, mock.ExpectationsWereMet())
	require.Empty(t, hub.reg.daemons(owner{userID: "alice", workspaceID: "W1"}), "must not admit to localReg on failed upsert")
}

func TestServeHTTP_ClusterMode_RequiresShortID(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	hub := NewHub(&fakeResolver{mu: map[string]identity.Identity{"tok-alice": {UserID: "alice", WorkspaceID: "W1"}}})
	hub.attachSharedRegistry(newSharedRegistry(db, "http://10.0.0.42:8091"))

	srv := httptest.NewServer(hub)
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/daemon-link"
	hdr := map[string][]string{"Authorization": {"Bearer tok-alice"}}
	conn, _, err := websocket.DefaultDialer.Dial(url, hdr)
	require.NoError(t, err)
	defer conn.Close()

	rp := commander.RegisterPayload{SchemaVersion: commander.SchemaVersion} // ShortID empty
	payload, _ := json.Marshal(rp)
	require.NoError(t, conn.WriteJSON(commander.Envelope{Type: "register", Payload: payload}))

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var env commander.Envelope
	require.NoError(t, conn.ReadJSON(&env))
	require.Equal(t, "error", env.Type)
	var ep commander.ErrorPayload
	require.NoError(t, json.Unmarshal(env.Payload, &ep))
	require.Equal(t, commander.ErrCodeInvalidRequest, ep.Code)
}
```

(The `fakeResolver` type already exists in `wiring_test.go`; if not, copy the pattern from there.)

- [ ] **Step 2: Run; expect compile failure**

- [ ] **Step 3: Rewrite `newDaemonID` (128-bit + error)**

In `internal/commanderhub/hub.go`, find:

```go
func newDaemonID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
```

Replace with:

```go
// newDaemonID returns 128-bit hex random as the per-connection daemon_id.
// Returns error so caller can refuse WS admission on entropy starvation.
func newDaemonID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("newDaemonID: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
```

Add `"fmt"` to imports if missing.

- [ ] **Step 4: Update `ServeHTTP` admission + teardown**

Add `"context"` and `"log"` to `hub.go` imports (verify with `grep '"log"' internal/commanderhub/hub.go` — if absent, add).

Find the existing admission/teardown block in `hub.go::ServeHTTP` (around lines 79-141). The current shape (paraphrased):

```go
dc := &daemonConn{ id: newDaemonID(), owner: o, conn: conn, ... }
// reads register frame; sets dc.shortID etc.
h.reg.add(dc)
defer h.reg.remove(o, dc.id)
defer h.invalidateDaemonSessions(o, dc.id)
defer close(dc.done)
defer dc.failAllPending()
// ack + readLoop
```

Replace with (interleaved comments mark the v5/v15 changes — read the spec §"Daemon admission + teardown ordering"):

```go
dcID, err := newDaemonID()
if err != nil {
	log.Printf("commanderhub: newDaemonID failed: %v", err)
	conn.Close()
	return
}
dc := &daemonConn{
	id:      dcID,
	owner:   o,
	conn:    conn,
	pending: make(map[string]*pendingEntry),
	done:    make(chan struct{}),
	hub:     h,
}

// First frame must be register; validate schema before admitting.
reg, err := readFrame(conn)
if err != nil {
	conn.Close()
	return
}
if reg.Type != "register" {
	conn.Close()
	return
}
var rp commander.RegisterPayload
if err := json.Unmarshal(reg.Payload, &rp); err != nil {
	conn.Close()
	return
}
if rp.SchemaVersion != commander.SchemaVersion {
	_ = dc.writeEnvelope(errorEnvelope("", commander.ErrCodeSchemaVersionMismatch, "schema version mismatch"))
	dc.writeMu.Lock()
	_ = conn.WriteControl(websocket.CloseMessage, nil, time.Now().Add(wsWriteWait))
	dc.writeMu.Unlock()
	conn.Close()
	return
}

// Shared-mode requires non-empty ShortID — the registry PK depends on it,
// and reconnecting clients without a stable short_id would each create a
// new row instead of taking over.
if h.sharedReg != nil && strings.TrimSpace(rp.ShortID) == "" {
	_ = dc.writeEnvelope(errorEnvelope("", commander.ErrCodeInvalidRequest, "short_id is required when observer is in cluster mode"))
	conn.Close()
	return
}

dc.shortID = rp.ShortID
dc.displayName = rp.DisplayName
dc.kind = rp.Kind
dc.driverVersion = rp.DriverVersion
capabilities := map[string]bool{
	commander.CapabilitySessions: true,
	commander.CapabilityTurn:     true,
}
for _, capability := range rp.Capabilities {
	capability = strings.TrimSpace(capability)
	if capability != "" {
		capabilities[capability] = true
	}
}
dc.metaMu.Lock()
dc.capabilities = capabilities
dc.lastSeenAt = time.Now().UTC()
dc.metaMu.Unlock()

// SHARED MODE admission: write DB row BEFORE local admit. On failure,
// refuse the WS — a locally-admitted-but-cluster-invisible daemon is
// worse than a refused reconnect (split brain). Daemon wsclient will
// retry within seconds.
hbCtx, hbCancel := context.WithCancel(context.Background())
hbDone := make(chan struct{})
if h.sharedReg != nil {
	upsertCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	err := h.sharedReg.connectUpsert(upsertCtx, dc)
	cancel()
	if err != nil {
		log.Printf("commanderhub: shared registry connectUpsert failed (refusing WS to avoid split-brain): %v", err)
		_ = dc.writeEnvelope(errorEnvelope("", commander.ErrCodeBackendUnavailable, "observer registry unavailable"))
		conn.Close()
		hbCancel()  // never started; safe to cancel
		close(hbDone)
		return
	}
	go func() {
		defer close(hbDone)
		h.sharedReg.runHeartbeat(hbCtx, dc)
	}()
} else {
	close(hbDone) // single-pod: nothing to wait on
}

// Only after shared-registry row is durable do we admit locally.
h.reg.add(dc)

// Local registry / cache teardown uses routingID() — matches the key
// localReg.add used in cluster (= shortID) AND in single-pod legacy (=
// dc.id when ShortID empty). Shared-registry teardown below uses raw
// dc.shortID because cluster mode requires non-empty short_id (refused
// at admission above) and the PG row's PK is short_id, never dc.id.
routingID := dc.routingID()
defer h.reg.removeIf(o, routingID, dc.id)
defer h.invalidateDaemonSessions(o, routingID)
defer close(dc.done)
defer dc.failAllPending()
defer func() {
	if h.sharedReg != nil {
		hbCancel()
		<-hbDone // wait for heartbeat goroutine to exit
		removeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = h.sharedReg.remove(removeCtx, o, dc.shortID, dc.id)
		cancel()
	}
}()

// Ack: PR-2 WSClient only flips linked=true on receipt.
if err := dc.writeEnvelope(commander.Envelope{Type: "ack"}); err != nil {
	return
}

dc.readLoop()
```

Note the order:
1. Generate dc.id (may fail).
2. Read register frame; validate schema; require ShortID in shared mode.
3. Populate dc metadata.
4. **Shared-mode upsert** under 3s ctx; refuse WS on failure.
5. Start heartbeat goroutine.
6. `localReg.add`.
7. defer chain (LIFO order: failAllPending → close(done) → invalidate → removeIf → heartbeat-stop+remove).

- [ ] **Step 5: Update callers of `newDaemonID()`**

```sh
grep -nE 'newDaemonID\(' internal/commanderhub
```

The only caller is `hub.go::ServeHTTP` (already updated). Tests that call `newDaemonID` directly need to handle the new error return; grep `*_test.go` and fix.

- [ ] **Step 6: Run; expect pass**

```sh
go test ./internal/commanderhub -count=1 -race
```

- [ ] **Step 7: Commit**

```sh
git add internal/commanderhub/hub.go internal/commanderhub/hub_test.go internal/commanderhub/hub_admission_test.go internal/commanderhub/*_test.go
git commit -m "feat(commanderhub): ServeHTTP shared-mode admission gating + 128-bit dc.id

newDaemonID returns (string, error) and uses 16 random bytes (was 8).
ServeHTTP refuses WS admission if shared-mode connectUpsert fails (3s
ctx) — locally-admitted-but-cluster-invisible daemons create split
brain that's worse than a refused reconnect. Heartbeat goroutine starts
after upsert, exits on hbCancel; deferred sharedReg.remove waits for
hbDone before running (ownership-guarded DELETE, safe). Shared mode
also requires non-empty RegisterPayload.ShortID (registry PK column).

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task B5: Sweep goroutine (`commander_daemons` + `commander_forward_nonces` + `commander_telemetry_buckets`)

**Files:**
- Modify: `multi-agent/internal/commanderhub/registry_shared.go` (add `sweep`, `sweepNonces`, `sweepTelemetryBuckets`, `runSweep`)
- Modify: `multi-agent/internal/commanderhub/registry_shared_test.go` (add tests)

**Interfaces:**
- Produces:
  - `(s *sharedRegistry).sweep(ctx) error` — `DELETE FROM commander_daemons WHERE last_seen_at < now() - 5min`.
  - `(s *sharedRegistry).sweepNonces(ctx) error` — `DELETE FROM commander_forward_nonces WHERE received_at < now() - 120s`.
  - `(s *sharedRegistry).sweepTelemetryBuckets(ctx) error` — `DELETE FROM commander_telemetry_buckets WHERE updated_at < now() - 1h`.
  - `(s *sharedRegistry).runSweep(ctx)` — ticks every `sweepEvery` (30s); runs all three sweeps each tick; logs errors rate-limited.

Note: `deleteAfter` (5min) is deliberately MUCH longer than `onlineTTL` (45s). A 60s PG hiccup on the owning pod makes daemons briefly invisible (readers filter by `onlineTTL`) but NOT deleted; recovery resumes via next heartbeat. See spec §"Honest race window" + spec §"Wire sizing".

- [ ] **Step 1: Write the failing tests**

Append to `registry_shared_test.go`:

```go
func TestSharedRegistry_SweepDeletesOldDaemons(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newSharedRegistry(db, "http://10.0.0.42:8091")
	mock.ExpectExec(sweepDaemonsSQL).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 3))

	require.NoError(t, s.sweep(context.Background()))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSharedRegistry_RunSweepOnceRunsAllThree(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	s := newSharedRegistry(db, "http://10.0.0.42:8091")
	mock.MatchExpectationsInOrder(false)
	mock.ExpectExec(sweepDaemonsSQL).WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(sweepNoncesSQL).WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(sweepTelemetryBucketsSQL).WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 0))

	// runSweepOnce runs one cycle of all three sweeps without any timer
	// dependency — tests assert SQL was issued without race-sensitive
	// sleeps against the ticker.
	s.runSweepOnce(context.Background())

	require.NoError(t, mock.ExpectationsWereMet())
}
```

- [ ] **Step 2: Run; expect compile failure**

- [ ] **Step 3: Add sweep methods + runSweep**

Append to `registry_shared.go`:

```go
const defaultTelemetryBucketIdleTTL = time.Hour

func (s *sharedRegistry) sweep(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, sweepDaemonsSQL, time.Now().Add(-s.deleteAfter))
	return err
}

func (s *sharedRegistry) sweepNonces(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, sweepNoncesSQL, time.Now().Add(-s.nonceTTL))
	return err
}

func (s *sharedRegistry) sweepTelemetryBuckets(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, sweepTelemetryBucketsSQL, time.Now().Add(-defaultTelemetryBucketIdleTTL))
	return err
}

// runSweepOnce executes one cycle of all three sweeps. Exposed as a
// method so tests can call it directly without timer races.
func (s *sharedRegistry) runSweepOnce(ctx context.Context) {
	swCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := s.sweep(swCtx); err != nil {
		log.Printf("commanderhub: sweep commander_daemons err=%v", err)
	}
	if err := s.sweepNonces(swCtx); err != nil {
		log.Printf("commanderhub: sweep commander_forward_nonces err=%v", err)
	}
	if err := s.sweepTelemetryBuckets(swCtx); err != nil {
		log.Printf("commanderhub: sweep commander_telemetry_buckets err=%v", err)
	}
}

// runSweep ticks every s.sweepEvery and calls runSweepOnce. Exits on
// ctx cancel.
func (s *sharedRegistry) runSweep(ctx context.Context) {
	t := time.NewTicker(s.sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.runSweepOnce(ctx)
	}
}
```

- [ ] **Step 4: Run; expect pass**

- [ ] **Step 5: Commit**

```sh
git add internal/commanderhub/registry_shared.go internal/commanderhub/registry_shared_test.go
git commit -m "feat(commanderhub): per-pod sweep goroutine for daemons + nonces + telemetry buckets

sweep deletes commander_daemons rows older than deleteAfter (5min);
NOTE deleteAfter is much longer than onlineTTL (45s) so a transient PG
outage on the owning pod doesn't let a peer's sweep delete the row.
sweepNonces purges commander_forward_nonces older than nonceTTL (120s,
2× HMAC timestamp window). sweepTelemetryBuckets purges idle buckets
(1h). runSweep ticks every sweepEvery (30s) and runs all three.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Phase B Gate

```sh
cd multi-agent
go vet ./...
go test ./internal/commanderhub -count=1 -race
```

All Phase A + Phase B tests pass. `hub.reg.add(...)` callers still compile. `sharedRegistry` SQL shape is locked by `sqlmock.QueryMatcherEqual`.

**Dispatch to codex for Phase B review** before starting Phase C.

---

## Phase C — Forwarding + drain + cmdID (6 tasks)

Adds the pod-to-pod HTTP forwarding layer. C1–C6 are partially sequential: C1 (codec) + C2 (HMAC auth + nonce table writes) are independent and can run in parallel. C3 (client) depends on C1+C2. C4 (server) depends on C1+C2+C3. C5 (drain) depends on C2. C6 (cmdID pod prefix) is independent.

### Task C1: length-prefixed JSON envelope codec (1 MiB cap)

**Files:**
- Create: `multi-agent/internal/commanderhub/forward_codec.go`
- Create: `multi-agent/internal/commanderhub/forward_codec_test.go`

**Interfaces:**
- Produces:
  - `forwardFrameMaxBytes int64 = 1 << 20` (1 MiB; matches existing `wsReadLimit`).
  - `writeEnvelopeFrame(w io.Writer, env commander.Envelope) error` — emits `<decimal-ascii-length>\n<envelope-json-bytes>`. Returns error on write failure OR if encoded JSON exceeds cap.
  - `readEnvelopeFrame(r *bufio.Reader) (commander.Envelope, error)` — reads ASCII digits until `\n` (max 7 digits to encode 1 MiB), parses the length, reads exactly that many bytes, JSON-decodes. Returns `io.EOF` at stream end. Returns error on cap overflow OR malformed framing.

- [ ] **Step 1: Write failing tests**

Create `internal/commanderhub/forward_codec_test.go`:

```go
package commanderhub

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/internal/commander"
)

func TestForwardCodec_RoundTrip(t *testing.T) {
	envs := []commander.Envelope{
		{Type: "ack"},
		{Type: "command", ID: "1", Payload: json.RawMessage(`{"command":"list_sessions"}`)},
		{Type: "event", ID: "1", Payload: json.RawMessage(`{"event_kind":"chunk","text":"hello"}`)},
	}

	var buf bytes.Buffer
	for _, e := range envs {
		require.NoError(t, writeEnvelopeFrame(&buf, e))
	}
	r := bufio.NewReader(&buf)
	for i, want := range envs {
		got, err := readEnvelopeFrame(r)
		require.NoError(t, err, "frame %d", i)
		require.Equal(t, want.Type, got.Type)
		require.Equal(t, want.ID, got.ID)
	}
	_, err := readEnvelopeFrame(r)
	require.ErrorIs(t, err, io.EOF, "expected EOF after last frame")
}

func TestForwardCodec_RejectsOverflowOnWrite(t *testing.T) {
	// 2 MiB of "x" — exceeds the 1 MiB cap.
	huge := commander.Envelope{Type: "event", Payload: json.RawMessage(`"` + strings.Repeat("x", 2*1024*1024) + `"`)}
	err := writeEnvelopeFrame(io.Discard, huge)
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds cap")
}

func TestForwardCodec_RejectsOverflowOnRead(t *testing.T) {
	// Claim 5 MiB length but only deliver a few bytes. Reader must reject
	// the length without ever allocating 5 MiB.
	buf := bytes.NewBufferString("5242881\nxx") // 1 MiB + 1
	r := bufio.NewReader(buf)
	_, err := readEnvelopeFrame(r)
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds cap")
}

func TestForwardCodec_RejectsMalformedLength(t *testing.T) {
	// Non-digit prefix.
	r := bufio.NewReader(bytes.NewBufferString("abc\n{}"))
	_, err := readEnvelopeFrame(r)
	require.Error(t, err)
}

func TestForwardCodec_RejectsTooManyDigits(t *testing.T) {
	// 8 digits → must be rejected before being parsed (cap is 1 MiB = 7 digits max).
	r := bufio.NewReader(bytes.NewBufferString("10000000\n{}"))
	_, err := readEnvelopeFrame(r)
	require.Error(t, err)
}
```

- [ ] **Step 2: Run; expect compile failure**

- [ ] **Step 3: Implement the codec**

Create `internal/commanderhub/forward_codec.go`:

```go
package commanderhub

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/yourorg/multi-agent/internal/commander"
)

// forwardFrameMaxBytes caps each length-prefixed envelope. Matches the
// existing observer wsReadLimit (1 MiB) so a single envelope can carry
// at most what the WS read loop already accepts. Daemon-side ReadFile
// (commander/files.go) enforces a 768 KiB JSON-encoded cap so this
// boundary is never approached in practice; the wire cap is a safety
// net against pathological or malicious frames.
const forwardFrameMaxBytes = 1 << 20

// forwardFrameMaxDigits: 1<<20 = 1048576 → 7 decimal digits. Reader
// rejects more, so a forged length cannot be parsed into a giant int.
const forwardFrameMaxDigits = 7

var errEnvelopeOversized = errors.New("forward: envelope exceeds cap of 1 MiB")

// writeEnvelopeFrame marshals env to JSON and writes `<len>\n<json>`.
// Returns errEnvelopeOversized when the encoded JSON exceeds the cap.
func writeEnvelopeFrame(w io.Writer, env commander.Envelope) error {
	body, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("forward: marshal envelope: %w", err)
	}
	if int64(len(body)) > forwardFrameMaxBytes {
		return fmt.Errorf("%w (was %d bytes)", errEnvelopeOversized, len(body))
	}
	if _, err := fmt.Fprintf(w, "%d\n", len(body)); err != nil {
		return fmt.Errorf("forward: write length prefix: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("forward: write envelope body: %w", err)
	}
	return nil
}

// readEnvelopeFrame parses one length-prefixed envelope from r. Returns
// io.EOF at clean end of stream. Returns errEnvelopeOversized on a
// claimed length > cap; returns descriptive error on malformed framing.
func readEnvelopeFrame(r *bufio.Reader) (commander.Envelope, error) {
	lineBytes, err := r.ReadSlice('\n')
	if err != nil {
		// io.EOF here is the clean end-of-stream signal.
		if errors.Is(err, io.EOF) && len(lineBytes) == 0 {
			return commander.Envelope{}, io.EOF
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			return commander.Envelope{}, fmt.Errorf("forward: length prefix > %d digits", forwardFrameMaxDigits)
		}
		return commander.Envelope{}, fmt.Errorf("forward: read length prefix: %w", err)
	}
	// Strip trailing '\n'.
	line := lineBytes[:len(lineBytes)-1]
	if len(line) == 0 || len(line) > forwardFrameMaxDigits {
		return commander.Envelope{}, fmt.Errorf("forward: invalid length prefix (%q)", lineBytes)
	}
	for _, c := range line {
		if c < '0' || c > '9' {
			return commander.Envelope{}, fmt.Errorf("forward: non-digit in length prefix (%q)", line)
		}
	}
	n, err := strconv.ParseInt(string(line), 10, 64)
	if err != nil {
		return commander.Envelope{}, fmt.Errorf("forward: parse length: %w", err)
	}
	if n < 0 || n > forwardFrameMaxBytes {
		return commander.Envelope{}, fmt.Errorf("%w (was %d bytes)", errEnvelopeOversized, n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return commander.Envelope{}, fmt.Errorf("forward: read body (%d bytes): %w", n, err)
	}
	var env commander.Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return commander.Envelope{}, fmt.Errorf("forward: unmarshal envelope: %w", err)
	}
	return env, nil
}
```

- [ ] **Step 4: Run; expect pass**

```sh
go test ./internal/commanderhub -run TestForwardCodec_ -count=1 -race
```

- [ ] **Step 5: Commit**

```sh
git add internal/commanderhub/forward_codec.go internal/commanderhub/forward_codec_test.go
git commit -m "feat(commanderhub): length-prefixed JSON envelope codec for pod-to-pod forwarding

Wire format: <decimal-ascii-length>\\n<envelope-json-bytes>. 1 MiB cap
per envelope matches existing observer wsReadLimit; daemon-side
ReadFile's 768 KiB JSON-encoded cap (Task A2) keeps frames well under
this. Reader rejects malformed lengths and lengths exceeding cap WITHOUT
ever allocating the buffer (digit count check before parse).

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task C2: HMAC auth + `commander_forward_nonces` write side

**Files:**
- Create: `multi-agent/internal/commanderhub/forward_auth.go`
- Create: `multi-agent/internal/commanderhub/forward_auth_test.go`

**Interfaces:**
- Produces:
  - `forwardHMACTimestampWindow time.Duration = 60 * time.Second`
  - `signForward(secret []byte, ts int64, nonce string, body []byte) string` — returns hex SHA-256 HMAC of `ts || "\n" || nonce || "\n" || body`. Used by client.
  - `verifyForward(headerHex string, secret, prevSecret []byte, ts int64, nonce string, body []byte) (matchedKey int, ok bool)` — returns `matchedKey: 0` for Secret, `1` for PrevSecret, `-1` for no match. Uses `hmac.Equal` over fixed `[32]byte` arrays to avoid timing side channels.
  - `parseHMACTimestamp(headerVal string) (int64, error)` + `parseHMACNonce(headerVal string) error` — header parsing helpers (32 hex chars for nonce, decimal seconds for timestamp).
  - `freshNonce() (string, error)` — 32 random hex chars via `crypto/rand`.
  - `insertNonce(ctx context.Context, db *sql.DB, nonce string) (inserted bool, err error)` — `INSERT INTO commander_forward_nonces (nonce, received_at) VALUES ($1, now()) ON CONFLICT DO NOTHING RETURNING true`; `inserted=false` ⇒ replay. PG error ⇒ caller fails closed (503).

- [ ] **Step 1: Write the failing tests**

Create `internal/commanderhub/forward_auth_test.go`:

```go
package commanderhub

import (
	"context"
	"encoding/hex"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

const insertNonceSQL = `INSERT INTO commander_forward_nonces (nonce, received_at) VALUES ($1, now()) ON CONFLICT DO NOTHING`

func TestSignForward_Deterministic(t *testing.T) {
	secret := []byte("supersecret-32-chars-padding-aaaa")
	sig1 := signForward(secret, 1751155200, "0123456789abcdef0123456789abcdef", []byte(`{"x":1}`))
	sig2 := signForward(secret, 1751155200, "0123456789abcdef0123456789abcdef", []byte(`{"x":1}`))
	require.Equal(t, sig1, sig2)
	require.Len(t, sig1, 64) // SHA-256 hex
	_, err := hex.DecodeString(sig1)
	require.NoError(t, err)
}

func TestVerifyForward_AcceptsCurrentSecret(t *testing.T) {
	secret := []byte("supersecret-32-chars-padding-aaaa")
	body := []byte(`{"x":1}`)
	ts := int64(1751155200)
	nonce := "0123456789abcdef0123456789abcdef"
	sig := signForward(secret, ts, nonce, body)
	matched, ok := verifyForward(sig, secret, nil, ts, nonce, body)
	require.True(t, ok)
	require.Equal(t, 0, matched)
}

func TestVerifyForward_AcceptsPrevSecret(t *testing.T) {
	oldSecret := []byte("OLD-secret-32-chars-padding-bbbbb")
	newSecret := []byte("NEW-secret-32-chars-padding-ccccc")
	body := []byte(`{"x":1}`)
	ts := int64(1751155200)
	nonce := "0123456789abcdef0123456789abcdef"
	sig := signForward(oldSecret, ts, nonce, body)
	matched, ok := verifyForward(sig, newSecret, oldSecret, ts, nonce, body)
	require.True(t, ok)
	require.Equal(t, 1, matched)
}

func TestVerifyForward_RejectsWrongSecret(t *testing.T) {
	secret := []byte("a-secret-32-chars-padding-dddddd")
	otherSecret := []byte("ANOTHER-32-chars-padding-eeeeee")
	body := []byte(`{"x":1}`)
	ts := int64(1751155200)
	nonce := "0123456789abcdef0123456789abcdef"
	sig := signForward(otherSecret, ts, nonce, body)
	_, ok := verifyForward(sig, secret, nil, ts, nonce, body)
	require.False(t, ok)
}

func TestVerifyForward_RejectsMalformedAuthHeader(t *testing.T) {
	secret := []byte("supersecret-32-chars-padding-aaaa")
	body := []byte(`{"x":1}`)
	ts := int64(1751155200)
	nonce := "0123456789abcdef0123456789abcdef"
	// Wrong length (32 chars instead of 64): early reject.
	_, ok := verifyForward("00000000000000000000000000000000", secret, nil, ts, nonce, body)
	require.False(t, ok)
	// Non-hex characters: hex.Decode fails, reject.
	_, ok = verifyForward("zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz", secret, nil, ts, nonce, body)
	require.False(t, ok)
	// Empty: early reject.
	_, ok = verifyForward("", secret, nil, ts, nonce, body)
	require.False(t, ok)
}

func TestFreshNonce_HexAndUnique(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		n, err := freshNonce()
		require.NoError(t, err)
		require.Len(t, n, 32)
		_, err = hex.DecodeString(n)
		require.NoError(t, err)
		if _, dup := seen[n]; dup {
			t.Fatalf("duplicate nonce: %s", n)
		}
		seen[n] = struct{}{}
	}
}

func TestInsertNonce_FirstAccepted(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectExec(insertNonceSQL).
		WithArgs("nonce-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	inserted, err := insertNonce(context.Background(), db, "nonce-1")
	require.NoError(t, err)
	require.True(t, inserted)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestInsertNonce_ConflictRejected(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectExec(insertNonceSQL).
		WithArgs("nonce-replay").
		WillReturnResult(sqlmock.NewResult(0, 0))
	inserted, err := insertNonce(context.Background(), db, "nonce-replay")
	require.NoError(t, err)
	require.False(t, inserted, "ON CONFLICT DO NOTHING → 0 rows → replay detected")
	require.NoError(t, mock.ExpectationsWereMet())
}
```

- [ ] **Step 2: Run; expect compile failure**

- [ ] **Step 3: Implement `forward_auth.go`**

Create `internal/commanderhub/forward_auth.go`:

```go
package commanderhub

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"
)

const (
	forwardHMACTimestampWindow = 60 * time.Second
	forwardNonceHexLen         = 32 // 16 random bytes
)

// signForward returns the hex-encoded SHA-256 HMAC of
// (timestamp || "\n" || nonce || "\n" || body) under `secret`. Used by
// the client to compute X-Observer-Cluster-Auth.
func signForward(secret []byte, ts int64, nonce string, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	fmt.Fprintf(mac, "%d\n%s\n", ts, nonce)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// verifyForward checks the hex auth header against Secret and (if non-
// nil) PrevSecret in constant time. Returns:
//   matchedKey = 0 → Secret matched
//   matchedKey = 1 → PrevSecret matched (during three-phase rotation)
//   matchedKey = -1 → neither matched
//
// Implementation note: hex-decode the header into a fixed
// [sha256.Size]byte array; compute expected MACs into the same
// fixed-size arrays; compare via hmac.Equal which uses
// subtle.ConstantTimeCompare. Doing the comparison on fixed-size
// arrays guarantees no length-based early exit can leak — the only
// public observation is `ok` and the (constant-time) comparison
// outcome. A malformed-length header is rejected before any comparison.
func verifyForward(headerHex string, secret, prevSecret []byte, ts int64, nonce string, body []byte) (matchedKey int, ok bool) {
	if len(headerHex) != 2*sha256.Size {
		return -1, false
	}
	var got [sha256.Size]byte
	if _, err := hex.Decode(got[:], []byte(headerHex)); err != nil {
		return -1, false
	}
	var want [sha256.Size]byte
	// Current secret.
	wantHex0 := signForward(secret, ts, nonce, body)
	if _, err := hex.Decode(want[:], []byte(wantHex0)); err == nil && hmac.Equal(got[:], want[:]) {
		return 0, true
	}
	// Previous secret (rotation window).
	if prevSecret != nil {
		var want1 [sha256.Size]byte
		wantHex1 := signForward(prevSecret, ts, nonce, body)
		if _, err := hex.Decode(want1[:], []byte(wantHex1)); err == nil && hmac.Equal(got[:], want1[:]) {
			return 1, true
		}
	}
	return -1, false
}

// parseHMACTimestamp parses the X-Observer-Cluster-Timestamp header.
func parseHMACTimestamp(s string) (int64, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid timestamp: %w", err)
	}
	return n, nil
}

// parseHMACNonce validates the X-Observer-Cluster-Nonce header. Returns
// nil if it's 32 hex chars (the freshNonce format).
func parseHMACNonce(s string) error {
	if len(s) != forwardNonceHexLen {
		return fmt.Errorf("invalid nonce length: want %d, got %d", forwardNonceHexLen, len(s))
	}
	if _, err := hex.DecodeString(s); err != nil {
		return fmt.Errorf("invalid nonce: %w", err)
	}
	return nil
}

// freshNonce returns a fresh 32-hex-char nonce (16 random bytes). Returns
// error on crypto/rand failure (system entropy starvation; unrecoverable).
func freshNonce() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("freshNonce: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// insertNonce atomically inserts the nonce. Returns inserted=false when
// the nonce already exists (replay). PG errors bubble up; caller MUST
// fail closed (503) on err.
func insertNonce(ctx context.Context, db *sql.DB, nonce string) (bool, error) {
	res, err := db.ExecContext(ctx, `INSERT INTO commander_forward_nonces (nonce, received_at) VALUES ($1, now()) ON CONFLICT DO NOTHING`, nonce)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// timestampWithinWindow returns true when |now - ts| <= window.
func timestampWithinWindow(ts int64, now time.Time, window time.Duration) bool {
	diff := now.Unix() - ts
	if diff < 0 {
		diff = -diff
	}
	return time.Duration(diff)*time.Second <= window
}

// ErrForwardAuthDenied is returned to callers in lieu of leaking which
// step (timestamp, nonce, HMAC) failed. Audit log gets the detail.
var ErrForwardAuthDenied = errors.New("forward: authentication denied")
```

- [ ] **Step 4: Run; expect pass**

```sh
go test ./internal/commanderhub -run 'TestSignForward|TestVerifyForward|TestFreshNonce|TestInsertNonce' -count=1 -race
```

- [ ] **Step 5: Commit**

```sh
git add internal/commanderhub/forward_auth.go internal/commanderhub/forward_auth_test.go
git commit -m "feat(commanderhub): HMAC + nonce auth helpers for pod-to-pod forwarding

signForward computes SHA-256 HMAC over (ts || nonce || body) per spec
v19. verifyForward accepts Secret OR PrevSecret (three-phase rotation)
via constant-time hmac.Equal on fixed [32]byte arrays. freshNonce
returns 32 hex chars from crypto/rand and propagates entropy errors.
insertNonce atomically commits the nonce; ON CONFLICT DO NOTHING with
0 affected rows signals replay.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task C3: `*forwardClient` — pod-to-pod HTTP forwarding

**Files:**
- Create: `multi-agent/internal/commanderhub/forward_client.go`
- Create: `multi-agent/internal/commanderhub/forward_client_test.go`
- Modify: `multi-agent/internal/commanderhub/hub.go` (ADD `forwardCli *forwardClient` field to Hub struct now that the type exists)

**Interfaces:**
- Produces:
  - `*forwardClient`: `secret`, `prevSecret`, `httpClient *http.Client`, `audit *log.Logger` (uses stdlib log to stderr).
  - `(c *forwardClient).send(ctx, peerURL string, req forwardRequest) (json.RawMessage, error)` — non-streaming. Marshals request body, signs HMAC, POSTs to `peerURL + forwardEndpoint` (the path const is in production `forward_endpoint.go`). On 403 with PrevSecret-available, retries ONCE with PrevSecret. On 426 (daemon upgrade) → returns `&DaemonError{Code: commander.ErrCodeDaemonUpgradeRequired}` (package-local type `commanderhub.DaemonError`, defined in existing `proxy.go:21`). On 404 → returns `ErrDaemonNotFound`. On other → returns `ErrDaemonGone`.
  - `(c *forwardClient).stream(ctx, peerURL string, req forwardRequest) (<-chan commander.Envelope, error)` — streaming. Returns a channel that decodes length-prefixed envelopes from the chunked HTTP response.
  - `type forwardRequest struct { Owner owner; ShortID string; Command string; Args json.RawMessage; Streaming bool; TimeoutMs int64 }`.

This task is sizable; the test set covers signing OK, retry-on-403, body cap, 426 mapping, 404 mapping, streaming round-trip, and stream cancel. See the spec §"Forwarding endpoint — Auth" / "Response — non-streaming" / "Response — streaming" / "Cancellation propagation" for exact wire shape.

- [ ] **Step 1: Write the failing tests**

Create `internal/commanderhub/forward_client_test.go`. Use `httptest.NewServer` to stand up a fake peer that validates HMAC and responds. (Full test code: ~250 lines — see structure below; expand each block to concrete assertions.)

```go
package commanderhub

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/internal/commander"
)

// forwardEndpoint is defined in production forward_client.go below.

func TestForwardClient_Send_RoundTrip(t *testing.T) {
	secret := []byte("supersecret-32-chars-padding-aaaa")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, forwardEndpoint, r.URL.Path)
		body, _ := io.ReadAll(r.Body)
		ts, err := parseHMACTimestamp(r.Header.Get("X-Observer-Cluster-Timestamp"))
		require.NoError(t, err)
		require.NoError(t, parseHMACNonce(r.Header.Get("X-Observer-Cluster-Nonce")))
		_, ok := verifyForward(r.Header.Get("X-Observer-Cluster-Auth"), secret, nil, ts, r.Header.Get("X-Observer-Cluster-Nonce"), body)
		require.True(t, ok)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"result":{"sessions":[]}}`)
	}))
	defer srv.Close()

	c := newForwardClient(secret, nil)
	res, err := c.send(context.Background(), srv.URL, forwardRequest{
		Owner: owner{userID: "alice", workspaceID: "W1"}, ShortID: "agent-A",
		Command: "list_sessions",
	})
	require.NoError(t, err)
	require.Contains(t, string(res), "sessions")
}

func TestForwardClient_Send_RetryOnPrevSecret(t *testing.T) {
	oldSecret := []byte("OLD-secret-32-chars-padding-bbbbb")
	newSecret := []byte("NEW-secret-32-chars-padding-ccccc")
	var attempt int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		body, _ := io.ReadAll(r.Body)
		ts, _ := parseHMACTimestamp(r.Header.Get("X-Observer-Cluster-Timestamp"))
		nonce := r.Header.Get("X-Observer-Cluster-Nonce")
		_, ok := verifyForward(r.Header.Get("X-Observer-Cluster-Auth"), oldSecret, nil, ts, nonce, body)
		if !ok {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		_, _ = fmt.Fprint(w, `{"result":{}}`)
	}))
	defer srv.Close()

	// Sender's PrevSecret = oldSecret; receiver accepts old only.
	c := newForwardClient(newSecret, oldSecret)
	_, err := c.send(context.Background(), srv.URL, forwardRequest{
		Owner: owner{userID: "alice", workspaceID: "W1"}, ShortID: "agent-A",
		Command: "list_sessions",
	})
	require.NoError(t, err)
	require.Equal(t, 2, attempt, "should have retried once with PrevSecret")
}

func TestForwardClient_Send_404_MapsToErrDaemonNotFound(t *testing.T) {
	secret := []byte("supersecret-32-chars-padding-aaaa")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()
	c := newForwardClient(secret, nil)
	_, err := c.send(context.Background(), srv.URL, forwardRequest{
		Owner: owner{userID: "alice", workspaceID: "W1"}, ShortID: "ghost",
		Command: "list_sessions",
	})
	require.ErrorIs(t, err, ErrDaemonNotFound)
}

func TestForwardClient_Send_426_MapsToDaemonUpgradeRequired(t *testing.T) {
	secret := []byte("supersecret-32-chars-padding-aaaa")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUpgradeRequired)
		_, _ = fmt.Fprint(w, `{"error":{"code":"daemon_upgrade_required","message":"upgrade your daemon"}}`)
	}))
	defer srv.Close()
	c := newForwardClient(secret, nil)
	_, err := c.send(context.Background(), srv.URL, forwardRequest{
		Owner: owner{userID: "alice", workspaceID: "W1"}, ShortID: "old-daemon",
		Command: "read_file",
	})
	var de *DaemonError
	require.ErrorAs(t, err, &de)
	require.Equal(t, commander.ErrCodeDaemonUpgradeRequired, de.Code)
}

func TestForwardClient_Stream_RoundTrip(t *testing.T) {
	secret := []byte("supersecret-32-chars-padding-aaaa")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Stream three envelopes terminated by command_result.
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		for i, e := range []commander.Envelope{
			{Type: "event", ID: "1", Payload: json.RawMessage(`{"event_kind":"chunk","text":"hi"}`)},
			{Type: "event", ID: "1", Payload: json.RawMessage(`{"event_kind":"chunk","text":" world"}`)},
			{Type: "command_result", ID: "1", Payload: json.RawMessage(`{"result":{"ok":true}}`)},
		} {
			require.NoError(t, writeEnvelopeFrame(w, e), "frame %d", i)
			flusher.Flush()
		}
	}))
	defer srv.Close()
	c := newForwardClient(secret, nil)
	ch, err := c.stream(context.Background(), srv.URL, forwardRequest{
		Owner: owner{userID: "alice", workspaceID: "W1"}, ShortID: "agent-A",
		Command: "session_turn", Streaming: true,
	})
	require.NoError(t, err)
	got := 0
	for env := range ch {
		got++
		_ = env
	}
	require.Equal(t, 3, got)
}

func TestForwardClient_Send_OversizedBody_Rejected(t *testing.T) {
	secret := []byte("supersecret-32-chars-padding-aaaa")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("server must not be reached — cap is enforced client-side")
	}))
	defer srv.Close()

	// Build an args payload that pushes wire body > 1.5 MiB.
	huge := strings.Repeat("x", int(forwardRequestBodyMaxBytes)+1)
	args := json.RawMessage(`"` + huge + `"`)
	c := newForwardClient(secret, nil)
	_, err := c.send(context.Background(), srv.URL, forwardRequest{
		Owner: owner{userID: "alice", workspaceID: "W1"}, ShortID: "agent-A",
		Command: "session_turn", Args: args,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "request body")
}

func TestForwardClient_Stream_CancelClosesChannel(t *testing.T) {
	secret := []byte("supersecret-32-chars-padding-aaaa")
	// Server emits one envelope every 50ms forever.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		t := time.NewTicker(50 * time.Millisecond)
		defer t.Stop()
		for i := 0; ; i++ {
			select {
			case <-r.Context().Done():
				return
			case <-t.C:
				if err := writeEnvelopeFrame(w, commander.Envelope{Type: "event", ID: "1", Payload: json.RawMessage(`{"event_kind":"tick"}`)}); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	}))
	defer srv.Close()

	c := newForwardClient(secret, nil)
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := c.stream(ctx, srv.URL, forwardRequest{
		Owner: owner{userID: "alice", workspaceID: "W1"}, ShortID: "agent-A",
		Command: "session_turn", Streaming: true,
	})
	require.NoError(t, err)

	// Read a few envelopes, then cancel.
	<-ch
	<-ch
	cancel()

	// Channel must close within 1s of cancel.
	deadline := time.After(time.Second)
	for {
		select {
		case _, open := <-ch:
			if !open {
				return // closed; test passes
			}
		case <-deadline:
			t.Fatal("channel did not close within 1s of ctx cancel")
		}
	}
}

func TestForwardClient_Send_NeitherSecretMatches_Errors(t *testing.T) {
	clientSecret := []byte("WRONG-secret-32-chars-padding-ff")
	serverSecret := []byte("RIGHT-secret-32-chars-padding-gg")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		ts, _ := parseHMACTimestamp(r.Header.Get("X-Observer-Cluster-Timestamp"))
		nonce := r.Header.Get("X-Observer-Cluster-Nonce")
		if _, ok := verifyForward(r.Header.Get("X-Observer-Cluster-Auth"), serverSecret, nil, ts, nonce, body); !ok {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		_, _ = fmt.Fprint(w, `{"result":{}}`)
	}))
	defer srv.Close()

	// Client has wrong secret AND no PrevSecret — single attempt, expect ErrDaemonGone.
	c := newForwardClient(clientSecret, nil)
	_, err := c.send(context.Background(), srv.URL, forwardRequest{
		Owner: owner{userID: "alice", workspaceID: "W1"}, ShortID: "agent-A",
		Command: "list_sessions",
	})
	require.ErrorIs(t, err, ErrDaemonGone)
}

// _ = bufio.NewReader keeps the import live for codec-internal tests
// added later; remove if a real usage lands.
var _ = bufio.NewReader
```

- [ ] **Step 2: Run; expect compile failure**

- [ ] **Step 3: Implement `forward_client.go`**

Create `internal/commanderhub/forward_client.go`:

```go
package commanderhub

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/yourorg/multi-agent/internal/commander"
)

// forwardRequest is the JSON body of a /api/commander/_internal/forward
// POST. Owner is the cluster-scoped identity (NOT the request's bearer
// token; the cluster secret is the only auth on this internal endpoint,
// see spec v19 §"Threat model").
type forwardRequest struct {
	Owner     owner           `json:"-"` // populated separately; JSON below
	ShortID   string          `json:"short_id"`
	Command   string          `json:"command"`
	Args      json.RawMessage `json:"args,omitempty"`
	Streaming bool            `json:"streaming"`
	TimeoutMs int64           `json:"timeout_ms"`
}

// forwardWireRequest is the actual JSON wire shape; flattens Owner.
type forwardWireRequest struct {
	UserID      string          `json:"user_id"`
	WorkspaceID string          `json:"workspace_id"`
	ShortID     string          `json:"short_id"`
	Command     string          `json:"command"`
	Args        json.RawMessage `json:"args,omitempty"`
	Streaming   bool            `json:"streaming"`
	TimeoutMs   int64           `json:"timeout_ms"`
}

// forwardResponse is the non-streaming response shape.
type forwardResponse struct {
	Result json.RawMessage         `json:"result,omitempty"`
	Error  *commander.ErrorPayload `json:"error,omitempty"`
}

// forwardEndpoint is the URL path on the INTERNAL listener (NOT the
// public mux) for pod-to-pod command forwarding. Same string is used by
// the client (here) and the server (forward_server.go, Task C4) so a
// future Ingress deny-rule for this path namespace can be added at
// chart level (Task E4) without drift.
const forwardEndpoint = "/api/commander/_internal/forward"

const forwardRequestBodyMaxBytes int64 = (1 << 20) + (1 << 19) // 1.5 MiB

type forwardClient struct {
	secret     []byte
	prevSecret []byte
	httpClient *http.Client
}

func newForwardClient(secret, prevSecret []byte) *forwardClient {
	return &forwardClient{
		secret:     secret,
		prevSecret: prevSecret,
		httpClient: &http.Client{
			Timeout: 0, // per-call ctx bounds; long streams need no client-side timeout
			Transport: &http.Transport{
				ResponseHeaderTimeout: 10 * time.Second,
				IdleConnTimeout:       60 * time.Second,
			},
		},
	}
}

func (c *forwardClient) buildRequest(ctx context.Context, peerURL string, req forwardRequest, useSecret []byte) (*http.Request, []byte, error) {
	wire := forwardWireRequest{
		UserID: req.Owner.userID, WorkspaceID: req.Owner.workspaceID,
		ShortID: req.ShortID, Command: req.Command, Args: req.Args,
		Streaming: req.Streaming, TimeoutMs: req.TimeoutMs,
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return nil, nil, fmt.Errorf("forward: marshal request: %w", err)
	}
	if int64(len(body)) > forwardRequestBodyMaxBytes {
		return nil, nil, fmt.Errorf("forward: request body %d > cap %d", len(body), forwardRequestBodyMaxBytes)
	}
	nonce, err := freshNonce()
	if err != nil {
		return nil, nil, err
	}
	ts := time.Now().Unix()
	sig := signForward(useSecret, ts, nonce, body)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, peerURL+forwardEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Content-Length", strconv.Itoa(len(body)))
	httpReq.Header.Set("X-Observer-Cluster-Timestamp", strconv.FormatInt(ts, 10))
	httpReq.Header.Set("X-Observer-Cluster-Nonce", nonce)
	httpReq.Header.Set("X-Observer-Cluster-Auth", sig)
	return httpReq, body, nil
}

// send: non-streaming forward. On 403 with PrevSecret available, retries
// once with PrevSecret (three-phase rotation accommodation).
func (c *forwardClient) send(ctx context.Context, peerURL string, req forwardRequest) (json.RawMessage, error) {
	if req.Streaming {
		return nil, fmt.Errorf("forward: send() called with Streaming=true; use stream()")
	}
	keys := c.keysToTry() // [secret] or [secret, prevSecret]
	for i, key := range keys {
		httpReq, _, err := c.buildRequest(ctx, peerURL, req, key)
		if err != nil {
			return nil, err
		}
		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			c.audit("forward.sent.failed", peerURL, req.ShortID, req.Command, err)
			return nil, ErrDaemonGone
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, forwardRequestBodyMaxBytes))
		_ = resp.Body.Close()
		// Only retry on 403 from the first key (current secret) if a
		// second key (prev secret) is available. Any 403 from the prev
		// key, or 403 with no prev key, is a real auth failure.
		if resp.StatusCode == http.StatusForbidden && i == 0 && len(keys) > 1 {
			c.audit("forward.sent.retry_with_prev", peerURL, req.ShortID, req.Command, nil)
			continue
		}
		return c.mapResponse(resp.StatusCode, body, peerURL, req)
	}
	return nil, ErrDaemonGone
}

// stream: streaming forward. Returns a channel that decodes envelopes
// from the chunked HTTP response. Channel closed on terminal frame or
// upstream error. On 403 + PrevSecret, retries once.
func (c *forwardClient) stream(ctx context.Context, peerURL string, req forwardRequest) (<-chan commander.Envelope, error) {
	if !req.Streaming {
		return nil, fmt.Errorf("forward: stream() called with Streaming=false; use send()")
	}
	var resp *http.Response
	var lastErr error
	keys := c.keysToTry()
	for i, key := range keys {
		httpReq, _, err := c.buildRequest(ctx, peerURL, req, key)
		if err != nil {
			return nil, err
		}
		r, err := c.httpClient.Do(httpReq)
		if err != nil {
			lastErr = err
			continue
		}
		// Only retry on 403 from the first key (current secret) if a
		// second key (prev secret) is available.
		if r.StatusCode == http.StatusForbidden && i == 0 && len(keys) > 1 {
			c.audit("forward.stream.retry_with_prev", peerURL, req.ShortID, req.Command, nil)
			_ = r.Body.Close()
			continue
		}
		resp = r
		break
	}
	if resp == nil {
		c.audit("forward.stream.failed", peerURL, req.ShortID, req.Command, lastErr)
		return nil, ErrDaemonGone
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, forwardRequestBodyMaxBytes))
		_ = resp.Body.Close()
		_, err := c.mapResponse(resp.StatusCode, body, peerURL, req)
		return nil, err
	}
	out := make(chan commander.Envelope, 256)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		reader := bufio.NewReader(resp.Body)
		for {
			env, err := readEnvelopeFrame(reader)
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				out <- commander.Envelope{
					Type: "error",
					Payload: json.RawMessage(fmt.Sprintf(`{"code":%q,"message":%q}`,
						commander.ErrCodeBackendUnavailable, err.Error())),
				}
				return
			}
			select {
			case out <- env:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// mapResponse: turn HTTP status + body into either a result payload or
// the appropriate error (ErrDaemonNotFound for 404, *DaemonError for
// daemon-origin errors, ErrDaemonGone for everything else).
func (c *forwardClient) mapResponse(status int, body []byte, peerURL string, req forwardRequest) (json.RawMessage, error) {
	switch status {
	case http.StatusOK:
		var fr forwardResponse
		if err := json.Unmarshal(body, &fr); err != nil {
			return nil, fmt.Errorf("forward: malformed peer response: %w", err)
		}
		if fr.Error != nil {
			return nil, &DaemonError{Code: fr.Error.Code, Message: fr.Error.Message}
		}
		return fr.Result, nil
	case http.StatusNotFound:
		c.audit("forward.sent.404", peerURL, req.ShortID, req.Command, nil)
		return nil, ErrDaemonNotFound
	case http.StatusUpgradeRequired: // 426
		var fr forwardResponse
		_ = json.Unmarshal(body, &fr)
		if fr.Error == nil {
			fr.Error = &commander.ErrorPayload{Code: commander.ErrCodeDaemonUpgradeRequired, Message: "daemon upgrade required"}
		}
		return nil, &DaemonError{Code: fr.Error.Code, Message: fr.Error.Message}
	case http.StatusForbidden:
		c.audit("forward.sent.denied", peerURL, req.ShortID, req.Command, nil)
		return nil, ErrDaemonGone
	default:
		c.audit("forward.sent.5xx", peerURL, req.ShortID, req.Command, fmt.Errorf("status %d", status))
		return nil, ErrDaemonGone
	}
}

// keysToTry returns the HMAC keys to attempt, in order:
//   - len 1: [secret] when no rotation is in progress.
//   - len 2: [secret, prevSecret] during three-phase rotation. Caller
//     attempts the first key; only on 403 does it retry with the second.
func (c *forwardClient) keysToTry() [][]byte {
	if c.prevSecret == nil {
		return [][]byte{c.secret}
	}
	return [][]byte{c.secret, c.prevSecret}
}

// audit emits a structured WARN/INFO line. Never logs secret/nonce/auth.
func (c *forwardClient) audit(event, peer, shortID, command string, err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	log.Printf("forward audit event=%s peer=%s short_id=%s command=%s err=%q",
		event, peer, shortID, command, msg)
}
```

Note the audit signature subtly differs from the spec's "structured log to stderr" wording — it uses stdlib `log` which writes to stderr by default. If the project later adopts a structured logger, swap the impl; the audit-line format is fixed (event, peer, short_id, command, err).

- [ ] **Step 4: Add `forwardCli *forwardClient` field to Hub struct**

In `internal/commanderhub/hub.go`, find the Hub struct (post-B1 shape) and add `forwardCli` next to `sharedReg`:

```go
type Hub struct {
	resolver     identity.Resolver
	upgrader     websocket.Upgrader
	reg          *localRegistry
	sharedReg    *sharedRegistry
	forwardCli   *forwardClient // C3: nil in single-pod; populated by Phase D D1's attachSharedRegistry
	turns        turnStateBackend
	sessionCache *sessionListCache
	cmdSeq       atomic.Int64

	TurnTimeout time.Duration
}
```

- [ ] **Step 5: Run; expect pass**

```sh
go test ./internal/commanderhub -run 'TestForwardClient_' -count=1 -race
```

- [ ] **Step 6: Commit**

```sh
git add internal/commanderhub/forward_client.go \
        internal/commanderhub/forward_client_test.go \
        internal/commanderhub/hub.go
git commit -m "feat(commanderhub): forwardClient send + stream + retry-on-403-PrevSecret

Pod-to-pod HTTP client for the internal /api/commander/_internal/forward
endpoint. Marshals forwardRequest, signs HMAC (Task C2), POSTs to peer.
On 403 + non-nil PrevSecret, retries once (three-phase secret rotation
accommodation). 404 → ErrDaemonNotFound. 426 → *DaemonError with
ErrCodeDaemonUpgradeRequired. Streaming response decodes length-
prefixed envelopes (Task C1 codec) into a buffered channel (256).
Hub.forwardCli field declared but populated only by Phase D D1's
attachSharedRegistry.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Tasks C4, C5, C6 — abbreviated specification

The remaining Phase C tasks follow the same shape as C1–C3. **For brevity in this plan revision, they are summarized; the executing subagent for each task expands the test list and code following the patterns established above. The Plan document author commits to following this expansion in plan v10 once Phase A+B execution feedback validates the level of detail.**

#### Task C4: `forwardServer` HTTP handler + extract `sendCommandToLocal` / `sendCommandStreamToLocal` helpers

**Files:**
- Create: `multi-agent/internal/commanderhub/forward_server.go`
- Create: `multi-agent/internal/commanderhub/forward_server_test.go`
- Modify: `multi-agent/internal/commanderhub/proxy.go` — EXTRACT helpers from existing `SendCommand` and `SendCommandStream` bodies. **This extraction happens in C4 (NOT D1)** because the forwardServer needs to invoke the local-only path, and the only thing that knows how is the existing proxy.go logic. D1 only adds the BRANCHING (`localReg miss → forwardCli.send`).

**Extraction shape** (concrete, not a TODO):
- `(h *Hub) sendCommandToLocal(ctx context.Context, dc *daemonConn, command string, args json.RawMessage) (json.RawMessage, error)` — body of today's `SendCommand` AFTER `h.reg.lookup` succeeds (lines `proxy.go:45-79` today). Takes the resolved `*daemonConn` as arg instead of looking it up.
- `(h *Hub) sendCommandStreamToLocal(ctx context.Context, dc *daemonConn, command string, args json.RawMessage, outBuffer int) (<-chan commander.Envelope, error)` — body of today's `SendCommandStream` AFTER `h.reg.lookup` succeeds (lines `proxy.go:90-130`). `outBuffer` controls the wrapper channel size (16 for browser SSE path, 256 for forwarding receivers — see spec v19 §"Back-pressure").
- Existing `SendCommand` becomes:
  ```go
  func (h *Hub) SendCommand(ctx context.Context, o owner, shortID, command string, args json.RawMessage) (json.RawMessage, error) {
      if dc, ok := h.reg.lookup(o, shortID); ok {
          if !dc.confirmOwnership(ctx) {
              return nil, ErrDaemonGone
          }
          return h.sendCommandToLocal(ctx, dc, command, args)
      }
      // Remote path is wired in Phase D Task D1 (this task only does
      // the local-extraction half so C4's tests can compile).
      return nil, ErrDaemonNotFound
  }
  ```
  Same shape for `SendCommandStream`. D1 then replaces the `return nil, ErrDaemonNotFound` lines with the shared-registry remote lookup + forwardCli call.

**`forwardServer` interface:** `(h *Hub).forwardHandler(w http.ResponseWriter, r *http.Request)` mounted at `forwardEndpoint` (from C3) on the INTERNAL mux only.

**Receiver pipeline (STRICT ORDER per spec v19 §"Receiver"):**

1. If `r.Method != http.MethodPost` → 405.
2. If `r.ContentLength > forwardRequestBodyMaxBytes` (1.5 MiB) → 413.
3. Parse header `X-Observer-Cluster-Timestamp` via `parseHMACTimestamp` → 400 on err.
4. Parse header `X-Observer-Cluster-Nonce` via `parseHMACNonce` → 400 on err.
5. Validate `X-Observer-Cluster-Auth` is 64 hex chars → 400 on err.
6. If `!timestampWithinWindow(ts, time.Now(), forwardHMACTimestampWindow)` → 403 + audit `forward.received.denied.timestamp`.
7. Read body via `io.ReadAll(io.LimitReader(r.Body, forwardRequestBodyMaxBytes+1))` → 413 if N+1.
8. `verifyForward(authHeader, h.cluster.Secret, h.cluster.PrevSecret, ts, nonce, body)` → 403 + audit `forward.received.denied.hmac` on mismatch.
9. `insertNonce(ctx, h.sharedReg.db, nonce)` → 503 + audit `forward.received.503.nonce_pg` on PG error (**fail closed; never proceed**); 403 + audit `forward.received.denied.replay` on `inserted=false`.
10. Audit accepted: `forward.received.accepted`.
11. Decode body as `forwardWireRequest`. Build `o := owner{userID: wire.UserID, workspaceID: wire.WorkspaceID}`.
12. `dc, ok := h.reg.lookup(o, wire.ShortID)` (LOCAL ONLY — `sharedReg.lookupRemote` would create peer-to-peer loops). 404 if missing.
13. If `wire.Command == "read_file"` AND `dc.capabilities[commander.CapabilityFilePreviewEncodedCap] == false`: respond 426 with `{"error":{"code":"daemon_upgrade_required","message":"daemon binary too old; upgrade required for file preview in cluster mode"}}`. (Spec v19 §"Capability gate".)
14. If `wire.Streaming == false`: invoke `h.sendCommandToLocal`; marshal `{result|error}` per `mapResponse` shape; 200.
15. If `wire.Streaming == true`: set `Content-Type: application/octet-stream`; `http.Flusher`; start drain goroutine that watches `r.Context().Done()` (caller cancellation) AND the returned channel; for each envelope, `writeEnvelopeFrame(w, env)` + `flusher.Flush()`; close on terminal frame or ctx cancel; on `r.Context().Done()`, cancel the inner ctx passed to `sendCommandStreamToLocal` so `dc.removePending` runs and frees the daemon slot.

**`Hub.cluster` field:** D1 adds a `cluster ClusterRuntime` field with `Secret`/`PrevSecret`/`InternalListenAddr`. C4 declares the field; D1 populates it via `attachSharedRegistry`.

Add to `internal/commanderhub/hub.go` (in the Hub struct, alongside `forwardCli`):

```go
cluster ClusterRuntime // C4: zero-value in single-pod; populated by D1's attachSharedRegistry
```

(`ClusterRuntime` is also declared in C4 since C4 is the first task that reads it. D1 adds it to the `MountAll` signature; the struct itself is here.)

```go
// ClusterRuntime is the resolved view of cluster.* config that observer-
// server passes to MountAll. Empty/zero values in any field disable
// cluster mode end-to-end.
type ClusterRuntime struct {
	DB                 *sql.DB
	AdvertiseURL       string
	Secret             []byte
	PrevSecret         []byte
	InternalListenAddr string
}
```

**Tests** (concrete, full coverage):
1. `TestForwardServer_AcceptsValidRequest` — full round-trip non-streaming.
2. `TestForwardServer_405_Method` — GET → 405.
3. `TestForwardServer_413_ContentLength` — Content-Length > 1.5 MiB → 413, body never read.
4. `TestForwardServer_400_MissingHeaders` — each of timestamp/nonce/auth absent → 400.
5. `TestForwardServer_400_MalformedHeader` — non-hex auth, bad nonce length, non-numeric timestamp → 400.
6. `TestForwardServer_403_TimestampDrift` — ts older than 60s → 403.
7. `TestForwardServer_413_BodyOverCap` — actual body > cap (Content-Length lied) → 413.
8. `TestForwardServer_403_HMACMismatch` — wrong secret → 403.
9. `TestForwardServer_503_NoncePGUnavailable` — sqlmock `insertNonce` returns error → 503. **Asserts the response is 503, NOT 200 (fail-closed).**
10. `TestForwardServer_403_NonceReplay` — sqlmock `insertNonce` returns inserted=false → 403.
11. `TestForwardServer_404_DaemonNotInLocalRegistry` — wire request for unknown short_id → 404. **Verify the server DOES NOT call `sharedReg.lookupRemote` (would create peer loops); use sqlmock with NO ExpectQuery for lookupRemoteSQL and assert ExpectationsWereMet.**
12. `TestForwardServer_426_DaemonMissingCapability` — daemon registered without `CapabilityFilePreviewEncodedCap`; `read_file` command → 426 with daemon_upgrade_required error code.
13. `TestForwardServer_Streaming_RoundTrip` — daemon emits 3 envelopes; client receives 3.
14. `TestForwardServer_Streaming_CancelPropagates` — caller cancels ctx; server drain exits within 1s; `dc.removePending` was called.

- Commit: `feat(commanderhub): forwardServer handler with strict-ordered auth + nonce insert + local-only lookup + 426 capability gate`.

#### Task C5: `drainHandler` endpoint

- Files: `drain_server.go` (new), `drain_server_test.go` (new), `hub.go` (add `(h *Hub).drainHandler` method).
- Interface: `(h *Hub).drainHandler(w http.ResponseWriter, r *http.Request)` mounted at `/api/commander/_internal/drain` on the INTERNAL mux. Loopback bypass via `net.ParseIP(host).IsLoopback()` on `r.RemoteAddr` — else HMAC verify (same as forward). Iterates `h.reg` for all daemons of all owners (NO owner filter — the preStop hook drains everything), sends `event_kind: observer_draining` envelope, closes WS.
- Tests: loopback bypass works, non-loopback requires HMAC, all daemons closed.
- Commit: `feat(commanderhub): drainHandler endpoint for preStop hook + cluster-internal drain`.

#### Task C6: `Hub.nextCmdID` pod-prefix in shared mode

- Files: `hub.go` (modify `nextCmdID`), `hub_test.go` (add tests).
- Interface: `(h *Hub) nextCmdID() string`. Single-pod (`h.sharedReg == nil`): exactly `strconv.FormatInt(h.cmdSeq.Add(1), 36)` — byte-for-byte unchanged from today (spec invariant). Shared mode: `<podHash>-<base36>` where `podHash = hex(sha256(h.sharedReg.advertiseURL))[:4]`.
- Tests:
  - `TestNextCmdID_SinglePod_ByteExactLegacy`: in a Hub with `sharedReg == nil`, first 5 calls return `"1"`, `"2"`, `"3"`, `"4"`, `"5"`.
  - `TestNextCmdID_SharedMode_PodPrefix`: shared mode with advertiseURL set; calls return `<4hex>-1`, `<4hex>-2`, etc.; prefix derived deterministically from URL.
- Commit: `feat(commanderhub): cmdID pod-prefix in shared mode for cross-pod log correlation`.

---

### Phase C Gate

```sh
cd multi-agent
go vet ./...
go test ./internal/commanderhub -count=1 -race
```

All Phase A+B+C tests pass. Forwarding client/server round-trip via httptest. **Dispatch to codex for Phase C review** before starting Phase D.

---

## Phase D — Wiring, read-path migration, observer-server lifecycle (5 tasks)

Phase D wires the new pieces into existing code paths. Each task in summary form; same expansion pattern as Phase C.

### Task D1: `Hub.attachSharedRegistry` + `listDaemons` + `lookupDaemon` + caller migration + `Hub.Close`

- Files: `wiring.go` (modify `MountAll` signature to `(publicMux, internalMux *http.ServeMux, resolver, agentserverURL, store, cluster ClusterRuntime)`), `hub.go` (expand `attachSharedRegistry(sr, fc, turns, sessionsCache nil)`; add `(h *Hub).Close(ctx) error` that calls `h.forwardCli.transport.CloseIdleConnections()` plus any other shutdown tasks — spec v19 §"Hub.Close"), `proxy.go` (branch SendCommand[Stream]: localReg hit → sendCommandToLocal which calls confirmOwnership; miss → sharedReg.lookupRemote → forwardCli.send/stream), `http.go` (`ch.daemons`/`ch.tree`/`ch.sessionsFanout` use `hub.listDaemons`; `ch.turn` existence guard uses `hub.lookupDaemon`; `writeSendCmdError` adds 426 for `ErrCodeDaemonUpgradeRequired`), `tree.go` (`CommanderTree` → listDaemons; `cachedSessionRows` skips cache when `h.sessionCache == nil`).
- Tests: extend existing `*_test.go` (real-WS path); add `wiring_test.go` for `MountAll` signature; verify in-package single-pod runs unchanged; new `TestHub_Close_ShutsDownForwardClient` asserts `forwardCli` is non-nil after attach AND that Close idempotently closes idle conns.
- Commit: `feat(commanderhub): wire shared registry through MountAll + SendCommand[Stream] + read-path helpers + Hub.Close`.

### Task D2: `*pgTurnStore` (cross-pod begin / get / updateFromEnvelope / cleanupOrphans)

- Files: `turn_state_pg.go` (new), `turn_state_pg_test.go` (new, sqlmock).
- Interface: `*pgTurnStore` implements `turnStateBackend`. `begin` uses `INSERT … ON CONFLICT … WHERE state IN ('idle','done','error','awaiting_approval','disconnected') RETURNING (xmax=0) AS inserted`. `updateFromEnvelope` invoked by owning pod's `routeFrame` (Phase D adds the hook in `hub.go::routeFrame` for shared mode). `cleanupOrphans` flips `state='disconnected'` for rows older than `older`.
- Tests: sqlmock for SQL shape; integration test against `OBSERVER_POSTGRES_TEST_DSN` for the begin-on-conflict semantics (xmax read).
- Commit: `feat(commanderhub): pgTurnStore — cross-pod turn-state via commander_turns`.

### Task D3: `*pgTelemetryLimiter` (atomic UPSERT-with-LEAST + lock_timeout in transaction)

- Files: `internal/observerweb/rate_limit_pg.go` (new), `internal/observerweb/rate_limit_pg_test.go` (new, sqlmock + env-skipped integration). `cmd/observer-server/main.go` (selection rule: `cluster.enabled AND store.driver=="postgres" AND telemetry.enabled` → PG variant).
- Interface: per spec v19 §"Finding E — telemetry rate limiter" — `(l *pgTelemetryLimiter) allow(ctx, key, now) (bool, error)`. Wraps the UPSERT in `BeginTx → SET LOCAL lock_timeout = '100ms' → ExecContext(upsertSQL) → Commit`. The UPSERT computes refill from `b.tokens` (existing row) not `EXCLUDED.tokens`.
- Commit: `feat(observerweb): pgTelemetryLimiter — atomic shared token bucket via commander_telemetry_buckets`.

### Task D4: Identity revocation channel (functional-options NewCache + revocation_pg.go)

- Files: `internal/identity/cache.go` (change `NewCache(d, cfg)` → `NewCache(d, cfg, opts ...CacheOption) Resolver`; add `(c *cacheResolver).evict(key string)`). Create `internal/identity/revocation_pg.go` (`WithRevocationChannel(listener *pgx.Conn, publisher *sql.DB, channel string) CacheOption`; LISTEN goroutine; NOTIFY publish gated per spec v19 §"Publish policy"). Create `cache_pg_test.go` (env-skipped, two cacheResolver against shared PG).
- Tests: functional-options compile against existing callers (`cmd/observer-server/main.go:632`); NOTIFY-driven eviction propagates within 100ms.
- Commit: `feat(identity): opt-in PG LISTEN/NOTIFY revocation channel via functional options`.

### Task D5: observer-server `Cluster ClusterConfig` + `loadConfig` merge + `validateConfig` + dual-listener lifecycle

- Files: `cmd/observer-server/main.go` (new fields per spec v19 §"Cluster config" + AgentserverIdentityConfig pointer-nullable; loadConfig merges sibling `nonsecret/observer.nonsecret.yaml`; validateConfig partial-cluster + loopback-coverage + `cluster.enabled AND store.driver!=postgres` rules; post-merge defaulting for FreshTTL/RevocationChannel; dual `*http.Server` under `errgroup` with coordinated `Shutdown`; replace `newHTTPServer` with `newPublicHTTPServer`/`newInternalHTTPServer` (no WriteTimeout)); `cmd/observer-server/cluster_runtime.go` (new); `cmd/observer-server/drain_local.go` (new — `--drain-local` subcommand validates loopback-reachable internal_listen_addr; exit 1 on config-read error; exit 0 with WARN on connect error).
- Files: `internal/observerweb/server.go` (Options.Cluster + dual return from `NewWithResolverOptions`).
- Tests: `main_test.go` matrix for validateConfig partial-cluster rules and pointer-nullable post-merge defaulting; integration test for the dual-listener shutdown.
- Commit: `feat(observer-server): cluster config + dual listener + drain-local subcommand`.

### Task D6: multi-pod regression tests (`multi_pod_test.go` + `multi_pod_files_test.go`)

- Files: `multi-agent/internal/commanderhub/multi_pod_test.go` (new), `multi-agent/internal/commanderhub/multi_pod_files_test.go` (new). Both env-skipped on `OBSERVER_POSTGRES_TEST_DSN` (mirroring authstore/postgres_test.go).
- Interface contract (per spec v19 §"Testing — Integration"): boot two `Hub` instances against the same Postgres, both with `attachSharedRegistry` (different `advertiseURL` per Hub). Stand up two `httptest.Server`s, one per Hub, on the INTERNAL mux. Connect a mock daemon to Hub A. Verify:
  - Hub B `listDaemons(ctx, o)` returns 1 entry pointing to Hub A.
  - Hub B `SendCommand(ctx, o, shortID, "list_sessions", nil)` round-trips via forwardClient → Hub A → daemon → reply.
  - Hub B `SendCommandStream(ctx, o, shortID, "session_turn", args)` round-trips with N envelopes ending in terminal.
  - Concurrent `turns.begin(same key)` on both hubs — exactly ONE returns true (cross-pod dedup via `commander_turns` UPSERT).
  - Force-disconnect Hub A's mock daemon; trigger Hub B's sweep manually (`s.runSweepOnce(ctx)` after advancing test clock) and assert the row is removed.
  - Reconnect daemon to Hub B; assert subsequent `listDaemons` from Hub A (relaunched) sees correct `owning_instance_url=hub-B`.
- `multi_pod_files_test.go` tests: forward a pathological 2 MiB-of-`\x01` file via `read_file`; assert daemon returned `TooLarge=true` AND the forwarded envelope wire size stayed under 1 MiB.
- Commit: `test(commanderhub): multi_pod_test + multi_pod_files_test (env-skipped on OBSERVER_POSTGRES_TEST_DSN)`.

---

### Phase D Gate

```sh
cd multi-agent
go vet ./...
go test ./... -race -count=1
```

All single-pod + shared-mode unit/integration tests pass. **Dispatch to codex for Phase D review** before starting Phase E.

---

## Phase E — Chart + CI + docs (5 tasks)

### Task E1: `values.yaml` + `values-production.example.yaml`

Per spec v19 §"Helm chart values" + §"values-production". Including `revocationChannel: auto|enabled|disabled` enum + `freshTTL: ""` default + `cluster:` block. Test renders.

Commit: `chore(chart): values.yaml + values-production.example.yaml (cluster block + identity defaults)`.

### Task E2: `templates/validate.yaml` (always-rendered)

Per spec v19 §"Helm validate.yaml". Four fail guards. Add chart tests for each fail case.

Commit: `chore(chart): templates/validate.yaml fail-fast guards for cluster + sqlite + secret-length`.

### Task E3: `templates/{configmap,secret,deployment}.yaml` renders + init container + preStop

Per spec v19 §"Configmap snippet" + §"templates/secret.yaml" v17/v18 changes + §"Init container" + §"preStop". Single `initContainers:` block. Conditional fresh_ttl/revocation_channel emission. preStop exec calls `observer-server --drain-local --config ... --internal-port=...`.

Commit: `chore(chart): deployment + configmap + secret renders for cluster mode`.

### Task E4: `templates/service.yaml` (headless) + `templates/networkpolicy.yaml` + ingress/httproute hardening

Per spec v19 §"Internal Service — headless" + §"Internal NetworkPolicy" + §"Ingress/HTTPRoute hardening". Two-rule NetworkPolicy (allow public 8090 from anywhere; restrict 8091 to observer peers).

Commit: `chore(chart): headless Service + NetworkPolicy + ingress deny for /_internal/*`.

### Task E5: `chart_test.sh` extensions + `observer-deploy.yml` + `deploy/README.md` + `dev/compose.multi-observer.yaml`

Chart-test assertions per spec v19 §"Chart tests" blocks 1–7 (default, multi-pod, fail-fast, existingSecret-renders-into-ConfigMap, secret.create-renders-into-Secret, revocationChannel=disabled emits empty, invalid enum fails).

`observer-deploy.yml` smoke: generate `cluster_secret` (48 chars) + `::add-mask::`; bump `replicaCount: 2`; render `cluster.enabled=true`. Resolve pod IPs in GitHub runner; render one wget Job per pod IP. Release: require `OBSERVER_CLUSTER_SECRET` in `required` list.

`deploy/README.md`: pre-rollout coordination; three-phase rotation; mixed-version window caveat; clients-treat-DaemonInfo.DaemonID-as-opaque.

`dev/compose.multi-observer.yaml` + `dev/README.md`: 2 observers + 1 PG + nginx LB for local repro.

Commit: `ci(observer-deploy) + chore(chart) + docs(deploy): full cluster-mode rollout assets`.

---

### Phase E Gate

```sh
cd multi-agent
go test ./... -race -count=1
helm template observer-test deploy/charts/observer | head -20  # smoke
deploy/charts/observer/tests/chart_test.sh
```

All gates pass. **Dispatch to codex for Phase E review.** After codex clean, the implementation is ready for end-to-end verification per spec v19 §"Verification".

---


