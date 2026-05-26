// Package humanloop wires the slave's chat backend to the user at the driver:
// when the backend's stdio MCP server "humanloop" receives an ask_user /
// request_permission tool call, it forwards the payload over a unix socket
// to the slave's chat executor, which pauses the conversation.
package humanloop

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

// Payload is what humanloop server sends to the executor when the model calls
// ask_user or request_permission. It mirrors AskUserPayload in
// internal/executor; keeping them as two types avoids a cyclic import.
type Payload struct {
	Kind     string   `json:"kind"`              // "ask_user" | "request_permission"
	Question string   `json:"question,omitempty"`
	Options  []string `json:"options,omitempty"`
	Context  string   `json:"context,omitempty"`
	Intent   string   `json:"intent,omitempty"`
	Target   string   `json:"target,omitempty"`
	Reason   string   `json:"reason,omitempty"`
}

// IPCServer listens on a unix socket for a single Payload from the humanloop
// MCP subcommand and then closes.
type IPCServer struct {
	ln   net.Listener
	path string
}

func ListenIPC(path string) (*IPCServer, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	_ = os.Remove(path) // best-effort: drop stale socket
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("humanloop listen %s: %w", path, err)
	}
	return &IPCServer{ln: ln, path: path}, nil
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
	_ = os.Remove(s.path)
	return err
}

// IPCClient dials the executor's socket and sends one Payload.
type IPCClient struct {
	conn net.Conn
}

func DialIPC(path string) (*IPCClient, error) {
	c, err := net.Dial("unix", path)
	if err != nil {
		return nil, fmt.Errorf("humanloop dial %s: %w", path, err)
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
