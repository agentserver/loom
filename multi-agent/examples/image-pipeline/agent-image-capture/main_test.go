package main

import (
	"context"
	"image"
	_ "image/png"
	"strings"
	"testing"

	"github.com/yourorg/multi-agent/pkg/transport"
	httpx "github.com/yourorg/multi-agent/pkg/transport/http"
)

func TestRunCapture(t *testing.T) {
	srv, err := httpx.New(httpx.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	out, err := runCapture(context.Background(), srv)
	if err != nil {
		t.Fatal(err)
	}
	h, ok := transport.ParseHandle(out)
	if !ok {
		t.Fatalf("output is not a Handle JSON: %q", out)
	}
	if h.Type != "image_url" {
		t.Fatalf("Type = %q, want image_url", h.Type)
	}
	if h.MIME != "image/png" {
		t.Fatalf("MIME = %q, want image/png", h.MIME)
	}
	if h.Bytes < 100 {
		t.Fatalf("Bytes = %d, want >= 100", h.Bytes)
	}
	if !strings.HasPrefix(h.URL, srv.PublicURL()+"/blobs/") {
		t.Fatalf("URL = %q, want prefix %q", h.URL, srv.PublicURL()+"/blobs/")
	}
	rc, err := srv.Get(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	img, _, err := image.Decode(rc)
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 256 || img.Bounds().Dy() != 256 {
		t.Fatalf("image bounds %v want 256x256", img.Bounds())
	}
}
