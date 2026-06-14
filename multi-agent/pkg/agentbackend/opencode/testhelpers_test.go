package opencode

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// goBuildFake compiles `source` into an executable named `name` (+.exe on
// Windows) under t.TempDir() and returns the absolute path. Used by
// fake-bin helpers in this package's tests. Mirrors the buildFakeCodex
// pattern at pkg/agentbackend/codex/executor_test.go:281.
func goBuildFake(t *testing.T, source, name string) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, name)
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", exe, src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fake %s: %v\n%s", name, err, out)
	}
	return exe
}
