package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	// Register backend kinds so Load's isRegisteredKind whitelist
	// recognises "claude"/"codex" in unit tests. Production binaries
	// register these in cmd/{driver,master,slave}-agent/main.go.
	_ "github.com/yourorg/multi-agent/pkg/agentbackend/claude"
	_ "github.com/yourorg/multi-agent/pkg/agentbackend/codex"
	_ "github.com/yourorg/multi-agent/pkg/agentbackend/opencode"
)

// loadFromString writes body to a temp file and Loads it. Helper for
// the issue #15 schema validation tests that don't need any other
// per-test scaffolding beyond a yaml fixture.
func loadFromString(t *testing.T, body string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

func TestLoad_ValidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
server:
  url: https://example.com
  name: agent-1
agent:
  kind: claude
  bin: claude
  workdir: /tmp/proj
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

func TestLoadAgentWorkerMode(t *testing.T) {
	cfg, err := loadFromString(t, `
server: {url: "http://observer", name: "test"}
agent:
  kind: codex
  bin: codex
  workdir: /tmp/repo
  worker_mode: app_server
discovery: {display_name: "daemon", description: "test", skills: []}
`)
	require.NoError(t, err)
	require.Equal(t, "app_server", cfg.Agent.WorkerMode)
}

func TestLoadAgentCodexAndLoomHomeFields(t *testing.T) {
	cfg, err := loadFromString(t, `
server: {url: "http://observer", name: "test"}
agent:
  kind: codex
  bin: codex
  workdir: /tmp/repo
  codex_home: /tmp/agent/.codex
  loom_home: /tmp/loom
discovery: {display_name: "daemon", description: "test", skills: []}
`)
	require.NoError(t, err)
	require.Equal(t, "/tmp/agent/.codex", cfg.Agent.CodexHome)
	require.Equal(t, "/tmp/loom", cfg.Agent.LoomHome)
}

func TestLoad_MissingURL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(path, []byte("server:\n  name: x\nagent: {kind: claude, bin: claude, workdir: /tmp/proj}\n"), 0o600))

	_, err := Load(path)
	require.ErrorContains(t, err, "server.url")
}

func TestSaveLoad_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	c := &Config{
		Server:    Server{URL: "https://x", Name: "n"},
		Agent:     Agent{Kind: "claude", Bin: "claude", WorkDir: "/tmp/proj"},
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
agent: {kind: claude, bin: claude, workdir: /tmp/proj}
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
agent: {kind: claude, bin: claude, workdir: /tmp/proj}
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
	require.NoError(t, os.WriteFile(path, []byte("server:\n  url: x\nagent: {kind: claude, bin: claude, workdir: /tmp/proj}\n"), 0o600))
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
agent: {kind: claude, bin: claude, workdir: /tmp/proj}
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

func TestLoad_ObserverTelemetryDefaultsDisabled(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "observer.token")
	path := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
server:
  url: https://example.com
  name: m
agent: {kind: claude, bin: claude, workdir: /tmp/proj}
discovery:
  display_name: slave-display
observer:
  enabled: true
  url: https://observer.example
  workspace_id: ws
  agent_id: slave-display
  api_key: ak_x
  token_state_path: `+tokenPath+`
`), 0o600))

	c, err := Load(path)
	require.NoError(t, err)
	require.False(t, c.Observer.TelemetryEnabled)
}

func TestLoad_ObserverTelemetryCanBeEnabled(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "observer.token")
	path := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
server:
  url: https://example.com
  name: m
agent: {kind: claude, bin: claude, workdir: /tmp/proj}
discovery:
  display_name: slave-display
observer:
  enabled: true
  telemetry_enabled: true
  telemetry_api_key: ops-secret
  url: https://observer.example
  workspace_id: ws
  agent_id: slave-display
  api_key: ak_x
  token_state_path: `+tokenPath+`
`), 0o600))

	c, err := Load(path)
	require.NoError(t, err)
	require.True(t, c.Observer.TelemetryEnabled)
}

func TestLoad_ObserverCanUseAgentserverProxyToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
server:
  url: https://example.com
  name: m
credentials:
  proxy_token: proxy-token
  workspace_id: ws-agentserver
  short_id: master-short
agent: {kind: claude, bin: claude, workdir: /tmp/proj}
discovery:
  display_name: master-display
observer:
  enabled: true
  telemetry_enabled: true
  telemetry_api_key: ops-secret
  url: https://observer.example
`), 0o600))

	c, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, "ws-agentserver", c.Observer.WorkspaceID)
	require.Equal(t, "master-short", c.Observer.AgentID)
	require.Empty(t, c.Observer.APIKey)
	require.Empty(t, c.Observer.TokenStatePath)
}

func TestLoad_ObserverAllowsPendingAgentserverRegistration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
server:
  url: https://example.com
  name: m
agent: {kind: claude, bin: claude, workdir: /tmp/proj}
discovery:
  display_name: master-display
observer:
  enabled: true
  url: https://observer.example
`), 0o600))

	c, err := Load(path)
	require.NoError(t, err)
	require.Empty(t, c.Observer.WorkspaceID)
	require.Empty(t, c.Observer.AgentID)
	require.Empty(t, c.Observer.APIKey)
	require.Empty(t, c.Observer.TokenStatePath)
}

func TestLoad_ObserverTelemetryEnabledRequiresAPIKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	tokenPath := filepath.Join(dir, "observer.token")
	require.NoError(t, os.WriteFile(path, []byte(`
server:
  url: https://example.com
  name: m
agent: {kind: claude, bin: claude, workdir: /tmp/proj}
discovery:
  display_name: master-display
observer:
  enabled: true
  url: https://observer.example
  workspace_id: ws
  agent_id: a
  api_key: ak
  token_state_path: `+tokenPath+`
  telemetry_enabled: true
`), 0o600))

	_, err := Load(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "observer.telemetry_api_key is required when observer.telemetry_enabled is true")
}

func TestLoad_ResourcesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	src := `
server: {url: "http://x", name: "s"}
agent: {kind: claude, bin: claude, workdir: /tmp/proj}
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
	if err := os.WriteFile(path, []byte(`server: {url: "u", name: "n"}`+"\n"+`agent: {kind: claude, bin: claude, workdir: /tmp/proj}`), 0o600); err != nil {
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

func TestLoad_ObserverEnabledRequiresAPIKeyAndTokenStatePath(t *testing.T) {
	cases := []struct {
		name    string
		extra   string
		wantSub string
	}{
		{
			name: "missing api_key",
			extra: func() string {
				parent := t.TempDir()
				return "  token_state_path: " + filepath.Join(parent, "observer.token") + "\n"
			}(),
			wantSub: "observer.api_key",
		},
		{
			name:    "missing token_state_path",
			extra:   "  api_key: ak\n",
			wantSub: "observer.token_state_path",
		},
		{
			name:    "relative token_state_path",
			extra:   "  api_key: ak\n  token_state_path: relative/path\n",
			wantSub: "must be an absolute path",
		},
		{
			name:    "missing parent dir",
			extra:   "  api_key: ak\n  token_state_path: /nonexistent_dir_xyz/observer.token\n",
			wantSub: "parent directory",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "c.yaml")
			body := "server:\n  url: https://example.com\n  name: m\nagent: {kind: claude, bin: claude, workdir: /tmp/proj}\ndiscovery:\n  display_name: a\nobserver:\n  enabled: true\n  url: https://observer.example\n  workspace_id: ws\n  agent_id: a\n" + tc.extra
			require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
			_, err := Load(path)
			require.Error(t, err)
			require.True(t, strings.Contains(err.Error(), tc.wantSub),
				"want error containing %q, got %v", tc.wantSub, err)
		})
	}
}

func TestLoad_ObserverRejectsObsoleteTokenField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
server:
  url: https://example.com
  name: m
agent: {kind: claude, bin: claude, workdir: /tmp/proj}
discovery:
  display_name: a
observer:
  enabled: true
  url: https://observer.example
  workspace_id: ws
  agent_id: a
  token: leftover-token
`), 0o600))
	_, err := Load(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "token",
		"strict yaml decoder should reject the obsolete token field")
}

func TestLoad_ObserverSuccessWithAPIKeyAndPath(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	require.NoError(t, os.Mkdir(stateDir, 0o755))
	tokenPath := filepath.Join(stateDir, "observer.token")

	path := filepath.Join(t.TempDir(), "c.yaml")
	body := "server:\n  url: https://example.com\n  name: m\nagent: {kind: claude, bin: claude, workdir: /tmp/proj}\ndiscovery:\n  display_name: a\nobserver:\n  enabled: true\n  url: https://observer.example\n  workspace_id: ws\n  agent_id: a\n  api_key: ak_x\n  token_state_path: " + tokenPath + "\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	c, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, "ak_x", c.Observer.APIKey)
	require.Equal(t, tokenPath, c.Observer.TokenStatePath)
}

func TestLoadHumanloopDefaults(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte("server:\n  url: \"http://x\"\nagent: {kind: claude, bin: claude, workdir: /tmp/proj}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Humanloop.ShutdownGraceSec != 10 {
		t.Errorf("ShutdownGraceSec default = %d, want 10", cfg.Humanloop.ShutdownGraceSec)
	}
	if cfg.Humanloop.MaxQuestionsPerTask != 5 {
		t.Errorf("MaxQuestionsPerTask default = %d, want 5", cfg.Humanloop.MaxQuestionsPerTask)
	}
}

func TestLoadHumanloopOverrides(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	yaml := "server:\n  url: \"http://x\"\nagent: {kind: claude, bin: claude, workdir: /tmp/proj}\nhumanloop:\n  shutdown_grace_sec: 3\n  max_questions_per_task: 99\n"
	if err := os.WriteFile(p, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Humanloop.ShutdownGraceSec != 3 || cfg.Humanloop.MaxQuestionsPerTask != 99 {
		t.Errorf("override failed: got %+v", cfg.Humanloop)
	}
}

// TestSlaveLoad_RequiresAgentKind pins issue #15: agent.kind is
// mandatory; no implicit default.
func TestSlaveLoad_RequiresAgentKind(t *testing.T) {
	yaml := `server:
  url: https://example.invalid
  name: x
agent:
  workdir: /tmp/proj
  bin: claude
discovery:
  display_name: x
`
	c, err := loadFromString(t, yaml)
	if err == nil {
		t.Fatalf("expected error; got %+v", c)
	}
	if !strings.Contains(err.Error(), "agent.kind") {
		t.Fatalf("err should mention agent.kind; got %v", err)
	}
}

// TestSlaveLoad_RequiresAgentWorkDir pins issue #15: agent.workdir is
// mandatory; no fallback to cwd (PR #14 P1 band-aid removed).
func TestSlaveLoad_RequiresAgentWorkDir(t *testing.T) {
	yaml := `server:
  url: https://example.invalid
  name: x
agent:
  kind: claude
  bin: claude
discovery:
  display_name: x
`
	c, err := loadFromString(t, yaml)
	if err == nil {
		t.Fatalf("expected error; got %+v", c)
	}
	if !strings.Contains(err.Error(), "agent.workdir") {
		t.Fatalf("err should mention agent.workdir; got %v", err)
	}
}

// TestSlaveLoad_RejectsUnknownAgentKind names the registered set so
// operators can see what import they're missing.
func TestSlaveLoad_RejectsUnknownAgentKind(t *testing.T) {
	yaml := `server:
  url: https://example.invalid
  name: x
agent:
  kind: gemini
  workdir: /tmp/proj
  bin: gemini
discovery:
  display_name: x
`
	_, err := loadFromString(t, yaml)
	if err == nil {
		t.Fatal("expected error for unknown agent.kind")
	}
	for _, want := range []string{"gemini", "claude", "codex"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err missing %q: %v", want, err)
		}
	}
}

// TestSlaveLoad_RejectsLegacyClaudeKey gives operators an actionable
// migration error instead of yaml's generic "unknown field" noise.
func TestSlaveLoad_RejectsLegacyClaudeKey(t *testing.T) {
	yaml := `server:
  url: https://example.invalid
  name: x
agent:
  kind: claude
  workdir: /tmp/proj
  bin: claude
claude:
  workdir: /tmp/old
discovery:
  display_name: x
`
	_, err := loadFromString(t, yaml)
	if err == nil {
		t.Fatal("expected error for legacy claude: key")
	}
	if !strings.Contains(err.Error(), "claude") || !strings.Contains(err.Error(), "agent.workdir") {
		t.Fatalf("err should name 'claude' legacy key and point at agent.workdir; got %v", err)
	}
}

// TestSlaveLoad_RejectsBareLegacyClaudeKey pins the §issue-15 P3
// reviewer concern: bare `claude:` (no value) or `claude: null` also
// triggers the friendly migration error.
func TestSlaveLoad_RejectsBareLegacyClaudeKey(t *testing.T) {
	yaml := `server:
  url: https://example.invalid
  name: x
agent:
  kind: claude
  workdir: /tmp/proj
  bin: claude
claude:
discovery:
  display_name: x
`
	_, err := loadFromString(t, yaml)
	if err == nil {
		t.Fatal("expected error for bare legacy claude: key")
	}
	if !strings.Contains(err.Error(), "claude") || !strings.Contains(err.Error(), "agent.workdir") {
		t.Fatalf("err should name 'claude' legacy key and point at agent.workdir; got %v", err)
	}
}

// TestSlaveLoad_RejectsLegacyCodexKey same for codex.
func TestSlaveLoad_RejectsLegacyCodexKey(t *testing.T) {
	yaml := `server:
  url: https://example.invalid
  name: x
agent:
  kind: codex
  workdir: /tmp/proj
  bin: codex
codex:
  workdir: /tmp/old
discovery:
  display_name: x
`
	_, err := loadFromString(t, yaml)
	if err == nil {
		t.Fatal("expected error for legacy codex: key")
	}
	if !strings.Contains(err.Error(), "codex") {
		t.Fatalf("err should name 'codex' legacy key; got %v", err)
	}
}

// TestSlaveLoad_HappyPathCodex pins the new YAML shape.
func TestSlaveLoad_HappyPathCodex(t *testing.T) {
	yaml := `server:
  url: https://example.invalid
  name: x
agent:
  kind: codex
  workdir: /tmp/proj
  bin: codex
  extra_args:
    - --foo
discovery:
  display_name: x
`
	c, err := loadFromString(t, yaml)
	if err != nil {
		t.Fatal(err)
	}
	if c.Agent.Kind != "codex" {
		t.Errorf("kind=%q", c.Agent.Kind)
	}
	if c.Agent.WorkDir != "/tmp/proj" {
		t.Errorf("workdir=%q", c.Agent.WorkDir)
	}
	if c.Agent.Bin != "codex" {
		t.Errorf("bin=%q", c.Agent.Bin)
	}
	if len(c.Agent.ExtraArgs) != 1 || c.Agent.ExtraArgs[0] != "--foo" {
		t.Errorf("extra_args=%v", c.Agent.ExtraArgs)
	}
	if c.Planner.Bin != "codex" {
		t.Errorf("planner.bin=%q want codex (should follow agent.bin)", c.Planner.Bin)
	}
}

// TestSlaveLoad_AcceptsOpencodeKind — mirror of the driver-side test.
func TestSlaveLoad_AcceptsOpencodeKind(t *testing.T) {
	yaml := `server:
  url: https://example.invalid
  name: x
agent:
  kind: opencode
  workdir: /tmp/proj
discovery:
  display_name: x
`
	c, err := loadFromString(t, yaml)
	if err != nil {
		t.Fatal(err)
	}
	if c.Agent.Kind != "opencode" {
		t.Fatalf("Agent.Kind=%q want opencode", c.Agent.Kind)
	}
}
