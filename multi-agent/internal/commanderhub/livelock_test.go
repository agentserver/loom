package commanderhub

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/internal/commander"
	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/internal/identity"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// TestSendOrDrop_TerminalUnblocksOnCancel is the focused regression test for the
// terminal-send livelock.
//
// It exercises the EXACT code path that wedged, with no timing races: a pending
// entry whose data channel ch is full (cap 16), and a terminal frame being sent
// via sendOrDrop's blocking terminal branch. Under the pre-fix code that branch
// only selected on `done`, so with ch full and `done` open it blocked forever —
// and since this runs in the read loop, dc.done only closes AFTER the read loop
// returns, so it would never close. removePending (called by a cancelling
// consumer) now closes the per-entry cancel, and sendOrDrop selects on cancel,
// so the blocked terminal send returns promptly.
//
// This test would hang (and fail via the select timeout) under the buggy
// sendOrDrop that ignores cancel.
func TestSendOrDrop_TerminalUnblocksOnCancel(t *testing.T) {
	dc := &daemonConn{
		pending: make(map[string]*pendingEntry),
		done:    make(chan struct{}), // open: simulates a live connection (NOT closing it)
	}
	const cmdID = "cmd-1"
	pe := dc.registerPending(cmdID, true)

	// Fill the data channel to its cap so a further send blocks. ch is cap 16.
	for i := 0; i < 16; i++ {
		pe.ch <- commander.Envelope{Type: "event", ID: cmdID}
	}

	// terminal frame that routeFrame would deliver.
	terminal := commander.Envelope{Type: "command_result", ID: cmdID}

	// Run the terminal send (routeFrame's blocking path) in a goroutine.
	delivered := make(chan bool, 1)
	go func() {
		// Mirror routeFrame's call: non-terminal would drop, terminal forces through.
		delivered <- sendOrDrop(pe.ch, terminal, true, pe.cancel, dc.done)
	}()

	// Give the terminal send a moment to block on the full channel (it cannot
	// succeed: ch is full and nobody drains it; done is open and never closed).
	time.Sleep(50 * time.Millisecond)

	// A consumer cancels: removePending closes the per-entry cancel. Under the
	// fix this unblocks the stuck terminal send.
	dc.removePending(cmdID)

	select {
	case ok := <-delivered:
		require.False(t, ok, "terminal should be dropped after consumer cancel, not delivered")
	case <-time.After(2 * time.Second):
		t.Fatal("terminal send did not unblock on cancel within 2s — read loop would wedge")
	}

	// The data channel must NOT have been closed (the never-close invariant).
	select {
	case env, ok := <-pe.ch:
		require.True(t, ok, "ch must never be closed")
		require.Equal(t, "event", env.Type)
	default:
	}
}

// TestStream_CancelFullBufferDoesNotWedgeReadLoop is the end-to-end regression
// test for the terminal-send livelock.
//
// It stands up a real daemon whose backend saturates the pending buffer beyond
// cap 16 (consumer undrained), cancels the consumer while the buffer is full,
// then emits the terminal frame — reproducing the wedge window — and finally
// asserts a SECOND command on the SAME daemon still completes within a short
// timeout. Under the buggy code the wedged read loop could not read the second
// command's reply, so it would hang (DeadlineExceeded → test fails). Under the
// fix removePending closed the per-entry cancel → the terminal send unblocked →
// the read loop drained and served the second command.
func TestStream_CancelFullBufferDoesNotWedgeReadLoop(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}

	// Timing is driven by two signals so the wedge window is hit deterministically:
	//   - bufferFilled: closed once the backend has emitted >16 chunks (the pending
	//     buffer cap), guaranteeing ch is full because the consumer never drains.
	//   - sendTerminal: the backend waits on this before emitting the terminal
	//     command_result, so the test cancels the consumer FIRST (buffer full,
	//     terminal not yet read by routeFrame) and only then releases the terminal.
	bufferFilled := make(chan struct{})
	sendTerminal := make(chan struct{})

	var emitted int64
	backend := &tbBackend{
		resumeFn: func(ctx context.Context, _ agentbackend.SessionRef, _ string, sink executor.Sink) (executor.Result, error) {
			// Emit well beyond the 16-deep buffer. The consumer never drains, so ch
			// saturates to its cap.
			for i := 0; i < 40; i++ {
				sink.Write("chunk", "x")
				atomic.AddInt64(&emitted, 1)
			}
			close(bufferFilled)
			select {
			case <-sendTerminal:
			case <-ctx.Done():
				return executor.Result{Summary: "ctx-done"}, nil
			}
			sink.Close()
			return executor.Result{Summary: "done"}, nil
		},
		listFn: func(context.Context) ([]agentbackend.Session, error) {
			return []agentbackend.Session{{ID: "second-cmd-ok"}}, nil
		},
	}

	hub, _, o, cleanup := dialFakeDaemon(t, resolver, "tok-alice", backend)
	defer cleanup()

	di := hub.reg.daemons(o)
	require.NotEmpty(t, di)
	daemonID := di[0].DaemonID

	// Start a streaming command but do NOT drain `out`, so ch backs up to its cap.
	ctx, cancel := context.WithCancel(context.Background())
	out, err := hub.SendCommandStream(ctx, o, daemonID, "session_turn",
		jsonRaw(t, commander.SessionTurnArgs{ID: "s1", Prompt: "go"}))
	require.NoError(t, err)
	_ = out // intentionally undrained: this is what saturates ch

	// Wait until the backend has saturated the buffer.
	select {
	case <-bufferFilled:
	case <-time.After(3 * time.Second):
		t.Fatal("backend never saturated the buffer")
	}

	// Cancel the consumer: the SendCommandStream goroutine exits and runs
	// `defer dc.removePending(cmdID)` → closes the per-entry cancel. This is the
	// race window: routeFrame may have already grabbed the entry for an in-flight
	// frame, or will grab it for the terminal; either way the cancel now unblocks.
	cancel()

	// Release the terminal frame. Under the bug routeFrame blocks forever sending
	// into the full ch (dc.done never closes because the read loop is the stuck
	// goroutine). Under the fix the terminal send selects <-cancel and returns.
	close(sendTerminal)

	// Allow the (un)wedged read loop to recover before issuing the next command.
	time.Sleep(150 * time.Millisecond)

	// THE KEY ASSERTION: a second command on the SAME daemon must complete within
	// a short timeout. Under the buggy code the read loop is wedged → list_sessions
	// reply is never read → DeadlineExceeded → test fails.
	cctx, ccancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer ccancel()
	payload, err := hub.SendCommand(cctx, o, daemonID, "list_sessions", nil)
	require.NoError(t, err, "second command hung — read loop wedged by the cancelled terminal send")
	require.Contains(t, string(payload), "second-cmd-ok")

	// Sanity: the daemon streamed frames before we cancelled.
	require.GreaterOrEqual(t, atomic.LoadInt64(&emitted), int64(1))
}
