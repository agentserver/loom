package agentbackend

import (
	"encoding/json"
	"strings"
)

// ParentLink is the origin tuple carried by the loom_origin marker. JSON tags
// fix the on-disk field names (agent/name/session) independent of Go field
// order, so callers can unmarshal without depending on marshal ordering.
type ParentLink struct {
	SessionID   string `json:"session"`
	AgentID     string `json:"agent"`
	DisplayName string `json:"name"`
}

type loomOriginMarker struct {
	LoomOrigin ParentLink `json:"loom_origin"`
}

const loomOriginPrefix = `{"loom_origin":`

// BuildLoomOrigin renders a single-line JSON marker carrying the parent link
// through DelegateTaskRequest.SystemContext. Values are JSON-escaped, so any
// display name (incl. ones containing "/>", quotes, or "<") is safe. The
// marker is one line ending with "\n".
func BuildLoomOrigin(agentID, displayName, sessionID string) string {
	b, _ := json.Marshal(loomOriginMarker{LoomOrigin: ParentLink{
		AgentID: agentID, DisplayName: displayName, SessionID: sessionID,
	}})
	return string(b) + "\n"
}

// ParseLoomOrigin extracts the parent link from a SystemContext string and
// returns the context with the marker line removed. ok is false when no
// well-formed marker is present. Robust: uses encoding/json, so values
// containing "/>", quotes, or "<" parse correctly.
func ParseLoomOrigin(systemContext string) (ParentLink, string, bool) {
	var found ParentLink
	markerLine := ""
	rest := make([]string, 0, 4)
	for _, line := range strings.Split(systemContext, "\n") {
		trimmed := strings.TrimSpace(line)
		if markerLine == "" && strings.HasPrefix(trimmed, loomOriginPrefix) {
			var m loomOriginMarker
			if err := json.Unmarshal([]byte(trimmed), &m); err == nil {
				found = m.LoomOrigin
				markerLine = line
				continue // drop this line from cleaned output
			}
		}
		rest = append(rest, line)
	}
	if markerLine == "" {
		return ParentLink{}, systemContext, false
	}
	if found.SessionID == "" && found.AgentID == "" {
		return ParentLink{}, systemContext, false
	}
	cleaned := strings.Join(rest, "\n")
	// Trim a leading blank line left where the marker was, if it was first.
	cleaned = strings.TrimPrefix(cleaned, "\n")
	return found, cleaned, true
}
