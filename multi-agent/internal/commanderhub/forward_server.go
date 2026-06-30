package commanderhub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/yourorg/multi-agent/internal/commander"
)

// forwardHandler handles incoming POST /api/commander/_internal/forward
// requests from peer pods in cluster mode. It verifies HMAC authentication,
// prevents replay attacks via nonce insertion, and dispatches the command to
// the local registry (never to a remote peer — loop prevention).
//
// Pipeline (strict order per spec v19):
//
//	0. Shared-mode guard: sharedReg == nil || cluster.Secret == nil || sharedReg.db == nil → 503
//	1. Method check: non-POST → 405
//	2. Content-Length cap: > maxForwardBodySize → 413
//	3. Parse X-Forward-Ts → 400
//	4. Parse/validate X-Forward-Nonce → 400
//	5. Validate X-Forward-Sig is 64 hex chars → 400
//	6. Timestamp window check → 403
//	7. ReadAll (capped) → 413 if over cap
//	8. HMAC verify → 403
//	9. insertNonce → 503 on PG error (fail closed), 403 on replay
//	10. Decode body as forwardRequest
//	11. Audit accepted
//	12. Local registry lookup (ONLY — never lookupRemote, loop prevention) → 404
//	13. Capability check (read_file + no file_preview_encoded_cap → 426)
//	14. Non-streaming: sendCommandToLocal → marshal result → 200
//	15. Streaming: Content-Type octet-stream, drain goroutine, per-envelope writeEnvelopeFrame
func (h *Hub) forwardHandler(w http.ResponseWriter, r *http.Request) {
	// 0. Shared-mode guard.
	if h.sharedReg == nil || len(h.cluster.Secret) == 0 || h.sharedReg.db == nil {
		log.Printf("commanderhub: forward.received.503.not_shared_mode method=%s remote=%s", r.Method, r.RemoteAddr)
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]any{
			"error": map[string]any{
				"code":    "backend_unavailable",
				"message": "observer is not in cluster mode",
			},
		})
		return
	}

	// 1. Method check.
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 2. Content-Length cap (early reject before reading body).
	if r.ContentLength > int64(maxForwardBodySize) {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}

	// 3. Parse timestamp.
	tsStr := r.Header.Get("X-Forward-Ts")
	ts, err := parseHMACTimestamp(tsStr)
	if err != nil {
		http.Error(w, "bad timestamp: "+err.Error(), http.StatusBadRequest)
		return
	}

	// 4. Parse/validate nonce.
	nonce := r.Header.Get("X-Forward-Nonce")
	if err := parseHMACNonce(nonce); err != nil {
		http.Error(w, "bad nonce: "+err.Error(), http.StatusBadRequest)
		return
	}

	// 5. Validate sig header is 64 hex chars.
	sig := r.Header.Get("X-Forward-Sig")
	if len(sig) != 64 {
		http.Error(w, "bad sig: must be 64 hex chars", http.StatusBadRequest)
		return
	}
	for _, c := range sig {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			http.Error(w, "bad sig: non-hex character", http.StatusBadRequest)
			return
		}
	}

	// 6. Timestamp window check.
	if !timestampWithinWindow(ts, time.Now(), forwardHMACTimestampWindow) {
		log.Printf("commanderhub: forward.received.denied.timestamp remote=%s ts=%d", r.RemoteAddr, ts)
		http.Error(w, "timestamp outside allowed window", http.StatusForbidden)
		return
	}

	// 7. Read body (capped).
	body, err := io.ReadAll(io.LimitReader(r.Body, int64(maxForwardBodySize)+1))
	if err != nil {
		http.Error(w, "read body error", http.StatusInternalServerError)
		return
	}
	if len(body) > maxForwardBodySize {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}

	// 8. HMAC verify.
	_, ok := verifyForward(sig, h.cluster.Secret, h.cluster.PrevSecret, ts, nonce, body)
	if !ok {
		log.Printf("commanderhub: forward.received.denied.hmac remote=%s", r.RemoteAddr)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// 9. insertNonce — fail closed on PG error.
	ctx := r.Context()
	inserted, err := insertNonce(ctx, h.sharedReg.db, nonce)
	if err != nil {
		log.Printf("commanderhub: forward.received.503.nonce_pg remote=%s nonce_prefix=%s err=%v", r.RemoteAddr, noncePrefix(nonce), err)
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]any{
			"error": map[string]any{
				"code":    "backend_unavailable",
				"message": "nonce storage unavailable",
			},
		})
		return
	}
	if !inserted {
		log.Printf("commanderhub: forward.received.denied.replay remote=%s nonce_prefix=%s", r.RemoteAddr, noncePrefix(nonce))
		http.Error(w, "replay detected", http.StatusForbidden)
		return
	}

	// 10. Decode body as forwardRequest.
	var wire forwardRequest
	if err := json.Unmarshal(body, &wire); err != nil {
		http.Error(w, "bad body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// 11. Audit accepted.
	log.Printf("commanderhub: forward.received.accepted user_id=%s workspace_id=%s daemon_id=%s command=%s streaming=%v remote=%s",
		wire.UserID, wire.WorkspaceID, wire.DaemonID, wire.Command, wire.Stream, r.RemoteAddr)

	// Build the owner from the wire request.
	o := owner{userID: wire.UserID, workspaceID: wire.WorkspaceID}

	// 12. Local registry lookup ONLY — never lookupRemote (loop prevention).
	dc, ok2 := h.reg.lookup(o, wire.DaemonID)
	if !ok2 {
		http.NotFound(w, r)
		return
	}

	// 13. Capability check: read_file requires file_preview_encoded_cap.
	if wire.Command == "read_file" {
		dc.metaMu.Lock()
		hasCap := dc.capabilities[commander.CapabilityFilePreviewEncodedCap]
		dc.metaMu.Unlock()
		if !hasCap {
			writeJSONStatus(w, http.StatusUpgradeRequired, map[string]any{
				"error": map[string]any{
					"code":    commander.ErrCodeDaemonUpgradeRequired,
					"message": "daemon must be upgraded to support file_preview_encoded_cap",
				},
			})
			return
		}
	}

	if !wire.Stream {
		// 14. Non-streaming path.
		result, err := h.sendCommandToLocal(ctx, dc, wire.Command, wire.Args)
		if err != nil {
			if errors.Is(err, ErrDaemonGone) {
				writeJSONStatus(w, http.StatusBadGateway, forwardResponse{
					Error: &forwardRespErr{Code: "daemon_gone", Message: "daemon disconnected"},
				})
				return
			}
			var de *DaemonError
			if errors.As(err, &de) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(forwardResponse{
					Error: &forwardRespErr{Code: de.Code, Message: de.Message},
				})
				return
			}
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(forwardResponse{Result: result})
		return
	}

	// 15. Streaming path.
	// Use a child context so we can cancel it when the HTTP request is done,
	// ensuring dc.removePending runs even if the client disconnects.
	innerCtx, innerCancel := context.WithCancel(ctx)
	defer innerCancel()

	envCh, err := h.sendCommandStreamToLocal(innerCtx, dc, wire.Command, wire.Args, forwardStreamBuf)
	if err != nil {
		if errors.Is(err, ErrDaemonGone) {
			http.Error(w, "daemon disconnected", http.StatusBadGateway)
			return
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)

	flusher, canFlush := w.(http.Flusher)

	enc := NewEnvelopeEncoder(w)
	reqDone := ctx.Done()
	for {
		select {
		case env, more := <-envCh:
			if !more {
				return
			}
			if err := enc.Encode(&env); err != nil {
				// Client disconnected; innerCancel will clean up.
				return
			}
			if canFlush {
				flusher.Flush()
			}
			if isTerminalStreamEnvelope(env) {
				return
			}
		case <-reqDone:
			// HTTP request done (client disconnected); cancel inner ctx so
			// sendCommandStreamToLocal's goroutine drains and dc.removePending runs.
			innerCancel()
			reqDone = nil // arm only once
		}
	}
}
