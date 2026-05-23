package claude

import (
	"context"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

type captureSink struct {
	mu     sync.Mutex
	events []struct{ Type, Data string }
	closed bool
}

func (c *captureSink) Write(t, d string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, struct{ Type, Data string }{t, d})
}
func (c *captureSink) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
}

func fakeClaudePath(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	p, err := filepath.Abs(filepath.Join(filepath.Dir(file), "../../../testdata/fake-claude.sh"))
	require.NoError(t, err)
	return p
}

func TestExecutorParsesAssistantText(t *testing.T) {
	b := New(agentbackend.ClaudeConfig{
		Bin:     fakeClaudePath(t),
		WorkDir: t.TempDir(),
	}, []string{"FAKE_CLAUDE_MODE=normal"})
	sink := &captureSink{}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := b.Run(ctx, agentbackend.Task{Prompt: "ignored"}, sink)
	require.NoError(t, err)
	require.Equal(t, "hello world", res.Summary)
	require.Equal(t, "", res.CapabilityChange)
	require.True(t, sink.closed)

	var got string
	for _, ev := range sink.events {
		if ev.Type == "chunk" {
			got += ev.Data
		}
	}
	require.Equal(t, "hello world", got)
}

func TestExecutor_CapabilityParsed(t *testing.T) {
	b := New(agentbackend.ClaudeConfig{
		Bin:     fakeClaudePath(t),
		WorkDir: t.TempDir(),
	}, []string{"FAKE_CLAUDE_MODE=capability"})
	sink := &captureSink{}
	res, err := b.Run(context.Background(), agentbackend.Task{Prompt: "ignored"}, sink)
	require.NoError(t, err)
	require.Equal(t, "installed foo", res.Summary)
	require.Equal(t, "foo CLI now available", res.CapabilityChange)

	var sawCap bool
	for _, ev := range sink.events {
		if ev.Type == "capability" && ev.Data == "foo CLI now available" {
			sawCap = true
		}
	}
	require.True(t, sawCap, "expected capability event before close")
}

func TestExecutor_NoCapabilityChange(t *testing.T) {
	b := New(agentbackend.ClaudeConfig{
		Bin:     fakeClaudePath(t),
		WorkDir: t.TempDir(),
	}, []string{"FAKE_CLAUDE_MODE=nochange"})
	sink := &captureSink{}
	res, err := b.Run(context.Background(), agentbackend.Task{Prompt: "ignored"}, sink)
	require.NoError(t, err)
	require.Equal(t, "answer", res.Summary)
	require.Equal(t, "", res.CapabilityChange)
}

func TestExecutor_Exit1(t *testing.T) {
	b := New(agentbackend.ClaudeConfig{
		Bin:     fakeClaudePath(t),
		WorkDir: t.TempDir(),
	}, []string{"FAKE_CLAUDE_MODE=exit1"})
	sink := &captureSink{}
	_, err := b.Run(context.Background(), agentbackend.Task{Prompt: "ignored"}, sink)
	require.Error(t, err)
	require.Contains(t, err.Error(), "boom")
}

func TestExecutor_Timeout(t *testing.T) {
	b := New(agentbackend.ClaudeConfig{
		Bin:     fakeClaudePath(t),
		WorkDir: t.TempDir(),
	}, []string{"FAKE_CLAUDE_MODE=sleep", "FAKE_CLAUDE_SLEEP=10"})
	sink := &captureSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, err := b.Run(ctx, agentbackend.Task{Prompt: "ignored"}, sink)
	require.Error(t, err)
	require.Contains(t, err.Error(), "timeout")
}

func TestExecutor_GarbageLines_StillCompletes(t *testing.T) {
	b := New(agentbackend.ClaudeConfig{
		Bin:     fakeClaudePath(t),
		WorkDir: t.TempDir(),
	}, []string{"FAKE_CLAUDE_MODE=garbage"})
	sink := &captureSink{}
	res, err := b.Run(context.Background(), agentbackend.Task{Prompt: "ignored"}, sink)
	require.NoError(t, err)
	require.Equal(t, "ok", res.Summary)
}
