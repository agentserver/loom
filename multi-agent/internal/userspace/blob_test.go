package userspace

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/objectstore"
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

func TestBlobStoreUsesObjectKeyWhenConfigured(t *testing.T) {
	db := newTestDB(t)
	require.NoError(t, Migrate(db))
	objects := objectstore.NewMemory()
	b, err := NewObjectBlobStore(db, objects)
	require.NoError(t, err)
	sha, err := b.Put([]byte("hello"))
	require.NoError(t, err)
	require.Equal(t, ComputeSHA256Hex([]byte("hello")), sha)
}

func TestNewObjectBlobStoreRequiresObjectStore(t *testing.T) {
	db := newTestDB(t)
	b, err := NewObjectBlobStore(db, nil)
	require.Nil(t, b)
	require.ErrorContains(t, err, "object store required")
}

func TestObjectBlobStoreStoresObjectKeyAndOpensContent(t *testing.T) {
	db := newTestDB(t)
	objects := objectstore.NewMemory()
	b, err := NewObjectBlobStore(db, objects)
	require.NoError(t, err)

	sha, err := b.Put([]byte("hello"))
	require.NoError(t, err)

	wantKey := "workspaces/userspace/blobs/" + sha
	var objectKey string
	require.NoError(t, db.QueryRow(`SELECT object_key FROM userspace_blobs WHERE sha256=?`, sha).Scan(&objectKey))
	require.Equal(t, wantKey, objectKey)

	rc, sz, err := b.Open(sha)
	require.NoError(t, err)
	defer rc.Close()
	require.Equal(t, int64(5), sz)
	body, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, "hello", string(body))
}

func TestObjectBlobStoreDuplicateInsertRaceKeepsSharedObject(t *testing.T) {
	db := newTestDB(t)
	base := objectstore.NewMemory()
	objects := &insertBlobRowAfterPutStore{Store: base, db: db}
	b, err := NewObjectBlobStore(db, objects)
	require.NoError(t, err)

	sha, err := b.Put([]byte("race"))
	require.NoError(t, err)
	require.Equal(t, ComputeSHA256Hex([]byte("race")), sha)

	var refcount int
	require.NoError(t, db.QueryRow(`SELECT refcount FROM userspace_blobs WHERE sha256=?`, sha).Scan(&refcount))
	require.Equal(t, 2, refcount)

	key := "workspaces/userspace/blobs/" + sha
	rc, err := base.Open(context.Background(), key)
	require.NoError(t, err)
	defer rc.Close()
	body, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, "race", string(body))
}

type insertBlobRowAfterPutStore struct {
	objectstore.Store
	db   *sql.DB
	once sync.Once
	err  error
}

func (s *insertBlobRowAfterPutStore) Put(ctx context.Context, key, mime string, body io.Reader) (objectstore.ObjectInfo, error) {
	info, err := s.Store.Put(ctx, key, mime, body)
	if err != nil {
		return info, err
	}
	s.once.Do(func() {
		_, s.err = s.db.Exec(`
			INSERT INTO userspace_blobs(sha256, size_bytes, object_key, blob_path, refcount, created_at)
			VALUES(?, ?, ?, ?, 1, ?)`,
			info.SHA256, info.Bytes, key, key, nowUTC())
	})
	return info, s.err
}
