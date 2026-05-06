//go:build smoke

package smoke

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/executor"
)

type captureSink struct{ data strings.Builder; closed bool }

func (c *captureSink) Write(_, d string) { c.data.WriteString(d) }
func (c *captureSink) Close()            { c.closed = true }

func TestSmoke_RealClaude(t *testing.T) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude binary not on PATH")
	}
	e := executor.NewClaudeExecutor(executor.ClaudeConfig{Bin: "claude"})
	sink := &captureSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := e.Run(ctx, executor.Task{
		ID: "smoke", Prompt: "Reply with only the digit 2 (no words).",
	}, sink)
	require.NoError(t, err)
	require.Contains(t, res.Summary, "2")
	require.True(t, sink.closed)
}
