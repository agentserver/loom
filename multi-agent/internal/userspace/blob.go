package userspace

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// BlobStore is sha256-addressed file storage with refcount in SQLite.
// Two callers writing the same bytes increment the same refcount.
// On refcount → 0 the row stays (with refcount=0) but the file is removed.
// (Lazy table cleanup can be added later.)
type BlobStore struct {
	db   *sql.DB
	root string
}

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
