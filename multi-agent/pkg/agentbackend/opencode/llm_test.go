package opencode

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// TestLLMRunnerReturnsTrimmedStdout pins the LLMRunner contract: spawn
// the configured bin, feed prompt via stdin, return trimmed stdout.
// Mirrors pkg/agentbackend/codex/llm_test.go.
func TestLLMRunnerReturnsTrimmedStdout(t *testing.T) {
	fakeBin := buildFakeOpencode(t, `package main
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

// TestLLMRunner_SurfacesStderrTailOnExit pins that exec failure wraps
// stderr tail (operators need to see why opencode bailed).
func TestLLMRunner_SurfacesStderrTailOnExit(t *testing.T) {
	fakeBin := buildFakeOpencode(t, `package main
import (
	"fmt"
	"os"
)
func main() {
	fmt.Fprintln(os.Stderr, "OPENCODE_ERROR_MARKER: provider auth failed")
	os.Exit(2)
}
`)
	llm := newLLM(agentbackend.Config{Bin: fakeBin}, nil)
	_, err := llm.Run(context.Background(), "ping")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "OPENCODE_ERROR_MARKER") {
		t.Fatalf("err=%v want stderr tail with marker", err)
	}
}

// TestLLMRunner_ExtraArgsAppended pins that cfg.ExtraArgs are appended
// AFTER the default flags (operator overrides survive the default
// --dangerously-skip-permissions injection).
func TestLLMRunner_ExtraArgsAppended(t *testing.T) {
	// Fake bin emits its argv as the only stdout line so we can inspect it.
	fakeBin := buildFakeOpencode(t, `package main
import (
	"fmt"
	"io"
	"os"
	"strings"
)
func main() {
	_, _ = io.Copy(io.Discard, os.Stdin)
	fmt.Print(strings.Join(os.Args[1:], "|"))
}
`)
	llm := newLLM(agentbackend.Config{Bin: fakeBin, ExtraArgs: []string{"--model", "anthropic/claude-3"}}, nil)
	out, err := llm.Run(context.Background(), "ping")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "--dangerously-skip-permissions") {
		t.Fatalf("default permissions flag missing: %s", out)
	}
	if !strings.Contains(out, "--model|anthropic/claude-3") {
		t.Fatalf("extra args missing: %s", out)
	}
	// Order: defaults first, extras after
	defaultIdx := strings.Index(out, "--dangerously-skip-permissions")
	extraIdx := strings.Index(out, "--model")
	if defaultIdx > extraIdx {
		t.Fatalf("extras should come AFTER defaults: %s", out)
	}
	_ = fmt.Sprintf("") // silence unused import if any
}

// buildFakeOpencode compiles a Go source as a fake opencode bin and returns
// its path. Mirrors pkg/agentbackend/codex/executor_test.go:buildFakeCodex.
func buildFakeOpencode(t *testing.T, source string) string {
	t.Helper()
	return goBuildFake(t, source, "opencode")
}
