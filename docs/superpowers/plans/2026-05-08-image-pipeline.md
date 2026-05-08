# Image-Pipeline E2E Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stand up a live end-to-end test that pipes "capture an image, then compress it" through `master-agent` to two custom agentsdk-based workspace agents. Bake in a small `pkg/transport` library so future sub-task agents have a documented side-channel for large/binary artifacts.

**Architecture:** Two new directories, no changes to existing code. `multi-agent/pkg/transport/` is a reusable Go library (Handle struct + Transport interface + http and sharedfs reference impls). `multi-agent/examples/image-pipeline/` contains two custom agents (built directly on `agentserver/pkg/agentsdk`, *not* on `cmd/slave-agent`), an e2e driver binary, supporting unit-testable internals (`imageops`, `handlepick`, `agentboot`), and a bash script to wire them all up against a real `agentserver` + real `claude` (planner/reducer only).

**Tech Stack:** Go 1.22+, `github.com/agentserver/agentserver/pkg/agentsdk`, `github.com/yourorg/multi-agent/pkg/transport` (new), `gopkg.in/yaml.v3`, `image/png`, `image/jpeg`, stdlib `net/http` and `database/sql` + `modernc.org/sqlite` (already in go.sum). Reuses existing `cmd/master-agent` unchanged.

**Spec:** `docs/superpowers/specs/2026-05-08-image-pipeline-e2e-design.md`

---

## File structure

| File | Purpose |
|---|---|
| `multi-agent/pkg/transport/transport.go` | Handle struct + Transport interface + Marshal / ParseHandle |
| `multi-agent/pkg/transport/transport_test.go` | Round-trip + ParseHandle fallback tests |
| `multi-agent/pkg/transport/http/http.go` | In-process HTTP server, Put/Get/Close |
| `multi-agent/pkg/transport/http/http_test.go` | round-trip, dedupe, Close, race |
| `multi-agent/pkg/transport/sharedfs/sharedfs.go` | local-FS Put/Get |
| `multi-agent/pkg/transport/sharedfs/sharedfs_test.go` | round-trip, dedupe, traversal guard |
| `multi-agent/examples/image-pipeline/internal/imageops/imageops.go` | SynthPNG + EncodeJPEG |
| `multi-agent/examples/image-pipeline/internal/imageops/imageops_test.go` | round-trip + size shrink |
| `multi-agent/examples/image-pipeline/internal/handlepick/handlepick.go` | FirstURL regex |
| `multi-agent/examples/image-pipeline/internal/handlepick/handlepick_test.go` | regex cases |
| `multi-agent/examples/image-pipeline/internal/agentboot/agentboot.go` | shared register/card/Connect helper + Config struct |
| `multi-agent/examples/image-pipeline/agent-image-capture/main.go` | task handler binary |
| `multi-agent/examples/image-pipeline/agent-image-capture/main_test.go` | runCapture helper test |
| `multi-agent/examples/image-pipeline/agent-image-capture/config.example.yaml` | doc shape |
| `multi-agent/examples/image-pipeline/agent-image-compress/main.go` | task handler binary |
| `multi-agent/examples/image-pipeline/agent-image-compress/main_test.go` | runCompress helper test |
| `multi-agent/examples/image-pipeline/agent-image-compress/config.example.yaml` | doc shape |
| `multi-agent/examples/image-pipeline/e2e-driver/main.go` | discover → delegate → wait → assert |
| `multi-agent/examples/image-pipeline/scripts/e2e.sh` | bash orchestrator |
| `multi-agent/examples/image-pipeline/README.md` | how to set up + run e2e |

All paths are absolute from the repo root `/mnt/c/Users/DELL/multi-agent`. The Go module root is `/mnt/c/Users/DELL/multi-agent/multi-agent` (note: nested directory). All `go` commands below run from the module root.

---

## Task 1: pkg/transport core (Handle + Marshal/ParseHandle)

**Files:**
- Create: `multi-agent/pkg/transport/transport.go`
- Create: `multi-agent/pkg/transport/transport_test.go`

- [ ] **Step 1: Write the failing test**

Create `multi-agent/pkg/transport/transport_test.go`:

```go
package transport

import (
	"reflect"
	"testing"
)

func TestHandle_MarshalRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		h    Handle
	}{
		{"minimal", Handle{Type: "image_url", URL: "http://x/y"}},
		{"with_bytes_mime", Handle{Type: "image_url", URL: "http://x/y", Bytes: 123, MIME: "image/png"}},
		{"with_meta", Handle{Type: "image_url", URL: "http://x/y", Bytes: 99, MIME: "image/jpeg", Meta: map[string]string{"original_bytes": "200", "ratio": "0.49"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := c.h.Marshal()
			got, ok := ParseHandle(s)
			if !ok {
				t.Fatalf("ParseHandle failed for %q", s)
			}
			if !reflect.DeepEqual(got, c.h) {
				t.Fatalf("round-trip mismatch:\n got:  %+v\n want: %+v", got, c.h)
			}
		})
	}
}

func TestParseHandle_FallbackCases(t *testing.T) {
	cases := []string{
		"",
		"not json at all",
		`{"foo":"bar"}`,                        // missing type and url
		`{"type":"image_url"}`,                 // missing url
		`{"url":"http://x/y"}`,                 // missing type
		`{"type":"","url":"http://x/y"}`,       // empty type
		`{"type":"image_url","url":""}`,        // empty url
	}
	for _, c := range cases {
		if _, ok := ParseHandle(c); ok {
			t.Errorf("ParseHandle(%q) returned ok=true; want false", c)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run from `/mnt/c/Users/DELL/multi-agent/multi-agent`:
```bash
go test ./pkg/transport/...
```
Expected: build failure with `no Go files` or `transport: no such package`.

- [ ] **Step 3: Write minimal implementation**

Create `multi-agent/pkg/transport/transport.go`:

```go
// Package transport provides a small library for moving sub-task artifacts
// out of the {{nX.output}} prompt-template channel and onto a side channel.
//
// A producer Puts bytes via a Transport and gets back a Handle (a small JSON
// document carrying a URL/path). It returns Handle.Marshal() as its task
// output. The framework substitutes that string into the next node's prompt
// as usual; the consumer ParseHandles the substituted text and Gets the bytes
// back.
//
// This package is NOT imported by multi-agent/internal/* — the framework is
// transport-agnostic on purpose.
package transport

import (
	"context"
	"encoding/json"
	"io"
)

// Handle is the small JSON-serializable descriptor that travels through the
// {{nX.output}} template path. Bytes themselves move via the side channel
// referenced by URL.
type Handle struct {
	Type  string            `json:"type"`            // caller-defined: image_url, blob_url, ...
	URL   string            `json:"url"`             // dereferencing locator
	Bytes int64             `json:"bytes,omitempty"` // size hint
	MIME  string            `json:"mime,omitempty"`  // e.g. image/png
	Meta  map[string]string `json:"meta,omitempty"`  // free-form
}

// Marshal returns the canonical one-line JSON form. Always succeeds.
func (h Handle) Marshal() string {
	b, _ := json.Marshal(h)
	return string(b)
}

// ParseHandle attempts to interpret s as a Handle JSON document. Returns
// (zero, false) if s is not JSON or lacks the required Type/URL fields, so
// callers can transparently fall back to treating s as plain text.
func ParseHandle(s string) (Handle, bool) {
	var h Handle
	if err := json.Unmarshal([]byte(s), &h); err != nil {
		return Handle{}, false
	}
	if h.Type == "" || h.URL == "" {
		return Handle{}, false
	}
	return h, true
}

// Transport stores and retrieves opaque byte payloads. Producers Put bytes
// and receive a Handle (with empty Type — caller fills it in); consumers Get
// bytes from a Handle.
type Transport interface {
	Put(ctx context.Context, mime string, data io.Reader) (Handle, error)
	Get(ctx context.Context, h Handle) (io.ReadCloser, error)
	io.Closer
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./pkg/transport/
```
Expected: `ok  github.com/yourorg/multi-agent/pkg/transport`.

- [ ] **Step 5: Commit**

```bash
cd /mnt/c/Users/DELL/multi-agent
git add multi-agent/pkg/transport/transport.go multi-agent/pkg/transport/transport_test.go
git commit -m "$(cat <<'EOF'
feat(pkg/transport): Handle struct + Marshal/ParseHandle convention

Defines the side-channel pattern documented in the image-pipeline spec:
producers return Handle.Marshal() as their sub-task output; consumers
ParseHandle the substituted prompt text. Pure types + JSON helpers,
no transport implementation yet.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: pkg/transport/http reference implementation

**Files:**
- Create: `multi-agent/pkg/transport/http/http.go`
- Create: `multi-agent/pkg/transport/http/http_test.go`

- [ ] **Step 1: Write the failing test**

Create `multi-agent/pkg/transport/http/http_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./pkg/transport/http/
```
Expected: build failure with `no Go files` or symbol-not-found errors.

- [ ] **Step 3: Write minimal implementation**

Create `multi-agent/pkg/transport/http/http.go`:

```go
// Package httpx is a reference Transport implementation that exposes blobs
// over an in-process HTTP server. Each producer creates its own Server and
// the URLs returned by Put are valid until Close is called (or the process
// dies).
package httpx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/yourorg/multi-agent/pkg/transport"
)

// Options configures Server. All fields are optional.
type Options struct {
	Addr      string // bind addr; default "127.0.0.1:0" (random port)
	PublicURL string // override URL prefix returned by Put; default "http://" + bound addr
}

// Server is a Transport backed by an in-process HTTP listener and an in-memory
// blob map. Safe for concurrent use.
type Server struct {
	addr      string
	publicURL string
	srv       *http.Server
	listener  net.Listener

	mu    sync.RWMutex
	blobs map[string]blob
}

type blob struct {
	mime string
	data []byte
}

// New starts the listener immediately so Put/Get and Addr work as soon as it
// returns. Caller must Close to release the port.
func New(opts Options) (*Server, error) {
	addr := opts.Addr
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", addr, err)
	}
	s := &Server{
		addr:      ln.Addr().String(),
		publicURL: opts.PublicURL,
		listener:  ln,
		blobs:     make(map[string]blob),
	}
	if s.publicURL == "" {
		s.publicURL = "http://" + s.addr
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/blobs/", s.handle)
	s.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

// Addr returns host:port the listener is bound to.
func (s *Server) Addr() string { return s.addr }

// PublicURL returns the URL prefix used when minting handles.
func (s *Server) PublicURL() string { return s.publicURL }

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/blobs/")
	s.mu.RLock()
	b, ok := s.blobs[id]
	s.mu.RUnlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", b.mime)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(b.data)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(b.data)
}

// Put reads all bytes from data, stores under sha256-prefix id, and returns
// a Handle whose URL refers back to this server. Type is left empty for the
// caller to fill in.
func (s *Server) Put(_ context.Context, mime string, data io.Reader) (transport.Handle, error) {
	buf, err := io.ReadAll(data)
	if err != nil {
		return transport.Handle{}, fmt.Errorf("read: %w", err)
	}
	sum := sha256.Sum256(buf)
	id := hex.EncodeToString(sum[:])[:16]
	s.mu.Lock()
	s.blobs[id] = blob{mime: mime, data: buf}
	s.mu.Unlock()
	return transport.Handle{
		URL:   s.publicURL + "/blobs/" + id,
		Bytes: int64(len(buf)),
		MIME:  mime,
	}, nil
}

// Get fetches the bytes referenced by h. h.URL must point at this server (or
// any URL reachable via http.Get). Errors on non-2xx.
func (s *Server) Get(ctx context.Context, h transport.Handle) (io.ReadCloser, error) {
	if h.URL == "" {
		return nil, errors.New("empty URL")
	}
	// If the handle points at this server, short-circuit by reading the map.
	if strings.HasPrefix(h.URL, s.publicURL+"/blobs/") {
		id := path.Base(h.URL)
		s.mu.RLock()
		b, ok := s.blobs[id]
		s.mu.RUnlock()
		if !ok {
			return nil, fmt.Errorf("blob %s not found", id)
		}
		return io.NopCloser(bytes.NewReader(b.data)), nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		resp.Body.Close()
		return nil, fmt.Errorf("get %s: %s", h.URL, resp.Status)
	}
	return resp.Body, nil
}

// Close stops the server and releases the port.
func (s *Server) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.srv.Shutdown(ctx)
}
```

- [ ] **Step 4: Run test to verify it passes (with race)**

```bash
go test -race ./pkg/transport/http/
```
Expected: `ok  github.com/yourorg/multi-agent/pkg/transport/http`.

- [ ] **Step 5: Commit**

```bash
cd /mnt/c/Users/DELL/multi-agent
git add multi-agent/pkg/transport/http/
git commit -m "$(cat <<'EOF'
feat(pkg/transport/http): in-process HTTP transport

Each Server runs an HTTP listener on a configurable addr (default
127.0.0.1:0) and stores blobs in an in-memory content-addressed map
keyed by sha256-prefix. Put returns a Handle whose URL points back
at the server; Get short-circuits same-server reads from the map and
falls back to http.Get otherwise.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: pkg/transport/sharedfs reference implementation

**Files:**
- Create: `multi-agent/pkg/transport/sharedfs/sharedfs.go`
- Create: `multi-agent/pkg/transport/sharedfs/sharedfs_test.go`

- [ ] **Step 1: Write the failing test**

Create `multi-agent/pkg/transport/sharedfs/sharedfs_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./pkg/transport/sharedfs/
```
Expected: build failure (no Go files in package).

- [ ] **Step 3: Write minimal implementation**

Create `multi-agent/pkg/transport/sharedfs/sharedfs.go`:

```go
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
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./pkg/transport/sharedfs/
```
Expected: `ok  github.com/yourorg/multi-agent/pkg/transport/sharedfs`.

- [ ] **Step 5: Commit**

```bash
cd /mnt/c/Users/DELL/multi-agent
git add multi-agent/pkg/transport/sharedfs/
git commit -m "$(cat <<'EOF'
feat(pkg/transport/sharedfs): local-FS Transport reference impl

Same content-addressed scheme as the http impl (sha256-prefix id).
Put writes to dir/<id> and returns file:// URL; Get refuses paths
outside dir (path-traversal guard). Demonstrates the Transport
interface is not HTTP-specific.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: imageops package (SynthPNG + EncodeJPEG)

**Files:**
- Create: `multi-agent/examples/image-pipeline/internal/imageops/imageops.go`
- Create: `multi-agent/examples/image-pipeline/internal/imageops/imageops_test.go`

- [ ] **Step 1: Write the failing test**

Create `multi-agent/examples/image-pipeline/internal/imageops/imageops_test.go`:

```go
package imageops

import (
	"bytes"
	"image"
	"image/png"
	"testing"
)

func TestSynthPNG_DecodableAndDeterministic(t *testing.T) {
	a, err := SynthPNG(64, 64, 42)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := SynthPNG(64, 64, 42)
	if !bytes.Equal(a, b) {
		t.Fatal("same seed produced different bytes; not deterministic")
	}
	img, _, err := image.Decode(bytes.NewReader(a))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 64 || img.Bounds().Dy() != 64 {
		t.Fatalf("unexpected size: %v", img.Bounds())
	}
}

func TestSynthPNG_DifferentSeed_DifferentBytes(t *testing.T) {
	a, _ := SynthPNG(32, 32, 1)
	b, _ := SynthPNG(32, 32, 2)
	if bytes.Equal(a, b) {
		t.Fatal("different seeds produced identical bytes")
	}
}

func TestEncodeJPEG_ShrinksAndRoundTrips(t *testing.T) {
	pngBytes, _ := SynthPNG(256, 256, 42)
	img, _ := png.Decode(bytes.NewReader(pngBytes))
	jpegBytes, err := EncodeJPEG(img, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(jpegBytes) >= len(pngBytes) {
		t.Fatalf("compression did not shrink: png=%d jpeg=%d", len(pngBytes), len(jpegBytes))
	}
	if _, _, err := image.Decode(bytes.NewReader(jpegBytes)); err != nil {
		t.Fatalf("jpeg not decodable: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./examples/image-pipeline/internal/imageops/
```
Expected: package not found / build failure.

- [ ] **Step 3: Write minimal implementation**

Create `multi-agent/examples/image-pipeline/internal/imageops/imageops.go`:

```go
// Package imageops contains the deterministic image generation and
// JPEG-encoding primitives used by the image-pipeline example agents.
// Kept separate so they're testable without an agent process.
package imageops

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand"
)

// SynthPNG generates a width x height PNG of seeded pseudo-random RGBA
// pixels. Same (width, height, seed) always produces identical bytes.
func SynthPNG(width, height int, seed int64) ([]byte, error) {
	if width < 1 || width > 4096 || height < 1 || height > 4096 {
		return nil, fmt.Errorf("dimensions out of range: %dx%d", width, height)
	}
	r := rand.New(rand.NewSource(seed))
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(r.Intn(256)),
				G: uint8(r.Intn(256)),
				B: uint8(r.Intn(256)),
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("png encode: %w", err)
	}
	return buf.Bytes(), nil
}

// EncodeJPEG writes img as a JPEG at the given quality (1..100).
func EncodeJPEG(img image.Image, quality int) ([]byte, error) {
	if quality < 1 || quality > 100 {
		return nil, fmt.Errorf("quality out of range: %d", quality)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, fmt.Errorf("jpeg encode: %w", err)
	}
	return buf.Bytes(), nil
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./examples/image-pipeline/internal/imageops/
```
Expected: `ok  github.com/yourorg/multi-agent/examples/image-pipeline/internal/imageops`.

- [ ] **Step 5: Commit**

```bash
cd /mnt/c/Users/DELL/multi-agent
git add multi-agent/examples/image-pipeline/internal/imageops/
git commit -m "$(cat <<'EOF'
feat(image-pipeline/imageops): SynthPNG + EncodeJPEG

Deterministic seeded PNG generation and JPEG re-encoding helpers,
factored out so agent task handlers stay thin and the actual image
work is unit-testable without a running agent process.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: handlepick package (FirstURL regex)

**Files:**
- Create: `multi-agent/examples/image-pipeline/internal/handlepick/handlepick.go`
- Create: `multi-agent/examples/image-pipeline/internal/handlepick/handlepick_test.go`

- [ ] **Step 1: Write the failing test**

Create `multi-agent/examples/image-pipeline/internal/handlepick/handlepick_test.go`:

```go
package handlepick

import "testing"

func TestFirstURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{`Compress {"type":"image_url","url":"http://x/y","bytes":12}`, "http://x/y", true},
		{`Use this https://example.com/img.png as input`, "https://example.com/img.png", true},
		{`no urls in here`, "", false},
		{``, "", false},
		{`see http://a.b/c.`, "http://a.b/c", true},                       // strip trailing dot
		{`(http://a.b/c)`, "http://a.b/c", true},                          // strip trailing paren
		{`first http://a.b/c then http://d.e/f`, "http://a.b/c", true},    // first wins
	}
	for _, c := range cases {
		got, ok := FirstURL(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("FirstURL(%q) = (%q, %v); want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./examples/image-pipeline/internal/handlepick/
```
Expected: package not found.

- [ ] **Step 3: Write minimal implementation**

Create `multi-agent/examples/image-pipeline/internal/handlepick/handlepick.go`:

```go
// Package handlepick extracts the first URL from a free-form prompt string.
// Used by the compress agent to dereference a handle JSON without parsing
// JSON strictly (the prompt may have arbitrary surrounding text written by
// the LLM planner).
package handlepick

import (
	"regexp"
	"strings"
)

var urlRe = regexp.MustCompile(`https?://[^\s"<>]+`)

// FirstURL returns the first http/https URL in s with trailing punctuation
// stripped, or ("", false) if none is found.
func FirstURL(s string) (string, bool) {
	m := urlRe.FindString(s)
	if m == "" {
		return "", false
	}
	m = strings.TrimRight(m, ".,;:!?)")
	return m, true
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./examples/image-pipeline/internal/handlepick/
```
Expected: `ok  github.com/yourorg/multi-agent/examples/image-pipeline/internal/handlepick`.

- [ ] **Step 5: Commit**

```bash
cd /mnt/c/Users/DELL/multi-agent
git add multi-agent/examples/image-pipeline/internal/handlepick/
git commit -m "$(cat <<'EOF'
feat(image-pipeline/handlepick): FirstURL prompt scanner

Extracts the first http/https URL from a free-form prompt and strips
trailing punctuation. Used by the compress agent to dereference an
upstream handle without strict JSON parsing.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: agentboot helper package

**Files:**
- Create: `multi-agent/examples/image-pipeline/internal/agentboot/agentboot.go`

This task has no dedicated test file — the helper is trivial glue (read yaml, register or set creds, post card, Connect). It's exercised end-to-end by the agent main_test.go and the e2e script. We commit it only after a downstream consumer (Task 7) demonstrates it builds.

- [ ] **Step 1: Write the implementation**

Create `multi-agent/examples/image-pipeline/internal/agentboot/agentboot.go`:

```go
// Package agentboot is shared boot/registration glue for the image-pipeline
// example agents. Keeps each agent's main.go to ~25 lines.
package agentboot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"gopkg.in/yaml.v3"
)

// Config is the minimal yaml shape used by both image agents.
type Config struct {
	Server struct {
		URL  string `yaml:"url"`
		Name string `yaml:"name"`
	} `yaml:"server"`
	Credentials struct {
		SandboxID   string `yaml:"sandbox_id"`
		TunnelToken string `yaml:"tunnel_token"`
		ProxyToken  string `yaml:"proxy_token"`
		WorkspaceID string `yaml:"workspace_id"`
		ShortID     string `yaml:"short_id"`
	} `yaml:"credentials"`
	Discovery struct {
		DisplayName string   `yaml:"display_name"`
		Description string   `yaml:"description"`
		Skills      []string `yaml:"skills"`
	} `yaml:"discovery"`
	ListenAddr string `yaml:"listen_addr"` // for the in-process httpx.Server
}

// LoadConfig reads and parses the yaml file at path.
func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if c.Server.URL == "" || c.Server.Name == "" {
		return nil, fmt.Errorf("config missing server.url or server.name")
	}
	if c.Discovery.DisplayName == "" {
		return nil, fmt.Errorf("config missing discovery.display_name")
	}
	return &c, nil
}

// SaveConfig writes c back to path (used after first-run device flow).
func SaveConfig(path string, c *Config) error {
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// EnsureRegistered runs device-flow if credentials are missing, otherwise
// just calls SetRegistration. On first run it prints the verification URL
// to stderr and writes credentials back to cfgPath.
func EnsureRegistered(ctx context.Context, cli *agentsdk.Client, cfg *Config, cfgPath string) error {
	if cfg.Credentials.ProxyToken != "" {
		cli.SetRegistration(&agentsdk.Registration{
			SandboxID:   cfg.Credentials.SandboxID,
			TunnelToken: cfg.Credentials.TunnelToken,
			ProxyToken:  cfg.Credentials.ProxyToken,
			WorkspaceID: cfg.Credentials.WorkspaceID,
			ShortID:     cfg.Credentials.ShortID,
		})
		return nil
	}
	dc, err := agentsdk.RequestDeviceCode(ctx, cfg.Server.URL)
	if err != nil {
		return fmt.Errorf("device code: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Open this URL in a browser to register %q:\n  %s\n", cfg.Server.Name, dc.VerificationURIComplete)
	tok, err := agentsdk.PollForToken(ctx, cfg.Server.URL, dc)
	if err != nil {
		return fmt.Errorf("poll token: %w", err)
	}
	reg, err := cli.Register(ctx, tok.AccessToken)
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}
	cfg.Credentials.SandboxID = reg.SandboxID
	cfg.Credentials.TunnelToken = reg.TunnelToken
	cfg.Credentials.ProxyToken = reg.ProxyToken
	cfg.Credentials.WorkspaceID = reg.WorkspaceID
	cfg.Credentials.ShortID = reg.ShortID
	if err := SaveConfig(cfgPath, cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}

// PublishCard posts the discovery card. Mirrors multi-agent/internal/tunnel/tunnel.go:81
// (the SDK has no helper).
func PublishCard(ctx context.Context, cfg *Config) error {
	body, _ := json.Marshal(map[string]interface{}{
		"display_name": cfg.Discovery.DisplayName,
		"description":  cfg.Discovery.Description,
		"agent_type":   "custom",
		"card": map[string]interface{}{
			"skills":        cfg.Discovery.Skills,
			"accepts_tasks": true,
			"has_web_ui":    false,
			"version":       "0.1.0",
		},
	})
	url := strings.TrimRight(cfg.Server.URL, "/") + "/api/agent/discovery/cards"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Credentials.ProxyToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("publish card: status %d", resp.StatusCode)
	}
	return nil
}

// Run is the one-line entry point used by each agent's main: load config,
// register or set creds, publish card, Connect with the given handler.
// Blocks until ctx is cancelled or Connect returns an error.
func Run(ctx context.Context, cfgPath string, handler agentsdk.TaskHandler) error {
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		return err
	}
	cli := agentsdk.NewClient(agentsdk.Config{ServerURL: cfg.Server.URL, Name: cfg.Server.Name})
	if err := EnsureRegistered(ctx, cli, cfg, cfgPath); err != nil {
		return err
	}
	if err := PublishCard(ctx, cfg); err != nil {
		return fmt.Errorf("publish card: %w", err)
	}
	return cli.Connect(ctx, agentsdk.Handlers{
		Task: handler,
		OnConnect: func() {
			fmt.Fprintf(os.Stderr, "agentboot: %s connected\n", cfg.Server.Name)
		},
		OnDisconnect: func(err error) {
			fmt.Fprintf(os.Stderr, "agentboot: %s disconnected: %v\n", cfg.Server.Name, err)
		},
	}, agentsdk.WithTaskPollInterval(2*time.Second))
}
```

- [ ] **Step 2: Verify it builds (no test yet, just compile)**

```bash
go build ./examples/image-pipeline/internal/agentboot/
```
Expected: no output, exit 0.

- [ ] **Step 3: Commit**

```bash
cd /mnt/c/Users/DELL/multi-agent
git add multi-agent/examples/image-pipeline/internal/agentboot/
git commit -m "$(cat <<'EOF'
feat(image-pipeline/agentboot): shared agentsdk boot helper

Reads the small YAML config, runs device-flow on first launch (and
writes credentials back), publishes the discovery card, then enters
the agentsdk task poll loop with the supplied handler. Lets each
image agent's main.go be ~25 lines.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: agent-image-capture binary

**Files:**
- Create: `multi-agent/examples/image-pipeline/agent-image-capture/main.go`
- Create: `multi-agent/examples/image-pipeline/agent-image-capture/main_test.go`
- Create: `multi-agent/examples/image-pipeline/agent-image-capture/config.example.yaml`

- [ ] **Step 1: Write the failing test**

Create `multi-agent/examples/image-pipeline/agent-image-capture/main_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./examples/image-pipeline/agent-image-capture/
```
Expected: build failure (`runCapture` undefined).

- [ ] **Step 3: Write minimal implementation**

Create `multi-agent/examples/image-pipeline/agent-image-capture/main.go`:

```go
// agent-image-capture is a custom workspace agent that, on receiving any
// task, synthesizes a 256x256 PNG and returns a Handle JSON pointing at the
// in-process HTTP transport.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/examples/image-pipeline/internal/agentboot"
	"github.com/yourorg/multi-agent/examples/image-pipeline/internal/imageops"
	"github.com/yourorg/multi-agent/pkg/transport"
	httpx "github.com/yourorg/multi-agent/pkg/transport/http"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to YAML config")
	flag.Parse()

	cfg, err := agentboot.LoadConfig(*cfgPath)
	if err != nil {
		log.Fatalf("agent-image-capture: %v", err)
	}
	srv, err := httpx.New(httpx.Options{Addr: cfg.ListenAddr})
	if err != nil {
		log.Fatalf("agent-image-capture: httpx: %v", err)
	}
	defer srv.Close()
	fmt.Fprintf(os.Stderr, "LISTEN %s\n", srv.Addr())

	handler := func(ctx context.Context, task *agentsdk.Task) error {
		out, err := runCapture(ctx, srv)
		if err != nil {
			return task.Fail(ctx, err.Error())
		}
		return task.Complete(ctx, agentsdk.TaskResult{Output: out})
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := agentboot.Run(ctx, *cfgPath, handler); err != nil {
		log.Fatalf("agent-image-capture: %v", err)
	}
}

// runCapture is the unit-testable core: synthesize a PNG, push it through the
// transport, return the Handle JSON string suitable for TaskResult.Output.
func runCapture(ctx context.Context, srv *httpx.Server) (string, error) {
	pngBytes, err := imageops.SynthPNG(256, 256, 42)
	if err != nil {
		return "", fmt.Errorf("synth: %w", err)
	}
	h, err := srv.Put(ctx, "image/png", bytes.NewReader(pngBytes))
	if err != nil {
		return "", fmt.Errorf("put: %w", err)
	}
	h.Type = "image_url"
	return transport.Handle{
		Type:  "image_url",
		URL:   h.URL,
		Bytes: h.Bytes,
		MIME:  h.MIME,
	}.Marshal(), nil
}
```

Create `multi-agent/examples/image-pipeline/agent-image-capture/config.example.yaml`:

```yaml
server:
  url: https://agent.example.com
  name: agent-image-capture-e2e

credentials:
  sandbox_id: ""
  tunnel_token: ""
  proxy_token: ""
  workspace_id: ""
  short_id: ""

discovery:
  display_name: image-capture
  description: |
    Image capture agent. Always returns a JSON handle string
    {"type":"image_url","url":"http://...","mime":"image/png","bytes":N}
    pointing at a freshly generated 256x256 PNG. Ignores task prompt content.
  skills: [capture]

listen_addr: 127.0.0.1:0
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./examples/image-pipeline/agent-image-capture/
```
Expected: `ok  github.com/yourorg/multi-agent/examples/image-pipeline/agent-image-capture`.

- [ ] **Step 5: Commit**

```bash
cd /mnt/c/Users/DELL/multi-agent
git add multi-agent/examples/image-pipeline/agent-image-capture/
git commit -m "$(cat <<'EOF'
feat(image-pipeline/agent-image-capture): custom agent main + tests

agentsdk task handler that synthesizes a 256x256 PNG via imageops,
pushes it through pkg/transport/http, and returns a handle JSON as
the task output. The work is in runCapture(ctx, srv) so the unit
test exercises it without needing a real Task.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: agent-image-compress binary

**Files:**
- Create: `multi-agent/examples/image-pipeline/agent-image-compress/main.go`
- Create: `multi-agent/examples/image-pipeline/agent-image-compress/main_test.go`
- Create: `multi-agent/examples/image-pipeline/agent-image-compress/config.example.yaml`

- [ ] **Step 1: Write the failing test**

Create `multi-agent/examples/image-pipeline/agent-image-compress/main_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./examples/image-pipeline/agent-image-compress/
```
Expected: build failure (`runCompress` undefined).

- [ ] **Step 3: Write minimal implementation**

Create `multi-agent/examples/image-pipeline/agent-image-compress/main.go`:

```go
// agent-image-compress is a custom workspace agent that, on receiving a
// task, finds the first http(s) URL in the task prompt, downloads the
// image, re-encodes it as JPEG, and returns a Handle JSON pointing at the
// new bytes.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"image"
	_ "image/png" // for SynthPNG-produced inputs
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"syscall"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/examples/image-pipeline/internal/agentboot"
	"github.com/yourorg/multi-agent/examples/image-pipeline/internal/handlepick"
	"github.com/yourorg/multi-agent/examples/image-pipeline/internal/imageops"
	"github.com/yourorg/multi-agent/pkg/transport"
	httpx "github.com/yourorg/multi-agent/pkg/transport/http"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to YAML config")
	flag.Parse()

	cfg, err := agentboot.LoadConfig(*cfgPath)
	if err != nil {
		log.Fatalf("agent-image-compress: %v", err)
	}
	srv, err := httpx.New(httpx.Options{Addr: cfg.ListenAddr})
	if err != nil {
		log.Fatalf("agent-image-compress: httpx: %v", err)
	}
	defer srv.Close()
	fmt.Fprintf(os.Stderr, "LISTEN %s\n", srv.Addr())

	handler := func(ctx context.Context, task *agentsdk.Task) error {
		out, err := runCompress(ctx, srv, task.Prompt)
		if err != nil {
			return task.Fail(ctx, err.Error())
		}
		return task.Complete(ctx, agentsdk.TaskResult{Output: out})
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := agentboot.Run(ctx, *cfgPath, handler); err != nil {
		log.Fatalf("agent-image-compress: %v", err)
	}
}

var qualityRe = regexp.MustCompile(`quality\s*=\s*(\d+)`)

// runCompress: pluck a URL out of prompt, fetch image, re-encode, push, return Handle.
func runCompress(ctx context.Context, srv *httpx.Server, prompt string) (string, error) {
	url, ok := handlepick.FirstURL(prompt)
	if !ok {
		return "", fmt.Errorf("no URL in prompt")
	}
	quality := 50
	if m := qualityRe.FindStringSubmatch(prompt); m != nil {
		if q, err := strconv.Atoi(m[1]); err == nil && q >= 1 && q <= 100 {
			quality = q
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("get %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("get %s: %s", url, resp.Status)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	originalBytes := len(raw)
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	jpegBytes, err := imageops.EncodeJPEG(img, quality)
	if err != nil {
		return "", err
	}
	h, err := srv.Put(ctx, "image/jpeg", bytes.NewReader(jpegBytes))
	if err != nil {
		return "", fmt.Errorf("put: %w", err)
	}
	out := transport.Handle{
		Type:  "image_url",
		URL:   h.URL,
		Bytes: h.Bytes,
		MIME:  "image/jpeg",
		Meta: map[string]string{
			"original_bytes": strconv.Itoa(originalBytes),
			"ratio":          fmt.Sprintf("%.2f", float64(len(jpegBytes))/float64(originalBytes)),
			"quality":        strconv.Itoa(quality),
		},
	}
	return out.Marshal(), nil
}
```

Create `multi-agent/examples/image-pipeline/agent-image-compress/config.example.yaml`:

```yaml
server:
  url: https://agent.example.com
  name: agent-image-compress-e2e

credentials:
  sandbox_id: ""
  tunnel_token: ""
  proxy_token: ""
  workspace_id: ""
  short_id: ""

discovery:
  display_name: image-compress
  description: |
    Image compress agent. Reads the first https?:// URL it finds in the
    task prompt, downloads the image, re-encodes as JPEG (quality=50 default;
    override via "quality=N" substring in prompt). Returns a JSON handle
    string {"type":"image_url","url":"http://...","mime":"image/jpeg","bytes":M,
    "meta":{"original_bytes":"N","ratio":"M/N","quality":"N"}}.
  skills: [compress]

listen_addr: 127.0.0.1:0
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./examples/image-pipeline/agent-image-compress/
```
Expected: `ok  github.com/yourorg/multi-agent/examples/image-pipeline/agent-image-compress`.

- [ ] **Step 5: Commit**

```bash
cd /mnt/c/Users/DELL/multi-agent
git add multi-agent/examples/image-pipeline/agent-image-compress/
git commit -m "$(cat <<'EOF'
feat(image-pipeline/agent-image-compress): custom agent main + tests

agentsdk task handler that extracts the first URL from the prompt
(via handlepick), downloads the image, re-encodes it as JPEG via
imageops.EncodeJPEG, pushes through pkg/transport/http, and returns
a handle JSON. Quality default=50, override with "quality=N" in
prompt. Work lives in runCompress(ctx, srv, prompt) for unit tests.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: e2e-driver binary

**Files:**
- Create: `multi-agent/examples/image-pipeline/e2e-driver/main.go`

This binary has no automated test (it requires a live agentserver). It's exercised by Task 10's bash script. We commit it once it builds clean.

- [ ] **Step 1: Write the implementation**

Create `multi-agent/examples/image-pipeline/e2e-driver/main.go`:

```go
// e2e-driver is the assertion runner for the image-pipeline e2e. It loads a
// pre-registered agent config, polls until the master and both image agents
// are visible in agentserver discovery, submits a fanout task to the master,
// waits for completion, and asserts:
//
//   - master task status == completed
//   - reducer output contains an image_url + an http URL we can GET
//   - GETting that URL yields valid JPEG bytes
//   - if --master-data-db is given, sub_tasks rows are both completed and
//     n2.bytes < n1.bytes (real compression happened)
//
// Exit 0 = pass, 1 = fail.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	_ "image/jpeg"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/examples/image-pipeline/internal/agentboot"
	"github.com/yourorg/multi-agent/examples/image-pipeline/internal/handlepick"
	"github.com/yourorg/multi-agent/pkg/transport"
)

func main() {
	cfg := flag.String("config", "", "driver agent config (must have credentials)")
	target := flag.String("target-display-name", "master-e2e-image", "master display_name to discover")
	expect := flag.String("expect-agents", "image-capture,image-compress", "comma-separated display_names that must also be visible before submitting")
	prompt := flag.String("prompt", "Capture a 256x256 image using the image-capture agent, then pass its output URL to the image-compress agent at quality=50. Sub-task n2 MUST reference n1's output via {{n1.output}} so my orchestrator can substitute it. Final answer should include the final compressed image URL and the byte size.", "task prompt")
	skill := flag.String("skill", "fanout", "task skill")
	timeout := flag.Duration("timeout", 300*time.Second, "overall driver timeout")
	dbPath := flag.String("master-data-db", "", "optional: path to master's data.db for deeper assertions")
	flag.Parse()

	if *cfg == "" {
		log.Fatal("--config required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	c, err := agentboot.LoadConfig(*cfg)
	if err != nil {
		log.Fatalf("driver: load config: %v", err)
	}
	cli := agentsdk.NewClient(agentsdk.Config{ServerURL: c.Server.URL, Name: c.Server.Name})
	if c.Credentials.ProxyToken == "" {
		log.Fatal("driver: config has no credentials; register first")
	}
	cli.SetRegistration(&agentsdk.Registration{
		SandboxID:   c.Credentials.SandboxID,
		TunnelToken: c.Credentials.TunnelToken,
		ProxyToken:  c.Credentials.ProxyToken,
		WorkspaceID: c.Credentials.WorkspaceID,
		ShortID:     c.Credentials.ShortID,
	})

	// Phase 1: wait for all expected agents to be visible.
	want := append(strings.Split(*expect, ","), *target)
	masterID, err := waitForAgents(ctx, cli, want, *target, 60*time.Second)
	if err != nil {
		log.Fatalf("driver: discover: %v", err)
	}
	fmt.Printf("driver: master agent_id=%s\n", masterID)

	// Phase 2: delegate.
	resp, err := cli.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID:       masterID,
		Skill:          *skill,
		Prompt:         *prompt,
		TimeoutSeconds: int((*timeout - 60*time.Second).Seconds()),
	})
	if err != nil {
		log.Fatalf("driver: delegate: %v", err)
	}
	fmt.Printf("driver: submitted task_id=%s\n", resp.TaskID)

	// Phase 3: wait for completion.
	info, err := cli.WaitForTask(ctx, resp.TaskID, 2*time.Second)
	if err != nil {
		log.Fatalf("driver: wait: %v", err)
	}
	if info.Status != "completed" {
		log.Fatalf("driver: master task status=%s reason=%s", info.Status, info.FailureReason)
	}
	fmt.Printf("driver: master output (len=%d): %s\n", len(info.Output), truncate(info.Output, 400))

	// Phase 4: assertions on reducer output.
	url, ok := handlepick.FirstURL(info.Output)
	if !ok {
		log.Fatalf("driver: reducer output contains no URL:\n%s", info.Output)
	}
	finalBytes, mime, err := fetchAndCheckJPEG(ctx, url)
	if err != nil {
		log.Fatalf("driver: final URL check: %v", err)
	}
	fmt.Printf("driver: final URL %s -> %d bytes (%s)\n", url, finalBytes, mime)

	// Phase 5: optional sqlite deep check.
	if *dbPath != "" {
		if err := checkSubTasks(*dbPath, resp.TaskID); err != nil {
			log.Fatalf("driver: sub-task assertions: %v", err)
		}
		fmt.Println("driver: sub-task assertions passed")
	}

	fmt.Println("OK image-pipeline e2e")
}

func waitForAgents(ctx context.Context, cli *agentsdk.Client, displayNames []string, target string, deadline time.Duration) (string, error) {
	dctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	tk := time.NewTicker(2 * time.Second)
	defer tk.Stop()
	for {
		cards, err := cli.DiscoverAgents(dctx)
		if err == nil {
			present := make(map[string]string)
			for _, c := range cards {
				present[c.DisplayName] = c.AgentID
			}
			missing := []string{}
			for _, n := range displayNames {
				if _, ok := present[n]; !ok {
					missing = append(missing, n)
				}
			}
			if len(missing) == 0 {
				return present[target], nil
			}
			fmt.Printf("driver: waiting for agents: %v\n", missing)
		} else {
			fmt.Printf("driver: discover error (will retry): %v\n", err)
		}
		select {
		case <-dctx.Done():
			return "", fmt.Errorf("timeout waiting for agents %v", displayNames)
		case <-tk.C:
		}
	}
}

func fetchAndCheckJPEG(ctx context.Context, url string) (int, string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return 0, "", fmt.Errorf("status %s", resp.Status)
	}
	mime := resp.Header.Get("Content-Type")
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, mime, err
	}
	if _, _, err := image.Decode(bytesReader(body)); err != nil {
		return len(body), mime, fmt.Errorf("decode: %w", err)
	}
	return len(body), mime, nil
}

func bytesReader(b []byte) io.Reader {
	return strings.NewReader(string(b))
}

func checkSubTasks(dbPath, parentID string) error {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT node_id, status, output FROM sub_tasks WHERE parent_id = ? ORDER BY node_id`, parentID)
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}
	defer rows.Close()
	type sub struct {
		NodeID, Status, Output string
	}
	var subs []sub
	for rows.Next() {
		var s sub
		var out sql.NullString
		if err := rows.Scan(&s.NodeID, &s.Status, &out); err != nil {
			return err
		}
		s.Output = out.String
		subs = append(subs, s)
	}
	if len(subs) != 2 {
		return fmt.Errorf("expected 2 sub_tasks, got %d", len(subs))
	}
	for _, s := range subs {
		if s.Status != "completed" {
			return fmt.Errorf("sub-task %s status=%s", s.NodeID, s.Status)
		}
	}
	h0, ok := transport.ParseHandle(subs[0].Output)
	if !ok {
		return fmt.Errorf("n1 output not a handle: %s", subs[0].Output)
	}
	h1, ok := transport.ParseHandle(subs[1].Output)
	if !ok {
		return fmt.Errorf("n2 output not a handle: %s", subs[1].Output)
	}
	if h0.MIME != "image/png" {
		return fmt.Errorf("n1.MIME = %q, want image/png", h0.MIME)
	}
	if h1.MIME != "image/jpeg" {
		return fmt.Errorf("n2.MIME = %q, want image/jpeg", h1.MIME)
	}
	if h1.Bytes >= h0.Bytes {
		return fmt.Errorf("compression did not shrink: n1=%d n2=%d", h0.Bytes, h1.Bytes)
	}
	bb, _ := json.Marshal(map[string]interface{}{"n1": h0, "n2": h1})
	fmt.Printf("driver: sub-tasks OK %s\n", string(bb))
	return nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}
```

- [ ] **Step 2: Verify it builds**

```bash
go build ./examples/image-pipeline/e2e-driver/
```
Expected: no output, exit 0.

- [ ] **Step 3: Commit**

```bash
cd /mnt/c/Users/DELL/multi-agent
git add multi-agent/examples/image-pipeline/e2e-driver/
git commit -m "$(cat <<'EOF'
feat(image-pipeline/e2e-driver): live e2e assertion runner

Polls DiscoverAgents until master + both image agents are visible,
DelegateTasks a fanout prompt to the master, WaitForTasks for
completion, then asserts the reducer output, fetches the final URL,
and (optionally) opens master/data.db to verify sub-task statuses
and the PNG->JPEG byte shrink. Exit 0 = pass, 1 = fail.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 10: e2e.sh bash orchestrator

**Files:**
- Create: `multi-agent/examples/image-pipeline/scripts/e2e.sh`

- [ ] **Step 1: Write the script**

Create `multi-agent/examples/image-pipeline/scripts/e2e.sh`:

```bash
#!/usr/bin/env bash
# Live end-to-end test for the image pipeline.
#
# Prerequisites (one-time):
#   - AGENTSERVER_URL set, agentserver reachable
#   - ANTHROPIC_API_KEY set, claude on PATH
#   - go and sqlite3 on PATH
#   - Four pre-registered config files (with credentials filled by prior
#     interactive device-flow registration), paths supplied via env:
#       MASTER_CONFIG    config.yaml for cmd/master-agent
#       CAPTURE_CONFIG   config.yaml for examples/image-pipeline/agent-image-capture
#       COMPRESS_CONFIG  config.yaml for examples/image-pipeline/agent-image-compress
#       DRIVER_CONFIG    config.yaml for examples/image-pipeline/e2e-driver
#
# See README.md in this directory for first-time setup.
set -euo pipefail

require_env() {
  for v in AGENTSERVER_URL ANTHROPIC_API_KEY MASTER_CONFIG CAPTURE_CONFIG COMPRESS_CONFIG DRIVER_CONFIG; do
    if [ -z "${!v:-}" ]; then
      echo "missing required env: $v" >&2
      exit 2
    fi
  done
  for cmd in go claude sqlite3; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
      echo "missing required command: $cmd" >&2
      exit 2
    fi
  done
}
require_env

script_dir=$(cd "$(dirname "$0")" && pwd)
module_root=$(cd "$script_dir/../../.." && pwd)
cd "$module_root"

work=$(mktemp -d)
echo "work dir: $work"
pids=()
cleanup() {
  for p in "${pids[@]:-}"; do
    [ -n "$p" ] && kill "$p" 2>/dev/null || true
  done
  rm -rf "$work"
}
trap cleanup EXIT

# Build all four binaries.
mkdir -p "$work/bin"
echo "building binaries..."
go build -o "$work/bin/master-agent"          ./cmd/master-agent
go build -o "$work/bin/agent-image-capture"   ./examples/image-pipeline/agent-image-capture
go build -o "$work/bin/agent-image-compress"  ./examples/image-pipeline/agent-image-compress
go build -o "$work/bin/e2e-driver"            ./examples/image-pipeline/e2e-driver

# Lay out per-agent working dirs and copy configs.
for name in master capture compress; do
  mkdir -p "$work/$name"
done
cp "$MASTER_CONFIG"   "$work/master/config.yaml"
cp "$CAPTURE_CONFIG"  "$work/capture/config.yaml"
cp "$COMPRESS_CONFIG" "$work/compress/config.yaml"

# Launch the three long-running agents.
echo "launching agent-image-capture..."
( cd "$work/capture"  && "$work/bin/agent-image-capture"  --config config.yaml ) > "$work/capture.log"  2>&1 &
pids+=($!)
echo "launching agent-image-compress..."
( cd "$work/compress" && "$work/bin/agent-image-compress" --config config.yaml ) > "$work/compress.log" 2>&1 &
pids+=($!)
echo "launching master-agent..."
( cd "$work/master"   && "$work/bin/master-agent"         config.yaml         ) > "$work/master.log"   2>&1 &
pids+=($!)

echo "running e2e driver..."
set +e
"$work/bin/e2e-driver" \
  --config "$DRIVER_CONFIG" \
  --target-display-name master-e2e-image \
  --expect-agents image-capture,image-compress \
  --master-data-db "$work/master/data.db" \
  --timeout 300s
status=$?
set -e

if [ "$status" -ne 0 ]; then
  echo "e2e FAILED. Logs:" >&2
  for log in master.log capture.log compress.log; do
    echo "--- $work/$log ---" >&2
    tail -n 50 "$work/$log" >&2 || true
  done
  exit "$status"
fi

echo "OK image-pipeline e2e"
```

- [ ] **Step 2: Mark executable and verify it parses**

```bash
chmod +x /mnt/c/Users/DELL/multi-agent/multi-agent/examples/image-pipeline/scripts/e2e.sh
bash -n /mnt/c/Users/DELL/multi-agent/multi-agent/examples/image-pipeline/scripts/e2e.sh
```
Expected: no output, exit 0 (syntax OK).

- [ ] **Step 3: Commit**

```bash
cd /mnt/c/Users/DELL/multi-agent
git add multi-agent/examples/image-pipeline/scripts/e2e.sh
git commit -m "$(cat <<'EOF'
feat(image-pipeline/scripts): e2e.sh orchestrator

Builds the four binaries, lays out per-agent working dirs, launches
master + 2 image agents, runs the e2e-driver, and prints log tails
on failure. Requires four pre-registered configs supplied via env;
README documents the one-time setup.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 11: README

**Files:**
- Create: `multi-agent/examples/image-pipeline/README.md`

- [ ] **Step 1: Write the README**

Create `multi-agent/examples/image-pipeline/README.md`:

```markdown
# image-pipeline

End-to-end example demonstrating how to wire `multi-agent`'s `master-agent`
to two custom workspace agents that exchange a non-text artifact (an image)
through a side channel, while the inter-node "you've finished, here's a
handle" message still flows through the framework's standard
`{{nX.output}}` template path.

## Why this exists

The framework substitutes upstream sub-task outputs into downstream prompts
via the `{{nX.output}}` template (see `internal/orchestrator/dag.go:204`).
That works for short text, but stuffing a base64 PNG into a prompt is
expensive and forces intermediate slaves to act as byte couriers. Instead,
the producer can return a small **handle JSON** as its output:

```json
{"type":"image_url","url":"http://127.0.0.1:54321/blobs/9f86d081884c7d65","bytes":51234,"mime":"image/png"}
```

The framework substitutes that string into the next prompt as usual. The
consumer parses the URL out and `Get`s the bytes from the side channel
(here: HTTP, but the same pattern works for shared FS or any other
transport you implement). See `pkg/transport` for the small library that
makes this convenient.

## Layout

- `internal/imageops/`   — `SynthPNG` + `EncodeJPEG` (deterministic PNG generator + JPEG encoder)
- `internal/handlepick/` — `FirstURL` regex used by the compress agent
- `internal/agentboot/`  — shared "load yaml → register or set creds → publish card → Connect" glue
- `agent-image-capture/` — custom agent: on any task, returns a handle JSON for a fresh 256x256 PNG
- `agent-image-compress/`— custom agent: on any task, finds the first URL in the prompt, downloads, re-encodes as JPEG, returns a new handle JSON
- `e2e-driver/`          — Go binary that DelegateTasks the master and asserts the reducer output
- `scripts/e2e.sh`       — bash wrapper that builds, launches, runs the driver, and reports

## First-time setup

The e2e expects four pre-registered configs (each tied to its own agentserver
sandbox identity). Register them once by running each binary interactively:

```bash
# From multi-agent/ module root, for each of master / capture / compress / driver:
cp examples/image-pipeline/agent-image-capture/config.example.yaml /tmp/capture.yaml
$EDITOR /tmp/capture.yaml      # set server.url to your agentserver
go run ./examples/image-pipeline/agent-image-capture --config /tmp/capture.yaml
# Open the printed verification URL in a browser, complete login.
# The binary writes credentials back into /tmp/capture.yaml; ctrl-C when registered.
```

Repeat for `agent-image-compress`, `cmd/master-agent`, and `e2e-driver` (the
driver also needs an identity to call DelegateTask — the same minimal config
shape works; you can reuse the agentboot config file for the driver).

Note: master-agent uses a different config schema (`cmd/master-agent/config.example.yaml`),
not the agentboot shape. Set `discovery.display_name: master-e2e-image` and
`skills: [route, fanout]`.

## Running the e2e

Once you have four configs with credentials:

```bash
export AGENTSERVER_URL=https://your-agentserver
export ANTHROPIC_API_KEY=sk-...
export MASTER_CONFIG=/tmp/master.yaml
export CAPTURE_CONFIG=/tmp/capture.yaml
export COMPRESS_CONFIG=/tmp/compress.yaml
export DRIVER_CONFIG=/tmp/driver.yaml

cd multi-agent/   # module root
./examples/image-pipeline/scripts/e2e.sh
```

Expected output ends with `OK image-pipeline e2e`. On failure, the script
prints the tail of master.log / capture.log / compress.log.

Real `claude` is invoked twice per run (planner + reducer); sub-task work is
done in-process by the Go agents, so the run cost is small and bounded.

## Adapting the pattern

The `pkg/transport` interface is intentionally small: `Put(mime, reader) →
Handle` and `Get(handle) → reader`. Swap `pkg/transport/http` for
`pkg/transport/sharedfs` (or your own implementation) if your topology
favors a shared filesystem, S3, or anything else. The framework doesn't
care; only the producer and consumer agents need to agree on a transport.
```

- [ ] **Step 2: Commit**

```bash
cd /mnt/c/Users/DELL/multi-agent
git add multi-agent/examples/image-pipeline/README.md
git commit -m "$(cat <<'EOF'
docs(image-pipeline): README explaining handle JSON convention + setup

Documents why the example exists (side-channel for large artifacts
instead of inflating prompts), the layout of the seven new packages
and binaries, the one-time per-agent device-flow registration, and
how to run the e2e. Calls out that pkg/transport is substitutable.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 12: Final verification (build / vet / test all)

- [ ] **Step 1: Run build, vet, test**

```bash
cd /mnt/c/Users/DELL/multi-agent/multi-agent
go build ./...
go vet ./...
go test ./...
```
Expected: zero errors; `ok` for every package; no `[build failed]` lines.

- [ ] **Step 2: Confirm no salve / TBD / TODO leaked into the new code**

```bash
grep -rn "TBD\|TODO\|salve" \
  multi-agent/pkg/transport \
  multi-agent/examples/image-pipeline \
  || echo "clean"
```
Expected: `clean`.

- [ ] **Step 3: Final summary commit (only if any housekeeping needed; otherwise skip)**

If the previous two steps both pass with no fixes needed, this task ends without a new commit. If you had to fix something, commit it with a clear message.

---

## Self-review note (already incorporated)

While writing this plan I cross-checked these spec requirements:

- ✅ `pkg/transport` package with Handle + Transport + http + sharedfs → Tasks 1-3
- ✅ Handle JSON convention (`{type, url, bytes, mime, meta}`) used consistently across producer outputs and consumer assertions
- ✅ Two custom agents using `agentsdk` directly, **not** `cmd/slave-agent`, **no** claude or MCP on the agent side → Tasks 6-8
- ✅ Shared `agentboot` helper to keep each `main.go` tiny → Task 6
- ✅ `imageops` and `handlepick` factored out so handler work is unit-testable without `*agentsdk.Task` → Tasks 4-5
- ✅ e2e-driver: discover → delegate → wait → assert reducer + fetch URL + sqlite deep check → Task 9
- ✅ bash e2e: build → launch → run driver → log tails on failure → Task 10
- ✅ README covers handle JSON, layout, first-time device-flow setup, running → Task 11
- ✅ No changes to `multi-agent/internal/*` — all tasks add files under `pkg/transport/` and `examples/image-pipeline/` only

Type/symbol consistency:
- `transport.Handle` fields used: `Type, URL, Bytes, MIME, Meta` (consistent across all tasks)
- `transport.Transport` methods: `Put(ctx, mime, data) (Handle, error)`, `Get(ctx, h) (io.ReadCloser, error)`, `Close()` (consistent)
- `httpx.Server` methods used: `New, Addr, PublicURL, Put, Get, Close` (defined in Task 2, used in 7/8)
- `agentboot.LoadConfig`, `EnsureRegistered`, `PublishCard`, `Run`, `Config` (defined in Task 6, used in 7/8/9)
- `imageops.SynthPNG(width, height, seed)`, `imageops.EncodeJPEG(img, quality)` (defined in Task 4, used in 7/8)
- `handlepick.FirstURL(s) (string, bool)` (defined in Task 5, used in 8/9)
