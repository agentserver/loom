// Package humanloop wires the slave's chat backend to the user at the driver:
// when the backend's stdio MCP server "humanloop" receives an ask_user /
// request_permission tool call, it forwards the payload over an IPC endpoint
// to the slave's chat executor, which pauses the conversation.
package humanloop

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"strings"
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
}

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
}

func ListenIPC(baseDir string) (*IPCServer, Endpoint, error) {
	return listenIPC(baseDir)
}

func (s *IPCServer) Receive() (Payload, error) {
	conn, err := s.ln.Accept()
	if err != nil {
		return Payload{}, fmt.Errorf("humanloop accept: %w", err)
	}
	defer conn.Close()
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return Payload{}, fmt.Errorf("humanloop read: %w", err)
	}
	var p Payload
	if err := json.Unmarshal(line, &p); err != nil {
		return Payload{}, fmt.Errorf("humanloop unmarshal: %w", err)
	}
	return p, nil
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
	conn net.Conn
}

func DialIPC(ep Endpoint) (*IPCClient, error) {
	if ep.Network == "" {
		return nil, fmt.Errorf("humanloop endpoint network is empty")
	}
	if ep.Address == "" {
		return nil, fmt.Errorf("humanloop endpoint address is empty")
	}
	c, err := net.Dial(ep.Network, ep.Address)
	if err != nil {
		return nil, fmt.Errorf("humanloop dial %s %s: %w", ep.Network, ep.Address, err)
	}
	return &IPCClient{conn: c}, nil
}

func (c *IPCClient) Send(p Payload) error {
	b, err := json.Marshal(p)
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
