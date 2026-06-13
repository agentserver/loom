package observerstore

import "time"

// SetAgentLastSeenAtForTest forces the agents.last_seen_at column for the
// given (workspace, agent) to t. Intended for tests that need to verify the
// register duplicate-takeover guard's reaction to a specific last_seen_at —
// in particular the ingest-extends-guard regression test in observerweb.
// Returns no error if the agent row doesn't exist.
func (s *SQLiteStore) SetAgentLastSeenAtForTest(workspaceID, agentID string, t time.Time) error {
	_, err := s.db.Exec(`UPDATE agents SET last_seen_at=? WHERE workspace_id=? AND id=?`,
		t.UTC().Format(time.RFC3339Nano), workspaceID, agentID)
	return err
}
