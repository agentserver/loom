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
