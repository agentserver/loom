package capabilitydoc

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/capability"
	"github.com/yourorg/multi-agent/internal/config"
)

func TestStoreRefreshWritesPersistentHierarchicalCapabilityDoc(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "CURRENT_STATE.md"), []byte("## Tools\n- cached state"), 0o644))
	dynamicPath := filepath.Join(dir, "dynamic_mcp.yaml")
	require.NoError(t, os.WriteFile(dynamicPath, []byte(`
servers:
  generated_policy:
    transport: stdio
    command: python3
    args: ["mcp/generated_policy.py"]
    tools:
      - server: generated_policy
        name: evaluate_policy
        description: Evaluates generated policy rules.
  legacy_dynamic:
    transport: stdio
    command: python3
    args: ["mcp/legacy.py"]
    tools:
      - legacy_tool
`), 0o644))

	store := NewStore(dir)
	err := store.Refresh(context.Background(), Input{
		Config: &config.Config{
			Server: config.Server{Name: "slave-a"},
			Discovery: config.Discovery{
				DisplayName: "slave-a",
				Description: "test slave",
				Skills:      []string{"chat", "mcp", "build_mcp"},
			},
			MCPServers: map[string]config.MCPServer{
				"static_csv": {Transport: "stdio", Command: "python3", Args: []string{"csv.py"}},
			},
			Resources: &config.Resources{MemoryGB: 16, Tags: []string{"gpu"}},
		},
		WorkDir:        "/workspace/slave-a",
		DynamicMCPPath: dynamicPath,
		MCPTools: []capability.MCPToolDescriptor{{
			Server:      "static_csv",
			Name:        "profile_csv",
			Description: "Profiles CSV files.",
		}},
		Reason: "startup",
	})

	require.NoError(t, err)
	body, err := os.ReadFile(filepath.Join(dir, "CAPABILITIES.md"))
	require.NoError(t, err)
	text := string(body)
	require.Contains(t, text, "# Capability Document")
	require.Contains(t, text, "## Summary")
	require.Contains(t, text, "## Runtime")
	require.Contains(t, text, "## Skills")
	require.Contains(t, text, "## MCP Servers")
	require.Contains(t, text, "### static_csv")
	require.Contains(t, text, "profile_csv")
	require.Contains(t, text, "### generated_policy")
	require.Contains(t, text, "evaluate_policy")
	require.Contains(t, text, "### legacy_dynamic")
	require.Contains(t, text, "legacy_dynamic/legacy_tool")
	require.Contains(t, text, "## Resources")
	require.Contains(t, text, "memory_gb: 16")
	require.Contains(t, text, "## Current State")
	require.Contains(t, text, "cached state")
}
