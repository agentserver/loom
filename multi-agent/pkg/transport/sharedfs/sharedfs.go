// Package sharedfs is a reference Transport implementation backed by a local
// directory. Useful when the producer and consumer share a filesystem and
// you want to avoid a network hop. Demonstrates that pkg/transport.Transport
// is genuinely substitutable; mostly here as a contrast to pkg/transport/http.
package sharedfs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/yourorg/multi-agent/pkg/transport"
)

// FS is a Transport rooted at a local directory. Safe for concurrent use.
type FS struct {
	dir string
}

// New ensures dir exists (mkdir -p) and returns a new FS.
func New(dir string) (*FS, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, err
	}
	return &FS{dir: abs}, nil
}

// Put writes data to dir/<sha256-prefix>, returning a file:// Handle.
func (f *FS) Put(_ context.Context, mime string, data io.Reader) (transport.Handle, error) {
	buf, err := io.ReadAll(data)
	if err != nil {
		return transport.Handle{}, fmt.Errorf("read: %w", err)
	}
	sum := sha256.Sum256(buf)
	id := hex.EncodeToString(sum[:])[:16]
	path := filepath.Join(f.dir, id)
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		return transport.Handle{}, fmt.Errorf("write: %w", err)
	}
	return transport.Handle{
		URL:   "file://" + path,
		Bytes: int64(len(buf)),
		MIME:  mime,
	}, nil
}

// Get opens the file referenced by h, refusing paths that fall outside dir.
func (f *FS) Get(_ context.Context, h transport.Handle) (io.ReadCloser, error) {
	if !strings.HasPrefix(h.URL, "file://") {
		return nil, fmt.Errorf("not a file:// URL: %s", h.URL)
	}
	raw := strings.TrimPrefix(h.URL, "file://")
	abs, err := filepath.Abs(raw)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(f.dir, abs)
	if err != nil || strings.HasPrefix(rel, "..") || rel == ".." {
		return nil, errors.New("path outside transport root")
	}
	return os.Open(abs)
}

// Close is a no-op (the OS owns the files).
func (f *FS) Close() error { return nil }
