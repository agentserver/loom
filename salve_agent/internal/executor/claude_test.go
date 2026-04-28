package executor

import (
	"context"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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
	p, err := filepath.Abs(filepath.Join(filepath.Dir(file), "../../testdata/fake-claude.sh"))
	require.NoError(t, err)
	return p
}

func TestClaude_NormalStreaming(t *testing.T) {
	e := NewClaudeExecutor(ClaudeConfig{Bin: fakeClaudePath(t), Env: []string{"FAKE_CLAUDE_MODE=normal"}})
	sink := &captureSink{}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := e.Run(ctx, Task{ID: "t1", Prompt: "hi"}, sink)
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
