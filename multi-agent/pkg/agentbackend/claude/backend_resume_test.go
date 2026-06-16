package claude

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

func TestBackendRunResumeUsesSessionWorkingDir(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	sessionDir := t.TempDir()
	configDir := t.TempDir()

	id := "resume-cwd-session"
	encodedCwd := strings.ReplaceAll(sessionDir, string(filepath.Separator), "-")
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
