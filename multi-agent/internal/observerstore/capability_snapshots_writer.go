package observerstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/yourorg/multi-agent/internal/capability"
)

// ErrSnapshotContainsSecret is returned by WriteSnapshot when the
// canonical JSON of the snapshot contains a substring matching a known
// raw-token regex (see capability/snapshot.go rawTokenPatterns + spec
// §7(e)). Callers should treat this as a programmer error — the
// descriptor source needs to be fixed, not retried.
var ErrSnapshotContainsSecret = errors.New("observerstore: snapshot contains embedded secret")

// nowUTC is overridable in tests for deterministic timestamps. Tests
// that flip it MUST be serial (no t.Parallel) and restore via t.Cleanup
// — the global has no internal lock and concurrent WriteSnapshot calls
// would race.
var nowUTC = func() time.Time { return time.Now().UTC() }

// insertCapabilitySnapshotSQL inserts a (hash, snapshot_json,
// first_seen_at) row into the content-addressed dedup table. Duplicate
// hashes are silently ignored so the JSON blob is stored exactly once
// per unique host state. Compile-time-constant, ? placeholders only.
const insertCapabilitySnapshotSQL = `
INSERT INTO capability_snapshots
  (hash, snapshot_json, first_seen_at)
VALUES (?, ?, ?)
ON CONFLICT(hash) DO NOTHING;
`

// insertCapabilitySnapshotUsageSQL inserts an attribution row so the
// eval runner can trace which (workspace, agent) observed which hash at
// which instant. The PRIMARY KEY (workspace_id, agent_id, hash, used_at)
// makes re-inserts at the exact same instant idempotent while allowing
// the same agent to log multiple observations of the same hash across
// time.
const insertCapabilitySnapshotUsageSQL = `
INSERT INTO capability_snapshot_usages
  (workspace_id, agent_id, hash, used_at)
VALUES (?, ?, ?, ?)
ON CONFLICT DO NOTHING;
`

// WriteSnapshot persists snap to the capability_snapshots dedup table
// AND logs an attribution row in capability_snapshot_usages.
//
// Behaviour (spec §5.2 + §7.2):
//
//   - canonical JSON marshalling fails (only reachable when the caller
//     hand-built a Snapshot with a malformed MCPTools.InputSchema rather
//     than going through NewSnapshot): return the wrapped error before
//     any side-effect.
//   - canonical JSON contains any raw-token regex match: return
//     ErrSnapshotContainsSecret; nothing is written, no skip log is
//     emitted (§7(e) + §7.2). This check runs BEFORE the ablation
//     short-circuit so an embedded raw token surfaces as a hard error
//     regardless of upload state — the two concerns are orthogonal.
//   - capability.IsUploadDisabled() == true: log a skip line via the
//     standard logger and return nil. The local snapshot collection on
//     the slave is unaffected — this only gates upload (§7(d)). We MUST
//     read via IsUploadDisabled(), not the raw DisableUpload variable
//     directly, so the load is race-free against a late CLI flip.
//   - otherwise: open a transaction, insert into capability_snapshots
//     with ON CONFLICT(hash) DO NOTHING (dedup by content) AND insert
//     into capability_snapshot_usages (always — the same agent
//     re-observing the same hash at a different instant is a new row).
//     Commit atomically.
//
// All bound values pass through `?` placeholders; an SQL meta-string
// inside agent_id or workspace_id round-trips verbatim.
func WriteSnapshot(
	ctx context.Context,
	db *sql.DB,
	agentID string,
	workspaceID string,
	snap capability.Snapshot,
) error {
	// Compute canonicalisation FIRST so a hand-built malformed snapshot
	// fails with a returned error rather than panicking inside
	// snap.Hash(). Hash is derived from the same body we ultimately
	// store, guaranteeing snapshot_json always SHA-256-checks back to
	// its hash column even under future changes to ComputeHash.
	body, err := capability.CanonicalJSON(snap)
	if err != nil {
		return fmt.Errorf("observerstore: canonical json: %w", err)
	}
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])

	// Secret-scan BEFORE the ablation short-circuit (spec §7.2).
	if capability.JSONContainsRawToken(body) {
		return ErrSnapshotContainsSecret
	}
	if capability.IsUploadDisabled() {
		log.Printf("[ablation] NoCapabilityDiscovery: skipped snapshot hash=%s", hash)
		return nil
	}

	// Both timestamps captured from a single nowUTC() call so the two
	// rows share the same instant when this is a first observation.
	now := nowUTC().Format(time.RFC3339Nano)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// Rollback is a no-op after successful Commit (per database/sql
	// docs), so this defer is safe regardless of the happy path.
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, insertCapabilitySnapshotSQL,
		hash, string(body), now,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, insertCapabilitySnapshotUsageSQL,
		workspaceID, agentID, hash, now,
	); err != nil {
		return err
	}
	return tx.Commit()
}
