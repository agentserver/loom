package driver

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"sync"
	"sync/atomic"
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
type MCPToolError struct {
	Message string
}

func (e *MCPToolError) Error() string { return e.Message }

type MCPServer struct {
	tools     map[string]Tool
	toolOrder []string
	writeMu   sync.Mutex
	linesOut  int64
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

// Serve reads one JSON-RPC message per line from r and writes responses to w.
// Returns when r reaches EOF or an "exit" notification is received.
func (s *MCPServer) Serve(r io.Reader, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req jsonRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			s.writeError(w, json.RawMessage(`null`), -32700, "parse error: "+err.Error())
			continue
		}
		if req.Method == "exit" {
			return nil
		}
		s.dispatch(w, &req)
	}
	return scanner.Err()
}

// WaitForLines is a test helper that blocks until at least n response lines
// have been written. Used so tests don't race the goroutine running Serve.
func (s *MCPServer) WaitForLines(n int) {
	for atomic.LoadInt64(&s.linesOut) < int64(n) {
		// tight spin is fine for short tests
	}
}

func (s *MCPServer) dispatch(w io.Writer, req *jsonRPCRequest) {
	switch req.Method {
	case "initialize":
		s.handleInitialize(w, req)
	case "notifications/initialized":
		// notifications get no response
	case "tools/list":
		s.handleToolsList(w, req)
	case "tools/call":
		s.handleToolsCall(w, req)
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

func (s *MCPServer) handleToolsCall(w io.Writer, req *jsonRPCRequest) {
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
	result, err := t.Call(context.Background(), p.Arguments)
	if err != nil {
		s.writeError(w, idOrNull(req.ID), -32000, err.Error())
		return
	}
	s.writeResult(w, req.ID, map[string]interface{}{
		"content": []map[string]interface{}{
			{"type": "text", "text": string(result)},
		},
	})
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
	w.Write(b)
	w.Write([]byte("\n"))
	if f, ok := w.(interface{ Sync() error }); ok {
		_ = f.Sync()
	}
	atomic.AddInt64(&s.linesOut, 1)
}

func idOrNull(id *json.RawMessage) json.RawMessage {
	if id == nil {
		return json.RawMessage(`null`)
	}
	return *id
}
