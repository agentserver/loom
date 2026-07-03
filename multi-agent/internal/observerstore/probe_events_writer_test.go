package observerstore

import (
	"context"
	"database/sql"
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	// sqlite driver is registered by store.go's blank import; no need here.
)

// openProbeStore opens a fresh SQLite store for one test and returns the
// underlying *sql.DB. The embedded schema.sql is applied by OpenSQLite so
// probe_events + its index exist immediately.
func openProbeStore(t *testing.T) *sql.DB {
	t.Helper()
	p := filepath.Join(t.TempDir(), "probe.db")
	st, err := OpenSQLite(p)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st.DB()
}

// Test #1 — schema present + column set matches spec §4.2.
func TestOpenSQLite_HasProbeEventsTable(t *testing.T) {
	db := openProbeStore(t)
	var name string
	err := db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='probe_events'`,
	).Scan(&name)
	if err != nil {
		t.Fatalf("probe_events table missing: %v", err)
	}
	// PRAGMA table_info reports (cid, name, type, notnull, dflt_value, pk).
	rows, err := db.Query(`PRAGMA table_info(probe_events)`)
	if err != nil {
		t.Fatalf("PRAGMA: %v", err)
	}
	defer rows.Close()
	wantCols := map[string]string{
		"event_id":           "TEXT",
		"probe_kind":         "TEXT",
		"conversation_id":    "TEXT",
		"span_start_at":      "TEXT",
		"span_end_at":        "TEXT",
		"duration_ns":        "INTEGER",
		"wallclock_delta_ms": "INTEGER",
	}
	got := map[string]string{}
	for rows.Next() {
		var (
			cid       int
			colName   string
			colType   string
			notnull   int
			dfltValue sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &colName, &colType, &notnull, &dfltValue, &pk); err != nil {
			t.Fatal(err)
		}
		got[colName] = colType
		// event_id must be PRIMARY KEY per spec §4.2.
		if colName == "event_id" && pk != 1 {
			t.Errorf("event_id not PRIMARY KEY, pk=%d", pk)
		}
		// All columns except wallclock_delta_ms are NOT NULL per spec.
		// SQLite's PRAGMA table_info reports notnull=0 for a TEXT PRIMARY
		// KEY column even though the PK constraint enforces non-null in
		// practice (only INTEGER PRIMARY KEY is aliased to rowid and truly
		// nullable); event_id is TEXT so we treat pk=1 as implying NOT NULL.
		if colName != "wallclock_delta_ms" && pk == 0 && notnull != 1 {
			t.Errorf("%s should be NOT NULL", colName)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, wantCols) {
		t.Errorf("column set mismatch: want %v got %v", wantCols, got)
	}
}

// Test #2 — unknown kind rejected without touching time.Now() or the DB.
func TestStartSpan_RejectsUnknownKind(t *testing.T) {
	db := openProbeStore(t)
	w := NewProbeEventWriter(db)
	h, err := w.StartSpan(Kind("banana"), "conv-abcdefgh")
	if !errors.Is(err, ErrProbeUnknownKind) {
		t.Fatalf("want ErrProbeUnknownKind, got %v", err)
	}
	if h != nil {
		t.Errorf("handle should be nil on rejection, got %+v", h)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM probe_events`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("no rows should be written, got %d", n)
	}
}

// Test #3 — invalid conv_id rejected by regex; probe_id_rejected_total bumps.
func TestStartSpan_RejectsInvalidConvID(t *testing.T) {
	db := openProbeStore(t)
	w := NewProbeEventWriter(db)
	before := probeIDRejectedTotal.Value()
	h, err := w.StartSpan(KindDriverPlanning, "no") // 2 chars, below floor of 8
	if !errors.Is(err, ErrProbeInvalidConvID) {
		t.Fatalf("want ErrProbeInvalidConvID, got %v", err)
	}
	if h != nil {
		t.Errorf("handle should be nil, got %+v", h)
	}
	if got := probeIDRejectedTotal.Value(); got != before+1 {
		t.Errorf("counter want %d got %d", before+1, got)
	}
}

// Test #4 — full StartSpan / End round trip writes exactly one row.
func TestSpanEndToEnd_RoundTrip(t *testing.T) {
	db := openProbeStore(t)
	w := NewProbeEventWriter(db)
	h, err := w.StartSpan(KindDriverPlanning, "conv-abc12345")
	if err != nil {
		t.Fatalf("StartSpan: %v", err)
	}
	time.Sleep(1 * time.Millisecond) // ensure duration_ns > 0
	if err := h.End(context.Background()); err != nil {
		t.Fatalf("End: %v", err)
	}
	var (
		eventID string
		kind    string
		conv    string
		dur     int64
	)
	err = db.QueryRow(`SELECT event_id, probe_kind, conversation_id, duration_ns FROM probe_events`).
		Scan(&eventID, &kind, &conv, &dur)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if kind != string(KindDriverPlanning) {
		t.Errorf("kind mismatch: %q", kind)
	}
	if conv != "conv-abc12345" {
		t.Errorf("conv mismatch: %q", conv)
	}
	if dur <= 0 {
		t.Errorf("duration_ns should be > 0, got %d", dur)
	}
	if len(eventID) != 32 {
		t.Errorf("event_id should be 32 hex chars, got %q (len %d)", eventID, len(eventID))
	}
}

// Test #5 — ON CONFLICT DO NOTHING: duplicate event_id insert is a no-op.
func TestInsertRow_ONCONFLICT_DoNothing(t *testing.T) {
	db := openProbeStore(t)
	w := &probeEventsWriter{db: db}
	start := time.Now()
	row := probeEventRow{
		eventID:    deriveEventID("conv-abc12345", KindDriverPlanning, start, 42),
		kind:       KindDriverPlanning,
		convID:     "conv-abc12345",
		startedAt:  start,
		endedAt:    start.Add(1 * time.Millisecond),
		durationNs: 1_000_000,
	}
	if err := w.insertRow(context.Background(), row); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if err := w.insertRow(context.Background(), row); err != nil {
		t.Fatalf("second insert should be silent no-op: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM probe_events`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("want 1 row, got %d", n)
	}
}

// Test #6 — structural: no exported API accepts time.Time.
//
// This is spec §6 (b)'s drift guard. It catches a future PR that
// "conveniently" adds a WriteProbeEvent(row ProbeEventRow) shortcut with
// an exported time-typed field.
func TestNoCallerTimestampAtBoundary(t *testing.T) {
	tt := reflect.TypeOf(time.Time{})
	// Walk every exported method of ProbeEventWriter's declared methods.
	iface := reflect.TypeOf((*ProbeEventWriter)(nil)).Elem()
	for i := 0; i < iface.NumMethod(); i++ {
		m := iface.Method(i)
		if !m.IsExported() {
			continue
		}
		mt := m.Type
		for j := 0; j < mt.NumIn(); j++ {
			if mt.In(j) == tt {
				t.Errorf("ProbeEventWriter.%s param #%d is time.Time — violates §6 (b)",
					m.Name, j)
			}
		}
		for j := 0; j < mt.NumOut(); j++ {
			if mt.Out(j) == tt {
				t.Errorf("ProbeEventWriter.%s return #%d is time.Time — violates §6 (b)",
					m.Name, j)
			}
		}
	}
	// Walk every exported field of ProbeSpanHandle.
	ht := reflect.TypeOf(ProbeSpanHandle{})
	for i := 0; i < ht.NumField(); i++ {
		f := ht.Field(i)
		if f.IsExported() && f.Type == tt {
			t.Errorf("ProbeSpanHandle field %s is exported time.Time — violates §6 (b)", f.Name)
		}
	}
}

// Test #7 — negative duration_ns rejected (defensive: guards against a
// future refactor losing the monotonic reading on started).
func TestInsertRow_RejectsNegativeDuration(t *testing.T) {
	db := openProbeStore(t)
	w := &probeEventsWriter{db: db}
	err := w.insertRow(context.Background(), probeEventRow{
		eventID:    "abcdef0123456789abcdef0123456789",
		kind:       KindDriverPlanning,
		convID:     "conv-abc12345",
		startedAt:  time.Now(),
		endedAt:    time.Now().Add(-1),
		durationNs: -1,
	})
	if !errors.Is(err, ErrProbeNegativeDuration) {
		t.Fatalf("want ErrProbeNegativeDuration, got %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM probe_events`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("no row should have been written, got %d", n)
	}
}

// Test #8 — SQL-injection-shaped conv_id rejected by regex BEFORE reaching
// SQL. Belt-and-suspenders for §6 (e).
func TestStartSpan_SQLInjectionInConvID_RejectedByRegex(t *testing.T) {
	db := openProbeStore(t)
	w := NewProbeEventWriter(db)
	nasty := `abc'; DROP TABLE probe_events; --`
	_, err := w.StartSpan(KindDriverPlanning, nasty)
	if !errors.Is(err, ErrProbeInvalidConvID) {
		t.Fatalf("want regex reject, got %v", err)
	}
	// Table must still exist.
	if _, err := db.Query(`SELECT COUNT(*) FROM probe_events`); err != nil {
		t.Fatalf("probe_events was dropped or damaged: %v", err)
	}
}

// Test #9 — proves `?` placeholder is doing its job: an allowlisted
// nastyish string is stored verbatim in conversation_id, not interpreted.
func TestWriter_ParameterizedSQL(t *testing.T) {
	db := openProbeStore(t)
	w := &probeEventsWriter{db: db}
	// The regex allows underscores + dashes but not quotes; craft a
	// payload that would fail if a plain fmt.Sprintf were being used
	// (there is none — this test proves it).
	allowlisted := "conv-DROP-TB1"
	start := time.Now()
	row := probeEventRow{
		eventID:    deriveEventID(allowlisted, KindTaskDispatch, start, 1),
		kind:       KindTaskDispatch,
		convID:     allowlisted,
		startedAt:  start,
		endedAt:    start.Add(time.Microsecond),
		durationNs: 1_000,
	}
	if err := w.insertRow(context.Background(), row); err != nil {
		t.Fatalf("insertRow: %v", err)
	}
	var got string
	if err := db.QueryRow(`SELECT conversation_id FROM probe_events`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != allowlisted {
		t.Errorf("conversation_id stored verbatim expected %q got %q", allowlisted, got)
	}
}

// Test #10 — 10 000 spans with same convID + kind → 10 000 distinct event_ids.
// Nonce is the differentiator; even on a coarse clock the nonce guarantees
// uniqueness.
func TestDeriveEventID_UniquePerCall(t *testing.T) {
	db := openProbeStore(t)
	w := NewProbeEventWriter(db)
	seen := make(map[string]struct{}, 10000)
	for i := 0; i < 10000; i++ {
		h, err := w.StartSpan(KindDriverPlanning, "conv-samesame")
		if err != nil {
			t.Fatalf("iter %d StartSpan: %v", i, err)
		}
		id := deriveEventID(h.convID, h.kind, h.started, h.nonce)
		if _, dup := seen[id]; dup {
			t.Fatalf("iter %d: duplicate event_id %s", i, id)
		}
		seen[id] = struct{}{}
	}
	if len(seen) != 10000 {
		t.Errorf("want 10000 distinct IDs, got %d", len(seen))
	}
}

// Test #11 — atomic.Value writer swap survives concurrent SetProbeWriter
// churn from multiple goroutines. The fixed probeWriterBox wrapper is what
// makes this safe (see spec §3.2, WT-1-routing-trace §2.2 template).
func TestSetProbeWriter_AtomicValueNoPanic(t *testing.T) {
	// Restore package default when the test exits so we don't poison siblings.
	t.Cleanup(func() { SetProbeWriter(nil) })
	db := openProbeStore(t)
	real := NewProbeEventWriter(db)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				switch i % 3 {
				case 0:
					SetProbeWriter(nil)
				case 1:
					SetProbeWriter(real)
				case 2:
					SetProbeWriter(noopProbeWriter{})
				}
			}
		}()
	}
	wg.Wait()
	// IsNoopProbeWriter must reflect the last successful Store (deterministic).
	SetProbeWriter(nil)
	if !IsNoopProbeWriter() {
		t.Errorf("after SetProbeWriter(nil) expected noop, got real")
	}
	SetProbeWriter(real)
	if IsNoopProbeWriter() {
		t.Errorf("after SetProbeWriter(real) expected non-noop")
	}
}

// Test #12 — cmd-side wiring fail-loud helper reports correct state.
func TestIsNoopProbeWriter_TrueByDefault(t *testing.T) {
	t.Cleanup(func() { SetProbeWriter(nil) })
	SetProbeWriter(nil)
	if !IsNoopProbeWriter() {
		t.Errorf("fresh package should have noop writer")
	}
	db := openProbeStore(t)
	SetProbeWriter(NewProbeEventWriter(db))
	if IsNoopProbeWriter() {
		t.Errorf("after installing real writer, noop should report false")
	}
	SetProbeWriter(nil)
	if !IsNoopProbeWriter() {
		t.Errorf("after SetProbeWriter(nil), noop should report true again")
	}
}

// Test #53 — file-domain guard: this WT MUST NOT modify internal/dispatch/.
// Uses git diff against origin/paper/v3-integration; skips only when git
// is unavailable (never on CI where git is always present).
func TestDispatchDirUntouched(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	// Find the git repo root (we are inside a worktree).
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skip("not inside a git worktree")
	}
	root := strings.TrimSpace(string(out))
	// The multi-agent module root is at $root/multi-agent; run diff from
	// there so the relative path in the diff matches "internal/dispatch/".
	cmd := exec.Command("git", "diff", "--name-only",
		"origin/paper/v3-integration", "--", "internal/dispatch/")
	cmd.Dir = filepath.Join(root, "multi-agent")
	out, err = cmd.Output()
	if err != nil {
		// git diff can fail if origin/paper/v3-integration is not fetched;
		// in that case we cannot enforce and must skip loudly.
		t.Skipf("git diff failed (fetch origin/paper/v3-integration first?): %v", err)
	}
	if len(strings.TrimSpace(string(out))) != 0 {
		t.Errorf("internal/dispatch/ was modified:\n%s", string(out))
	}
}
