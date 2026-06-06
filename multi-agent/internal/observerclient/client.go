package observerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/yourorg/multi-agent/internal/observer"
)

const (
	queueSize         = 128
	closeTimeout      = 3 * time.Second
	registerTimeout   = 5 * time.Second
	reRegisterCoolDur = 60 * time.Second
)

type Config struct {
	Enabled          bool
	TelemetryEnabled bool
	TelemetryAPIKey  string
	URL              string
	WorkspaceID      string
	WorkspaceName    string // optional; first-writer-wins at observer
	AgentID          string
	AgentRole        string
	APIKey           string
	TokenStatePath   string
}

type Client struct {
	cfg              Config
	url              string // /api/events
	enabled          bool
	telemetryEnabled bool
	queue            chan observer.Event
	http             *http.Client

	tokenMu        sync.Mutex
	token          string
	lastReRegister time.Time

	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

// New constructs an observer client. When cfg.Enabled is true, New blocks
// synchronously while it either loads a cached token from cfg.TokenStatePath
// or calls register() against cfg.URL. A failure here is fatal — main()
// should log.Fatal and let systemd Restart=on-failure retry.
func New(cfg Config) (*Client, error) {
	c := &Client{
		cfg:              cfg,
		url:              strings.TrimRight(cfg.URL, "/") + "/api/events",
		enabled:          cfg.Enabled && cfg.URL != "",
		telemetryEnabled: cfg.Enabled && cfg.TelemetryEnabled && cfg.URL != "",
		http:             &http.Client{Timeout: 2 * time.Second},
	}
	if !c.enabled {
		return c, nil
	}

	tok, err := c.loadOrRegister(context.Background())
	if err != nil {
		return nil, err
	}
	c.token = tok

	if c.telemetryEnabled {
		c.queue = make(chan observer.Event, queueSize)
		c.wg.Add(1)
		go c.run()
	}
	return c, nil
}

func (c *Client) Enabled() bool {
	return c != nil && c.enabled
}

// Token returns the live per-agent token. Other consumers (e.g. driver's
// ObserverRelay) read this on every request so re-registration propagates.
func (c *Client) Token() string {
	if c == nil || !c.enabled {
		return ""
	}
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	return c.token
}

func (c *Client) Emit(ev observer.Event) {
	if c == nil || !c.enabled || !c.telemetryEnabled {
		return
	}
	if ev.TS == "" {
		ev.TS = time.Now().UTC().Format(time.RFC3339Nano)
	}
	ev.WorkspaceID = c.cfg.WorkspaceID
	ev.AgentID = c.cfg.AgentID
	ev.AgentRole = c.cfg.AgentRole

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	select {
	case c.queue <- ev:
	default:
		fmt.Fprintln(os.Stderr, "observerclient: event queue full; dropping event")
	}
}

func (c *Client) Close() {
	if c == nil || !c.enabled || !c.telemetryEnabled {
		return
	}
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		close(c.queue)
	}
	c.mu.Unlock()

	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(closeTimeout):
		fmt.Fprintln(os.Stderr, "observerclient: close timed out; dropping queued events")
	}
}

func (c *Client) run() {
	defer c.wg.Done()
	for ev := range c.queue {
		c.post(ev)
	}
}

func (c *Client) post(ev observer.Event) {
	body, err := json.Marshal(ev)
	if err != nil {
		fmt.Fprintf(os.Stderr, "observerclient: marshal event: %v\n", err)
		return
	}
	req, err := http.NewRequest(http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "observerclient: build request: %v\n", err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+c.Token())
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.TelemetryAPIKey != "" {
		req.Header.Set("X-Loom-Telemetry-Key", c.cfg.TelemetryAPIKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "observerclient: post event: %v\n", err)
		return
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		// success
	case resp.StatusCode == http.StatusUnauthorized:
		c.handle401(context.Background())
	case resp.StatusCode == http.StatusForbidden:
		fmt.Fprintln(os.Stderr,
			"observerclient: ingest 403 — check observer.workspace_id matches the api-key's workspace")
	default:
		fmt.Fprintf(os.Stderr, "observerclient: post event status: %s\n", resp.Status)
	}
}
