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
	infos, err := ch.hub.listDaemons(r.Context(), o)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"daemons": infos})
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
// daemon → 404, invalid_request → 400, daemon_upgrade_required → 426,
// anything else → 502. The turn handler streams and forwards error frames
// as SSE, so it does not use this.
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
		case commander.ErrCodeDaemonUpgradeRequired:
			http.Error(w, err.Error(), http.StatusUpgradeRequired)
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
		Fresh  bool   `json:"fresh,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	if _, ok, err := ch.hub.lookupDaemon(r.Context(), o, daemonID); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	} else if !ok {
		http.NotFound(w, r)
		return
	}
	key := turnKey{owner: o, shortID: daemonID, sessionID: sid}
	began, err := ch.hub.turns.begin(r.Context(), key)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if !began {
		http.Error(w, "turn already in flight", http.StatusConflict)
		return
	}
	args, _ := json.Marshal(commander.SessionTurnArgs{ID: sid, Prompt: body.Prompt, Fresh: body.Fresh})
	turnCtx, cancel := context.WithTimeout(context.Background(), ch.hub.TurnTimeout)
	defer cancel()

	chunkCh, err := ch.hub.SendCommandStream(turnCtx, o, daemonID, "session_turn", args)
	if errors.Is(err, ErrDaemonNotFound) {
		_ = ch.hub.turns.finish(r.Context(), key, turnStateDisconnected)
		ch.hub.invalidateDaemonSessions(key.owner, key.shortID)
		http.NotFound(w, r)
		return
	}
	if errors.Is(err, ErrDaemonGone) {
		_ = ch.hub.turns.finish(r.Context(), key, turnStateDisconnected)
		ch.hub.invalidateDaemonSessions(key.owner, key.shortID)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if err != nil {
		_ = ch.hub.turns.fail(r.Context(), key, err.Error())
		ch.hub.invalidateDaemonSessions(key.owner, key.shortID)
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
			// Fresh-session protocol: when the terminal command_result
			// carries a real backend session ID in result.session_id and
			// it differs from the placeholder we minted client-side,
			// rekey turn-state BEFORE any terminal write so finish/
			// invalidate land under the real key. The placeholder
			// entry is dropped — the daemon-sessions invalidation
			// then propagates the real session into the tree.
			if env.Type == "command_result" {
				if realID := payloadSessionID(env.Payload); realID != "" && realID != key.sessionID {
					realKey := turnKey{owner: key.owner, shortID: key.shortID, sessionID: realID}
					_ = ch.hub.turns.rekey(r.Context(), key, realKey)
					key = realKey
				}
			}
			ch.updateTurnStateFromEnvelope(r.Context(), key, env)
			if writeSSE {
				sse.writeEnvelope(env)
			}
			if isTerminalStreamEnvelope(env) {
				terminal = true
			}
		case <-reqDone:
			writeSSE = false
			reqDone = nil
		}
	}
streamClosed:
	if !terminal {
		ch.finishTurnWithoutTerminal(r.Context(), key, turnCtx.Err(), sse, writeSSE)
	}
}

func (ch *commanderHandlers) finishTurnWithoutTerminal(ctx context.Context, key turnKey, ctxErr error, sse *sseWriter, writeSSE bool) {
	switch {
	case errors.Is(ctxErr, context.DeadlineExceeded):
		msg := "no terminal frame within timeout"
		_ = ch.hub.turns.fail(ctx, key, msg)
		if writeSSE {
			sse.emitError("timeout", msg)
		}
	case errors.Is(ctxErr, context.Canceled):
		msg := context.Canceled.Error()
		_ = ch.hub.turns.fail(ctx, key, msg)
		if writeSSE {
			sse.emitError("request_canceled", msg)
		}
	default:
		_ = ch.hub.turns.finish(ctx, key, turnStateDisconnected)
		if writeSSE {
			sse.emitError(commander.ErrCodeBackendUnavailable, "daemon disconnected")
		}
	}
	ch.hub.invalidateDaemonSessions(key.owner, key.shortID)
}

func (ch *commanderHandlers) updateTurnStateFromEnvelope(ctx context.Context, key turnKey, env commander.Envelope) {
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
				_ = ch.hub.turns.set(ctx, key, turnStateQueued)
			case agentbackend.StatusStarting:
				_ = ch.hub.turns.set(ctx, key, turnStateQueued)
			case agentbackend.StatusAnswering:
				_ = ch.hub.turns.set(ctx, key, turnStateAnswering)
			case agentbackend.StatusAwaitingApproval:
				_ = ch.hub.turns.finish(ctx, key, turnStateAwaitingApproval)
				ch.hub.invalidateDaemonSessions(key.owner, key.shortID)
			case agentbackend.StatusDone:
				_ = ch.hub.turns.finish(ctx, key, turnStateDone)
				ch.hub.invalidateDaemonSessions(key.owner, key.shortID)
			case agentbackend.StatusError:
				_ = ch.hub.turns.fail(ctx, key, ep.Text)
				ch.hub.invalidateDaemonSessions(key.owner, key.shortID)
			default:
				switch ep.Text {
				case "queued on daemon", "queued-on-daemon", "accepted by daemon":
					_ = ch.hub.turns.set(ctx, key, turnStateQueued)
				case "starting codex":
					_ = ch.hub.turns.set(ctx, key, turnStateQueued)
				case "codex running":
					_ = ch.hub.turns.set(ctx, key, turnStateAnswering)
				}
			}
		case "chunk":
			_ = ch.hub.turns.set(ctx, key, turnStateAnswering)
		}
	case "command_result":
		if payloadAwaitingUser(env.Payload) {
			_ = ch.hub.turns.finish(ctx, key, turnStateAwaitingApproval)
		} else {
			_ = ch.hub.turns.finish(ctx, key, turnStateDone)
		}
		ch.hub.invalidateDaemonSessions(key.owner, key.shortID)
	case "error":
		_ = ch.hub.turns.fail(ctx, key, errorMessage(env.Payload))
		ch.hub.invalidateDaemonSessions(key.owner, key.shortID)
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

// payloadSessionID extracts the real backend session ID from a
// terminal command_result payload (marshalTurnResult writes it as
// result.session_id). Returns empty when absent or unparseable.
//
// The returned id is treated as a backend-native session id (used to
// rekey the turn-state map from the client placeholder). This relies on
// commander.marshalTurnResult emitting result.session_id from the
// backend executor's Result.SessionID, not from any bridge id — see the
// caveat in commander/http.go::marshalTurnResult and issue #29. If the
// commander wire protocol ever splits into backend / bridge fields,
// this helper must be updated to read the backend field explicitly
// instead of falling through whatever happens to be in session_id.
func payloadSessionID(payload []byte) string {
	var body struct {
		Result struct {
			SessionID string `json:"session_id"`
		} `json:"result"`
	}
	_ = json.Unmarshal(payload, &body)
	return body.Result.SessionID
}
