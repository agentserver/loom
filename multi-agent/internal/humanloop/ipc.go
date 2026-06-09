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

const preauthReadTimeout = 250 * time.Millisecond

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
		p, ok, err := s.receiveOne()
		if err != nil {
			return Payload{}, err
		}
		if ok {
			return p, nil
		}
	}
}

func (s *IPCServer) receiveOne() (Payload, bool, error) {
	conn, err := s.ln.Accept()
	if err != nil {
		return Payload{}, false, fmt.Errorf("humanloop accept: %w", err)
	}
	defer conn.Close()
	if s.secret != "" {
		_ = conn.SetReadDeadline(time.Now().Add(preauthReadTimeout))
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		var netErr net.Error
		if s.secret != "" && errors.As(err, &netErr) && netErr.Timeout() {
			return Payload{}, false, nil
		}
		return Payload{}, false, fmt.Errorf("humanloop read: %w", err)
	}
	if s.secret != "" {
		var msg ipcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			return Payload{}, false, nil
		}
		if msg.Secret != s.secret {
			return Payload{}, false, nil
		}
		return msg.Payload, true, nil
	}
	var p Payload
	if err := json.Unmarshal(line, &p); err != nil {
		return Payload{}, false, fmt.Errorf("humanloop unmarshal: %w", err)
	}
	return p, true, nil
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
