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

// ErrSchemaVersionMismatch is returned when observer rejects this daemon's
// protocol version. It is terminal: reconnecting cannot repair a schema split.
var ErrSchemaVersionMismatch = errors.New("commander: schema version mismatch")

// WSConfig is the dial and behavior config for WSClient.
type WSConfig struct {
	URL            string
	ProxyToken     string
	Register       RegisterPayload
	Handler        *Handler
	HeartbeatInt   time.Duration
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

// WSClient owns the outbound WebSocket link to observer.
type WSClient struct {
	cfg WSConfig

	mu     sync.Mutex
	linked bool
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
	return &WSClient{cfg: cfg}
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
		err := c.runOnce(ctx)
		c.setLinked(false)
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, ErrSchemaVersionMismatch) {
			return err
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil
		}
		backoff *= 2
		if backoff > c.cfg.MaxBackoff {
			backoff = c.cfg.MaxBackoff
		}
	}
	return nil
}

func (c *WSClient) runOnce(ctx context.Context) error {
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+c.cfg.ProxyToken)

	dialer := *websocket.DefaultDialer
	conn, _, err := dialer.DialContext(ctx, c.cfg.URL, hdr)
	if err != nil {
		return err
	}
	defer conn.Close()

	connDone := make(chan struct{})
	defer close(connDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-connDone:
		}
	}()

	var writeMu sync.Mutex
	write := func(env Envelope) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteJSON(env)
	}

	registerPayload, _ := json.Marshal(c.cfg.Register)
	if err := write(Envelope{Type: "register", Payload: registerPayload}); err != nil {
		return err
	}
	c.setLinked(true)

	heartbeatDone := make(chan struct{})
	defer close(heartbeatDone)
	go func() {
		ticker := time.NewTicker(c.cfg.HeartbeatInt)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_ = write(Envelope{Type: "heartbeat"})
			case <-heartbeatDone:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		var env Envelope
		if err := conn.ReadJSON(&env); err != nil {
			return err
		}
		switch env.Type {
		case "command":
			go c.dispatchCommand(ctx, env, write)
		case "ping":
			_ = write(Envelope{Type: "heartbeat"})
		case "error":
			if isSchemaMismatch(env.Payload) {
				return ErrSchemaVersionMismatch
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

	case "session_turn":
		var args SessionTurnArgs
		if err := json.Unmarshal(cmd.Args, &args); err != nil {
			_ = write(errorEnvelope(env.ID, ErrCodeInternal, "bad session_turn args: "+err.Error()))
			return
		}
		sink := newWSSink(env.ID, write)
		result, err := c.cfg.Handler.SessionTurn(ctx, args.ID, args.Prompt, sink)
		if errors.Is(err, agentbackend.ErrSessionNotFound) {
			_ = write(errorEnvelope(env.ID, ErrCodeSessionNotFound, "session not found"))
			return
		}
		if err != nil {
			_ = write(errorEnvelope(env.ID, ErrCodeBackendUnavailable, err.Error()))
			return
		}
		_ = write(Envelope{Type: "command_result", ID: env.ID, Payload: mustMarshalTurnResult(result)})

	default:
		_ = write(errorEnvelope(env.ID, ErrCodeInternal, fmt.Sprintf("unknown command: %s", cmd.Command)))
	}
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
