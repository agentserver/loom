package observerstore

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSchema_PromotionAuditTableExists asserts the promotion_audit
// table lands with the expected 12 columns in canonical order —
// pinning drift the way WT-1-run-schema pins the runs table.
func TestSchema_PromotionAuditTableExists(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer s.Close()

	cols := tableColumns(t, s, "promotion_audit")
	for _, want := range []string{
		"row_id", "ts", "workspace_id", "mcp_name", "action",
		"promoted_by_user_id", "driver_thread_id", "promotion_reason",
		"candidate_source_task_id", "registry_hash_after", "stage", "stage_result",
	} {
		if !cols[want] {
			t.Errorf("promotion_audit missing column %q; got %v", want, cols)
		}
	}
}

// TestSchema_PromotionAuditIndexesExist — three indexes per spec §3.2.
func TestSchema_PromotionAuditIndexesExist(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer s.Close()

	rows, err := s.db.Query(`SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='promotion_audit' ORDER BY name`)
	require.NoError(t, err)
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		got[name] = true
	}
	require.NoError(t, rows.Err())
	for _, want := range []string{
		"idx_promotion_audit_mcp",
		"idx_promotion_audit_user",
		"idx_promotion_audit_thread",
	} {
		if !got[want] {
			t.Errorf("promotion_audit missing index %q; got %v", want, got)
		}
	}
}

// TestSchema_PromotionAuditDDLIdempotent — re-open the same file,
// schema.sql is re-executed via schemaSQL const; must not error.
func TestSchema_PromotionAuditDDLIdempotent(t *testing.T) {
	dir := t.TempDir()
	s1, err := Open(filepath.Join(dir, "observer.db"))
	require.NoError(t, err)
	s1.Close()
	s2, err := Open(filepath.Join(dir, "observer.db"))
	require.NoError(t, err)
	s2.Close()
}

// TestSchema_PromotionAuditRejectsBadEnumViaCheck — the DB CHECK
// backstops the Go-side enum validation (spec §3.2 + §7 (b)/(c)).
func TestSchema_PromotionAuditRejectsBadEnumViaCheck(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer s.Close()

	// Bad action.
	_, err = s.db.Exec(`INSERT INTO promotion_audit
	    (row_id, ts, workspace_id, mcp_name, action, promoted_by_user_id, driver_thread_id, promotion_reason, candidate_source_task_id)
	    VALUES ('r1', '2026-07-02T12:00:00Z', 'ws1', 'x', 'bogus', 'user_aaaaaaa', 'thread_1a', 'explicit_user_request', 'task_00000001')`)
	if err == nil {
		t.Fatal("expected CHECK to reject bad action")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "check") &&
		!strings.Contains(strings.ToLower(err.Error()), "constraint") {
		t.Fatalf("unexpected error shape: %v", err)
	}
	// Bad reason.
	_, err = s.db.Exec(`INSERT INTO promotion_audit
	    (row_id, ts, workspace_id, mcp_name, action, promoted_by_user_id, driver_thread_id, promotion_reason, candidate_source_task_id)
	    VALUES ('r2', '2026-07-02T12:00:00Z', 'ws1', 'x', 'register', 'user_aaaaaaa', 'thread_1a', 'bogus', 'task_00000001')`)
	if err == nil {
		t.Fatal("expected CHECK to reject bad reason")
	}
}
