package codex

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

func TestLLMRunnerReturnsTrimmedStdout(t *testing.T) {
	dir := t.TempDir()
	fakeBin := filepath.Join(dir, "codex")
	script := "#!/usr/bin/env bash\ncat >/dev/null\nprintf '   pong   \\n\\n'\n"
	if err := os.WriteFile(fakeBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	llm := newLLM(agentbackend.CodexConfig{Bin: fakeBin}, nil)
	out, err := llm.Run(context.Background(), "ping")
	if err != nil {
		t.Fatal(err)
	}
	if out != "pong" {
		t.Fatalf("out=%q want %q", out, "pong")
	}
}
