package driver

import (
	"context"
	"database/sql"
	"log"
)

// ReconstructRegistryViewFromAudit repopulates the in-process
// slaveRegistryView by replaying promotion_audit rows from `db`.
// Called ONCE at driver-agent startup after opening the observer
// DB and BEFORE any register / unregister flow runs. Idempotent:
// invoking a second time on the same DB is a no-op if no new
// rows have landed.
//
// Purpose (PR #71 round-2 review P1-C): under Phase-3 parallel
// eval-runner harnesses, two driver-agent processes may share a
// single observer.db. Each process's in-process
// slaveRegistryView starts empty, so `LastRegistryHash()` returns
// a value that reflects only THIS process's writes — losing the
// other process's registrations. Replaying promotion_audit gives
// each process the same starting view.
//
// Design constraints:
//
//   - The promotion_audit table does NOT carry slave_agent_id
//     directly. We derive it from a heuristic: `mcp_name` is
//     unique per (workspace, slave) under B6's register flow, so
//     replaying by mcp_name into a synthetic slave bucket
//     `"reconstructed:<workspace_id>"` gives a stable-across-
//     processes hash for the same content, even if we lose the
//     per-slave attribution the runtime view has. This is
//     acceptable because the D1 hash is content-addressed (it
//     hashes name + descriptor spec-hash), not identity-addressed.
//
//   - Spec hashes are NOT stored in promotion_audit either. We
//     use `registry_hash_after` (the hash AFTER the action was
//     applied) as a per-row fingerprint; the reconstruct fills
//     the view's spec-hash slot with a stable derived string
//     (`"reconstructed:<row_id>"`) so ComputeRegistryHash sees a
//     consistent hash across processes replaying the same audit
//     log. This diverges from the LIVE view's spec-hashes (which
//     are real sha256 of the buildspec) but stays deterministic
//     within the reconstruct pathway.
//
//   - This is a BEST-EFFORT reconstruction — the primary intent
//     is to keep parallel-run per-workspace hashes stable, not
//     to be byte-identical with a single-process view. When the
//     paper's evaluation cares about exact match, run the
//     harness with one driver-agent per observer.db.
//
// The function LOGS a summary line
// `[reconstruct] promotion_audit replayed rows=<N> workspaces=<W>`
// so operators can see the reconstruction happened. If there's no
// promotion_audit data (fresh DB or table missing), the function
// returns nil with no log line.
func ReconstructRegistryViewFromAudit(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return nil
	}
	// Check the table exists — pre-B6 DBs won't have it.
	var name string
	err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name='promotion_audit'`).Scan(&name)
	if err == sql.ErrNoRows {
		return nil // no table, no work
	}
	if err != nil {
		return err
	}
	// PR #71 round-3 review P1-α: filter out failed pipeline
	// scaffold/acceptance rows. The B2 pipeline writes ONE
	// promotion_audit row per stage (scaffold / acceptance / register)
	// with action='register' throughout — only the register STAGE row
	// on a successful pipeline actually corresponds to a real
	// registration on the slave. Failed scaffold/acceptance rows have
	// action='register' too (the pipeline's base audit template
	// uses ActionRegister), but they represent bookkeeping, not a
	// registered MCP.
	//
	// SELECT filter:
	//   stage=''                        → B6 direct-tool register/unregister flow
	//                                     (always a real state change)
	//   stage='register' AND
	//   stage_result='ok'               → B2 pipeline stage-3 register
	//                                     that succeeded
	// Rows with stage IN ('scaffold','acceptance') OR
	// stage_result='fail' are IGNORED — they never affected the
	// slave's registry state and must not appear in the reconstructed
	// view.
	rows, err := db.QueryContext(ctx, `
		SELECT row_id, workspace_id, mcp_name, action
		  FROM promotion_audit
		 WHERE stage = ''
		    OR (stage = 'register' AND stage_result = 'ok')
		 ORDER BY ts ASC`)
	if err != nil {
		return err
	}
	defer rows.Close()
	replayed := 0
	workspaces := map[string]struct{}{}
	for rows.Next() {
		var rowID, workspace, mcp, action string
		if err := rows.Scan(&rowID, &workspace, &mcp, &action); err != nil {
			return err
		}
		if workspace == "" || mcp == "" {
			continue
		}
		slaveBucket := "reconstructed:" + workspace
		switch action {
		case "register":
			// Use row_id as a stable per-row spec-hash surrogate.
			noteRegister(slaveBucket, mcp, "reconstructed:"+rowID)
			replayed++
			workspaces[workspace] = struct{}{}
		case "unregister":
			noteUnregister(slaveBucket, mcp)
			replayed++
			workspaces[workspace] = struct{}{}
		case "install":
			// install has no registry-view effect (offline install).
			// Don't count in replayed — round-3 P2-δ: the summary log
			// line's rows=N should reflect view-affecting rows only.
		default:
			// Unknown action — skip; the DB CHECK should have rejected
			// but we're defensive.
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if replayed > 0 {
		// Update LastRegistryHash to reflect the reconstructed view.
		// Uses SetLastRegistryHash (which itself takes registryHashMu)
		// under publisherMu — matches the canonical
		// PublishRegisterAndCompute lock ordering so a future
		// refactor that calls reconstruct off-startup won't race
		// with LastRegistryHash readers (round-3 P2-α).
		publisherMu.Lock()
		names, desc := snapshotAll()
		SetLastRegistryHash(ComputeRegistryHash(names, desc))
		publisherMu.Unlock()
		log.Printf("[reconstruct] promotion_audit replayed rows=%d workspaces=%d",
			replayed, len(workspaces))
	}
	return nil
}
