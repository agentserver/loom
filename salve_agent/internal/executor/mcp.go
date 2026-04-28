package executor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
)

type MCPServerCfg struct {
	Transport string
	// stdio
	Command string
	Args    []string
	Env     map[string]string
	// http
	URL     string
	Headers map[string]string
}

type MCPExecutor struct {
	cfg     map[string]MCPServerCfg
	mu      sync.Mutex
	stdios  map[string]*stdioConn
	httpCli *http.Client
}

func NewMCPExecutor(cfg map[string]MCPServerCfg) *MCPExecutor {
	return &MCPExecutor{
		cfg:     cfg,
		stdios:  make(map[string]*stdioConn),
		httpCli: &http.Client{},
	}
}

func (e *MCPExecutor) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for name, s := range e.stdios {
		s.kill()
		delete(e.stdios, name)
	}
}

type mcpPrompt struct {
	Server string                 `json:"server"`
	Tool   string                 `json:"tool"`
	Args   map[string]interface{} `json:"args"`
}

type mcpToolResult struct {
	Result            json.RawMessage `json:"result"`
	CapabilityChanged bool            `json:"capability_changed"`
	ChangeHint        string          `json:"change_hint"`
}

func (e *MCPExecutor) Run(ctx context.Context, t Task, sink Sink) (Result, error) {
	defer sink.Close()
	var p mcpPrompt
	if err := json.Unmarshal([]byte(t.Prompt), &p); err != nil {
		return Result{}, fmt.Errorf("mcp prompt must be JSON: %w", err)
	}
	if p.Server == "" || p.Tool == "" {
		return Result{}, fmt.Errorf("missing server or tool")
	}
	cfg, ok := e.cfg[p.Server]
	if !ok {
		return Result{}, fmt.Errorf("unknown mcp server: %s", p.Server)
	}

	var raw json.RawMessage
	var err error
	switch cfg.Transport {
	case "stdio":
		raw, err = e.callStdio(ctx, p.Server, cfg, p.Tool, p.Args)
	case "http":
		raw, err = e.callHTTP(ctx, cfg, p.Tool, p.Args)
	default:
		return Result{}, fmt.Errorf("unsupported transport: %s", cfg.Transport)
	}
	if err != nil {
		return Result{}, err
	}

	var tr mcpToolResult
	if err := json.Unmarshal(raw, &tr); err != nil || len(tr.Result) == 0 {
		return Result{}, fmt.Errorf("malformed mcp response")
	}

	summary := stringifyResult(tr.Result)
	change := ""
	if tr.CapabilityChanged {
		change = tr.ChangeHint
		if change == "" {
			change = "unspecified"
		}
	}
	return Result{Summary: summary, CapabilityChange: change}, nil
}

func stringifyResult(r json.RawMessage) string {
	var s string
	if json.Unmarshal(r, &s) == nil {
		return s
	}
	return string(r)
}

// ---------- stdio ----------

type stdioConn struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	nextID int64
	mu     sync.Mutex
}

func (s *stdioConn) kill() {
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_ = s.cmd.Wait()
	}
}

func (e *MCPExecutor) connStdio(name string, cfg MCPServerCfg) (*stdioConn, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if c, ok := e.stdios[name]; ok && c.cmd.ProcessState == nil {
		return c, nil
	}
	cmd := exec.Command(cfg.Command, cfg.Args...)
	for k, v := range cfg.Env {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &stdioConn{cmd: cmd, stdin: in, stdout: bufio.NewReaderSize(out, 1<<20)}
	e.stdios[name] = c
	return c, nil
}

func (e *MCPExecutor) callStdio(ctx context.Context, name string, cfg MCPServerCfg, tool string, args map[string]interface{}) (json.RawMessage, error) {
	c, err := e.connStdio(name, cfg)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	id := atomic.AddInt64(&c.nextID, 1)
	req := map[string]interface{}{
		"jsonrpc": "2.0", "id": id, "method": "tools/call",
		"params": map[string]interface{}{"name": tool, "arguments": args},
	}
	line, _ := json.Marshal(req)
	if _, err := c.stdin.Write(append(line, '\n')); err != nil {
		c.kill()
		e.mu.Lock()
		delete(e.stdios, name)
		e.mu.Unlock()
		return nil, fmt.Errorf("mcp stdio write: %w", err)
	}

	type doneT struct {
		b   []byte
		err error
	}
	ch := make(chan doneT, 1)
	go func() {
		b, err := c.stdout.ReadBytes('\n')
		ch <- doneT{b, err}
	}()

	select {
	case <-ctx.Done():
		c.kill()
		e.mu.Lock()
		delete(e.stdios, name)
		e.mu.Unlock()
		return nil, fmt.Errorf("timeout")
	case d := <-ch:
		if d.err != nil {
			c.kill()
			e.mu.Lock()
			delete(e.stdios, name)
			e.mu.Unlock()
			return nil, fmt.Errorf("mcp stdio read: %w", d.err)
		}
		var resp struct {
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(d.b, &resp); err != nil {
			return nil, fmt.Errorf("mcp parse: %w", err)
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("%s", resp.Error.Message)
		}
		return resp.Result, nil
	}
}

// ---------- http (placeholder, fleshed out in Task 14) ----------

func (e *MCPExecutor) callHTTP(ctx context.Context, cfg MCPServerCfg, tool string, args map[string]interface{}) (json.RawMessage, error) {
	return nil, fmt.Errorf("http transport not yet implemented")
}

// suppress unused import warnings if any
var _ = strings.NewReader
