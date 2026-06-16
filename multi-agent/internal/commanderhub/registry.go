// Package commanderhub implements the observer side of commander-web-entry
// (PR-3): the /daemon-link WebSocket hub, the /api/commander reverse proxy,
// and the /commander web page. See
// docs/superpowers/specs/2026-06-15-commander-observer-hub-design.md.
package commanderhub

import (
	"sort"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// owner is the isolation key: daemons under the same (UserID, WorkspaceID)
// are mutually visible; cross-owner is invisible + unreachable (404).
type owner struct {
	userID      string
	workspaceID string
}

// DaemonInfo is the JSON snapshot of an online daemon, returned to the web.
type DaemonInfo struct {
	DaemonID      string   `json:"daemon_id"`
	DisplayName   string   `json:"display_name"`
	Kind          string   `json:"kind"`
	DriverVersion string   `json:"driver_version"`
	Capabilities  []string `json:"capabilities,omitempty"`
	LastSeenAt    string   `json:"last_seen_at,omitempty"`
	SessionCount  int      `json:"session_count,omitempty"`
	ActiveCount   int      `json:"active_count,omitempty"`
	TurnCount     int      `json:"turn_count,omitempty"`
}

// daemonConn is one live daemon WebSocket link. Defined here (not hub.go) so
// the registry can be unit-tested without a real upgrade. Fields used by the
// WS read/write/pending machinery are populated in hub.go / proxy.go.
type daemonConn struct {
	id            string
	owner         owner
	displayName   string
	kind          string
	driverVersion string

	metaMu       sync.Mutex
	capabilities map[string]bool
	lastSeenAt   time.Time

	conn      *websocket.Conn
	writeMu   sync.Mutex // serializes conn.WriteJSON / WriteControl
	pendingMu sync.Mutex // guards pending map
	pending   map[string]*pendingEntry
	done      chan struct{} // closed when the read loop exits
	hub       *Hub
}

func (dc *daemonConn) info() DaemonInfo {
	dc.metaMu.Lock()
	capabilities := make([]string, 0, len(dc.capabilities))
	for capability, enabled := range dc.capabilities {
		if enabled {
			capabilities = append(capabilities, capability)
		}
	}
	sort.Strings(capabilities)
	lastSeenAt := ""
	if !dc.lastSeenAt.IsZero() {
		lastSeenAt = dc.lastSeenAt.UTC().Format(time.RFC3339Nano)
	}
	dc.metaMu.Unlock()

	return DaemonInfo{
		DaemonID:      dc.id,
		DisplayName:   dc.displayName,
		Kind:          dc.kind,
		DriverVersion: dc.driverVersion,
		Capabilities:  capabilities,
		LastSeenAt:    lastSeenAt,
	}
}

// registry maps owner → daemonID → *daemonConn. All methods are goroutine-safe.
type registry struct {
	mu    sync.Mutex
	conns map[owner]map[string]*daemonConn
}

func newRegistry() *registry {
	return &registry{conns: make(map[owner]map[string]*daemonConn)}
}

// add indexes dc by its own owner + id. dc.id and dc.owner must be set.
func (r *registry) add(dc *daemonConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.conns[dc.owner]
	if m == nil {
		m = make(map[string]*daemonConn)
		r.conns[dc.owner] = m
	}
	m[dc.id] = dc
}

func (r *registry) remove(o owner, daemonID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.conns[o]
	if m == nil {
		return
	}
	delete(m, daemonID)
	if len(m) == 0 {
		delete(r.conns, o)
	}
}

func (r *registry) lookup(o owner, daemonID string) (*daemonConn, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	dc := r.conns[o][daemonID]
	return dc, dc != nil
}

func (r *registry) daemons(o owner) []DaemonInfo {
	r.mu.Lock()
	m := r.conns[o]
	conns := make([]*daemonConn, 0, len(m))
	for _, dc := range m {
		conns = append(conns, dc)
	}
	r.mu.Unlock()

	out := make([]DaemonInfo, 0, len(conns))
	for _, dc := range conns {
		out = append(out, dc.info())
	}
	return out
}
