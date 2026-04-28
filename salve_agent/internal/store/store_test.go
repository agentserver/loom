package store

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpen_CreatesSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	s, err := Open(path)
	require.NoError(t, err)
	defer s.Close()

	var n int
	require.NoError(t, s.DB().QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('tasks','task_chunks','pending_acks')`,
	).Scan(&n))
	require.Equal(t, 3, n)
}
