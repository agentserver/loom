package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	loomMetaSchema = 1
	loomMetaMaxAge = 30 * 24 * time.Hour
)

func timeNow() time.Time { return time.Now() }

type loomMeta struct {
	Schema            int    `json:"schema"`
	SessionID         string `json:"session_id"`
	ParentSessionID   string `json:"parent_session_id"`
	ParentAgentID     string `json:"parent_agent_id"`
	ParentDisplayName string `json:"parent_display_name"`
	Origin            string `json:"origin"`
	Kind              string `json:"kind"`
	CreatedAt         string `json:"created_at"`
}

func loomMetaDir(base string) string {
	return filepath.Join(base, "loom-meta")
}

func loomMetaPath(base, threadID string) string {
	return filepath.Join(loomMetaDir(base), threadID+".json")
}

func writeLoomMeta(base string, m loomMeta) error {
	if base == "" {
		return nil
	}
	if m.Schema != loomMetaSchema || m.Kind != "codex" || m.Origin != "agent_task" || m.SessionID == "" {
		return nil
	}
	if err := os.MkdirAll(loomMetaDir(base), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(loomMetaPath(base, m.SessionID), data, 0o600)
}

func readLoomMeta(base, threadID string) (loomMeta, bool) {
	var m loomMeta
	if base == "" || threadID == "" {
		return m, false
	}
	data, err := os.ReadFile(loomMetaPath(base, threadID))
	if err != nil {
		return m, false
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, false
	}
	return m, true
}

func reaper(base string, liveThreadIDs []string) {
	if base == "" {
		return
	}
	entries, err := os.ReadDir(loomMetaDir(base))
	if err != nil {
		return
	}
	live := make(map[string]struct{}, len(liveThreadIDs))
	for _, id := range liveThreadIDs {
		live[id] = struct{}{}
	}
	cutoff := timeNow().Add(-loomMetaMaxAge)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		id, ok := threadIDFromLoomMetaName(entry.Name())
		if !ok {
			continue
		}
		path := filepath.Join(loomMetaDir(base), entry.Name())
		info, err := entry.Info()
		if err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(path)
			continue
		}
		if _, ok := live[id]; !ok {
			_ = os.Remove(path)
		}
	}
}

func threadIDFromLoomMetaName(name string) (string, bool) {
	const suffix = ".json"
	id, ok := strings.CutSuffix(name, suffix)
	if !ok || id == "" {
		return "", false
	}
	return id, true
}

// writeCurrentSession writes a transient pointer to the currently-active
// codex thread id, read by serve-mcp to learn the parent session when codex
// calls submit_task (codex and serve-mcp are separate processes sharing
// CODEX_HOME). Written on every thread.started (Run + RunResume). NOT a
// sidecar; does not participate in parent-link merge or reaping. best-effort.
func writeCurrentSession(base, threadID string) error {
	if base == "" || threadID == "" {
		return nil
	}
	if err := os.MkdirAll(loomMetaDir(base), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(loomMetaDir(base), "current"), []byte(threadID), 0o600)
}

// ReadCurrentSession returns the thread id last written by writeCurrentSession,
// or "" if absent/unreadable. Exported so internal/driver (serve-mcp) can read
// the parent session marker; it cannot call codex's unexported helpers.
func ReadCurrentSession(base string) string {
	if base == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(loomMetaDir(base), "current"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
