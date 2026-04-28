package executor

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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
