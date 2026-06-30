package commanderhub

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/yourorg/multi-agent/internal/commander"
)

var (
	ErrDaemonNotFound = errors.New("commanderhub: daemon not found for this owner")
	ErrDaemonGone     = errors.New("commanderhub: daemon disconnected")
)

// DaemonError carries a daemon-originated error envelope (commander.ErrCode*).
// SendCommand returns it (instead of a flattened fmt error) so HTTP handlers
// can map specific codes to statuses — e.g. session_not_found → 404.
type DaemonError struct {
	Code    string
	Message string
}

func (e *DaemonError) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

const (
	defaultCmdTimeout  = 10 * time.Second
	defaultTurnTimeout = 10 * time.Minute // safety max after browser/SSE disconnect
)

// SendCommand runs a non-streaming command (list_sessions / get_session) on one
// daemon and returns the command_result payload. ErrDaemonNotFound → caller 404.
func (h *Hub) SendCommand(ctx context.Context, o owner, daemonID, command string, args json.RawMessage) (json.RawMessage, error) {
	dc, ok := h.reg.lookup(o, daemonID)
	if !ok {
		return nil, ErrDaemonNotFound
	}
	if !dc.confirmOwnership(ctx) {
		return nil, ErrDaemonGone
	}
	select {
	case <-dc.done:
		return nil, ErrDaemonGone
	default:
	}
	cmdID := h.nextCmdID()
	pe := dc.registerPending(cmdID, false)
	ch := pe.ch
	defer dc.removePending(cmdID)
	if err := dc.writeEnvelope(commandEnvelope(cmdID, command, args)); err != nil {
		return nil, ErrDaemonGone
	}
	// ch is never closed (see daemonConn.registerPending): !ok below is dead but
	// kept as defensive code. Disconnect is detected via <-dc.done, cancel via
	// <-ctx.Done(), terminal via the env.Type switch.
	for {
		select {
		case env, ok := <-ch:
			if !ok {
				return nil, ErrDaemonGone // dead: ch is never closed
			}
			switch env.Type {
			case "error":
				return nil, &DaemonError{Code: errorCode(env.Payload), Message: errorMessage(env.Payload)}
			case "command_result":
				return env.Payload, nil
			}
			// event: ignore for non-streaming commands
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-dc.done:
			return nil, ErrDaemonGone
		}
	}
}

// SendCommandStream runs a streaming command (session_turn). Events and the
// terminal command_result/error or terminal status event are forwarded on the
// returned channel, which is closed when the turn ends or the daemon/ctx is done.
func (h *Hub) SendCommandStream(ctx context.Context, o owner, daemonID, command string, args json.RawMessage) (<-chan commander.Envelope, error) {
	dc, ok := h.reg.lookup(o, daemonID)
	if !ok {
		return nil, ErrDaemonNotFound
	}
	if !dc.confirmOwnership(ctx) {
		return nil, ErrDaemonGone
	}
	select {
	case <-dc.done:
		return nil, ErrDaemonGone
	default:
	}
	cmdID := h.nextCmdID()
	pe := dc.registerPending(cmdID, true)
	ch := pe.ch
	if err := dc.writeEnvelope(commandEnvelope(cmdID, command, args)); err != nil {
		dc.removePending(cmdID)
		return nil, ErrDaemonGone
	}
	out := make(chan commander.Envelope, 16)
	go func() {
		defer close(out)
		defer dc.removePending(cmdID)
		// ch is never closed (see daemonConn.registerPending): !ok below is dead
		// but kept defensively. Disconnect via <-dc.done, cancel via <-ctx.Done().
		for {
			select {
			case env, ok := <-ch:
				if !ok {
					return // dead: ch is never closed
				}
				select {
				case out <- env:
				case <-ctx.Done():
					return
				case <-dc.done:
					return
				}
				if isTerminalStreamEnvelope(env) {
					return
				}
			case <-ctx.Done():
				return
			case <-dc.done:
				return
			}
		}
	}()
	return out, nil
}

func (h *Hub) ListFiles(ctx context.Context, o owner, daemonID, sessionID, path string) (json.RawMessage, error) {
	args, _ := json.Marshal(commander.FileListArgs{ID: sessionID, Path: path})
	return h.SendCommand(ctx, o, daemonID, "list_files", args)
}

func (h *Hub) ReadFile(ctx context.Context, o owner, daemonID, sessionID, path string) (json.RawMessage, error) {
	args, _ := json.Marshal(commander.FileReadArgs{ID: sessionID, Path: path})
	return h.SendCommand(ctx, o, daemonID, "read_file", args)
}

// DaemonSessions is one row of the fan-out GET /sessions result.
type DaemonSessions struct {
	DaemonID    string           `json:"daemon_id"`
	DisplayName string           `json:"display_name"`
	Kind        string           `json:"kind"`
	Status      string           `json:"status"` // ok|timeout|error|disconnected
	Error       string           `json:"error,omitempty"`
	Sessions    []map[string]any `json:"sessions,omitempty"`
}

// FanOutSessions concurrently asks every online daemon of this owner for its
// sessions, each under defaultCmdTimeout. Slow/dead daemons surface a per-row
// status and do not block the rest (fail-open).
func (h *Hub) FanOutSessions(ctx context.Context, o owner) []DaemonSessions {
	snapshot := h.reg.daemons(o)
	results := make([]DaemonSessions, len(snapshot))
	var wg sync.WaitGroup
	for i := range snapshot {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			results[i] = h.oneDaemonSessions(ctx, o, snapshot[i])
		}()
	}
	wg.Wait()
	return results
}

func (h *Hub) oneDaemonSessions(ctx context.Context, o owner, di DaemonInfo) DaemonSessions {
	row := DaemonSessions{DaemonID: di.DaemonID, DisplayName: di.DisplayName, Kind: di.Kind}
	cctx, cancel := context.WithTimeout(ctx, defaultCmdTimeout)
	defer cancel()
	payload, err := h.SendCommand(cctx, o, di.DaemonID, "list_sessions", nil)
	switch {
	case errors.Is(err, ErrDaemonNotFound):
		row.Status = "disconnected"
	case errors.Is(err, context.DeadlineExceeded):
		row.Status, row.Error = "timeout", "context deadline exceeded"
	case err != nil:
		row.Status, row.Error = "error", err.Error()
	default:
		row.Status = "ok"
		var body struct {
			Sessions []map[string]any `json:"sessions"`
		}
		_ = json.Unmarshal(payload, &body)
		row.Sessions = body.Sessions
	}
	return row
}

// --- helpers ---

func commandEnvelope(cmdID, command string, args json.RawMessage) commander.Envelope {
	payload, _ := json.Marshal(commander.CommandPayload{Command: command, Args: args})
	return commander.Envelope{Type: "command", ID: cmdID, Payload: payload}
}

func errorCode(payload []byte) string {
	var ep commander.ErrorPayload
	_ = json.Unmarshal(payload, &ep)
	return ep.Code
}

// errorMessage decodes commander.ErrorPayload.Message (mirrors errorCode).
func errorMessage(payload []byte) string {
	var ep commander.ErrorPayload
	_ = json.Unmarshal(payload, &ep)
	return ep.Message
}
