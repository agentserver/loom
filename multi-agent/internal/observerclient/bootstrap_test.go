package observerclient

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWriteTokenFileSetsMode0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observer.token")
	require.NoError(t, writeTokenFile(path, "tk_abc123"))

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "tk_abc123", string(got))
}

func TestWriteTokenFileTruncatesExistingContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observer.token")
	require.NoError(t, os.WriteFile(path, []byte("OLD_LONG_CONTENT_xxxxxxxxxxx"), 0o600))

	require.NoError(t, writeTokenFile(path, "new_short"))

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "new_short", string(got))
}

func TestReadTokenFileTrimsWhitespace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observer.token")
	require.NoError(t, os.WriteFile(path, []byte("  tk_xyz789\n"), 0o600))

	got, ok, err := readTokenFile(path)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "tk_xyz789", got)
}

func TestReadTokenFileMissingReturnsNotOk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nope.token")

	_, ok, err := readTokenFile(path)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestReadTokenFileEmptyReturnsNotOk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observer.token")
	require.NoError(t, os.WriteFile(path, []byte("   \n\t"), 0o600))

	_, ok, err := readTokenFile(path)
	require.NoError(t, err)
	require.False(t, ok, "whitespace-only file should be treated as missing")
}
