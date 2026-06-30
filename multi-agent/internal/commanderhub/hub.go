package commanderhub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/yourorg/multi-agent/internal/commander"
	"github.com/yourorg/multi-agent/internal/identity"
)

const (
	wsReadLimit   = 1 << 20 // 1 MiB inbound cap (mirrors PR-2 daemon side)
	wsWriteWait   = 5 * time.Second
	wsReadTimeout = 90 * time.Second // 3x default heartbeat (30s) → dead peer after 3 missed pongs
)

// Hub owns the /daemon-link WebSocket endpoint and the owner-keyed registry of
// live daemon connections.
type Hub struct {
	resolver     identity.Resolver
	upgrader     websocket.Upgrader
	reg          *localRegistry
	sharedReg    *sharedRegistry // B1: nil in single-pod; populated by attachSharedRegistry (Phase B B4)
	forwardCli   *forwardClient  // C3: nil in single-pod; populated by attachForwardClient
	turns        turnStateBackend
	sessionCache *sessionListCache
	cmdSeq       atomic.Int64 // generates per-command IDs (see proxy.go)

	// TurnTimeout is the observer-side safety max applied to a session_turn
	// command. Turns continue draining after the browser/SSE client disconnects;
	// this bounds daemon work that never sends a terminal frame. Defaults to
	// defaultTurnTimeout (10 min); a caller may override it after NewHub.
	TurnTimeout time.Duration
}

// NewHub builds a Hub backed by resolver for bearer-token → Identity resolution.
func NewHub(resolver identity.Resolver) *Hub {
	return &Hub{
		resolver:     resolver,
		upgrader:     websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
		reg:          newLocalRegistry(),
		turns:        newMemTurnStore(),
		sessionCache: newSessionListCache(10 * time.Second),
		TurnTimeout:  defaultTurnTimeout,
	}
}

// ServeHTTP implements GET /api/daemon-link.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tok, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		http.Error(w, "missing bearer token", http.StatusUnauthorized)
		return
	}
	ident, err := h.resolver.Resolve(r.Context(), tok)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	o := owner{userID: ident.UserID, workspaceID: ident.WorkspaceID}

	// Generate 128-bit (16-byte) random connection ID; refuse upgrade on
	// crypto/rand failure rather than silently using weak entropy.
	connID, err := newDaemonID()
	if err != nil {
		http.Error(w, "server error", http.StatusServiceUnavailable)
		return
	}

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already wrote the error response.
	}
	conn.SetReadLimit(wsReadLimit)
	// Read deadline detects half-open peers (killed without a TCP FIN): if we
	// hear nothing for wsReadTimeout, ReadJSON fails → read loop returns → the
	// ServeHTTP defers tear down the registry entry + pending map.
	_ = conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(wsReadTimeout)) })

	dc := &daemonConn{
		id:      connID,
		owner:   o,
		conn:    conn,
		pending: make(map[string]*pendingEntry),
		done:    make(chan struct{}),
		hub:     h,
	}

	// First frame must be register; validate schema before admitting.
	reg, err := readFrame(conn)
	if err != nil {
		conn.Close()
		return
	}
	if reg.Type != "register" {
		conn.Close()
		return
	}
	var rp commander.RegisterPayload
	if err := json.Unmarshal(reg.Payload, &rp); err != nil {
		conn.Close()
		return
	}
	if rp.SchemaVersion != commander.SchemaVersion {
		_ = dc.writeEnvelope(errorEnvelope("", commander.ErrCodeSchemaVersionMismatch, "schema version mismatch"))
		dc.writeMu.Lock()
		_ = conn.WriteControl(websocket.CloseMessage, nil, time.Now().Add(wsWriteWait))
		dc.writeMu.Unlock()
		conn.Close()
		return
	}

	// Cluster-mode: require non-empty (and non-whitespace) ShortID so peer pods
	// can resolve the daemon by a stable name (not an ephemeral connection ID).
	if h.sharedReg != nil && strings.TrimSpace(rp.ShortID) == "" {
		_ = dc.writeEnvelope(errorEnvelope("", commander.ErrCodeInvalidRequest, "short_id is required when observer is in cluster mode"))
		dc.writeMu.Lock()
		_ = conn.WriteControl(websocket.CloseMessage, nil, time.Now().Add(wsWriteWait))
		dc.writeMu.Unlock()
		conn.Close()
		return
	}

	dc.shortID = rp.ShortID
	dc.displayName = rp.DisplayName
	dc.kind = rp.Kind
	dc.driverVersion = rp.DriverVersion
	capabilities := map[string]bool{
		commander.CapabilitySessions: true,
		commander.CapabilityTurn:     true,
	}
	for _, capability := range rp.Capabilities {
		capability = strings.TrimSpace(capability)
		if capability != "" {
			capabilities[capability] = true
		}
	}
	dc.metaMu.Lock()
	dc.capabilities = capabilities
	dc.lastSeenAt = time.Now().UTC()
	dc.metaMu.Unlock()

	// Cluster-mode admission: upsert into shared Postgres registry BEFORE
	// adding to local registry, under a 3s timeout. On failure, refuse WS.
	if h.sharedReg != nil {
		upsertCtx, upsertCancel := context.WithTimeout(r.Context(), 3*time.Second)
		upsertErr := h.sharedReg.connectUpsert(upsertCtx, dc)
		upsertCancel()
		if upsertErr != nil {
			_ = dc.writeEnvelope(errorEnvelope("", commander.ErrCodeBackendUnavailable, "registry unavailable"))
			dc.writeMu.Lock()
			_ = conn.WriteControl(websocket.CloseMessage, nil, time.Now().Add(wsWriteWait))
			dc.writeMu.Unlock()
			conn.Close()
			return
		}
	}

	routingID := dc.routingID()

	h.reg.add(dc)

	// Teardown (reverse order of setup):
	// 1. Stop heartbeat first so it cannot touch conn after we start removing.
	// 2. Remove from shared registry (connection-id-guarded; safe if ownership lost).
	// 3. Remove from local registry (predicate-guarded; safe on reconnect race).
	// 4. Invalidate session cache.
	// 5. Signal waiters and fail pending commands.
	hbCtx, hbCancel := context.WithCancel(context.Background())
	hbDone := make(chan struct{})

	if h.sharedReg != nil {
		go func() {
			defer close(hbDone)
			h.sharedReg.runHeartbeat(hbCtx, dc)
		}()
	} else {
		close(hbDone)
	}

	// Teardown defers run in LIFO order:
	// 1st registered = last to run: remove from local registry (predicate-guarded).
	// 2nd registered: invalidate session cache.
	// 3rd registered: signal waiters (close dc.done).
	// 4th registered: fail all pending commands.
	// 5th registered = first to run: stop heartbeat, then remove from shared registry.
	// This ordering ensures the shared row is cleaned up before the local entry is
	// removed, and that waiters/pending are only unblocked after teardown is complete.
	defer h.reg.removeIf(o, routingID, func(existing *daemonConn) bool { return existing.id == dc.id })
	defer h.invalidateDaemonSessions(o, routingID)
	defer close(dc.done)
	defer dc.failAllPending()
	defer func() {
		hbCancel()
		<-hbDone
		if h.sharedReg != nil {
			rmCtx, rmCancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = h.sharedReg.remove(rmCtx, o, dc.shortID, dc.id)
			rmCancel()
		}
	}()

	// Ack: PR-2 WSClient only flips linked=true on receipt.
	if err := dc.writeEnvelope(commander.Envelope{Type: "ack"}); err != nil {
		return
	}

	dc.readLoop()
}

// attachSharedRegistry sets the shared Postgres registry on this Hub.
// Called during wiring (Phase D D1) after the Hub is constructed.
func (h *Hub) attachSharedRegistry(sr *sharedRegistry) {
	h.sharedReg = sr
}

// --- daemonConn WS mechanics ---

func readFrame(conn *websocket.Conn) (commander.Envelope, error) {
	var env commander.Envelope
	return env, conn.ReadJSON(&env)
}

func (dc *daemonConn) writeEnvelope(env commander.Envelope) error {
	dc.writeMu.Lock()
	defer dc.writeMu.Unlock()
	_ = dc.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
	return dc.conn.WriteJSON(env)
}

// pendingEntry is one in-flight command's reply state. The data channel ch is
// NEVER closed (closing it would race routeFrame's unlocked sendOrDrop against a
// consumer's removePending, panicking on a late frame). cancel is closed by
// removePending to unblock a stuck terminal send in routeFrame: if a consumer
// cancels while the buffer is full, the blocking terminal send must have an
// escape hatch other than dc.done (which is closed only AFTER the read loop
// returns — and the read loop is exactly the stuck goroutine).
type pendingEntry struct {
	ch        chan commander.Envelope // data channel; NEVER closed (GC reclaims it)
	cancel    chan struct{}           // closed by removePending to unblock a stuck terminal send
	streaming bool                    // streaming commands may terminate on status_code terminal events
}

// registerPending reserves a reply entry for cmdID and returns it. The data
// channel ch is NEVER closed (see pendingEntry); the per-entry cancel channel is
// closed by removePending. Consumers read from entry.ch and detect completion
// without a ch-close: terminal command_result/error frames for all commands,
// terminal status events for streaming commands, disconnect via <-dc.done, and
// cancel via <-ctx.Done().
func (dc *daemonConn) registerPending(cmdID string, streaming bool) *pendingEntry {
	pe := &pendingEntry{
		ch:        make(chan commander.Envelope, 16),
		cancel:    make(chan struct{}),
		streaming: streaming,
	}
	dc.pendingMu.Lock()
	dc.pending[cmdID] = pe
	dc.pendingMu.Unlock()
	return pe
}

// removePending drops the registry entry for cmdID and closes its per-entry
// cancel channel. Closing cancel is safe: it is only closed here and only ever
// received-from (never sent-to). It does NOT close entry.ch — a late daemon
// frame landing in routeFrame after this delete finds no map entry and is
// dropped, OR, if routeFrame already grabbed the entry before this delete, its
// stuck terminal send selects <-cancel and unblocks instead of wedging the read
// loop forever.
func (dc *daemonConn) removePending(cmdID string) {
	dc.pendingMu.Lock()
	pe, ok := dc.pending[cmdID]
	if ok {
		delete(dc.pending, cmdID)
	}
	dc.pendingMu.Unlock()
	if ok {
		close(pe.cancel)
	}
}

// failAllPending swaps in a fresh empty map so routeFrame lookups miss for every
// in-flight command, and closes each old entry's per-entry cancel so any in-
// flight routeFrame terminal send unblocks. It does NOT close any data channel
// (see pendingEntry). Called on read-loop exit; by the time ServeHTTP's defers
// run this the read loop (and thus any routeFrame) has already returned, but the
// closes are correct/safe regardless.
func (dc *daemonConn) failAllPending() {
	dc.pendingMu.Lock()
	old := dc.pending
	dc.pending = make(map[string]*pendingEntry)
	dc.pendingMu.Unlock()
	for _, pe := range old {
		close(pe.cancel)
	}
}

// readLoop drains inbound frames and routes each to its pending reply channel
// by frame.ID. Returns on read error / close → ServeHTTP's defers clean up.
func (dc *daemonConn) readLoop() {
	for {
		env, err := readFrame(dc.conn)
		if err != nil {
			return
		}
		// A successful read means the peer is alive; push the read deadline
		// out. A failed SetReadDeadline here is harmless (conn already closing).
		now := time.Now()
		dc.metaMu.Lock()
		dc.lastSeenAt = now.UTC()
		dc.metaMu.Unlock()
		_ = dc.conn.SetReadDeadline(now.Add(wsReadTimeout))
		dc.routeFrame(env)
	}
}

func (dc *daemonConn) routeFrame(env commander.Envelope) {
	if env.ID == "" {
		return // ack / heartbeat / unsolicited: nothing to correlate
	}
	dc.pendingMu.Lock()
	pe := dc.pending[env.ID]
	dc.pendingMu.Unlock()
	if pe == nil {
		return // unknown id (stale/late, or removed by a cancelling consumer): drop
	}
	terminal := isTerminalEnvelope(env) || (pe.streaming && isTerminalStatusEnvelope(env))
	if !sendOrDrop(pe.ch, env, terminal, pe.cancel, dc.done) {
		return
	}
	if terminal {
		dc.removePending(env.ID)
	}
}

// sendOrDrop delivers env to ch. Non-terminal events are dropped if the buffer
// is full (avoid blocking the read loop on a slow consumer). Terminal frames
// force through, blocking on cancel (consumer cancelled → removePending closed
// the per-entry cancel) or done (connection gone) as escapes. The cancel escape
// is what prevents a terminal frame from wedging the read loop forever when the
// consumer cancelled with a full buffer: dc.done alone is insufficient because
// it is closed only after the read loop returns — and the read loop is exactly
// the stuck goroutine. Returns false if dropped.
func sendOrDrop(ch chan commander.Envelope, env commander.Envelope, terminal bool, cancel <-chan struct{}, done <-chan struct{}) bool {
	select {
	case ch <- env:
		return true
	default:
	}
	if !terminal {
		return false
	}
	select {
	case ch <- env:
		return true
	case <-cancel:
		return false // consumer cancelled; drop the terminal
	case <-done:
		return false // connection gone
	}
}

// nextCmdID returns a hub-unique command id (used by proxy.go).
func (h *Hub) nextCmdID() string {
	return strconv.FormatInt(h.cmdSeq.Add(1), 36)
}

// --- shared utils (bearerToken also used by auth.go) ---

func bearerToken(auth string) (string, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		return "", false
	}
	tok := strings.TrimSpace(strings.TrimPrefix(auth, prefix))
	return tok, tok != ""
}

func newDaemonID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func errorEnvelope(id, code, message string) commander.Envelope {
	payload, _ := json.Marshal(commander.ErrorPayload{Code: code, Message: message})
	return commander.Envelope{Type: "error", ID: id, Payload: payload}
}
