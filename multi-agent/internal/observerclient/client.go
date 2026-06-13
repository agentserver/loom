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
	Enabled               bool
	TelemetryEnabled      bool
	TelemetryAPIKey       string
	URL                   string
	WorkspaceID           string
	WorkspaceName         string // optional; first-writer-wins at observer
	AgentID               string
	AgentRole             string
	APIKey                string
	AgentserverProxyToken string
	TokenStatePath        string
	// BootstrapTimeout caps how long New() will wait on the initial
	// loadOrRegister roundtrip. Zero → default 5s. Negative → no timeout
	// (legacy blocking behavior). On timeout, New() returns a degraded
	// Client (enabled=true, token="") so the process starts up; the first
	// Emit hits 401 and handle401 acquires a token when observer recovers.
	BootstrapTimeout time.Duration
	// ForceRegister tells the observer to rotate the token of an agent_id
	// that's still active. Default false → observer 409s on same-id within
	// 5min. Set true when the caller knows the prior process is dead /
	// intentionally being replaced.
	ForceRegister bool
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
	proxyTokenMode bool
	lastReRegister time.Time

	cooldownMu    sync.Mutex
	cooldownUntil time.Time

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
		proxyTokenMode:   cfg.AgentserverProxyToken != "",
	}
	if !c.enabled {
		return c, nil
	}
	if err := validateEnabledConfig(cfg); err != nil {
		return nil, err
	}

	bt := cfg.BootstrapTimeout
	if bt == 0 {
		bt = 5 * time.Second
	}
	var bootstrapCtx context.Context
	var bootstrapCancel context.CancelFunc
	if bt > 0 {
		bootstrapCtx, bootstrapCancel = context.WithTimeout(context.Background(), bt)
	} else {
		bootstrapCtx, bootstrapCancel = context.WithCancel(context.Background())
	}
	defer bootstrapCancel()
	tok, err := c.loadOrRegister(bootstrapCtx)
	if err != nil {
		// Degraded mode: process starts; first Emit triggers handle401 which
		// re-registers once observer is reachable. Far better than hard-fail
		// on transient observer outage where there's no systemd restart
		// to recover (jetson, HPC login nodes).
		// Fixes §1.3 #9 of docs/review-2026-06-13.md.
		fmt.Fprintf(os.Stderr,
			"observerclient: bootstrap failed (%v); entering degraded mode — "+
				"events will queue and post once token is acquired\n", err)
		c.token = ""
	} else {
		c.token = tok
	}

	if c.telemetryEnabled {
		c.queue = make(chan observer.Event, queueSize)
		c.wg.Add(1)
		go c.run()
	}
	return c, nil
}

func validateEnabledConfig(cfg Config) error {
	if cfg.WorkspaceID == "" {
		return fmt.Errorf("observerclient: workspace_id is required when enabled")
	}
	if cfg.AgentID == "" {
		return fmt.Errorf("observerclient: agent_id is required when enabled")
	}
	if cfg.AgentRole == "" {
		return fmt.Errorf("observerclient: agent_role is required when enabled")
	}
	if cfg.AgentserverProxyToken == "" && (cfg.APIKey == "" || cfg.TokenStatePath == "") {
		return fmt.Errorf("observerclient: agentserver proxy token or observer api_key/token_state_path is required when enabled")
	}
	return nil
}

func (c *Client) Enabled() bool {
	return c != nil && c.enabled
}

func (c *Client) TelemetryEnabled() bool {
	return c != nil && c.telemetryEnabled
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

// inCooldown returns the remaining cooldown duration, or 0 if not in
// cooldown. Used by run() to gate dequeue during a 401 re-register window.
// Fixes part of §1.3 #12.
func (c *Client) inCooldown() time.Duration {
	c.cooldownMu.Lock()
	defer c.cooldownMu.Unlock()
	if c.cooldownUntil.IsZero() {
		return 0
	}
	rem := time.Until(c.cooldownUntil)
	if rem <= 0 {
		c.cooldownUntil = time.Time{}
		return 0
	}
	return rem
}

// setCooldown puts run() into a quiet window where it doesn't dequeue
// events (so they don't waste post attempts that would 401 again while
// handle401 is mid-flight). Called at the start of handle401.
func (c *Client) setCooldown(d time.Duration) {
	c.cooldownMu.Lock()
	c.cooldownUntil = time.Now().Add(d)
	c.cooldownMu.Unlock()
}

// clearCooldown is called by handle401 on successful re-register so run()
// resumes dequeue immediately rather than waiting out the full window.
func (c *Client) clearCooldown() {
	c.cooldownMu.Lock()
	c.cooldownUntil = time.Time{}
	c.cooldownMu.Unlock()
}

// isClosed reports whether Close has been called. Exposed for run()'s
// cooldown wait so a long sleep can be aborted on shutdown instead of
// leaking the goroutine for up to reRegisterCoolDur past Close return.
func (c *Client) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *Client) Emit(ev observer.Event) {
	if !c.TelemetryEnabled() {
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
	if !c.TelemetryEnabled() {
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
		// 401 re-register in progress: stop dequeue until handle401 either
		// succeeds (clearCooldown → loop resumes immediately) or the
		// cooldown expires naturally. Otherwise this event (and every
		// queued one after it) would hit 401, be rejected by the
		// per-process cooldown check, and silently drop.
		// Fixes §1.3 #12 of docs/review-2026-06-13.md.
		//
		// Note: we already popped ev from the queue, so we wait it out
		// and then post it. The wait is bounded by cooldownUntil.
		//
		// Sleep in short chunks and re-check c.closed so Close() can
		// interrupt us promptly instead of waiting out the full
		// (up to reRegisterCoolDur) cooldown. The 500ms chunk balances
		// shutdown responsiveness against syscall overhead.
		for {
			rem := c.inCooldown()
			if rem <= 0 {
				break
			}
			chunk := rem
			if chunk > 500*time.Millisecond {
				chunk = 500 * time.Millisecond
			}
			time.Sleep(chunk)
			if c.isClosed() {
				return
			}
		}
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
