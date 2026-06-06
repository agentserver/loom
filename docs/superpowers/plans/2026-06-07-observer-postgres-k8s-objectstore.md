# Observer PostgreSQL K8s Object Store Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move observer production deployment to PostgreSQL on Kubernetes with Helm, object-store-backed artifact/write bodies, and operations-key-gated telemetry.

**Architecture:** Keep observer API as the control plane while adding a PostgreSQL store implementation, S3-compatible object storage for binary bodies, and a Helm chart for Kubernetes deployment. Telemetry remains opt-in and `/api/events` requires both an agent token and an operations telemetry API key. Event ingest uses synchronous projection in the first version but separates append/projection methods so a later worker can take over.

**Tech Stack:** Go 1.26.2, `database/sql`, `modernc.org/sqlite`, `github.com/jackc/pgx/v5/stdlib`, `github.com/minio/minio-go/v7`, PostgreSQL, S3-compatible object storage, Helm, Gateway API `HTTPRoute`, Kubernetes context `k8s-nj-prod`.

---

## Source Spec

Implement the design in:

- `docs/superpowers/specs/2026-06-06-observer-postgres-k8s-objectstore-design.md`

Acceptance cluster is fixed:

```yaml
- cluster:
    server: https://k8s-prod.nj.cs.ac.cn
  name: k8s-nj-prod
```

All real Kubernetes deployment, migration, retention, integration, and smoke testing must use:

```bash
kubectl --context k8s-nj-prod ...
helm --kube-context k8s-nj-prod ...
```

Local `kind`, `minikube`, and `tests/prod_test` may be used only for chart rendering or developer smoke checks. They do not satisfy acceptance testing.

## Scope Check

This plan covers several subsystems, but they are coupled by the observer production deployment contract. Execute the tasks in order. Each task ends at a working commit boundary and should be reviewed before moving to the next task.

## File Structure

Create or modify these files:

- `multi-agent/internal/observerstore/types.go`: shared observer store types and `Store` interface.
- `multi-agent/internal/observerstore/helpers.go`: exported hash/time/id helpers shared by SQLite and PostgreSQL implementations.
- `multi-agent/internal/observerstore/store.go`: current SQLite implementation renamed to `SQLiteStore` while preserving `Open` and `New`.
- `multi-agent/internal/observerstore/schema.sql`: SQLite schema kept in place for low-churn local/test behavior.
- `multi-agent/internal/observerstore/postgres/store.go`: PostgreSQL implementation.
- `multi-agent/internal/observerstore/postgres/schema.sql`: PostgreSQL schema and indexes.
- `multi-agent/internal/observerstore/postgres/migrate.go`: idempotent schema migration.
- `multi-agent/internal/observerstore/postgres/store_test.go`: env-gated PostgreSQL integration tests.
- `multi-agent/internal/observerstore/telemetry_keys.go`: telemetry API key shared structs and helpers.
- `multi-agent/internal/observerweb/server.go`: telemetry double-auth and object-store-aware API routes.
- `multi-agent/internal/observerweb/rate_limit.go`: small in-memory telemetry rate limiter for first production version.
- `multi-agent/internal/observerweb/server_test.go`: API behavior tests.
- `multi-agent/internal/observerclient/client.go`: telemetry opt-in gate and telemetry key header.
- `multi-agent/internal/observerclient/client_test.go`: client telemetry tests.
- `multi-agent/internal/config/config.go`: slave/master observer config additions.
- `multi-agent/internal/driver/config.go`: driver observer config additions.
- `multi-agent/cmd/{driver-agent,master-agent,slave-agent}/main.go`: pass telemetry settings into observer client.
- `multi-agent/cmd/observer-server/main.go`: store selection, object-store config, telemetry config, `/healthz`, and `/readyz`.
- `multi-agent/cmd/observer-server/main_test.go`: config validation tests.
- `multi-agent/internal/objectstore/objectstore.go`: object-store interface.
- `multi-agent/internal/objectstore/memory.go`: deterministic test implementation.
- `multi-agent/internal/objectstore/s3.go`: S3-compatible presign implementation.
- `multi-agent/internal/userspace/blob.go`: blob store abstraction update for object storage.
- `multi-agent/internal/userspace/schema_postgres.sql`: PostgreSQL-compatible userspace metadata schema.
- `multi-agent/deploy/charts/observer/Chart.yaml`: Helm chart metadata.
- `multi-agent/deploy/charts/observer/values.yaml`: safe defaults.
- `multi-agent/deploy/charts/observer/values-production.example.yaml`: production example for `k8s-nj-prod`.
- `multi-agent/deploy/charts/observer/values-local-minio.yaml`: render/dev-only local values.
- `multi-agent/deploy/charts/observer/templates/*.yaml`: Deployment, Service, HTTPRoute, ConfigMap, Secret, migration Job, retention CronJob, ServiceAccount, Ingress.
- `multi-agent/tests/k8s_observer/README.md`: acceptance commands for `k8s-nj-prod`.
- `multi-agent/tests/k8s_observer/smoke.sh`: smoke script using `kubectl --context k8s-nj-prod` and `helm --kube-context k8s-nj-prod`.

## Task 1: Add Store/Object/Telemetry Configuration and Health Endpoints

**Files:**
- Modify: `multi-agent/cmd/observer-server/main.go`
- Modify: `multi-agent/cmd/observer-server/main_test.go`
- Modify: `multi-agent/cmd/observer-server/config.example.yaml`

- [ ] **Step 1: Write failing config tests**

Append these tests to `multi-agent/cmd/observer-server/main_test.go`:

```go
func TestLoadConfig_PostgresObjectStoreTelemetryAndGatewayDefaults(t *testing.T) {
	cfg := loadConfigFromString(t, `
listen_addr: ":8090"
api_keys:
  - id: ak-default
    key: ak_secret
store:
  driver: postgres
  postgres:
    dsn_env: OBSERVER_DATABASE_URL
object_store:
  driver: s3
  s3:
    endpoint: s3.internal
    bucket: observer-artifacts
    access_key_env: OBSERVER_S3_ACCESS_KEY
    secret_key_env: OBSERVER_S3_SECRET_KEY
telemetry:
  enabled: true
  api_keys:
    - id: ops-global
      key_env: OBSERVER_TELEMETRY_KEY_GLOBAL
      workspace_id: "*"
`)
	require.Equal(t, "postgres", cfg.Store.Driver)
	require.Equal(t, "OBSERVER_DATABASE_URL", cfg.Store.Postgres.DSNEnv)
	require.Equal(t, "s3", cfg.ObjectStore.Driver)
	require.Equal(t, "observer-artifacts", cfg.ObjectStore.S3.Bucket)
	require.True(t, cfg.Telemetry.Enabled)
	require.Equal(t, "ops-global", cfg.Telemetry.APIKeys[0].ID)
}

func TestLoadConfig_RejectsProductionSQLiteWithoutOverride(t *testing.T) {
	path := writeConfig(t, `
api_keys:
  - id: ak-default
    key: ak_secret
store:
  driver: sqlite
  sqlite:
    path: observer.db
production: true
`)
	_, err := loadConfig(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "sqlite store is not allowed in production")
}
```

- [ ] **Step 2: Run tests and verify they fail**

Run:

```bash
cd multi-agent
go test ./cmd/observer-server -run 'TestLoadConfig_(PostgresObjectStoreTelemetryAndGatewayDefaults|RejectsProductionSQLiteWithoutOverride)' -count=1
```

Expected: build fails because `Config.Store`, `Config.ObjectStore`, `Config.Telemetry`, and `Config.Production` do not exist.

- [ ] **Step 3: Add config structs and validation**

In `multi-agent/cmd/observer-server/main.go`, extend `Config`:

```go
type Config struct {
	ListenAddr string            `yaml:"listen_addr"`
	DBPath     string            `yaml:"db_path"`
	APIKeys    []APIKeyConfig    `yaml:"api_keys"`
	Store      StoreConfig       `yaml:"store"`
	ObjectStore ObjectStoreConfig `yaml:"object_store"`
	Telemetry  TelemetryConfig   `yaml:"telemetry"`
	Production bool              `yaml:"production"`
}

type StoreConfig struct {
	Driver   string         `yaml:"driver"`
	SQLite   SQLiteConfig   `yaml:"sqlite"`
	Postgres PostgresConfig `yaml:"postgres"`
	AllowSQLiteInProduction bool `yaml:"allow_sqlite_in_production"`
}

type SQLiteConfig struct {
	Path string `yaml:"path"`
}

type PostgresConfig struct {
	DSNEnv          string `yaml:"dsn_env"`
	MaxOpenConns    int    `yaml:"max_open_conns"`
	MaxIdleConns    int    `yaml:"max_idle_conns"`
	ConnMaxLifetime string `yaml:"conn_max_lifetime"`
}

type ObjectStoreConfig struct {
	Driver string   `yaml:"driver"`
	S3     S3Config `yaml:"s3"`
	Proxy  ProxyConfig `yaml:"proxy"`
}

type S3Config struct {
	Endpoint        string `yaml:"endpoint"`
	Region          string `yaml:"region"`
	Bucket          string `yaml:"bucket"`
	UseSSL          bool   `yaml:"use_ssl"`
	AccessKeyEnv    string `yaml:"access_key_env"`
	SecretKeyEnv    string `yaml:"secret_key_env"`
	PresignTTL       string `yaml:"presign_ttl"`
}

type ProxyConfig struct {
	Enabled     bool  `yaml:"enabled"`
	MaxBytes    int64 `yaml:"max_bytes"`
}

type TelemetryConfig struct {
	Enabled bool                    `yaml:"enabled"`
	APIKeys []TelemetryAPIKeyConfig `yaml:"api_keys"`
	RateLimit TelemetryRateLimitConfig `yaml:"rate_limit"`
	MaxBodyBytes int64               `yaml:"max_body_bytes"`
	RetentionDays int                `yaml:"retention_days"`
}

type TelemetryAPIKeyConfig struct {
	ID          string `yaml:"id"`
	KeyEnv      string `yaml:"key_env"`
	WorkspaceID string `yaml:"workspace_id"`
	Note        string `yaml:"note,omitempty"`
}

type TelemetryRateLimitConfig struct {
	PerMinute int `yaml:"per_minute"`
	Burst     int `yaml:"burst"`
}
```

In `loadConfig`, default the new fields after decoding:

```go
if cfg.Store.Driver == "" {
	cfg.Store.Driver = "sqlite"
}
if cfg.Store.SQLite.Path == "" {
	cfg.Store.SQLite.Path = cfg.DBPath
}
if cfg.Store.Postgres.MaxOpenConns == 0 {
	cfg.Store.Postgres.MaxOpenConns = 20
}
if cfg.Store.Postgres.MaxIdleConns == 0 {
	cfg.Store.Postgres.MaxIdleConns = 10
}
if cfg.Store.Postgres.ConnMaxLifetime == "" {
	cfg.Store.Postgres.ConnMaxLifetime = "30m"
}
if cfg.ObjectStore.Driver == "" {
	cfg.ObjectStore.Driver = "filesystem"
}
if cfg.ObjectStore.Proxy.MaxBytes == 0 {
	cfg.ObjectStore.Proxy.MaxBytes = 8 << 20
}
if cfg.Telemetry.RateLimit.PerMinute == 0 {
	cfg.Telemetry.RateLimit.PerMinute = 60
}
if cfg.Telemetry.RateLimit.Burst == 0 {
	cfg.Telemetry.RateLimit.Burst = 120
}
if cfg.Telemetry.MaxBodyBytes == 0 {
	cfg.Telemetry.MaxBodyBytes = 256 << 10
}
if cfg.Telemetry.RetentionDays == 0 {
	cfg.Telemetry.RetentionDays = 30
}
```

In `validateConfig`, add:

```go
switch cfg.Store.Driver {
case "sqlite":
	if cfg.Production && !cfg.Store.AllowSQLiteInProduction {
		return fmt.Errorf("sqlite store is not allowed in production")
	}
case "postgres":
	if cfg.Store.Postgres.DSNEnv == "" {
		return fmt.Errorf("store.postgres.dsn_env is required when store.driver is postgres")
	}
default:
	return fmt.Errorf("store.driver must be sqlite or postgres")
}

switch cfg.ObjectStore.Driver {
case "filesystem", "memory", "s3":
case "":
	cfg.ObjectStore.Driver = "filesystem"
default:
	return fmt.Errorf("object_store.driver must be filesystem, memory, or s3")
}
if cfg.ObjectStore.Driver == "s3" {
	if cfg.ObjectStore.S3.Endpoint == "" || cfg.ObjectStore.S3.Bucket == "" {
		return fmt.Errorf("object_store.s3.endpoint and bucket are required")
	}
	if cfg.ObjectStore.S3.AccessKeyEnv == "" || cfg.ObjectStore.S3.SecretKeyEnv == "" {
		return fmt.Errorf("object_store.s3 access_key_env and secret_key_env are required")
	}
}
if cfg.Telemetry.Enabled && len(cfg.Telemetry.APIKeys) == 0 {
	return fmt.Errorf("telemetry.api_keys must contain at least one key when telemetry.enabled is true")
}
```

- [ ] **Step 4: Add health endpoints**

Add `context` and `time` imports, then add health wrapper helpers in `multi-agent/cmd/observer-server/main.go`:

```go
func withHealth(app http.Handler, ready func(context.Context) error) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if ready != nil {
			if err := ready(ctx); err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready\n"))
	})
	mux.Handle("/", app)
	return mux
}
```

Add `newHTTPServer`:

```go
func newHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}
```

Replace:

```go
log.Fatal(http.ListenAndServe(cfg.ListenAddr, observerweb.New(st, usHandler)))
```

with:

```go
app := observerweb.New(st, usHandler)
srv := newHTTPServer(cfg.ListenAddr, withHealth(app, func(ctx context.Context) error {
	return st.DB().PingContext(ctx)
}))
log.Fatal(srv.ListenAndServe())
```

- [ ] **Step 5: Run tests**

Run:

```bash
cd multi-agent
go test ./cmd/observer-server -count=1
```

Expected: `ok github.com/yourorg/multi-agent/cmd/observer-server`.

- [ ] **Step 6: Commit**

```bash
git add multi-agent/cmd/observer-server/main.go multi-agent/cmd/observer-server/main_test.go multi-agent/cmd/observer-server/config.example.yaml
git commit -m "feat(observer): add production config shape"
```

## Task 2: Introduce Shared Store Interface and Preserve SQLite Behavior

**Files:**
- Create: `multi-agent/internal/observerstore/types.go`
- Create: `multi-agent/internal/observerstore/helpers.go`
- Modify: `multi-agent/internal/observerstore/store.go`
- Modify: `multi-agent/internal/observerweb/server.go`
- Modify: `multi-agent/cmd/observer-server/main.go`
- Test: `multi-agent/internal/observerstore/store_test.go`

- [ ] **Step 1: Write interface compile test**

Create `multi-agent/internal/observerstore/store_interface_test.go`:

```go
package observerstore

import "testing"

func TestSQLiteStoreImplementsStoreInterface(t *testing.T) {
	var _ Store = (*SQLiteStore)(nil)
}
```

This test should fail before implementation because `SQLiteStore` and the `Store` interface do not exist yet.

- [ ] **Step 2: Run failing test**

```bash
cd multi-agent
go test ./internal/observerstore -run TestSQLiteStoreImplementsStoreInterface -count=1
```

Expected: compile failure mentioning `undefined: SQLiteStore` or `Store is not an interface`.

- [ ] **Step 3: Create shared store interface**

Create `multi-agent/internal/observerstore/types.go`. Move the public data types from `store.go` into it: `Workspace`, `APIKeySpec`, `Agent`, `TaskView`, `SubtaskView`, `MCPServerView`, artifact/write constants, `ArtifactCreate`, `Artifact`, `ArtifactRequest`, `ArtifactContent`, `WriteCreate`, `Write`, `TaskContractRecord`, `ResourceSnapshotRecord`, `WorkspaceSummary`, and `TaskProgress`.

Add the interface in the same file. It must include the methods `observerweb`, `observer-server`, and tests need:

```go
package observerstore

import (
	"database/sql"
	"io"

	"github.com/yourorg/multi-agent/internal/observer"
)

type Store interface {
	Close() error
	DB() *sql.DB
	ReplaceAPIKeys(keys []APIKeySpec) error
	UpsertAPIKey(spec APIKeySpec) error
	LookupAPIKey(key string) (keyID string, ok bool, err error)
	UpsertWorkspaceLazy(id, name, apiKeyID string) error
	AgentBoundWorkspace(agentID string) (workspaceID string, found bool, err error)
	UpsertAgent(a Agent, token, apiKeyID string) error
	ValidateToken(token string) (Agent, bool, error)
	Ingest(ev observer.Event) error
	GetTaskProgress(workspaceID, taskID string) (TaskProgress, bool, error)
	CreateArtifact(ArtifactCreate) (Artifact, error)
	RequestArtifact(workspaceID, requesterAgentID, artifactID string) (ArtifactRequest, error)
	ListArtifactRequests(workspaceID, ownerAgentID string) ([]ArtifactRequest, error)
	StoreArtifactContent(workspaceID, ownerAgentID, artifactID, mime string, body io.Reader) error
	OpenArtifactContent(workspaceID, artifactID string) (ArtifactContent, error)
	CreateWrite(WriteCreate) (Write, error)
	StoreWriteContent(workspaceID, writerAgentID, writeID, mime string, body io.Reader) error
	UpdateWriteTaskID(workspaceID, ownerAgentID, writeID, taskID string) error
	ListCompletedWrites(workspaceID, ownerAgentID, taskID string) ([]Write, error)
	SaveTaskContract(TaskContractRecord) error
	GetTaskContract(workspaceID, taskID string) (TaskContractRecord, error)
	SaveResourceSnapshot(ResourceSnapshotRecord) error
	GetLatestResourceSnapshot(workspaceID string) (ResourceSnapshotRecord, error)
	ListWorkspaceSummaries() ([]WorkspaceSummary, error)
	EventCount() (int, error)
}
```

Rename the existing concrete type in `store.go`:

```go
type SQLiteStore struct {
	db *sql.DB
}
```

Change every method receiver in `store.go` from `func (s *Store)` to `func (s *SQLiteStore)`. Keep the package name `observerstore` and keep `schema.sql` where it is.

- [ ] **Step 4: Move SQLite implementation behind constructor**

Rename the concrete constructor:

```go
func OpenSQLite(path string) (*SQLiteStore, error)
```

Keep compatibility:

```go
func Open(path string) (*SQLiteStore, error) { return OpenSQLite(path) }
func New(path string) (*SQLiteStore, error) { return OpenSQLite(path) }
```

Do not create a SQLite subpackage in this implementation. The root package already contains the local/test SQLite store and the shared interface; PostgreSQL is added as a subpackage in Task 3.

- [ ] **Step 5: Update observerweb to depend on shared interface**

In `multi-agent/internal/observerweb/server.go`, replace the local `Store interface` with:

```go
type Store = observerstore.Store
```

Do not change handler logic in this task.

- [ ] **Step 6: Run focused tests**

```bash
cd multi-agent
go test ./internal/observerstore ./internal/observerweb ./cmd/observer-server -count=1
```

Expected: all pass.

- [ ] **Step 7: Commit**

```bash
git add multi-agent/internal/observerstore multi-agent/internal/observerweb/server.go multi-agent/cmd/observer-server/main.go
git commit -m "refactor(observerstore): define backend interface"
```

## Task 3: Add PostgreSQL Schema, Migration, and Store Constructor

**Files:**
- Create: `multi-agent/internal/observerstore/postgres/schema.sql`
- Create: `multi-agent/internal/observerstore/postgres/migrate.go`
- Create: `multi-agent/internal/observerstore/postgres/store.go`
- Create: `multi-agent/internal/observerstore/postgres/store_test.go`
- Modify: `multi-agent/go.mod`
- Modify: `multi-agent/go.sum`
- Modify: `multi-agent/cmd/observer-server/main.go`

- [ ] **Step 1: Add dependencies**

Run:

```bash
cd multi-agent
go get github.com/jackc/pgx/v5/stdlib
```

Expected: `go.mod` includes `github.com/jackc/pgx/v5`.

- [ ] **Step 2: Write env-gated migration test**

Create `multi-agent/internal/observerstore/postgres/store_test.go`:

```go
package postgres

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("OBSERVER_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("set OBSERVER_POSTGRES_TEST_DSN to run PostgreSQL integration tests")
	}
	return dsn
}

func TestMigrateCreatesCoreTables(t *testing.T) {
	st, err := Open(Config{DSN: testDSN(t)})
	require.NoError(t, err)
	defer st.Close()

	var exists bool
	err = st.db.QueryRow(`SELECT EXISTS (
		SELECT 1 FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name = 'events'
	)`).Scan(&exists)
	require.NoError(t, err)
	require.True(t, exists)
}
```

- [ ] **Step 3: Run test and verify skip or fail**

```bash
cd multi-agent
go test ./internal/observerstore/postgres -run TestMigrateCreatesCoreTables -count=1
```

Expected without env: `ok` with skip. Expected with env before implementation: compile failure because package does not exist.

- [ ] **Step 4: Add PostgreSQL schema**

Create `multi-agent/internal/observerstore/postgres/schema.sql` with this schema:

```sql
CREATE TABLE IF NOT EXISTS api_keys (
  id text PRIMARY KEY,
  key_hash text NOT NULL UNIQUE,
  note text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS workspaces (
  id text PRIMARY KEY,
  name text NOT NULL DEFAULT '',
  created_by_api_key_id text NOT NULL REFERENCES api_keys(id),
  created_at timestamptz NOT NULL,
  last_seen_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS agents (
  workspace_id text NOT NULL,
  id text NOT NULL,
  role text NOT NULL,
  display_name text NOT NULL,
  token_hash text NOT NULL,
  created_by_api_key_id text NOT NULL REFERENCES api_keys(id),
  PRIMARY KEY (workspace_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_agents_token_hash ON agents(token_hash);

CREATE TABLE IF NOT EXISTS telemetry_api_keys (
  id text PRIMARY KEY,
  key_hash text NOT NULL UNIQUE,
  note text NOT NULL DEFAULT '',
  workspace_id text NOT NULL DEFAULT '*',
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS events (
  event_id text PRIMARY KEY,
  ts timestamptz NOT NULL,
  workspace_id text NOT NULL,
  agent_id text NOT NULL,
  agent_role text NOT NULL,
  type text NOT NULL,
  task_id text NOT NULL DEFAULT '',
  parent_task_id text,
  subtask_id text,
  child_task_id text,
  summary text,
  subtask_summary text,
  status text,
  target_agent_id text,
  target_role text,
  mcp_server_name text,
  mcp_tools jsonb NOT NULL DEFAULT '[]'::jsonb,
  payload jsonb
);
CREATE INDEX IF NOT EXISTS idx_events_workspace_ts ON events(workspace_id, ts);
CREATE INDEX IF NOT EXISTS idx_events_workspace_task_ts ON events(workspace_id, task_id, ts);
CREATE INDEX IF NOT EXISTS idx_events_workspace_agent_ts ON events(workspace_id, agent_id, ts);
CREATE INDEX IF NOT EXISTS idx_events_workspace_type_ts ON events(workspace_id, type, ts);

CREATE TABLE IF NOT EXISTS tasks (
  workspace_id text NOT NULL,
  task_id text NOT NULL,
  driver_agent_id text,
  master_agent_id text,
  slave_agent_id text,
  summary text NOT NULL,
  status text NOT NULL,
  has_mcp boolean NOT NULL DEFAULT false,
  mcp_status text NOT NULL DEFAULT '',
  latest_progress text NOT NULL DEFAULT '',
  latest_progress_phase text NOT NULL DEFAULT '',
  latest_progress_at text NOT NULL DEFAULT '',
  final_output text NOT NULL DEFAULT '',
  is_final boolean NOT NULL DEFAULT false,
  output text,
  error text,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (workspace_id, task_id)
);
CREATE INDEX IF NOT EXISTS idx_tasks_workspace_updated ON tasks(workspace_id, updated_at);

CREATE TABLE IF NOT EXISTS subtasks (
  workspace_id text NOT NULL,
  parent_task_id text NOT NULL,
  subtask_id text NOT NULL,
  child_task_id text,
  master_agent_id text,
  slave_agent_id text,
  summary text NOT NULL,
  display_label text NOT NULL,
  status text NOT NULL,
  mcp_status text NOT NULL DEFAULT '',
  latest_progress text NOT NULL DEFAULT '',
  latest_progress_phase text NOT NULL DEFAULT '',
  latest_progress_at text NOT NULL DEFAULT '',
  output text,
  error text,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (workspace_id, parent_task_id, subtask_id)
);
CREATE INDEX IF NOT EXISTS idx_subtasks_child ON subtasks(workspace_id, child_task_id);

CREATE TABLE IF NOT EXISTS mcp_servers (
  workspace_id text NOT NULL,
  task_id text NOT NULL,
  parent_task_id text,
  slave_agent_id text NOT NULL,
  name text NOT NULL,
  tools jsonb NOT NULL DEFAULT '[]'::jsonb,
  tool_descriptors jsonb,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (workspace_id, task_id, name)
);

CREATE TABLE IF NOT EXISTS artifacts (
  workspace_id text NOT NULL,
  id text NOT NULL,
  owner_agent_id text NOT NULL,
  path text NOT NULL,
  kind text NOT NULL,
  mime text NOT NULL DEFAULT '',
  state text NOT NULL,
  bytes bigint NOT NULL DEFAULT 0,
  sha256 text NOT NULL DEFAULT '',
  object_key text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (workspace_id, id)
);
CREATE INDEX IF NOT EXISTS idx_artifacts_owner_state ON artifacts(workspace_id, owner_agent_id, state);

CREATE TABLE IF NOT EXISTS artifact_requests (
  workspace_id text NOT NULL,
  id text NOT NULL,
  artifact_id text NOT NULL,
  requester_agent_id text NOT NULL,
  owner_agent_id text NOT NULL,
  state text NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (workspace_id, id)
);
CREATE INDEX IF NOT EXISTS idx_artifact_requests_owner_state ON artifact_requests(workspace_id, owner_agent_id, state);

CREATE TABLE IF NOT EXISTS writes (
  workspace_id text NOT NULL,
  id text NOT NULL,
  owner_agent_id text NOT NULL,
  writer_agent_id text NOT NULL DEFAULT '',
  task_id text NOT NULL,
  path text NOT NULL,
  overwrite boolean NOT NULL DEFAULT false,
  state text NOT NULL,
  mime text NOT NULL DEFAULT '',
  bytes bigint NOT NULL DEFAULT 0,
  sha256 text NOT NULL DEFAULT '',
  object_key text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (workspace_id, id)
);
CREATE INDEX IF NOT EXISTS idx_writes_owner_task_state ON writes(workspace_id, owner_agent_id, task_id, state);

CREATE TABLE IF NOT EXISTS task_contracts (
  workspace_id text NOT NULL,
  task_id text NOT NULL,
  conversation_id text NOT NULL,
  owner_agent_id text NOT NULL,
  body jsonb NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (workspace_id, task_id)
);
CREATE INDEX IF NOT EXISTS idx_task_contracts_conversation ON task_contracts(workspace_id, conversation_id, updated_at);

CREATE TABLE IF NOT EXISTS resource_snapshots (
  workspace_id text NOT NULL,
  snapshot_id text NOT NULL,
  owner_agent_id text NOT NULL,
  body jsonb NOT NULL,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (workspace_id, snapshot_id)
);
CREATE INDEX IF NOT EXISTS idx_resource_snapshots_latest ON resource_snapshots(workspace_id, created_at);
```

- [ ] **Step 5: Add PostgreSQL store constructor**

Create `multi-agent/internal/observerstore/postgres/store.go`:

```go
package postgres

import (
	"database/sql"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type Config struct {
	DSN             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

type Store struct {
	db *sql.DB
}

func Open(cfg Config) (*Store, error) {
	db, err := sql.Open("pgx", cfg.DSN)
	if err != nil {
		return nil, err
	}
	if cfg.MaxOpenConns > 0 {
		db.SetMaxOpenConns(cfg.MaxOpenConns)
	}
	if cfg.MaxIdleConns > 0 {
		db.SetMaxIdleConns(cfg.MaxIdleConns)
	}
	if cfg.ConnMaxLifetime > 0 {
		db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }
```

Create `multi-agent/internal/observerstore/postgres/migrate.go`:

```go
package postgres

import (
	"database/sql"
	_ "embed"
)

//go:embed schema.sql
var schemaSQL string

func migrate(db *sql.DB) error {
	_, err := db.Exec(schemaSQL)
	return err
}
```

- [ ] **Step 6: Wire observer-server store selection**

In `multi-agent/cmd/observer-server/main.go`, add imports:

```go
observerstore "github.com/yourorg/multi-agent/internal/observerstore"
pgobs "github.com/yourorg/multi-agent/internal/observerstore/postgres"
```

Add helper:

```go
func openObserverStore(cfg *Config) (observerstore.Store, error) {
	switch cfg.Store.Driver {
	case "sqlite":
		return observerstore.Open(cfg.Store.SQLite.Path)
	case "postgres":
		dsn := os.Getenv(cfg.Store.Postgres.DSNEnv)
		if dsn == "" {
			return nil, fmt.Errorf("%s is required", cfg.Store.Postgres.DSNEnv)
		}
		lifetime, err := time.ParseDuration(cfg.Store.Postgres.ConnMaxLifetime)
		if err != nil {
			return nil, fmt.Errorf("store.postgres.conn_max_lifetime: %w", err)
		}
		return pgobs.Open(pgobs.Config{
			DSN: dsn,
			MaxOpenConns: cfg.Store.Postgres.MaxOpenConns,
			MaxIdleConns: cfg.Store.Postgres.MaxIdleConns,
			ConnMaxLifetime: lifetime,
		})
	default:
		return nil, fmt.Errorf("unsupported store driver %q", cfg.Store.Driver)
	}
}
```

- [ ] **Step 7: Run tests**

```bash
cd multi-agent
go test ./cmd/observer-server ./internal/observerstore/postgres -count=1
```

Expected: command passes; PostgreSQL test skips unless `OBSERVER_POSTGRES_TEST_DSN` is set.

- [ ] **Step 8: Commit**

```bash
git add multi-agent/go.mod multi-agent/go.sum multi-agent/cmd/observer-server multi-agent/internal/observerstore
git commit -m "feat(observerstore): add postgres backend skeleton"
```

## Task 4: Implement PostgreSQL Store Method Parity

**Files:**
- Modify: `multi-agent/internal/observerstore/postgres/store.go`
- Modify: `multi-agent/internal/observerstore/postgres/store_test.go`
- Reference: `multi-agent/internal/observerstore/store.go`

- [ ] **Step 1: Add parity tests for registration and event projection**

Append to `multi-agent/internal/observerstore/postgres/store_test.go`:

```go
func TestPostgresStoreRegistrationAndEventProjection(t *testing.T) {
	st, err := Open(Config{DSN: testDSN(t)})
	require.NoError(t, err)
	defer st.Close()

	require.NoError(t, st.ReplaceAPIKeys([]observerstore.APIKeySpec{
		{ID: "ak-test", Key: "secret"},
	}))
	keyID, ok, err := st.LookupAPIKey("secret")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "ak-test", keyID)

	require.NoError(t, st.UpsertWorkspaceLazy("ws1", "Workspace", keyID))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{
		WorkspaceID: "ws1", ID: "driver", Role: observer.RoleDriver, DisplayName: "driver",
	}, "token-driver", keyID))

	agent, ok, err := st.ValidateToken("token-driver")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "ws1", agent.WorkspaceID)

	err = st.Ingest(observer.Event{
		EventID: "ev1", TS: "2026-06-07T00:00:00Z",
		WorkspaceID: "ws1", AgentID: "driver", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: "task1", Summary: "do it", Status: "assigned",
	})
	require.NoError(t, err)

	progress, found, err := st.GetTaskProgress("ws1", "task1")
	require.NoError(t, err)
	require.True(t, found)
	require.False(t, progress.IsFinal)
}
```

Add imports:

```go
import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/observer"
	"github.com/yourorg/multi-agent/internal/observerstore"
)
```

- [ ] **Step 2: Run parity test and verify failure**

```bash
cd multi-agent
OBSERVER_POSTGRES_TEST_DSN="$OBSERVER_POSTGRES_TEST_DSN" go test ./internal/observerstore/postgres -run TestPostgresStoreRegistrationAndEventProjection -count=1
```

Expected with env: fail because methods are not implemented. Without env: skip.

- [ ] **Step 3: Port store methods**

Implement the PostgreSQL `Store` methods by porting logic from SQLite and changing SQL bind parameters:

- `?` becomes `$1`, `$2`, ...
- `INSERT OR IGNORE` becomes `INSERT ... ON CONFLICT DO NOTHING`
- SQLite integer booleans become PostgreSQL booleans
- `payload` and `mcp_tools` are JSONB strings passed as `[]byte` or `string`
- `RowsAffected` still works through `database/sql`

Start with these methods in order:

```go
func (s *Store) ReplaceAPIKeys(keys []observerstore.APIKeySpec) error
func (s *Store) LookupAPIKey(key string) (string, bool, error)
func (s *Store) UpsertWorkspaceLazy(id, name, apiKeyID string) error
func (s *Store) AgentBoundWorkspace(agentID string) (string, bool, error)
func (s *Store) UpsertAgent(a observerstore.Agent, token, apiKeyID string) error
func (s *Store) ValidateToken(token string) (observerstore.Agent, bool, error)
func (s *Store) Ingest(ev observer.Event) error
func (s *Store) GetTaskProgress(workspaceID, taskID string) (observerstore.TaskProgress, bool, error)
```

Create `multi-agent/internal/observerstore/helpers.go` and move these helper implementations out of the SQLite store file so PostgreSQL can use them:

```go
package observerstore

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"time"
)

func TokenHash(token string) string
func NowUTC() string
func GeneratedEventID() (string, error)
func PrefixedID(prefix string) (string, error)
func NullString(s string) any
```

Use the current private SQLite implementations as the body of these exported helpers:

```go
func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func NowUTC() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func GeneratedEventID() (string, error) {
	return PrefixedID("ev")
}

func PrefixedID(prefix string) (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	out := make([]byte, 18)
	for i := range out {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		out[i] = alphabet[n.Int64()]
	}
	return fmt.Sprintf("%s_%s", prefix, string(out)), nil
}

func NullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
```

In the SQLite store, replace calls to `tokenHash`, `nowUTC`, `generatedEventID`, `prefixedID`, and `nullString` with these exported helper names, then remove the duplicate private helper functions. Keep `applyAggregate` private in each concrete store because SQLite uses `?` bind markers and PostgreSQL uses `$1` bind markers.

- [ ] **Step 4: Port artifact/write metadata methods**

Implement:

```go
func (s *Store) CreateArtifact(observerstore.ArtifactCreate) (observerstore.Artifact, error)
func (s *Store) RequestArtifact(workspaceID, requesterAgentID, artifactID string) (observerstore.ArtifactRequest, error)
func (s *Store) ListArtifactRequests(workspaceID, ownerAgentID string) ([]observerstore.ArtifactRequest, error)
func (s *Store) CreateWrite(observerstore.WriteCreate) (observerstore.Write, error)
func (s *Store) UpdateWriteTaskID(workspaceID, ownerAgentID, writeID, taskID string) error
func (s *Store) ListCompletedWrites(workspaceID, ownerAgentID, taskID string) ([]observerstore.Write, error)
```

For this task, `StoreArtifactContent`, `OpenArtifactContent`, and `StoreWriteContent` return:

```go
return errors.New("observerstore/postgres: content storage requires object store")
```

The object-store task replaces these with metadata updates.

- [ ] **Step 5: Port contract, resource snapshot, and workspace summary methods**

Implement:

```go
func (s *Store) SaveTaskContract(observerstore.TaskContractRecord) error
func (s *Store) GetTaskContract(workspaceID, taskID string) (observerstore.TaskContractRecord, error)
func (s *Store) SaveResourceSnapshot(observerstore.ResourceSnapshotRecord) error
func (s *Store) GetLatestResourceSnapshot(workspaceID string) (observerstore.ResourceSnapshotRecord, error)
func (s *Store) ListWorkspaceSummaries() ([]observerstore.WorkspaceSummary, error)
func (s *Store) EventCount() (int, error)
func (s *Store) DB() *sql.DB
```

Use JSONB casts for JSON bodies:

```sql
INSERT INTO task_contracts(workspace_id, task_id, conversation_id, owner_agent_id, body, created_at, updated_at)
VALUES($1, $2, $3, $4, $5::jsonb, $6, $7)
```

`DB()` returns `s.db` so `cmd/observer-server` can attach userspace metadata tables before Task 7 replaces SQLite-specific blob storage.

- [ ] **Step 6: Run store tests**

```bash
cd multi-agent
go test ./internal/observerstore -count=1
OBSERVER_POSTGRES_TEST_DSN="$OBSERVER_POSTGRES_TEST_DSN" go test ./internal/observerstore/postgres -count=1
```

Expected: SQLite tests pass; PostgreSQL tests pass when env is set and skip when unset.

- [ ] **Step 7: Commit**

```bash
git add multi-agent/internal/observerstore
git commit -m "feat(observerstore): implement postgres metadata store"
```

## Task 5: Add Telemetry Opt-In, Operations Key Gate, and Rate Limit

**Files:**
- Modify: `multi-agent/internal/observerclient/client.go`
- Modify: `multi-agent/internal/observerclient/client_test.go`
- Modify: `multi-agent/internal/config/config.go`
- Modify: `multi-agent/internal/config/config_test.go`
- Modify: `multi-agent/internal/driver/config.go`
- Modify: `multi-agent/internal/driver/config_test.go`
- Modify: `multi-agent/cmd/{driver-agent,master-agent,slave-agent}/main.go`
- Modify: `multi-agent/internal/observerweb/server.go`
- Create: `multi-agent/internal/observerweb/rate_limit.go`
- Modify: `multi-agent/internal/observerweb/server_test.go`

- [ ] **Step 1: Write client failing test for default-off telemetry**

Add to `multi-agent/internal/observerclient/client_test.go`:

```go
func TestNewRegistersButTelemetryDefaultsDisabled(t *testing.T) {
	var eventCalls atomic.Int32
	var registerCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/agents/register":
			registerCalls.Add(1)
			_, _ = w.Write([]byte(`{"workspace_id":"ws1","token":"tk"}`))
		case "/api/events":
			eventCalls.Add(1)
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "observer.token")
	c, err := New(Config{
		Enabled: true, URL: srv.URL, WorkspaceID: "ws1", AgentID: "agent1",
		AgentRole: observer.RoleSlave, APIKey: "ak", TokenStatePath: path,
	})
	require.NoError(t, err)
	require.True(t, c.Enabled())
	c.Emit(observer.Event{TaskID: "t"})
	c.Close()
	require.Equal(t, int32(1), registerCalls.Load())
	require.Equal(t, int32(0), eventCalls.Load())
}
```

- [ ] **Step 2: Run failing test**

```bash
cd multi-agent
go test ./internal/observerclient -run TestNewRegistersButTelemetryDefaultsDisabled -count=1
```

Expected: fails because `/api/events` is still called.

- [ ] **Step 3: Add telemetry fields and header**

Update `observerclient.Config`:

```go
type Config struct {
	Enabled          bool
	TelemetryEnabled bool
	TelemetryAPIKey  string
	URL              string
	WorkspaceID      string
	WorkspaceName    string
	AgentID          string
	AgentRole        string
	APIKey           string
	TokenStatePath   string
}
```

Add `telemetryEnabled bool` to `Client`. Only create the queue and run goroutine when `cfg.Enabled && cfg.TelemetryEnabled`.

In `post`, set the operations key header:

```go
if c.cfg.TelemetryAPIKey != "" {
	req.Header.Set("X-Loom-Telemetry-Key", c.cfg.TelemetryAPIKey)
}
```

- [ ] **Step 4: Add config fields to driver/slave/master config**

In both `internal/config.Observer` and `internal/driver.Observer`, add:

```go
TelemetryEnabled bool   `yaml:"telemetry_enabled,omitempty"`
TelemetryAPIKey  string `yaml:"telemetry_api_key,omitempty"`
```

Validation rule:

```go
if c.Observer.TelemetryEnabled && c.Observer.TelemetryAPIKey == "" {
	return nil, fmt.Errorf("observer.telemetry_api_key is required when observer.telemetry_enabled is true")
}
```

Pass both fields into `observerclient.New` from `cmd/driver-agent/main.go`, `cmd/master-agent/main.go`, and `cmd/slave-agent/main.go`.

- [ ] **Step 5: Write server-side telemetry key tests**

Add to `multi-agent/internal/observerweb/server_test.go`:

```go
func TestPostEventRequiresTelemetryKey(t *testing.T) {
	h, st := newTestHandler(t)
	seedAPIKey(t, st, "ak-default", "ak_secret")
	registerBody := postRegister(t, h, "ak_secret",
		`{"agent_id":"agent1","role":"slave","display_name":"Agent 1","workspace_id":"ws1"}`,
		http.StatusOK)
	token := extractToken(t, registerBody)
	body := []byte(`{"workspace_id":"ws1","agent_id":"agent1","agent_role":"slave","type":"slave_task_started","task_id":"t1"}`)

	req := httptest.NewRequest(http.MethodPost, "/api/events", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusForbidden, rr.Code)
}
```

Add this passing-key test:

```go
func TestPostEventAcceptsValidTelemetryKey(t *testing.T) {
	h, st := newTestHandler(t)
	seedAPIKey(t, st, "ak-default", "ak_secret")
	require.NoError(t, st.ReplaceTelemetryAPIKeys([]observerstore.TelemetryAPIKeySpec{
		{ID: "ops", Key: "ops-secret", WorkspaceID: "*", Enabled: true},
	}))
	registerBody := postRegister(t, h, "ak_secret",
		`{"agent_id":"agent1","role":"slave","display_name":"Agent 1","workspace_id":"ws1"}`,
		http.StatusOK)
	token := extractToken(t, registerBody)
	body := []byte(`{"workspace_id":"ws1","agent_id":"agent1","agent_role":"slave","type":"slave_task_started","task_id":"t1"}`)

	req := httptest.NewRequest(http.MethodPost, "/api/events", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Loom-Telemetry-Key", "ops-secret")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusAccepted, rr.Code, rr.Body.String())
}
```

Add this rate-limit test:

```go
func TestPostEventRateLimit(t *testing.T) {
	_, st := newTestHandler(t)
	require.NoError(t, st.ReplaceTelemetryAPIKeys([]observerstore.TelemetryAPIKeySpec{
		{ID: "ops", Key: "ops-secret", WorkspaceID: "*", Enabled: true},
	}))
	h := NewWithOptions(st, nil, Options{
		TelemetryRateLimit: RateLimitConfig{PerMinute: 1, Burst: 1},
	})
	seedAPIKey(t, st, "ak-default", "ak_secret")
	registerBody := postRegister(t, h, "ak_secret",
		`{"agent_id":"agent1","role":"slave","display_name":"Agent 1","workspace_id":"ws1"}`,
		http.StatusOK)
	token := extractToken(t, registerBody)
	body := []byte(`{"workspace_id":"ws1","agent_id":"agent1","agent_role":"slave","type":"slave_task_started","task_id":"t1"}`)

	for i, want := range []int{http.StatusAccepted, http.StatusTooManyRequests} {
		req := httptest.NewRequest(http.MethodPost, "/api/events", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Loom-Telemetry-Key", "ops-secret")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		require.Equal(t, want, rr.Code, "attempt %d body=%s", i+1, rr.Body.String())
	}
}
```

- [ ] **Step 6: Implement telemetry key store and gate**

Add to `observerstore.Store` interface:

```go
ReplaceTelemetryAPIKeys(keys []TelemetryAPIKeySpec) error
LookupTelemetryAPIKey(key, workspaceID string) (keyID string, ok bool, err error)
```

Add shared type:

Create `multi-agent/internal/observerstore/telemetry_keys.go`:

```go
package observerstore

type TelemetryAPIKeySpec struct {
	ID          string
	Key         string
	WorkspaceID string
	Note        string
	Enabled     bool
}
```

In the SQLite schema/`ensureColumns`, add:

```sql
CREATE TABLE IF NOT EXISTS telemetry_api_keys (
  id TEXT PRIMARY KEY,
  key_hash TEXT NOT NULL UNIQUE,
  note TEXT NOT NULL DEFAULT '',
  workspace_id TEXT NOT NULL DEFAULT '*',
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL
);
```

Implement `ReplaceTelemetryAPIKeys` and `LookupTelemetryAPIKey` in the SQLite store using `TokenHash`. `LookupTelemetryAPIKey` must accept a row only when `enabled=1` and `(workspace_id='*' OR workspace_id=?)`.

Implement the same two methods in `internal/observerstore/postgres/store.go` using the PostgreSQL `telemetry_api_keys` table from Task 3. PostgreSQL lookup uses:

```sql
SELECT id
  FROM telemetry_api_keys
 WHERE key_hash=$1
   AND enabled=true
   AND (workspace_id='*' OR workspace_id=$2)
 LIMIT 1
```

Add observerweb options:

```go
type RateLimitConfig struct {
	PerMinute int
	Burst     int
}

type Options struct {
	TelemetryRateLimit RateLimitConfig
	MaxEventBodyBytes  int64
}

func NewWithOptions(s Store, usHandler *userspace.Handler, opts Options) http.Handler
```

Update `handler` to store the configured limits:

```go
type handler struct {
	s                 Store
	telemetryLimiter  *telemetryLimiter
	maxEventBodyBytes int64
}
```

Create `multi-agent/internal/observerweb/rate_limit.go`:

```go
package observerweb

import (
	"sync"
	"time"
)

type telemetryLimiter struct {
	mu        sync.Mutex
	perMinute int
	burst     int
	buckets   map[string]telemetryBucket
}

type telemetryBucket struct {
	windowStart time.Time
	count       int
}

func newTelemetryLimiter(perMinute, burst int) *telemetryLimiter {
	if perMinute <= 0 {
		perMinute = 60
	}
	if burst <= 0 {
		burst = perMinute
	}
	return &telemetryLimiter{
		perMinute: perMinute,
		burst:     burst,
		buckets:   map[string]telemetryBucket{},
	}
}

func (l *telemetryLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.buckets[key]
	if b.windowStart.IsZero() || now.Sub(b.windowStart) >= time.Minute {
		b = telemetryBucket{windowStart: now}
	}
	limit := l.perMinute
	if l.burst > limit {
		limit = l.burst
	}
	if b.count >= limit {
		l.buckets[key] = b
		return false
	}
	b.count++
	l.buckets[key] = b
	return true
}
```

`NewWithOptions` must default zero values before mounting routes:

```go
func mountRoutes(mux *http.ServeMux, h *handler, usHandler *userspace.Handler) {
	mux.HandleFunc("/api/events", h.postEvent)
	mux.HandleFunc("/api/agents/register", h.register)
	mux.HandleFunc("/api/tasks/", h.taskRouter)
	mux.HandleFunc("/api/artifacts", h.artifacts)
	mux.HandleFunc("/api/artifacts/", h.artifactRouter)
	mux.HandleFunc("/api/artifact-requests", h.artifactRequests)
	mux.HandleFunc("/api/write-tokens", h.writeTokens)
	mux.HandleFunc("/api/writes", h.writes)
	mux.HandleFunc("/api/writes/", h.writeRouter)
	mux.HandleFunc("/api/task-contracts", h.taskContracts)
	mux.HandleFunc("/api/task-contracts/", h.taskContractByID)
	mux.HandleFunc("/api/resource-snapshots", h.resourceSnapshots)
	mux.HandleFunc("/api/resource-snapshots/latest", h.latestResourceSnapshot)
	mux.HandleFunc("/api/workspaces", h.guardWebToken(h.listWorkspaces))
	if usHandler != nil {
		userspace.MountRoutes(mux, usHandler)
	}
}

func NewWithOptions(s Store, usHandler *userspace.Handler, opts Options) http.Handler {
	if opts.TelemetryRateLimit.PerMinute == 0 {
		opts.TelemetryRateLimit.PerMinute = 60
	}
	if opts.TelemetryRateLimit.Burst == 0 {
		opts.TelemetryRateLimit.Burst = 120
	}
	if opts.MaxEventBodyBytes == 0 {
		opts.MaxEventBodyBytes = 256 << 10
	}
	h := &handler{
		s: s,
		telemetryLimiter: newTelemetryLimiter(opts.TelemetryRateLimit.PerMinute, opts.TelemetryRateLimit.Burst),
		maxEventBodyBytes: opts.MaxEventBodyBytes,
	}
	mux := http.NewServeMux()
	mountRoutes(mux, h, usHandler)
	return mux
}
```

Keep `New` as:

```go
func New(s Store, usHandler *userspace.Handler) http.Handler {
	return NewWithOptions(s, usHandler, Options{
		TelemetryRateLimit: RateLimitConfig{PerMinute: 60, Burst: 120},
		MaxEventBodyBytes: 256 << 10,
	})
}
```

In `observerweb.postEvent`, after validating agent token and before decoding the body, require the telemetry key header:

```go
telemetryKey := strings.TrimSpace(r.Header.Get("X-Loom-Telemetry-Key"))
if telemetryKey == "" {
	http.Error(w, "missing telemetry api key", http.StatusForbidden)
	return
}
```

After decoding and identity match, call:

```go
telemetryKeyID, ok, err := h.s.LookupTelemetryAPIKey(telemetryKey, agent.WorkspaceID)
if err != nil {
	http.Error(w, err.Error(), http.StatusInternalServerError)
	return
}
if !ok {
	http.Error(w, "invalid telemetry api key", http.StatusForbidden)
	return
}
```

Store the returned telemetry key id and apply the in-memory limiter keyed by `agent.WorkspaceID + "\x00" + agent.ID + "\x00" + telemetryKeyID`:

```go
rateKey := agent.WorkspaceID + "\x00" + agent.ID + "\x00" + telemetryKeyID
if h.telemetryLimiter != nil && !h.telemetryLimiter.allow(rateKey, time.Now()) {
	http.Error(w, "telemetry rate limit exceeded", http.StatusTooManyRequests)
	return
}
```

In `cmd/observer-server/main.go`, after `ReplaceAPIKeys`, load configured ops telemetry keys:

```go
if cfg.Telemetry.Enabled {
	keys := make([]observerstore.TelemetryAPIKeySpec, 0, len(cfg.Telemetry.APIKeys))
	for _, k := range cfg.Telemetry.APIKeys {
		value := os.Getenv(k.KeyEnv)
		if value == "" {
			log.Fatalf("%s is required", k.KeyEnv)
		}
		keys = append(keys, observerstore.TelemetryAPIKeySpec{
			ID: k.ID, Key: value, WorkspaceID: k.WorkspaceID, Note: k.Note, Enabled: true,
		})
	}
	if err := st.ReplaceTelemetryAPIKeys(keys); err != nil {
		log.Fatal(err)
	}
}
```

Replace the handler construction added in Task 1 with the configured options:

```go
app := observerweb.NewWithOptions(st, usHandler, observerweb.Options{
	TelemetryRateLimit: observerweb.RateLimitConfig{
		PerMinute: cfg.Telemetry.RateLimit.PerMinute,
		Burst:     cfg.Telemetry.RateLimit.Burst,
	},
	MaxEventBodyBytes: cfg.Telemetry.MaxBodyBytes,
})
srv := newHTTPServer(cfg.ListenAddr, withHealth(app, func(ctx context.Context) error {
	return st.DB().PingContext(ctx)
}))
```

- [ ] **Step 7: Run tests**

```bash
cd multi-agent
go test ./internal/observerclient ./internal/config ./internal/driver ./internal/observerweb ./internal/observerstore -count=1
```

Expected: all pass.

- [ ] **Step 8: Commit**

```bash
git add multi-agent/internal/observerclient multi-agent/internal/config multi-agent/internal/driver multi-agent/cmd/driver-agent multi-agent/cmd/master-agent multi-agent/cmd/slave-agent multi-agent/internal/observerweb multi-agent/internal/observerstore
git commit -m "feat(observer): gate telemetry with ops key"
```

## Task 6: Add Object Store Abstraction and Presigned Artifact/Write Flow

**Files:**
- Create: `multi-agent/internal/objectstore/objectstore.go`
- Create: `multi-agent/internal/objectstore/memory.go`
- Create: `multi-agent/internal/objectstore/s3.go`
- Create: `multi-agent/internal/objectstore/objectstore_test.go`
- Modify: `multi-agent/internal/observerstore/types.go`
- Modify: `multi-agent/internal/observerweb/server.go`
- Modify: `multi-agent/internal/observerweb/server_test.go`
- Modify: `multi-agent/internal/observerstore/postgres/store.go`
- Modify: `multi-agent/internal/observerstore/postgres/schema.sql`
- Modify: `multi-agent/go.mod`
- Modify: `multi-agent/go.sum`

- [ ] **Step 1: Add dependency**

```bash
cd multi-agent
go get github.com/minio/minio-go/v7
```

- [ ] **Step 2: Add object store interface and memory implementation**

Create `multi-agent/internal/objectstore/objectstore.go`:

```go
package objectstore

import (
	"context"
	"io"
	"time"
)

type ObjectInfo struct {
	Bytes  int64
	SHA256 string
}

type Store interface {
	PutPresignedURL(ctx context.Context, key, mime string, expires time.Duration) (string, error)
	GetPresignedURL(ctx context.Context, key string, expires time.Duration) (string, error)
	Put(ctx context.Context, key, mime string, body io.Reader) (ObjectInfo, error)
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
}

func ArtifactKey(workspaceID, artifactID string) string {
	return "workspaces/" + workspaceID + "/artifacts/" + artifactID
}

func WriteKey(workspaceID, writeID string) string {
	return "workspaces/" + workspaceID + "/writes/" + writeID
}
```

Create `multi-agent/internal/objectstore/memory.go`:

```go
package objectstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/url"
	"sync"
	"time"
)

type Memory struct {
	mu   sync.Mutex
	data map[string][]byte
}

func NewMemory() *Memory {
	return &Memory{data: map[string][]byte{}}
}

func (*Memory) PutPresignedURL(ctx context.Context, key, mime string, expires time.Duration) (string, error) {
	return "memory://put/" + url.PathEscape(key), nil
}

func (*Memory) GetPresignedURL(ctx context.Context, key string, expires time.Duration) (string, error) {
	return "memory://get/" + url.PathEscape(key), nil
}

func (m *Memory) Put(ctx context.Context, key, mime string, body io.Reader) (ObjectInfo, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return ObjectInfo{}, err
	}
	sum := sha256.Sum256(data)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data == nil {
		m.data = map[string][]byte{}
	}
	m.data[key] = append([]byte(nil), data...)
	return ObjectInfo{Bytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}, nil
}

func (m *Memory) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data := append([]byte(nil), m.data[key]...)
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (m *Memory) Delete(ctx context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}
```

- [ ] **Step 3: Write HTTP API tests**

In `multi-agent/internal/observerweb/server_test.go`, add:

```go
func TestCreateArtifactReturnsPresignedPutURL(t *testing.T) {
	_, st := newTestHandler(t)
	h := NewWithOptions(st, nil, Options{Objects: objectstore.NewMemory()})
	seedAPIKey(t, st, "ak-default", "ak_secret")
	registerBody := postRegister(t, h, "ak_secret",
		`{"agent_id":"driver","role":"driver","display_name":"Driver","workspace_id":"ws1"}`,
		http.StatusOK)
	token := extractToken(t, registerBody)

	req := httptest.NewRequest(http.MethodPost, "/api/artifacts", strings.NewReader(`{"path":"/tmp/out.txt","kind":"file"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), `"put_url":"memory://put/`)
}
```

- [ ] **Step 4: Implement object-store-aware responses**

Extend the `observerweb.Options` type from Task 5:

```go
type Options struct {
	TelemetryRateLimit RateLimitConfig
	MaxEventBodyBytes  int64
	Objects            objectstore.Store
}
```

Extend the existing `handler` and `NewWithOptions` from Task 5 instead of replacing them:

```go
type handler struct {
	s                 Store
	telemetryLimiter  *telemetryLimiter
	maxEventBodyBytes int64
	objects           objectstore.Store
}
```

Keep the `mountRoutes` helper introduced in Task 5. Update the existing `NewWithOptions` handler literal so it carries `opts.Objects`:

```go
func NewWithOptions(s Store, usHandler *userspace.Handler, opts Options) http.Handler {
	if opts.TelemetryRateLimit.PerMinute == 0 {
		opts.TelemetryRateLimit.PerMinute = 60
	}
	if opts.TelemetryRateLimit.Burst == 0 {
		opts.TelemetryRateLimit.Burst = 120
	}
	if opts.MaxEventBodyBytes == 0 {
		opts.MaxEventBodyBytes = 256 << 10
	}
	h := &handler{
		s: s,
		telemetryLimiter: newTelemetryLimiter(opts.TelemetryRateLimit.PerMinute, opts.TelemetryRateLimit.Burst),
		maxEventBodyBytes: opts.MaxEventBodyBytes,
		objects: opts.Objects,
	}
	mux := http.NewServeMux()
	mountRoutes(mux, h, usHandler)
	return mux
}
```

When `objects != nil`, `POST /api/artifacts` returns:

```json
{"artifact_id":"...","state":"registered","put_url":"...","object_key":"..."}
```

When `objects == nil`, keep existing proxy behavior for SQLite tests and local mode.

- [ ] **Step 5: Add metadata update methods**

Add object-key fields to shared types in `multi-agent/internal/observerstore/types.go`:

```go
type Artifact struct {
	ID           string `json:"artifact_id"`
	WorkspaceID  string `json:"workspace_id,omitempty"`
	OwnerAgentID string `json:"owner_agent_id,omitempty"`
	Path         string `json:"path,omitempty"`
	Kind         string `json:"kind,omitempty"`
	MIME         string `json:"mime,omitempty"`
	State        string `json:"state"`
	Bytes        int64  `json:"bytes,omitempty"`
	SHA256       string `json:"sha256,omitempty"`
	ObjectKey    string `json:"object_key,omitempty"`
}

type Write struct {
	ID            string `json:"write_id"`
	WorkspaceID   string `json:"workspace_id,omitempty"`
	OwnerAgentID  string `json:"owner_agent_id,omitempty"`
	WriterAgentID string `json:"writer_agent_id,omitempty"`
	TaskID        string `json:"task_id,omitempty"`
	Path          string `json:"path"`
	Overwrite     bool   `json:"overwrite"`
	State         string `json:"state"`
	MIME          string `json:"mime,omitempty"`
	Bytes         int64  `json:"bytes,omitempty"`
	SHA256        string `json:"sha256,omitempty"`
	ObjectKey     string `json:"object_key,omitempty"`
	Content       []byte `json:"-"`
}
```

Add to `observerstore.Store`:

```go
MarkArtifactAvailable(workspaceID, ownerAgentID, artifactID, mime, sha256, objectKey string, bytes int64) error
MarkWriteCompleted(workspaceID, writerAgentID, writeID, mime, sha256, objectKey string, bytes int64) error
```

Implement in PostgreSQL by updating metadata only. Keep SQLite compatibility by setting `object_key` if the column exists and leaving `content` empty.

- [ ] **Step 6: Run tests**

```bash
cd multi-agent
go test ./internal/objectstore ./internal/observerweb ./internal/observerstore ./internal/observerstore/postgres -count=1
```

Expected: object store and observerweb tests pass; Postgres integration skips unless env is set.

- [ ] **Step 7: Commit**

```bash
git add multi-agent/go.mod multi-agent/go.sum multi-agent/internal/objectstore multi-agent/internal/observerweb multi-agent/internal/observerstore
git commit -m "feat(observer): add object store artifact flow"
```

## Task 7: Make Userspace Blob Storage Object-Store Compatible

**Files:**
- Modify: `multi-agent/internal/userspace/blob.go`
- Modify: `multi-agent/internal/userspace/blob_test.go`
- Create: `multi-agent/internal/userspace/schema_postgres.sql`
- Modify: `multi-agent/internal/userspace/migrate.go`
- Modify: `multi-agent/cmd/observer-server/main.go`

- [ ] **Step 1: Add userspace blob interface test**

In `multi-agent/internal/userspace/blob_test.go`, add:

```go
func TestBlobStoreUsesObjectKeyWhenConfigured(t *testing.T) {
	db := newTestDB(t)
	require.NoError(t, Migrate(db))
	objects := objectstore.NewMemory()
	b, err := NewObjectBlobStore(db, objects)
	require.NoError(t, err)
	sha, err := b.Put([]byte("hello"))
	require.NoError(t, err)
	require.Equal(t, ComputeSHA256Hex([]byte("hello")), sha)
}
```

- [ ] **Step 2: Run failing test**

```bash
cd multi-agent
go test ./internal/userspace -run TestBlobStoreUsesObjectKeyWhenConfigured -count=1
```

Expected: compile failure because `NewObjectBlobStore` does not exist.

- [ ] **Step 3: Add object-backed blob store**

Add to `multi-agent/internal/userspace/blob.go`:

```go
type ObjectBlobStore struct {
	db *sql.DB
	objects objectstore.Store
}

func NewObjectBlobStore(db *sql.DB, objects objectstore.Store) (*ObjectBlobStore, error) {
	if objects == nil {
		return nil, errors.New("userspace: object store required")
	}
	return &ObjectBlobStore{db: db, objects: objects}, nil
}
```

For `Put`, keep refcount metadata in SQL and use object keys:

```go
key := "workspaces/userspace/blobs/" + hexsum
_, err := b.objects.Put(context.Background(), key, "application/gzip", bytes.NewReader(content))
if err != nil {
	return "", err
}
```

Do not store tarball bytes in PostgreSQL.

- [ ] **Step 4: Add PostgreSQL userspace schema**

Create `multi-agent/internal/userspace/schema_postgres.sql` without SQLite FTS5:

```sql
CREATE TABLE IF NOT EXISTS userspace_packages (
  slug text PRIMARY KEY,
  kind text NOT NULL,
  description text NOT NULL DEFAULT '',
  tags_json jsonb NOT NULL DEFAULT '[]'::jsonb,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS userspace_blobs (
  sha256 text PRIMARY KEY,
  size_bytes bigint NOT NULL,
  object_key text NOT NULL,
  refcount bigint NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS userspace_package_versions (
  slug text NOT NULL REFERENCES userspace_packages(slug),
  version text NOT NULL,
  created_in_workspace text NOT NULL,
  created_by_agent_id text NOT NULL,
  manifest_json jsonb NOT NULL,
  spec_json jsonb,
  card_md text NOT NULL,
  tarball_sha256 text NOT NULL,
  blob_sha256 text NOT NULL REFERENCES userspace_blobs(sha256),
  status text NOT NULL DEFAULT 'ready',
  created_at timestamptz NOT NULL,
  PRIMARY KEY (slug, version)
);
CREATE INDEX IF NOT EXISTS idx_uspv_workspace ON userspace_package_versions(created_in_workspace);

CREATE TABLE IF NOT EXISTS userspace_workspace_installations (
  workspace_id text NOT NULL REFERENCES workspaces(id),
  slug text NOT NULL REFERENCES userspace_packages(slug),
  installed_version text NOT NULL,
  installed_at timestamptz NOT NULL,
  installed_by_agent_id text NOT NULL,
  PRIMARY KEY (workspace_id, slug),
  FOREIGN KEY (slug, installed_version) REFERENCES userspace_package_versions(slug, version)
);

CREATE INDEX IF NOT EXISTS idx_userspace_packages_search
ON userspace_packages USING gin (to_tsvector('simple', slug || ' ' || description || ' ' || tags_json::text));
```

- [ ] **Step 5: Run userspace tests**

```bash
cd multi-agent
go test ./internal/userspace -count=1
```

Expected: pass.

- [ ] **Step 6: Commit**

```bash
git add multi-agent/internal/userspace multi-agent/cmd/observer-server/main.go
git commit -m "feat(userspace): support object-backed blobs"
```

## Task 8: Add Helm Chart with Gateway API HTTPRoute

**Files:**
- Create: `multi-agent/deploy/charts/observer/Chart.yaml`
- Create: `multi-agent/deploy/charts/observer/values.yaml`
- Create: `multi-agent/deploy/charts/observer/values-local-minio.yaml`
- Create: `multi-agent/deploy/charts/observer/values-production.example.yaml`
- Create: `multi-agent/deploy/charts/observer/templates/_helpers.tpl`
- Create: `multi-agent/deploy/charts/observer/templates/deployment.yaml`
- Create: `multi-agent/deploy/charts/observer/templates/service.yaml`
- Create: `multi-agent/deploy/charts/observer/templates/httproute.yaml`
- Create: `multi-agent/deploy/charts/observer/templates/configmap.yaml`
- Create: `multi-agent/deploy/charts/observer/templates/secret.yaml`
- Create: `multi-agent/deploy/charts/observer/templates/migration-job.yaml`
- Create: `multi-agent/deploy/charts/observer/templates/retention-cronjob.yaml`
- Create: `multi-agent/deploy/charts/observer/templates/serviceaccount.yaml`
- Create: `multi-agent/deploy/charts/observer/templates/ingress.yaml`
- Create: `multi-agent/deploy/charts/observer/templates/notes.txt`
- Create: `multi-agent/deploy/charts/observer/tests/chart_test.sh`

- [ ] **Step 1: Write Helm render test**

Create `multi-agent/deploy/charts/observer/tests/chart_test.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail
CHART_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
rendered="$(helm template observer-test "$CHART_DIR" \
  --set gateway.enabled=true \
  --set gateway.host=observer.example.com \
  --set gateway.parentRefs[0].name=cilium-gateway \
  --set gateway.parentRefs[0].namespace=cilium-gateway)"
grep -q 'kind: HTTPRoute' <<<"$rendered"
grep -q 'apiVersion: gateway.networking.k8s.io/v1' <<<"$rendered"
grep -q 'observer.example.com' <<<"$rendered"
grep -q 'kind: Deployment' <<<"$rendered"
grep -q 'kind: Job' <<<"$rendered"
grep -q 'kind: CronJob' <<<"$rendered"
```

Make executable after creation:

```bash
chmod +x multi-agent/deploy/charts/observer/tests/chart_test.sh
```

- [ ] **Step 2: Run failing render test**

```bash
cd multi-agent
./deploy/charts/observer/tests/chart_test.sh
```

Expected: fails because chart files do not exist.

- [ ] **Step 3: Add chart metadata and values**

Create `Chart.yaml`:

```yaml
apiVersion: v2
name: observer
description: Observer server with PostgreSQL, object storage, and telemetry gate
type: application
version: 0.1.0
appVersion: "0.1.0"
```

Create `values.yaml` with safe defaults:

```yaml
replicaCount: 2
image:
  repository: ghcr.io/yourorg/multi-agent/observer-server
  tag: latest
  pullPolicy: IfNotPresent
service:
  port: 8090
gateway:
  enabled: false
  parentRefs:
    - name: cilium-gateway
      namespace: cilium-gateway
  host: observer.example.com
ingress:
  enabled: false
existingSecret: ""
config:
  production: true
  store:
    driver: postgres
    postgres:
      dsnEnv: OBSERVER_DATABASE_URL
  objectStore:
    driver: s3
  telemetry:
    enabled: true
migration:
  enabled: true
  useHelmHook: false
retention:
  enabled: true
  schedule: "17 3 * * *"
resources: {}
```

- [ ] **Step 4: Add HTTPRoute template matching agentserver style**

Create `templates/httproute.yaml`:

```yaml
{{- if .Values.gateway.enabled }}
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: {{ include "observer.fullname" . }}
spec:
  parentRefs:
    {{- toYaml .Values.gateway.parentRefs | nindent 4 }}
  hostnames:
    - {{ .Values.gateway.host | quote }}
  rules:
    - matches:
        - path:
            type: PathPrefix
            value: /
      backendRefs:
        - name: {{ include "observer.fullname" . }}
          port: {{ .Values.service.port }}
{{- end }}
```

Do not add URLRewrite to the main observer route.

- [ ] **Step 5: Add Deployment, Service, ConfigMap, Secret, Jobs**

Create `templates/_helpers.tpl`:

```yaml
{{- define "observer.fullname" -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "observer.labels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "observer.selectorLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
```

Deployment must include:

```yaml
readinessProbe:
  httpGet:
    path: /readyz
    port: http
livenessProbe:
  httpGet:
    path: /healthz
    port: http
```

Migration Job must use `helm.sh/hook` annotations only when `migration.useHelmHook=true`.

- [ ] **Step 6: Run Helm render test**

```bash
cd multi-agent
helm lint ./deploy/charts/observer
./deploy/charts/observer/tests/chart_test.sh
```

Expected: lint and render test pass.

- [ ] **Step 7: Commit**

```bash
git add multi-agent/deploy/charts/observer
git commit -m "feat(deploy): add observer helm chart"
```

## Task 9: Add k8s-nj-prod Deployment and Smoke Test Runbook

**Files:**
- Create: `multi-agent/tests/k8s_observer/README.md`
- Create: `multi-agent/tests/k8s_observer/smoke.sh`
- Modify: `multi-agent/deploy/charts/observer/values-production.example.yaml`

- [ ] **Step 1: Write smoke script**

Create `multi-agent/tests/k8s_observer/smoke.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail

CTX="${KUBE_CONTEXT:-k8s-nj-prod}"
RELEASE="${OBSERVER_RELEASE:-observer}"
NAMESPACE="${OBSERVER_NAMESPACE:-observer}"
CHART="${OBSERVER_CHART:-multi-agent/deploy/charts/observer}"
VALUES="${OBSERVER_VALUES:-multi-agent/deploy/charts/observer/values-production.example.yaml}"

kubectl --context "$CTX" get namespace "$NAMESPACE" >/dev/null
helm --kube-context "$CTX" lint "$CHART"
helm --kube-context "$CTX" template "$RELEASE" "$CHART" -n "$NAMESPACE" -f "$VALUES" >/tmp/observer-rendered.yaml
grep -q 'kind: HTTPRoute' /tmp/observer-rendered.yaml
kubectl --context "$CTX" -n "$NAMESPACE" get pods
kubectl --context "$CTX" -n "$NAMESPACE" rollout status "deploy/$RELEASE-observer" --timeout=180s
```

Make executable:

```bash
chmod +x multi-agent/tests/k8s_observer/smoke.sh
```

- [ ] **Step 2: Add runbook**

Create `multi-agent/tests/k8s_observer/README.md`:

````markdown
# Observer k8s-nj-prod Smoke

Acceptance cluster:

```yaml
- cluster:
    server: https://k8s-prod.nj.cs.ac.cn
  name: k8s-nj-prod
```

All real deployment and smoke commands must use:

```bash
kubectl --context k8s-nj-prod
helm --kube-context k8s-nj-prod
```

Render check:

```bash
cd /root/multi-agent/.worktrees/observer-postgres-k8s-design
multi-agent/tests/k8s_observer/smoke.sh
```
````

- [ ] **Step 3: Run non-mutating checks**

```bash
cd /root/multi-agent/.worktrees/observer-postgres-k8s-design
kubectl --context k8s-nj-prod config current-context
helm --kube-context k8s-nj-prod lint multi-agent/deploy/charts/observer
helm --kube-context k8s-nj-prod template observer multi-agent/deploy/charts/observer -n observer -f multi-agent/deploy/charts/observer/values-production.example.yaml >/tmp/observer-rendered.yaml
grep -q 'kind: HTTPRoute' /tmp/observer-rendered.yaml
```

Expected: current context prints `k8s-nj-prod`; Helm lint passes; rendered YAML contains `HTTPRoute`.

- [ ] **Step 4: Commit**

```bash
git add multi-agent/tests/k8s_observer multi-agent/deploy/charts/observer/values-production.example.yaml
git commit -m "test(observer): add k8s-nj-prod smoke runbook"
```

## Task 10: Full Verification and Acceptance

**Files:**
- No new source files unless previous tasks reveal missing documentation.

- [ ] **Step 1: Run full Go tests**

```bash
cd multi-agent
go test ./... -count=1
```

Expected: all packages pass. PostgreSQL and object-store integration tests skip unless their env vars are set.

- [ ] **Step 2: Run PostgreSQL integration tests when DSN is available**

```bash
cd multi-agent
OBSERVER_POSTGRES_TEST_DSN="$OBSERVER_POSTGRES_TEST_DSN" go test ./internal/observerstore/postgres -count=1
```

Expected: pass when `OBSERVER_POSTGRES_TEST_DSN` points to a disposable database.

- [ ] **Step 3: Run Helm checks**

```bash
cd multi-agent
helm lint ./deploy/charts/observer
./deploy/charts/observer/tests/chart_test.sh
```

Expected: both pass.

- [ ] **Step 4: Run k8s-nj-prod render and cluster smoke**

```bash
cd /root/multi-agent/.worktrees/observer-postgres-k8s-design
multi-agent/tests/k8s_observer/smoke.sh
```

Expected: the script uses `k8s-nj-prod`, renders HTTPRoute, and sees observer pods ready if the release has been deployed.

- [ ] **Step 5: Verify telemetry gate manually**

Use the deployed observer URL and an agent token:

```bash
curl -i -X POST "$OBSERVER_URL/api/events" \
  -H "Authorization: Bearer $AGENT_TOKEN" \
  -H "Content-Type: application/json" \
  --data '{"workspace_id":"ws","agent_id":"agent","agent_role":"slave","type":"slave_task_started","task_id":"t"}'
```

Expected: `403 Forbidden` because `X-Loom-Telemetry-Key` is missing.

Then retry:

```bash
curl -i -X POST "$OBSERVER_URL/api/events" \
  -H "Authorization: Bearer $AGENT_TOKEN" \
  -H "X-Loom-Telemetry-Key: $OBSERVER_TELEMETRY_KEY" \
  -H "Content-Type: application/json" \
  --data '{"workspace_id":"ws","agent_id":"agent","agent_role":"slave","type":"slave_task_started","task_id":"t"}'
```

Expected: `202 Accepted` only when the token identity and telemetry key scope match.

- [ ] **Step 6: Commit verification docs when modified**

When smoke testing changes `multi-agent/tests/k8s_observer/README.md` or chart notes, commit those files:

```bash
git add multi-agent/tests/k8s_observer multi-agent/deploy/charts/observer
git commit -m "docs(observer): record production smoke results"
```

## Execution Status

Updated: 2026-06-07.

- Task 1 completed: observer production config shape and health endpoints.
- Task 2 completed: shared observer store interface with SQLite compatibility.
- Task 3 completed: PostgreSQL schema, migration, and constructor skeleton.
- Task 4 completed: PostgreSQL metadata store parity.
- Task 5 completed: telemetry defaults off, operations-key gate, and rate limit.
- Task 6 completed: object-store abstraction and artifact/write proxy flow.
  - Commits: `9b6e270`, `50129ed`, `2ebccbf`, `b08d677`.
  - Review gate: Task 6 quality review approved after `b08d677`.
  - Verification: `go test ./internal/observerweb -run 'Test.*ObjectStore|TestArtifactLazyHTTPFlow|TestWriteHTTPFlow' -count=1`; `go test ./cmd/observer-server ./internal/observerweb ./internal/driver ./internal/orchestrator ./internal/objectstore -count=1`; `go test ./internal/objectstore ./internal/observerweb ./internal/observerstore ./internal/observerstore/postgres -count=1`; `go mod tidy -diff`; `git diff --check`; `go test ./... -count=1`.
  - Residual risk: no live MinIO/PostgreSQL integration run yet because `OBSERVER_POSTGRES_TEST_DSN` and live object-store credentials were not provided.
- Next task: Task 7, make userspace blob storage object-store compatible. Known risk: `cmd/observer-server` still runs SQLite-oriented userspace migration when the observer store is PostgreSQL until Task 7 resolves userspace metadata/blob storage.

## Self-Review Checklist

- Spec coverage:
  - PostgreSQL store: Tasks 2-4.
  - Helm/K8s deployment: Tasks 8-9.
  - k8s-nj-prod acceptance target: Tasks 9-10.
  - Operations telemetry API key: Task 5.
  - Telemetry default off: Task 5.
  - Object-store artifact/write body migration: Task 6.
  - Userspace blob migration: Task 7.
  - Synchronous projection with future async boundary: Tasks 3-4.
  - Rate limiting and retention: Tasks 5 and 8.
  - HTTPRoute agentserver-style template: Task 8.

- Plan scan:
  - Every implementation step names the file, symbol, command, or config field it changes.
  - Method signatures introduced in one task are reused with the same names in later tasks.

- Type consistency:
  - `TelemetryAPIKeySpec`, `Store`, `ObjectStoreConfig`, and `observerclient.Config` names must match exactly across tasks.
  - `X-Loom-Telemetry-Key` is the only telemetry key header.
  - Kubernetes context is always `k8s-nj-prod`.
