package commander

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"
)

// DaemonConfig wires the HTTP listener, WS client, and shared Handler.
type DaemonConfig struct {
	Handler    *Handler
	ListenAddr string
	WS         WSConfig
}

// Daemon orchestrates the local HTTP debug API and outbound observer WS link.
type Daemon struct {
	cfg      DaemonConfig
	handler  *Handler
	wsClient *WSClient

	mu       sync.Mutex
	httpAddr string
}

func NewDaemon(cfg DaemonConfig) *Daemon {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = "127.0.0.1:0"
	}
	return &Daemon{cfg: cfg, handler: cfg.Handler}
}

// HTTPAddr returns the actual bound HTTP address after Run starts.
func (d *Daemon) HTTPAddr() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.httpAddr
}

func (d *Daemon) setHTTPAddr(addr string) {
	d.mu.Lock()
	d.httpAddr = addr
	d.mu.Unlock()
}

// Run starts both transports and blocks until ctx is cancelled or a terminal
// transport error occurs.
func (d *Daemon) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	wsCfg := d.cfg.WS
	wsCfg.Handler = d.handler
	d.wsClient = NewWSClient(wsCfg)

	ln, err := net.Listen("tcp", d.cfg.ListenAddr)
	if err != nil {
		return err
	}
	d.setHTTPAddr(ln.Addr().String())

	srv := &http.Server{Handler: NewHTTPHandler(d.handler, LinkStatusFunc(d.wsClient.Linked))}

	errCh := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		err := srv.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	go func() {
		defer wg.Done()
		if err := d.wsClient.Run(runCtx); err != nil {
			errCh <- err
		}
	}()

	var retErr error
	select {
	case <-ctx.Done():
	case retErr = <-errCh:
	}
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)

	wg.Wait()
	return retErr
}
