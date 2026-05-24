package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

func TestPermissionsGetMissingReturnsAskMode(t *testing.T) {
	s := NewStore(t.TempDir())
	st, err := s.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode != "ask" {
		t.Fatalf("Mode=%q want ask", st.Mode)
	}
	if st.Backend != agentbackend.KindCodex {
		t.Fatalf("Backend=%v", st.Backend)
	}
}

func TestPermissionsPatchFullAccess(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	st, err := s.Patch(context.Background(), agentbackend.Patch{Presets: []string{"full_access"}})
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode != "full-access" {
		t.Fatalf("Mode=%q want full-access", st.Mode)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".codex", "config.toml"))
	if !strings.Contains(string(data), `approval_policy = "never"`) {
		t.Fatalf("config.toml missing approval_policy line:\n%s", data)
	}
	if !strings.Contains(string(data), `sandbox_mode = "danger-full-access"`) {
		t.Fatalf("config.toml missing sandbox_mode line:\n%s", data)
	}
}

func TestPermissionsPatchFileWriteSetsWorkspaceWrite(t *testing.T) {
	s := NewStore(t.TempDir())
	st, err := s.Patch(context.Background(), agentbackend.Patch{Presets: []string{"file_write"}})
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode != "workspace-write" {
		t.Fatalf("Mode=%q want workspace-write", st.Mode)
	}
}

func TestPermissionsPatchRejectsAllowAdd(t *testing.T) {
	s := NewStore(t.TempDir())
	_, err := s.Patch(context.Background(), agentbackend.Patch{AllowAdd: []string{"Bash(rm *)"}})
	if err == nil || !strings.Contains(err.Error(), "AllowAdd") {
		t.Fatalf("expected AllowAdd-rejection, got %v", err)
	}
}

func TestPermissionsPatchModePassthrough(t *testing.T) {
	s := NewStore(t.TempDir())
	st, err := s.Patch(context.Background(), agentbackend.Patch{Mode: "workspace-write"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode != "workspace-write" {
		t.Fatalf("Mode=%q want workspace-write", st.Mode)
	}
}

func TestPermissionsPatchIdempotent(t *testing.T) {
	s := NewStore(t.TempDir())
	p := agentbackend.Patch{Presets: []string{"full_access"}}
	a, err := s.Patch(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Patch(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if a.Mode != b.Mode {
		t.Fatalf("not idempotent: %v vs %v", a, b)
	}
}

// TestPermissionsPatchPreservesUnknownKeys verifies that a user-written
// config.toml with mcp_servers tables and other top-level scalars survives a
// Patch — Patch must only mutate approval_policy and sandbox_mode.
func TestPermissionsPatchPreservesUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	seed := `model = "o3"

[mcp_servers.driver]
command = "/usr/local/bin/driver-agent"
args = ["serve-mcp", "--config", "/etc/loom/config.yaml"]
enabled = true
`
	if err := os.WriteFile(filepath.Join(dir, ".codex", "config.toml"), []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	s := NewStore(dir)
	if _, err := s.Patch(context.Background(), agentbackend.Patch{Presets: []string{"full_access"}}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, want := range []string{
		`model = "o3"`,
		`[mcp_servers.driver]`,
		`command = "/usr/local/bin/driver-agent"`,
		`approval_policy = "never"`,
		`sandbox_mode = "danger-full-access"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("config.toml missing %q after Patch:\n%s", want, body)
		}
	}
}
