package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestSmoke launches driver-agent against a deliberately-broken config
// (server.url unreachable) but only exercises tools/list. The driver's MCP
// loop runs even when the tunnel goroutine is failing, so tools/list returns
// before the test times out.
func TestSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("smoke spawns a child process")
	}
	dir := t.TempDir()
	cfg := filepath.Join(dir, "driver.yaml")
	writeFixture(t, cfg)

	bin := filepath.Join(dir, "driver-agent")
	build := exec.Command("go", "build", "-o", bin,
		"github.com/yourorg/multi-agent/cmd/driver-agent")
	build.Dir = repoRoot()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	exe := exec.Command("go", "run", ".",
		"--driver-bin", bin, "--driver-config", cfg, "--mode", "smoke")
	exe.Dir = filepath.Join(repoRoot(), "examples", "generic-driver", "e2e")
	out, err := exe.CombinedOutput()
	if err != nil {
		t.Fatalf("smoke: %v\n%s", err, out)
	}
}

func writeFixture(t *testing.T, path string) {
	t.Helper()
	yaml := `
server: {url: "http://127.0.0.1:1", name: "smoke-driver"}
credentials:
  sandbox_id: "sbx-smoke"
  tunnel_token: "ttok"
  proxy_token: "ptok"
  workspace_id: "ws-smoke"
  short_id: "smoke"
agent:
  kind: claude
  bin: claude
  workdir: "` + filepath.Dir(path) + `"
discovery:
  display_name: smoke-driver
  description: "smoke test"
  skills: []
listen_addr: "127.0.0.1:0"
driver_defaults:
  task_timeout_sec: 5
  audit_log_dir: "` + filepath.Dir(path) + `"
  disable_uid_check: true
  max_dir_cache_entries: 100
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
}

func repoRoot() string {
	_, here, _, _ := runtime.Caller(0)
	// here = .../multi-agent/examples/generic-driver/e2e/smoke_test.go
	return filepath.Join(filepath.Dir(here), "..", "..", "..")
}
