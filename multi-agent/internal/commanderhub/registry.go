// Package commanderhub implements the observer side of commander-web-entry
// (PR-3): the /daemon-link WebSocket hub, the /api/commander reverse proxy,
// and the /commander web page. See
// docs/superpowers/specs/2026-06-15-commander-observer-hub-design.md.
package commanderhub

import (
	"context"
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
	// int64 to match Phase B's planned atomic.AddInt64/StoreInt64 usage in
	// runHeartbeatOnce — atomic.Int32 would force Phase B to use a wider
	// integer type at the call site.
	heartbeatErrCount atomic.Int64

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

// confirmOwnership checks whether this daemonConn still owns the row in the
// shared Postgres registry. SAFE in single-pod mode (returns true when
// dc.hub == nil || dc.hub.sharedReg == nil). In shared mode, checks the
// sticky dc.ownershipLost flag, else issues a 500ms-bounded SELECT.
//
// Ownership-lost semantics (codex Phase-B r3 MAJOR #1):
//
// Sticky-set ownershipLost ONLY when the SELECT returns a row whose
// (owning_instance_url, connection_id) doesn't match this conn — that
// is, a sibling pod or a newer same-pod connection has DEFINITIVELY
// taken over. The cluster can't reverse this; the WS must die.
//
// All other failure modes return false for THIS call only:
//   - Caller ctx cancelled / query timeout / PG transient error: future
//     calls retry.
//   - sql.ErrNoRows (row missing): the row may have been swept after a
//     PG outage; heartbeatUpsert self-heals on its next tick by
//     re-inserting. If we sticky-set ownershipLost here, we'd permanently
//     brick a daemon that the cluster considers healthy.
//
// On definitive loss, the heartbeat goroutine's separate force-close path
// is responsible for tearing the WS down; confirmOwnership itself does NOT
// close the WS.
func (dc *daemonConn) confirmOwnership(ctx context.Context) bool {
	// Single-pod mode: no shared registry, always own the connection.
	if dc.hub == nil || dc.hub.sharedReg == nil {
		return true
	}

	// Fast path: ownership already definitively lost.
	if dc.ownershipLost.Load() {
		return false
	}

	// Enforce 500ms deadline for the SELECT (bounded even if caller ctx is
	// unbounded). The shorter of caller ctx and 500ms wins.
	queryCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()

	row := dc.hub.sharedReg.db.QueryRowContext(queryCtx, confirmOwnershipSQL,
		dc.owner.userID, dc.owner.workspaceID, dc.shortID)
	var ownerURL, connID string
	if err := row.Scan(&ownerURL, &connID); err != nil {
		// All errors — including sql.ErrNoRows — are transient: don't
		// poison. Heartbeat self-heal re-inserts the row on its next
		// tick; an actually-displaced conn is signalled by a
		// (mismatched url|conn) row below, NOT by row-missing.
		return false
	}

	if ownerURL != dc.hub.sharedReg.advertiseURL || connID != dc.id {
		// Definitive: sibling pod owns row (or a newer same-pod conn).
		dc.ownershipLost.Store(true)
		return false
	}

	return true
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
