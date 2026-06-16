package commanderhub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/yourorg/multi-agent/internal/commander"
)

// Mount registers all commander routes on the observerweb shared mux. hub may be
// nil (commander disabled) — auth/login routes still mount.
func Mount(mux *http.ServeMux, hub *Hub, auth *Authenticator) {
	mux.HandleFunc("/api/commander/login", auth.ServeLogin)
	mux.HandleFunc("/api/commander/login/poll", auth.ServeLoginPoll)
	mux.HandleFunc("/api/commander/logout", auth.ServeLogout)
	if hub == nil {
		return
	}
	ch := &commanderHandlers{hub: hub, auth: auth}
	mux.HandleFunc("/api/commander/daemons", ch.daemons)
	mux.HandleFunc("/api/commander/sessions", ch.sessionsFanout)
	mux.HandleFunc("/api/commander/daemons/", ch.daemonScoped)
}

type commanderHandlers struct {
	hub  *Hub
	auth *Authenticator
}

func (ch *commanderHandlers) ownerOf(w http.ResponseWriter, r *http.Request) (owner, bool) {
	ident, ok := ch.auth.CommanderIdentity(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return owner{}, false
	}
	return owner{userID: ident.UserID, workspaceID: ident.WorkspaceID}, true
}

func (ch *commanderHandlers) daemons(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	o, ok := ch.ownerOf(w, r)
	if !ok {
		return
	}
	writeJSON(w, map[string]any{"daemons": ch.hub.reg.daemons(o)})
}

func (ch *commanderHandlers) sessionsFanout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	o, ok := ch.ownerOf(w, r)
	if !ok {
		return
	}
	writeJSON(w, map[string]any{"daemons": ch.hub.FanOutSessions(r.Context(), o)})
}

// daemonScoped routes /api/commander/daemons/{id}/sessions[/{sid}[/turn]].
func (ch *commanderHandlers) daemonScoped(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/commander/daemons/")
	id, rest2, _ := strings.Cut(rest, "/")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	switch {
	case rest2 == "sessions":
		ch.listSessions(w, r, id)
	case strings.HasPrefix(rest2, "sessions/"):
		sub := strings.TrimPrefix(rest2, "sessions/")
		sid, tail, _ := strings.Cut(sub, "/")
		switch {
		case sid == "":
			http.NotFound(w, r)
		case tail == "turn":
			ch.turn(w, r, id, sid)
		case tail == "":
			ch.getSession(w, r, id, sid)
		default:
			http.NotFound(w, r)
		}
	default:
		http.NotFound(w, r)
	}
}

func (ch *commanderHandlers) listSessions(w http.ResponseWriter, r *http.Request, daemonID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	o, ok := ch.ownerOf(w, r)
	if !ok {
		return
	}
	payload, err := ch.hub.SendCommand(r.Context(), o, daemonID, "list_sessions", nil)
	if err != nil {
		writeSendCmdError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(payload)
}

func (ch *commanderHandlers) getSession(w http.ResponseWriter, r *http.Request, daemonID, sid string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	o, ok := ch.ownerOf(w, r)
	if !ok {
		return
	}
	args, _ := json.Marshal(commander.GetSessionArgs{ID: sid})
	payload, err := ch.hub.SendCommand(r.Context(), o, daemonID, "get_session", args)
	if err != nil {
		writeSendCmdError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(payload)
}

// writeSendCmdError maps a SendCommand error to an HTTP status for the
// non-streaming handlers (listSessions, getSession). A daemon-originated
// session_not_found or an absent daemon → 404; anything else → 502. The turn
// handler streams and forwards error frames as SSE, so it does not use this.
func writeSendCmdError(w http.ResponseWriter, err error) {
	var de *DaemonError
	if errors.As(err, &de) && de.Code == commander.ErrCodeSessionNotFound {
		http.NotFound(w, nil)
		return
	}
	if errors.Is(err, ErrDaemonNotFound) {
		http.NotFound(w, nil)
		return
	}
	http.Error(w, err.Error(), http.StatusBadGateway)
}

func (ch *commanderHandlers) turn(w http.ResponseWriter, r *http.Request, daemonID, sid string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	o, ok := ch.ownerOf(w, r)
	if !ok {
		return
	}
	var body struct {
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	args, _ := json.Marshal(commander.SessionTurnArgs{ID: sid, Prompt: body.Prompt})
	ctx, cancel := context.WithTimeout(r.Context(), ch.hub.TurnTimeout)
	defer cancel()

	chunkCh, err := ch.hub.SendCommandStream(ctx, o, daemonID, "session_turn", args)
	if errors.Is(err, ErrDaemonNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	sse := newSSEWriter(w)
	terminal := false
	for env := range chunkCh {
		sse.writeEnvelope(env)
		if env.Type == "command_result" || env.Type == "error" {
			terminal = true
		}
	}
	if !terminal {
		// stream closed without a terminal frame: timeout vs disconnect
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			sse.emitError("timeout", "no terminal frame within timeout")
		} else {
			sse.emitError(commander.ErrCodeBackendUnavailable, "daemon disconnected")
		}
	}
}
