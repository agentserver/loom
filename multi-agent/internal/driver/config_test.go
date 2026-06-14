package driver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	// Register backend kinds so LoadConfig's isRegisteredKind whitelist
	// recognises "claude"/"codex" in unit tests. Production binaries
	// register these in cmd/{driver,master,slave}-agent/main.go.
	_ "github.com/yourorg/multi-agent/pkg/agentbackend/claude"
	_ "github.com/yourorg/multi-agent/pkg/agentbackend/codex"
	_ "github.com/yourorg/multi-agent/pkg/agentbackend/opencode"
)

func TestLoadConfig_FullRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "driver.yaml")
	if err := os.WriteFile(path, []byte(`
server:
  url: https://agent.example.com
  name: driver-yuzishu
credentials:
  sandbox_id: sbx-abc
  tunnel_token: ttok
  proxy_token: ptok
  workspace_id: ws1
  short_id: drv-001
agent:
  kind: claude
  bin: claude
  workdir: /tmp/proj
discovery:
  display_name: driver-yuzishu
  description: "Local driver agent."
  skills: []
listen_addr: "127.0.0.1:0"
driver_defaults:
  target_display_name: master-prod
  task_timeout_sec: 900
  audit_log_dir: /tmp/audit
  disable_uid_check: true
  max_dir_cache_entries: 25000
`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if c.Server.URL != "https://agent.example.com" {
		t.Errorf("server.url: %s", c.Server.URL)
	}
	if c.Credentials.ShortID != "drv-001" {
		t.Errorf("short_id: %s", c.Credentials.ShortID)
	}
	if c.DriverDefaults.TargetDisplayName != "master-prod" {
		t.Errorf("target_display_name: %s", c.DriverDefaults.TargetDisplayName)
	}
	if c.DriverDefaults.TaskTimeoutSec != 900 {
		t.Errorf("task_timeout_sec: %d", c.DriverDefaults.TaskTimeoutSec)
	}
	if !c.DriverDefaults.DisableUIDCheck {
		t.Errorf("disable_uid_check: false")
	}
	if c.DriverDefaults.MaxDirCacheEntries != 25000 {
		t.Errorf("max_dir_cache_entries: %d", c.DriverDefaults.MaxDirCacheEntries)
	}
}

func TestLoadConfig_DefaultsApplied(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "driver.yaml")
	if err := os.WriteFile(path, []byte(`
server: {url: https://x, name: drv}
agent: {kind: claude, bin: claude, workdir: /tmp/proj}
discovery: {display_name: drv}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if c.DriverDefaults.TaskTimeoutSec != 600 {
		t.Errorf("default task_timeout_sec: want 600, got %d", c.DriverDefaults.TaskTimeoutSec)
	}
	if c.DriverDefaults.MaxDirCacheEntries != 50000 {
		t.Errorf("default max_dir_cache_entries: want 50000, got %d", c.DriverDefaults.MaxDirCacheEntries)
	}
	if c.Planner.Bin != "claude" {
		t.Errorf("default planner.bin: want claude, got %q", c.Planner.Bin)
	}
	if c.Planner.TimeoutSec != 60 {
		t.Errorf("default planner.timeout_sec: want 60, got %d", c.Planner.TimeoutSec)
	}
	if c.Fanout.MaxConcurrency != 4 {
		t.Errorf("default fanout.max_concurrency: want 4, got %d", c.Fanout.MaxConcurrency)
	}
	if c.Fanout.SubTaskDefaults.TimeoutSec != 600 {
		t.Errorf("default fanout.subtask_defaults.timeout_sec: want 600, got %d", c.Fanout.SubTaskDefaults.TimeoutSec)
	}
}

// TestDriverConfig_ObserverForceRegisterRoundTrip locks the §1.3 #11 PR2
// fix: the operator-facing `observer.force_register: true` YAML field must
// round-trip into cfg.Observer.ForceRegister so cmd/driver-agent can pass it
// through to observerclient.New. Without this field, observerclient's
// ForceRegister flag is unreachable from configuration.
func TestDriverConfig_ObserverForceRegisterRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "driver.yaml")
	if err := os.WriteFile(path, []byte(`
server: {url: https://x, name: drv}
agent: {kind: claude, bin: claude, workdir: /tmp/proj}
discovery: {display_name: driver-display}
observer:
  enabled: true
  url: https://observer.example
  workspace_id: ws1
  agent_id: drv-1
  api_key: ak_secret
  token_state_path: ` + filepath.Join(dir, "obs.token") + `
  force_register: true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !c.Observer.ForceRegister {
		t.Fatalf("observer.force_register: want true, got false")
	}

	// Default (omitted) must remain false — never accidentally takeover.
	path2 := filepath.Join(dir, "driver2.yaml")
	if err := os.WriteFile(path2, []byte(`
server: {url: https://x, name: drv}
agent: {kind: claude, bin: claude, workdir: /tmp/proj}
discovery: {display_name: driver-display}
observer:
  enabled: true
  url: https://observer.example
  workspace_id: ws1
  agent_id: drv-1
  api_key: ak_secret
  token_state_path: `+filepath.Join(dir, "obs2.token")+`
`), 0o600); err != nil {
		t.Fatal(err)
	}
	c2, err := LoadConfig(path2)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if c2.Observer.ForceRegister {
		t.Fatalf("observer.force_register default: want false, got true")
	}
}

func TestLoadConfig_ObserverURLWithEnabledFalseKeepsDisabledAndDefaultsAgentID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "driver.yaml")
	if err := os.WriteFile(path, []byte(`
server: {url: https://x, name: drv}
agent: {kind: claude, bin: claude, workdir: /tmp/proj}
discovery: {display_name: driver-display}
observer:
  url: https://observer.example
  enabled: false
`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if c.Observer.Enabled {
		t.Fatal("observer should remain disabled when enabled: false is set")
	}
	if c.Observer.AgentID != "driver-display" {
		t.Fatalf("observer agent_id: want driver-display, got %q", c.Observer.AgentID)
	}
}

func TestLoadConfig_ObserverTelemetryDefaultsDisabled(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "observer.token")
	path := filepath.Join(dir, "driver.yaml")
	if err := os.WriteFile(path, []byte(`
server: {url: https://x, name: drv}
agent: {kind: claude, bin: claude, workdir: /tmp/proj}
discovery: {display_name: driver-display}
observer:
  enabled: true
  url: https://observer.example
  workspace_id: ws
  agent_id: driver-display
  api_key: ak_x
  token_state_path: `+tokenPath+`
`), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if c.Observer.TelemetryEnabled {
		t.Fatal("observer telemetry should default disabled")
	}
}

func TestLoadConfig_ObserverTelemetryCanBeEnabled(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "observer.token")
	path := filepath.Join(dir, "driver.yaml")
	if err := os.WriteFile(path, []byte(`
server: {url: https://x, name: drv}
agent: {kind: claude, bin: claude, workdir: /tmp/proj}
discovery: {display_name: driver-display}
observer:
  enabled: true
  telemetry_enabled: true
  telemetry_api_key: ops-secret
  url: https://observer.example
  workspace_id: ws
  agent_id: driver-display
  api_key: ak_x
  token_state_path: `+tokenPath+`
`), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !c.Observer.TelemetryEnabled {
		t.Fatal("observer telemetry should be enabled when explicitly configured")
	}
}

func TestLoadConfig_ObserverCanUseAgentserverProxyToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "driver.yaml")
	if err := os.WriteFile(path, []byte(`
server: {url: https://x, name: drv}
credentials:
  proxy_token: proxy-token
  workspace_id: ws-agentserver
  short_id: driver-short
agent: {kind: claude, bin: claude, workdir: /tmp/proj}
discovery: {display_name: driver-display}
observer:
  enabled: true
  telemetry_enabled: true
  telemetry_api_key: ops-secret
  url: https://observer.example
`), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if c.Observer.WorkspaceID != "ws-agentserver" {
		t.Fatalf("observer workspace_id: want ws-agentserver, got %q", c.Observer.WorkspaceID)
	}
	if c.Observer.AgentID != "driver-short" {
		t.Fatalf("observer agent_id: want driver-short, got %q", c.Observer.AgentID)
	}
	if c.Observer.APIKey != "" {
		t.Fatalf("observer api_key should be empty in proxy-token mode, got %q", c.Observer.APIKey)
	}
	if c.Observer.TokenStatePath != "" {
		t.Fatalf("observer token_state_path should be empty in proxy-token mode, got %q", c.Observer.TokenStatePath)
	}
}

func TestLoadConfig_ObserverAllowsPendingAgentserverRegistration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "driver.yaml")
	if err := os.WriteFile(path, []byte(`
server: {url: https://x, name: drv}
agent: {kind: claude, bin: claude, workdir: /tmp/proj}
discovery: {display_name: driver-display}
observer:
  enabled: true
  url: https://observer.example
`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if c.Observer.WorkspaceID != "" {
		t.Fatalf("observer workspace_id should stay empty before agentserver registration, got %q", c.Observer.WorkspaceID)
	}
	if c.Observer.AgentID != "" {
		t.Fatalf("observer agent_id should stay empty before agentserver registration, got %q", c.Observer.AgentID)
	}
}

func TestLoadConfig_ObserverTelemetryEnabledRequiresAPIKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "driver.yaml")
	tokenPath := filepath.Join(dir, "observer.token")
	if err := os.WriteFile(path, []byte(`
server: {url: https://x, name: drv}
agent: {kind: claude, bin: claude, workdir: /tmp/proj}
discovery: {display_name: driver-display}
observer:
  enabled: true
  url: https://observer.example
  workspace_id: ws
  agent_id: a
  api_key: ak
  token_state_path: `+tokenPath+`
  telemetry_enabled: true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected observer telemetry config error")
	}
	if !strings.Contains(err.Error(), "observer.telemetry_api_key is required when observer.telemetry_enabled is true") {
		t.Fatalf("error: %v", err)
	}
}

func TestLoadConfig_RejectsMissingRequired(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	os.WriteFile(path, []byte(`server: {url: ""}`), 0o600)
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected error for missing server.url")
	}
}

func TestSaveConfig_RoundTrip(t *testing.T) {
	c := &Config{}
	c.Server.URL = "https://a"
	c.Server.Name = "n"
	c.Discovery.DisplayName = "n"
	c.Credentials.ProxyToken = "secret"
	c.Agent.Kind = "claude"
	c.Agent.Bin = "claude"
	c.Agent.WorkDir = "/tmp/proj"
	dir := t.TempDir()
	path := filepath.Join(dir, "out.yaml")
	if err := SaveConfig(path, c); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("perm: %o", st.Mode().Perm())
	}
	got, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("re-load: %v", err)
	}
	if got.Credentials.ProxyToken != "secret" {
		t.Errorf("round-trip lost ProxyToken: %q", got.Credentials.ProxyToken)
	}
}

func TestLoadConfig_ObserverRejectsObsoleteTokenField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(path, []byte(`
server:
  url: https://example.com
  name: m
discovery:
  display_name: driver-display
observer:
  enabled: true
  url: https://observer.example
  workspace_id: ws
  agent_id: a
  token: leftover-token
`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for obsolete token field")
	}
	if !strings.Contains(err.Error(), "token") {
		t.Fatalf("error should mention token: %v", err)
	}
}

// TestDriverLoadAgentKindCodex pins Planner.Bin defaulting to Agent.Bin for codex.
func TestDriverLoadAgentKindCodex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.yaml")
	os.WriteFile(path, []byte(`
server: { url: x, name: y }
credentials: {}
agent: { kind: codex, bin: codex, workdir: /tmp/proj }
discovery: { display_name: a }
`), 0o600)
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Agent.Kind != "codex" {
		t.Fatalf("kind=%q", c.Agent.Kind)
	}
	if c.Planner.Bin != "codex" {
		t.Fatalf("planner.bin=%q (should follow agent.bin)", c.Planner.Bin)
	}
}

func TestLoadConfig_ObserverEnabledRequiresAPIKeyAndTokenStatePath(t *testing.T) {
	cases := []struct {
		name    string
		extra   string
		wantSub string
	}{
		{
			name: "missing api_key",
			extra: func() string {
				return "  token_state_path: " + filepath.Join(t.TempDir(), "observer.token") + "\n"
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
			body := "server:\n  url: https://example.com\n  name: m\nagent:\n  kind: claude\n  bin: claude\n  workdir: /tmp/proj\ndiscovery:\n  display_name: driver-display\nobserver:\n  enabled: true\n  url: https://observer.example\n  workspace_id: ws\n  agent_id: a\n" + tc.extra
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadConfig(path)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("expected error to contain %q, got %v", tc.wantSub, err)
			}
		})
	}
}

// TestLoadConfig_DriverDefaultsWorkDirDefaultsToAgentWorkdir pins issue #15:
// DriverDefaults.WorkDir defaults to agent.workdir (the jail root).
func TestLoadConfig_DriverDefaultsWorkDirDefaultsToAgentWorkdir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "driver.yaml")
	if err := os.WriteFile(path, []byte(`
server: {url: https://x, name: drv}
discovery: {display_name: drv}
agent:
  kind: claude
  bin: claude
  workdir: /opt/project/claude
`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if c.DriverDefaults.WorkDir != "/opt/project/claude" {
		t.Errorf("DriverDefaults.WorkDir: want /opt/project/claude, got %q", c.DriverDefaults.WorkDir)
	}
}

// TestLoadConfig_DriverDefaultsWorkDirExplicitWins covers operator override:
// an explicit driver_defaults.workdir is preserved, not clobbered by the
// agent.workdir default.
func TestLoadConfig_DriverDefaultsWorkDirExplicitWins(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "driver.yaml")
	if err := os.WriteFile(path, []byte(`
server: {url: https://x, name: drv}
discovery: {display_name: drv}
agent:
  kind: claude
  bin: claude
  workdir: /opt/project/claude
driver_defaults:
  workdir: /opt/explicit
`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if c.DriverDefaults.WorkDir != "/opt/explicit" {
		t.Errorf("explicit driver_defaults.workdir should win: got %q", c.DriverDefaults.WorkDir)
	}
}

// TestLoadConfig_RequiresAgentKind pins issue #15: agent.kind is
// mandatory; no implicit default.
func TestLoadConfig_RequiresAgentKind(t *testing.T) {
	yaml := `server:
  url: https://example.invalid
  name: x
agent:
  workdir: /tmp/proj
  bin: claude
discovery:
  display_name: x
`
	path := writeTempYAML(t, yaml)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for missing agent.kind")
	}
	if !strings.Contains(err.Error(), "agent.kind") {
		t.Fatalf("err should mention agent.kind; got %v", err)
	}
}

// TestLoadConfig_RequiresAgentWorkDir pins issue #15: agent.workdir
// is mandatory; no fallback to cwd (PR #14 P1 band-aid removed).
func TestLoadConfig_RequiresAgentWorkDir(t *testing.T) {
	yaml := `server:
  url: https://example.invalid
  name: x
agent:
  kind: claude
  bin: claude
discovery:
  display_name: x
`
	path := writeTempYAML(t, yaml)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for missing agent.workdir")
	}
	if !strings.Contains(err.Error(), "agent.workdir") {
		t.Fatalf("err should mention agent.workdir; got %v", err)
	}
}

// TestLoadConfig_RejectsUnknownAgentKind names the registered set so
// operators can see what import they're missing.
func TestLoadConfig_RejectsUnknownAgentKind(t *testing.T) {
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
	path := writeTempYAML(t, yaml)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for unknown agent.kind")
	}
	for _, want := range []string{"gemini", "claude", "codex"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err missing %q: %v", want, err)
		}
	}
}

// TestLoadConfig_RejectsLegacyClaudeKey gives operators an actionable
// migration error instead of yaml's generic "unknown field" noise.
func TestLoadConfig_RejectsLegacyClaudeKey(t *testing.T) {
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
	path := writeTempYAML(t, yaml)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for legacy claude: key")
	}
	if !strings.Contains(err.Error(), "claude") || !strings.Contains(err.Error(), "agent.workdir") {
		t.Fatalf("err should name 'claude' legacy key and point at agent.workdir; got %v", err)
	}
}

// TestLoadConfig_RejectsBareLegacyClaudeKey pins the §issue-15 P3
// reviewer concern: bare `claude:` (no value) or `claude: null` also
// triggers the friendly migration error, not the generic
// "unknown field" message from the strict decoder.
func TestLoadConfig_RejectsBareLegacyClaudeKey(t *testing.T) {
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
	path := writeTempYAML(t, yaml)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for bare legacy claude: key")
	}
	if !strings.Contains(err.Error(), "claude") || !strings.Contains(err.Error(), "agent.workdir") {
		t.Fatalf("err should name 'claude' legacy key and point at agent.workdir; got %v", err)
	}
}

// TestLoadConfig_RejectsLegacyCodexKey same for codex.
func TestLoadConfig_RejectsLegacyCodexKey(t *testing.T) {
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
	path := writeTempYAML(t, yaml)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for legacy codex: key")
	}
	if !strings.Contains(err.Error(), "codex") {
		t.Fatalf("err should name 'codex' legacy key; got %v", err)
	}
}

// TestLoadConfig_HappyPathClaude pins the new YAML shape.
func TestLoadConfig_HappyPathClaude(t *testing.T) {
	yaml := `server:
  url: https://example.invalid
  name: x
agent:
  kind: claude
  workdir: /tmp/proj
  bin: claude
  extra_args:
    - --foo
discovery:
  display_name: x
`
	path := writeTempYAML(t, yaml)
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Agent.Kind != "claude" {
		t.Errorf("kind=%q", c.Agent.Kind)
	}
	if c.Agent.WorkDir != "/tmp/proj" {
		t.Errorf("workdir=%q", c.Agent.WorkDir)
	}
	if c.Agent.Bin != "claude" {
		t.Errorf("bin=%q", c.Agent.Bin)
	}
	if len(c.Agent.ExtraArgs) != 1 || c.Agent.ExtraArgs[0] != "--foo" {
		t.Errorf("extra_args=%v", c.Agent.ExtraArgs)
	}
}

// writeTempYAML is a tiny helper for tests that need a yaml file on disk.
func writeTempYAML(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestLoadConfig_AcceptsOpencodeKind pins issue-15's "add backend
// only via import" promise: agent.kind: opencode loads after the
// driver-agent main imports pkg/agentbackend/opencode for side
// effects. The test relies on the side-effect imports config_test.go
// already has for claude+codex; add opencode to that block too.
func TestLoadConfig_AcceptsOpencodeKind(t *testing.T) {
	yaml := `server:
  url: https://example.invalid
  name: x
agent:
  kind: opencode
  workdir: /tmp/proj
discovery:
  display_name: x
`
	path := writeTempYAML(t, yaml)
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Agent.Kind != "opencode" {
		t.Fatalf("Agent.Kind=%q want opencode", c.Agent.Kind)
	}
	if c.Agent.Bin != "opencode" {
		t.Fatalf("Agent.Bin=%q want opencode (factory default)", c.Agent.Bin)
	}
}
