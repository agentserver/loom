package probes

import (
	"context"
	"expvar"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yourorg/multi-agent/internal/secretscrub"
)

func TestEmit_NonBlocking_WhenChannelFull(t *testing.T) {
	e := NewEmitter(8, io.Discard)
	// Freeze the drain by closing early — subsequent Emit calls hit
	// the closed drain and exercise the channel-full → drop path
	// deterministically.
	_, _ = e.Close()
	for i := 0; i < 300; i++ {
		start := time.Now()
		if err := e.Emit(context.Background(), MetricTaskSuccessRate, true, nil); err != nil {
			t.Fatalf("emit returned error: %v", err)
		}
		if d := time.Since(start); d > 1*time.Millisecond {
			t.Fatalf("emit blocked: %s (iter %d)", d, i)
		}
	}
}

func TestEmit_DropCounter_Bumps(t *testing.T) {
	e := NewEmitter(2, io.Discard)
	_, _ = e.Close() // freeze the drain
	for i := 0; i < 10; i++ {
		_ = e.Emit(context.Background(), MetricTaskSuccessRate, true, nil)
	}
	if got := atomic.LoadInt64(&e.dropped); got < 1 {
		t.Fatalf("droppedTotal did not bump: got %d, want >=1", got)
	}
}

func TestEmit_SanitizesLabelValues(t *testing.T) {
	before := readRedactedCounter()
	e := NewEmitter(0, io.Discard)
	err := e.Emit(context.Background(), MetricTaskSuccessRate, true, map[string]string{
		"bad": "sk-ABCDEFGHIJKLMNOP",
	})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	recs, _ := e.Close()
	if len(recs) != 1 {
		t.Fatalf("want 1 record, got %d", len(recs))
	}
	if got := recs[0].Labels["bad"]; !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("label not sanitized: %q", got)
	}
	after := readRedactedCounter()
	if after-before != 1 {
		t.Fatalf("secretscrub.RedactedTotal bumped %d, want 1", after-before)
	}
	_ = secretscrub.RedactedTotal // touch the imported symbol
}

func TestEmit_RejectsUnknownMetric(t *testing.T) {
	// Serialise stderr writes with a mutex — the flusher goroutine
	// drains the warn queue on Close, and Fprintln + strings.Builder
	// are not concurrency-safe on their own.
	var buf synchronizedBuf
	e := NewEmitter(0, &buf)
	err := e.Emit(context.Background(), MetricKey("not_a_real_metric"), 1, nil)
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	recs, _ := e.Close()
	if len(recs) != 0 {
		t.Fatalf("unknown metric was not dropped: %v", recs)
	}
	if !strings.Contains(buf.String(), "not_a_real_metric") {
		t.Fatalf("stderr warn missing: %q", buf.String())
	}
}

func TestClose_Idempotent(t *testing.T) {
	e := NewEmitter(0, io.Discard)
	a, err := e.Close()
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 0 {
		t.Fatalf("second Close returned %d records, want 0", len(b))
	}
	_ = a
}

func TestEmit_NilEmitter_IsNoop(t *testing.T) {
	var e *Emitter
	if err := e.Emit(context.Background(), MetricTaskSuccessRate, true, nil); err != nil {
		t.Fatal(err)
	}
}

// readRedactedCounter reads the expvar counter with a defensive path
// so a future rename of the expvar name shows up as an assertion
// mismatch rather than a nil-deref.
func readRedactedCounter() int64 {
	v := expvar.Get("route_reason_redacted_total")
	if v == nil {
		return 0
	}
	iv, ok := v.(*expvar.Int)
	if !ok {
		return 0
	}
	return iv.Value()
}

// synchronizedBuf is a mutex-guarded strings.Builder-ish writer used by
// tests whose stderr is written by the flusher goroutine (post-Close)
// as well as by the test goroutine (assertions).
type synchronizedBuf struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (s *synchronizedBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}
func (s *synchronizedBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// blockingWriter blocks forever on Write, simulating a wedged pipe
// (e.g. `... 2>&1 | slow_consumer` where the reader stops draining).
type blockingWriter struct {
	blocked chan struct{}
}

func newBlockingWriter() *blockingWriter { return &blockingWriter{blocked: make(chan struct{})} }
func (b *blockingWriter) Write(p []byte) (int, error) {
	// First write blocks forever; subsequent writes never fire because
	// Fprintln returns error the caller ignores.
	<-b.blocked
	return len(p), nil
}

// TestClose_DoesNotBlock_WhenStderrWedged is the P1 regression guard
// for the fresh-review finding: a wedged stderr must NOT wedge Close.
// The warn-writer goroutine may hang on the wedged writer, but Close
// (called on the runner's main goroutine) must return promptly so the
// runner completes.
func TestClose_DoesNotBlock_WhenStderrWedged(t *testing.T) {
	stderr := newBlockingWriter()
	e := NewEmitter(0, stderr)
	// Queue a warn so the flusher's warn goroutine picks it up and
	// blocks on the wedged writer.
	e.Warn("this write will block forever")
	// Small yield so the warn goroutine picks up the message before
	// Close fires — otherwise the drain loop empties warnCh cleanly.
	time.Sleep(20 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		_, _ = e.Close()
		close(done)
	}()
	select {
	case <-done:
		// success: Close returned even though stderr is wedged
	case <-time.After(2 * time.Second):
		t.Fatal("Close blocked on wedged stderr — P1 regression")
	}
	// Unblock the warn writer so the goroutine exits and -race is
	// happy at test teardown.
	close(stderr.blocked)
}

// TestClose_Idempotence_ReturnsEmptyOnSecondCall verifies spec §3.1
// contract: subsequent Close calls return an empty slice + nil error,
// not the cached record slice.
func TestClose_Idempotence_ReturnsEmptyOnSecondCall(t *testing.T) {
	e := NewEmitter(0, io.Discard)
	_ = e.Emit(context.Background(), MetricTaskSuccessRate, true, nil)
	first, err := e.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 {
		t.Fatalf("first Close: want 1 record, got %d", len(first))
	}
	second, err := e.Close()
	if err != nil {
		t.Fatal(err)
	}
	if second != nil {
		t.Fatalf("second Close: want nil slice per spec §3.1, got %v", second)
	}
}
