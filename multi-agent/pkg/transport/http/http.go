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
