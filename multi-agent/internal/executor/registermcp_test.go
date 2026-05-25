package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/observer"
)

const minimalMCPSource = `import sys, json
def main():
    for line in sys.stdin:
        try:
            req = json.loads(line)
        except Exception:
            continue
        method = req.get("method", "")
        rid = req.get("id", 0)
        if method == "tools/list":
            print(json.dumps({"jsonrpc":"2.0","id":rid,"result":{"tools":[{"name":"echo"}]}}), flush=True)
        elif method == "tools/call":
            print(json.dumps({"jsonrpc":"2.0","id":rid,"result":{"result":"ok","capability_changed":False}}), flush=True)
        else:
            print(json.dumps({"jsonrpc":"2.0","id":rid,"error":{"message":"unknown"}}), flush=True)
if __name__ == "__main__":
    main()
`

func newRegisterMCPForTest(t *testing.T) (*RegisterMCPExecutor, string) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
	work := t.TempDir()
	mcpExec := NewMCPExecutor(map[string]MCPServerCfg{})
	t.Cleanup(mcpExec.Close)
	repub := func(ctx context.Context) error { return nil }
	r := NewRegisterMCPExecutor(RegisterMCPConfig{
		WorkDir:   work,
		MCPExec:   mcpExec,
		Republish: repub,
	})
	return r, work
}

func writeSource(t *testing.T, workDir, rel, body string) {
	t.Helper()
	full := filepath.Join(workDir, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, []byte(body), 0o644))
}

func TestRegisterMCP_HappyPath(t *testing.T) {
	r, work := newRegisterMCPForTest(t)
	writeSource(t, work, "generated_mcp/echo/v1.py", minimalMCPSource)

	prompt := `{
		"spec": {
			"name": "echo",
			"description": "Echo tool",
			"version": 1,
			"tools": [{"name":"echo","description":"echo","args_schema":{"type":"object"},"result_description":"r"}],
			"allowed_packages": []
		},
		"source_path": "generated_mcp/echo/v1.py"
	}`
	res, err := r.Run(context.Background(), Task{ID: "t1", Skill: "register_mcp", Prompt: prompt}, &nopSink{})
	require.NoError(t, err)
	require.Contains(t, res.Summary, `"type":"mcp_tool_set"`)

	// dynamic_mcp.yaml has the entry
	df, err := ReadDynamicYAML(DynamicYAMLPath(work))
	require.NoError(t, err)
	require.Contains(t, df.Servers, "echo")
	require.Equal(t, "stdio", df.Servers["echo"].Transport)
	require.Equal(t, "python3", df.Servers["echo"].Command)

	// MCPExecutor knows about the server
	require.Contains(t, r.MCPExec.Servers(), "echo")
}

func TestRegisterMCP_RejectsNonJSONPrompt(t *testing.T) {
	r, _ := newRegisterMCPForTest(t)
	_, err := r.Run(context.Background(), Task{ID: "t", Prompt: "not json"}, &nopSink{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be JSON")
}

func TestRegisterMCP_RejectsInvalidSpec(t *testing.T) {
	r, work := newRegisterMCPForTest(t)
	writeSource(t, work, "generated_mcp/x/v1.py", minimalMCPSource)
	prompt := `{"spec":{"name":"X bad"},"source_path":"generated_mcp/x/v1.py"}`
	_, err := r.Run(context.Background(), Task{ID: "t", Prompt: prompt}, &nopSink{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid spec")
}

func TestRegisterMCP_RejectsBadSyntax(t *testing.T) {
	r, work := newRegisterMCPForTest(t)
	writeSource(t, work, "generated_mcp/echo/v1.py", "def broken(:\n    pass\n")
	prompt := `{
		"spec":{"name":"echo","description":"d","version":1,
			"tools":[{"name":"echo","description":"d","args_schema":{"type":"object"},"result_description":"r"}],
			"allowed_packages":[]},
		"source_path":"generated_mcp/echo/v1.py"
	}`
	_, err := r.Run(context.Background(), Task{ID: "t", Prompt: prompt}, &nopSink{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "syntax")
}

func TestRegisterMCP_RejectsDisallowedImport(t *testing.T) {
	r, work := newRegisterMCPForTest(t)
	src := "import requests_html\n" + minimalMCPSource
	writeSource(t, work, "generated_mcp/echo/v1.py", src)
	prompt := `{
		"spec":{"name":"echo","description":"d","version":1,
			"tools":[{"name":"echo","description":"d","args_schema":{"type":"object"},"result_description":"r"}],
			"allowed_packages":[]},
		"source_path":"generated_mcp/echo/v1.py"
	}`
	_, err := r.Run(context.Background(), Task{ID: "t", Prompt: prompt}, &nopSink{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "disallowed imports")
}

func TestRegisterMCP_RejectsSourcePathEscape(t *testing.T) {
	r, _ := newRegisterMCPForTest(t)
	prompt := `{
		"spec":{"name":"echo","description":"d","version":1,
			"tools":[{"name":"echo","description":"d","args_schema":{"type":"object"},"result_description":"r"}],
			"allowed_packages":[]},
		"source_path":"../etc/passwd"
	}`
	_, err := r.Run(context.Background(), Task{ID: "t", Prompt: prompt}, &nopSink{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "escapes workdir")
}

func TestRegisterMCP_IdempotentReRegister(t *testing.T) {
	r, work := newRegisterMCPForTest(t)
	writeSource(t, work, "generated_mcp/echo/v1.py", minimalMCPSource)
	prompt := `{
		"spec":{"name":"echo","description":"d","version":1,
			"tools":[{"name":"echo","description":"d","args_schema":{"type":"object"},"result_description":"r"}],
			"allowed_packages":[]},
		"source_path":"generated_mcp/echo/v1.py"
	}`
	_, err := r.Run(context.Background(), Task{ID: "t1", Prompt: prompt}, &nopSink{})
	require.NoError(t, err)
	// Second call with same spec + same file: must succeed (overwrite is fine).
	_, err = r.Run(context.Background(), Task{ID: "t2", Prompt: prompt}, &nopSink{})
	require.NoError(t, err)
	df, err := ReadDynamicYAML(DynamicYAMLPath(work))
	require.NoError(t, err)
	require.Len(t, df.Servers, 1)
}

func TestRegisterMCP_RepublishCalled(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
	work := t.TempDir()
	mcpExec := NewMCPExecutor(map[string]MCPServerCfg{})
	t.Cleanup(mcpExec.Close)
	called := 0
	r := NewRegisterMCPExecutor(RegisterMCPConfig{
		WorkDir: work, MCPExec: mcpExec,
		Republish: func(ctx context.Context) error { called++; return nil },
	})
	writeSource(t, work, "generated_mcp/echo/v1.py", minimalMCPSource)
	prompt := `{
		"spec":{"name":"echo","description":"d","version":1,
			"tools":[{"name":"echo","description":"d","args_schema":{"type":"object"},"result_description":"r"}],
			"allowed_packages":[]},
		"source_path":"generated_mcp/echo/v1.py"
	}`
	_, err := r.Run(context.Background(), Task{ID: "t", Prompt: prompt}, &nopSink{})
	require.NoError(t, err)
	require.Equal(t, 1, called)
}

func TestRegisterMCP_ObserverEmitsCreated(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
	work := t.TempDir()
	mcpExec := NewMCPExecutor(map[string]MCPServerCfg{})
	t.Cleanup(mcpExec.Close)
	obs := &fakeObserver{}
	r := NewRegisterMCPExecutor(RegisterMCPConfig{
		WorkDir: work, MCPExec: mcpExec, Observer: obs,
	})
	writeSource(t, work, "generated_mcp/echo/v1.py", minimalMCPSource)
	prompt := `{
		"spec":{"name":"echo","description":"d","version":1,
			"tools":[{"name":"echo","description":"d","args_schema":{"type":"object"},"result_description":"r"}],
			"allowed_packages":[]},
		"source_path":"generated_mcp/echo/v1.py"
	}`
	_, err := r.Run(context.Background(), Task{ID: "t", Prompt: prompt}, &nopSink{})
	require.NoError(t, err)
	_, ok := observerEventOfType(obs.events, observer.EventMCPServerCreated)
	require.True(t, ok, "expected EventMCPServerCreated")
}

func TestMCPExecutor_UnregisterStdio_Removes(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}

	// Write a real MCP server script so ListTools can succeed and actually
	// spawn the subprocess — proving the kill() branch executes on unregister.
	tmpFile := filepath.Join(t.TempDir(), "echo_mcp.py")
	require.NoError(t, os.WriteFile(tmpFile, []byte(minimalMCPSource), 0o644))

	mcpExec := NewMCPExecutor(map[string]MCPServerCfg{})
	t.Cleanup(mcpExec.Close)
	require.NoError(t, mcpExec.RegisterStdio("echo", MCPServerCfg{
		Transport: "stdio", Command: "python3", Args: []string{tmpFile},
	}))
	require.Contains(t, mcpExec.Servers(), "echo")

	// Force the subprocess to spawn by calling ListTools.
	ctx := context.Background()
	tools, err := mcpExec.ListTools(ctx, "echo")
	require.NoError(t, err)
	require.Len(t, tools, 1, "expected one tool from the minimal MCP server")

	// The subprocess must now be live in the stdios map (pre-condition for
	// the kill() branch we are about to exercise).
	mcpExec.mu.Lock()
	conn := mcpExec.stdios["echo"]
	mcpExec.mu.Unlock()
	require.NotNil(t, conn, "subprocess should be alive before unregister")
	require.NotNil(t, conn.cmd.Process, "os.Process should be set")

	// Unregister: this must call kill() on the running subprocess.
	require.NoError(t, mcpExec.UnregisterStdio("echo"))
	require.NotContains(t, mcpExec.Servers(), "echo")

	// The process should now be dead (Wait already called inside kill()).
	require.NotNil(t, conn.cmd.ProcessState, "process should have exited after kill")
}

func TestMCPExecutor_UnregisterStdio_NotRegistered(t *testing.T) {
	mcpExec := NewMCPExecutor(map[string]MCPServerCfg{})
	t.Cleanup(mcpExec.Close)
	err := mcpExec.UnregisterStdio("nope")
	require.ErrorIs(t, err, ErrMCPNotRegistered)
}
