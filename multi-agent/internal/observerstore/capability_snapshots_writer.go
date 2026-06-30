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

// nowUTC is overridable in tests for a deterministic created_at value.
var nowUTC = func() time.Time { return time.Now().UTC() }

// insertCapabilitySnapshotSQL is a compile-time-constant statement using
// `?` placeholders only. There is no fmt.Sprintf, no string
// concatenation, no dynamic identifier substitution — see spec §7(c).
const insertCapabilitySnapshotSQL = `
INSERT INTO capability_snapshots
  (hash, agent_id, workspace_id, created_at, snapshot_json)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(hash) DO NOTHING;
`

// WriteSnapshot persists snap to the capability_snapshots table.
//
// Behaviour (spec §5.2):
//
//   - canonical JSON marshalling fails (only reachable when the caller
//     hand-built a Snapshot with a malformed MCPTools.InputSchema rather
//     than going through NewSnapshot): return the wrapped error before
//     any side-effect.
//   - capability.DisableUpload == true: log a skip line via the standard
//     logger and return nil. The local snapshot collection on the slave
//     is unaffected — this only gates upload (§7(d)).
//   - canonical JSON contains any raw-token regex match: return
//     ErrSnapshotContainsSecret; nothing is written (§7(e)).
//   - otherwise: ExecContext insert with `ON CONFLICT(hash) DO NOTHING`,
//     making duplicate inserts idempotent.
//
// All five values bound to the statement (hash, agent_id, workspace_id,
// created_at, snapshot_json) are passed as parameters; an SQL meta-string
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
	// snap.Hash() (which also calls CanonicalJSON). The hash is derived
	// from the same body we ultimately store, guaranteeing the row's
	// snapshot_json column always SHA-256-checks back to its `hash`
	// column even under future changes to ComputeHash.
	body, err := capability.CanonicalJSON(snap)
	if err != nil {
		return fmt.Errorf("observerstore: canonical json: %w", err)
	}
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])

	if capability.DisableUpload {
		log.Printf("[ablation] NoCapabilityDiscovery: skipped snapshot hash=%s", hash)
		return nil
	}
	if capability.JSONContainsRawToken(body) {
		return ErrSnapshotContainsSecret
	}
	if _, err := db.ExecContext(ctx, insertCapabilitySnapshotSQL,
		hash, agentID, workspaceID, nowUTC().Format(time.RFC3339Nano), string(body),
	); err != nil {
		return err
	}
	return nil
}
