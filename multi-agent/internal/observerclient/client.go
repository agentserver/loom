package observerclient

import (
	"bytes"
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
	queueSize    = 128
	closeTimeout = 3 * time.Second
)

type Config struct {
	Enabled     bool
	URL         string
	WorkspaceID string
	AgentID     string
	AgentRole   string
	Token       string
}

type Client struct {
	cfg     Config
	url     string
	enabled bool
	queue   chan observer.Event
	http    *http.Client

	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

func New(cfg Config) *Client {
	c := &Client{
		cfg:     cfg,
		url:     strings.TrimRight(cfg.URL, "/") + "/api/events",
		enabled: cfg.Enabled && cfg.URL != "" && cfg.Token != "",
		http:    &http.Client{Timeout: 2 * time.Second},
	}
	if !c.enabled {
		return c
	}
	c.queue = make(chan observer.Event, queueSize)
	c.wg.Add(1)
	go c.run()
	return c
}

func (c *Client) Enabled() bool {
	return c != nil && c.enabled
}

func (c *Client) Emit(ev observer.Event) {
	if !c.Enabled() {
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
	if !c.Enabled() {
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
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "observerclient: post event: %v\n", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "observerclient: post event status: %s\n", resp.Status)
	}
}
