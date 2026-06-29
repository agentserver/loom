package driver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/yourorg/multi-agent/internal/observerstore"
)

// Tool is what the MCP server dispatches to. Each tool advertises a name,
// description, and JSON-Schema for its arguments.
type Tool interface {
	Name() string
	Description() string
	InputSchema() json.RawMessage
	Call(ctx context.Context, args json.RawMessage) (json.RawMessage, error)
}

// MCPToolError lets a tool return a JSON-RPC -32000 error with a custom message.
// Any other error from Call is also surfaced as -32000.
//
// Category is the optional FailureCategory tag for failure analytics
// (Phase 1/2 A4/A6/D5/D8). It is intentionally not serialized into the
// JSON-RPC wire payload (Error() only returns Message) — the tag is for
// in-process observer/driver reporting, not for the codex parent process.
// The zero value (empty string) maps to observerstore.FailUnknown.
type MCPToolError struct {
	Message  string
	Category observerstore.FailureCategory
}

func (e *MCPToolError) Error() string { return e.Message }

// FailureCategory satisfies observerstore.Categorized so observerstore.CategoryOf
// can extract the tag without an explicit *MCPToolError type assertion.
func (e *MCPToolError) FailureCategory() observerstore.FailureCategory {
	if e == nil {
		return observerstore.FailUnknown
	}
	return e.Category
}

type MCPServer struct {
	tools     map[string]Tool
	toolOrder []string
	writeMu   sync.Mutex
	linesOut  int64
	broken    int32 // set to 1 by writeLine when the writer returns EPIPE/closed-pipe
}

func NewMCPServer(tools []Tool) *MCPServer {
	s := &MCPServer{tools: map[string]Tool{}}
	for _, t := range tools {
		s.tools[t.Name()] = t
		s.toolOrder = append(s.toolOrder, t.Name())
	}
	return s
}

type jsonRPCRequest struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method"`
	Params  json.RawMessage  `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// scannedLine carries one stdin line (or terminal error) from the reader
// goroutine to the Serve main loop, so the main loop can select on ctx.Done()
// rather than block in scanner.Scan().
type scannedLine struct {
	bytes []byte // owned copy, safe to use after the next scan
	err   error  // non-nil on EOF or scanner error; signals the read loop has ended
}

// Serve reads one JSON-RPC message per line from r and writes responses to w.
// Tool calls run concurrently so a long-running tool cannot block later
// requests; response ids let JSON-RPC clients match out-of-order replies.
// Returns when r reaches EOF, ctx is cancelled, or an "exit" notification is
// received. The ctx is propagated into every tool Call so callers that wait
// (e.g. wait_task long-polling) can unwind when the driver shuts down.
//
// Reads happen in a goroutine so the main loop can wake on ctx cancel even
// when stdin is silent (production case: parent codex doesn't close driver's
// stdin on SIGTERM). The reader goroutine still blocks in scanner.Scan() — Go
// has no portable way to interrupt a syscall read on os.Stdin — but it is
// abandoned once Serve returns; the GC reclaims it when the underlying file
// is closed, which happens at process exit.
func (s *MCPServer) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	var wg sync.WaitGroup
	lines := make(chan scannedLine, 1)

	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
		for scanner.Scan() {
			// scanner.Bytes() is reused on next Scan; copy before handing off.
			b := append([]byte(nil), scanner.Bytes()...)
			select {
			case lines <- scannedLine{bytes: b}:
			case <-ctx.Done():
				return
			}
		}
		// Scan() returned false: either EOF (Err()==nil) or a scanner error.
		err := scanner.Err()
		if err == nil {
			err = io.EOF
		}
		select {
		case lines <- scannedLine{err: err}:
		case <-ctx.Done():
		}
	}()

	for {
		if atomic.LoadInt32(&s.broken) == 1 {
			wg.Wait()
			return errors.New("mcp stdout broken pipe")
		}
		select {
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		case ln, ok := <-lines:
			if !ok {
				// reader goroutine exited (ctx done before terminal msg arrived)
				wg.Wait()
				if atomic.LoadInt32(&s.broken) == 1 {
					return errors.New("mcp stdout broken pipe")
				}
				return ctx.Err()
			}
			if ln.err != nil {
				wg.Wait()
				if atomic.LoadInt32(&s.broken) == 1 {
					return errors.New("mcp stdout broken pipe")
				}
				if errors.Is(ln.err, io.EOF) {
					return ctx.Err() // nil unless cancelled
				}
				return ln.err
			}
			if len(ln.bytes) == 0 {
				continue
			}
			var req jsonRPCRequest
			if err := json.Unmarshal(ln.bytes, &req); err != nil {
				s.writeError(w, json.RawMessage(`null`), -32700, "parse error: "+err.Error())
				continue
			}
			if req.Method == "exit" {
				wg.Wait()
				return nil
			}
			s.dispatch(ctx, w, &req, &wg)
		}
	}
}

// WaitForLines is a test helper that blocks until at least n response lines
// have been written. Used so tests don't race the goroutine running Serve.
func (s *MCPServer) WaitForLines(n int) {
	for atomic.LoadInt64(&s.linesOut) < int64(n) {
		// tight spin is fine for short tests
	}
}

func (s *MCPServer) dispatch(ctx context.Context, w io.Writer, req *jsonRPCRequest, wg *sync.WaitGroup) {
	switch req.Method {
	case "initialize":
		s.handleInitialize(w, req)
	case "notifications/initialized":
		// notifications get no response
	case "tools/list":
		s.handleToolsList(w, req)
	case "tools/call":
		s.handleToolsCall(ctx, w, req, wg)
	case "shutdown":
		s.writeResult(w, req.ID, struct{}{})
	default:
		if req.ID != nil {
			s.writeError(w, *req.ID, -32601, "method not found: "+req.Method)
		}
	}
}

func (s *MCPServer) handleInitialize(w io.Writer, req *jsonRPCRequest) {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	json.Unmarshal(req.Params, &p)
	if p.ProtocolVersion == "" {
		p.ProtocolVersion = "2024-11-05"
	}
	s.writeResult(w, req.ID, map[string]interface{}{
		"protocolVersion": p.ProtocolVersion,
		"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
		"serverInfo":      map[string]interface{}{"name": "driver-agent", "version": "0.1.0"},
	})
}

func (s *MCPServer) handleToolsList(w io.Writer, req *jsonRPCRequest) {
	type schema struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"inputSchema"`
	}
	out := make([]schema, 0, len(s.toolOrder))
	for _, name := range s.toolOrder {
		t := s.tools[name]
		out = append(out, schema{Name: t.Name(), Description: t.Description(), InputSchema: t.InputSchema()})
	}
	s.writeResult(w, req.ID, map[string]interface{}{"tools": out})
}

func (s *MCPServer) handleToolsCall(ctx context.Context, w io.Writer, req *jsonRPCRequest, wg *sync.WaitGroup) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		s.writeError(w, idOrNull(req.ID), -32602, "invalid params: "+err.Error())
		return
	}
	t, ok := s.tools[p.Name]
	if !ok {
		s.writeError(w, idOrNull(req.ID), -32602, "unknown tool: "+p.Name)
		return
	}
	id := req.ID
	args := p.Arguments
	wg.Add(1)
	go func() {
		defer wg.Done()
		result, err := t.Call(ctx, args)
		if err != nil {
			s.writeError(w, idOrNull(id), -32000, err.Error())
			return
		}
		s.writeResult(w, id, map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": string(result)},
			},
		})
	}()
}

func (s *MCPServer) writeResult(w io.Writer, id *json.RawMessage, result interface{}) {
	resp := jsonRPCResponse{JSONRPC: "2.0", ID: idOrNull(id), Result: result}
	s.writeLine(w, resp)
}

func (s *MCPServer) writeError(w io.Writer, id json.RawMessage, code int, msg string) {
	resp := jsonRPCResponse{JSONRPC: "2.0", ID: id, Error: &jsonRPCError{Code: code, Message: msg}}
	s.writeLine(w, resp)
}

func (s *MCPServer) writeLine(w io.Writer, v interface{}) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := w.Write(b); err != nil {
		s.handleWriteErr(err)
		return
	}
	if _, err := w.Write([]byte("\n")); err != nil {
		s.handleWriteErr(err)
		return
	}
	if f, ok := w.(interface{ Sync() error }); ok {
		_ = f.Sync()
	}
	atomic.AddInt64(&s.linesOut, 1)
}

func (s *MCPServer) handleWriteErr(err error) {
	fmt.Fprintf(os.Stderr, "driver: mcp write: %v\n", err)
	if errors.Is(err, io.ErrClosedPipe) || errors.Is(err, syscall.EPIPE) {
		atomic.StoreInt32(&s.broken, 1)
	}
}

func idOrNull(id *json.RawMessage) json.RawMessage {
	if id == nil {
		return json.RawMessage(`null`)
	}
	return *id
}
