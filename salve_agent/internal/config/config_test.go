package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoad_ValidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
server:
  url: https://example.com
  name: agent-1
claude:
  bin: claude
mcp_servers:
  echo:
    transport: stdio
    command: /bin/echo
discovery:
  display_name: A
  skills: [chat, mcp]
`), 0o600))

	c, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, "https://example.com", c.Server.URL)
	require.Equal(t, "agent-1", c.Server.Name)
	require.Equal(t, "stdio", c.MCPServers["echo"].Transport)
	require.ElementsMatch(t, []string{"chat", "mcp"}, c.Discovery.Skills)
}

func TestLoad_MissingURL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(path, []byte("server:\n  name: x\n"), 0o600))

	_, err := Load(path)
	require.ErrorContains(t, err, "server.url")
}
