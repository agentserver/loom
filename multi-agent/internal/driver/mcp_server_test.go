package driver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
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
	go srv.Serve(context.Background(), in, &out)
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
	go srv.Serve(context.Background(), in, &out)
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
	go srv.Serve(context.Background(), in, &out)
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
	go srv.Serve(context.Background(), in, &out)
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

func TestMCPServer_ConcurrentToolsCall(t *testing.T) {
	releaseSlow := make(chan struct{})
	slowStarted := make(chan struct{})
	released := false
	release := func() {
		if !released {
			close(releaseSlow)
			released = true
		}
	}
	defer release()

	tools := []Tool{
		&mockTool{name: "slow", call: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
			close(slowStarted)
			<-releaseSlow
			return json.RawMessage(`{"ok":"slow"}`), nil
		}},
		&mockTool{name: "fast", call: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"ok":"fast"}`), nil
		}},
	}
	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"slow","arguments":{}}}` + "\n" +
			`{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"fast","arguments":{}}}` + "\n",
	)
	outR, outW := io.Pipe()
	srv := NewMCPServer(tools)
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(context.Background(), in, outW)
		_ = outW.Close()
	}()

	select {
	case <-slowStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("slow tool was not invoked")
	}

	reader := bufio.NewReader(outR)
	line, ok := readLineBefore(t, reader, 200*time.Millisecond)
	if !ok {
		t.Fatal("fast tools/call did not respond while slow tools/call was still running")
	}
	id, text := decodeToolResponse(t, line)
	if id != 11 || !strings.Contains(text, `"ok":"fast"`) {
		t.Fatalf("first response = id %d text %q, want fast response id 11", id, text)
	}

	release()
	line = readLineEventually(t, reader, 2*time.Second)
	id, text = decodeToolResponse(t, line)
	if id != 10 || !strings.Contains(text, `"ok":"slow"`) {
		t.Fatalf("second response = id %d text %q, want slow response id 10", id, text)
	}
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("Serve returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after input EOF and slow tool completion")
	}
}

func TestMCPServer_UnknownMethod_ReturnsMethodNotFound(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":5,"method":"weird/thing"}` + "\n")
	var out bytes.Buffer
	srv := NewMCPServer([]Tool{})
	go srv.Serve(context.Background(), in, &out)
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

func readLineBefore(t *testing.T, r *bufio.Reader, d time.Duration) ([]byte, bool) {
	t.Helper()
	ch := make(chan []byte, 1)
	errCh := make(chan error, 1)
	go func() {
		line, err := r.ReadBytes('\n')
		if err != nil {
			errCh <- err
			return
		}
		ch <- line
	}()
	select {
	case line := <-ch:
		return line, true
	case err := <-errCh:
		t.Fatalf("read response: %v", err)
		return nil, false
	case <-time.After(d):
		return nil, false
	}
}

func readLineEventually(t *testing.T, r *bufio.Reader, d time.Duration) []byte {
	t.Helper()
	line, ok := readLineBefore(t, r, d)
	if !ok {
		t.Fatalf("timed out reading response after %s", d)
	}
	return line
}

func decodeToolResponse(t *testing.T, line []byte) (int, string) {
	t.Helper()
	var resp struct {
		ID     int `json:"id"`
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("unmarshal response: %v (line=%s)", err, string(line))
	}
	if len(resp.Result.Content) != 1 {
		t.Fatalf("response content count = %d, want 1 (line=%s)", len(resp.Result.Content), string(line))
	}
	return resp.ID, resp.Result.Content[0].Text
}

func firstLine(s string) []byte {
	i := strings.Index(s, "\n")
	if i < 0 {
		return []byte(s)
	}
	return []byte(s[:i])
}

// TestMCPServerServe_StopsOnContextCancel verifies that cancelling the ctx
// passed into Serve drains in-flight long-running tool calls and returns from
// Serve in bounded time, instead of waiting on stdin EOF. Fixes §1.1 #3 of
// docs/review-2026-06-13.md.
func TestMCPServerServe_StopsOnContextCancel(t *testing.T) {
	blocking := &mockTool{
		name: "blocker",
		call: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	// pipe reader stays open (no EOF) so the ONLY way Serve can return is via
	// ctx cancel propagating through the in-flight tool call + Serve loop.
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write([]byte(
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"blocker","arguments":{}}}` + "\n",
		))
		// intentionally do not close pw — Serve should exit anyway
	}()
	var out bytes.Buffer
	srv := NewMCPServer([]Tool{blocking})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, pr, &out) }()

	// Give the tool a moment to be dispatched, then cancel.
	// Reader stays open intentionally: Serve must unblock on ctx cancel even
	// when stdin would otherwise stay open forever (real production case:
	// codex doesn't close driver's stdin on SIGTERM).
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// expected within 1s; Serve must wake out of its read on ctx cancel
		// (it cannot wait for pw to close — production parent won't close stdin)
	case <-time.After(2 * time.Second):
		t.Fatalf("Serve did not return after ctx cancel; out=%s", out.String())
	}
	_ = pw.Close()
}

// TestMCPServerServe_StopsOnContextCancel_Idle verifies the SIGTERM-on-idle
// path: even with NO in-flight tool call and NO pending stdin data, ctx
// cancel must unblock the Serve loop (otherwise driver-agent's
// signal.NotifyContext is a no-op when the parent isn't sending requests).
func TestMCPServerServe_StopsOnContextCancel_Idle(t *testing.T) {
	pr, pw := io.Pipe() // reader stays open with no data forever
	defer pw.Close()

	var out bytes.Buffer
	srv := NewMCPServer([]Tool{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, pr, &out) }()

	// Let Serve enter its blocking read, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// expected: Serve wakes out of its read and returns ctx.Err()
	case <-time.After(2 * time.Second):
		t.Fatal("idle Serve did not return after ctx cancel")
	}
}

// TestMCPServerWriteLine_EPIPETriggersStop verifies that when stdout is
// closed (e.g. parent Claude Code process died), Serve detects the broken
// pipe on the next write and exits instead of silently looping on a dead
// channel. Fixes §1.1 #3 (second half) of docs/review-2026-06-13.md.
func TestMCPServerWriteLine_EPIPETriggersStop(t *testing.T) {
	tool := &mockTool{
		name: "ping",
		call: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"ok":true}`), nil
		},
	}
	// reader side closed → any Write returns ErrClosedPipe
	pr, pw := io.Pipe()
	_ = pr.Close()

	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ping","arguments":{}}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ping","arguments":{}}}` + "\n",
	)
	srv := NewMCPServer([]Tool{tool})
	done := make(chan error, 1)
	go func() { done <- srv.Serve(context.Background(), in, pw) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Serve returned nil; expected broken-pipe error")
		}
		if !strings.Contains(err.Error(), "broken") && !strings.Contains(err.Error(), "closed pipe") {
			t.Fatalf("Serve err = %v; expected broken-pipe", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not exit on broken pipe")
	}
}
