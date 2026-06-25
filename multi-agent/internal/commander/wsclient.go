package commander

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

const (
	wsReadLimitBytes = int64(1 << 20)
	wsWriteWait      = 5 * time.Second
)

// ErrSchemaVersionMismatch is returned when observer rejects this daemon's
// protocol version. It is terminal: reconnecting cannot repair a schema split.
var ErrSchemaVersionMismatch = errors.New("commander: schema version mismatch")

// ErrObserverUnauthorized is returned when observer rejects the WebSocket
// handshake with 401 or 403. Retrying cannot repair an invalid token.
var ErrObserverUnauthorized = errors.New("commander: observer unauthorized")

// WSConfig is the dial and behavior config for WSClient.
type WSConfig struct {
	URL               string
	ProxyToken        string
	Register          RegisterPayload
	Handler           *Handler
	HeartbeatInt      time.Duration
	InitialBackoff    time.Duration
	MaxBackoff        time.Duration
	BackoffResetAfter time.Duration
}

// WSClient owns the outbound WebSocket link to observer.
type WSClient struct {
	cfg WSConfig

	mu     sync.Mutex
	linked bool

	turnMu    sync.Mutex
	turnLocks map[string]*turnLock
}

type turnLock struct {
	mu   sync.Mutex
	refs int
}

func NewWSClient(cfg WSConfig) *WSClient {
	if cfg.HeartbeatInt == 0 {
		cfg.HeartbeatInt = 30 * time.Second
	}
	if cfg.InitialBackoff == 0 {
		cfg.InitialBackoff = time.Second
	}
	if cfg.MaxBackoff == 0 {
		cfg.MaxBackoff = 30 * time.Second
	}
	if cfg.BackoffResetAfter == 0 {
		cfg.BackoffResetAfter = time.Minute
	}
	return &WSClient{cfg: cfg, turnLocks: make(map[string]*turnLock)}
}

func (c *WSClient) Linked() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.linked
}

func (c *WSClient) setLinked(v bool) {
	c.mu.Lock()
	c.linked = v
	c.mu.Unlock()
}

// Run keeps the observer link alive until ctx is cancelled or a terminal
// protocol error is received.
func (c *WSClient) Run(ctx context.Context) error {
	backoff := c.cfg.InitialBackoff
	for ctx.Err() == nil {
		connectedFor, err := c.runOnce(ctx)
		c.setLinked(false)
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, ErrSchemaVersionMismatch) || errors.Is(err, ErrObserverUnauthorized) {
			return err
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil
		}
		backoff = nextReconnectBackoff(backoff, c.cfg.InitialBackoff, c.cfg.MaxBackoff, connectedFor, c.cfg.BackoffResetAfter)
	}
	return nil
}

func (c *WSClient) runOnce(ctx context.Context) (time.Duration, error) {
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+c.cfg.ProxyToken)

	dialer := *websocket.DefaultDialer
	conn, resp, err := dialer.DialContext(ctx, c.cfg.URL, hdr)
	if err != nil {
		if resp != nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
			return 0, fmt.Errorf("%w: status %d", ErrObserverUnauthorized, resp.StatusCode)
		}
		return 0, err
	}
	defer conn.Close()
	conn.SetReadLimit(wsReadLimitBytes)

	connCtx, connCancel := context.WithCancel(ctx)
	defer connCancel()

	go func() {
		<-connCtx.Done()
		_ = conn.Close()
	}()

	var writeMu sync.Mutex
	readTimeout := wsReadTimeout(c.cfg.HeartbeatInt)
	refreshReadDeadline := func() error {
		return conn.SetReadDeadline(time.Now().Add(readTimeout))
	}
	if err := refreshReadDeadline(); err != nil {
		return 0, err
	}
	conn.SetPongHandler(func(string) error {
		return refreshReadDeadline()
	})
	conn.SetPingHandler(func(appData string) error {
		if err := refreshReadDeadline(); err != nil {
			return err
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(wsWriteWait))
	})

	write := func(env Envelope) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		if err := conn.SetWriteDeadline(time.Now().Add(wsWriteWait)); err != nil {
			connCancel()
			return err
		}
		err := conn.WriteJSON(env)
		if err != nil {
			connCancel()
		}
		return err
	}
	writeControl := func(messageType int, data []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		err := conn.WriteControl(messageType, data, time.Now().Add(wsWriteWait))
		if err != nil {
			connCancel()
		}
		return err
	}

	registerPayload, _ := json.Marshal(c.cfg.Register)
	if err := write(Envelope{Type: "register", Payload: registerPayload}); err != nil {
		return 0, err
	}
	connectedAt := time.Now()

	go func() {
		ticker := time.NewTicker(c.cfg.HeartbeatInt)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := writeControl(websocket.PingMessage, nil); err != nil {
					return
				}
				_ = write(Envelope{Type: "heartbeat"})
			case <-connCtx.Done():
				return
			}
		}
	}()

	for {
		var env Envelope
		if err := conn.ReadJSON(&env); err != nil {
			connCancel()
			return time.Since(connectedAt), err
		}
		if err := refreshReadDeadline(); err != nil {
			connCancel()
			return time.Since(connectedAt), err
		}
		switch env.Type {
		case "ack":
			c.setLinked(true)
		case "command":
			go c.dispatchCommand(connCtx, env, write)
		case "ping":
			_ = write(Envelope{Type: "heartbeat"})
		case "error":
			if isSchemaMismatch(env.Payload) {
				return time.Since(connectedAt), ErrSchemaVersionMismatch
			}
		}
	}
}

func (c *WSClient) dispatchCommand(ctx context.Context, env Envelope, write func(Envelope) error) {
	var cmd CommandPayload
	if err := json.Unmarshal(env.Payload, &cmd); err != nil {
		_ = write(errorEnvelope(env.ID, ErrCodeInternal, "bad command payload: "+err.Error()))
		return
	}

	switch cmd.Command {
	case "list_sessions":
		sessions, err := c.cfg.Handler.ListSessions(ctx)
		if err != nil {
			_ = write(errorEnvelope(env.ID, ErrCodeBackendUnavailable, err.Error()))
			return
		}
		payload, _ := json.Marshal(map[string]any{"sessions": sessions})
		_ = write(Envelope{Type: "command_result", ID: env.ID, Payload: payload})

	case "get_session":
		var args GetSessionArgs
		if err := json.Unmarshal(cmd.Args, &args); err != nil {
			_ = write(errorEnvelope(env.ID, ErrCodeInternal, "bad get_session args: "+err.Error()))
			return
		}
		session, messages, err := c.cfg.Handler.GetSession(ctx, args.ID)
		if errors.Is(err, agentbackend.ErrSessionNotFound) {
			_ = write(errorEnvelope(env.ID, ErrCodeSessionNotFound, "session not found"))
			return
		}
		if err != nil {
			_ = write(errorEnvelope(env.ID, ErrCodeBackendUnavailable, err.Error()))
			return
		}
		payload, _ := json.Marshal(map[string]any{"session": session, "messages": messages})
		_ = write(Envelope{Type: "command_result", ID: env.ID, Payload: payload})

	case "list_files":
		var args FileListArgs
		if err := json.Unmarshal(cmd.Args, &args); err != nil {
			_ = write(errorEnvelope(env.ID, ErrCodeInternal, "bad list_files args: "+err.Error()))
			return
		}
		result, err := c.cfg.Handler.ListFiles(ctx, args.ID, args.Path)
		if errors.Is(err, agentbackend.ErrSessionNotFound) {
			_ = write(errorEnvelope(env.ID, ErrCodeSessionNotFound, "session not found"))
			return
		}
		if errors.Is(err, errFileRequest) {
			_ = write(errorEnvelope(env.ID, ErrCodeInvalidRequest, err.Error()))
			return
		}
		if err != nil {
			_ = write(errorEnvelope(env.ID, ErrCodeBackendUnavailable, err.Error()))
			return
		}
		payload, _ := json.Marshal(result)
		_ = write(Envelope{Type: "command_result", ID: env.ID, Payload: payload})

	case "read_file":
		var args FileReadArgs
		if err := json.Unmarshal(cmd.Args, &args); err != nil {
			_ = write(errorEnvelope(env.ID, ErrCodeInternal, "bad read_file args: "+err.Error()))
			return
		}
		result, err := c.cfg.Handler.ReadFile(ctx, args.ID, args.Path)
		if errors.Is(err, agentbackend.ErrSessionNotFound) {
			_ = write(errorEnvelope(env.ID, ErrCodeSessionNotFound, "session not found"))
			return
		}
		if errors.Is(err, errFileRequest) {
			_ = write(errorEnvelope(env.ID, ErrCodeInvalidRequest, err.Error()))
			return
		}
		if err != nil {
			_ = write(errorEnvelope(env.ID, ErrCodeBackendUnavailable, err.Error()))
			return
		}
		payload, _ := json.Marshal(result)
		_ = write(Envelope{Type: "command_result", ID: env.ID, Payload: payload})

	case "session_turn":
		var args SessionTurnArgs
		if err := json.Unmarshal(cmd.Args, &args); err != nil {
			_ = write(errorEnvelope(env.ID, ErrCodeInternal, "bad session_turn args: "+err.Error()))
			return
		}
		sink := newWSSink(env.ID, write)
		agentbackend.WriteStatus(sink, agentbackend.StatusQueued, "queued on daemon")
		unlock := c.lockTurn(args.ID)
		defer unlock()
		result, err := c.cfg.Handler.SessionTurn(ctx, args.ID, args.Prompt, args.Fresh, sink)
		if errors.Is(err, agentbackend.ErrSessionNotFound) {
			_ = write(errorEnvelope(env.ID, ErrCodeSessionNotFound, "session not found"))
			return
		}
		if err != nil {
			_ = write(errorEnvelope(env.ID, ErrCodeBackendUnavailable, err.Error()))
			return
		}
		_ = write(Envelope{Type: "command_result", ID: env.ID, Payload: marshalTurnResult(result)})

	default:
		_ = write(errorEnvelope(env.ID, ErrCodeInternal, fmt.Sprintf("unknown command: %s", cmd.Command)))
	}
}

func (c *WSClient) lockTurn(sessionID string) func() {
	c.turnMu.Lock()
	lock := c.turnLocks[sessionID]
	if lock == nil {
		lock = &turnLock{}
		c.turnLocks[sessionID] = lock
	}
	lock.refs++
	c.turnMu.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		c.turnMu.Lock()
		lock.refs--
		if lock.refs == 0 && c.turnLocks[sessionID] == lock {
			delete(c.turnLocks, sessionID)
		}
		c.turnMu.Unlock()
	}
}

func wsReadTimeout(heartbeat time.Duration) time.Duration {
	if heartbeat <= 0 {
		return time.Minute
	}
	return 2 * heartbeat
}

func nextReconnectBackoff(current, initial, max, connectedFor, resetAfter time.Duration) time.Duration {
	if resetAfter > 0 && connectedFor >= resetAfter {
		return initial
	}
	next := current * 2
	if next > max {
		return max
	}
	return next
}

func errorEnvelope(id, code, message string) Envelope {
	payload, _ := json.Marshal(ErrorPayload{Code: code, Message: message})
	return Envelope{Type: "error", ID: id, Payload: payload}
}

func isSchemaMismatch(payload json.RawMessage) bool {
	var body ErrorPayload
	if err := json.Unmarshal(payload, &body); err != nil {
		return false
	}
	return body.Code == ErrCodeSchemaVersionMismatch
}
