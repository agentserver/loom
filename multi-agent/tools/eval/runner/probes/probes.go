// Package probes emits E1/E6 evaluation metric records collected by the
// eval-runner into a bounded, non-blocking buffer. See
// docs/specs/wt2-e1e6-probes.spec.md.
package probes

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yourorg/multi-agent/internal/secretscrub"
)

// MetricKey is the typed enum for the eight E1/E6 metrics. Any Emit
// call with a value outside AllMetrics() is dropped with a warn log.
type MetricKey string

const (
	MetricTaskSuccessRate            MetricKey = "task_success_rate"
	MetricLifecycleClosureRate       MetricKey = "lifecycle_closure_rate"
	MetricTimeToCompletion           MetricKey = "time_to_completion_ns"
	MetricHumanContextSelectionCount MetricKey = "human_context_selection_count"
	MetricWrongContextFailureRate    MetricKey = "wrong_context_failure_rate"
	MetricArtifactCorrectnessRate    MetricKey = "artifact_correctness_rate"
	MetricManualSetupStepCount       MetricKey = "manual_setup_step_count"
	MetricConfigTouchCount           MetricKey = "config_touch_count"
)

// AllMetrics returns the eight-element slice in declaration order.
// Changing the order is a schema migration for writer.go's probe
// columns (spec §4).
func AllMetrics() []MetricKey {
	return []MetricKey{
		MetricTaskSuccessRate,
		MetricLifecycleClosureRate,
		MetricTimeToCompletion,
		MetricHumanContextSelectionCount,
		MetricWrongContextFailureRate,
		MetricArtifactCorrectnessRate,
		MetricManualSetupStepCount,
		MetricConfigTouchCount,
	}
}

var validMetric = func() map[MetricKey]bool {
	m := make(map[MetricKey]bool, 8)
	for _, k := range AllMetrics() {
		m[k] = true
	}
	return m
}()

// Record is one buffered emission returned by Close.
type Record struct {
	Metric          MetricKey
	Value           any
	Labels          map[string]string
	EmittedAt       time.Time
	EmittedAtMonoNs int64
}

// Emitter buffers Emit calls onto a bounded channel and drains them on
// Close. All Emit calls are non-blocking (spec §7(a)).
//
// Warn discipline: Emit NEVER writes to stderr directly — a slow
// stderr (redirected to a wedged pipe) would block the runner. Warns
// go to bounded warnCh (buffered), drained by a warnWriter goroutine
// off the hot path. Channel-full warns increment atomic counters that
// the warnWriter folds into a single summary line on Close.
//
// Close waits on TWO signals: the record-collector's drained channel
// (unconditional) AND a bounded wait on warnWriter's completion —
// bounded so a wedged stderr still can't wedge Close, but sized so
// healthy stderrs see all pending warns before Close returns.
type Emitter struct {
	ch          chan Record
	warnCh      chan string
	done        chan struct{}
	drained     chan []Record
	warnDrained chan struct{} // closed by warnWriter after its drain-then-exit path
	stderr      io.Writer

	mono time.Time // monotonic reference captured in NewEmitter

	dropped     int64 // atomic — Emit-side buffer overflow count
	warnDropped int64 // atomic — warnCh overflow count

	closeOnce sync.Once
	result    []Record
}

// closeWarnWait is the bounded ceiling Close waits for warnWriter to
// finish its post-done drain. Long enough for a healthy stderr to
// flush ~32 warns (the warnCh capacity) even under load; short enough
// that a wedged stderr — write blocks forever — still lets Close
// return promptly. 250 ms was chosen empirically: `go test -race
// -count=100` shows zero flakes on the ordering-sensitive tests at
// this ceiling, and the wedged-stderr regression test still
// completes well inside its 2 s deadline.
const closeWarnWait = 250 * time.Millisecond

// NewEmitter returns an Emitter with a bufferSize-record channel and
// starts the flusher goroutine. bufferSize <= 0 defaults to 256.
func NewEmitter(bufferSize int, stderr io.Writer) *Emitter {
	if bufferSize <= 0 {
		bufferSize = 256
	}
	if stderr == nil {
		stderr = io.Discard
	}
	e := &Emitter{
		ch:          make(chan Record, bufferSize),
		warnCh:      make(chan string, 32), // small; overflow → atomic counter
		done:        make(chan struct{}),
		drained:     make(chan []Record, 1),
		warnDrained: make(chan struct{}),
		stderr:      stderr,
		mono:        time.Now(),
	}
	go e.flusher()
	return e
}

// Emit records one (metric, value, labels) tuple. Never blocks —
// stderr writes are pushed to a bounded warnCh drained by the flusher
// (§7(a)). nil-Emitter receiver is a no-op.
func (e *Emitter) Emit(_ context.Context, metric MetricKey, value any, labels map[string]string) error {
	if e == nil {
		return nil
	}
	if !validMetric[metric] {
		e.warn(fmt.Sprintf("probes: dropping unknown metric %q", metric))
		return nil
	}
	// Sanitize labels BEFORE the channel send so a slow flusher cannot
	// race with caller mutations (§7(b)).
	sanitized := make(map[string]string, len(labels))
	for k, v := range labels {
		sanitized[k] = secretscrub.Sanitize(v)
	}
	rec := Record{
		Metric:          metric,
		Value:           value,
		Labels:          sanitized,
		EmittedAt:       time.Now(),
		EmittedAtMonoNs: time.Since(e.mono).Nanoseconds(),
	}
	select {
	case e.ch <- rec:
	default:
		// Channel full → drop, bump counter, warn (off-path).
		atomic.AddInt64(&e.dropped, 1)
		e.warn(fmt.Sprintf("probes: buffer full, dropped %s record", metric))
	}
	return nil
}

// Warn queues a warning line for the flusher to write. If the warn
// channel is itself full, the warn is dropped and warnDropped
// increments — a summary line at Close reports the count. NEVER
// blocks the caller (spec §7(a)).
//
// Exported so probe helpers (setup.go, humanloop.go, wrongctx.go) can
// route their diagnostics off the runner's hot path. nil-Emitter
// receiver is a no-op.
func (e *Emitter) Warn(msg string) {
	if e == nil {
		return
	}
	select {
	case e.warnCh <- msg:
	default:
		atomic.AddInt64(&e.warnDropped, 1)
	}
}

// warn is the unexported alias kept for existing internal call sites.
func (e *Emitter) warn(msg string) { e.Warn(msg) }

// Close stops the record-collector goroutine and returns accumulated
// records in emission order. Idempotent: the first call returns the
// drained slice; subsequent calls return nil, nil (spec §3.1
// "subsequent calls return an empty slice + nil error").
//
// Close MUST NOT block on stderr I/O — spec §7(a). Sequencing:
//  1. Signal done → both flusher goroutines start their drain-and-exit paths.
//  2. Unconditionally wait on drained — the record collector does zero
//     stderr I/O, so this is safe regardless of stderr state.
//  3. Bounded wait on warnDrained — a healthy stderr flushes the pending
//     warns before Close returns, but a wedged stderr caps the wait at
//     closeWarnWait so Close still returns promptly. This restores the
//     "diagnostics visible after Close" ordering that existing tests
//     and post-run diagnostics depend on, without re-introducing the
//     wedged-stderr wedge the earlier P1 fix was designed to prevent.
func (e *Emitter) Close() ([]Record, error) {
	if e == nil {
		return nil, nil
	}
	first := false
	e.closeOnce.Do(func() {
		first = true
		close(e.done)
		e.result = <-e.drained
		select {
		case <-e.warnDrained:
		case <-time.After(closeWarnWait):
			// Wedged stderr; leave warnWriter running (it exits on its
			// own if stderr ever unblocks). Documented in the summary
			// comment above.
		}
	})
	if first {
		return e.result, nil
	}
	return nil, nil
}

// flusher runs two independent loops:
//
//   - the record collector drains e.ch into an in-memory slice and
//     signals e.drained on Close — this is the ONLY thing Close waits
//     on, so it must never do stderr I/O (spec §7(a)).
//
//   - the warn writer drains e.warnCh into stderr in a separate
//     goroutine. Because it runs off the Close path, a wedged stderr
//     wedges only this goroutine; the runner's main goroutine
//     continues.
func (e *Emitter) flusher() {
	// Spin up the warn writer.
	go e.warnWriter()

	var buf []Record
	for {
		select {
		case r := <-e.ch:
			buf = append(buf, r)
		case <-e.done:
			// Drain remaining buffered records — no stderr writes on
			// this path so Close never blocks.
			for {
				select {
				case r := <-e.ch:
					buf = append(buf, r)
				default:
					e.drained <- buf
					return
				}
			}
		}
	}
}

// warnWriter drains warnCh into stderr. Runs in a separate goroutine
// so a slow stderr does NOT back-pressure Close. Signals warnDrained
// once its drain-then-exit path completes; a wedged stderr keeps this
// goroutine alive (never signals warnDrained) but the runner has
// already returned via Close's bounded wait.
func (e *Emitter) warnWriter() {
	for {
		select {
		case w := <-e.warnCh:
			fmt.Fprintln(e.stderr, w)
		case <-e.done:
			// Best-effort drain: try to write everything queued at
			// the moment of Close, then emit the overflow summary,
			// signal warnDrained, and exit. If stderr blocks
			// mid-drain this goroutine wedges before signalling —
			// Close's bounded wait handles that case.
			for {
				select {
				case w := <-e.warnCh:
					fmt.Fprintln(e.stderr, w)
				default:
					if wd := atomic.LoadInt64(&e.warnDropped); wd > 0 {
						fmt.Fprintf(e.stderr, "probes: %d warn(s) dropped due to warn-channel overflow\n", wd)
					}
					close(e.warnDrained)
					return
				}
			}
		}
	}
}
