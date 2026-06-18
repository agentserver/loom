package codex

import (
	"context"
	"encoding/json"
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
	codexHome := t.TempDir()

	id := "dddddddd-1111-2222-3333-eeeeeeeeeeee"
	sessionRoot := filepath.Join(codexHome, "sessions", "2026", "06", "16")
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
	argsPath := filepath.Join(home, "args.txt")
	envPath := filepath.Join(home, "env.txt")
	fakeBin := buildFakeCodex(t, fmt.Sprintf(`package main
import (
	"fmt"
	"io"
	"os"
	"strings"
)
func main() {
	cwd, _ := os.Getwd()
	_ = os.WriteFile(%q, []byte(cwd), 0o600)
	_ = os.WriteFile(%q, []byte(strings.Join(os.Args[1:], "\n")), 0o600)
	_ = os.WriteFile(%q, []byte(os.Getenv("CODEX_HOME")+"|"+os.Getenv("LOOM_BACKEND_ENV")), 0o600)
	_, _ = io.Copy(io.Discard, os.Stdin)
	fmt.Println(`+"`"+`{"type":"thread.started","thread_id":"resumed"}`+"`"+`)
	fmt.Println(`+"`"+`{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}`+"`"+`)
}
`, cwdPath, argsPath, envPath))

	b := New(agentbackend.Config{
		Bin:       fakeBin,
		WorkDir:   configDir,
		ExtraArgs: []string{"--profile", "loom-test"},
	}, []string{"CODEX_HOME=" + codexHome, "LOOM_BACKEND_ENV=present"})
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

	argsRaw, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(string(argsRaw), "\n")
	if len(args) < 3 || args[0] != "exec" || args[1] != "resume" || args[2] != id {
		t.Fatalf("resume argv head=%v want [exec resume %s]", args, id)
	}
	argsJoined := strings.Join(args, "\n")
	for _, want := range []string{"--profile", "loom-test", "mcp_servers.loom_humanloop.command", "humanloop-mcp"} {
		if !strings.Contains(argsJoined, want) {
			t.Fatalf("resume argv missing %q:\n%s", want, argsJoined)
		}
	}

	envRaw, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(envRaw), codexHome+"|present"; got != want {
		t.Fatalf("resume env=%q want %q", got, want)
	}
}

func TestSessionWorkingDirReadsOnlyMetadata(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	sessionDir := t.TempDir()

	id := "eeeeeeee-1111-2222-3333-ffffffffffff"
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
	body := string(meta) + "\n" + `{"timestamp":"2026-06-16T01:00:01Z","type":"response_item","payload":{"type":"function_call_output","content":"ignored"}}` + "\n"
	if err := os.WriteFile(filepath.Join(sessionRoot, "rollout-2026-06-16T01-00-00-"+id+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	b := New(agentbackend.Config{Bin: "codex", WorkDir: t.TempDir()}, nil)
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
