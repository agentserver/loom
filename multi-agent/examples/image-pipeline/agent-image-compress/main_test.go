package main

import (
	"bytes"
	"context"
	"image"
	_ "image/jpeg"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yourorg/multi-agent/examples/image-pipeline/internal/imageops"
	"github.com/yourorg/multi-agent/pkg/transport"
	httpx "github.com/yourorg/multi-agent/pkg/transport/http"
)

func TestRunCompress(t *testing.T) {
	pngBytes, _ := imageops.SynthPNG(256, 256, 42)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(pngBytes)
	}))
	defer upstream.Close()

	srv, _ := httpx.New(httpx.Options{})
	defer srv.Close()

	prompt := "Compress this image: " + upstream.URL + "/img.png at quality=50"
	out, err := runCompress(context.Background(), srv, prompt)
	if err != nil {
		t.Fatal(err)
	}
	h, ok := transport.ParseHandle(out)
	if !ok {
		t.Fatalf("output not a Handle: %q", out)
	}
	if h.MIME != "image/jpeg" {
		t.Fatalf("MIME = %q, want image/jpeg", h.MIME)
	}
	if h.Bytes >= int64(len(pngBytes)) {
		t.Fatalf("compressed bytes %d not less than input %d", h.Bytes, len(pngBytes))
	}
	if !strings.HasPrefix(h.URL, srv.PublicURL()+"/blobs/") {
		t.Fatalf("URL = %q, want prefix %q", h.URL, srv.PublicURL()+"/blobs/")
	}
	rc, err := srv.Get(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	body := bytes.Buffer{}
	body.ReadFrom(rc)
	if _, _, err := image.Decode(&body); err != nil {
		t.Fatalf("compressed not decodable as image: %v", err)
	}
}

func TestRunCompress_NoURL(t *testing.T) {
	srv, _ := httpx.New(httpx.Options{})
	defer srv.Close()
	_, err := runCompress(context.Background(), srv, "compress something but I forgot the URL")
	if err == nil {
		t.Fatal("expected error for prompt with no URL")
	}
}

func TestRunCompress_BadHTTP(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", 500)
	}))
	defer bad.Close()
	srv, _ := httpx.New(httpx.Options{})
	defer srv.Close()
	_, err := runCompress(context.Background(), srv, "compress: "+bad.URL+"/x")
	if err == nil {
		t.Fatal("expected error for upstream 500")
	}
}
