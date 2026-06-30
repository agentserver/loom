// Package commanderhub implements the observer side of commander-web-entry
// (PR-3): the /daemon-link WebSocket hub, the /api/commander reverse proxy,
// and the /commander web page. See
// docs/superpowers/specs/2026-06-15-commander-observer-hub-design.md.
package commanderhub

import (
	"sort"
	"sync"
	"sync/atomic"
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
	ShortID       string   `json:"short_id,omitempty"`
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
	shortID       string
	displayName   string
	kind          string
	driverVersion string

	// ownershipLost is set to true when the shared Postgres registry records a
	// different owning_instance_url for this daemon's shortID (i.e., a faster
	// pod won the registration race). The heartbeat loop checks this flag and
	// terminates the connection so the winning pod takes over cleanly.
	ownershipLost atomic.Bool

	// heartbeatErrCount counts consecutive heartbeat write failures. The
	// heartbeat loop terminates the connection after a threshold is reached.
	heartbeatErrCount atomic.Int32

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

// routingID returns the stable identity used as the registry key and in
// DaemonInfo.DaemonID. When the daemon registered with a non-empty ShortID
// (multi-pod shared-registry mode), that ShortID is used so reconnects from
// the same physical daemon keep the same key. For legacy single-pod daemons
// that register with an empty ShortID, it falls back to the ephemeral dc.id
// — preserving existing behavior bit-exactly.
func (dc *daemonConn) routingID() string {
	if dc.shortID != "" {
		return dc.shortID
	}
	return dc.id
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
		DaemonID:      dc.routingID(),
		ShortID:       dc.shortID,
		DisplayName:   dc.displayName,
		Kind:          dc.kind,
		DriverVersion: dc.driverVersion,
		Capabilities:  capabilities,
		LastSeenAt:    lastSeenAt,
	}
}

// localRegistry maps owner → routingID → *daemonConn. All methods are
// goroutine-safe. Keys are routingID values (dc.routingID()), which equal
// dc.shortID when set and dc.id otherwise (legacy fallback).
type localRegistry struct {
	mu    sync.Mutex
	conns map[owner]map[string]*daemonConn
}

func newLocalRegistry() *localRegistry {
	return &localRegistry{conns: make(map[owner]map[string]*daemonConn)}
}

// add indexes dc by its owner + routingID(). dc.id, dc.shortID, and dc.owner
// must be set before calling add.
func (r *localRegistry) add(dc *daemonConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.conns[dc.owner]
	if m == nil {
		m = make(map[string]*daemonConn)
		r.conns[dc.owner] = m
	}
	m[dc.routingID()] = dc
}

func (r *localRegistry) remove(o owner, routingID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.conns[o]
	if m == nil {
		return
	}
	delete(m, routingID)
	if len(m) == 0 {
		delete(r.conns, o)
	}
}

// removeIf removes the entry at (o, routingID) only when pred(existing)
// returns true. This prevents a reconnecting daemon from evicting its
// successor: the deferred teardown passes a predicate that matches the
// specific *daemonConn it owns, so a new conn that already wrote to the same
// slot is left intact.
func (r *localRegistry) removeIf(o owner, routingID string, pred func(*daemonConn) bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.conns[o]
	if m == nil {
		return
	}
	existing, ok := m[routingID]
	if !ok || !pred(existing) {
		return
	}
	delete(m, routingID)
	if len(m) == 0 {
		delete(r.conns, o)
	}
}

func (r *localRegistry) lookup(o owner, routingID string) (*daemonConn, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	dc := r.conns[o][routingID]
	return dc, dc != nil
}

func (r *localRegistry) daemons(o owner) []DaemonInfo {
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
