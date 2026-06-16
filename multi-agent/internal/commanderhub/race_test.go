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
)

// TestStream_CancelWhileStreamingNoPanic is the regression test for the
// send-on-closed-channel panic that crashed the observer.
//
// Setup: a real daemon (WSClient + tbBackend) streams many chunk events and a
// terminal command_result. The browser-turn consumer (SendCommandStream) starts
// draining, then CANCELS its ctx partway — exactly what the 30s turn timeout or
// a browser disconnect does. In the proxy goroutine, `defer removePending` then
// fires while the daemon is still emitting frames; routeFrame (in the read loop)
// does a locked lookup, releases the lock, and calls sendOrDrop on the pending
// channel. Under the OLD code removePending closed that channel, so a late frame
// in that window panicked "send on closed channel". Under the fix the channel is
// never closed, so routeFrame's lookup misses (entry deleted) and the late frame
// is dropped — no panic.
//
// Run with -race -count to stress the race window.
func TestStream_CancelWhileStreamingNoPanic(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}

	// resumeFn emits 200 chunks with small sleeps, then a terminal result. The
	// sleeps spread frames across time so the consumer's cancel can land while
	// the daemon is mid-stream (maximizing the race window).
	const chunks = 200
	var sent int64
	backend := &tbBackend{
		resumeFn: func(ctx context.Context, _, _ string, sink executor.Sink) (executor.Result, error) {
			for i := 0; i < chunks; i++ {
				sink.Write("chunk", "x")
				atomic.AddInt64(&sent, 1)
				// Tiny sleep keeps the daemon from dumping all frames into the
				// 16-deep buffer at once; instead they trickle while we cancel.
				select {
				case <-time.After(200 * time.Microsecond):
				case <-ctx.Done():
					return executor.Result{Summary: "ctx-done"}, nil
				}
			}
			sink.Close()
			return executor.Result{Summary: "done"}, nil
		},
	}

	hub, _, o, cleanup := dialFakeDaemon(t, resolver, "tok-alice", backend)
	defer cleanup()

	di := hub.reg.daemons(o)
	require.NotEmpty(t, di)

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := hub.SendCommandStream(ctx, o, di[0].DaemonID, "session_turn",
		jsonRaw(t, commander.SessionTurnArgs{ID: "s1", Prompt: "go"}))
	require.NoError(t, err)

	// Drain the consumer in a goroutine; it must not panic. Returned after the
	// consumer goroutine ends (channel closed by the stream's defer on
	// terminal/cancel/dc.done).
	consumerDone := make(chan struct{})
	var got int64
	go func() {
		defer close(consumerDone)
		// range over out; SendCommandStream closes `out` on every exit path.
		for range ch {
			atomic.AddInt64(&got, 1)
		}
	}()

	// Cancel partway: wait until the daemon has emitted a handful of frames,
	// then pull the rug — simulating the 30s turn timeout / browser disconnect.
	require.Eventually(t, func() bool { return atomic.LoadInt64(&sent) >= 3 },
		time.Second, time.Microsecond*500, "daemon never started streaming")
	cancel()

	// Consumer must terminate cleanly: cancel fires <-ctx.Done() in the stream
	// goroutine, its defer removePending runs (now delete-only, no close), `out`
	// closes, the range ends. No panic ever reaches the process.
	select {
	case <-consumerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("consumer did not terminate after cancel")
	}

	// Sanity: the daemon streamed at least a few frames before we cancelled.
	require.GreaterOrEqual(t, atomic.LoadInt64(&sent), int64(1))
}
