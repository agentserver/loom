package userspace

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// TestBlobStore_PutReleasePutOpenWorks pins the §1.3 #13 PR3 fix:
// after Release-to-0 removes the on-disk file (row intentionally retained
// for audit, see Release comment), the next Put MUST recreate the file
// when it bumps refcount 0→1. Otherwise the subsequent Open opens a
// deleted path and fails.
func TestBlobStore_PutReleasePutOpenWorks(t *testing.T) {
	db := newTestDB(t)
	b, err := NewBlobStore(db, t.TempDir())
	require.NoError(t, err)
	content := []byte("test-content")

	// First Put: refcount 0→1, writes file.
	h1, err := b.Put(content)
	require.NoError(t, err)

	// Release to 0: file removed, row retained.
	require.NoError(t, b.Release(h1))

	// Second Put on same content: previously bumped refcount 0→1 but
	// didn't recreate the file, so Open would fail. Fixed: must recreate.
	h2, err := b.Put(content)
	require.NoError(t, err)
	require.Equal(t, h1, h2)

	// Refcount must now be 1 (Release dropped to 0, Put bumped to 1).
	var rc int
	require.NoError(t, db.QueryRow(`SELECT refcount FROM userspace_blobs WHERE sha256=?`, h2).Scan(&rc))
	require.Equal(t, 1, rc)

	// Open must succeed — the file was recreated by Put.
	r, sz, err := b.Open(h2)
	require.NoError(t, err, "Open after Put-Release-Put must succeed; file must have been recreated")
	defer r.Close()
	require.Equal(t, int64(len(content)), sz)
	got, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, content, got)
}

// TestBlobStore_ConcurrentPutSameSha_NoDoubleWrite pins the §1.3 #13(a)
// invariant: N goroutines Put(same content) must produce ONE blob file
// on disk and a final refcount == N. Old code (SELECT → ErrNoRows →
// WriteFile + INSERT) had a TOCTOU race that under -race manifests as
// either failures > 0 (UNIQUE constraint) or refcount < N (concurrent
// updates lost).
func TestBlobStore_ConcurrentPutSameSha_NoDoubleWrite(t *testing.T) {
	db := newTestDB(t)
	// modernc.org/sqlite `:memory:` is per-connection; pin to a single conn
	// so all goroutines see the same schema/data.
	db.SetMaxOpenConns(1)
	b, err := NewBlobStore(db, t.TempDir())
	if err != nil {
		t.Fatalf("NewBlobStore: %v", err)
	}
	content := []byte("hello-world-blob-content")
	const N = 20
	var wg sync.WaitGroup
	var failures int32
	var hexsum string
	var hexsumMu sync.Mutex
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h, err := b.Put(content)
			if err != nil {
				atomic.AddInt32(&failures, 1)
				t.Logf("put err: %v", err)
				return
			}
			hexsumMu.Lock()
			hexsum = h
			hexsumMu.Unlock()
		}()
	}
	wg.Wait()
	if failures > 0 {
		t.Fatalf("concurrent Put failures: %d (must be 0)", failures)
	}
	// Final refcount must equal N.
	var rc int
	if err := db.QueryRow(
		`SELECT refcount FROM userspace_blobs WHERE sha256=?`, hexsum).Scan(&rc); err != nil {
		t.Fatalf("refcount query: %v", err)
	}
	if rc != N {
		t.Fatalf("expected refcount=%d, got %d (TOCTOU: concurrent Put didn't atomically bump)", N, rc)
	}
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

func TestObjectBlobStoreReleasePostgresDeletesOnlyAfterCommit(t *testing.T) {
	db, rec := newReleaseOrderDB(t, releaseOrderConfig{
		refcount: 1,
		key:      "workspaces/userspace/blobs/sha-release",
	})
	objects := &releaseOrderObjectStore{Memory: objectstore.NewMemory(), rec: rec}
	b := &ObjectBlobStore{db: db, objects: objects}

	require.NoError(t, b.releasePostgres("sha-release"))

	require.Equal(t, []string{"begin", "select", "update", "commit", "begin", "select", "delete", "commit"}, rec.Events())
}

func TestObjectBlobStoreReleasePostgresDoesNotDeleteWhenCommitFails(t *testing.T) {
	db, rec := newReleaseOrderDB(t, releaseOrderConfig{
		refcount:  1,
		key:       "workspaces/userspace/blobs/sha-release",
		commitErr: errors.New("commit failed"),
	})
	objects := &releaseOrderObjectStore{Memory: objectstore.NewMemory(), rec: rec}
	b := &ObjectBlobStore{db: db, objects: objects}

	err := b.releasePostgres("sha-release")
	require.ErrorContains(t, err, "commit failed")
	require.NotContains(t, rec.Events(), "delete")
	require.Equal(t, []string{"begin", "select", "update", "commit"}, rec.Events())
}

func TestObjectBlobStoreReleasePostgresSkipsDeleteWhenBlobRecreatedAfterDecrement(t *testing.T) {
	db, rec := newReleaseOrderDB(t, releaseOrderConfig{
		refcount:                 1,
		key:                      "workspaces/userspace/blobs/sha-release",
		recreateAfterFirstCommit: true,
	})
	objects := &releaseOrderObjectStore{Memory: objectstore.NewMemory(), rec: rec}
	b := &ObjectBlobStore{db: db, objects: objects}

	require.NoError(t, b.releasePostgres("sha-release"))

	require.NotContains(t, rec.Events(), "delete")
	require.Equal(t, []string{"begin", "select", "update", "commit", "recreate", "begin", "select", "commit"}, rec.Events())
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

type releaseOrderConfig struct {
	refcount                 int
	key                      string
	commitErr                error
	recreateAfterFirstCommit bool
}

type releaseOrderRecorder struct {
	mu                       sync.Mutex
	events                   []string
	refcount                 int
	key                      string
	commitErr                error
	recreateAfterFirstCommit bool
	commits                  int
}

func (r *releaseOrderRecorder) Add(event string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *releaseOrderRecorder) Events() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

type releaseOrderObjectStore struct {
	*objectstore.Memory
	rec *releaseOrderRecorder
}

func (s *releaseOrderObjectStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.rec.Add("delete")
	return nil
}

var (
	releaseOrderSQLOnce      sync.Once
	releaseOrderSQLRecorders sync.Map
)

func newReleaseOrderDB(t *testing.T, cfg releaseOrderConfig) (*sql.DB, *releaseOrderRecorder) {
	t.Helper()
	releaseOrderSQLOnce.Do(func() {
		sql.Register("userspace_release_order_sql", releaseOrderDriver{})
	})
	name := t.Name() + "_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	rec := &releaseOrderRecorder{
		refcount:                 cfg.refcount,
		key:                      cfg.key,
		commitErr:                cfg.commitErr,
		recreateAfterFirstCommit: cfg.recreateAfterFirstCommit,
	}
	releaseOrderSQLRecorders.Store(name, rec)
	t.Cleanup(func() {
		releaseOrderSQLRecorders.Delete(name)
	})

	db, err := sql.Open("userspace_release_order_sql", name)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db, rec
}

type releaseOrderDriver struct{}

func (releaseOrderDriver) Open(name string) (driver.Conn, error) {
	value, ok := releaseOrderSQLRecorders.Load(name)
	if !ok {
		return nil, errors.New("release order recorder not found")
	}
	return &releaseOrderConn{rec: value.(*releaseOrderRecorder)}, nil
}

type releaseOrderConn struct {
	rec *releaseOrderRecorder
}

func (c *releaseOrderConn) Prepare(query string) (driver.Stmt, error) {
	return nil, errors.New("release order prepare is not implemented")
}

func (c *releaseOrderConn) Close() error { return nil }

func (c *releaseOrderConn) Begin() (driver.Tx, error) {
	c.rec.Add("begin")
	return &releaseOrderTx{rec: c.rec}, nil
}

func (c *releaseOrderConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.rec.mu.Lock()
	defer c.rec.mu.Unlock()
	c.rec.events = append(c.rec.events, "update")
	if len(args) >= 2 {
		switch v := args[1].Value.(type) {
		case int:
			c.rec.refcount = v
		case int64:
			c.rec.refcount = int(v)
		}
	}
	return driver.RowsAffected(1), nil
}

func (c *releaseOrderConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.rec.Add("select")
	return &releaseOrderRows{rec: c.rec}, nil
}

type releaseOrderTx struct {
	rec *releaseOrderRecorder
}

func (tx *releaseOrderTx) Commit() error {
	tx.rec.mu.Lock()
	defer tx.rec.mu.Unlock()
	tx.rec.events = append(tx.rec.events, "commit")
	tx.rec.commits++
	if tx.rec.recreateAfterFirstCommit && tx.rec.commits == 1 {
		tx.rec.refcount = 1
		tx.rec.events = append(tx.rec.events, "recreate")
	}
	return tx.rec.commitErr
}

func (tx *releaseOrderTx) Rollback() error {
	tx.rec.Add("rollback")
	return nil
}

type releaseOrderRows struct {
	rec  *releaseOrderRecorder
	read bool
}

func (r *releaseOrderRows) Columns() []string {
	return []string{"refcount", "object_key"}
}

func (r *releaseOrderRows) Close() error { return nil }

func (r *releaseOrderRows) Next(dest []driver.Value) error {
	if r.read {
		return io.EOF
	}
	r.read = true
	dest[0] = int64(r.rec.refcount)
	dest[1] = r.rec.key
	return nil
}
