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
// go to bounded warnCh (buffered), drained by the flusher goroutine
// off the hot path. Channel-full warns increment atomic counters that
// the flusher folds into a single summary line on Close.
type Emitter struct {
	ch      chan Record
	warnCh  chan string
	done    chan struct{}
	drained chan []Record
	stderr  io.Writer

	mono time.Time // monotonic reference captured in NewEmitter

	dropped     int64 // atomic — Emit-side buffer overflow count
	warnDropped int64 // atomic — warnCh overflow count

	closeOnce sync.Once
	closed    atomic.Bool
	result    []Record
}

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
		ch:      make(chan Record, bufferSize),
		warnCh:  make(chan string, 32), // small; overflow → atomic counter
		done:    make(chan struct{}),
		drained: make(chan []Record, 1),
		stderr:  stderr,
		mono:    time.Now(),
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

// Close stops the flusher and returns accumulated records in emission
// order. Idempotent.
func (e *Emitter) Close() ([]Record, error) {
	if e == nil {
		return nil, nil
	}
	e.closeOnce.Do(func() {
		close(e.done)
		e.result = <-e.drained
		e.closed.Store(true)
	})
	return e.result, nil
}

func (e *Emitter) flusher() {
	var buf []Record
	for {
		select {
		case r := <-e.ch:
			buf = append(buf, r)
		case w := <-e.warnCh:
			fmt.Fprintln(e.stderr, w) // off the hot path
		case <-e.done:
			// Drain remaining buffered records + warns before returning.
			for {
				select {
				case r := <-e.ch:
					buf = append(buf, r)
				case w := <-e.warnCh:
					fmt.Fprintln(e.stderr, w)
				default:
					if wd := atomic.LoadInt64(&e.warnDropped); wd > 0 {
						fmt.Fprintf(e.stderr, "probes: %d warn(s) dropped due to warn-channel overflow\n", wd)
					}
					e.drained <- buf
					return
				}
			}
		}
	}
}
