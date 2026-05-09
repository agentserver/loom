package httpx

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/yourorg/multi-agent/pkg/transport"
)

func TestPutGet_RoundTrip(t *testing.T) {
	srv, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	ctx := context.Background()
	payload := []byte("hello world")
	h, err := srv.Put(ctx, "text/plain", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if h.URL == "" || h.Bytes != int64(len(payload)) || h.MIME != "text/plain" {
		t.Fatalf("unexpected handle: %+v", h)
	}
	rc, err := srv.Get(ctx, h)
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
	srv, _ := New(Options{})
	defer srv.Close()
	ctx := context.Background()
	h1, _ := srv.Put(ctx, "application/octet-stream", bytes.NewReader([]byte("same")))
	h2, _ := srv.Put(ctx, "application/octet-stream", bytes.NewReader([]byte("same")))
	if h1.URL != h2.URL {
		t.Fatalf("dedupe failed: %q vs %q", h1.URL, h2.URL)
	}
}

func TestGet_NotFound(t *testing.T) {
	srv, _ := New(Options{})
	defer srv.Close()
	ctx := context.Background()
	_, err := srv.Get(ctx, transport.Handle{URL: srv.PublicURL() + "/blobs/deadbeef0000dead"})
	if err == nil {
		t.Fatal("expected error for missing blob")
	}
}

func TestClose_StopsServer(t *testing.T) {
	srv, _ := New(Options{})
	addr := srv.Addr()
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get("http://" + addr + "/blobs/anything")
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected dial error after Close")
	}
}

func TestPut_ConcurrentRace(t *testing.T) {
	srv, _ := New(Options{})
	defer srv.Close()
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			payload := []byte("payload-" + strings.Repeat("x", i))
			if _, err := srv.Put(ctx, "application/octet-stream", bytes.NewReader(payload)); err != nil {
				t.Errorf("put %d: %v", i, err)
			}
		}()
	}
	wg.Wait()
}

func TestPublicURL_Override(t *testing.T) {
	srv, _ := New(Options{PublicURL: "http://override.example/prefix"})
	defer srv.Close()
	if !strings.HasPrefix(srv.PublicURL(), "http://override.example/prefix") {
		t.Fatalf("PublicURL not honored: got %q", srv.PublicURL())
	}
	ctx := context.Background()
	h, _ := srv.Put(ctx, "text/plain", strings.NewReader("hi"))
	if !strings.HasPrefix(h.URL, "http://override.example/prefix/blobs/") {
		t.Fatalf("URL prefix not applied: got %q", h.URL)
	}
}
