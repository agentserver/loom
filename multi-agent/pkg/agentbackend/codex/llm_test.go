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
	llm := newLLM(agentbackend.Config{Bin: fakeBin}, nil)
	out, err := llm.Run(context.Background(), "ping")
	if err != nil {
		t.Fatal(err)
	}
	if out != "pong" {
		t.Fatalf("out=%q want %q", out, "pong")
	}
}

func TestLLMRunnerSubprocessEnvHasSingleCodexHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", "/stale-from-process")
	t.Setenv("Codex_Home", "/stale-case-variant")
	fakeBin := buildFakeCodex(t, `package main
import (
	"fmt"
	"io"
	"os"
	"strings"
)
func main() {
	_, _ = io.Copy(io.Discard, os.Stdin)
	var lines []string
	for _, e := range os.Environ() {
		k, _, ok := strings.Cut(e, "=")
		if ok && strings.EqualFold(k, "CODEX_HOME") {
			lines = append(lines, e)
		}
	}
	fmt.Print(strings.Join(lines, "\n"))
}
`)
	llm := newLLM(agentbackend.Config{Bin: fakeBin}, []string{"CODEX_HOME=" + home})
	got, err := llm.Run(context.Background(), "ping")
	if err != nil {
		t.Fatal(err)
	}
	if got != "CODEX_HOME="+home {
		t.Fatalf("llm subprocess CODEX_HOME lines = %q, want exactly CODEX_HOME=%s", got, home)
	}
}
