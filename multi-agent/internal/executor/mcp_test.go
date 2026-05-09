package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func buildFakeMCP(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	src, _ := filepath.Abs(filepath.Join(filepath.Dir(file), "../../testdata/fake-mcp-stdio"))
	out := filepath.Join(t.TempDir(), "fake-mcp")
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
	bin := buildFakeMCPStdio(t)
	cfg := MCPServerCfg{Transport: "stdio", Command: bin}
	e := NewMCPExecutor(map[string]MCPServerCfg{"x": cfg})
	defer e.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tools, err := e.ListTools(ctx, "x")
	require.NoError(t, err)
	want := map[string]bool{"echo": true, "raise": true, "boom": true}
	require.Equal(t, 3, len(tools), "got %v", tools)
	for _, n := range tools {
		require.True(t, want[n], "unexpected tool name %q", n)
	}
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
