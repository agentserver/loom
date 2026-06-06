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
	require.Contains(t, ready.Body.String(), "database unavailable")
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
