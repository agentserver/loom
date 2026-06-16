package commanderhub

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

type CommanderTree struct {
	Daemons []DaemonTree `json:"daemons"`
}

type DaemonTree struct {
	DaemonInfo
	Status   string       `json:"status"`
	Error    string       `json:"error,omitempty"`
	Sessions []SessionRow `json:"sessions,omitempty"`
}

type SessionRow struct {
	DaemonID         string    `json:"daemon_id"`
	SessionID        string    `json:"session_id"`
	Kind             string    `json:"kind"`
	Title            string    `json:"title"`
	WorkingDir       string    `json:"working_dir,omitempty"`
	UpdatedAt        time.Time `json:"updated_at,omitempty"`
	MessageCount     int       `json:"message_count,omitempty"`
	Preview          string    `json:"preview,omitempty"`
	TurnState        string    `json:"turn_state"`
	ActiveWorker     bool      `json:"active_worker"`
	AwaitingApproval bool      `json:"awaiting_approval"`
}

type sessionListCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[cacheKey]sessionCacheEntry
}

type cacheKey struct {
	owner    owner
	daemonID string
}

type sessionCacheEntry struct {
	expires time.Time
	rows    []SessionRow
}

func newSessionListCache(ttl time.Duration) *sessionListCache {
	return &sessionListCache{ttl: ttl, entries: make(map[cacheKey]sessionCacheEntry)}
}

func sessionTitle(title, preview, id string) string {
	for _, s := range []string{title, preview} {
		s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
		if s != "" {
			return s
		}
	}
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func sessionRowFromBackend(daemonID string, sess agentbackend.Session, snap turnSnapshot) SessionRow {
	state := string(snap.State)
	if state == "" {
		state = string(turnStateIdle)
	}
	return SessionRow{
		DaemonID:         daemonID,
		SessionID:        sess.ID,
		Kind:             string(sess.Kind),
		Title:            sessionTitle(sess.Title, sess.Preview, sess.ID),
		WorkingDir:       sess.WorkingDir,
		UpdatedAt:        sess.UpdatedAt,
		MessageCount:     sess.MessageCount,
		Preview:          sess.Preview,
		TurnState:        state,
		ActiveWorker:     snap.ActiveWorker,
		AwaitingApproval: snap.AwaitingApproval,
	}
}

func sortSessionRows(rows []SessionRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].UpdatedAt.After(rows[j].UpdatedAt)
	})
}

func (h *Hub) CommanderTree(ctx context.Context, o owner) CommanderTree {
	infos := h.reg.daemons(o)
	out := CommanderTree{Daemons: make([]DaemonTree, 0, len(infos))}
	for _, info := range infos {
		row := DaemonTree{DaemonInfo: info, Status: "ok"}
		sessions, err := h.cachedSessionRows(ctx, o, info)
		if err != nil {
			row.Status = "error"
			row.Error = err.Error()
		} else {
			row.Sessions = sessions
			row.SessionCount = len(sessions)
			for _, session := range sessions {
				if session.ActiveWorker {
					row.ActiveCount++
				}
				if session.TurnState == string(turnStateQueued) ||
					session.TurnState == string(turnStateStarting) ||
					session.TurnState == string(turnStateAnswering) {
					row.TurnCount++
				}
			}
		}
		out.Daemons = append(out.Daemons, row)
	}
	return out
}

func (h *Hub) cachedSessionRows(ctx context.Context, o owner, info DaemonInfo) ([]SessionRow, error) {
	key := cacheKey{owner: o, daemonID: info.DaemonID}
	now := time.Now()
	h.sessionCache.mu.Lock()
	if ent, ok := h.sessionCache.entries[key]; ok && now.Before(ent.expires) {
		rows := append([]SessionRow(nil), ent.rows...)
		h.sessionCache.mu.Unlock()
		h.mergeCurrentTurnState(o, info.DaemonID, rows)
		return rows, nil
	}
	h.sessionCache.mu.Unlock()

	rows, err := h.refreshSessionRows(ctx, o, info)
	if err != nil {
		return nil, err
	}
	h.sessionCache.mu.Lock()
	h.sessionCache.entries[key] = sessionCacheEntry{
		expires: time.Now().Add(h.sessionCache.ttl),
		rows:    append([]SessionRow(nil), rows...),
	}
	h.sessionCache.mu.Unlock()
	return append([]SessionRow(nil), rows...), nil
}

func (h *Hub) invalidateDaemonSessions(o owner, daemonID string) {
	h.sessionCache.mu.Lock()
	delete(h.sessionCache.entries, cacheKey{owner: o, daemonID: daemonID})
	h.sessionCache.mu.Unlock()
}

func (h *Hub) refreshSessionRows(ctx context.Context, o owner, info DaemonInfo) ([]SessionRow, error) {
	payload, err := h.SendCommand(ctx, o, info.DaemonID, "list_sessions", nil)
	if err != nil {
		return nil, err
	}
	var body struct {
		Sessions []agentbackend.Session `json:"sessions"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		return nil, err
	}
	rows := make([]SessionRow, 0, len(body.Sessions))
	for _, sess := range body.Sessions {
		snap := h.turns.get(turnKey{owner: o, daemonID: info.DaemonID, sessionID: sess.ID})
		rows = append(rows, sessionRowFromBackend(info.DaemonID, sess, snap))
	}
	sortSessionRows(rows)
	return rows, nil
}

func (h *Hub) mergeCurrentTurnState(o owner, daemonID string, rows []SessionRow) {
	for i := range rows {
		snap := h.turns.get(turnKey{owner: o, daemonID: daemonID, sessionID: rows[i].SessionID})
		state := string(snap.State)
		if state == "" {
			state = string(turnStateIdle)
		}
		rows[i].TurnState = state
		rows[i].ActiveWorker = snap.ActiveWorker
		rows[i].AwaitingApproval = snap.AwaitingApproval
	}
}
