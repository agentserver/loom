# Shared commanderhub Daemon Registry Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the commanderhub work correctly when the observer is horizontally scaled (replicaCount > 1), by sharing daemon-registry + turn-state via Postgres and forwarding pod-to-pod commands over an authenticated internal HTTP listener. Closes [issue #49](https://github.com/agentserver/loom/issues/49).

**Architecture:** Four layers. (1) Postgres-backed `commander_daemons` table — owner-pod UPSERTs on connect, heartbeats every 15 s with ownership guard, sweeps stale rows after 5 min. (2) Internal forwarding listener on a separate port (`:8091` default) authenticated via HMAC + nonce + 60 s replay window, with NetworkPolicy + Ingress deny rule defense-in-depth. (3) Postgres-backed `turnStateStore` — owner-pod `routeFrame` is the single writer; `turns.begin()` provides cross-pod turn-in-flight dedup. (4) `sessionListCache` disabled in shared mode (per-pod cache + cross-pod invalidation cost > benefit). All four gated by config; fail-closed on partial config.

**Tech Stack:** Go 1.26.x, gorilla/websocket, jackc/pgx/v5 (via `database/sql` driver), encoding/json, crypto/hmac, Postgres 16, Kubernetes 1.27+ (Helm chart, NetworkPolicy v1, downward API), HTTP/1.1 chunked, length-prefixed JSON envelopes.

## Global Constraints

- **Source spec:** `docs/superpowers/specs/2026-06-29-shared-daemon-registry-design.md` (v9; codex-reviewed clean).
- **No regression to single-pod mode.** Every change must preserve current behavior when `cluster.advertise_url` and `cluster.secret_env` are both empty. The 30+ existing test sites that call `hub.reg.add(...)`/`hub.reg.daemons(...)` must continue to compile (only `daemonConn` fixtures gain a `shortID` field set).
- **Fail-closed on partial cluster config.** `validateConfig` rejects any mix where exactly one of (advertise URL, secret) is configured. The chart's `templates/validate.yaml` rejects `replicaCount > 1` without `cluster.enabled=true` AND without `store.driver=postgres`.
- **Wire caps (immutable across this plan):** forward request body ≤ 1.5 MiB (`1 << 20 + 1 << 19`); each length-prefixed envelope ≤ 1 MiB (`1 << 20`); observer-side `wsReadLimit` STAYS at 1 MiB. The daemon-side `commander/files.go::Handler.ReadFile` is what keeps `read_file` responses within the envelope cap.
- **Auth on internal listener:** HMAC-SHA256 over `(timestamp || "\n" || nonce || "\n" || body)`, compared via `hmac.Equal` on fixed-size `[32]byte` arrays. Timestamp window: 60 s. Nonce: 32 random hex chars, atomic INSERT to `commander_forward_nonces` AFTER HMAC verify. **Loopback bypass on `/api/commander/_internal/drain` only.** Secret rotation via current+previous secret pair (three-phase ops procedure).
- **TDD discipline.** Every task starts with a failing test, then minimal code, then a passing test, then commit.
- **Commit prefixes:** Go commits use `feat(commanderhub): …` / `fix(commanderhub): …`. Chart commits use `chore(chart): …`. CI commits use `ci(observer-deploy): …`. Docs commits use `docs(deploy): …` / `docs(spec): …`.
- **No `go.work`.** This repo has only `multi-agent/go.mod`; run all `go` commands from `multi-agent/`.
- **Postgres integration tests are env-skipped.** All tests requiring Postgres check `OBSERVER_POSTGRES_TEST_DSN`; skip with `t.Skip(...)` when unset. CI does not require these.
- **Race detector mandatory.** Every `go test` command uses `-race`.

---

## Source Spec

Implement:

- `docs/superpowers/specs/2026-06-29-shared-daemon-registry-design.md`

## File Structure

The plan touches four areas: **commanderhub Go package**, **observer-server command/config**, **commander shared package (daemon-side)**, and **Helm chart + CI**.

### commanderhub Go package (`multi-agent/internal/commanderhub/`)

- Modify: `registry.go`
  - Rename existing `registry` → `localRegistry`; add `removeIf(o, shortID, connectionID)` for connection-id-guarded delete; preserve `add`/`lookup`/`daemons` method surface. `daemonConn` keeps its `id` field (per-connection random hex; serves as `connection_id`); add `shortID` (already present via register payload assignment at `hub.go:111`) + `ownershipLost atomic.Bool`.
- Create: `registry_shared.go`
  - New `*sharedRegistry` type: `connectUpsert`, `heartbeatUpsert`, `remove`, `lookupRemote`, `listAll`, `sweep`, `sweepNonces`, `confirmOwnership` (queried via `daemonConn.confirmOwnership` helper).
- Create: `registry_shared_test.go`
  - `go-sqlmock` driven SQL assertions: ownership-guarded UPSERT/UPDATE/DELETE, peer-only `lookupRemote`, sweep filter.
- Modify: `hub.go`
  - `Hub` struct grows `sharedReg *sharedRegistry`, `forwardCli *forwardClient`. `NewHub(resolver)` signature unchanged; new `(h *Hub).attachSharedRegistry(sr, fc, turns)` used by `MountAll`. `newDaemonID` → 128-bit + error. `ServeHTTP` admission order: connectUpsert → localReg.add. Heartbeat goroutine wired via `runHeartbeat(ctx, dc)`. Deferred teardown: `localReg.removeIf` + `sharedReg.remove(..., dc.shortID, dc.id)`. Read path helpers: `listDaemons(ctx, o)`, `lookupDaemon(ctx, o, shortID)`.
- Modify: `proxy.go`
  - `SendCommand`/`SendCommandStream` branch on `localReg.lookup` → local OR `sharedReg.lookupRemote` → remote forward. Extract `sendCommandToLocal`/`sendCommandStreamToLocal` helpers. Both helpers call `dc.confirmOwnership(ctx)` before `writeEnvelope`. `FanOutSessions` uses `listDaemons`.
- Modify: `http.go`
  - `ch.daemons`/`ch.tree`/`ch.sessionsFanout` use `hub.listDaemons`. `ch.turn` existence guard uses `hub.lookupDaemon`. `writeSendCmdError` adds case for `commander.ErrCodeDaemonUpgradeRequired` → HTTP 426.
- Modify: `tree.go`
  - `CommanderTree` calls `listDaemons`. `cachedSessionRows` skips cache when `h.sessionCache == nil`. `invalidateDaemonSessions` is no-op when nil.
- Modify: `turn_state.go`
  - Extract `turnStateBackend` interface (with `context.Context`); rename `turnKey.daemonID` → `shortID`. In-memory impl satisfies interface, becomes `*memTurnStore`.
- Create: `turn_state_pg.go`
  - `*pgTurnStore` against `commander_turns`. `begin` uses `INSERT … ON CONFLICT … WHERE state IN (terminal-states) RETURNING (xmax=0)`. `updateFromEnvelope`/`cleanupOrphans` methods.
- Create: `turn_state_pg_test.go`
  - `go-sqlmock` driven.
- Create: `forward_codec.go`
  - Length-prefixed JSON envelope codec (read/write), 1 MiB envelope cap.
- Create: `forward_codec_test.go`
- Create: `forward_client.go`
  - HTTP client for pod-to-pod forwarding: HMAC signing, nonce generation, retry on 403 with PrevSecret, audit log line per send. `send(ctx, peerURL, req) (json.RawMessage, error)` and `stream(ctx, peerURL, req) (<-chan commander.Envelope, error)`.
- Create: `forward_client_test.go`
  - `httptest.Server`-driven: signing correctness, retry-on-403, body-cap, response-error mapping back to `*DaemonError`.
- Create: `forward_server.go`
  - `(h *Hub).forwardHandler` mounted at `/api/commander/_internal/forward` on internal mux. Implements receiver steps 1-8 (length check, headers, timestamp, body read, HMAC verify, nonce insert, audit, local-only lookup). Then calls `sendCommandToLocal`/`sendCommandStreamToLocal`; streams envelopes via codec.
- Create: `forward_server_test.go`
  - `httptest.Server`-driven: auth fail modes, replay rejection, body cap, stream cap, cancellation propagation, daemon-error round-trip.
- Create: `drain_server.go`
  - `(h *Hub).drainHandler` mounted at `/api/commander/_internal/drain` on internal mux. Loopback-bypass OR HMAC; iterates `localReg`, sends `observer_draining` event, closes WS.
- Create: `drain_server_test.go`
- Modify: `wiring.go`
  - `MountAll(publicMux, internalMux, resolver, agentserverURL, store, cluster ClusterRuntime)`. Builds `sharedRegistry`/`forwardClient`/`pgTurnStore` when `cluster.AdvertiseURL != ""`; calls `attachSharedRegistry`; mounts forward+drain on internal mux; starts sweeper goroutine.
- Modify: `wiring_test.go`
  - Update existing call site for new `MountAll` signature.
- Modify: existing `*_test.go` (`hub_test.go`, `proxy_test.go`, `http_test.go`, `tree_test.go`, `race_test.go`, `livelock_test.go`, `e2e_test.go`, `integration_test.go`)
  - Add `shortID: "<sentinel>"` to `daemonConn` literals; update `hub.reg.remove(o, id)` calls (verified rare) to `removeIf(o, shortID, connID)`.
- Create: `multi_pod_test.go`
  - Two-Hub Postgres-backed integration test (env-skipped). Asserts cross-pod visibility + forwarding + concurrent `turns.begin` dedup + sweep.
- Create: `multi_pod_files_test.go`
  - Forward a pathological 2 MiB control-byte file; assert `TooLarge=true`, envelope < 1 MiB.

### commanderhub authstore (`internal/commanderhub/authstore/`)

- Modify: `schema_postgres.sql`
  - Add three tables: `commander_daemons`, `commander_turns`, `commander_forward_nonces`.
- Create: `schema_postgres_rollback.sql`
  - Manual down migration: `DROP TABLE IF EXISTS …`.
- Modify: `postgres_test.go`
  - Conformance test verifies new tables created with expected columns/PKs/constraints (skip-on-missing-DSN).

### commander shared package (`internal/commander/`)

- Modify: `protocol.go`
  - Add `ErrCodeDaemonUpgradeRequired = "daemon_upgrade_required"`. Add `CapabilityFilePreviewEncodedCap = "file_preview_encoded_cap"`.
- Modify: `files.go::Handler.ReadFile`
  - After constructing `res`, `json.Marshal(res)` → if encoded length > 768 KiB, set `TooLarge=true, Content=""`.
- Modify: `files_test.go`
  - Test: 2 MiB file of `\x01` bytes returns `TooLarge=true, Content=""`.

### observer-server command (`cmd/observer-server/`)

- Modify: `main.go`
  - Config: `Cluster ClusterConfig` field. `validateConfig` partial-config rules + non-loopback internal_listen_addr rejection. `loadConfig` merges sibling `nonsecret/observer.nonsecret.yaml`. `buildClusterRuntime(cfg, st.DB())` resolves env vars + reads secrets from env. New `--drain-local` flag and subcommand path. `newPublicHTTPServer` + `newInternalHTTPServer` (streaming-safe; no WriteTimeout). Both servers started in errgroup; coordinated `Shutdown`.
- Create: `drain_local.go`
  - `runDrainLocal(cfg *Config) int` — config-read errors exit 1; connect errors exit 0 with WARN.
- Create: `cluster_runtime.go`
  - `buildClusterRuntime(cfg *Config, db *sql.DB) (commanderhub.ClusterRuntime, error)`.
- Modify: `main_test.go`
  - `validateConfig` matrix tests for partial cluster config.

### observerweb (`internal/observerweb/`)

- Modify: `server.go`
  - `Options` adds `Cluster commanderhub.ClusterRuntime` field. `NewWithResolverOptions(...) (publicHandler, internalHandler http.Handler)` (two returns). Two-arg constructors updated.
- Modify: `server_test.go`
  - Update tests to handle dual return.

### Helm chart (`deploy/charts/observer/`)

- Modify: `values.yaml`
  - `replicaCount: 2 → 1`. New `cluster:` block.
- Modify: `values-production.example.yaml`
  - `cluster.enabled: true`; doc note for `existingSecret` requirement.
- Create: `templates/validate.yaml`
  - Always-rendered template with comment-only body + 4× `{{- fail }}` guards.
- Modify: `templates/secret.yaml`
  - Add `cluster-secret`/`cluster-secret-prev` data keys (only inside existing `secret.create` gate).
- Modify: `templates/configmap.yaml`
  - `observer.nonsecret.yaml` adds `cluster:` block.
- Modify: `templates/deployment.yaml`
  - Single `initContainers:` block conditional on either Postgres-wait or cluster-secret-check. Add cluster env vars (downward API). Internal container port. preStop exec hook. Rolling strategy when cluster enabled.
- Modify: `templates/service.yaml`
  - Add second headless Service (`<release>-observer-headless`) when cluster enabled.
- Create: `templates/networkpolicy.yaml`
  - Two-rule policy: allow 8090 from anywhere, restrict 8091 to observer peers.
- Modify: `templates/ingress.yaml`, `templates/httproute.yaml`
  - Add deny-prefix rule for `/api/commander/_internal/`.
- Modify: `tests/chart_test.sh`
  - Three new assertion blocks.

### CI (`.github/workflows/`)

- Modify: `observer-deploy.yml`
  - Smoke job: generate `cluster_secret`, `::add-mask::`, bump `replicaCount: 2`, render `cluster.enabled=true`. Add new step to resolve pod IPs and per-pod readiness probe. Release job: require `OBSERVER_CLUSTER_SECRET` in secrets list.

### Docs (`deploy/`, `dev/`)

- Modify: `deploy/README.md`
  - Pre-rollout instructions; three-phase secret rotation playbook; mixed-version window caveats; cluster-secret threat model summary.
- Create: `dev/compose.multi-observer.yaml`
  - 2 observers + 1 Postgres + nginx LB for local repro.
- Create: `dev/README.md`
  - `make multi-observer-up` documentation.

---

## Task ordering

Tasks 1-4 lay the schema + interfaces with no behavior change (pre-flight).
Tasks 5-9 implement the registry + forwarding layers.
Tasks 10-12 wire the new pieces into the existing hub.
Tasks 13-15 add observability/lifecycle (audit log, drain, preStop).
Tasks 16-19 cover the chart + CI changes.
Tasks 20-21 cover daemon-side `commander` changes.
Tasks 22-24 are integration tests + docs.

Total: 24 tasks. A reasonable pace is 2-4 tasks per day.

---

## Task 1: Add ErrCodeDaemonUpgradeRequired + CapabilityFilePreviewEncodedCap

**Files:**
- Modify: `multi-agent/internal/commander/protocol.go:11-19` (const blocks)
- Modify: `multi-agent/internal/commander/protocol_test.go` (extend existing test file)

**Interfaces:**
- Produces:
  - `commander.ErrCodeDaemonUpgradeRequired string = "daemon_upgrade_required"`
  - `commander.CapabilityFilePreviewEncodedCap string = "file_preview_encoded_cap"`

- [ ] **Step 1: Write the failing test**

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

- [ ] **Step 2: Run test to verify it fails**

Run: `cd multi-agent && go test ./internal/commander -run 'TestErrCodeDaemonUpgradeRequiredDefined|TestCapabilityFilePreviewEncodedCapDefined' -count=1`

Expected: compile failure with `undefined: ErrCodeDaemonUpgradeRequired` and `undefined: CapabilityFilePreviewEncodedCap`.

- [ ] **Step 3: Add the constants**

Edit `internal/commander/protocol.go`. Find the capabilities block at lines 14-18:

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
	// CapabilityFilePreviewEncodedCap signals the daemon enforces a
	// JSON-encoded size cap on read_file responses (see
	// internal/commander/files.go::Handler.ReadFile). Observer shared-mode
	// gates read_file forwarding on this capability.
	CapabilityFilePreviewEncodedCap = "file_preview_encoded_cap"
)
```

Find the error code block at lines 124-128:

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
	// to HTTP 426 Upgrade Required so the client surfaces an actionable
	// "update your daemon" message.
	ErrCodeDaemonUpgradeRequired  = "daemon_upgrade_required"
)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd multi-agent && go test ./internal/commander -count=1 -race`

Expected: PASS (all existing tests + the two new ones).

- [ ] **Step 5: Commit**

```bash
git add internal/commander/protocol.go internal/commander/protocol_test.go
git commit -m "feat(commander): add ErrCodeDaemonUpgradeRequired + CapabilityFilePreviewEncodedCap"
```

---

## Task 2: Enforce JSON-encoded size cap in Handler.ReadFile

**Files:**
- Modify: `multi-agent/internal/commander/files.go:76-132` (ReadFile body + new constant)
- Modify: `multi-agent/internal/commander/files_test.go` (add encoded-size test)
- Modify: `multi-agent/cmd/driver-agent/main.go` (advertise capability)
- Modify: `multi-agent/cmd/slave-agent/main.go` (advertise capability)

**Interfaces:**
- Consumes: `commander.CapabilityFilePreviewEncodedCap` from Task 1.
- Produces: `Handler.ReadFile` returns `TooLarge=true, Content=""` when JSON-encoded result exceeds 768 KiB. Both daemon binaries advertise the new capability so observer can gate `read_file` forwarding.

- [ ] **Step 1: Write the failing test**

Inspect `internal/commander/files_test.go` to learn the test helper for constructing a `Handler` with a backend that resolves a session to a temp root. Use the existing pattern. Append:

```go
func TestReadFile_EncodedSizeCapPreventsControlByteBlowup(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "tricky.txt")
	// 1 MiB of 0x01 bytes: valid UTF-8, not binary, but each byte JSON-escapes
	// to  (6 bytes), so naive serialization would be ~6 MiB.
	tricky := bytes.Repeat([]byte{0x01}, 1024*1024)
	require.NoError(t, os.WriteFile(path, tricky, 0o644))

	h, sessID := newReadFileTestHandler(t, root)
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

If `newReadFileTestHandler` doesn't exist, refactor an existing helper from the file or inline the setup pattern other tests in the same file already use (look for `TestReadFile_*` tests for the pattern).

- [ ] **Step 2: Run test to verify it fails**

Run: `cd multi-agent && go test ./internal/commander -run TestReadFile_EncodedSizeCapPreventsControlByteBlowup -count=1`

Expected: FAIL — `res.TooLarge` is false (today's code returns full content), and `len(out)` is ~6 MiB.

- [ ] **Step 3: Add `maxEncodedFileResponse` + encoded-size guard**

Edit `internal/commander/files.go`. Add `"encoding/json"` to the imports if not already present (it isn't — verify).

After the existing `var (...)` block near the top (around line 20), add:

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

In `ReadFile`, find the final block (lines 124-131):

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

- [ ] **Step 4: Run test to verify it passes**

Run: `cd multi-agent && go test ./internal/commander -count=1 -race`

Expected: PASS (all existing tests + the new one).

- [ ] **Step 5: Advertise the capability in both daemon binaries**

Open `cmd/driver-agent/main.go` and locate the `commander.RegisterPayload{...}` literal near line 361 (inside the `commander.NewDaemon(commander.DaemonConfig{...})` call). The `Capabilities:` field is likely a slice of `commander.Capability*` constants. Add `commander.CapabilityFilePreviewEncodedCap` to that slice.

Example transform — if the current literal is:

```go
Register: commander.RegisterPayload{
    SchemaVersion: commander.SchemaVersion,
    ShortID:       cfg.Daemon.ShortID,
    DisplayName:   cfg.Daemon.DisplayName,
    Kind:          cfg.Daemon.Kind,
    DriverVersion: build.Version,
    Capabilities: []string{
        commander.CapabilitySessions,
        commander.CapabilityTurn,
        commander.CapabilityFiles,
    },
},
```

change to:

```go
Register: commander.RegisterPayload{
    SchemaVersion: commander.SchemaVersion,
    ShortID:       cfg.Daemon.ShortID,
    DisplayName:   cfg.Daemon.DisplayName,
    Kind:          cfg.Daemon.Kind,
    DriverVersion: build.Version,
    Capabilities: []string{
        commander.CapabilitySessions,
        commander.CapabilityTurn,
        commander.CapabilityFiles,
        commander.CapabilityFilePreviewEncodedCap,
    },
},
```

Apply the same change in `cmd/slave-agent/main.go` near line 453.

- [ ] **Step 6: Run daemon tests**

Run: `cd multi-agent && go test ./cmd/driver-agent ./cmd/slave-agent ./internal/commander -count=1 -race`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/commander/files.go internal/commander/files_test.go cmd/driver-agent/main.go cmd/slave-agent/main.go
git commit -m "feat(commander): bound ReadFile JSON-encoded size; advertise file_preview_encoded_cap

Pathological all-control-byte text files JSON-escape each byte as \\uXXXX,
producing payloads that exceed wsReadLimit (1 MiB) and the forwarding cap.
ReadFile now marshals the result and returns TooLarge=true (with empty
content) when the encoded size exceeds 768 KiB. driver-agent and
slave-agent advertise CapabilityFilePreviewEncodedCap so the observer can
gate read_file forwarding on this guarantee."
```

---

## Task 3: Add Postgres schema for commander_daemons + commander_turns + commander_forward_nonces

**Files:**
- Modify: `multi-agent/internal/commanderhub/authstore/schema_postgres.sql` (append three CREATE TABLE blocks)
- Create: `multi-agent/internal/commanderhub/authstore/schema_postgres_rollback.sql`
- Modify: `multi-agent/internal/commanderhub/authstore/postgres_test.go` (add table-existence + PK + CHECK assertions)

**Interfaces:**
- Produces: three Postgres tables created by `MigratePostgres(db)`:
  - `commander_daemons` PK `(user_id, workspace_id, short_id)`; cols `connection_id`, `display_name`, `kind`, `driver_version`, `capabilities jsonb`, `owning_instance_url`, `last_seen_at`, `created_at`.
  - `commander_turns` PK `(user_id, workspace_id, short_id, session_id)`; cols `state` (CHECK enum: idle/queued/answering/awaiting_approval/done/error/disconnected), `awaiting_approval`, `active_worker`, `message`, `updated_at`.
  - `commander_forward_nonces` PK `nonce`; col `received_at`.

- [ ] **Step 1: Write the failing tests**

Edit `internal/commanderhub/authstore/postgres_test.go`. Append (after the existing `TestPostgresStore_Conformance`):

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
		"commander_daemons", "commander_turns", "commander_forward_nonces",
	} {
		var exists bool
		require.NoError(t, db.QueryRow(
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)`,
			name,
		).Scan(&exists))
		require.True(t, exists, "table %s not created", name)
	}

	// PK assertion: commander_daemons keyed by short_id (NOT by ephemeral
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
}
```

- [ ] **Step 2: Run test to verify it fails (or is skipped without DSN)**

If you have a local PG instance:

```bash
OBSERVER_POSTGRES_TEST_DSN="postgres://user:pass@localhost:5432/test?sslmode=disable" \
  go test ./internal/commanderhub/authstore -run TestPostgresStore_ClusterTablesCreated -count=1
```

Expected: FAIL with `table commander_daemons not created`.

If you don't have local PG, `t.Skip` fires — that's the expected baseline.

- [ ] **Step 3: Append the schema**

Append to `internal/commanderhub/authstore/schema_postgres.sql`:

```sql

-- Issue #49 cluster-mode tables. See
-- docs/superpowers/specs/2026-06-29-shared-daemon-registry-design.md.

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
```

- [ ] **Step 4: Create rollback file**

Create `internal/commanderhub/authstore/schema_postgres_rollback.sql`:

```sql
-- Manual down migration for the issue-#49 cluster-mode tables.
-- Run with `psql "$OBSERVER_DATABASE_URL" -f schema_postgres_rollback.sql`
-- BEFORE rolling back observer-server to a pre-issue-#49 image.
DROP TABLE IF EXISTS commander_forward_nonces;
DROP TABLE IF EXISTS commander_turns;
DROP TABLE IF EXISTS commander_daemons;
```

- [ ] **Step 5: Run the conformance tests**

With local PG:

```bash
OBSERVER_POSTGRES_TEST_DSN="postgres://..." go test ./internal/commanderhub/authstore -count=1 -race
```

Without PG:

```bash
go test ./internal/commanderhub/authstore -count=1 -race
```

Expected (either case): PASS (the new test is skipped without DSN; existing conformance still passes).

- [ ] **Step 6: Commit**

```bash
git add internal/commanderhub/authstore/schema_postgres.sql \
        internal/commanderhub/authstore/schema_postgres_rollback.sql \
        internal/commanderhub/authstore/postgres_test.go
git commit -m "feat(commanderhub/authstore): commander_daemons + commander_turns + commander_forward_nonces tables

Three Postgres tables for the issue-#49 shared registry. Idempotent
DDL appended to the existing MigratePostgres script. Down migration in a
separate manual rollback script. Conformance test asserts table
creation, the (user, workspace, short_id) PK on commander_daemons, and
the CHECK enum on commander_turns.state."
```

---

## Task 4: Rename registry → localRegistry; add removeIf; switch lookup key to short_id

**Files:**
- Modify: `multi-agent/internal/commanderhub/registry.go` (type rename + add `removeIf`; change `lookup`/`add` key semantics)
- Modify: `multi-agent/internal/commanderhub/registry_test.go` (extend with two new tests)
- Modify: `multi-agent/internal/commanderhub/hub.go:30,47` (field type + constructor call)
- Modify: existing `*_test.go` that construct `daemonConn{}` literals (add `shortID:` field, set to existing `id:` value for parity): `hub_test.go`, `proxy_test.go`, `http_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces:
  - Type `*localRegistry` (renamed from `*registry`).
  - Constructor `newLocalRegistry() *localRegistry` (renamed from `newRegistry`).
  - Method `(r *localRegistry).add(dc *daemonConn)` — same behavior, but indexes by `dc.shortID` (NOT `dc.id`).
  - Method `(r *localRegistry).lookup(o owner, shortID string) (*daemonConn, bool)` — keyed by shortID.
  - Method `(r *localRegistry).remove(o owner, shortID string)` — unconditional delete; kept for tests.
  - Method `(r *localRegistry).removeIf(o owner, shortID, connectionID string)` — NEW; only deletes when the stored conn's `id` matches `connectionID`.
  - Method `(r *localRegistry).daemons(o owner) []DaemonInfo` — unchanged.

This task does NOT change `Hub.ServeHTTP`'s admission path yet (that's Task 11). It only renames + extends `localRegistry` and fixes test fixtures.

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

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd multi-agent && go test ./internal/commanderhub -run 'TestLocalRegistry_RemoveIfMatchesConnectionID|TestLocalRegistry_LookupByShortID' -count=1`

Expected: compile failures (`newLocalRegistry`/`removeIf` undefined; `lookup` signature still expects daemonID).

- [ ] **Step 3: Replace the registry implementation**

Edit `internal/commanderhub/registry.go`. Replace the existing `registry` type + constructor + `add` + `remove` + `lookup` (lines 85-125) with:

```go
// localRegistry maps owner → shortID → *daemonConn. Keyed externally by
// stable short_id (so cluster-mode SQL rows align with in-memory state);
// removeIf uses the per-connection daemonConn.id as a connection_id
// generation guard so a same-pod fast reconnect's old WS goroutine
// doesn't delete the newer entry. All methods are goroutine-safe.
type localRegistry struct {
	mu    sync.Mutex
	conns map[owner]map[string]*daemonConn // owner -> shortID -> dc
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
// connectionID. Same-pod fast reconnect: old WS's deferred remove must
// not delete the new connection's entry.
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

- [ ] **Step 4: Update Hub.reg field + constructor**

Edit `internal/commanderhub/hub.go`. Find:

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

- [ ] **Step 5: Fix existing test fixtures**

Enumerate `daemonConn{}` literals in tests:

```bash
grep -nE '\bdaemonConn\{' internal/commanderhub/*_test.go
```

For each literal: if it sets `id:` and not `shortID:`, add `shortID:` with the SAME string value. Example transform:

Before:
```go
hub.reg.add(&daemonConn{id: "a1", owner: owner{"alice", "W1"}, displayName: "alice-mac", kind: "claude"})
```

After:
```go
hub.reg.add(&daemonConn{id: "a1", shortID: "a1", owner: owner{"alice", "W1"}, displayName: "alice-mac", kind: "claude"})
```

Tests that retrieve `hub.reg.daemons(o)[0].DaemonID` and feed it back to `lookup` still work because the same string serves as both `id` and `shortID` in the fixture.

If any test calls `hub.reg.add(dc)` and then `hub.reg.lookup(o, dc.id)` expecting the *id*-key lookup, the fixture's `shortID == id` makes it still pass. If any test reaches further and explicitly distinguishes id from shortID, update it to use `shortID` (none currently do — verify via grep).

- [ ] **Step 6: Re-run the whole package**

Run: `cd multi-agent && go vet ./internal/commanderhub/...`

Expected: clean.

Run: `cd multi-agent && go test ./internal/commanderhub -count=1 -race`

Expected: PASS (all existing tests + two new `TestLocalRegistry_*`).

- [ ] **Step 7: Commit**

```bash
git add internal/commanderhub/registry.go \
        internal/commanderhub/registry_test.go \
        internal/commanderhub/hub.go \
        internal/commanderhub/*_test.go
git commit -m "refactor(commanderhub): rename registry to localRegistry; key by short_id; add removeIf

In-memory registry renamed to localRegistry and keyed externally by stable
short_id, matching the upcoming shared-registry PK. Per-connection
daemonConn.id serves as the connection generation; new removeIf()
compares it before deleting so a same-pod fast reconnect can't evict
the newer entry. Existing test fixtures gain a shortID field set to the
existing id value for behavior parity."
```

---

## Task 5: Rename turnKey.daemonID → shortID; extract turnStateBackend interface

**Files:**
- Modify: `multi-agent/internal/commanderhub/turn_state.go` (rename field; extract interface; rename `*turnStateStore` → `*memTurnStore` with context-aware methods)
- Modify: `multi-agent/internal/commanderhub/turn_state_test.go` (update fixtures)
- Modify: `multi-agent/internal/commanderhub/http.go` (10 caller sites: `turnKey{owner:..., daemonID:..., sessionID:...}`)
- Modify: `multi-agent/internal/commanderhub/hub.go` (Hub.turns field type → `turnStateBackend`)
- Modify: `multi-agent/internal/commanderhub/tree.go` (`mergeCurrentTurnState`, `refreshSessionRows` — update key construction)

**Interfaces:**
- Consumes: nothing new.
- Produces:
  - `turnKey struct { owner owner; shortID string; sessionID string }` (was `daemonID`).
  - `turnStateBackend` interface (in `turn_state.go`):
    ```go
    type turnStateBackend interface {
        begin(ctx context.Context, key turnKey) (bool, error)
        set(ctx context.Context, key turnKey, state turnState) error
        finish(ctx context.Context, key turnKey, state turnState) error
        fail(ctx context.Context, key turnKey, msg string) error
        rekey(ctx context.Context, old, new turnKey) error
        get(ctx context.Context, key turnKey) (turnSnapshot, error)
    }
    ```
  - `*memTurnStore` (renamed from `*turnStateStore`) implements `turnStateBackend`.
  - All `Hub.turns.*` callers thread a `ctx` (`context.Background()` for now in `routeFrame` paths that don't have one; will be replaced with proper ctx in Task 12).

This task introduces the interface plumbing without changing observable behavior (in-memory store still backs everything; ctx threads through but is not consulted). The Postgres impl arrives in Task 6.

- [ ] **Step 1: Write the failing test for the interface**

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

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd multi-agent && go test ./internal/commanderhub -run 'TestMemTurnStoreSatisfiesBackend|TestTurnKey_FieldRenamed' -count=1`

Expected: compile failures (`newMemTurnStore`/`turnStateBackend`/`turnKey.shortID` undefined).

- [ ] **Step 3: Rename the field + extract the interface**

Edit `internal/commanderhub/turn_state.go`. Add `"context"` to imports.

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

Add the interface near the top (after the `turnState` consts):

```go
// turnStateBackend is the cross-pod-compatible abstraction over the
// in-memory turnStateStore. Single-pod mode uses *memTurnStore;
// shared mode swaps in *pgTurnStore (see turn_state_pg.go).
//
// Every method takes a ctx so PG-backed implementations can honor
// per-call timeouts. In-memory impl ignores ctx (operations are O(1)
// under a mutex).
type turnStateBackend interface {
	begin(ctx context.Context, key turnKey) (bool, error)
	set(ctx context.Context, key turnKey, state turnState) error
	finish(ctx context.Context, key turnKey, state turnState) error
	fail(ctx context.Context, key turnKey, msg string) error
	rekey(ctx context.Context, oldKey, newKey turnKey) error
	get(ctx context.Context, key turnKey) (turnSnapshot, error)
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

Update every method receiver from `*turnStateStore` to `*memTurnStore` AND make each method accept a `ctx context.Context` and return an `error`. The error is always `nil` for the in-memory impl. Concrete bodies remain essentially unchanged. Example:

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

func (s *memTurnStore) set(_ context.Context, key turnKey, state turnState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.m[key]
	cur.State = state
	cur.InFlight = state == turnStateQueued || state == turnStateAnswering
	cur.updatedAt = time.Now()
	s.m[key] = cur
	return nil
}

func (s *memTurnStore) finish(_ context.Context, key turnKey, state turnState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.m[key]
	cur.State = state
	cur.InFlight = false
	cur.AwaitingApproval = state == turnStateAwaitingApproval
	cur.updatedAt = time.Now()
	s.m[key] = cur
	s.pruneLocked()
	return nil
}

func (s *memTurnStore) fail(_ context.Context, key turnKey, msg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.m[key]
	cur.State = turnStateError
	cur.InFlight = false
	cur.Message = msg
	cur.updatedAt = time.Now()
	s.m[key] = cur
	s.pruneLocked()
	return nil
}

func (s *memTurnStore) rekey(_ context.Context, oldKey, newKey turnKey) error {
	if oldKey == newKey {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.m[oldKey]
	if !ok {
		return nil
	}
	delete(s.m, oldKey)
	if _, exists := s.m[newKey]; !exists {
		cur.updatedAt = time.Now()
		s.m[newKey] = cur
	}
	return nil
}

func (s *memTurnStore) get(_ context.Context, key turnKey) (turnSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if snap, ok := s.m[key]; ok {
		return snap, nil
	}
	return turnSnapshot{State: turnStateIdle}, nil
}
```

`pruneLocked` is unchanged.

- [ ] **Step 4: Update Hub.turns field type + constructor**

In `internal/commanderhub/hub.go`, find:

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

- [ ] **Step 5: Update all call sites in http.go and tree.go**

Grep first:

```bash
grep -nE 'turnKey\{|hub\.turns\.|ch\.hub\.turns\.|\.turns\.' internal/commanderhub/*.go
```

For every literal `turnKey{owner: ..., daemonID: ..., sessionID: ...}`, change `daemonID:` to `shortID:`. The string value passed should be `daemonID` for now (callers still get the per-connection id; the next task will switch this).

For every method call on `Hub.turns.{begin,set,finish,fail,rekey,get}`, add `ctx` as the first argument. In `http.go::ch.turn`, use `r.Context()`. In `tree.go::mergeCurrentTurnState` and `refreshSessionRows`, use the ctx that's already in scope. In `routeFrame` callers (none in this task; that's Task 11) you'd use `context.Background()` because routeFrame doesn't have a per-request ctx.

For `ch.turn` at `http.go:230`:

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

Apply analogous transforms to the 9 other call sites (`finish`, `fail`, `rekey`, `get`). In `tree.go::mergeCurrentTurnState`:

Before:
```go
snap := h.turns.get(turnKey{owner: o, daemonID: daemonID, sessionID: rows[i].SessionID})
```

After:
```go
snap, _ := h.turns.get(ctx, turnKey{owner: o, shortID: daemonID, sessionID: rows[i].SessionID})
```

(The `mergeCurrentTurnState` signature already takes `o owner, daemonID string, rows []SessionRow`; it now also needs a `ctx context.Context` parameter, which `cachedSessionRows` already has from its caller. Add `ctx` to the signature and all call sites.)

Similarly, `refreshSessionRows` constructs `turnKey{owner: o, daemonID: info.DaemonID, sessionID: sess.ID}`; change `daemonID:` → `shortID:` and update the `h.turns.get(...)` call to take ctx and `_, ` the error.

- [ ] **Step 6: Update turn_state_test.go fixtures**

For every `turnKey{daemonID: "..."}` in `turn_state_test.go`, change to `shortID:`. Method calls need ctx too:

Before:
```go
store := newTurnStateStore()
if !store.begin(key) { ... }
```

After:
```go
store := newMemTurnStore()
ok, err := store.begin(context.Background(), key)
require.NoError(t, err)
require.True(t, ok)
```

Add `"context"` import if needed.

- [ ] **Step 7: Run package tests**

Run: `cd multi-agent && go build ./internal/commanderhub/...`

Expected: PASS.

Run: `cd multi-agent && go test ./internal/commanderhub -count=1 -race`

Expected: PASS (all existing tests + the two new ones).

- [ ] **Step 8: Commit**

```bash
git add internal/commanderhub/turn_state.go \
        internal/commanderhub/turn_state_test.go \
        internal/commanderhub/hub.go \
        internal/commanderhub/http.go \
        internal/commanderhub/tree.go
git commit -m "refactor(commanderhub): turnKey.daemonID → shortID; turnStateBackend interface

In-memory turnStateStore becomes *memTurnStore implementing a new
turnStateBackend interface, with context-aware methods. turnKey field
renamed to match the upcoming PG-backed PK (user, workspace, short_id,
session). Pure refactor; no observable behavior change yet."
```

---
