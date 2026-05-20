package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadConfigDefaultsAndValidates(t *testing.T) {
	cfg := loadConfigFromString(t, `
workspaces:
  - id: ws1
    name: Workspace
    api_keys:
      - id: ak-default
        key: ak_secret
`)

	require.Equal(t, ":8090", cfg.ListenAddr)
	require.Equal(t, "observer.db", cfg.DBPath)
	require.Len(t, cfg.Workspaces, 1)
	require.Len(t, cfg.Workspaces[0].APIKeys, 1)
	require.Equal(t, "ak-default", cfg.Workspaces[0].APIKeys[0].ID)
}

func TestLoadDistributedObserverExampleConfig(t *testing.T) {
	cfg, err := loadConfig("../../dev/configs/observer.example.yaml")
	require.NoError(t, err)

	require.Equal(t, ":8080", cfg.ListenAddr)
	require.Equal(t, "observer.db", cfg.DBPath)
	require.Len(t, cfg.Workspaces, 1)
	require.Equal(t, "dev", cfg.Workspaces[0].ID)
	require.Len(t, cfg.Workspaces[0].APIKeys, 1)
	require.Equal(t, "ak-dev", cfg.Workspaces[0].APIKeys[0].ID)
}

func TestLoadConfigRejectsObsoleteAgentsField(t *testing.T) {
	path := writeConfig(t, `
workspaces:
  - id: ws1
    name: Workspace
    api_keys:
      - id: ak-default
        key: ak_secret
    agents:
      - id: driver
        role: driver
        display_name: Driver
        token: driver-token
`)
	_, err := loadConfig(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "agents", "yaml strict mode should reject the obsolete field")
}

func TestLoadConfigRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "empty workspace id",
			yaml: `
workspaces:
  - name: Workspace
    api_keys:
      - id: ak-default
        key: ak_secret
`,
			wantErr: "workspace[0].id is required",
		},
		{
			name: "empty workspace name",
			yaml: `
workspaces:
  - id: ws1
    api_keys:
      - id: ak-default
        key: ak_secret
`,
			wantErr: "workspace[0].name is required",
		},
		{
			name: "workspace with no api_keys",
			yaml: `
workspaces:
  - id: ws1
    name: Workspace
`,
			wantErr: "workspace[ws1] must define at least one api_keys entry",
		},
		{
			name: "workspace with empty api_keys list",
			yaml: `
workspaces:
  - id: ws1
    name: Workspace
    api_keys: []
`,
			wantErr: "workspace[ws1] must define at least one api_keys entry",
		},
		{
			name: "duplicate api_keys id in workspace",
			yaml: `
workspaces:
  - id: ws1
    name: Workspace
    api_keys:
      - id: ak-dup
        key: key-1
      - id: ak-dup
        key: key-2
`,
			wantErr: "duplicate api_keys.id ak-dup in workspace ws1",
		},
		{
			name: "empty api_key id",
			yaml: `
workspaces:
  - id: ws1
    name: Workspace
    api_keys:
      - id: ""
        key: key-1
`,
			wantErr: "workspace[ws1].api_keys[0].id is required",
		},
		{
			name: "empty api_key value",
			yaml: `
workspaces:
  - id: ws1
    name: Workspace
    api_keys:
      - id: ak-empty
        key: ""
`,
			wantErr: "workspace[ws1].api_keys[ak-empty].key is required",
		},
		{
			name: "duplicate workspace id",
			yaml: `
workspaces:
  - id: ws1
    name: Workspace A
    api_keys:
      - id: ak-a
        key: key-a
  - id: ws1
    name: Workspace B
    api_keys:
      - id: ak-b
        key: key-b
`,
			wantErr: "duplicate workspace id ws1",
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
