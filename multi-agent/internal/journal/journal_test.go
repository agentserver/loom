package journal

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/executor"
)

// fakeLLM implements LLMRunner inline so the journal tests don't have
// to shell out. Two modes: an "ok" mode that returns a deterministic
// merged doc, and a "fail" mode that returns an error to exercise the
// merge-failure degradation path.
type fakeLLM struct {
	fail bool
}

func (f fakeLLM) Run(_ context.Context, _ string) (string, error) {
	if f.fail {
		return "", fmt.Errorf("merge failed")
	}
	return "## Tools\n- updated by fake merge\n\n## MCP Servers\n- (none)\n", nil
}

func TestRecord_FirstWriteCreatesFile(t *testing.T) {
	dir := t.TempDir()
	j, err := New(Config{Dir: dir, LLM: fakeLLM{}})
	require.NoError(t, err)
	err = j.Record(context.Background(), executor.Task{ID: "t1", Skill: "mcp"}, executor.Result{CapabilityChange: "did x"})
	require.NoError(t, err)

	body, err := os.ReadFile(filepath.Join(dir, "CURRENT_STATE.md"))
	require.NoError(t, err)
	require.Contains(t, string(body), "updated by fake merge")

	hist, err := os.ReadFile(filepath.Join(dir, "history.md"))
	require.NoError(t, err)
	require.Contains(t, string(hist), "t1")
	require.Contains(t, string(hist), "did x")
}

func TestRecord_MergeFailureStillAppendsHistory(t *testing.T) {
	dir := t.TempDir()
	j, _ := New(Config{Dir: dir, LLM: fakeLLM{fail: true}})
	err := j.Record(context.Background(), executor.Task{ID: "t2", Skill: "chat"}, executor.Result{CapabilityChange: "tried"})
	require.NoError(t, err) // journal.Record never errors out: it logs and degrades

	_, err = os.Stat(filepath.Join(dir, "CURRENT_STATE.md"))
	require.True(t, os.IsNotExist(err), "CURRENT_STATE.md must NOT be created on merge failure")

	hist, _ := os.ReadFile(filepath.Join(dir, "history.md"))
	require.Contains(t, string(hist), "[merge failed:")
	require.Contains(t, string(hist), "tried")
}
