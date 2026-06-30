//go:build evaltool

package faultinject

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Default control-plane bind address. Loopback only; spec §4.1, §7 (a).
const DefaultListen = "127.0.0.1:18189"

// Inject payload size limits. Spec §4.2.
const (
	maxTargetBytes    = 512
	maxParamEntries   = 16
	maxParamValueLen  = 1024
	maxRequestBodyLen = 1 << 16 // 64 KiB; defence against runaway bodies.
)

// HTTP server timeouts. Spec §4.1, §7 (h).
const (
	defaultReadHeaderTimeout = 5 * time.Second
	defaultReadTimeout       = 30 * time.Second
	defaultWriteTimeout      = 30 * time.Second
	defaultIdleTimeout       = 60 * time.Second
)

// Config configures a fault-injection control-plane Server. Listen
// defaults to DefaultListen when empty; Store and Audit are constructed
// with sensible defaults when nil.
type Config struct {
	Listen string
	Store  *Store
	Audit  io.Writer        // defaults to os.Stderr; tests pass a bytes.Buffer
	Now    func() time.Time // defaults to time.Now().UTC; used by Audit timestamps
}

// Server hosts the /inject, /clear, /list endpoints over loopback.
type Server struct {
	cfg     Config
	store   *Store
	audit   *AuditWriter
	httpSrv *http.Server
	addr    atomic.Pointer[string]
	once    sync.Once
}

// NewServer validates the listen address (loopback only — spec §7 a) and
// returns a Server ready to call Serve.
func NewServer(cfg Config) (*Server, error) {
	if cfg.Listen == "" {
		cfg.Listen = DefaultListen
	}
	if err := assertLoopback(cfg.Listen); err != nil {
		return nil, err
	}
	if cfg.Store == nil {
		cfg.Store = NewStore()
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	s := &Server{
		cfg:   cfg,
		store: cfg.Store,
		audit: NewAuditWriter(cfg.Audit, cfg.Now),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/inject", s.handleInject)
	mux.HandleFunc("/clear", s.handleClear)
	mux.HandleFunc("/list", s.handleList)
	s.httpSrv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: defaultReadHeaderTimeout,
		ReadTimeout:       defaultReadTimeout,
		WriteTimeout:      defaultWriteTimeout,
		IdleTimeout:       defaultIdleTimeout,
	}
	return s, nil
}

// Serve starts the listener and blocks until ctx is cancelled or the
// server returns an unrecoverable error. The bound address (with the
// real port if Listen used :0) is published via Addr once the listener
// is up.
func (s *Server) Serve(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return fmt.Errorf("faultinject: listen %q: %w", s.cfg.Listen, err)
	}
	// Belt-and-braces: post-bind verify the listener IS on loopback (e.g.
	// a hostname that resolved to loopback at NewServer time but somehow
	// rebound elsewhere — paranoid but cheap). Fail closed: anything
	// that is not explicitly loopback (incl. unspecified, which binds
	// all interfaces) is rejected.
	if a, ok := ln.Addr().(*net.TCPAddr); ok {
		if a.IP != nil && !a.IP.IsLoopback() {
			ln.Close()
			return fmt.Errorf("%w: post-bind addr %s", ErrControlPlaneMustBeLoopback, a)
		}
	}
	addr := ln.Addr().String()
	s.addr.Store(&addr)

	go func() {
		<-ctx.Done()
		_ = s.httpSrv.Shutdown(context.Background())
	}()
	if err := s.httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown stops the HTTP server with the supplied context's deadline.
// Safe to call multiple times.
func (s *Server) Shutdown(ctx context.Context) error {
	var err error
	s.once.Do(func() {
		if s.httpSrv != nil {
			err = s.httpSrv.Shutdown(ctx)
		}
	})
	return err
}

// Addr returns the bound listener address, or "" if Serve has not yet
// announced one.
func (s *Server) Addr() string {
	p := s.addr.Load()
	if p == nil {
		return ""
	}
	return *p
}

// ListenAddr returns the configured (pre-bind) listen address, after
// defaulting. Useful when Listen was specified with a non-zero port and
// the caller wants to display it before Serve runs.
func (s *Server) ListenAddr() string { return s.cfg.Listen }

// HTTPServer exposes the underlying *http.Server for test inspection
// (timeouts assertion in F14). Production callers should not depend on
// this method.
func (s *Server) HTTPServer() *http.Server { return s.httpSrv }

// assertLoopback resolves the host part of addr and returns
// ErrControlPlaneMustBeLoopback if any resolved IP is not a loopback
// address. A literal "0.0.0.0" / "::" / unspecified IP is also rejected
// — they bind every interface, which is the opposite of loopback only.
func assertLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%w: parse %q: %v", ErrControlPlaneMustBeLoopback, addr, err)
	}
	if host == "" {
		// "":port means listen on every interface — explicitly forbidden.
		return fmt.Errorf("%w: empty host in %q (binds all interfaces)", ErrControlPlaneMustBeLoopback, addr)
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsUnspecified() {
			return fmt.Errorf("%w: unspecified IP %q binds all interfaces", ErrControlPlaneMustBeLoopback, host)
		}
		if !ip.IsLoopback() {
			return fmt.Errorf("%w: %q is not a loopback IP", ErrControlPlaneMustBeLoopback, host)
		}
		return nil
	}
	// Hostname — resolve and verify every result is loopback.
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("%w: resolve %q: %v", ErrControlPlaneMustBeLoopback, host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("%w: %q resolved to no addresses", ErrControlPlaneMustBeLoopback, host)
	}
	for _, ip := range ips {
		if ip.IsUnspecified() || !ip.IsLoopback() {
			return fmt.Errorf("%w: hostname %q resolves to non-loopback %s", ErrControlPlaneMustBeLoopback, host, ip)
		}
	}
	return nil
}

// --- HTTP handlers ----------------------------------------------------------

type injectRequest struct {
	RunID  string            `json:"run_id"`
	Kind   FaultKind         `json:"kind"`
	Target string            `json:"target"`
	Params map[string]string `json:"params"`
}

type clearRequest struct {
	RunID string `json:"run_id"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, errorResponse{Error: err.Error()})
}

func (s *Server) handleInject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeErr(w, http.StatusMethodNotAllowed, fmt.Errorf("faultinject: method %s not allowed", r.Method))
		return
	}
	var req injectRequest
	body := http.MaxBytesReader(w, r.Body, maxRequestBodyLen)
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("faultinject: decode body: %v", err))
		return
	}
	if err := ValidateRunID(req.RunID); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if !IsKnownKind(req.Kind) {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("%w: %q", ErrInjectionKindUnknown, req.Kind))
		return
	}
	if len(req.Target) > maxTargetBytes {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("%w: %d bytes", ErrInjectionTargetTooLong, len(req.Target)))
		return
	}
	if len(req.Params) > maxParamEntries {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("%w: %d entries", ErrInjectionParamsTooLarge, len(req.Params)))
		return
	}
	for k, v := range req.Params {
		if len(v) > maxParamValueLen {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("%w: value of %q is %d bytes", ErrInjectionParamsTooLarge, k, len(v)))
			return
		}
	}

	d, err := s.store.Add(req.RunID, req.Kind, req.Target, req.Params)
	if err != nil {
		// Translate known store errors to 400; anything else is a 500.
		switch {
		case errors.Is(err, ErrInjectionRunIDInvalid),
			errors.Is(err, ErrInjectionKindUnknown),
			errors.Is(err, ErrInjectionRateLimited):
			writeErr(w, http.StatusBadRequest, err)
		default:
			writeErr(w, http.StatusInternalServerError, err)
		}
		return
	}
	// Registration audit — proves /inject is not silently dropped on the
	// server side. Hook-fire audits live in hookbridge.go.
	s.audit.emit(AuditRecord{
		TS:     s.cfg.Now().UTC().Format(time.RFC3339Nano),
		RunID:  req.RunID,
		Kind:   req.Kind,
		Hook:   "control.inject",
		Action: "registered",
		Seq:    d.Seq,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "seq": d.Seq})
}

func (s *Server) handleClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeErr(w, http.StatusMethodNotAllowed, fmt.Errorf("faultinject: method %s not allowed", r.Method))
		return
	}
	var req clearRequest
	body := http.MaxBytesReader(w, r.Body, maxRequestBodyLen)
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("faultinject: decode body: %v", err))
		return
	}
	n, err := s.store.Clear(req.RunID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cleared": n})
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeErr(w, http.StatusMethodNotAllowed, fmt.Errorf("faultinject: method %s not allowed", r.Method))
		return
	}
	q := r.URL.Query()
	// Reject ambiguous repeats — a client passing ?run_id=a&run_id=b
	// would otherwise silently fall through to whichever value came
	// first. Surface the ambiguity rather than picking arbitrarily.
	if vals := q["run_id"]; len(vals) > 1 {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("faultinject: run_id query parameter must appear at most once"))
		return
	}
	runID := q.Get("run_id")
	list, err := s.store.List(runID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if list == nil {
		list = []FaultDirective{}
	}
	writeJSON(w, http.StatusOK, list)
}
