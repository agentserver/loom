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
	cmd.Env = cmd.Environ()
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

// ---------- http ----------

func (e *MCPExecutor) callHTTP(ctx context.Context, cfg MCPServerCfg, tool string, args map[string]interface{}) (json.RawMessage, error) {
	body, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]interface{}{"name": tool, "arguments": args},
	})
	req, err := http.NewRequestWithContext(ctx, "POST", cfg.URL, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range cfg.Headers {
		req.Header.Set(k, v)
	}
	resp, err := e.httpCli.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mcp http: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("mcp http %d: %s", resp.StatusCode, string(raw))
	}
	var rpc struct {
		Result json.RawMessage `json:"result"`
		Error  *struct{ Message string } `json:"error"`
	}
	if err := json.Unmarshal(raw, &rpc); err != nil {
		return nil, fmt.Errorf("mcp http parse: %w", err)
	}
	if rpc.Error != nil {
		return nil, fmt.Errorf("%s", rpc.Error.Message)
	}
	return rpc.Result, nil
}

// CallTool is the adapter used by the webui's /bridge/call handler to let
// generated MCP servers re-enter the slave's MCP executor (compose existing
// tools). It dispatches to callStdio or callHTTP depending on the named
// server's transport.
func (e *MCPExecutor) CallTool(ctx context.Context, server, tool string, args map[string]interface{}) (json.RawMessage, error) {
	e.mu.Lock()
	cfg, ok := e.cfg[server]
	e.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("unknown mcp server: %s", server)
	}
	switch cfg.Transport {
	case "stdio":
		return e.callStdio(ctx, server, cfg, tool, args)
	case "http":
		return e.callHTTP(ctx, cfg, tool, args)
	default:
		return nil, fmt.Errorf("unsupported transport: %s", cfg.Transport)
	}
}

// RegisterStdio adds (or replaces) a stdio MCP server entry at runtime.
// If a server with this name already exists, its subprocess is killed and
// removed before the new entry is installed; subsequent calls will spawn
// the new subprocess on demand. Only "stdio" transport is accepted.
func (e *MCPExecutor) RegisterStdio(name string, cfg MCPServerCfg) error {
	if cfg.Transport != "stdio" {
		return fmt.Errorf("RegisterStdio: transport must be stdio, got %q", cfg.Transport)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if old, ok := e.stdios[name]; ok {
		old.kill()
		delete(e.stdios, name)
	}
	e.cfg[name] = cfg
	return nil
}

// Servers returns the names of all currently-registered MCP servers (both
// static config + any RegisterStdio'd at runtime). Used by the slave-agent
// main's Republish callback to re-enumerate tools after build_mcp adds a
// new server.
func (e *MCPExecutor) Servers() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, 0, len(e.cfg))
	for name := range e.cfg {
		out = append(out, name)
	}
	return out
}

// ListTools returns the tool names exposed by the named server, by issuing
// one tools/list JSON-RPC call. The server must be registered (in cfg) at
// call time. Spawns the subprocess if it is not yet running.
func (e *MCPExecutor) ListTools(ctx context.Context, name string) ([]string, error) {
	e.mu.Lock()
	cfg, ok := e.cfg[name]
	e.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("unknown mcp server: %s", name)
	}
	if cfg.Transport != "stdio" {
		return nil, fmt.Errorf("ListTools: only stdio supported, got %q", cfg.Transport)
	}
	c, err := e.connStdio(name, cfg)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	id := atomic.AddInt64(&c.nextID, 1)
	req := map[string]interface{}{
		"jsonrpc": "2.0", "id": id, "method": "tools/list",
		"params": map[string]interface{}{},
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
		b, rerr := c.stdout.ReadBytes('\n')
		ch <- doneT{b, rerr}
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
		var raw struct {
			Result struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			} `json:"result"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(d.b, &raw); err != nil {
			return nil, fmt.Errorf("mcp parse: %w", err)
		}
		if raw.Error != nil {
			return nil, fmt.Errorf("%s", raw.Error.Message)
		}
		out := make([]string, 0, len(raw.Result.Tools))
		for _, t := range raw.Result.Tools {
			out = append(out, t.Name)
		}
		return out, nil
	}
}
