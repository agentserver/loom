package commanderhub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/yourorg/multi-agent/internal/commander"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
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
	mux.HandleFunc("/api/commander/tree", ch.tree)
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

func (ch *commanderHandlers) tree(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	o, ok := ch.ownerOf(w, r)
	if !ok {
		return
	}
	writeJSON(w, ch.hub.CommanderTree(r.Context(), o))
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
		case tail == "files":
			ch.listFiles(w, r, id, sid)
		case tail == "files/content":
			ch.readFile(w, r, id, sid)
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
		writeSendCmdError(w, r, err)
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
		writeSendCmdError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(payload)
}

func (ch *commanderHandlers) listFiles(w http.ResponseWriter, r *http.Request, daemonID, sid string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	o, ok := ch.ownerOf(w, r)
	if !ok {
		return
	}
	payload, err := ch.hub.ListFiles(r.Context(), o, daemonID, sid, r.URL.Query().Get("path"))
	if err != nil {
		writeSendCmdError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(payload)
}

func (ch *commanderHandlers) readFile(w http.ResponseWriter, r *http.Request, daemonID, sid string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	o, ok := ch.ownerOf(w, r)
	if !ok {
		return
	}
	payload, err := ch.hub.ReadFile(r.Context(), o, daemonID, sid, r.URL.Query().Get("path"))
	if err != nil {
		writeSendCmdError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(payload)
}

// writeSendCmdError maps a SendCommand error to an HTTP status for the
// non-streaming handlers. Daemon-originated session_not_found or an absent
// daemon → 404, invalid_request → 400, anything else → 502. The turn handler
// streams and forwards error frames as SSE, so it does not use this.
func writeSendCmdError(w http.ResponseWriter, r *http.Request, err error) {
	var de *DaemonError
	if errors.As(err, &de) {
		switch de.Code {
		case commander.ErrCodeSessionNotFound:
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		case commander.ErrCodeInvalidRequest:
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if errors.Is(err, ErrDaemonNotFound) {
		http.NotFound(w, r)
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
	if _, ok := ch.hub.reg.lookup(o, daemonID); !ok {
		http.NotFound(w, r)
		return
	}
	key := turnKey{owner: o, daemonID: daemonID, sessionID: sid}
	if !ch.hub.turns.begin(key) {
		http.Error(w, "turn already in flight", http.StatusConflict)
		return
	}
	args, _ := json.Marshal(commander.SessionTurnArgs{ID: sid, Prompt: body.Prompt})
	turnCtx, cancel := context.WithTimeout(context.Background(), ch.hub.TurnTimeout)
	defer cancel()

	chunkCh, err := ch.hub.SendCommandStream(turnCtx, o, daemonID, "session_turn", args)
	if errors.Is(err, ErrDaemonNotFound) {
		ch.hub.turns.finish(key, turnStateDisconnected)
		ch.hub.invalidateDaemonSessions(key.owner, key.daemonID)
		http.NotFound(w, r)
		return
	}
	if errors.Is(err, ErrDaemonGone) {
		ch.hub.turns.finish(key, turnStateDisconnected)
		ch.hub.invalidateDaemonSessions(key.owner, key.daemonID)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if err != nil {
		ch.hub.turns.fail(key, err.Error())
		ch.hub.invalidateDaemonSessions(key.owner, key.daemonID)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	sse := newSSEWriter(w)
	terminal := false
	writeSSE := true
	reqDone := r.Context().Done()
	for !terminal {
		select {
		case env, ok := <-chunkCh:
			if !ok {
				goto streamClosed
			}
			ch.updateTurnStateFromEnvelope(key, env)
			if writeSSE {
				sse.writeEnvelope(env)
			}
			if env.Type == "command_result" || env.Type == "error" {
				terminal = true
			}
		case <-reqDone:
			writeSSE = false
			reqDone = nil
		}
	}
streamClosed:
	if !terminal {
		ch.finishTurnWithoutTerminal(key, turnCtx.Err(), sse, writeSSE)
	}
}

func (ch *commanderHandlers) finishTurnWithoutTerminal(key turnKey, ctxErr error, sse *sseWriter, writeSSE bool) {
	switch {
	case errors.Is(ctxErr, context.DeadlineExceeded):
		msg := "no terminal frame within timeout"
		ch.hub.turns.fail(key, msg)
		if writeSSE {
			sse.emitError("timeout", msg)
		}
	case errors.Is(ctxErr, context.Canceled):
		msg := context.Canceled.Error()
		ch.hub.turns.fail(key, msg)
		if writeSSE {
			sse.emitError("request_canceled", msg)
		}
	default:
		ch.hub.turns.finish(key, turnStateDisconnected)
		if writeSSE {
			sse.emitError(commander.ErrCodeBackendUnavailable, "daemon disconnected")
		}
	}
	ch.hub.invalidateDaemonSessions(key.owner, key.daemonID)
}

func (ch *commanderHandlers) updateTurnStateFromEnvelope(key turnKey, env commander.Envelope) {
	switch env.Type {
	case "event":
		var ep commander.EventPayload
		if err := json.Unmarshal(env.Payload, &ep); err != nil {
			return
		}
		switch ep.EventKind {
		case "status":
			switch ep.StatusCode {
			case agentbackend.StatusQueued:
				ch.hub.turns.set(key, turnStateQueued)
			case agentbackend.StatusStarting:
				ch.hub.turns.set(key, turnStateQueued)
			case agentbackend.StatusAnswering:
				ch.hub.turns.set(key, turnStateAnswering)
			case agentbackend.StatusAwaitingApproval:
				ch.hub.turns.finish(key, turnStateAwaitingApproval)
			case agentbackend.StatusDone:
				ch.hub.turns.finish(key, turnStateDone)
			case agentbackend.StatusError:
				ch.hub.turns.fail(key, ep.Text)
			default:
				switch ep.Text {
				case "queued on daemon", "queued-on-daemon", "accepted by daemon":
					ch.hub.turns.set(key, turnStateQueued)
				case "starting codex":
					ch.hub.turns.set(key, turnStateQueued)
				case "codex running":
					ch.hub.turns.set(key, turnStateAnswering)
				}
			}
		case "chunk":
			ch.hub.turns.set(key, turnStateAnswering)
		}
	case "command_result":
		if payloadAwaitingUser(env.Payload) {
			ch.hub.turns.finish(key, turnStateAwaitingApproval)
		} else {
			ch.hub.turns.finish(key, turnStateDone)
		}
		ch.hub.invalidateDaemonSessions(key.owner, key.daemonID)
	case "error":
		ch.hub.turns.fail(key, errorMessage(env.Payload))
		ch.hub.invalidateDaemonSessions(key.owner, key.daemonID)
	}
}

func payloadAwaitingUser(payload []byte) bool {
	var body struct {
		Result struct {
			AwaitingUser any `json:"awaiting_user"`
		} `json:"result"`
	}
	_ = json.Unmarshal(payload, &body)
	return body.Result.AwaitingUser != nil
}
