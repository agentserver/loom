package commander

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// LinkStatusFunc reports whether the daemon currently has an active observer
// WebSocket link. It feeds /healthz without coupling HTTP to WS internals.
type LinkStatusFunc func() bool

// NewHTTPHandler mounts the daemon's local debug API on a fresh ServeMux.
// When authToken is non-empty every request must provide
// Authorization: Bearer <authToken>.
func NewHTTPHandler(h *Handler, link LinkStatusFunc, authToken string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		fmt.Fprintf(w, "ok\nlinked: %v\n", link())
	})
	mux.HandleFunc("/sessions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		sessions, err := h.ListSessions(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"sessions": sessions})
	})
	mux.HandleFunc("/sessions/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/sessions/")
		if rest == "" {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(rest, "/turn") {
			id := strings.TrimSuffix(rest, "/turn")
			if id == "" {
				http.NotFound(w, r)
				return
			}
			handleTurn(h, w, r, id)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		sess, messages, err := h.GetSession(r.Context(), rest)
		if errors.Is(err, agentbackend.ErrSessionNotFound) {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"session": sess, "messages": messages})
	})
	if authToken == "" {
		return mux
	}
	return requireBearer(authToken, mux)
}

func handleTurn(h *Handler, w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad body: "+err.Error(), http.StatusBadRequest)
		return
	}

	sink := newSSESink(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	res, err := h.SessionTurn(r.Context(), id, body.Prompt, sink)
	if errors.Is(err, agentbackend.ErrSessionNotFound) {
		if sink.Written() {
			sink.EmitError(ErrCodeSessionNotFound, "session not found")
			return
		}
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	if err != nil {
		if sink.Written() {
			sink.EmitError(ErrCodeInternal, err.Error())
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sink.EmitDone(marshalTurnResult(res))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func requireBearer(token string, next http.Handler) http.Handler {
	want := "Bearer " + token
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func marshalTurnResult(res executor.Result) []byte {
	body, _ := json.Marshal(map[string]any{
		"result": map[string]any{
			"summary":           res.Summary,
			"capability_change": res.CapabilityChange,
			"session_id":        res.SessionID,
			"awaiting_user":     res.AwaitingUser,
		},
	})
	return body
}
