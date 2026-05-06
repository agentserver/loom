package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoad_ValidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
server:
  url: https://example.com
  name: agent-1
claude:
  bin: claude
mcp_servers:
  echo:
    transport: stdio
    command: /bin/echo
discovery:
  display_name: A
  skills: [chat, mcp]
`), 0o600))

	c, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, "https://example.com", c.Server.URL)
	require.Equal(t, "agent-1", c.Server.Name)
	require.Equal(t, "stdio", c.MCPServers["echo"].Transport)
	require.ElementsMatch(t, []string{"chat", "mcp"}, c.Discovery.Skills)
}

func TestLoad_MissingURL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(path, []byte("server:\n  name: x\n"), 0o600))

	_, err := Load(path)
	require.ErrorContains(t, err, "server.url")
}

func TestSaveLoad_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	c := &Config{
		Server:    Server{URL: "https://x", Name: "n"},
		Discovery: Discovery{DisplayName: "D", Skills: []string{"chat"}},
	}
	c.Credentials.SandboxID = "sb-1"
	c.Credentials.TunnelToken = "tt"
	c.Credentials.ProxyToken = "pt"
	c.Credentials.ShortID = "sid"

	require.NoError(t, c.Save(path))
	got, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, c.Credentials, got.Credentials)
	require.Equal(t, c.Server, got.Server)

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestLoad_MasterFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
server:
  url: https://example.com
  name: m
discovery:
  display_name: M
  skills: [route, fanout]
planner:
  bin: /usr/bin/claude
  timeout_sec: 90
fanout:
  max_concurrency: 8
  default_policy: best_effort
  policy_by_skill:
    fanout_strict: all_or_nothing
  subtask_defaults:
    timeout_sec: 1200
`), 0o600))

	c, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, "/usr/bin/claude", c.Planner.Bin)
	require.Equal(t, 90, c.Planner.TimeoutSec)
	require.Equal(t, 8, c.Fanout.MaxConcurrency)
	require.Equal(t, "best_effort", c.Fanout.DefaultPolicy)
	require.Equal(t, "all_or_nothing", c.Fanout.PolicyBySkill["fanout_strict"])
	require.Equal(t, 1200, c.Fanout.SubTaskDefaults.TimeoutSec)
}

func TestLoad_MasterDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(path, []byte("server:\n  url: x\n"), 0o600))
	c, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, 60, c.Planner.TimeoutSec)
	require.Equal(t, 4, c.Fanout.MaxConcurrency)
	require.Equal(t, "best_effort", c.Fanout.DefaultPolicy)
	require.Equal(t, 600, c.Fanout.SubTaskDefaults.TimeoutSec)
}
