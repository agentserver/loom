package claude

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

func TestLLMRunnerEchoesStdin(t *testing.T) {
	dir := t.TempDir()
	fakeBin := filepath.Join(dir, "claude")
	script := "#!/usr/bin/env bash\nread line\nprintf '%s\\n' \"$line\"\n"
	if err := os.WriteFile(fakeBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := agentbackend.ClaudeConfig{Bin: fakeBin, ExtraArgs: nil}
	llm := newLLM(cfg, nil)
	out, err := llm.Run(context.Background(), "ping")
	if err != nil {
		t.Fatal(err)
	}
	if out != "ping" {
		t.Fatalf("out=%q want %q", out, "ping")
	}
}
