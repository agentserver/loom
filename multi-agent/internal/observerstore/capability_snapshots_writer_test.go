// capability_snapshots_writer_test.go covers spec
// docs/specs/wt1-capability-snapshot.spec.md §5 + §7(c)/(d)/(e).
//
// NOTE: tests in this file that mutate the package global
// capability.DisableUpload (via capability.SetDisableUpload) MUST NOT
// call t.Parallel(); they share package state. Each such test uses
// t.Cleanup to restore SetDisableUpload(false).
package observerstore

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yourorg/multi-agent/internal/capability"
	"github.com/yourorg/multi-agent/internal/commandiface"
)

// validSnap returns a valid Snapshot for writer tests.
func validSnap(t *testing.T) capability.Snapshot {
	t.Helper()
	s, err := capability.NewSnapshot(capability.Snapshot{
		OS:       "linux",
		Arch:     "amd64",
		Platform: commandiface.Platform{OS: "linux", Arch: "amd64"},
		Network:  capability.NetworkInternet,
		Tools: []capability.ToolVersion{
			{Name: "go", Version: "1.22.0"},
		},
	})
	if err != nil {
		t.Fatalf("NewSnapshot: %v", err)
	}
	return s
}

// openInMemoryStore opens a fresh on-disk SQLite store under t.TempDir
// so each test gets its own observer DB. We avoid `file::memory:` because
// the `modernc.org/sqlite` driver's shared-cache memory DBs interact
// poorly with the WAL pragma; t.TempDir is cleaned up automatically.
func openInMemoryStore(t *testing.T) *SQLiteStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "observer.db")
	store, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() {
		_ = store.db.Close()
		_ = os.Remove(path)
	})
	return store
}

func TestWriteSnapshot_HappyPath(t *testing.T) {
	t.Parallel()
	store := openInMemoryStore(t)
	snap := validSnap(t)
	ctx := context.Background()

	if err := WriteSnapshot(ctx, store.db, "agent-1", "ws-1", snap); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}

	// capability_snapshots: one row keyed by hash with the JSON blob.
	var hash, body, firstSeenAt string
	row := store.db.QueryRowContext(ctx,
		`SELECT hash, snapshot_json, first_seen_at
		   FROM capability_snapshots WHERE hash = ?`, snap.Hash())
	if err := row.Scan(&hash, &body, &firstSeenAt); err != nil {
		t.Fatalf("capability_snapshots scan: %v", err)
	}
	if hash != snap.Hash() {
		t.Errorf("hash = %s; want %s", hash, snap.Hash())
	}
	if body == "" {
		t.Errorf("snapshot_json is empty")
	}
	if firstSeenAt == "" {
		t.Errorf("first_seen_at is empty")
	}

	// capability_snapshot_usages: one row per observation.
	var wsID, agentID, uHash, usedAt string
	row = store.db.QueryRowContext(ctx,
		`SELECT workspace_id, agent_id, hash, used_at
		   FROM capability_snapshot_usages WHERE hash = ?`, snap.Hash())
	if err := row.Scan(&wsID, &agentID, &uHash, &usedAt); err != nil {
		t.Fatalf("capability_snapshot_usages scan: %v", err)
	}
	if agentID != "agent-1" || wsID != "ws-1" {
		t.Errorf("(agent_id, workspace_id) = (%q, %q); want (agent-1, ws-1)", agentID, wsID)
	}
	if usedAt == "" {
		t.Errorf("used_at is empty")
	}
}

// §7(c) — parameterised SQL: an agent_id containing SQL meta-characters
// MUST round-trip verbatim and not corrupt any table. The injection
// payload targets both possible tables now, since the writer touches
// two of them per call.
func TestWriteSnapshot_Parameterized_SQLMetaInAgentID(t *testing.T) {
	t.Parallel()
	store := openInMemoryStore(t)
	snap := validSnap(t)
	ctx := context.Background()
	injection := `a'); DROP TABLE capability_snapshots; DROP TABLE capability_snapshot_usages; --`

	if err := WriteSnapshot(ctx, store.db, injection, "ws-1", snap); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}

	// Both tables still exist.
	var n int
	if err := store.db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('capability_snapshots','capability_snapshot_usages')`,
	).Scan(&n); err != nil {
		t.Fatalf("table existence query: %v", err)
	}
	if n != 2 {
		t.Fatalf("table count = %d; want 2 (a table was dropped — SQL was concatenated, not parameterised)", n)
	}

	// Row count == 1 in each table.
	if err := store.db.QueryRowContext(ctx,
		`SELECT count(*) FROM capability_snapshots`).Scan(&n); err != nil {
		t.Fatalf("dedup count: %v", err)
	}
	if n != 1 {
		t.Errorf("capability_snapshots row count = %d; want 1", n)
	}
	if err := store.db.QueryRowContext(ctx,
		`SELECT count(*) FROM capability_snapshot_usages`).Scan(&n); err != nil {
		t.Fatalf("usages count: %v", err)
	}
	if n != 1 {
		t.Errorf("capability_snapshot_usages row count = %d; want 1", n)
	}

	// Retrieved agent_id is the literal injection string.
	var got string
	if err := store.db.QueryRowContext(ctx,
		`SELECT agent_id FROM capability_snapshot_usages`).Scan(&got); err != nil {
		t.Fatalf("agent_id query: %v", err)
	}
	if got != injection {
		t.Errorf("agent_id = %q; want %q (parameter binding lost the literal)", got, injection)
	}
}

// Same snap re-inserted by the same agent at the same instant is a
// no-op (dedup row unchanged; usage row deduplicated by composite PK).
func TestWriteSnapshot_IdempotentOnDuplicateHash(t *testing.T) {
	// Fix the clock so the two calls produce identical used_at values,
	// exercising the usages-table PRIMARY KEY dedup path. Serial: mutates
	// nowUTC package global.
	prev := nowUTC
	fixed := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	nowUTC = func() time.Time { return fixed }
	t.Cleanup(func() { nowUTC = prev })

	store := openInMemoryStore(t)
	snap := validSnap(t)
	ctx := context.Background()

	if err := WriteSnapshot(ctx, store.db, "agent-1", "ws-1", snap); err != nil {
		t.Fatalf("first WriteSnapshot: %v", err)
	}
	if err := WriteSnapshot(ctx, store.db, "agent-1", "ws-1", snap); err != nil {
		t.Fatalf("second WriteSnapshot: %v", err)
	}

	var n int
	if err := store.db.QueryRowContext(ctx,
		`SELECT count(*) FROM capability_snapshots WHERE hash = ?`, snap.Hash()).Scan(&n); err != nil {
		t.Fatalf("dedup count: %v", err)
	}
	if n != 1 {
		t.Errorf("capability_snapshots row count after duplicate insert = %d; want 1", n)
	}
	if err := store.db.QueryRowContext(ctx,
		`SELECT count(*) FROM capability_snapshot_usages WHERE hash = ?`, snap.Hash()).Scan(&n); err != nil {
		t.Fatalf("usages count: %v", err)
	}
	if n != 1 {
		t.Errorf("capability_snapshot_usages row count after same-instant duplicate = %d; want 1", n)
	}
}

// Two agents observing the same snapshot share ONE row in the dedup
// table (that is the whole point of dedup) but must each land their OWN
// row in the attribution table so a runner can retrieve per-agent
// usage. Round-5 audit finding: the prior implementation silently
// dropped agent-B's attribution.
func TestWriteSnapshot_DifferentAgentsShareDedupButKeepUsages(t *testing.T) {
	// Fix the clock so timestamps are deterministic per (agent_id) —
	// but we advance between agents so used_at is distinct where the
	// PRIMARY KEY would otherwise collide. Serial: mutates nowUTC.
	prev := nowUTC
	t0 := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	step := 0
	nowUTC = func() time.Time { defer func() { step++ }(); return t0.Add(time.Duration(step) * time.Second) }
	t.Cleanup(func() { nowUTC = prev })

	store := openInMemoryStore(t)
	snap := validSnap(t)
	ctx := context.Background()

	if err := WriteSnapshot(ctx, store.db, "agent-A", "ws-1", snap); err != nil {
		t.Fatalf("agent-A: %v", err)
	}
	if err := WriteSnapshot(ctx, store.db, "agent-B", "ws-1", snap); err != nil {
		t.Fatalf("agent-B: %v", err)
	}

	// Dedup: one row for the hash.
	var n int
	if err := store.db.QueryRowContext(ctx,
		`SELECT count(*) FROM capability_snapshots`).Scan(&n); err != nil {
		t.Fatalf("dedup count: %v", err)
	}
	if n != 1 {
		t.Errorf("capability_snapshots row count = %d; want 1 (dedup by hash)", n)
	}

	// Attribution: BOTH agents' usages recoverable via the index-backed
	// query. Round-5 audit contract.
	var aCount, bCount int
	if err := store.db.QueryRowContext(ctx,
		`SELECT count(*) FROM capability_snapshot_usages WHERE workspace_id = ? AND agent_id = ?`,
		"ws-1", "agent-A").Scan(&aCount); err != nil {
		t.Fatalf("agent-A usage count: %v", err)
	}
	if err := store.db.QueryRowContext(ctx,
		`SELECT count(*) FROM capability_snapshot_usages WHERE workspace_id = ? AND agent_id = ?`,
		"ws-1", "agent-B").Scan(&bCount); err != nil {
		t.Fatalf("agent-B usage count: %v", err)
	}
	if aCount != 1 || bCount != 1 {
		t.Errorf("per-agent usage counts = (A=%d, B=%d); want (1, 1) — attribution lost", aCount, bCount)
	}

	// Confirm the (workspace_id, agent_id, used_at) index exists so a
	// runner can efficiently query per-agent usages.
	var idxName string
	if err := store.db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_capability_snapshot_usages_agent'`,
	).Scan(&idxName); err != nil {
		t.Fatalf("index lookup: %v", err)
	}
	if idxName != "idx_capability_snapshot_usages_agent" {
		t.Errorf("index name = %q; want idx_capability_snapshot_usages_agent", idxName)
	}
}

// §7(e) — embedded secret in MCP descriptor description is rejected.
func TestWriteSnapshot_RejectsEmbeddedSecretInMCPDescription(t *testing.T) {
	t.Parallel()
	store := openInMemoryStore(t)
	snap, err := capability.NewSnapshot(capability.Snapshot{
		OS:       "linux",
		Arch:     "amd64",
		Platform: commandiface.Platform{OS: "linux", Arch: "amd64"},
		Network:  capability.NetworkInternet,
		MCPTools: []capability.MCPToolDescriptor{{
			Server:      "srv",
			Name:        "tool",
			Description: "use this with sk-abc123def4567890xyz to authenticate",
		}},
	})
	if err != nil {
		t.Fatalf("NewSnapshot: %v", err)
	}
	ctx := context.Background()

	err = WriteSnapshot(ctx, store.db, "agent-1", "ws-1", snap)
	if !errors.Is(err, ErrSnapshotContainsSecret) {
		t.Fatalf("WriteSnapshot: want ErrSnapshotContainsSecret, got %v", err)
	}
	var n int
	if err := store.db.QueryRowContext(ctx,
		`SELECT count(*) FROM capability_snapshots`).Scan(&n); err != nil {
		t.Fatalf("dedup count: %v", err)
	}
	if n != 0 {
		t.Errorf("capability_snapshots row count = %d; want 0 (secret-strip should not have inserted)", n)
	}
	if err := store.db.QueryRowContext(ctx,
		`SELECT count(*) FROM capability_snapshot_usages`).Scan(&n); err != nil {
		t.Fatalf("usages count: %v", err)
	}
	if n != 0 {
		t.Errorf("capability_snapshot_usages row count = %d; want 0 (transaction should have rolled back)", n)
	}
}

// §7(e) — embedded secret in a tool name is rejected. We bypass
// NewSnapshot here because the snapshot factory has no per-field
// raw-token check on tool names (the leak path is at observerstore
// pre-write, not at construction). The point of this test is that
// WriteSnapshot catches whatever the canonical JSON leaks.
func TestWriteSnapshot_RejectsEmbeddedSecretInToolName(t *testing.T) {
	t.Parallel()
	store := openInMemoryStore(t)
	snap := validSnap(t)
	snap.Tools = append(snap.Tools, capability.ToolVersion{
		Name:    "ghp_ABCDEFGHIJKLMNOPQRSTUV",
		Version: "1.0",
	})
	ctx := context.Background()

	err := WriteSnapshot(ctx, store.db, "agent-1", "ws-1", snap)
	if !errors.Is(err, ErrSnapshotContainsSecret) {
		t.Fatalf("WriteSnapshot: want ErrSnapshotContainsSecret, got %v", err)
	}
}

// A hand-built Snapshot with malformed MCPTools.InputSchema (bypassing
// NewSnapshot) must surface as a returned error from WriteSnapshot, NOT
// a panic from snap.Hash(). Verifies the canonical-first ordering
// inside WriteSnapshot.
func TestWriteSnapshot_ReturnsErrorOnMalformedSnapshot(t *testing.T) {
	t.Parallel()
	store := openInMemoryStore(t)
	bad := capability.Snapshot{
		OS:       "linux",
		Arch:     "amd64",
		Platform: commandiface.Platform{OS: "linux", Arch: "amd64"},
		Network:  capability.NetworkInternet,
		MCPTools: []capability.MCPToolDescriptor{{
			Server:      "srv",
			Name:        "tool",
			InputSchema: []byte("{not json"), // malformed; would panic in snap.Hash()
		}},
	}
	ctx := context.Background()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("WriteSnapshot panicked instead of returning error: %v", r)
		}
	}()
	err := WriteSnapshot(ctx, store.db, "agent-1", "ws-1", bad)
	if err == nil {
		t.Fatalf("WriteSnapshot(malformed snap): want error, got nil")
	}
}

// §7.2 — secret-scan runs BEFORE the ablation short-circuit so a
// leaky descriptor still surfaces as ErrSnapshotContainsSecret even
// when uploads are ablated off. (Reviewer round 4 P2.)
func TestNoCapabilityDiscovery_SecretScanRunsBeforeAblationSkip(t *testing.T) {
	capability.SetDisableUpload(true)
	t.Cleanup(func() { capability.SetDisableUpload(false) })

	store := openInMemoryStore(t)
	snap, err := capability.NewSnapshot(capability.Snapshot{
		OS:       "linux",
		Arch:     "amd64",
		Platform: commandiface.Platform{OS: "linux", Arch: "amd64"},
		Network:  capability.NetworkInternet,
		MCPTools: []capability.MCPToolDescriptor{{
			Server:      "srv",
			Name:        "tool",
			Description: "use sk-abc123def4567890xyz to authenticate",
		}},
	})
	if err != nil {
		t.Fatalf("NewSnapshot: %v", err)
	}
	ctx := context.Background()

	err = WriteSnapshot(ctx, store.db, "agent-1", "ws-1", snap)
	if !errors.Is(err, ErrSnapshotContainsSecret) {
		t.Fatalf("WriteSnapshot(ablation=on, embedded secret): want ErrSnapshotContainsSecret, got %v", err)
	}
}

// Spec §5.2 contract: tests substitute a deterministic value through
// nowUTC and assert it round-trips into first_seen_at (dedup) and
// used_at (usages) columns. Serial: mutates nowUTC.
func TestWriteSnapshot_PersistedCreatedAt(t *testing.T) {
	prev := nowUTC
	fixed := time.Date(2025, 1, 2, 3, 4, 5, 678901234, time.UTC)
	nowUTC = func() time.Time { return fixed }
	t.Cleanup(func() { nowUTC = prev })

	store := openInMemoryStore(t)
	snap := validSnap(t)
	ctx := context.Background()

	if err := WriteSnapshot(ctx, store.db, "agent-1", "ws-1", snap); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}

	want := fixed.Format(time.RFC3339Nano)

	var firstSeen string
	if err := store.db.QueryRowContext(ctx,
		`SELECT first_seen_at FROM capability_snapshots WHERE hash = ?`, snap.Hash()).Scan(&firstSeen); err != nil {
		t.Fatalf("query first_seen_at: %v", err)
	}
	if firstSeen != want {
		t.Errorf("first_seen_at = %q; want %q", firstSeen, want)
	}

	var usedAt string
	if err := store.db.QueryRowContext(ctx,
		`SELECT used_at FROM capability_snapshot_usages WHERE hash = ?`, snap.Hash()).Scan(&usedAt); err != nil {
		t.Fatalf("query used_at: %v", err)
	}
	if usedAt != want {
		t.Errorf("used_at = %q; want %q", usedAt, want)
	}
}

// §7(d) — IsUploadDisabled()==true short-circuits the DB write but
// returns nil. Neither table is touched.
func TestNoCapabilityDiscovery_SkipsUploadButReturnsNil(t *testing.T) {
	capability.SetDisableUpload(true)
	t.Cleanup(func() { capability.SetDisableUpload(false) })

	store := openInMemoryStore(t)
	snap := validSnap(t)
	ctx := context.Background()

	if err := WriteSnapshot(ctx, store.db, "agent-1", "ws-1", snap); err != nil {
		t.Fatalf("WriteSnapshot under DisableUpload=true: %v; want nil", err)
	}

	var n int
	if err := store.db.QueryRowContext(ctx,
		`SELECT count(*) FROM capability_snapshots`).Scan(&n); err != nil {
		t.Fatalf("dedup count: %v", err)
	}
	if n != 0 {
		t.Errorf("capability_snapshots row count = %d; want 0", n)
	}
	if err := store.db.QueryRowContext(ctx,
		`SELECT count(*) FROM capability_snapshot_usages`).Scan(&n); err != nil {
		t.Fatalf("usages count: %v", err)
	}
	if n != 0 {
		t.Errorf("capability_snapshot_usages row count = %d; want 0", n)
	}
}

// §7(d) — when DisableUpload=true, WriteSnapshot must log one line via
// the standard log package containing "NoCapabilityDiscovery: skipped"
// and the hash so ablation auditors can distinguish intentional skip
// from silent crash.
func TestNoCapabilityDiscovery_LogsSkipLine(t *testing.T) {
	capability.SetDisableUpload(true)
	t.Cleanup(func() { capability.SetDisableUpload(false) })

	// Capture the standard logger.
	var buf bytes.Buffer
	prevOut := log.Default().Writer()
	prevFlags := log.Default().Flags()
	log.Default().SetOutput(&buf)
	log.Default().SetFlags(0)
	t.Cleanup(func() {
		log.Default().SetOutput(prevOut)
		log.Default().SetFlags(prevFlags)
	})

	store := openInMemoryStore(t)
	snap := validSnap(t)
	ctx := context.Background()

	if err := WriteSnapshot(ctx, store.db, "agent-1", "ws-1", snap); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "NoCapabilityDiscovery: skipped") {
		t.Errorf("log output %q does not contain 'NoCapabilityDiscovery: skipped'", out)
	}
	if !strings.Contains(out, snap.Hash()) {
		t.Errorf("log output %q does not contain the snapshot hash %q", out, snap.Hash())
	}
}
