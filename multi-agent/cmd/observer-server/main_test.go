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
    agents:
      - id: driver
        role: driver
        display_name: Driver
        token: driver-token
`)

	require.Equal(t, ":8090", cfg.ListenAddr)
	require.Equal(t, "observer.db", cfg.DBPath)
	require.Len(t, cfg.Workspaces, 1)
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
    agents:
      - id: driver
        role: driver
        display_name: Driver
        token: driver-token
`,
			wantErr: "workspace[0].id is required",
		},
		{
			name: "empty workspace name",
			yaml: `
workspaces:
  - id: ws1
    agents:
      - id: driver
        role: driver
        display_name: Driver
        token: driver-token
`,
			wantErr: "workspace[0].name is required",
		},
		{
			name: "workspace with no agents",
			yaml: `
workspaces:
  - id: ws1
    name: Workspace
`,
			wantErr: "workspace[ws1] must define at least one agent",
		},
		{
			name: "empty agent id",
			yaml: `
workspaces:
  - id: ws1
    name: Workspace
    agents:
      - role: driver
        display_name: Driver
        token: driver-token
`,
			wantErr: "workspace[ws1].agents[0].id is required",
		},
		{
			name: "empty agent role",
			yaml: `
workspaces:
  - id: ws1
    name: Workspace
    agents:
      - id: driver
        display_name: Driver
        token: driver-token
`,
			wantErr: "workspace[ws1].agents[driver].role is required",
		},
		{
			name: "empty agent display name",
			yaml: `
workspaces:
  - id: ws1
    name: Workspace
    agents:
      - id: driver
        role: driver
        token: driver-token
`,
			wantErr: "workspace[ws1].agents[driver].display_name is required",
		},
		{
			name: "empty agent token",
			yaml: `
workspaces:
  - id: ws1
    name: Workspace
    agents:
      - id: driver
        role: driver
        display_name: Driver
`,
			wantErr: "workspace[ws1].agents[driver].token is required",
		},
		{
			name: "invalid role",
			yaml: `
workspaces:
  - id: ws1
    name: Workspace
    agents:
      - id: driver
        role: worker
        display_name: Driver
        token: driver-token
`,
			wantErr: "workspace[ws1].agents[driver].role must be one of driver, master, slave",
		},
		{
			name: "whitespace padded workspace id",
			yaml: `
workspaces:
  - id: " ws1"
    name: Workspace
    agents:
      - id: driver
        role: driver
        display_name: Driver
        token: driver-token
`,
			wantErr: "workspace[0].id must not contain leading or trailing whitespace",
		},
		{
			name: "whitespace padded agent id",
			yaml: `
workspaces:
  - id: ws1
    name: Workspace
    agents:
      - id: "driver "
        role: driver
        display_name: Driver
        token: driver-token
`,
			wantErr: "workspace[ws1].agents[0].id must not contain leading or trailing whitespace",
		},
		{
			name: "whitespace padded role",
			yaml: `
workspaces:
  - id: ws1
    name: Workspace
    agents:
      - id: driver
        role: " driver"
        display_name: Driver
        token: driver-token
`,
			wantErr: "workspace[ws1].agents[driver].role must not contain leading or trailing whitespace",
		},
		{
			name: "whitespace padded token",
			yaml: `
workspaces:
  - id: ws1
    name: Workspace
    agents:
      - id: driver
        role: driver
        display_name: Driver
        token: "driver-token "
`,
			wantErr: "workspace[ws1].agents[driver].token must not contain leading or trailing whitespace",
		},
		{
			name: "duplicate workspace id",
			yaml: `
workspaces:
  - id: ws1
    name: Workspace 1
    agents:
      - id: driver
        role: driver
        display_name: Driver
        token: driver-token
  - id: ws1
    name: Workspace 2
    agents:
      - id: master
        role: master
        display_name: Master
        token: master-token
`,
			wantErr: "duplicate workspace id ws1",
		},
		{
			name: "duplicate agent id",
			yaml: `
workspaces:
  - id: ws1
    name: Workspace
    agents:
      - id: driver
        role: driver
        display_name: Driver
        token: driver-token
      - id: driver
        role: master
        display_name: Master
        token: master-token
`,
			wantErr: "duplicate agent id driver in workspace ws1",
		},
		{
			name: "duplicate token",
			yaml: `
workspaces:
  - id: ws1
    name: Workspace
    agents:
      - id: driver
        role: driver
        display_name: Driver
        token: shared-token
  - id: ws2
    name: Workspace 2
    agents:
      - id: master
        role: master
        display_name: Master
        token: shared-token
`,
			wantErr: "duplicate token shared-token",
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
