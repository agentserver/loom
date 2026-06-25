package agentbackend

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveCodexHome(t *testing.T) {
	t.Run("explicit codexHome wins (overrides workdir + shortID)", func(t *testing.T) {
		got := ResolveCodexHome("/explicit", "/loom", "drv-1", "/some/workdir")
		if got != "/explicit" {
			t.Fatalf("got %q, want /explicit", got)
		}
	})

	t.Run("workdir set (absolute) overrides loomHome+shortID", func(t *testing.T) {
		got := ResolveCodexHome("", "/loom", "drv-1", "/abs/workdir")
		want := "/abs/workdir/.codex"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("workdir set (relative) is absolutized before joining .codex", func(t *testing.T) {
		// Switch cwd to a known directory so we can assert the absolute form.
		dir := t.TempDir()
		oldwd, err := os.Getwd()
		if err != nil {
			t.Fatalf("Getwd: %v", err)
		}
		t.Cleanup(func() { _ = os.Chdir(oldwd) })
		if err := os.Chdir(dir); err != nil {
			t.Fatalf("Chdir: %v", err)
		}
		got := ResolveCodexHome("", "/loom", "drv-1", "./repo")
		// On macOS the temp dir may resolve through /private/...; use EvalSymlinks
		// for the wanted absolute form to match what filepath.Abs returns.
		wantBase, err := filepath.Abs("./repo")
		if err != nil {
			t.Fatalf("filepath.Abs: %v", err)
		}
		want := filepath.Join(wantBase, ".codex")
		if got != want {
			t.Fatalf("got %q, want %q (absolutized form of ./repo + .codex)", got, want)
		}
	})

	t.Run("workdir empty falls back to loomHome+shortID legacy path", func(t *testing.T) {
		got := ResolveCodexHome("", "/loom", "drv-1", "")
		want := "/loom/drv-1/.codex"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("empty shortID + empty workdir returns empty", func(t *testing.T) {
		got := ResolveCodexHome("", "/loom", "", "")
		if got != "" {
			t.Fatalf("got %q, want empty", got)
		}
	})

	t.Run("LOOM_HOME env used when loomHome empty and workdir empty", func(t *testing.T) {
		t.Setenv("LOOM_HOME", "/env-loom")
		got := ResolveCodexHome("", "", "drv-1", "")
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
		got := ResolveCodexHome("", "", "drv-1", "")
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
		got := ResolveCodexHome("", "", "drv-1", "")
		if got != "" {
			t.Fatalf("got %q, want empty (unresolvable home)", got)
		}
	})
}
