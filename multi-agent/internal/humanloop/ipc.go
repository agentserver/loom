// Package humanloop wires the slave's chat backend to the user at the driver:
// when the backend's stdio MCP server "humanloop" receives an ask_user /
// request_permission tool call, it forwards the payload over an IPC endpoint
// to the slave's chat executor, which pauses the conversation.
package humanloop

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// Payload is what humanloop server sends to the executor when the model calls
// ask_user or request_permission. It mirrors AskUserPayload in
// internal/executor; keeping them as two types avoids a cyclic import.
type Payload struct {
	Kind     string   `json:"kind"` // "ask_user" | "request_permission"
	Question string   `json:"question,omitempty"`
	Options  []string `json:"options,omitempty"`
	Context  string   `json:"context,omitempty"`
	Intent   string   `json:"intent,omitempty"`
	Target   string   `json:"target,omitempty"`
	Reason   string   `json:"reason,omitempty"`
}

type Endpoint struct {
	Network string `json:"network"`
	Address string `json:"address"`
	Secret  string `json:"secret,omitempty"`
}

type ipcMessage struct {
	Secret  string  `json:"secret,omitempty"`
	Payload Payload `json:"payload"`
}

type ipcAck struct {
	Status string `json:"status"`
}

const (
	preauthReadTimeout = 250 * time.Millisecond
	ackTimeout         = time.Second
)

func EndpointArg(ep Endpoint) string {
	b, _ := json.Marshal(ep)
	return string(b)
}

func ParseEndpointArg(arg string) (Endpoint, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return Endpoint{}, fmt.Errorf("humanloop endpoint is empty")
	}
	if strings.HasPrefix(arg, "{") {
		var ep Endpoint
		if err := json.Unmarshal([]byte(arg), &ep); err != nil {
			return Endpoint{}, fmt.Errorf("parse humanloop endpoint: %w", err)
		}
		if err := validateEndpoint(ep); err != nil {
			return Endpoint{}, err
		}
		return ep, nil
	}
	return Endpoint{Network: "unix", Address: arg}, nil
}

func validateEndpoint(ep Endpoint) error {
	if ep.Network == "" {
		return fmt.Errorf("humanloop endpoint network is empty")
	}
	if ep.Address == "" {
		return fmt.Errorf("humanloop endpoint address is empty")
	}
	switch ep.Network {
	case "unix":
		return nil
	case "tcp":
		host, _, err := net.SplitHostPort(ep.Address)
		if err != nil {
			return fmt.Errorf("humanloop tcp endpoint address %q: %w", ep.Address, err)
		}
		if ep.Secret == "" {
			return fmt.Errorf("humanloop tcp endpoint secret is empty")
		}
		if strings.EqualFold(host, "localhost") {
			return nil
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("humanloop tcp endpoint host %q is not loopback", host)
		}
		return nil
	default:
		return fmt.Errorf("humanloop endpoint network %q is unsupported", ep.Network)
	}
}

// IPCServer listens for a single Payload from the humanloop
// MCP subcommand and then closes.
type IPCServer struct {
	ln      net.Listener
	cleanup func()
	secret  string
}

type ReceivedPayload struct {
	Payload Payload

	conn net.Conn
	once sync.Once
	err  error
}

func ListenIPC(baseDir string) (*IPCServer, Endpoint, error) {
	srv, ep, err := listenIPC(baseDir)
	if err != nil {
		return nil, Endpoint{}, err
	}
	secret, err := newIPCSecret()
	if err != nil {
		_ = srv.Close()
		return nil, Endpoint{}, err
	}
	ep.Secret = secret
	srv.secret = secret
	return srv, ep, nil
}

func (s *IPCServer) Receive() (Payload, error) {
	for {
		pending, ok, err := s.receiveOnePending()
		if err != nil {
			return Payload{}, err
		}
		if ok {
			_ = pending.Ack()
			return pending.Payload, nil
		}
	}
}

func (s *IPCServer) ReceivePending() (*ReceivedPayload, error) {
	for {
		pending, ok, err := s.receiveOnePending()
		if err != nil {
			return nil, err
		}
		if ok {
			return pending, nil
		}
	}
}

func (s *IPCServer) receiveOnePending() (*ReceivedPayload, bool, error) {
	conn, err := s.ln.Accept()
	if err != nil {
		return nil, false, fmt.Errorf("humanloop accept: %w", err)
	}
	if s.secret != "" {
		_ = conn.SetReadDeadline(time.Now().Add(preauthReadTimeout))
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		_ = conn.Close()
		var netErr net.Error
		if s.secret != "" && errors.As(err, &netErr) && netErr.Timeout() {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("humanloop read: %w", err)
	}
	if s.secret != "" {
		var msg ipcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			_ = conn.Close()
			return nil, false, nil
		}
		if msg.Secret != s.secret {
			_ = conn.Close()
			return nil, false, nil
		}
		_ = conn.SetReadDeadline(time.Time{})
		return &ReceivedPayload{Payload: msg.Payload, conn: conn}, true, nil
	}
	var p Payload
	if err := json.Unmarshal(line, &p); err != nil {
		_ = conn.Close()
		return nil, false, fmt.Errorf("humanloop unmarshal: %w", err)
	}
	return &ReceivedPayload{Payload: p, conn: conn}, true, nil
}

func (p *ReceivedPayload) Ack() error {
	if p == nil || p.conn == nil {
		return nil
	}
	p.once.Do(func() {
		p.err = p.ack()
	})
	return p.err
}

func (p *ReceivedPayload) ack() error {
	_ = p.conn.SetWriteDeadline(time.Now().Add(ackTimeout))
	_, writeErr := p.conn.Write([]byte(`{"status":"ok"}` + "\n"))
	closeErr := p.conn.Close()
	if writeErr != nil {
		return fmt.Errorf("humanloop ack: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("humanloop ack close: %w", closeErr)
	}
	return nil
}

func (s *IPCServer) Close() error {
	err := s.ln.Close()
	if s.cleanup != nil {
		s.cleanup()
	}
	return err
}

// IPCClient dials the executor's endpoint and sends one Payload.
type IPCClient struct {
	conn   net.Conn
	secret string
}

func DialIPC(ep Endpoint) (*IPCClient, error) {
	if err := validateEndpoint(ep); err != nil {
		return nil, err
	}
	c, err := net.Dial(ep.Network, ep.Address)
	if err != nil {
		return nil, fmt.Errorf("humanloop dial %s %s: %w", ep.Network, ep.Address, err)
	}
	return &IPCClient{conn: c, secret: ep.Secret}, nil
}

func (c *IPCClient) Send(p Payload) error {
	var body any = p
	if c.secret != "" {
		body = ipcMessage{Secret: c.secret, Payload: p}
	}
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if _, err := c.conn.Write(b); err != nil {
		return fmt.Errorf("humanloop send: %w", err)
	}
	if err := c.conn.SetReadDeadline(time.Now().Add(ackTimeout)); err != nil {
		return fmt.Errorf("humanloop ack deadline: %w", err)
	}
	line, err := bufio.NewReader(c.conn).ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("humanloop ack read: %w", err)
	}
	var ack ipcAck
	if err := json.Unmarshal(line, &ack); err != nil {
		return fmt.Errorf("humanloop ack decode: %w", err)
	}
	if ack.Status != "ok" {
		return fmt.Errorf("humanloop ack status %q", ack.Status)
	}
	return nil
}

func (c *IPCClient) Close() error { return c.conn.Close() }

func newIPCSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("humanloop secret: %w", err)
	}
	return hex.EncodeToString(b), nil
}
