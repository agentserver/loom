package userspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/yourorg/multi-agent/internal/objectstore"
)

const (
	userspaceBlobObjectPrefix = "workspaces/userspace/blobs/"
	userspaceBlobObjectMIME   = "application/octet-stream"
)

// BlobStorage is the storage surface used by userspace package APIs.
type BlobStorage interface {
	Put(content []byte) (string, error)
	Open(sha256hex string) (io.ReadCloser, int64, error)
	Release(sha256hex string) error
}

// BlobStore is sha256-addressed file storage with refcount in SQLite.
// Two callers writing the same bytes increment the same refcount.
// On refcount → 0 the row stays (with refcount=0) but the file is removed.
// (Lazy table cleanup can be added later.)
type BlobStore struct {
	db   *sql.DB
	root string
}

var _ BlobStorage = (*BlobStore)(nil)

func NewBlobStore(db *sql.DB, root string) (*BlobStore, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &BlobStore{db: db, root: root}, nil
}

// Put writes content (if not already present) and increments refcount.
// Returns the sha256 hex digest.
func (b *BlobStore) Put(content []byte) (string, error) {
	sum := sha256.Sum256(content)
	hexsum := hex.EncodeToString(sum[:])
	path := b.pathFor(hexsum)

	var existing int
	err := b.db.QueryRow(`SELECT refcount FROM userspace_blobs WHERE sha256=?`, hexsum).Scan(&existing)
	switch {
	case err == sql.ErrNoRows:
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			return "", err
		}
		_, err = b.db.Exec(`
			INSERT INTO userspace_blobs(sha256, size_bytes, blob_path, refcount, created_at)
			VALUES(?, ?, ?, 1, ?)`,
			hexsum, len(content), filepath.Join(blobShard(hexsum), hexsum), nowUTC())
		if err != nil {
			os.Remove(path)
			return "", err
		}
		return hexsum, nil
	case err != nil:
		return "", err
	default:
		_, err = b.db.Exec(`UPDATE userspace_blobs SET refcount = refcount + 1 WHERE sha256=?`, hexsum)
		return hexsum, err
	}
}

// ErrBlobNotFound is returned by Open when the blob has refcount 0 or no row.
var ErrBlobNotFound = errors.New("userspace: blob not found")

// Open returns a ReadCloser for the blob; not found → (nil, ErrBlobNotFound).
func (b *BlobStore) Open(sha256hex string) (io.ReadCloser, int64, error) {
	var sz int64
	err := b.db.QueryRow(`SELECT size_bytes FROM userspace_blobs WHERE sha256=? AND refcount > 0`,
		sha256hex).Scan(&sz)
	if err == sql.ErrNoRows {
		return nil, 0, ErrBlobNotFound
	}
	if err != nil {
		return nil, 0, err
	}
	f, err := os.Open(b.pathFor(sha256hex))
	if err != nil {
		return nil, 0, err
	}
	return f, sz, nil
}

// Release decrements refcount. On zero, the file is unlinked; the row stays
// (refcount=0) so the next Put with the same content can recreate the file
// without losing the audit trail.
func (b *BlobStore) Release(sha256hex string) error {
	_, err := b.db.Exec(
		`UPDATE userspace_blobs SET refcount = refcount - 1
		   WHERE sha256=? AND refcount > 0`, sha256hex)
	if err != nil {
		return err
	}
	var cnt int
	err = b.db.QueryRow(`SELECT refcount FROM userspace_blobs WHERE sha256=?`, sha256hex).Scan(&cnt)
	if err != nil {
		return err
	}
	if cnt == 0 {
		return os.Remove(b.pathFor(sha256hex))
	}
	return nil
}

func (b *BlobStore) pathFor(hexsum string) string {
	return filepath.Join(b.root, blobShard(hexsum), hexsum)
}

// ObjectBlobStore is sha256-addressed storage backed by object storage, with
// SQL retaining only metadata and refcounts.
type ObjectBlobStore struct {
	db          *sql.DB
	objects     objectstore.Store
	hasBlobPath bool
}

var _ BlobStorage = (*ObjectBlobStore)(nil)

func NewObjectBlobStore(db *sql.DB, objects objectstore.Store) (*ObjectBlobStore, error) {
	if objects == nil {
		return nil, errors.New("userspace: object store required")
	}
	hasObjectKey, err := userspaceBlobColumnExists(db, "object_key")
	if err != nil {
		return nil, err
	}
	if !hasObjectKey {
		return nil, errors.New("userspace: userspace_blobs.object_key column missing")
	}
	hasBlobPath, err := userspaceBlobColumnExists(db, "blob_path")
	if err != nil {
		return nil, err
	}
	return &ObjectBlobStore{db: db, objects: objects, hasBlobPath: hasBlobPath}, nil
}

// Put writes content to object storage when needed and increments refcount.
// Returns the sha256 hex digest.
func (b *ObjectBlobStore) Put(content []byte) (string, error) {
	hexsum := ComputeSHA256Hex(content)
	key := objectBlobKey(hexsum)

	if !b.hasBlobPath {
		return b.putPostgres(content, hexsum, key)
	}
	if err := b.putObject(key, content, hexsum); err != nil {
		return "", err
	}
	if err := b.upsertObjectBlob(hexsum, len(content), key); err != nil {
		return "", err
	}
	return hexsum, nil
}

// Open returns a ReadCloser for the blob; not found → (nil, ErrBlobNotFound).
func (b *ObjectBlobStore) Open(sha256hex string) (io.ReadCloser, int64, error) {
	var sz int64
	var key string
	err := b.db.QueryRow(
		`SELECT size_bytes, object_key FROM userspace_blobs WHERE sha256=$1 AND refcount > 0`,
		sha256hex,
	).Scan(&sz, &key)
	if err == sql.ErrNoRows {
		return nil, 0, ErrBlobNotFound
	}
	if err != nil {
		return nil, 0, err
	}
	rc, err := b.objects.Open(context.Background(), key)
	if err != nil {
		return nil, 0, err
	}
	return rc, sz, nil
}

// Release decrements refcount. On zero, the object is removed while retaining
// the SQL metadata row for audit history.
func (b *ObjectBlobStore) Release(sha256hex string) error {
	if !b.hasBlobPath {
		return b.releasePostgres(sha256hex)
	}
	_, err := b.db.Exec(
		`UPDATE userspace_blobs SET refcount = refcount - 1
		   WHERE sha256=$1 AND refcount > 0`, sha256hex)
	if err != nil {
		return err
	}
	var cnt int
	var key string
	err = b.db.QueryRow(`SELECT refcount, object_key FROM userspace_blobs WHERE sha256=$1`, sha256hex).Scan(&cnt, &key)
	if err != nil {
		return err
	}
	if cnt == 0 && key != "" {
		return b.objects.Delete(context.Background(), key)
	}
	return nil
}

func (b *ObjectBlobStore) putPostgres(content []byte, hexsum, key string) (string, error) {
	tx, err := b.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback() //nolint:errcheck

	_, err = tx.Exec(`
		INSERT INTO userspace_blobs(sha256, size_bytes, object_key, refcount, created_at)
		VALUES($1, $2, $3, 0, $4)
		ON CONFLICT(sha256) DO NOTHING`,
		hexsum, len(content), key, nowUTC())
	if err != nil {
		return "", err
	}

	var refcount int
	err = tx.QueryRow(`SELECT refcount FROM userspace_blobs WHERE sha256=$1 FOR UPDATE`, hexsum).Scan(&refcount)
	if err != nil {
		return "", err
	}
	if refcount == 0 {
		if err := b.putObject(key, content, hexsum); err != nil {
			return "", err
		}
		_, err = tx.Exec(`
			UPDATE userspace_blobs
			   SET size_bytes=$2, object_key=$3, refcount=1
			 WHERE sha256=$1`,
			hexsum, len(content), key)
	} else {
		_, err = tx.Exec(`UPDATE userspace_blobs SET refcount = refcount + 1 WHERE sha256=$1`, hexsum)
	}
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return hexsum, nil
}

func (b *ObjectBlobStore) releasePostgres(sha256hex string) error {
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	var refcount int
	var key string
	err = tx.QueryRow(`SELECT refcount, object_key FROM userspace_blobs WHERE sha256=$1 FOR UPDATE`, sha256hex).Scan(&refcount, &key)
	if err != nil {
		return err
	}
	if refcount <= 0 {
		return tx.Commit()
	}

	next := refcount - 1
	_, err = tx.Exec(`UPDATE userspace_blobs SET refcount=$2 WHERE sha256=$1`, sha256hex, next)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if next == 0 && key != "" {
		return b.objects.Delete(context.Background(), key)
	}
	return nil
}

func (b *ObjectBlobStore) putObject(key string, content []byte, hexsum string) error {
	info, err := b.objects.Put(context.Background(), key, userspaceBlobObjectMIME, bytes.NewReader(content))
	if err != nil {
		return err
	}
	if info.SHA256 != "" && info.SHA256 != hexsum {
		_ = b.objects.Delete(context.Background(), key)
		return fmt.Errorf("userspace: object store sha256 mismatch for %s", key)
	}
	return nil
}

func (b *ObjectBlobStore) upsertObjectBlob(hexsum string, sizeBytes int, key string) error {
	if b.hasBlobPath {
		_, err := b.db.Exec(`
			INSERT INTO userspace_blobs(sha256, size_bytes, object_key, blob_path, refcount, created_at)
			VALUES($1, $2, $3, $3, 1, $4)
			ON CONFLICT(sha256) DO UPDATE SET
			    size_bytes = excluded.size_bytes,
			    object_key = excluded.object_key,
			    blob_path = excluded.blob_path,
			    refcount = CASE
			        WHEN userspace_blobs.refcount > 0 THEN userspace_blobs.refcount + 1
			        ELSE 1
			    END`,
			hexsum, sizeBytes, key, nowUTC())
		return err
	}
	_, err := b.db.Exec(`
		INSERT INTO userspace_blobs(sha256, size_bytes, object_key, refcount, created_at)
		VALUES($1, $2, $3, 1, $4)
		ON CONFLICT(sha256) DO UPDATE SET
		    size_bytes = excluded.size_bytes,
		    object_key = excluded.object_key,
		    refcount = CASE
		        WHEN userspace_blobs.refcount > 0 THEN userspace_blobs.refcount + 1
		        ELSE 1
		    END`,
		hexsum, sizeBytes, key, nowUTC())
	return err
}

func objectBlobKey(hexsum string) string {
	return userspaceBlobObjectPrefix + hexsum
}

func userspaceBlobColumnExists(db *sql.DB, column string) (bool, error) {
	rows, err := db.Query(fmt.Sprintf(`SELECT %s FROM userspace_blobs WHERE 1=0`, column))
	if err == nil {
		defer rows.Close()
		return true, nil
	}
	if isMissingColumnError(err) {
		return false, nil
	}
	return false, err
}

func isMissingColumnError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such column") ||
		(strings.Contains(msg, "column") && strings.Contains(msg, "does not exist"))
}

func blobShard(hexsum string) string {
	if len(hexsum) < 2 {
		return "_"
	}
	return hexsum[:2]
}

// ComputeSHA256Hex returns the hex digest of content. Helper for callers that
// want to verify a tarball matches an advertised hash.
func ComputeSHA256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
