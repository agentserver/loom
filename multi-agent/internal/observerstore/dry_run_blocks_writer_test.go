package observerstore

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"
)

// openTestDB opens an in-memory SQLite DB with the observer schema
// applied. Uses OpenSQLite so the embedded schema.sql is executed —
// this exercises the migration path in addition to the writer.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	store, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store.DB()
}

// Schema check: dry_run_blocks table + indexes must be applied by
// OpenSQLite via the embedded schema.sql.
func TestDryRunBlocks_SchemaLoaded(t *testing.T) {
	db := openTestDB(t)

	var name string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='dry_run_blocks'`).Scan(&name)
	if err != nil {
		t.Fatalf("dry_run_blocks table not applied by schema.sql: %v", err)
	}

	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='dry_run_blocks' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := map[string]bool{
		"idx_dry_run_blocks_conv":          false,
		"idx_dry_run_blocks_contract_hash": false,
		"idx_dry_run_blocks_attempt":       false,
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		if _, ok := want[n]; ok {
			want[n] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("index %s not created", name)
		}
	}
}

// Writer scaffold present.
func TestDryRunBlockWriter_ConstructorReturnsNonNil(t *testing.T) {
	db := openTestDB(t)
	w := NewDryRunBlockWriter(db)
	if w == nil {
		t.Fatal("NewDryRunBlockWriter returned nil")
	}
}

// Basic round-trip: insert one row, read it back with all columns intact.
func TestWriteDryRunBlock_RoundTrip(t *testing.T) {
	db := openTestDB(t)
	w := NewDryRunBlockWriter(db)
	row := DryRunBlockRow{
		BlockID:                "blk-abc123",
		AttemptID:              "att-xyz789",
		ConversationID:         "conv-1",
		ExperimentID:           "exp-A",
		ContractHash:           "sha256:aaaa",
		CapabilitySnapshotHash: "sha256:bbbb",
		BlockKind:              "missing_file",
		Field:                  "data_contract.read_artifacts[0].name",
		Expected:               "config.yaml",
		Actual:                 "no snapshot file resource matches",
		Detail:                 "data_contract.read_artifacts[0].name: expected config.yaml, actual no snapshot file resource matches",
		BlockedAt:              time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC),
	}
	if err := w.WriteDryRunBlock(context.Background(), row); err != nil {
		t.Fatalf("write: %v", err)
	}
	var kind, field, expected, actual string
	err := db.QueryRow(`SELECT block_kind, field, expected, actual FROM dry_run_blocks WHERE block_id=?`, row.BlockID).
		Scan(&kind, &field, &expected, &actual)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if kind != row.BlockKind || field != row.Field || expected != row.Expected || actual != row.Actual {
		t.Errorf("round-trip mismatch:\n got  %+v\n want %+v",
			[]string{kind, field, expected, actual},
			[]string{row.BlockKind, row.Field, row.Expected, row.Actual})
	}
}

// ON CONFLICT(block_id) DO NOTHING — repeated writes are idempotent.
func TestWriteDryRunBlock_IdempotentOnBlockID(t *testing.T) {
	db := openTestDB(t)
	w := NewDryRunBlockWriter(db)
	row := DryRunBlockRow{
		BlockID: "blk-dedup", AttemptID: "a1", ConversationID: "c1",
		ContractHash: "h1", CapabilitySnapshotHash: "s1",
		BlockKind: "policy_violation", BlockedAt: time.Now().UTC(),
	}
	if err := w.WriteDryRunBlock(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteDryRunBlock(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM dry_run_blocks WHERE block_id=?`, "blk-dedup").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("expected 1 row after two identical writes; got %d", n)
	}
}

// §7(c) writer truncates at 8 KiB regardless of upstream.
func TestWriteDryRunBlock_DetailTruncatedAt8KiB(t *testing.T) {
	db := openTestDB(t)
	w := NewDryRunBlockWriter(db)
	huge := make([]byte, 20*1024)
	for i := range huge {
		huge[i] = 'x'
	}
	row := DryRunBlockRow{
		BlockID: "blk-huge", AttemptID: "a1", ConversationID: "c1",
		ContractHash: "h1", CapabilitySnapshotHash: "s1",
		BlockKind: "wrong_version", Detail: string(huge),
		BlockedAt: time.Now().UTC(),
	}
	if err := w.WriteDryRunBlock(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := db.QueryRow(`SELECT detail FROM dry_run_blocks WHERE block_id=?`, "blk-huge").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if len(stored) > dryRunBlockMaxDetailBytes {
		t.Errorf("writer failed to truncate detail: stored %d bytes > cap %d", len(stored), dryRunBlockMaxDetailBytes)
	}
}

// §7(c) SQL-meta chars in a caller-controlled column must round-trip
// verbatim through parameterized ? placeholders. A string-concat bug
// would either error or produce a corrupt row (or, worst-case, drop
// the table).
func TestWriteDryRunBlock_SQLMetaCharactersPersistVerbatim(t *testing.T) {
	db := openTestDB(t)
	w := NewDryRunBlockWriter(db)
	meta := `'; DROP TABLE dry_run_blocks; --`
	row := DryRunBlockRow{
		BlockID: "blk-meta", AttemptID: "a1", ConversationID: "c1",
		ContractHash: "h1", CapabilitySnapshotHash: "s1",
		BlockKind: "forbidden_cred",
		Field:     meta,
		Expected:  meta,
		Actual:    meta,
		Detail:    meta,
		BlockedAt: time.Now().UTC(),
	}
	if err := w.WriteDryRunBlock(context.Background(), row); err != nil {
		t.Fatalf("write: %v", err)
	}
	// If SQL-injection succeeded, the table would be gone.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM dry_run_blocks WHERE block_id=?`, "blk-meta").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("row not persisted (injection attempt broke schema?): n=%d", n)
	}
}

// CI-conditional perf gate: WriteDryRunBlock should complete in < 5 ms
// on any modern SQLite backend. Skipped under -short or when CI is set.
func TestWriteDryRunBlock_PerfBudget(t *testing.T) {
	if testing.Short() || os.Getenv("CI") != "" {
		t.Skip("perf assertion skipped in short/CI mode")
	}
	db := openTestDB(t)
	w := NewDryRunBlockWriter(db)
	row := DryRunBlockRow{
		BlockID: "blk-perf", AttemptID: "a1", ConversationID: "c1",
		ContractHash: "h1", CapabilitySnapshotHash: "s1",
		BlockKind: "policy_violation", BlockedAt: time.Now().UTC(),
	}
	start := time.Now()
	if err := w.WriteDryRunBlock(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el > 5*time.Millisecond {
		t.Errorf("write exceeded 5ms budget: %v", el)
	}
}

// §7(g) reverse audit: given only dry_run_blocks + events, the §6.1
// SELECT must return the correct PolicyViolationPreventionRate.
// Seeded shape: 5 dry-run attempts, 2 with policy_violation, 1 with
// wrong_version, 2 clean. Expected rate = 2/5 = 0.4.
func TestConsumerView_PolicyViolationPreventionRate(t *testing.T) {
	db := openTestDB(t)
	w := NewDryRunBlockWriter(db)
	now := time.Now().UTC()

	seed := []struct {
		attemptID, blockKind string
	}{
		{"att-1", "policy_violation"},
		{"att-2", "policy_violation"},
		{"att-3", "wrong_version"},
		// att-4 and att-5: clean (only events, no blocks).
	}
	for i, s := range seed {
		if err := w.WriteDryRunBlock(context.Background(), DryRunBlockRow{
			BlockID: "blk-" + s.attemptID + "-" + s.blockKind, AttemptID: s.attemptID,
			ConversationID: "conv-" + s.attemptID, ExperimentID: "exp-1",
			ContractHash: "h1", CapabilitySnapshotHash: "s1",
			BlockKind: s.blockKind, BlockedAt: now,
		}); err != nil {
			t.Fatalf("seed block %d: %v", i, err)
		}
	}

	// Seed 5 PreExecutionFaultCatchRate events (denominator).
	for _, id := range []string{"att-1", "att-2", "att-3", "att-4", "att-5"} {
		payload := `{"attempt_id":"` + id + `","numerator":0,"blocks_total":0,"contract_hash":"h1","experiment_id":"exp-1"}`
		_, err := db.Exec(`INSERT INTO events(event_id, ts, workspace_id, agent_id, agent_role, type, task_id, payload)
			VALUES(?, ?, 'ws', 'a', 'driver', ?, '', ?)`,
			"ev-"+id, now.Format(time.RFC3339Nano), "PreExecutionFaultCatchRate", payload)
		if err != nil {
			t.Fatalf("seed event %s: %v", id, err)
		}
	}

	// The §6.1 SELECT (unified form): numerator = DISTINCT attempts
	// with a policy_violation block; denominator = DISTINCT attempts
	// observed as PreExecutionFaultCatchRate events.
	const q = `
WITH policy_blocked AS (
    SELECT DISTINCT attempt_id FROM dry_run_blocks WHERE block_kind='policy_violation'
),
all_attempts AS (
    SELECT DISTINCT json_extract(payload, '$.attempt_id') AS attempt_id
    FROM events WHERE type='PreExecutionFaultCatchRate'
)
SELECT (SELECT COUNT(*) FROM policy_blocked) * 1.0 /
       NULLIF((SELECT COUNT(*) FROM all_attempts), 0);
`
	var rate sql.NullFloat64
	if err := db.QueryRow(q).Scan(&rate); err != nil {
		t.Fatalf("consumer SELECT: %v", err)
	}
	if !rate.Valid {
		t.Fatal("SELECT returned NULL")
	}
	if got, want := rate.Float64, 0.4; got != want {
		t.Errorf("PolicyViolationPreventionRate: got %v want %v", got, want)
	}
}
