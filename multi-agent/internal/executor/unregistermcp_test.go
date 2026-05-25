package executor

import (
	"context"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/observer"
)

// captureObserver records emitted events for assertion.
type captureObserver struct{ events []observer.Event }

func (c *captureObserver) Emit(ev observer.Event) { c.events = append(c.events, ev) }

// registerEchoForUnregister registers a minimal echo MCP via the RegisterMCPExecutor
// so the unregister path has a real entry to operate on.
func registerEchoForUnregister(t *testing.T) (*UnregisterMCPExecutor, *MCPExecutor, string, *captureObserver) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
	work := t.TempDir()
	mcpExec := NewMCPExecutor(map[string]MCPServerCfg{})
	t.Cleanup(mcpExec.Close)
	repub := func(ctx context.Context) error { return nil }
	obs := &captureObserver{}

	reg := NewRegisterMCPExecutor(RegisterMCPConfig{
		WorkDir:   work,
		MCPExec:   mcpExec,
		Republish: repub,
		Observer:  obs,
	})
	writeSource(t, work, "generated_mcp/echo/v1.py", minimalMCPSource)
	regPrompt := `{
		"spec": {
			"name": "echo",
			"description": "Echo tool",
			"version": 1,
			"tools": [{"name":"echo","description":"echo","args_schema":{"type":"object"},"result_description":"r"}],
			"allowed_packages": []
		},
		"source_path": "generated_mcp/echo/v1.py"
	}`
	_, err := reg.Run(context.Background(), Task{ID: "reg", Skill: "register_mcp", Prompt: regPrompt}, &nopSink{})
	require.NoError(t, err)

	obs.events = nil // discard register events so unregister assertions are clean

	unreg := NewUnregisterMCPExecutor(UnregisterMCPConfig{
		WorkDir:   work,
		MCPExec:   mcpExec,
		Republish: repub,
		Observer:  obs,
	})
	return unreg, mcpExec, work, obs
}

func TestUnregisterMCP_HappyPath(t *testing.T) {
	unreg, mcpExec, work, obs := registerEchoForUnregister(t)

	res, err := unreg.Run(context.Background(), Task{ID: "u1", Skill: "unregister_mcp", Prompt: `{"name":"echo"}`}, &nopSink{})
	require.NoError(t, err)
	require.Contains(t, res.Summary, `"type":"mcp_unregistered"`)
	require.Contains(t, res.Summary, `"name":"echo"`)
	require.Contains(t, res.Summary, `"removed":"true"`)

	df, err := ReadDynamicYAML(DynamicYAMLPath(work))
	require.NoError(t, err)
	require.NotContains(t, df.Servers, "echo")

	require.NotContains(t, mcpExec.Servers(), "echo")

	require.Len(t, obs.events, 1)
	require.Equal(t, observer.EventMCPServerRemoved, obs.events[0].Type)
	require.Equal(t, "echo", obs.events[0].MCPServerName)
}

func TestUnregisterMCP_StrictMissingErrors(t *testing.T) {
	work := t.TempDir()
	mcpExec := NewMCPExecutor(map[string]MCPServerCfg{})
	t.Cleanup(mcpExec.Close)
	unreg := NewUnregisterMCPExecutor(UnregisterMCPConfig{
		WorkDir: work, MCPExec: mcpExec,
		Republish: func(ctx context.Context) error { return nil },
	})

	_, err := unreg.Run(context.Background(), Task{ID: "u", Prompt: `{"name":"nope"}`}, &nopSink{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not registered")
}

func TestUnregisterMCP_IfPresentMissingNoOp(t *testing.T) {
	work := t.TempDir()
	mcpExec := NewMCPExecutor(map[string]MCPServerCfg{})
	t.Cleanup(mcpExec.Close)
	obs := &captureObserver{}
	unreg := NewUnregisterMCPExecutor(UnregisterMCPConfig{
		WorkDir: work, MCPExec: mcpExec,
		Republish: func(ctx context.Context) error { return nil },
		Observer:  obs,
	})

	res, err := unreg.Run(context.Background(), Task{ID: "u", Prompt: `{"name":"nope","if_present":true}`}, &nopSink{})
	require.NoError(t, err)
	require.Contains(t, res.Summary, `"type":"mcp_unregistered"`)
	require.Contains(t, res.Summary, `"removed":"false"`)
	require.Empty(t, obs.events, "no observer event when nothing was removed")
}

func TestUnregisterMCP_RejectsNonJSONPrompt(t *testing.T) {
	work := t.TempDir()
	mcpExec := NewMCPExecutor(map[string]MCPServerCfg{})
	t.Cleanup(mcpExec.Close)
	unreg := NewUnregisterMCPExecutor(UnregisterMCPConfig{WorkDir: work, MCPExec: mcpExec})

	_, err := unreg.Run(context.Background(), Task{ID: "u", Prompt: "not json"}, &nopSink{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be JSON")
}

func TestUnregisterMCP_RejectsEmptyName(t *testing.T) {
	work := t.TempDir()
	mcpExec := NewMCPExecutor(map[string]MCPServerCfg{})
	t.Cleanup(mcpExec.Close)
	unreg := NewUnregisterMCPExecutor(UnregisterMCPConfig{WorkDir: work, MCPExec: mcpExec})

	_, err := unreg.Run(context.Background(), Task{ID: "u", Prompt: `{"name":""}`}, &nopSink{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "name is required")
}
