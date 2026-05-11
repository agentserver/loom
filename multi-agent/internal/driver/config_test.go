package driver

import (
	"os"
	"path/filepath"
	"testing"
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
}

func TestLoadConfig_ObserverURLWithEnabledFalseKeepsDisabledAndDefaultsAgentID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "driver.yaml")
	if err := os.WriteFile(path, []byte(`
server: {url: https://x, name: drv}
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
