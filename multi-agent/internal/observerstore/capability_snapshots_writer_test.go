// capability_snapshots_writer_test.go covers spec
// docs/specs/wt1-capability-snapshot.spec.md §5 + §7(c)/(d)/(e).
//
// NOTE: tests in this file that mutate the package global
// capability.DisableUpload MUST NOT call t.Parallel(); they share package
// state. Each such test uses t.Cleanup to restore DisableUpload = false.
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

	var (
		hash, agentID, wsID, createdAt, body string
	)
	row := store.db.QueryRowContext(ctx,
		`SELECT hash, agent_id, workspace_id, created_at, snapshot_json
		   FROM capability_snapshots WHERE hash = ?`, snap.Hash())
	if err := row.Scan(&hash, &agentID, &wsID, &createdAt, &body); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if hash != snap.Hash() {
		t.Errorf("hash = %s; want %s", hash, snap.Hash())
	}
	if agentID != "agent-1" || wsID != "ws-1" {
		t.Errorf("(agent_id, workspace_id) = (%q, %q); want (agent-1, ws-1)", agentID, wsID)
	}
	if createdAt == "" {
		t.Errorf("created_at is empty")
	}
	if body == "" {
		t.Errorf("snapshot_json is empty")
	}
}

// §7(c) — parameterised SQL: an agent_id containing SQL meta-characters
// MUST round-trip verbatim and not corrupt the table.
func TestWriteSnapshot_Parameterized_SQLMetaInAgentID(t *testing.T) {
	t.Parallel()
	store := openInMemoryStore(t)
	snap := validSnap(t)
	ctx := context.Background()
	injection := `a'); DROP TABLE capability_snapshots; --`

	if err := WriteSnapshot(ctx, store.db, injection, "ws-1", snap); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}

	// Table still exists.
	var n int
	if err := store.db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='capability_snapshots'`,
	).Scan(&n); err != nil {
		t.Fatalf("table existence query: %v", err)
	}
	if n != 1 {
		t.Fatalf("table count = %d; want 1 (table was dropped — SQL was concatenated, not parameterised)", n)
	}

	// Row count == 1.
	if err := store.db.QueryRowContext(ctx,
		`SELECT count(*) FROM capability_snapshots`).Scan(&n); err != nil {
		t.Fatalf("row count: %v", err)
	}
	if n != 1 {
		t.Errorf("row count = %d; want 1", n)
	}

	// Retrieved agent_id is the literal injection string.
	var got string
	if err := store.db.QueryRowContext(ctx,
		`SELECT agent_id FROM capability_snapshots`).Scan(&got); err != nil {
		t.Fatalf("agent_id query: %v", err)
	}
	if got != injection {
		t.Errorf("agent_id = %q; want %q (parameter binding lost the literal)", got, injection)
	}
}

func TestWriteSnapshot_IdempotentOnDuplicateHash(t *testing.T) {
	t.Parallel()
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
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("row count after duplicate insert = %d; want 1 (ON CONFLICT DO NOTHING failed)", n)
	}
}

func TestWriteSnapshot_DifferentAgentsSameHashOneRow(t *testing.T) {
	t.Parallel()
	store := openInMemoryStore(t)
	snap := validSnap(t)
	ctx := context.Background()

	if err := WriteSnapshot(ctx, store.db, "agent-A", "ws-1", snap); err != nil {
		t.Fatalf("agent-A: %v", err)
	}
	if err := WriteSnapshot(ctx, store.db, "agent-B", "ws-1", snap); err != nil {
		t.Fatalf("agent-B: %v", err)
	}

	var rows int
	if err := store.db.QueryRowContext(ctx,
		`SELECT count(*) FROM capability_snapshots`).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Errorf("row count = %d; want 1 (hash PRIMARY KEY + ON CONFLICT dedup)", rows)
	}

	// Confirm the (workspace_id, agent_id, created_at) index exists so a
	// runner can still query both agents' usages by hash.
	var idxName string
	if err := store.db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_capability_snapshots_agent'`,
	).Scan(&idxName); err != nil {
		t.Fatalf("index lookup: %v", err)
	}
	if idxName != "idx_capability_snapshots_agent" {
		t.Errorf("index name = %q; want idx_capability_snapshots_agent", idxName)
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
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("row count = %d; want 0 (secret-strip should not have inserted)", n)
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
// a panic from snap.Hash(). Verifies the canonical-first ordering inside
// WriteSnapshot.
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

// §7(d) — DisableUpload=true short-circuits the DB write but returns nil.
func TestNoCapabilityDiscovery_SkipsUploadButReturnsNil(t *testing.T) {
	// Serial: mutates capability.DisableUpload package global.
	prev := capability.DisableUpload
	capability.DisableUpload = true
	t.Cleanup(func() { capability.DisableUpload = prev })

	store := openInMemoryStore(t)
	snap := validSnap(t)
	ctx := context.Background()

	if err := WriteSnapshot(ctx, store.db, "agent-1", "ws-1", snap); err != nil {
		t.Fatalf("WriteSnapshot under DisableUpload=true: %v; want nil", err)
	}

	var n int
	if err := store.db.QueryRowContext(ctx,
		`SELECT count(*) FROM capability_snapshots`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("row count = %d; want 0 (DisableUpload should have skipped insert)", n)
	}
}

// §7(d) — when DisableUpload=true, WriteSnapshot must log one line via
// the standard log package containing "NoCapabilityDiscovery: skipped"
// and the hash so ablation auditors can distinguish intentional skip
// from silent crash.
func TestNoCapabilityDiscovery_LogsSkipLine(t *testing.T) {
	prev := capability.DisableUpload
	capability.DisableUpload = true
	t.Cleanup(func() { capability.DisableUpload = prev })

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
