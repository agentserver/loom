package codex

import (
	"context"
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

func TestLLMRunnerReturnsTrimmedStdout(t *testing.T) {
	fakeBin := buildFakeCodex(t, `package main
import (
	"io"
	"os"
)
func main() {
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Stdout.Write([]byte("   pong   \n\n"))
}
`)
	llm := newLLM(agentbackend.CodexConfig{Bin: fakeBin}, nil)
	out, err := llm.Run(context.Background(), "ping")
	if err != nil {
		t.Fatal(err)
	}
	if out != "pong" {
		t.Fatalf("out=%q want %q", out, "pong")
	}
}
