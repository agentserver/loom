package main

import (
	"context"
	"net/http"
	"strings"
	"time"

	"nhooyr.io/websocket"
)

// handleTunnelUpgrade authenticates via ?token= against byTunnel, upgrades the
// WS, wraps into yamux server session, tracks it in Server.tunnels, runs a
// 20s heartbeat, and blocks until session close. Spec §4.3.
func (s *Server) handleTunnelUpgrade(w http.ResponseWriter, r *http.Request) {
	sandboxID := strings.TrimPrefix(r.URL.Path, "/api/tunnel/")
	if sandboxID == "" || strings.Contains(sandboxID, "/") {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "missing token", http.StatusUnauthorized)
		return
	}
	s.mu.RLock()
	ident, known := s.byTunnel[token]
	s.mu.RUnlock()
	if !known || ident.SandboxID != sandboxID {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		// Accept has already written a response
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	t, err := newStubTunnel(ctx, sandboxID, ws)
	if err != nil {
		ws.Close(websocket.StatusInternalError, "mux init failed")
		return
	}
	s.registerTunnel(sandboxID, t)

	// Heartbeat: ping every 20s with 5s per-ping timeout via ctx.
	// nhooyr.io/websocket v1.8.17 Conn.Ping is Ping(ctx) single-arg; timeout via ctx.
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.Done():
				return
			case <-ticker.C:
				pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
				err := ws.Ping(pingCtx)
				pingCancel()
				if err != nil {
					t.Close()
					return
				}
			}
		}
	}()

	// Block until the session dies.
	<-t.Done()
	// Only remove ourselves if we're still the active tunnel (fix P1 #7).
	s.unregisterTunnel(sandboxID, t)
}

// registerTunnel installs t as the active tunnel for sandboxID, closing any
// previous tunnel for the same sandbox (spec §3.6).
func (s *Server) registerTunnel(sid string, t *stubTunnel) {
	var old *stubTunnel
	s.mu.Lock()
	old = s.tunnels[sid]
	s.tunnels[sid] = t
	s.mu.Unlock()
	if old != nil {
		old.Close()
	}
}

// unregisterTunnel removes t from Server.tunnels only if s.tunnels[sid] == t.
// Returns true if the entry was actually removed (spec §3.6, fix round-1 P1 #7).
func (s *Server) unregisterTunnel(sid string, t *stubTunnel) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.tunnels[sid]; ok && cur == t {
		delete(s.tunnels, sid)
		return true
	}
	return false
}

// hasTunnel reports whether a tunnel session is currently registered for sid.
// Used by tests to poll for server-side registerTunnel completion without racy
// time.Sleep (plan review r1 P1-1).
func (s *Server) hasTunnel(sid string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.tunnels[sid]
	return ok
}
