package dispatch

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/salve_agent/internal/executor"
	"github.com/yourorg/salve_agent/internal/store"
)

type stubExec struct {
	res    executor.Result
	err    error
	called bool
}

func (s *stubExec) Run(ctx context.Context, t executor.Task, sink executor.Sink) (executor.Result, error) {
	s.called = true
	sink.Close()
	return s.res, s.err
}

// blockingExec blocks until its context is cancelled, then returns the ctx.Err().
type blockingExec struct {
	ctxDone chan struct{}
}

func newBlockingExec() *blockingExec {
	return &blockingExec{ctxDone: make(chan struct{})}
}

func (b *blockingExec) Run(ctx context.Context, t executor.Task, sink executor.Sink) (executor.Result, error) {
	defer sink.Close()
	<-ctx.Done()
	close(b.ctxDone)
	return executor.Result{}, ctx.Err()
}

type stubJournal struct {
	calls      int
	lastChange string
}

func (j *stubJournal) Record(ctx context.Context, t executor.Task, r executor.Result) error {
	j.calls++
	j.lastChange = r.CapabilityChange
	return nil
}

func newStore(t *testing.T) *store.Store {
	s, err := store.Open(filepath.Join(t.TempDir(), "x.db"))
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

func TestRoute_DefaultExecutor(t *testing.T) {
	def := &stubExec{res: executor.Result{Summary: "ok"}}
	mcp := &stubExec{}
	j := &stubJournal{}
	d := New(map[string]executor.Executor{"mcp": mcp, "": def}, j, newStore(t))
	_, err := d.Run(context.Background(), executor.Task{ID: "t", Skill: "chat"})
	require.NoError(t, err)
	require.True(t, def.called)
	require.False(t, mcp.called)
}

func TestRoute_MCPSkill(t *testing.T) {
	def := &stubExec{}
	mcp := &stubExec{res: executor.Result{Summary: "m"}}
	d := New(map[string]executor.Executor{"mcp": mcp, "": def}, &stubJournal{}, newStore(t))
	_, err := d.Run(context.Background(), executor.Task{ID: "t", Skill: "mcp"})
	require.NoError(t, err)
	require.True(t, mcp.called)
	require.False(t, def.called)
}

func TestFailed_SkipsJournal(t *testing.T) {
	def := &stubExec{err: errors.New("bad")}
	j := &stubJournal{}
	d := New(map[string]executor.Executor{"": def}, j, newStore(t))
	_, err := d.Run(context.Background(), executor.Task{ID: "t"})
	require.Error(t, err)
	require.Equal(t, 0, j.calls)
}

func TestNoCapabilityChange_SkipsJournal(t *testing.T) {
	def := &stubExec{res: executor.Result{Summary: "ok"}}
	j := &stubJournal{}
	d := New(map[string]executor.Executor{"": def}, j, newStore(t))
	_, err := d.Run(context.Background(), executor.Task{ID: "t"})
	require.NoError(t, err)
	require.Equal(t, 0, j.calls)
}

func TestCapabilityChange_CallsJournal(t *testing.T) {
	def := &stubExec{res: executor.Result{Summary: "ok", CapabilityChange: "x"}}
	j := &stubJournal{}
	d := New(map[string]executor.Executor{"": def}, j, newStore(t))
	_, err := d.Run(context.Background(), executor.Task{ID: "t"})
	require.NoError(t, err)
	require.Equal(t, 1, j.calls)
	require.Equal(t, "x", j.lastChange)
}

// TestRespectsTaskTimeout verifies that a per-task TimeoutSec is enforced: the
// executor's context must be cancelled within the deadline even though the
// parent context has no deadline of its own.
func TestRespectsTaskTimeout(t *testing.T) {
	blk := newBlockingExec()
	j := &stubJournal{}
	d := New(map[string]executor.Executor{"": blk}, j, newStore(t))

	// Parent context has no deadline; task carries a 1-second timeout.
	parentCtx := context.Background()
	start := time.Now()
	_, err := d.Run(parentCtx, executor.Task{ID: "t", TimeoutSec: 1})
	elapsed := time.Since(start)

	// The blocking executor's context must have been cancelled.
	select {
	case <-blk.ctxDone:
		// good – executor ctx was cancelled
	case <-time.After(100 * time.Millisecond):
		t.Fatal("executor ctx was not cancelled after task returned")
	}

	require.Error(t, err, "expected timeout error")
	require.Less(t, elapsed, 5*time.Second, "timeout took longer than expected")
	require.Greater(t, elapsed, 500*time.Millisecond, "timeout fired too early")
}
