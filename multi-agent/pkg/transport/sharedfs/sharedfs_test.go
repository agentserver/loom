package sharedfs

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/yourorg/multi-agent/pkg/transport"
)

func TestPutGet_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	fs, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	ctx := context.Background()
	payload := []byte("payload bytes")
	h, err := fs.Put(ctx, "application/octet-stream", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h.URL, "file://") {
		t.Fatalf("expected file:// URL, got %q", h.URL)
	}
	if h.Bytes != int64(len(payload)) || h.MIME != "application/octet-stream" {
		t.Fatalf("unexpected handle: %+v", h)
	}
	rc, err := fs.Get(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, payload) {
		t.Fatalf("got %q want %q", got, payload)
	}
}

func TestPut_Dedupes(t *testing.T) {
	dir := t.TempDir()
	fs, _ := New(dir)
	defer fs.Close()
	ctx := context.Background()
	h1, _ := fs.Put(ctx, "text/plain", bytes.NewReader([]byte("dup")))
	h2, _ := fs.Put(ctx, "text/plain", bytes.NewReader([]byte("dup")))
	if h1.URL != h2.URL {
		t.Fatalf("dedupe failed: %q vs %q", h1.URL, h2.URL)
	}
}

func TestGet_TraversalGuard(t *testing.T) {
	dir := t.TempDir()
	fs, _ := New(dir)
	defer fs.Close()
	ctx := context.Background()
	// Try to read /etc/passwd via a crafted file:// URL.
	_, err := fs.Get(ctx, transport.Handle{URL: "file:///etc/passwd"})
	if err == nil {
		t.Fatal("expected error rejecting outside-dir path")
	}
}

func TestGet_Missing(t *testing.T) {
	dir := t.TempDir()
	fs, _ := New(dir)
	defer fs.Close()
	ctx := context.Background()
	_, err := fs.Get(ctx, transport.Handle{URL: "file://" + dir + "/nonexistent"})
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}
