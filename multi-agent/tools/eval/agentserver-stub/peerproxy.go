package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// isTimeoutErr recognizes both the ctx deadline path and yamux's own
// net.Error Timeout() path (spec §4.4 — return 504, fix plan review r3 P0).
func isTimeoutErr(err error, ctx context.Context) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if ctx.Err() == context.DeadlineExceeded {
		return true
	}
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return true
	}
	return false
}

// handlePeerProxy forwards ANY /api/agent/peer/<target_short>/proxy[/path][?query]
// over the target's yamux tunnel using HTTPStreamMeta. Spec §4.4.
func (s *Server) handlePeerProxy(w http.ResponseWriter, r *http.Request) {
	// Method whitelist (fix plan review r1 P0-3): CONNECT/TRACE/OPTIONS have
	// no HTTP-over-yamux semantics; reject before auth.
	switch r.Method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodHead:
		// allowed
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Auth
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	s.mu.RLock()
	caller, known := s.byProxy[token]
	s.mu.RUnlock()
	if !known {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Parse: /api/agent/peer/<short>/proxy[/rest][?q]
	const rootPrefix = "/api/agent/peer/"
	if !strings.HasPrefix(r.URL.EscapedPath(), rootPrefix) {
		http.Error(w, "bad path", http.StatusNotFound)
		return
	}
	tail := strings.TrimPrefix(r.URL.EscapedPath(), rootPrefix)
	slash := strings.Index(tail, "/")
	if slash < 0 {
		http.Error(w, "bad path", http.StatusNotFound)
		return
	}
	targetShort := tail[:slash]
	after := tail[slash:]
	const proxyMark = "/proxy"
	// Strict match (fix plan review r3 P1-1): after must equal "/proxy"
	// or start with "/proxy/". Otherwise "/proxyevil" would falsely match.
	if after != proxyMark && !strings.HasPrefix(after, proxyMark+"/") {
		http.Error(w, "bad path", http.StatusNotFound)
		return
	}
	rest := strings.TrimPrefix(after, proxyMark)
	forwardPath := rest
	if forwardPath == "" {
		forwardPath = "/"
	}
	if r.URL.RawQuery != "" {
		forwardPath += "?" + r.URL.RawQuery
	}

	// Look up target card in caller.workspace
	var targetCard agentCard
	found := false
	s.mu.RLock()
	for _, c := range s.cards {
		if c.WorkspaceID == caller.WorkspaceID && c.ShortID == targetShort {
			targetCard = c
			found = true
			break
		}
	}
	var tun *stubTunnel
	if found {
		tun = s.tunnels[targetCard.SandboxID]
	}
	s.mu.RUnlock()

	if !found {
		http.Error(w, "unknown target", http.StatusNotFound)
		return
	}
	if tun == nil {
		http.Error(w, "no active tunnel", http.StatusBadGateway)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}

	headers := make(map[string]string, len(r.Header))
	for k, vs := range r.Header {
		if len(vs) > 0 {
			headers[k] = vs[0]
		}
	}
	meta := HTTPStreamMeta{
		Method:  r.Method,
		Path:    forwardPath,
		Headers: headers,
	}

	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	respMeta, respBody, err := tun.OpenHTTPStream(ctx, meta, body)
	if err != nil {
		if isTimeoutErr(err, ctx) {
			http.Error(w, "tunnel timeout", http.StatusGatewayTimeout)
			return
		}
		http.Error(w, "tunnel error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer respBody.Close()

	for k, v := range respMeta.Headers {
		w.Header().Set(k, v)
	}
	if respMeta.Status > 0 {
		w.WriteHeader(respMeta.Status)
	}
	_, _ = io.Copy(w, respBody)
}
