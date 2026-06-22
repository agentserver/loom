package agentbackend

import (
	"path/filepath"
	"testing"
)

func TestResolveCodexHome(t *testing.T) {
	t.Run("explicit codexHome wins", func(t *testing.T) {
		got := ResolveCodexHome("/explicit", "/loom", "drv-1")
		if got != "/explicit" {
			t.Fatalf("got %q, want /explicit", got)
		}
	})

	t.Run("empty shortID returns empty", func(t *testing.T) {
		got := ResolveCodexHome("", "/loom", "")
		if got != "" {
			t.Fatalf("got %q, want empty", got)
		}
	})

	t.Run("loomHome arg is used", func(t *testing.T) {
		got := ResolveCodexHome("", "/loom", "drv-1")
		want := "/loom/drv-1/.codex"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("LOOM_HOME env used when loomHome empty", func(t *testing.T) {
		t.Setenv("LOOM_HOME", "/env-loom")
		got := ResolveCodexHome("", "", "drv-1")
		want := "/env-loom/drv-1/.codex"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("falls back to HOME/.cache/multi-agent", func(t *testing.T) {
		t.Setenv("LOOM_HOME", "")
		home := t.TempDir()
		// Set both HOME (Unix) and USERPROFILE (Windows) so os.UserHomeDir resolves
		// cross-platform (Linux uses HOME, Windows uses USERPROFILE).
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		got := ResolveCodexHome("", "", "drv-1")
		want := filepath.Join(home, ".cache", "multi-agent", "drv-1", ".codex")
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("unresolvable home returns empty", func(t *testing.T) {
		t.Setenv("LOOM_HOME", "")
		// Clear both HOME (Unix) and USERPROFILE (Windows) to make UserHomeDir fail.
		t.Setenv("HOME", "")
		t.Setenv("USERPROFILE", "")
		got := ResolveCodexHome("", "", "drv-1")
		if got != "" {
			t.Fatalf("got %q, want empty (unresolvable home)", got)
		}
	})
}
