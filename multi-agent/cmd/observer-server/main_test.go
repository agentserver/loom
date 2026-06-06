package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	require.True(t, cfg.Identity.LegacyAPIKeys.Enabled)
	require.False(t, cfg.Identity.Agentserver.Enabled)
}

func TestLoadDistributedObserverExampleConfig(t *testing.T) {
	cfg, err := loadConfig("../../dev/configs/observer.example.yaml")
	require.NoError(t, err)

	require.Equal(t, ":8080", cfg.ListenAddr)
	require.Equal(t, "observer.db", cfg.DBPath)
	require.Len(t, cfg.APIKeys, 1)
	require.Equal(t, "ak-dev", cfg.APIKeys[0].ID)
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
			name: "both identity sources disabled",
			yaml: `
listen_addr: ":8090"
db_path: ":memory:"
identity:
  legacy_api_keys:
    enabled: false
  agentserver:
    enabled: false
api_keys: []
`,
			wantErr: "at least one identity source must be enabled",
		},
		{
			name: "agentserver enabled without url",
			yaml: `
listen_addr: ":8090"
db_path: ":memory:"
identity:
  legacy_api_keys:
    enabled: false
  agentserver:
    enabled: true
api_keys: []
`,
			wantErr: "identity.agentserver.url is required",
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

func TestLoadConfigAllowsAgentserverOnlyWithoutAPIKeys(t *testing.T) {
	cfg := loadConfigFromString(t, `
listen_addr: ":8090"
db_path: ":memory:"
identity:
  legacy_api_keys:
    enabled: false
  agentserver:
    enabled: true
    url: https://agentserver.test
    fresh_ttl: 2m
    stale_grace: 10m
    request_timeout: 3s
    cache_capacity: 123
    startup_probe: false
api_keys: []
`)

	require.False(t, cfg.Identity.LegacyAPIKeys.Enabled)
	require.True(t, cfg.Identity.Agentserver.Enabled)
	require.Equal(t, "https://agentserver.test", cfg.Identity.Agentserver.URL)
	require.Equal(t, durationConfig(2*time.Minute), cfg.Identity.Agentserver.FreshTTL)
	require.Equal(t, durationConfig(10*time.Minute), cfg.Identity.Agentserver.StaleGrace)
	require.Equal(t, durationConfig(3*time.Second), cfg.Identity.Agentserver.RequestTimeout)
	require.Equal(t, 123, cfg.Identity.Agentserver.CacheCapacity)
	require.False(t, cfg.Identity.Agentserver.StartupProbe)
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
