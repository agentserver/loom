//go:build evaltool

package faultinject

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// AuditWriter serialises audit records as single-line JSON with a
// trailing newline. Concurrent writers are mutex-serialised so audit
// lines are never interleaved. Per spec §7 (e), every InjectIfActive
// hit MUST emit one line via Emit; silent injection is a P0 review
// failure.
type AuditWriter struct {
	mu  sync.Mutex
	out io.Writer
	now func() time.Time
}

// NewAuditWriter wraps w (e.g. os.Stderr) as an AuditWriter. If w is nil
// the writer falls back to os.Stderr. If now is nil it defaults to
// time.Now().UTC().
func NewAuditWriter(w io.Writer, now func() time.Time) *AuditWriter {
	if w == nil {
		w = os.Stderr
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &AuditWriter{out: w, now: now}
}

// AuditRecord is the schema of one audit line. The struct's JSON tags
// are the wire-format contract from spec §8.
type AuditRecord struct {
	TS     string    `json:"ts"`
	RunID  string    `json:"run_id"`
	Kind   FaultKind `json:"kind"`
	Hook   string    `json:"hook"`
	Action string    `json:"action"`
	Seq    int       `json:"seq"`
}

// EmitInjected writes a hook-fire audit record (action="injected").
// Called from hookbridge.go when a HookPoint fires. Safe for concurrent
// use; sink errors are surfaced to os.Stderr but do not propagate.
func (a *AuditWriter) EmitInjected(runID string, kind FaultKind, hook string, seq int) {
	a.emit(AuditRecord{
		TS:     a.now().UTC().Format(time.RFC3339Nano),
		RunID:  runID,
		Kind:   kind,
		Hook:   hook,
		Action: "injected",
		Seq:    seq,
	})
}

// emit writes a fully-formed AuditRecord. Lowercase so only the package
// can construct records with arbitrary Action values (currently
// "registered" by the server, "injected" by the bridge).
func (a *AuditWriter) emit(rec AuditRecord) {
	buf, err := json.Marshal(rec)
	if err != nil {
		// json.Marshal of a fixed-shape struct with string/int fields
		// cannot fail in practice; report just in case.
		fmt.Fprintf(os.Stderr, "faultinject: audit marshal failed: %v\n", err)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, err := a.out.Write(append(buf, '\n')); err != nil {
		// Audit sink failure must not silence the inject path; surface to
		// stderr so an operator can investigate.
		fmt.Fprintf(os.Stderr, "faultinject: audit write failed: %v\n", err)
	}
}
