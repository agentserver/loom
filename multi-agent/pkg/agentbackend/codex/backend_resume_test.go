package codex

import (
	"context"
	"encoding/json"
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

	id := "dddddddd-1111-2222-3333-eeeeeeeeeeee"
	sessionRoot := filepath.Join(home, ".codex", "sessions", "2026", "06", "16")
	if err := os.MkdirAll(sessionRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	meta, err := json.Marshal(map[string]any{
		"timestamp": "2026-06-16T01:00:00Z",
		"type":      "session_meta",
		"payload": map[string]string{
			"id":  id,
			"cwd": sessionDir,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionRoot, "rollout-2026-06-16T01-00-00-"+id+".jsonl"), append(meta, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	cwdPath := filepath.Join(home, "cwd.txt")
	fakeBin := buildFakeCodex(t, fmt.Sprintf(`package main
import (
	"fmt"
	"io"
	"os"
)
func main() {
	cwd, _ := os.Getwd()
	_ = os.WriteFile(%q, []byte(cwd), 0o600)
	_, _ = io.Copy(io.Discard, os.Stdin)
	fmt.Println(`+"`"+`{"type":"thread.started","thread_id":"resumed"}`+"`"+`)
	fmt.Println(`+"`"+`{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}`+"`"+`)
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
