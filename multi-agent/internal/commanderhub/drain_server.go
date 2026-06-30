package commanderhub

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/yourorg/multi-agent/internal/commander"
)

// drainHandler handles incoming POST/GET /api/commander/_internal/drain requests.
// When RemoteAddr's host is a loopback IP (127.x or ::1), it skips HMAC auth.
// Otherwise, it requires the same HMAC+nonce auth as forwardHandler.
// On success, sends "observer_draining" event to all daemons of all owners,
// closes their WS connections, and returns 200 OK.
func (h *Hub) drainHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check if request is from loopback address.
	isLoopback := isLoopbackRemoteAddr(r.RemoteAddr)

	if !isLoopback {
		// Non-loopback: require full HMAC auth pipeline (same as forwardHandler).
		if !h.verifyDrainAuth(w, r) {
			return // verifyDrainAuth wrote the error response
		}
	}

	// Enter draining mode atomically under admitMu so that any WS upgrade
	// that passed the pre-check but has not yet called h.reg.add either:
	//   (a) sees draining=true after acquiring admitMu → rejects itself, or
	//   (b) completed h.reg.add before we got admitMu → is included in the
	//       drainAllLocalDaemons snapshot below.
	// After this block, no new daemons can be admitted and all current daemons
	// will be drained, so the pod is safe for preStop / eviction.
	h.admitMu.Lock()
	h.draining.Store(true)
	h.admitMu.Unlock()

	// Wait for any ServeHTTP goroutine that passed the pre-check before we set
	// draining=true to finish its post-upsert cleanup (sharedReg.remove in the
	// draining-rejection branch). admitMu is released above so those goroutines
	// can acquire it, see draining=true, and complete their remove+WS-close path.
	// We bound the wait by the request context deadline (k8s preStop timeout).
	inFlightDone := make(chan struct{})
	go func() { h.inFlightAdmissions.Wait(); close(inFlightDone) }()
	select {
	case <-inFlightDone:
	case <-r.Context().Done():
		log.Printf("commanderhub: drainHandler ctx deadline reached waiting for in-flight admissions; proceeding with drain")
	}

	// Drain all local daemons.
	h.drainAllLocalDaemons("observer-restart")
	w.WriteHeader(http.StatusOK)
}

// waitInFlightAdmissions blocks until all in-flight admission goroutines have
// finished or ctx is cancelled. Exposed for testing.
func (h *Hub) waitInFlightAdmissions(ctx context.Context) {
	done := make(chan struct{})
	go func() { h.inFlightAdmissions.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// isLoopbackRemoteAddr parses the remote address and checks if the host is a
// loopback IP (127.x or ::1). Returns false on error.
func isLoopbackRemoteAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

// verifyDrainAuth checks HMAC authentication for the drain endpoint.
// It reads the body (drain body is empty or {}), validates timestamp/nonce/HMAC,
// and returns true on success. On failure, it writes an error response and returns false.
func (h *Hub) verifyDrainAuth(w http.ResponseWriter, r *http.Request) bool {
	// Shared-mode guard: if not in shared mode, secrets not set, or DB unavailable, fail.
	// Mirrors the forwardHandler step-0 guard to prevent panic in insertNonce on nil DB.
	if h.sharedReg == nil || len(h.cluster.Secret) == 0 || h.sharedReg.db == nil {
		log.Printf("commanderhub: drain.received.503.not_shared_mode remote=%s", r.RemoteAddr)
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]any{
			"error": map[string]any{
				"code":    "backend_unavailable",
				"message": "observer is not in cluster mode",
			},
		})
		return false
	}

	// 1. Parse timestamp.
	tsStr := r.Header.Get("X-Forward-Ts")
	ts, err := parseHMACTimestamp(tsStr)
	if err != nil {
		http.Error(w, "bad timestamp: "+err.Error(), http.StatusBadRequest)
		return false
	}

	// 2. Parse/validate nonce.
	nonce := r.Header.Get("X-Forward-Nonce")
	if err := parseHMACNonce(nonce); err != nil {
		http.Error(w, "bad nonce: "+err.Error(), http.StatusBadRequest)
		return false
	}

	// 3. Validate sig header is 64 hex chars.
	sig := r.Header.Get("X-Forward-Sig")
	if len(sig) != 64 {
		http.Error(w, "bad sig: must be 64 hex chars", http.StatusBadRequest)
		return false
	}
	for _, c := range sig {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			http.Error(w, "bad sig: non-hex character", http.StatusBadRequest)
			return false
		}
	}

	// 4. Timestamp window check.
	if !timestampWithinWindow(ts, time.Now(), forwardHMACTimestampWindow) {
		http.Error(w, "timestamp outside allowed window", http.StatusForbidden)
		return false
	}

	// 5. Read body (drain body is empty or JSON {}).
	body, err := io.ReadAll(io.LimitReader(r.Body, int64(maxForwardBodySize)+1))
	if err != nil {
		http.Error(w, "read body error", http.StatusInternalServerError)
		return false
	}
	if len(body) > maxForwardBodySize {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return false
	}

	// 6. HMAC verify (purpose="drain" to prevent cross-endpoint replay from /forward).
	_, ok := verifyDrain(sig, h.cluster.Secret, h.cluster.PrevSecret, ts, nonce, body)
	if !ok {
		log.Printf("commanderhub: drain.denied.hmac remote=%s", r.RemoteAddr)
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}

	// 7. insertNonce — fail closed on PG error, reject on replay.
	ctx := r.Context()
	inserted, err := insertNonce(ctx, h.sharedReg.db, nonce)
	if err != nil {
		log.Printf("commanderhub: drain.received.503.nonce_pg remote=%s nonce_prefix=%s err=%v", r.RemoteAddr, noncePrefix(nonce), err)
		http.Error(w, "nonce storage unavailable", http.StatusServiceUnavailable)
		return false
	}
	if !inserted {
		log.Printf("commanderhub: drain.received.denied.replay remote=%s nonce_prefix=%s", r.RemoteAddr, noncePrefix(nonce))
		http.Error(w, "replay detected", http.StatusForbidden)
		return false
	}

	return true
}

// drainAllLocalDaemons iterates over all daemons in the local registry (all owners),
// sends an "observer_draining" event envelope to each, and closes their WS connections.
// Errors are logged at WARN level and execution continues.
func (h *Hub) drainAllLocalDaemons(reason string) {
	h.reg.mu.Lock()
	// Collect all daemons across all owners.
	var daemons []*daemonConn
	for _, m := range h.reg.conns {
		for _, dc := range m {
			daemons = append(daemons, dc)
		}
	}
	h.reg.mu.Unlock()

	// Send observer_draining event and close each daemon connection.
	for _, dc := range daemons {
		// Create an observer_draining event envelope.
		payload, _ := json.Marshal(commander.EventPayload{
			EventKind: "observer_draining",
			Text:      reason,
		})
		env := commander.Envelope{
			Type:    "event",
			Payload: payload,
		}

		if err := dc.writeEnvelope(env); err != nil {
			log.Printf("commanderhub: drain.write_error daemon_id=%s err=%v", dc.routingID(), err)
		}

		// Close the WebSocket connection.
		if err := dc.conn.Close(); err != nil {
			log.Printf("commanderhub: drain.close_error daemon_id=%s err=%v", dc.routingID(), err)
		}
	}
}
