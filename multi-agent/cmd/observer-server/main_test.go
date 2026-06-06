package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/objectstore"
	"github.com/yourorg/multi-agent/internal/observerstore"
)

func TestLoadConfigDefaultsAndValidates(t *testing.T) {
	cfg := loadConfigFromString(t, `
api_keys:
  - id: ak-default
    key: ak_secret
`)

	require.Equal(t, ":8090", cfg.ListenAddr)
	require.Equal(t, "observer.db", cfg.DBPath)
	require.Len(t, cfg.APIKeys, 1)
	require.Equal(t, "ak-default", cfg.APIKeys[0].ID)
	require.Equal(t, "sqlite", cfg.Store.Driver)
	require.Equal(t, cfg.DBPath, cfg.Store.SQLite.Path)
	require.Equal(t, 20, cfg.Store.Postgres.MaxOpenConns)
	require.Equal(t, 10, cfg.Store.Postgres.MaxIdleConns)
	require.Equal(t, "30m", cfg.Store.Postgres.ConnMaxLifetime)
	require.Equal(t, "filesystem", cfg.ObjectStore.Driver)
	require.Equal(t, int64(8<<20), cfg.ObjectStore.Proxy.MaxBytes)
	require.Equal(t, 60, cfg.Telemetry.RateLimit.PerMinute)
	require.Equal(t, 120, cfg.Telemetry.RateLimit.Burst)
	require.Equal(t, int64(256<<10), cfg.Telemetry.MaxBodyBytes)
	require.Equal(t, 30, cfg.Telemetry.RetentionDays)
}

func TestLoadDistributedObserverExampleConfig(t *testing.T) {
	cfg, err := loadConfig("../../dev/configs/observer.example.yaml")
	require.NoError(t, err)

	require.Equal(t, ":8080", cfg.ListenAddr)
	require.Equal(t, "observer.db", cfg.DBPath)
	require.Len(t, cfg.APIKeys, 1)
	require.Equal(t, "ak-dev", cfg.APIKeys[0].ID)
}

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

func TestConfigureTelemetryAPIKeysClearsPersistedKeysWhenTelemetryDisabled(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()

	require.NoError(t, st.ReplaceTelemetryAPIKeys([]observerstore.TelemetryAPIKeySpec{
		{ID: "old", Key: "old-secret", WorkspaceID: "*", Enabled: true},
	}))
	keyID, ok, err := st.LookupTelemetryAPIKey("old-secret", "ws1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "old", keyID)

	cfg := &Config{
		Telemetry: TelemetryConfig{
			Enabled: false,
			APIKeys: []TelemetryAPIKeyConfig{{
				ID: "ops", KeyEnv: "OBSERVER_TELEMETRY_KEY_SHOULD_NOT_BE_REQUIRED", WorkspaceID: "*",
			}},
		},
	}
	require.NoError(t, configureTelemetryAPIKeys(st, cfg))

	_, ok, err = st.LookupTelemetryAPIKey("old-secret", "ws1")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestOpenObjectStoreFilesystemReturnsNil(t *testing.T) {
	objects, err := openObjectStore(&Config{
		ObjectStore: ObjectStoreConfig{Driver: "filesystem"},
	})
	require.NoError(t, err)
	require.Nil(t, objects)
}

func TestOpenObjectStoreMemoryReturnsStore(t *testing.T) {
	objects, err := openObjectStore(&Config{
		ObjectStore: ObjectStoreConfig{Driver: "memory"},
	})
	require.NoError(t, err)
	require.IsType(t, &objectstore.Memory{}, objects)
}

func TestObserverWebOptionsIncludesObjectProxyLimit(t *testing.T) {
	objects := objectstore.NewMemory()
	cfg := &Config{
		ObjectStore: ObjectStoreConfig{Proxy: ProxyConfig{MaxBytes: 1234}},
		Telemetry: TelemetryConfig{
			MaxBodyBytes: 4567,
			RateLimit:    TelemetryRateLimitConfig{PerMinute: 8, Burst: 9},
		},
	}

	opts := observerWebOptions(cfg, objects)
	require.Same(t, objects, opts.Objects)
	require.Equal(t, int64(1234), opts.MaxObjectProxyBytes)
	require.Equal(t, int64(4567), opts.MaxEventBodyBytes)
	require.Equal(t, 8, opts.TelemetryRateLimit.PerMinute)
	require.Equal(t, 9, opts.TelemetryRateLimit.Burst)
}

func TestOpenObjectStoreS3RequiresCredentialEnvValues(t *testing.T) {
	const accessEnv = "OBSERVER_TEST_S3_ACCESS_KEY"
	const secretEnv = "OBSERVER_TEST_S3_SECRET_KEY"
	t.Setenv(accessEnv, "")
	t.Setenv(secretEnv, "")

	objects, err := openObjectStore(&Config{
		ObjectStore: ObjectStoreConfig{
			Driver: "s3",
			S3: S3Config{
				Endpoint:     "s3.internal",
				Bucket:       "observer-artifacts",
				AccessKeyEnv: accessEnv,
				SecretKeyEnv: secretEnv,
			},
		},
	})
	require.Error(t, err)
	require.Nil(t, objects)
	require.Contains(t, err.Error(), accessEnv+" is required")
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

func TestEffectiveSQLitePathUsesStorePathForDatabaseAndBlobRoot(t *testing.T) {
	cfg := loadConfigFromString(t, `
db_path: /legacy/observer.db
api_keys:
  - id: ak-default
    key: ak_secret
store:
  driver: sqlite
  sqlite:
    path: /configured/observer.db
`)

	path := effectiveSQLitePath(cfg)
	require.Equal(t, "/configured/observer.db", path)
	require.Equal(t, "/configured/userspace-blobs", userspaceBlobRoot(path))

	cfg.Store.SQLite.Path = ""
	require.Equal(t, "/legacy/observer.db", effectiveSQLitePath(cfg))
	require.Equal(t, filepath.Join(os.TempDir(), "userspace-blobs"), userspaceBlobRoot(":memory:"))
}

func TestWithHealthHandlesLivenessReadinessAndApp(t *testing.T) {
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("app\n"))
	})
	handler := withHealth(app, func(ctx context.Context) error {
		return nil
	})

	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	require.Equal(t, http.StatusOK, health.Code)
	require.Equal(t, "ok\n", health.Body.String())

	ready := httptest.NewRecorder()
	handler.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	require.Equal(t, http.StatusOK, ready.Code)
	require.Equal(t, "ready\n", ready.Body.String())

	disallowed := httptest.NewRecorder()
	handler.ServeHTTP(disallowed, httptest.NewRequest(http.MethodPost, "/healthz", nil))
	require.Equal(t, http.StatusMethodNotAllowed, disallowed.Code)

	passthrough := httptest.NewRecorder()
	handler.ServeHTTP(passthrough, httptest.NewRequest(http.MethodGet, "/api/agents", nil))
	require.Equal(t, http.StatusOK, passthrough.Code)
	require.Equal(t, "app\n", passthrough.Body.String())
}

func TestWithHealthReturnsUnavailableWhenReadyFails(t *testing.T) {
	handler := withHealth(http.NotFoundHandler(), func(ctx context.Context) error {
		return errors.New("database unavailable")
	})

	ready := httptest.NewRecorder()
	handler.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	require.Equal(t, http.StatusServiceUnavailable, ready.Code)
	require.Equal(t, "not ready\n", ready.Body.String())
	require.NotContains(t, ready.Body.String(), "database unavailable")
}

func TestLoadConfigRejectsObsoleteWorkspacesField(t *testing.T) {
	path := writeConfig(t, `
api_keys:
  - id: ak-default
    key: ak_secret
workspaces:
  - id: ws1
    name: Workspace
`)
	_, err := loadConfig(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "workspaces", "yaml strict mode should reject the obsolete field")
}

func TestLoadConfigRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "config with no api_keys",
			yaml: `
listen_addr: ":8090"
db_path: ":memory:"
api_keys: []
`,
			wantErr: "must define at least one api_keys entry",
		},
		{
			name: "duplicate api_keys id",
			yaml: `
listen_addr: ":8090"
db_path: ":memory:"
api_keys:
  - { id: dup, key: k1 }
  - { id: dup, key: k2 }
`,
			wantErr: "duplicate api_keys.id dup",
		},
		{
			name: "empty api_key id",
			yaml: `
listen_addr: ":8090"
db_path: ":memory:"
api_keys:
  - { id: "", key: k1 }
`,
			wantErr: "api_keys[0].id is required",
		},
		{
			name: "invalid store driver",
			yaml: `
api_keys:
  - { id: ak-default, key: ak_secret }
store:
  driver: mysql
`,
			wantErr: "store.driver must be sqlite or postgres",
		},
		{
			name: "postgres missing dsn_env",
			yaml: `
api_keys:
  - { id: ak-default, key: ak_secret }
store:
  driver: postgres
`,
			wantErr: "store.postgres.dsn_env is required when store.driver is postgres",
		},
		{
			name: "invalid object store driver",
			yaml: `
api_keys:
  - { id: ak-default, key: ak_secret }
object_store:
  driver: gcs
`,
			wantErr: "object_store.driver must be filesystem, memory, or s3",
		},
		{
			name: "s3 missing endpoint and bucket",
			yaml: `
api_keys:
  - { id: ak-default, key: ak_secret }
object_store:
  driver: s3
  s3:
    access_key_env: OBSERVER_S3_ACCESS_KEY
    secret_key_env: OBSERVER_S3_SECRET_KEY
`,
			wantErr: "object_store.s3.endpoint and bucket are required",
		},
		{
			name: "s3 missing credentials",
			yaml: `
api_keys:
  - { id: ak-default, key: ak_secret }
object_store:
  driver: s3
  s3:
    endpoint: s3.internal
    bucket: observer-artifacts
`,
			wantErr: "object_store.s3 access_key_env and secret_key_env are required",
		},
		{
			name: "telemetry enabled without api keys",
			yaml: `
api_keys:
  - { id: ak-default, key: ak_secret }
telemetry:
  enabled: true
`,
			wantErr: "telemetry.api_keys must contain at least one key when telemetry.enabled is true",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeConfig(t, tt.yaml)
			_, err := loadConfig(path)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func loadConfigFromString(t *testing.T, yaml string) *Config {
	t.Helper()

	cfg, err := loadConfig(writeConfig(t, yaml))
	require.NoError(t, err)
	return cfg
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "observer.yaml")
	require.NoError(t, os.WriteFile(path, []byte(strings.TrimSpace(content)+"\n"), 0o600))
	return path
}
