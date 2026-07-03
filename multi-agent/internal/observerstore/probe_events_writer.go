package observerstore

// WT-2-overhead-probes: writer for the probe_events table. This file is the
// SINGLE structural gate that enforces "no caller-supplied timestamps at the
// observer boundary" (spec §6 (b)):
//
//   - Kind, ProbeSpanHandle, ProbeEventWriter are the only exported types.
//   - ProbeSpanHandle's time-carrying fields (started / nonce / writer) are
//     unexported so cross-package callers cannot construct a forged handle
//     via struct literal.
//   - insertRow and probeEventRow are unexported so only
//     (*ProbeSpanHandle).End can drive a DB write; End stamps time.Now()
//     locally.
//
// See docs/specs/wt2-overhead-probes.spec.md §3.3.C for the full contract.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"expvar"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"sync/atomic"
	"time"
)

// Kind enumerates the three probe span kinds. The type is deliberately
// distinct from string so a bare typo like "driver_planing" cannot be
// passed at the call site — callers must reference one of the three
// exported constants. The underlying string values are the exact literals
// persisted to probe_events.probe_kind and consumed by the SELECT template
// in spec §4.5.
type Kind string

const (
	KindDriverPlanning Kind = "driver_planning"
	KindTaskDispatch   Kind = "task_dispatch"
	KindObserverWrite  Kind = "observer_write"
)

// validKinds is the allowlist checked by StartSpan and insertRow. Any
// value outside this set is rejected before time.Now() is called.
var validKinds = map[Kind]struct{}{
	KindDriverPlanning: {},
	KindTaskDispatch:   {},
	KindObserverWrite:  {},
}

// probeConvIDRE mirrors spec §6 (c). Must accept 8..128 chars from the
// alphabet [A-Za-z0-9_-] and reject everything else. Duplicated here
// (not imported from tools/eval/microbench/common) because internal/
// packages cannot depend on tools/.
var probeConvIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

// probeIDRejectedTotal counts spans that were rejected at StartSpan or
// insertRow because their conversation_id failed regex validation. Exposed
// via expvar so a running slave-agent can be scraped and drift alarmed on.
var probeIDRejectedTotal = expvar.NewInt("probe_id_rejected_total")

// ErrProbeUnknownKind is returned by StartSpan when the caller passes a
// Kind outside {KindDriverPlanning, KindTaskDispatch, KindObserverWrite}.
var ErrProbeUnknownKind = errors.New("probe_events: unknown kind")

// ErrProbeInvalidConvID is returned by StartSpan / insertRow when the
// conversation_id fails the §6 (c) regex.
var ErrProbeInvalidConvID = errors.New("probe_events: invalid conversation_id")

// ErrProbeNegativeDuration is returned by insertRow if a computed
// duration_ns is negative. Guards against a future refactor losing the
// monotonic reading on the retained start time.
var ErrProbeNegativeDuration = errors.New("probe_events: negative duration_ns")

// probeNonce is a process-local monotonic counter mixed into event_id so
// two spans landing on the same nanosecond in the same conv/kind still
// produce distinct primary keys.
var probeNonce atomic.Uint64

// ProbeEventWriter is the observerstore-side interface every probe uses.
// It exposes only StartSpan and the IsNoop fail-loud hook — the DB-touching
// path is unexported (insertRow) so only (*ProbeSpanHandle).End can drive
// a write.
type ProbeEventWriter interface {
	StartSpan(kind Kind, convID string) (*ProbeSpanHandle, error)
	IsNoop() bool

	// insertRow is unexported: implementations MUST be in the observerstore
	// package. This is the structural gate that prevents an external package
	// from bypassing (*ProbeSpanHandle).End's time.Now() stamp.
	insertRow(ctx context.Context, r probeEventRow) error
}

// ProbeSpanHandle is opaque outside this package: all fields are
// unexported, so a caller in another package cannot forge a
// (kind, convID, started, nonce) tuple via struct literal. The only way
// to obtain a non-zero handle is via ProbeEventWriter.StartSpan.
type ProbeSpanHandle struct {
	kind    Kind
	convID  string
	started time.Time // monotonic reading preserved
	nonce   uint64
	writer  ProbeEventWriter
}

// End closes the span. span_end_at is stamped INSIDE this function; the
// duration is end.Sub(started).Nanoseconds() with both times still
// carrying their monotonic readings, so wall-clock jumps between
// StartSpan and End cannot corrupt duration_ns.
//
// End is a no-op on the noop writer. When the writer returns a non-nil
// error End logs the failure but does NOT propagate it — probes are an
// audit artifact, not business logic; silently failing writers still need
// to reveal themselves via the log line.
func (h *ProbeSpanHandle) End(ctx context.Context) error {
	if h == nil || h.writer == nil || h.writer.IsNoop() {
		return nil
	}
	end := time.Now()
	dur := end.Sub(h.started).Nanoseconds()
	row := probeEventRow{
		eventID:    deriveEventID(h.convID, h.kind, h.started, h.nonce),
		kind:       h.kind,
		convID:     h.convID,
		startedAt:  h.started,
		endedAt:    end,
		durationNs: dur,
	}
	if err := h.writer.insertRow(ctx, row); err != nil {
		log.Printf("[probe] write failed: %v kind=%s conv=%s event=%s", err, h.kind, h.convID, row.eventID)
		return err
	}
	return nil
}

// probeEventRow is the unexported serialization shape sent to insertRow.
// Both time.Time fields carry monotonic readings (stamped inside this
// package); external code cannot build one because the type itself is
// unexported.
type probeEventRow struct {
	eventID          string
	kind             Kind
	convID           string
	startedAt        time.Time
	endedAt          time.Time
	durationNs       int64
	wallclockDeltaMs int64
}

// deriveEventID mirrors WT-1-routing-trace §6 (f) exactly. sha256 truncated
// to 16 bytes → 32 hex chars → fits comfortably in TEXT PRIMARY KEY.
func deriveEventID(convID string, kind Kind, start time.Time, nonce uint64) string {
	sum := sha256.Sum256([]byte(
		convID + "|" + string(kind) + "|" +
			strconv.FormatInt(start.UnixNano(), 10) + "|" +
			strconv.FormatUint(nonce, 10),
	))
	return hex.EncodeToString(sum[:16])
}

// noopProbeWriter is the default writer installed at package init. Its
// StartSpan returns a handle whose writer is also the noop, so End is a
// pure no-op. IsNoop returns true so cmd-side wiring can fail-loud if it
// forgot to call SetProbeWriter.
type noopProbeWriter struct{}

func (noopProbeWriter) StartSpan(kind Kind, convID string) (*ProbeSpanHandle, error) {
	// The noop still enforces validation so ill-formed callers see the
	// same error whether or not a real writer is installed — otherwise
	// production would see an error the dev's noop-backed test missed.
	if _, ok := validKinds[kind]; !ok {
		return nil, fmt.Errorf("%w %q", ErrProbeUnknownKind, kind)
	}
	if !probeConvIDRE.MatchString(convID) {
		probeIDRejectedTotal.Add(1)
		return nil, ErrProbeInvalidConvID
	}
	return &ProbeSpanHandle{kind: kind, convID: convID, writer: noopProbeWriter{}}, nil
}
func (noopProbeWriter) IsNoop() bool { return true }
func (noopProbeWriter) insertRow(context.Context, probeEventRow) error {
	return nil
}

// probeWriterBox is a fixed concrete type stored in the atomic.Value so
// swapping noop → real → noop doesn't trip the "same concrete type"
// invariant (same trick as WT-1-routing-trace §2.2).
type probeWriterBox struct{ w ProbeEventWriter }

var activeProbeWriter atomic.Value // holds probeWriterBox

func init() { activeProbeWriter.Store(probeWriterBox{w: noopProbeWriter{}}) }

// SetProbeWriter installs w as the current writer. Passing nil reverts to
// the noop. Goroutine-safe: SetProbeWriter and concurrent StartSpan callers
// never race because the swap is a single atomic.Value.Store.
func SetProbeWriter(w ProbeEventWriter) {
	if w == nil {
		w = noopProbeWriter{}
	}
	activeProbeWriter.Store(probeWriterBox{w: w})
}

// CurrentProbeWriter returns the currently installed writer. Injection
// helpers in orchestrator/executor call this on every span start; the
// atomic.Value load is cheap enough that we don't cache.
func CurrentProbeWriter() ProbeEventWriter {
	return activeProbeWriter.Load().(probeWriterBox).w
}

// IsNoopProbeWriter reports whether the currently installed writer is the
// noop. Cmd-side wiring calls this in main so it can log.Fatal if it
// forgot to call SetProbeWriter — mirrors WT-1's dispatch.IsNoopWriter().
func IsNoopProbeWriter() bool { return CurrentProbeWriter().IsNoop() }

// probeEventsWriter is the real SQLite-backed implementation.
//
// SQLite-only in this WT (same rationale as route_reasons_writer.go:47):
// the INSERT uses `?` placeholders. pgx/v5/stdlib does not rewrite `?`,
// so wiring against pg would fail every call. A pg-native writer with
// `$N` placeholders lives in a follow-up WT alongside the pg DDL.
type probeEventsWriter struct{ db *sql.DB }

// NewProbeEventWriter returns a SQLite-backed ProbeEventWriter. The
// schema migration (CREATE TABLE IF NOT EXISTS probe_events) is applied
// by OpenSQLite via the embedded schema.sql.
func NewProbeEventWriter(db *sql.DB) ProbeEventWriter {
	return &probeEventsWriter{db: db}
}

func (w *probeEventsWriter) IsNoop() bool { return false }

func (w *probeEventsWriter) StartSpan(kind Kind, convID string) (*ProbeSpanHandle, error) {
	// Validate BEFORE calling time.Now() so a rejected span costs nothing.
	if _, ok := validKinds[kind]; !ok {
		return nil, fmt.Errorf("%w %q", ErrProbeUnknownKind, kind)
	}
	if !probeConvIDRE.MatchString(convID) {
		probeIDRejectedTotal.Add(1)
		return nil, ErrProbeInvalidConvID
	}
	return &ProbeSpanHandle{
		kind:    kind,
		convID:  convID,
		started: time.Now(),
		nonce:   probeNonce.Add(1),
		writer:  w,
	}, nil
}

func (w *probeEventsWriter) insertRow(ctx context.Context, r probeEventRow) error {
	if _, ok := validKinds[r.kind]; !ok {
		return fmt.Errorf("%w %q", ErrProbeUnknownKind, r.kind)
	}
	if !probeConvIDRE.MatchString(r.convID) {
		return ErrProbeInvalidConvID
	}
	if r.durationNs < 0 {
		return ErrProbeNegativeDuration
	}
	_, err := w.db.ExecContext(ctx,
		`INSERT INTO probe_events(
            event_id, probe_kind, conversation_id,
            span_start_at, span_end_at, duration_ns, wallclock_delta_ms)
         VALUES(?,?,?,?,?,?,?)
         ON CONFLICT(event_id) DO NOTHING`,
		r.eventID, string(r.kind), r.convID,
		r.startedAt.UTC().Format(time.RFC3339Nano),
		r.endedAt.UTC().Format(time.RFC3339Nano),
		r.durationNs, r.wallclockDeltaMs,
	)
	return err
}
