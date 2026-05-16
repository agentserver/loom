package config

import (
	"os"
	"path/filepath"
	"reflect"
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

func TestCredentialsWorkspaceIDRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
server: { url: "https://agentserver.test" }
credentials:
  sandbox_id: sbx
  tunnel_token: tt
  proxy_token: pt
  workspace_id: ws-1
  short_id: short
discovery:
  display_name: agent
  skills: [chat]
fanout: { max_concurrency: 1 }
`), 0o600))

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, "ws-1", cfg.Credentials.WorkspaceID)
	require.NoError(t, cfg.Save(path))

	reloaded, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, "ws-1", reloaded.Credentials.WorkspaceID)
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

func TestLoad_ObserverURLWithEnabledFalseKeepsDisabledAndDefaultsAgentID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
server:
  url: https://example.com
  name: m
discovery:
  display_name: master-display
observer:
  url: https://observer.example
  enabled: false
`), 0o600))

	c, err := Load(path)
	require.NoError(t, err)
	require.False(t, c.Observer.Enabled)
	require.Equal(t, "master-display", c.Observer.AgentID)
}

func TestLoad_ObserverEnabledRequiresDeliveryFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
server:
  url: https://example.com
  name: m
discovery:
  display_name: master-display
observer:
  enabled: true
  url: https://observer.example
`), 0o600))

	_, err := Load(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "observer.workspace_id")
}

func TestLoad_ResourcesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	src := `
server: {url: "http://x", name: "s"}
resources:
  cpu:
    cores: 8
    arch: x86_64
  gpu:
    count: 1
    model: "RTX 4090"
    vram_gb: 24
  memory_gb: 32
  devices: [camera, gpio]
  tags: [photogrammetry]
`
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Resources == nil {
		t.Fatal("Resources nil")
	}
	if c.Resources.CPU == nil || c.Resources.CPU.Cores != 8 || c.Resources.CPU.Arch != "x86_64" {
		t.Fatalf("CPU mismatch: %+v", c.Resources.CPU)
	}
	if c.Resources.GPU == nil || c.Resources.GPU.Count != 1 || c.Resources.GPU.Model != "RTX 4090" || c.Resources.GPU.VRAMGB != 24 {
		t.Fatalf("GPU mismatch: %+v", c.Resources.GPU)
	}
	if c.Resources.MemoryGB != 32 {
		t.Fatalf("MemoryGB = %d", c.Resources.MemoryGB)
	}
	if !reflect.DeepEqual(c.Resources.Devices, []string{"camera", "gpio"}) {
		t.Fatalf("Devices = %v", c.Resources.Devices)
	}
	if !reflect.DeepEqual(c.Resources.Tags, []string{"photogrammetry"}) {
		t.Fatalf("Tags = %v", c.Resources.Tags)
	}
}

func TestLoad_ResourcesAbsentIsNil(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(path, []byte(`server: {url: "u", name: "n"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Resources != nil {
		t.Fatalf("expected nil Resources when not declared, got %+v", c.Resources)
	}
}
