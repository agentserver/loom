package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func buildFakeMCP(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	src, _ := filepath.Abs(filepath.Join(filepath.Dir(file), "../../testdata/fake-mcp-stdio"))
	out := filepath.Join(t.TempDir(), "fake-mcp")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Dir = src
	require.NoError(t, cmd.Run())
	return out
}

func buildFakeMCPStdio(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	src, _ := filepath.Abs(filepath.Join(filepath.Dir(file), "../../testdata/fake-mcp-stdio"))
	out := filepath.Join(t.TempDir(), "fake-mcp-stdio")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Dir = src
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fake-mcp-stdio: %v: %s", err, out)
	}
	return out
}

func TestMCP_Stdio_EchoNoCapability(t *testing.T) {
	bin := buildFakeMCP(t)
	e := NewMCPExecutor(map[string]MCPServerCfg{
		"demo": {Transport: "stdio", Command: bin},
	})
	defer e.Close()

	body, _ := json.Marshal(map[string]interface{}{
		"server": "demo",
		"tool":   "echo",
		"args":   map[string]string{"msg": "hi"},
	})
	res, err := e.Run(context.Background(), Task{ID: "t", Prompt: string(body)}, &captureSink{})
	require.NoError(t, err)
	require.True(t, strings.Contains(res.Summary, "hi"))
	require.Equal(t, "", res.CapabilityChange)
}

func TestMCP_Stdio_RaiseCapability(t *testing.T) {
	bin := buildFakeMCP(t)
	e := NewMCPExecutor(map[string]MCPServerCfg{
		"demo": {Transport: "stdio", Command: bin},
	})
	defer e.Close()

	body, _ := json.Marshal(map[string]interface{}{"server": "demo", "tool": "raise", "args": map[string]string{}})
	res, err := e.Run(context.Background(), Task{Prompt: string(body)}, &captureSink{})
	require.NoError(t, err)
	require.Equal(t, "did the thing", res.CapabilityChange)
}

func TestMCP_BadPromptJSON(t *testing.T) {
	e := NewMCPExecutor(nil)
	defer e.Close()
	_, err := e.Run(context.Background(), Task{Prompt: "not json"}, &captureSink{})
	require.ErrorContains(t, err, "mcp prompt must be JSON")
}

func TestMCP_UnknownServer(t *testing.T) {
	e := NewMCPExecutor(nil)
	defer e.Close()
	body, _ := json.Marshal(map[string]string{"server": "ghost", "tool": "x"})
	_, err := e.Run(context.Background(), Task{Prompt: string(body)}, &captureSink{})
	require.ErrorContains(t, err, "unknown mcp server")
}

func TestMCP_BoomReturnsError(t *testing.T) {
	bin := buildFakeMCP(t)
	e := NewMCPExecutor(map[string]MCPServerCfg{"demo": {Transport: "stdio", Command: bin}})
	defer e.Close()
	body, _ := json.Marshal(map[string]string{"server": "demo", "tool": "boom"})
	_, err := e.Run(context.Background(), Task{Prompt: string(body)}, &captureSink{})
	require.ErrorContains(t, err, "intentional failure")
}

func TestMCP_HTTP_EchoCapability(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string
			Params struct{ Name string }
		}
		_ = json.Unmarshal(body, &req)
		var result string
		cap := false
		hint := ""
		switch req.Params.Name {
		case "echo":
			result = `"hello"`
		case "raise":
			result = `"raised"`
			cap = true
			hint = "http-cap"
		}
		resp := map[string]interface{}{"jsonrpc": "2.0", "id": 1,
			"result": map[string]interface{}{
				"result":             json.RawMessage(result),
				"capability_changed": cap,
				"change_hint":        hint,
			}}
		b, _ := json.Marshal(resp)
		w.Write(b)
	}))
	defer srv.Close()

	e := NewMCPExecutor(map[string]MCPServerCfg{"web": {Transport: "http", URL: srv.URL}})
	defer e.Close()

	body, _ := json.Marshal(map[string]string{"server": "web", "tool": "raise"})
	res, err := e.Run(context.Background(), Task{Prompt: string(body)}, &captureSink{})
	require.NoError(t, err)
	require.Equal(t, "http-cap", res.CapabilityChange)
	require.Equal(t, "raised", res.Summary)
}

func TestMCPExecutor_ListTools(t *testing.T) {
	dir := t.TempDir()
	src := `
import sys, json
for line in sys.stdin:
    req = json.loads(line)
    if req.get("method") == "tools/list":
        print(json.dumps({"jsonrpc":"2.0","id":req["id"],"result":{"tools":[
            {"name":"echo","description":"Echo input","inputSchema":{"type":"object","properties":{"msg":{"type":"string"}}},"result_description":"Echoed payload"},
            {"name":"raise","description":"Raise capability","input_schema":{"type":"object","properties":{"flag":{"type":"boolean"}}}},
            {"name":"boom"}
        ]}}), flush=True)
`
	path := filepath.Join(dir, "list-tools.py")
	require.NoError(t, os.WriteFile(path, []byte(src), 0o600))
	e := NewMCPExecutor(map[string]MCPServerCfg{"x": {Transport: "stdio", Command: "python3", Args: []string{path}}})
	defer e.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tools, err := e.ListTools(ctx, "x")
	require.NoError(t, err)
	require.Len(t, tools, 3, "got %v", tools)
	assert.Equal(t, "x", tools[0].Server)
	assert.Equal(t, "echo", tools[0].Name)
	assert.Equal(t, "Echo input", tools[0].Description)
	assert.JSONEq(t, `{"type":"object","properties":{"msg":{"type":"string"}}}`, string(tools[0].InputSchema))
	assert.Equal(t, "Echoed payload", tools[0].ResultDescription)
	assert.Equal(t, "x", tools[1].Server)
	assert.Equal(t, "raise", tools[1].Name)
	assert.Equal(t, "Raise capability", tools[1].Description)
	assert.JSONEq(t, `{"type":"object","properties":{"flag":{"type":"boolean"}}}`, string(tools[1].InputSchema))
	assert.Equal(t, "x", tools[2].Server)
	assert.Equal(t, "boom", tools[2].Name)
	assert.Empty(t, tools[2].InputSchema)
}

func TestMCPExecutor_RegisterStdioReplaces(t *testing.T) {
	bin := buildFakeMCPStdio(t)
	e := NewMCPExecutor(map[string]MCPServerCfg{})
	defer e.Close()

	require.NoError(t, e.RegisterStdio("x", MCPServerCfg{Transport: "stdio", Command: bin}))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := e.ListTools(ctx, "x")
	require.NoError(t, err)

	// Re-register with the same command (forces process replace).
	require.NoError(t, e.RegisterStdio("x", MCPServerCfg{Transport: "stdio", Command: bin}))
	_, err = e.ListTools(ctx, "x")
	require.NoError(t, err, "post-reregister ListTools: %v", err)
}

func TestMCPExecutor_RegisterStdioRejectsHTTP(t *testing.T) {
	e := NewMCPExecutor(map[string]MCPServerCfg{})
	defer e.Close()
	err := e.RegisterStdio("x", MCPServerCfg{Transport: "http", URL: "http://x"})
	require.Error(t, err, "expected error for non-stdio transport")
}

// Standard-shape MCP servers (per the MCP spec) return tools/call results as
// {"content":[{"type":"text","text":"..."}], "isError"?: bool}.
// The slave executor must accept this shape, not only the internal
// {result, capability_changed, change_hint} wrapper used by build_mcp.
func TestMCP_Stdio_StandardContentShape(t *testing.T) {
	dir := t.TempDir()
	src := `
import sys, json
for line in sys.stdin:
    req = json.loads(line)
    if req.get("method") == "tools/call":
        print(json.dumps({"jsonrpc":"2.0","id":req["id"],"result":{"content":[{"type":"text","text":"hello world"}]}}), flush=True)
`
	path := filepath.Join(dir, "std.py")
	require.NoError(t, os.WriteFile(path, []byte(src), 0o600))
	e := NewMCPExecutor(map[string]MCPServerCfg{"std": {Transport: "stdio", Command: "python3", Args: []string{path}}})
	defer e.Close()

	body, _ := json.Marshal(map[string]string{"server": "std", "tool": "echo"})
	res, err := e.Run(context.Background(), Task{Prompt: string(body)}, &captureSink{})
	require.NoError(t, err)
	require.Equal(t, "hello world", res.Summary)
	require.Equal(t, "", res.CapabilityChange)
}

func TestMCP_Stdio_StandardContent_IsError(t *testing.T) {
	dir := t.TempDir()
	src := `
import sys, json
for line in sys.stdin:
    req = json.loads(line)
    if req.get("method") == "tools/call":
        print(json.dumps({"jsonrpc":"2.0","id":req["id"],"result":{"content":[{"type":"text","text":"oops"}],"isError":True}}), flush=True)
`
	path := filepath.Join(dir, "err.py")
	require.NoError(t, os.WriteFile(path, []byte(src), 0o600))
	e := NewMCPExecutor(map[string]MCPServerCfg{"x": {Transport: "stdio", Command: "python3", Args: []string{path}}})
	defer e.Close()

	body, _ := json.Marshal(map[string]string{"server": "x", "tool": "echo"})
	_, err := e.Run(context.Background(), Task{Prompt: string(body)}, &captureSink{})
	require.ErrorContains(t, err, "oops")
}

// Multi-block content concatenates text blocks in order.
func TestMCP_Stdio_StandardContent_MultiText(t *testing.T) {
	dir := t.TempDir()
	src := `
import sys, json
for line in sys.stdin:
    req = json.loads(line)
    if req.get("method") == "tools/call":
        print(json.dumps({"jsonrpc":"2.0","id":req["id"],"result":{"content":[
            {"type":"text","text":"foo"},
            {"type":"text","text":"bar"}
        ]}}), flush=True)
`
	path := filepath.Join(dir, "multi.py")
	require.NoError(t, os.WriteFile(path, []byte(src), 0o600))
	e := NewMCPExecutor(map[string]MCPServerCfg{"m": {Transport: "stdio", Command: "python3", Args: []string{path}}})
	defer e.Close()
	body, _ := json.Marshal(map[string]string{"server": "m", "tool": "echo"})
	res, err := e.Run(context.Background(), Task{Prompt: string(body)}, &captureSink{})
	require.NoError(t, err)
	require.Equal(t, "foobar", res.Summary)
}
