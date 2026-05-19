package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/contract"
	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/internal/observer"
	"github.com/yourorg/multi-agent/internal/store"
)

type stubExec struct {
	res    executor.Result
	err    error
	called bool
	gotTask executor.Task
}

func (s *stubExec) Run(ctx context.Context, t executor.Task, sink executor.Sink) (executor.Result, error) {
	s.called = true
	s.gotTask = t
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

type fakeObserver struct {
	events []observer.Event
}

func (f *fakeObserver) Emit(ev observer.Event) {
	f.events = append(f.events, ev)
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
	d := New(map[string]executor.Executor{"mcp": mcp, "": def}, j, newStore(t), nil)
	_, err := d.Run(context.Background(), executor.Task{ID: "t", Skill: "chat"})
	require.NoError(t, err)
	require.True(t, def.called)
	require.False(t, mcp.called)
}

func TestRoute_MCPSkill(t *testing.T) {
	def := &stubExec{}
	mcp := &stubExec{res: executor.Result{Summary: "m"}}
	d := New(map[string]executor.Executor{"mcp": mcp, "": def}, &stubJournal{}, newStore(t), nil)
	_, err := d.Run(context.Background(), executor.Task{ID: "t", Skill: "mcp"})
	require.NoError(t, err)
	require.True(t, mcp.called)
	require.False(t, def.called)
}

func TestFailed_SkipsJournal(t *testing.T) {
	def := &stubExec{err: errors.New("bad")}
	j := &stubJournal{}
	d := New(map[string]executor.Executor{"": def}, j, newStore(t), nil)
	_, err := d.Run(context.Background(), executor.Task{ID: "t"})
	require.Error(t, err)
	require.Equal(t, 0, j.calls)
}

func TestNoCapabilityChange_SkipsJournal(t *testing.T) {
	def := &stubExec{res: executor.Result{Summary: "ok"}}
	j := &stubJournal{}
	d := New(map[string]executor.Executor{"": def}, j, newStore(t), nil)
	_, err := d.Run(context.Background(), executor.Task{ID: "t"})
	require.NoError(t, err)
	require.Equal(t, 0, j.calls)
}

func TestCapabilityChange_CallsJournal(t *testing.T) {
	def := &stubExec{res: executor.Result{Summary: "ok", CapabilityChange: "x"}}
	j := &stubJournal{}
	d := New(map[string]executor.Executor{"": def}, j, newStore(t), nil)
	_, err := d.Run(context.Background(), executor.Task{ID: "t"})
	require.NoError(t, err)
	require.Equal(t, 1, j.calls)
	require.Equal(t, "x", j.lastChange)
}

func TestDispatcher_EmitsObserverLifecycleEvents(t *testing.T) {
	def := &stubExec{res: executor.Result{Summary: "ok"}}
	obs := &fakeObserver{}
	d := New(map[string]executor.Executor{"": def}, &stubJournal{}, newStore(t), obs)

	_, err := d.Run(context.Background(), executor.Task{ID: "t", Prompt: "build a tiny useful server"})

	require.NoError(t, err)
	require.Len(t, obs.events, 2)
	require.Equal(t, observer.EventSlaveTaskStarted, obs.events[0].Type)
	require.Equal(t, "t", obs.events[0].TaskID)
	require.Equal(t, "running", obs.events[0].Status)
	require.Equal(t, "build a tiny useful server", obs.events[0].Summary)
	require.Equal(t, observer.EventSlaveTaskCompleted, obs.events[1].Type)
	require.Equal(t, "completed", obs.events[1].Status)
	var payload map[string]string
	require.NoError(t, json.Unmarshal(obs.events[1].Payload, &payload))
	require.Equal(t, "ok", payload["output"])
}

func TestDispatcher_EmitsObserverFailurePayloadForExecutorError(t *testing.T) {
	def := &stubExec{err: errors.New("bad")}
	obs := &fakeObserver{}
	d := New(map[string]executor.Executor{"": def}, &stubJournal{}, newStore(t), obs)

	_, err := d.Run(context.Background(), executor.Task{ID: "t", Prompt: "do a thing"})

	require.Error(t, err)
	require.Len(t, obs.events, 2)
	require.Equal(t, observer.EventSlaveTaskFailed, obs.events[1].Type)
	require.Equal(t, "failed", obs.events[1].Status)
	var payload map[string]string
	require.NoError(t, json.Unmarshal(obs.events[1].Payload, &payload))
	require.Equal(t, "bad", payload["error"])
}

func TestDispatcher_EmitsObserverFailurePayloadForMissingExecutor(t *testing.T) {
	obs := &fakeObserver{}
	d := New(map[string]executor.Executor{}, &stubJournal{}, newStore(t), obs)

	_, err := d.Run(context.Background(), executor.Task{ID: "t", Skill: "missing", Prompt: "do a thing"})

	require.Error(t, err)
	require.Len(t, obs.events, 2)
	require.Equal(t, observer.EventSlaveTaskFailed, obs.events[1].Type)
	require.Equal(t, "failed", obs.events[1].Status)
	var payload map[string]string
	require.NoError(t, json.Unmarshal(obs.events[1].Payload, &payload))
	require.Contains(t, payload["error"], `no executor for skill "missing"`)
}

func envelopedPrompt(t *testing.T, body string) string {
	t.Helper()
	tc := contract.TaskContract{
		Version:        1,
		ConversationID: "conv-test",
		Intent: contract.IntentSpec{
			Goal:            "do the thing",
			SuccessCriteria: []string{"thing is done"},
		},
		DataContract: contract.DataContract{
			WriteTargets: []contract.WriteTarget{{Type: contract.WriteTargetArtifact, Kind: "document", Name: "out.md"}},
		},
	}
	tc.ApplyDefaults()
	p, err := contract.EncodeEnvelope(tc, body)
	require.NoError(t, err)
	return p
}

// TestContractEnvelope_StrippedForChat verifies that a chat-skill prompt wrapped
// in a TASK_CONTRACT envelope is decoded by the dispatcher: the executor sees
// only the body, not the envelope. Without this, slave Claude Code receives the
// raw envelope as natural-language preamble.
func TestContractEnvelope_StrippedForChat(t *testing.T) {
	exec := &stubExec{res: executor.Result{Summary: "ok"}}
	d := New(map[string]executor.Executor{"": exec}, &stubJournal{}, newStore(t), nil)

	prompt := envelopedPrompt(t, "Use this contract.")
	_, err := d.Run(context.Background(), executor.Task{ID: "t", Skill: "chat", Prompt: prompt})

	require.NoError(t, err)
	require.True(t, exec.called)
	require.Equal(t, "Use this contract.", exec.gotTask.Prompt,
		"executor should receive only the body, not the envelope")
}

// TestContractEnvelope_StrippedForBash verifies the strip works for skills
// whose prompts are JSON: without stripping, bash/mcp/register_mcp executors call
// json.Unmarshal on the envelope and immediately fail with "prompt must be JSON".
func TestContractEnvelope_StrippedForBash(t *testing.T) {
	exec := &stubExec{res: executor.Result{Summary: "ok"}}
	d := New(map[string]executor.Executor{"bash": exec}, &stubJournal{}, newStore(t), nil)

	jsonBody := `{"script":"echo hi","timeout_sec":5}`
	prompt := envelopedPrompt(t, jsonBody)
	_, err := d.Run(context.Background(), executor.Task{ID: "t", Skill: "bash", Prompt: prompt})

	require.NoError(t, err)
	require.True(t, exec.called)
	require.Equal(t, jsonBody, exec.gotTask.Prompt)
}

// TestContractEnvelope_MalformedFailsTask verifies that a half-formed envelope
// (start marker present, end marker missing) fails the task with a clear error
// before invoking any executor, rather than passing garbage downstream.
func TestContractEnvelope_MalformedFailsTask(t *testing.T) {
	exec := &stubExec{}
	d := New(map[string]executor.Executor{"": exec}, &stubJournal{}, newStore(t), nil)

	malformed := contract.EnvelopeStart + "\n{\"version\":1}\n" // missing end marker
	_, err := d.Run(context.Background(), executor.Task{ID: "t", Skill: "chat", Prompt: malformed})

	require.Error(t, err)
	require.Contains(t, err.Error(), "task contract envelope")
	require.False(t, exec.called, "executor must not run on malformed envelope")
}

// TestContractEnvelope_AbsentPassesThrough verifies that plain prompts without
// an envelope reach the executor verbatim (regression guard for non-contract
// tasks).
func TestContractEnvelope_AbsentPassesThrough(t *testing.T) {
	exec := &stubExec{res: executor.Result{Summary: "ok"}}
	d := New(map[string]executor.Executor{"": exec}, &stubJournal{}, newStore(t), nil)

	plain := "just do the thing"
	_, err := d.Run(context.Background(), executor.Task{ID: "t", Skill: "chat", Prompt: plain})

	require.NoError(t, err)
	require.Equal(t, plain, exec.gotTask.Prompt)
}

// TestRespectsTaskTimeout verifies that a per-task TimeoutSec is enforced: the
// executor's context must be cancelled within the deadline even though the
// parent context has no deadline of its own.
func TestRespectsTaskTimeout(t *testing.T) {
	blk := newBlockingExec()
	j := &stubJournal{}
	d := New(map[string]executor.Executor{"": blk}, j, newStore(t), nil)

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
