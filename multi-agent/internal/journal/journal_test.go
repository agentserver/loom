package journal

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/executor"
)

func fakeTextClaude(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	p, err := filepath.Abs(filepath.Join(filepath.Dir(file), "../../testdata/fake-claude-text.sh"))
	require.NoError(t, err)
	return p
}

func TestRecord_FirstWriteCreatesFile(t *testing.T) {
	dir := t.TempDir()
	j, err := New(Config{Dir: dir, ClaudeBin: fakeTextClaude(t)})
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
	j, _ := New(Config{Dir: dir, ClaudeBin: fakeTextClaude(t), Env: []string{"FAKE_CLAUDE_TEXT_MODE=fail"}})
	err := j.Record(context.Background(), executor.Task{ID: "t2", Skill: "chat"}, executor.Result{CapabilityChange: "tried"})
	require.NoError(t, err) // journal.Record never errors out: it logs and degrades

	_, err = os.Stat(filepath.Join(dir, "CURRENT_STATE.md"))
	require.True(t, os.IsNotExist(err), "CURRENT_STATE.md must NOT be created on merge failure")

	hist, _ := os.ReadFile(filepath.Join(dir, "history.md"))
	require.Contains(t, string(hist), "[merge failed:")
	require.Contains(t, string(hist), "tried")
}

// helper to silence imports
var _ = strings.HasPrefix
