package executor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRemoveDynamicYAML_RemovesExisting(t *testing.T) {
	work := t.TempDir()
	path := DynamicYAMLPath(work)
	require.NoError(t, UpsertDynamicYAML(path, DynamicEntry{
		Name: "echo", Transport: "stdio", Command: "python3", Args: []string{"e.py"}, Version: 1,
	}))

	removed, err := RemoveDynamicYAML(path, "echo")
	require.NoError(t, err)
	require.True(t, removed)

	df, err := ReadDynamicYAML(path)
	require.NoError(t, err)
	require.NotContains(t, df.Servers, "echo")
}

func TestRemoveDynamicYAML_NoOpWhenMissing(t *testing.T) {
	work := t.TempDir()
	path := DynamicYAMLPath(work)
	require.NoError(t, UpsertDynamicYAML(path, DynamicEntry{
		Name: "keep", Transport: "stdio", Command: "python3", Args: []string{"k.py"}, Version: 1,
	}))

	removed, err := RemoveDynamicYAML(path, "absent")
	require.NoError(t, err)
	require.False(t, removed)

	df, err := ReadDynamicYAML(path)
	require.NoError(t, err)
	require.Contains(t, df.Servers, "keep")
}

func TestRemoveDynamicYAML_MissingFile(t *testing.T) {
	work := t.TempDir()
	path := filepath.Join(work, "dynamic_mcp.yaml")
	_, err := os.Stat(path)
	require.True(t, os.IsNotExist(err), "precondition: file must not exist")

	removed, err := RemoveDynamicYAML(path, "anything")
	require.NoError(t, err)
	require.False(t, removed)
}
