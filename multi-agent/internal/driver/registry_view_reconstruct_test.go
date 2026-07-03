package driver

import (
	"bytes"
	"context"
	"log"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/internal/observerstore"
)

// TestReconstructRegistryViewFromAudit_FreshDBNoOp — pre-B6 DB (no
// promotion_audit table) must return nil without touching the view.
func TestReconstructRegistryViewFromAudit_FreshDBNoOp(t *testing.T) {
	// Use a raw connection with only a foreign table so
	// promotion_audit table is missing. Skip: OpenSQLite creates the
	// full schema, so we simulate "no table" by dropping it first.
	s, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer s.Close()
	_, err = s.DB().Exec(`DROP TABLE promotion_audit`)
	require.NoError(t, err)

	resetRegistryForTest()
	err = ReconstructRegistryViewFromAudit(context.Background(), s.DB())
	require.NoError(t, err)
	// LastRegistryHash still returns the empty-bytes sha256.
	require.Equal(t, EmptyBytesSHA256Hex, LastRegistryHash())
}

// TestReconstructRegistryViewFromAudit_EmptyTableNoLog — table
// exists but has zero rows. No log, no view change.
func TestReconstructRegistryViewFromAudit_EmptyTableNoLog(t *testing.T) {
	s, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer s.Close()

	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	resetRegistryForTest()
	require.NoError(t, ReconstructRegistryViewFromAudit(context.Background(), s.DB()))
	require.NotContains(t, buf.String(), "[reconstruct]")
	require.Equal(t, EmptyBytesSHA256Hex, LastRegistryHash())
}

// TestReconstructRegistryViewFromAudit_ReplaysRegisterUnregister —
// simulate another process's audit history; assert the current
// process's view reflects it after reconstruct.
func TestReconstructRegistryViewFromAudit_ReplaysRegisterUnregister(t *testing.T) {
	s, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer s.Close()

	// Seed audit rows as if from a prior process.
	rows := []struct {
		id, ws, name, action string
	}{
		{"r1", "ws-abc12345", "csv_profile", "register"},
		{"r2", "ws-abc12345", "log_parser", "register"},
		{"r3", "ws-abc12345", "csv_profile", "unregister"},
		{"r4", "ws-other0002", "api_wrapper", "register"},
	}
	for i, r := range rows {
		_, err := s.DB().Exec(`INSERT INTO promotion_audit
		    (row_id, ts, workspace_id, mcp_name, action, promoted_by_user_id, driver_thread_id, promotion_reason, candidate_source_task_id)
		    VALUES (?, ?, ?, ?, ?, 'user_abcdef', 'thread_01_a', 'explicit_user_request', 'task_1234567' || ?)`,
			r.id,
			"2026-07-02T12:00:0"+string(rune('0'+i))+".000000000Z",
			r.ws, r.name, r.action, string(rune('0'+i)))
		require.NoError(t, err)
	}

	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	resetRegistryForTest()
	require.NoError(t, ReconstructRegistryViewFromAudit(context.Background(), s.DB()))
	require.Contains(t, buf.String(), "[reconstruct] promotion_audit replayed rows=4")
	require.Contains(t, buf.String(), "workspaces=2")

	// LastRegistryHash is now NOT the empty-bytes constant (view
	// has entries).
	if LastRegistryHash() == EmptyBytesSHA256Hex {
		t.Fatal("expected LastRegistryHash to advance past empty after reconstruct")
	}

	// Verify view contents by snapshot: expect log_parser under
	// ws-abc12345 (register kept, csv_profile register→unregister
	// net-out), api_wrapper under ws-other0002.
	names, _ := snapshotAll()
	joined := strings.Join(names, ",")
	require.Contains(t, joined, "reconstructed:ws-abc12345:log_parser")
	require.NotContains(t, joined, "reconstructed:ws-abc12345:csv_profile", "unregister must have removed the entry")
	require.Contains(t, joined, "reconstructed:ws-other0002:api_wrapper")
}

// TestReconstructRegistryViewFromAudit_IgnoresFailedPipelineStages
// — PR #71 round-3 review P1-β. The B2 pipeline writes one
// promotion_audit row per stage (scaffold / acceptance / register)
// with action='register' throughout. Rows with stage IN
// ('scaffold','acceptance') OR stage_result='fail' represent
// bookkeeping, not real registrations — they must NOT flow into
// the reconstructed view. If they did, D1 field 14 would be
// poisoned post-restart with phantom MCPs for every failed
// pipeline attempt.
func TestReconstructRegistryViewFromAudit_IgnoresFailedPipelineStages(t *testing.T) {
	s, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer s.Close()

	// Seed 3 pipeline attempts that FAILED at various stages. Each
	// writes 1-2 rows depending on how far it got — all with
	// action='register' (the pipeline uses ActionRegister as the
	// base audit template), but stage != '' AND/OR stage_result='fail'.
	seeds := []struct {
		rowID, mcpName, stage, stageResult string
	}{
		// Attempt A: scaffold failed → 1 row.
		{"r_a1", "attempt_a", "scaffold", "fail"},
		// Attempt B: scaffold ok, acceptance failed → 2 rows.
		{"r_b1", "attempt_b", "scaffold", "ok"},
		{"r_b2", "attempt_b", "acceptance", "fail"},
		// Attempt C: scaffold ok, acceptance ok, register failed → 3 rows.
		{"r_c1", "attempt_c", "scaffold", "ok"},
		{"r_c2", "attempt_c", "acceptance", "ok"},
		{"r_c3", "attempt_c", "register", "fail"},
	}
	for i, seed := range seeds {
		_, err := s.DB().Exec(`INSERT INTO promotion_audit
		    (row_id, ts, workspace_id, mcp_name, action, promoted_by_user_id, driver_thread_id, promotion_reason, candidate_source_task_id, stage, stage_result)
		    VALUES (?, ?, 'ws-abc12345', ?, 'register', 'user_abcdef', 'thread_01_a', 'explicit_user_request', 'task_1234567' || ?, ?, ?)`,
			seed.rowID,
			"2026-07-02T12:00:0"+string(rune('0'+i))+".000000000Z",
			seed.mcpName,
			string(rune('0'+i)),
			seed.stage, seed.stageResult)
		require.NoError(t, err)
	}

	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	resetRegistryForTest()
	require.NoError(t, ReconstructRegistryViewFromAudit(context.Background(), s.DB()))

	// Zero rows survive the filter → no reconstruct log line, empty view.
	require.NotContains(t, buf.String(), "[reconstruct]")
	require.Equal(t, EmptyBytesSHA256Hex, LastRegistryHash(),
		"view must stay empty when only failed-pipeline rows exist")

	names, _ := snapshotAll()
	if len(names) != 0 {
		t.Fatalf("expected empty view; got names=%v", names)
	}
}

// TestReconstructRegistryViewFromAudit_CountsOnlyRegisterStageOfSuccessfulPipeline
// — PR #71 round-3 review P1-β complement. A SUCCESSFUL B2 pipeline
// writes 3 rows (scaffold-ok, acceptance-ok, register-ok). Only the
// register-ok row should land in the reconstructed view. The
// scaffold + acceptance rows are pipeline bookkeeping.
func TestReconstructRegistryViewFromAudit_CountsOnlyRegisterStageOfSuccessfulPipeline(t *testing.T) {
	s, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer s.Close()

	// Happy-path pipeline: 3 rows, all action='register',
	// stage_result='ok', stages scaffold / acceptance / register.
	seeds := []struct {
		rowID, stage string
	}{
		{"r_ok_1", "scaffold"},
		{"r_ok_2", "acceptance"},
		{"r_ok_3", "register"},
	}
	for i, seed := range seeds {
		_, err := s.DB().Exec(`INSERT INTO promotion_audit
		    (row_id, ts, workspace_id, mcp_name, action, promoted_by_user_id, driver_thread_id, promotion_reason, candidate_source_task_id, stage, stage_result)
		    VALUES (?, ?, 'ws-abc12345', 'happy_mcp', 'register', 'user_abcdef', 'thread_01_a', 'explicit_user_request', 'task_ok0000' || ?, ?, 'ok')`,
			seed.rowID,
			"2026-07-02T12:00:0"+string(rune('0'+i))+".000000000Z",
			string(rune('0'+i)),
			seed.stage)
		require.NoError(t, err)
	}

	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	resetRegistryForTest()
	require.NoError(t, ReconstructRegistryViewFromAudit(context.Background(), s.DB()))

	// Exactly one row replayed (the register-stage one).
	require.Contains(t, buf.String(), "[reconstruct] promotion_audit replayed rows=1")
	require.Contains(t, buf.String(), "workspaces=1")

	names, _ := snapshotAll()
	if len(names) != 1 {
		t.Fatalf("expected exactly 1 registered entry; got names=%v", names)
	}
	if names[0] != "reconstructed:ws-abc12345:happy_mcp" {
		t.Fatalf("unexpected entry: %q", names[0])
	}
	if LastRegistryHash() == EmptyBytesSHA256Hex {
		t.Fatal("LastRegistryHash must advance past empty after reconstruct")
	}
}
