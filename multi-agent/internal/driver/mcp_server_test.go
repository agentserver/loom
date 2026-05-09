package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// mockTool: simplest possible Tool implementer for testing dispatch.
type mockTool struct {
	name string
	call func(ctx context.Context, args json.RawMessage) (json.RawMessage, error)
}

func (m *mockTool) Name() string                 { return m.name }
func (m *mockTool) Description() string          { return "mock tool" }
func (m *mockTool) InputSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (m *mockTool) Call(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	return m.call(ctx, args)
}

func TestMCPServer_InitializeHandshake(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"claude-code","version":"x"}}}` + "\n")
	var out bytes.Buffer
	srv := NewMCPServer([]Tool{})
	go srv.Serve(in, &out)
	srv.WaitForLines(1)
	var resp struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(firstLine(out.String()), &resp); err != nil {
		t.Fatalf("unmarshal: %v (out=%s)", err, out.String())
	}
	if resp.ID != 1 {
		t.Errorf("id: %d", resp.ID)
	}
	if !bytes.Contains(resp.Result, []byte(`"protocolVersion":"2024-11-05"`)) {
		t.Errorf("init result missing protocolVersion: %s", resp.Result)
	}
}

func TestMCPServer_ToolsList_ReturnsSchemas(t *testing.T) {
	tools := []Tool{
		&mockTool{name: "alpha", call: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) { return nil, nil }},
		&mockTool{name: "beta", call: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) { return nil, nil }},
	}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n")
	var out bytes.Buffer
	srv := NewMCPServer(tools)
	go srv.Serve(in, &out)
	srv.WaitForLines(1)
	var resp struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	json.Unmarshal(firstLine(out.String()), &resp)
	if len(resp.Result.Tools) != 2 {
		t.Errorf("tools count: %d (out=%s)", len(resp.Result.Tools), out.String())
	}
	names := map[string]bool{}
	for _, tt := range resp.Result.Tools {
		names[tt.Name] = true
	}
	if !names["alpha"] || !names["beta"] {
		t.Errorf("names: %+v", names)
	}
}

func TestMCPServer_ToolsCall_Dispatch(t *testing.T) {
	called := false
	tool := &mockTool{
		name: "do",
		call: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
			called = true
			if !bytes.Contains(args, []byte(`"x":42`)) {
				t.Errorf("args: %s", args)
			}
			return json.RawMessage(`{"ok":true}`), nil
		},
	}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"do","arguments":{"x":42}}}` + "\n")
	var out bytes.Buffer
	srv := NewMCPServer([]Tool{tool})
	go srv.Serve(in, &out)
	srv.WaitForLines(1)
	if !called {
		t.Fatalf("tool not invoked (out=%s)", out.String())
	}
	var resp struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	json.Unmarshal(firstLine(out.String()), &resp)
	if len(resp.Result.Content) != 1 || resp.Result.Content[0].Type != "text" {
		t.Fatalf("result shape: %+v", resp.Result)
	}
	if !strings.Contains(resp.Result.Content[0].Text, `"ok":true`) {
		t.Errorf("text: %s", resp.Result.Content[0].Text)
	}
}

func TestMCPServer_ToolsCall_ErrorCode(t *testing.T) {
	tool := &mockTool{
		name: "bad",
		call: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
			return nil, &MCPToolError{Message: "bad input"}
		},
	}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"bad","arguments":{}}}` + "\n")
	var out bytes.Buffer
	srv := NewMCPServer([]Tool{tool})
	go srv.Serve(in, &out)
	srv.WaitForLines(1)
	var resp struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	json.Unmarshal(firstLine(out.String()), &resp)
	if resp.Error.Code != -32000 {
		t.Errorf("code: %d", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "bad input") {
		t.Errorf("msg: %s", resp.Error.Message)
	}
}

func TestMCPServer_UnknownMethod_ReturnsMethodNotFound(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":5,"method":"weird/thing"}` + "\n")
	var out bytes.Buffer
	srv := NewMCPServer([]Tool{})
	go srv.Serve(in, &out)
	srv.WaitForLines(1)
	var resp struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	json.Unmarshal(firstLine(out.String()), &resp)
	if resp.Error.Code != -32601 {
		t.Errorf("code: %d", resp.Error.Code)
	}
}

func firstLine(s string) []byte {
	i := strings.Index(s, "\n")
	if i < 0 {
		return []byte(s)
	}
	return []byte(s[:i])
}
