package claude

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

func TestBackendRunResumeUsesSessionWorkingDir(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	sessionDir := t.TempDir()
	configDir := t.TempDir()

	id := "resume-cwd-session"
	encodedCwd := encodeCwd(sessionDir)
	sessionRoot := filepath.Join(home, ".claude", "projects", encodedCwd)
	if err := os.MkdirAll(sessionRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"assistant","timestamp":"2026-06-16T01:00:00Z","sessionId":"` + id + `","message":{"role":"assistant","content":"ok"}}` + "\n"
	if err := os.WriteFile(filepath.Join(sessionRoot, id+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cwdPath := filepath.Join(home, "cwd.txt")
	fakeBin := buildFakeClaude(t, fmt.Sprintf(`package main
import (
	"fmt"
	"io"
	"os"
)
func main() {
	cwd, _ := os.Getwd()
	_ = os.WriteFile(%q, []byte(cwd), 0o600)
	_, _ = io.Copy(io.Discard, os.Stdin)
	fmt.Println(`+"`"+`{"type":"system","session_id":"resume-cwd-session"}`+"`"+`)
	fmt.Println(`+"`"+`{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}]}}`+"`"+`)
}
`, cwdPath))

	b := New(agentbackend.Config{Bin: fakeBin, WorkDir: configDir}, nil)
	if _, err := b.RunResume(context.Background(), id, "continue", &captureSink{}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(cwdPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != sessionDir {
		t.Fatalf("resume cwd=%q want session WorkingDir %q (config WorkDir was %q)", got, sessionDir, configDir)
	}
}

func TestSessionWorkingDirUsesProjectDirectory(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	sessionDir := t.TempDir()

	id := "resume-cwd-metadata-only"
	encodedCwd := encodeCwd(sessionDir)
	sessionRoot := filepath.Join(home, ".claude", "projects", encodedCwd)
	if err := os.MkdirAll(sessionRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionRoot, id+".jsonl"), []byte("not parsed by cwd lookup\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	b := New(agentbackend.Config{Bin: "claude", WorkDir: t.TempDir()}, nil)
	got, ok, err := b.sessionWorkingDir(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("sessionWorkingDir did not find session")
	}
	if got != sessionDir {
		t.Fatalf("sessionWorkingDir=%q want %q", got, sessionDir)
	}
}
