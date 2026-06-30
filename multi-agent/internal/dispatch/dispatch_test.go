package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/contract"
	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/internal/observer"
	"github.com/yourorg/multi-agent/internal/store"
)

type stubExec struct {
	res     executor.Result
	err     error
	called  bool
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
	// Empty Skill is treated as chat, so observer payload now sees the wrapped
	// kind:final marker (matches what driver reads back from store.Output).
	require.Contains(t, payload["output"], `"kind":"final"`)
	require.Contains(t, payload["output"], `"summary":"ok"`)
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
			ReadArtifacts: []contract.ArtifactRef{},
			WriteTargets:  []contract.WriteTarget{{Type: contract.WriteTargetArtifact, Kind: "document", Name: "out.md"}},
		},
		CapabilityRequirements: contract.CapabilityRequirements{Skills: []string{"chat"}},
		RecoveryHint:           "test recovery hint",
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

func TestRunChatWrapsFinalResult(t *testing.T) {
	s := newStore(t)
	exec := &stubExec{res: executor.Result{Summary: "done", SessionID: "S-1"}}
	d := New(map[string]executor.Executor{"": exec}, &stubJournal{}, s, nil)
	res, err := d.Run(context.Background(), executor.Task{ID: "T1", Skill: "", Prompt: "hi"})
	require.NoError(t, err)

	row, _, err := s.GetTaskWithChunks("T1")
	require.NoError(t, err)
	if !strings.Contains(row.Output, `"kind":"final"`) {
		t.Errorf("expected kind:final in stored output, got %q", row.Output)
	}
	if !strings.Contains(row.Output, `"session_id":"S-1"`) {
		t.Errorf("expected session_id in stored output, got %q", row.Output)
	}
	if !strings.Contains(row.Output, `"summary":"done"`) {
		t.Errorf("expected summary in stored output, got %q", row.Output)
	}
	if res.Summary != "done" {
		t.Errorf("res.Summary returned by Run should stay unwrapped, got %q", res.Summary)
	}
}

func TestRunChatWrapsAwaitingUser(t *testing.T) {
	s := newStore(t)
	exec := &stubExec{res: executor.Result{
		SessionID: "S-2",
		AwaitingUser: &executor.AskUserPayload{
			Kind: "ask_user", Question: "pick one?", Options: []string{"a", "b"},
		},
	}}
	d := New(map[string]executor.Executor{"chat": exec}, &stubJournal{}, s, nil)
	_, err := d.Run(context.Background(), executor.Task{ID: "T2", Skill: "chat", Prompt: "hi"})
	require.NoError(t, err)
	row, _, err := s.GetTaskWithChunks("T2")
	require.NoError(t, err)
	for _, want := range []string{`"kind":"awaiting_user"`, `"session_id":"S-2"`, `"question":"pick one?"`, `"options":["a","b"]`} {
		if !strings.Contains(row.Output, want) {
			t.Errorf("missing %s in %s", want, row.Output)
		}
	}
}

func TestRunBashLeavesResultUnwrapped(t *testing.T) {
	s := newStore(t)
	exec := &stubExec{res: executor.Result{Summary: "raw bash output"}}
	d := New(map[string]executor.Executor{"bash": exec}, &stubJournal{}, s, nil)
	_, err := d.Run(context.Background(), executor.Task{ID: "T3", Skill: "bash", Prompt: "ls"})
	require.NoError(t, err)
	row, _, err := s.GetTaskWithChunks("T3")
	require.NoError(t, err)
	if strings.Contains(row.Output, `"kind"`) {
		t.Errorf("non-chat skill should not be wrapped; got %s", row.Output)
	}
	if row.Output != "raw bash output" {
		t.Errorf("expected raw summary, got %q", row.Output)
	}
}

func TestRunChatResumeWrapsResult(t *testing.T) {
	s := newStore(t)
	exec := &stubExec{res: executor.Result{Summary: "resumed", SessionID: "S-3"}}
	d := New(map[string]executor.Executor{"chat_resume": exec}, &stubJournal{}, s, nil)
	_, err := d.Run(context.Background(), executor.Task{ID: "T4", Skill: "chat_resume", Prompt: "{}"})
	require.NoError(t, err)
	row, _, _ := s.GetTaskWithChunks("T4")
	if !strings.Contains(row.Output, `"kind":"final"`) || !strings.Contains(row.Output, `"summary":"resumed"`) {
		t.Errorf("chat_resume should be wrapped; got %s", row.Output)
	}
}

// TestDispatch_DuplicateInsertSkipsExecutor verifies that when the same task
// is delivered twice (poller ack lost → re-delivery), only ONE executor.Run
// is invoked. Fixes §1.2 #6 of docs/review-2026-06-13.md.
func TestDispatch_DuplicateInsertSkipsExecutor(t *testing.T) {
	def := &stubExec{res: executor.Result{Summary: "ok"}}
	d := New(map[string]executor.Executor{"chat": def}, &stubJournal{}, newStore(t), nil)

	task := executor.Task{ID: "task-dup", Skill: "chat", Prompt: "hello"}

	res1, err1 := d.Run(context.Background(), task)
	require.NoError(t, err1)
	require.Equal(t, "ok", res1.Summary)
	require.True(t, def.called)

	// Reset called so we can detect a second Run.
	def.called = false

	res2, err2 := d.Run(context.Background(), task)
	require.NoError(t, err2, "re-delivery must not error")
	require.False(t, def.called, "executor.Run must NOT be invoked a second time (would spawn a 2nd claude subprocess for chat skill)")

	// For chat skill, store.Output holds a kind:final JSON wrapper.
	// replayExistingTask must surface it so the caller still sees the result.
	require.NotEmpty(t, res2.Summary, "second Run must replay stored output, not return empty")
	require.Contains(t, res2.Summary, "ok", "replayed summary must contain original output")
}

// TestDispatch_DuplicateInsertOnFailedTaskReplaysError verifies that a
// re-delivered task whose first run failed returns the stored error, not nil.
func TestDispatch_DuplicateInsertOnFailedTaskReplaysError(t *testing.T) {
	def := &stubExec{err: errors.New("kaboom")}
	d := New(map[string]executor.Executor{"chat": def}, &stubJournal{}, newStore(t), nil)
	task := executor.Task{ID: "task-fail-dup", Skill: "chat", Prompt: "hi"}

	_, err1 := d.Run(context.Background(), task)
	require.Error(t, err1)

	def.called = false
	_, err2 := d.Run(context.Background(), task)
	require.Error(t, err2, "re-delivery of failed task must surface stored error")
	require.False(t, def.called)
	require.Contains(t, err2.Error(), "kaboom")
}

// TestDispatch_DuplicateInsertRunningReturnsSentinel verifies that when a
// duplicate task is delivered while the original is still running, Run
// returns ErrDuplicateTaskRunning (not nil) so the poller can no-op
// instead of clobbering server-side state with an empty completed PUT.
func TestDispatch_DuplicateInsertRunningReturnsSentinel(t *testing.T) {
	s := newStore(t)
	// Seed a row in 'running' state directly (simulating an in-flight executor)
	inserted, err := s.InsertIfAbsent(store.Task{ID: "task-running", Skill: "chat", Prompt: "x"})
	require.NoError(t, err)
	require.True(t, inserted)
	require.NoError(t, s.MarkRunning("task-running"))

	def := &stubExec{res: executor.Result{Summary: "would-run-but-shouldnt"}}
	d := New(map[string]executor.Executor{"chat": def}, &stubJournal{}, s, nil)

	res, err := d.Run(context.Background(), executor.Task{ID: "task-running", Skill: "chat", Prompt: "x"})
	require.False(t, def.called, "executor.Run must NOT be invoked")
	require.Equal(t, executor.Result{}, res)
	require.ErrorIs(t, err, ErrDuplicateTaskRunning,
		"running-branch must return sentinel error so poller can no-op (else it PUTs empty completed and clobbers in-flight state)")
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

func TestDispatcher_ReplayCompletedChatTaskSurfacesWrappedOutput(t *testing.T) {
	// Replay path returns row.Output as Summary; for chat skills that's the
	// kind-marker JSON envelope. The poller must forward it as raw JSON,
	// not re-encode via the raw-summary fallback. WrappedOutput is how
	// dispatch signals "this is already a JSON envelope, forward verbatim".
	s := newStore(t)
	// Seed the store with a completed chat task whose Output is the wrapped
	// envelope (the shape dispatch.Run produces for chat results).
	envelope := `{"kind":"final","summary":"hello","session_id":"thr-7"}`
	_, err := s.InsertIfAbsent(store.Task{ID: "t-replay-chat", Skill: "chat", Prompt: "hi"})
	require.NoError(t, err)
	require.NoError(t, s.Complete("t-replay-chat", envelope))

	d := New(map[string]executor.Executor{"": &stubExec{}}, &stubJournal{}, s, nil)
	res, err := d.Run(context.Background(), executor.Task{ID: "t-replay-chat", Skill: "chat", Prompt: "hi"})
	require.NoError(t, err)
	require.Equal(t, envelope, res.Summary, "Summary preserves the envelope for legacy callers")
	require.Equal(t, envelope, res.WrappedOutput, "WrappedOutput must be set so the poller forwards raw JSON instead of double-encoding")
}

func TestDispatcher_ReplayCompletedNonChatTaskLeavesWrappedOutputEmpty(t *testing.T) {
	// Non-chat skills (e.g. bash) store the raw summary string in row.Output.
	// WrappedOutput must stay empty so the poller's raw-summary fallback
	// JSON-encodes it correctly (the contract path agentserver expects).
	s := newStore(t)
	_, err := s.InsertIfAbsent(store.Task{ID: "t-replay-bash", Skill: "bash", Prompt: "ls"})
	require.NoError(t, err)
	require.NoError(t, s.Complete("t-replay-bash", "raw-bash-output"))

	d := New(map[string]executor.Executor{"bash": &stubExec{}}, &stubJournal{}, s, nil)
	res, err := d.Run(context.Background(), executor.Task{ID: "t-replay-bash", Skill: "bash", Prompt: "ls"})
	require.NoError(t, err)
	require.Equal(t, "raw-bash-output", res.Summary)
	require.Empty(t, res.WrappedOutput, "non-chat replay must not pretend to have a JSON envelope")
}

func TestDispatcher_ReplayCompletedChatTaskWithNonEnvelopeJSONLeavesWrappedEmpty(t *testing.T) {
	// Defensive: if a chat task's row.Output happens to be valid JSON but
	// not the envelope shape (e.g. a backend that bypassed the wrapping
	// path, or test seed using a raw JSON string), WrappedOutput MUST stay
	// empty so the poller's raw-summary fallback kicks in. Otherwise we'd
	// silently switch the wire type from string to object.
	s := newStore(t)
	rawJSON := `{"raw":"output that happens to be json"}`
	_, err := s.InsertIfAbsent(store.Task{ID: "t-chat-raw-json", Skill: "chat", Prompt: "hi"})
	require.NoError(t, err)
	require.NoError(t, s.Complete("t-chat-raw-json", rawJSON))

	d := New(map[string]executor.Executor{"": &stubExec{}}, &stubJournal{}, s, nil)
	res, err := d.Run(context.Background(), executor.Task{ID: "t-chat-raw-json", Skill: "chat", Prompt: "hi"})
	require.NoError(t, err)
	require.Equal(t, rawJSON, res.Summary)
	require.Empty(t, res.WrappedOutput, "non-envelope JSON must not be flagged as WrappedOutput on replay")
}

func TestDispatcher_ReplayCompletedChatTaskWithEmptySessionEnvelopeUnwrapsToSummary(t *testing.T) {
	// Asymmetry guard (mirror of poller's empty-session_id ack test).
	// dispatch.Run sets res.Summary plain ("ok") for the empty-session
	// case because WrappedOutput is only stamped when session_id != "".
	// Replay loads row.Output (the envelope text) and MUST produce the
	// same executor.Result Run did: Summary="ok", WrappedOutput="".
	// Without the unwrap, the poller would JSON-encode the envelope
	// text into a string like "{\"kind\":\"final\"...}", diverging from
	// the normal path. #24 P2 review 5.
	s := newStore(t)
	emptySessionEnvelope := `{"kind":"final","summary":"ok","session_id":""}`
	_, err := s.InsertIfAbsent(store.Task{ID: "t-replay-empty-sess", Skill: "chat", Prompt: "hi"})
	require.NoError(t, err)
	require.NoError(t, s.Complete("t-replay-empty-sess", emptySessionEnvelope))

	d := New(map[string]executor.Executor{"": &stubExec{}}, &stubJournal{}, s, nil)
	res, err := d.Run(context.Background(), executor.Task{ID: "t-replay-empty-sess", Skill: "chat", Prompt: "hi"})
	require.NoError(t, err)
	require.Equal(t, "ok", res.Summary, "replay must unwrap the envelope and surface the inner summary, not the envelope text")
	require.Empty(t, res.WrappedOutput, "empty-session_id final envelope must replay without WrappedOutput so the wire shape matches the normal path")
}

// ----------------------------------------------------------------------------
// WT-1-routing-trace: Dispatcher.Run integration tests.

func withCaptureWriter(t *testing.T) *capture {
	t.Helper()
	c := &capture{}
	SetWriter(c)
	t.Cleanup(func() { SetWriter(nil) })
	return c
}

func TestDispatch_TwoCandidates_TraceWhyChosen(t *testing.T) {
	cap := withCaptureWriter(t)
	bashExec := &stubExec{res: executor.Result{Summary: "bash-ok"}}
	chatExec := &stubExec{res: executor.Result{Summary: "chat-ok"}}
	d := New(map[string]executor.Executor{"bash": bashExec, "chat": chatExec}, &stubJournal{}, newStore(t), nil)
	_, err := d.Run(context.Background(), executor.Task{ID: "T", Skill: "chat", Prompt: "hi"})
	require.NoError(t, err)
	require.Len(t, cap.got, 1)
	dec := cap.got[0]
	require.Equal(t, "chat", dec.SelectedAgentID)
	require.False(t, dec.SelectedNone)
	require.Equal(t, ReasonCapabilityMatch, dec.ReasonCode)
	require.Len(t, dec.Candidates, 2)
	var bashCand, chatCand Candidate
	for _, c := range dec.Candidates {
		if c.AgentID == "bash" {
			bashCand = c
		} else {
			chatCand = c
		}
	}
	require.Equal(t, ReasonNoCapabilityMatch, bashCand.Reason)
	require.Equal(t, ReasonCapabilityMatch, chatCand.Reason)
}

func TestDispatch_NoCandidates_TraceFailure(t *testing.T) {
	cap := withCaptureWriter(t)
	d := New(map[string]executor.Executor{}, &stubJournal{}, newStore(t), nil)
	_, err := d.Run(context.Background(), executor.Task{ID: "T", Skill: "unknown", Prompt: "hi"})
	require.Error(t, err)
	require.Len(t, cap.got, 1)
	require.True(t, cap.got[0].SelectedNone)
	require.Equal(t, "", cap.got[0].SelectedAgentID)
	require.Equal(t, ReasonNoCapabilityMatch, cap.got[0].ReasonCode)
}

func TestDispatch_FinalizeAndEmit_DeferCoversEarlyReturns(t *testing.T) {
	t.Run("malformed-envelope", func(t *testing.T) {
		cap := withCaptureWriter(t)
		d := New(map[string]executor.Executor{"": &stubExec{}}, &stubJournal{}, newStore(t), nil)
		malformed := contract.EnvelopeStart + "\n{\"version\":1}\n"
		_, err := d.Run(context.Background(), executor.Task{ID: "TM", Skill: "chat", Prompt: malformed})
		require.Error(t, err)
		require.Len(t, cap.got, 1)
	})
	t.Run("no-executor", func(t *testing.T) {
		cap := withCaptureWriter(t)
		d := New(map[string]executor.Executor{}, &stubJournal{}, newStore(t), nil)
		_, err := d.Run(context.Background(), executor.Task{ID: "TN", Skill: "x"})
		require.Error(t, err)
		require.Len(t, cap.got, 1)
	})
	t.Run("executor-error", func(t *testing.T) {
		cap := withCaptureWriter(t)
		d := New(map[string]executor.Executor{"": &stubExec{err: errors.New("oops")}}, &stubJournal{}, newStore(t), nil)
		_, err := d.Run(context.Background(), executor.Task{ID: "TE"})
		require.Error(t, err)
		require.Len(t, cap.got, 1)
	})
	t.Run("insert-if-absent-error", func(t *testing.T) {
		// Force InsertIfAbsent to error by closing the underlying store
		// before Run. The defer must still write a trace row.
		cap := withCaptureWriter(t)
		s := newStore(t)
		require.NoError(t, s.Close())
		d := New(map[string]executor.Executor{"chat": &stubExec{}}, &stubJournal{}, s, nil)
		_, err := d.Run(context.Background(), executor.Task{ID: "TI", Skill: "chat", Prompt: "hi"})
		require.Error(t, err)
		require.Len(t, cap.got, 1, "InsertIfAbsent error path must still emit trace")
	})
	t.Run("duplicate-running-sentinel", func(t *testing.T) {
		cap := withCaptureWriter(t)
		s := newStore(t)
		ok, err := s.InsertIfAbsent(store.Task{ID: "TD", Skill: "chat"})
		require.NoError(t, err)
		require.True(t, ok)
		require.NoError(t, s.MarkRunning("TD"))
		d := New(map[string]executor.Executor{"chat": &stubExec{}}, &stubJournal{}, s, nil)
		_, err = d.Run(context.Background(), executor.Task{ID: "TD", Skill: "chat"})
		require.ErrorIs(t, err, ErrDuplicateTaskRunning)
		require.Len(t, cap.got, 1)
	})
}

// TestDispatch_StoreCompleteFails_TraceRecordsFailure verifies that when
// exec.Run returned a result but store.Complete fails afterwards, the
// trace's ReasonText annotates the persistence failure instead of leaving
// the row reading like a clean "matched skill X" success.
func TestDispatch_StoreCompleteFails_TraceRecordsFailure(t *testing.T) {
	cap := withCaptureWriter(t)
	s := newStore(t)
	// Pre-mark the task as completed in the store so a subsequent
	// Complete-after-success path hits an error. We mimic that by closing
	// the store right before the executor's Complete call: the executor
	// has already run (we don't block it), so when the dispatcher tries
	// to call store.Complete the underlying DB is closed.
	d := New(map[string]executor.Executor{"chat": &closingExec{store: s}}, &stubJournal{}, s, nil)
	_, err := d.Run(context.Background(), executor.Task{ID: "T-sc", Skill: "chat", Prompt: "hi"})
	require.Error(t, err, "Complete error must propagate up")
	require.Len(t, cap.got, 1)
	require.Contains(t, cap.got[0].ReasonText, "store.Complete failed",
		"ReasonText must record the post-success persistence failure so the audit trail does not misrepresent the outcome")
}

// closingExec returns success but closes the store mid-run, so when the
// dispatcher calls store.Complete afterwards it fails. Used to exercise
// the post-Run/pre-Complete failure path.
type closingExec struct{ store *store.Store }

func (e *closingExec) Run(_ context.Context, _ executor.Task, sink executor.Sink) (executor.Result, error) {
	sink.Close()
	_ = e.store.Close()
	return executor.Result{Summary: "ok"}, nil
}

func TestDispatch_FallbackExecutor_SelectedAsCapabilityMatch(t *testing.T) {
	cap := withCaptureWriter(t)
	fallback := &stubExec{res: executor.Result{Summary: "fallback-ok"}}
	d := New(map[string]executor.Executor{"": fallback}, &stubJournal{}, newStore(t), nil)
	_, err := d.Run(context.Background(), executor.Task{ID: "T-fb", Skill: "xyzzy", Prompt: "hi"})
	require.NoError(t, err)
	require.Len(t, cap.got, 1)
	require.Equal(t, "", cap.got[0].SelectedAgentID)
	require.False(t, cap.got[0].SelectedNone, "fallback selected is NOT a lookup failure")
	require.Equal(t, ReasonCapabilityMatch, cap.got[0].ReasonCode)
	require.Len(t, cap.got[0].Candidates, 1)
	require.Equal(t, "", cap.got[0].Candidates[0].AgentID)
	require.Equal(t, ReasonCapabilityMatch, cap.got[0].Candidates[0].Reason)
}

func TestDispatch_ConversationIDFallback_UsesTaskID(t *testing.T) {
	cap := withCaptureWriter(t)
	d := New(map[string]executor.Executor{"": &stubExec{res: executor.Result{Summary: "ok"}}}, &stubJournal{}, newStore(t), nil)
	_, err := d.Run(context.Background(), executor.Task{ID: "fallback-tid", Prompt: "plain"})
	require.NoError(t, err)
	require.Equal(t, "fallback-tid", cap.got[0].ConversationID)
}

func TestWriter_FailLogged_DispatchContinues(t *testing.T) {
	cap := &capture{err: errors.New("kaboom")}
	SetWriter(cap)
	t.Cleanup(func() { SetWriter(nil) })

	var buf bytes.Buffer
	prevOut := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prevOut) })

	d := New(map[string]executor.Executor{"": &stubExec{res: executor.Result{Summary: "ok"}}}, &stubJournal{}, newStore(t), nil)
	res, err := d.Run(context.Background(), executor.Task{ID: "T-log"})
	require.NoError(t, err, "dispatch must NOT propagate writer error")
	require.Equal(t, "ok", res.Summary)
	require.Contains(t, buf.String(), "[route-trace] write failed:")
	require.Contains(t, buf.String(), "kaboom")
	require.Contains(t, buf.String(), "conv=T-log")
	require.Regexp(t, `decision=[a-f0-9]{32}`, buf.String(),
		"log line must include decision=<id> so the incident can be traced")
}

func TestDispatch_TimestampDetached_PreservesTraceOnCtxCancel(t *testing.T) {
	cap := withCaptureWriter(t)
	d := New(map[string]executor.Executor{"": &stubExec{res: executor.Result{Summary: "ok"}}}, &stubJournal{}, newStore(t), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	_, _ = d.Run(ctx, executor.Task{ID: "T-cancel"})
	require.Len(t, cap.got, 1, "trace must still be written even when parent ctx was cancelled")
}

func TestDispatch_Run_SignatureUnchanged(t *testing.T) {
	rt := reflect.TypeOf((*Dispatcher)(nil))
	for i := 0; i < rt.NumMethod(); i++ {
		if rt.Method(i).Name == "Run" {
			require.Equal(t,
				"func(*dispatch.Dispatcher, context.Context, executor.Task) (executor.Result, error)",
				rt.Method(i).Type.String())
			return
		}
	}
	t.Fatal("Run method not found")
}
