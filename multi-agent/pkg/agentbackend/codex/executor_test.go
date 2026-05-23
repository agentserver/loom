package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

type captureSink struct {
	chunks []string
	closed bool
}

func (c *captureSink) Write(_, text string) { c.chunks = append(c.chunks, text) }
func (c *captureSink) Close()               { c.closed = true }

func TestExecutorReplaysFixture(t *testing.T) {
	fix, err := os.ReadFile("testdata/codex_exec.ndjson")
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	fakeBin := filepath.Join(dir, "codex")
	script := "#!/usr/bin/env bash\ncat >/dev/null\ncat <<'EOF'\n" + string(fix) + "\nEOF\n"
	if err := os.WriteFile(fakeBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	b := New(agentbackend.CodexConfig{Bin: fakeBin, WorkDir: dir}, nil)
	sink := &captureSink{}
	res, err := b.Run(context.Background(), agentbackend.Task{Prompt: "ignored"}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if !sink.closed {
		t.Fatal("sink not closed")
	}
	if res.Summary == "" {
		t.Fatal("empty summary")
	}
	if !strings.Contains(res.Summary, "pong") {
		t.Fatalf("summary %q does not contain pong", res.Summary)
	}
}
