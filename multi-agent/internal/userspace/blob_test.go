package userspace

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBlobStore_PutOpenRoundTrip(t *testing.T) {
	db := newTestDB(t)
	root := t.TempDir()
	b, err := NewBlobStore(db, root)
	require.NoError(t, err)
	sum, err := b.Put([]byte("hello"))
	require.NoError(t, err)
	require.Equal(t, ComputeSHA256Hex([]byte("hello")), sum)

	rc, sz, err := b.Open(sum)
	require.NoError(t, err)
	defer rc.Close()
	require.Equal(t, int64(5), sz)
	body, _ := io.ReadAll(rc)
	require.Equal(t, "hello", string(body))
}

func TestBlobStore_DedupIncrementsRefcount(t *testing.T) {
	db := newTestDB(t)
	b, _ := NewBlobStore(db, t.TempDir())
	sum, _ := b.Put([]byte("dup"))
	_, _ = b.Put([]byte("dup"))
	var rc int
	require.NoError(t, db.QueryRow(`SELECT refcount FROM userspace_blobs WHERE sha256=?`, sum).Scan(&rc))
	require.Equal(t, 2, rc)
}

func TestBlobStore_ReleaseToZeroRemovesFile(t *testing.T) {
	db := newTestDB(t)
	root := t.TempDir()
	b, _ := NewBlobStore(db, root)
	sum, _ := b.Put([]byte("temp"))
	require.NoError(t, b.Release(sum))
	_, err := os.Stat(filepath.Join(root, blobShard(sum), sum))
	require.True(t, os.IsNotExist(err))
	var rc int
	require.NoError(t, db.QueryRow(`SELECT refcount FROM userspace_blobs WHERE sha256=?`, sum).Scan(&rc))
	require.Equal(t, 0, rc)
}

func TestBlobStore_OpenZeroRefcountFails(t *testing.T) {
	db := newTestDB(t)
	b, _ := NewBlobStore(db, t.TempDir())
	sum, _ := b.Put([]byte("x"))
	_ = b.Release(sum)
	_, _, err := b.Open(sum)
	require.ErrorIs(t, err, ErrBlobNotFound)
}
