# Agent Observer Bootstrap Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire the three agent binaries onto the new `POST /api/agents/register` endpoint: agents present a shared api-key on first start, get a per-agent token, persist it to disk, and auto-recover from a 401 via re-registration with cooldown.

**Architecture:** All bootstrap logic lives in `internal/observerclient` (the package that already owns the wire to observer). `New(cfg) (*Client, error)` blocks synchronously on cold-start `register`, caches the token at `cfg.TokenStatePath` with mode 0600, and on a runtime 401 re-registers with a 60s cooldown. Each agent's `main.go` becomes a simple "if err != nil, log.Fatal". The driver's separate `ObserverRelay` (artifact/contract/snapshot HTTP) reads the live token from the same `Client` via a new `Token()` getter, so token rotation propagates automatically.

**Tech Stack:** Go 1.26, stdlib `net/http`, stdlib `crypto/rand`, `gopkg.in/yaml.v3` (strict mode), `testify/require`, `httptest` for HTTP fakes.

**Spec:** `docs/superpowers/specs/2026-05-20-agent-observer-bootstrap-design.md`

---

## File map

| File | Action | Responsibility |
|---|---|---|
| `multi-agent/internal/observerclient/bootstrap.go` | **create** | `register()`, `readTokenFile()`, `writeTokenFile()`, `loadOrRegister()`, `handle401()` |
| `multi-agent/internal/observerclient/client.go` | modify | `Config` shape, `New` signature, `Token()` getter, `post()` 401/403 routing |
| `multi-agent/internal/observerclient/client_test.go` | modify | New tests + rewrite the 3 existing tests that used the old `Token` field / non-error `New` |
| `multi-agent/internal/observerclient/bootstrap_test.go` | **create** | File-IO helpers, register HTTP, cold/warm-start, workspace mismatch |
| `multi-agent/internal/config/config.go` | modify | `Observer` struct fields, strict yaml decoder, validator |
| `multi-agent/internal/config/config_test.go` | modify | Replace `token` cases with `api_key`/`token_state_path`; strict-mode rejection |
| `multi-agent/internal/driver/config.go` | modify | Mirror the `Observer` struct + validator changes (driver-agent has its own config layer) |
| `multi-agent/internal/driver/config_test.go` | modify | Same fixture migration as above |
| `multi-agent/internal/driver/observer_relay.go` | modify | `NewObserverRelay(cfg, src TokenSource)`; read token per-request via `src.Token()` |
| `multi-agent/internal/driver/tools.go` | modify | Plumb `obs` (which satisfies `TokenSource`) into `NewObserverRelay` and the lazy `observerRelay()` accessor |
| `multi-agent/internal/driver/tools_test.go` | modify | Fixture updates: drop `tools.cfg.Observer.Token = "..."` lines; supply a stub `TokenSource` |
| `multi-agent/cmd/driver-agent/main.go` | modify | `obs, err := observerclient.New(...)`, `log.Fatal` path; pass `obs` to `NewObserverRelay` |
| `multi-agent/cmd/master-agent/main.go` | modify | `obs, err := observerclient.New(...)`, `log.Fatal` path |
| `multi-agent/cmd/slave-agent/main.go` | modify | `obs, err := observerclient.New(...)`, `log.Fatal` path |
| `multi-agent/cmd/driver-agent/config.example.yaml` | modify | Observer block → `api_key`, `token_state_path` |
| `multi-agent/cmd/master-agent/config.example.yaml` | modify | Observer block → `api_key`, `token_state_path` |
| `multi-agent/cmd/slave-agent/config.example.yaml` | modify | Observer block → `api_key`, `token_state_path` |

**Files explicitly NOT touched** (out of scope):

- `internal/observerstore/`, `internal/observerweb/`, `cmd/observer-server/` — server side shipped in the companion plan.
- `internal/dispatch/`, `internal/orchestrator/` — these use `observer.Event` constants but never touch the token.

---

## Implementation notes — read before starting

### Spec gap: `internal/driver/observer_relay.go`

The spec only mentioned `internal/config/Observer` and `cmd/*-agent/main.go`. It missed that `cmd/driver-agent` uses its own `internal/driver/Config` (separate from `internal/config`), and that `internal/driver/observer_relay.go` reaches into `cfg.Observer.Token` directly to POST artifacts/contracts/snapshots to observer — **not** via `observerclient`.

This plan extends the design in the minimum way: a tiny `TokenSource` interface (`Token() string`), implemented by `*observerclient.Client`, passed to `NewObserverRelay(cfg, src)`. `ObserverRelay` reads the live token on every request, so re-register propagates for free.

If you want to amend the spec after this lands, add a paragraph to §"Component changes" mentioning the `TokenSource` interface. The plan does not block on that.

### Concurrency discipline

`observerclient.Client` already uses one mutex for `closed`. This plan adds a **second** mutex `tokenMu` for `token` and `lastReRegister`. Don't reuse the existing `mu` — it's held while draining the queue at Close time and can deadlock if `post` tries to take it for a token read.

`post()` snapshots the token via `c.Token()` before constructing the request — it does NOT hold the token mutex while doing the HTTP round-trip.

### Existing error convention

`observerclient` writes to `os.Stderr` via `fmt.Fprintln` (no structured logger). Match that for the new code paths so we don't introduce a logging dependency.

### Strict yaml decoder cleanup

`internal/config/config.go` and `internal/driver/config.go` both currently use `yaml.Unmarshal`. After the strict-decoder switch, any leftover `token:` field will fail loud. That's the desired migration signal.

### Test running shortcut

After each task, run only the touched package's tests for fast feedback:

```
go test ./internal/observerclient/...
go test ./internal/config/...
go test ./internal/driver/...
go test ./cmd/...
```

The final task does `go test ./...` to catch cross-package breakage.

---

## Task 1: `bootstrap.go` — token file IO helpers

**Files:**
- Create: `multi-agent/internal/observerclient/bootstrap.go`
- Create: `multi-agent/internal/observerclient/bootstrap_test.go`

- [ ] **Step 1: Write the failing tests**

Create `multi-agent/internal/observerclient/bootstrap_test.go`:

```go
package observerclient

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWriteTokenFileSetsMode0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observer.token")
	require.NoError(t, writeTokenFile(path, "tk_abc123"))

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "tk_abc123", string(got))
}

func TestWriteTokenFileTruncatesExistingContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observer.token")
	require.NoError(t, os.WriteFile(path, []byte("OLD_LONG_CONTENT_xxxxxxxxxxx"), 0o600))

	require.NoError(t, writeTokenFile(path, "new_short"))

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "new_short", string(got))
}

func TestReadTokenFileTrimsWhitespace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observer.token")
	require.NoError(t, os.WriteFile(path, []byte("  tk_xyz789\n"), 0o600))

	got, ok, err := readTokenFile(path)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "tk_xyz789", got)
}

func TestReadTokenFileMissingReturnsNotOk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nope.token")

	_, ok, err := readTokenFile(path)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestReadTokenFileEmptyReturnsNotOk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observer.token")
	require.NoError(t, os.WriteFile(path, []byte("   \n\t"), 0o600))

	_, ok, err := readTokenFile(path)
	require.NoError(t, err)
	require.False(t, ok, "whitespace-only file should be treated as missing")
}
```

- [ ] **Step 2: Run tests, verify they fail**

```
cd multi-agent
go test ./internal/observerclient/ -run 'TestWriteTokenFile|TestReadTokenFile' -v
```

Expected: FAIL with `undefined: writeTokenFile` / `readTokenFile`.

- [ ] **Step 3: Implement the helpers**

Create `multi-agent/internal/observerclient/bootstrap.go`:

```go
package observerclient

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
)

// writeTokenFile writes the plaintext token to path with mode 0600, replacing
// any existing content (O_WRONLY|O_CREATE|O_TRUNC). The parent directory must
// already exist; this is the caller's responsibility (validated at config load).
func writeTokenFile(path, token string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(token); err != nil {
		return err
	}
	return f.Sync()
}

// readTokenFile reads the token from path. ok=false means the file does not
// exist or contains only whitespace; in that case err is nil. Any other I/O
// error is surfaced via err.
func readTokenFile(path string) (token string, ok bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	trimmed := string(bytes.TrimSpace(data))
	if trimmed == "" {
		return "", false, nil
	}
	return trimmed, true, nil
}
```

- [ ] **Step 4: Run tests, verify all pass**

```
cd multi-agent
go test ./internal/observerclient/ -run 'TestWriteTokenFile|TestReadTokenFile' -v
```

Expected: PASS on all 5 tests.

- [ ] **Step 5: Commit**

```
cd multi-agent
git add internal/observerclient/bootstrap.go internal/observerclient/bootstrap_test.go
git commit -m "feat(observerclient): token state file IO helpers

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: `bootstrap.go` — `register()` HTTP call

**Files:**
- Modify: `multi-agent/internal/observerclient/bootstrap.go` (append `register` function)
- Modify: `multi-agent/internal/observerclient/bootstrap_test.go` (append HTTP tests)

- [ ] **Step 1: Write the failing tests**

Append to `multi-agent/internal/observerclient/bootstrap_test.go`:

```go
import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"
)

func TestRegisterPostsAPIKeyAndAgentDetails(t *testing.T) {
	var gotAuth, gotPath, gotMethod, gotContentType string
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"workspace_id":"ws-1","agent_id":"slave-a","role":"slave","display_name":"Slave A","token":"tk_issued"}`))
	}))
	defer srv.Close()

	ctx := context.Background()
	token, ws, err := register(ctx, &http.Client{Timeout: 2 * time.Second}, srv.URL, "ak_secret", "slave-a", "slave", "Slave A")
	require.NoError(t, err)
	require.Equal(t, "tk_issued", token)
	require.Equal(t, "ws-1", ws)
	require.Equal(t, http.MethodPost, gotMethod)
	require.Equal(t, "/api/agents/register", gotPath)
	require.Equal(t, "Bearer ak_secret", gotAuth)
	require.Equal(t, "application/json", gotContentType)
	require.Equal(t, "slave-a", gotBody["agent_id"])
	require.Equal(t, "slave", gotBody["role"])
	require.Equal(t, "Slave A", gotBody["display_name"])
}

func TestRegisterStrips_TrailingSlashOnURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/agents/register", r.URL.Path)
		_, _ = w.Write([]byte(`{"workspace_id":"ws-1","token":"tk_ok"}`))
	}))
	defer srv.Close()

	ctx := context.Background()
	_, _, err := register(ctx, http.DefaultClient, srv.URL+"/", "ak", "agent", "slave", "")
	require.NoError(t, err)
}

func TestRegisterSurfacesNon2xxAsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`invalid api key`))
	}))
	defer srv.Close()

	_, _, err := register(context.Background(), http.DefaultClient, srv.URL, "ak_bad", "agent", "slave", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "403")
	require.Contains(t, err.Error(), "invalid api key")
}

func TestRegisterNetworkFailureReturnsError(t *testing.T) {
	// Closed server URL.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	_, _, err := register(context.Background(), &http.Client{Timeout: 200 * time.Millisecond}, url, "ak", "agent", "slave", "")
	require.Error(t, err)
}

func TestRegisterDefaultsDisplayNameWhenEmpty(t *testing.T) {
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"workspace_id":"ws","token":"tk"}`))
	}))
	defer srv.Close()

	_, _, err := register(context.Background(), http.DefaultClient, srv.URL, "ak", "agent-x", "slave", "")
	require.NoError(t, err)
	// register passes empty display_name through; the server (not us) defaults it.
	// We just want to confirm no "display_name":"" key drops the field — JSON encoder
	// emits the empty string and the server side handles it.
	_, present := gotBody["display_name"]
	require.True(t, present)
	require.Equal(t, "", gotBody["display_name"])
}

// Silence the unused import linter for strings.NewReader in case earlier tests don't use it.
var _ = strings.NewReader
```

- [ ] **Step 2: Run tests, verify they fail**

```
cd multi-agent
go test ./internal/observerclient/ -run 'TestRegister' -v
```

Expected: FAIL with `undefined: register`.

- [ ] **Step 3: Implement `register`**

Append to `multi-agent/internal/observerclient/bootstrap.go`:

```go
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"
)
```

(Merge with the existing import block — `bytes`, `errors`, `io/fs`, `os` were already there from Task 1. Add `context`, `encoding/json`, `fmt`, `io`, `net/http`, `strings`.)

Then append:

```go
type registerRequest struct {
	AgentID     string `json:"agent_id"`
	Role        string `json:"role"`
	DisplayName string `json:"display_name"`
}

type registerResponse struct {
	WorkspaceID string `json:"workspace_id"`
	AgentID     string `json:"agent_id"`
	Role        string `json:"role"`
	DisplayName string `json:"display_name"`
	Token       string `json:"token"`
}

// register POSTs to <baseURL>/api/agents/register with Authorization: Bearer <apiKey>.
// Returns the issued per-agent token and the workspace_id reported by the server.
// The caller is responsible for cross-checking workspace_id against the operator-declared value.
func register(
	ctx context.Context,
	httpc *http.Client,
	baseURL, apiKey, agentID, role, displayName string,
) (token, workspaceID string, err error) {
	body, _ := json.Marshal(registerRequest{
		AgentID:     agentID,
		Role:        role,
		DisplayName: displayName,
	})
	url := strings.TrimRight(baseURL, "/") + "/api/agents/register"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpc.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("observerclient register: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", "", fmt.Errorf("observerclient register: HTTP %d: %s",
			resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	var rr registerResponse
	if err := json.NewDecoder(resp.Body).Decode(&rr); err != nil {
		return "", "", fmt.Errorf("observerclient register: decode response: %w", err)
	}
	if rr.Token == "" {
		return "", "", errors.New("observerclient register: server returned empty token")
	}
	return rr.Token, rr.WorkspaceID, nil
}
```

- [ ] **Step 4: Run tests, verify all pass**

```
cd multi-agent
go test ./internal/observerclient/ -run 'TestRegister' -v
```

Expected: PASS on all 5 new tests plus the 5 from Task 1 still PASS.

- [ ] **Step 5: Commit**

```
cd multi-agent
git add internal/observerclient/bootstrap.go internal/observerclient/bootstrap_test.go
git commit -m "feat(observerclient): register() — POST /api/agents/register

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: `Client` API rewire — Config fields, `New(cfg) (*Client, error)`, `Token()`, `loadOrRegister()`

This is the biggest task because the config-field change, the `New` signature change, and the loadOrRegister wiring are circularly dependent — splitting them leaves the package un-buildable between steps. Existing tests (`TestClientEmitPostsBearerEvent`, `TestClientDisabledDropsEvents`, `TestClientCloseReturnsWhenPostStalls`) all use the old shape and must be rewritten in this same task.

**Files:**
- Modify: `multi-agent/internal/observerclient/client.go`
- Modify: `multi-agent/internal/observerclient/bootstrap.go` (append `loadOrRegister`)
- Modify: `multi-agent/internal/observerclient/client_test.go` (rewrite existing tests + add cold/warm-start tests)
- Modify: `multi-agent/internal/observerclient/bootstrap_test.go` (add `loadOrRegister` tests)

- [ ] **Step 1: Update `client.go` — Config fields, Client struct, New signature, Emit/Close/run/post wiring**

Overwrite `multi-agent/internal/observerclient/client.go` with:

```go
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
	Enabled        bool
	URL            string
	WorkspaceID    string
	AgentID        string
	AgentRole      string
	APIKey         string
	TokenStatePath string
}

type Client struct {
	cfg     Config
	url     string // /api/events
	enabled bool
	queue   chan observer.Event
	http    *http.Client

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
		cfg:     cfg,
		url:     strings.TrimRight(cfg.URL, "/") + "/api/events",
		enabled: cfg.Enabled && cfg.URL != "",
		http:    &http.Client{Timeout: 2 * time.Second},
	}
	if !c.enabled {
		return c, nil
	}

	tok, err := c.loadOrRegister(context.Background())
	if err != nil {
		return nil, err
	}
	c.token = tok

	c.queue = make(chan observer.Event, queueSize)
	c.wg.Add(1)
	go c.run()
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
	req.Header.Set("Authorization", "Bearer "+c.Token())
	req.Header.Set("Content-Type", "application/json")
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
```

(`handle401` is added in Task 4; this leaves a forward reference. The package will not build until Step 2 of Task 3 lands `loadOrRegister`, and won't build cleanly until Task 4 lands `handle401`. To keep this task self-contained, add a temporary stub at the bottom of `client.go`:)

```go
// Temporary stub — replaced by the real handle401 in Task 4.
func (c *Client) handle401(_ context.Context) {
	fmt.Fprintln(os.Stderr, "observerclient: ingest 401 — re-register handler not yet wired")
}
```

- [ ] **Step 2: Add `loadOrRegister` to `bootstrap.go`**

Append to `multi-agent/internal/observerclient/bootstrap.go`:

```go
// loadOrRegister returns the token to seed Client.token. It prefers an
// existing on-disk token (cfg.TokenStatePath) and falls back to a synchronous
// register call. On a register success the response is cross-checked against
// cfg.WorkspaceID and persisted to disk before returning.
func (c *Client) loadOrRegister(ctx context.Context) (string, error) {
	if tok, ok, err := readTokenFile(c.cfg.TokenStatePath); err != nil {
		return "", fmt.Errorf("observerclient: read token state: %w", err)
	} else if ok {
		return tok, nil
	}

	regCtx, cancel := context.WithTimeout(ctx, registerTimeout)
	defer cancel()

	httpc := &http.Client{Timeout: registerTimeout}
	tok, ws, err := register(regCtx, httpc, c.cfg.URL, c.cfg.APIKey,
		c.cfg.AgentID, c.cfg.AgentRole, c.cfg.AgentID)
	if err != nil {
		return "", err
	}
	if ws != c.cfg.WorkspaceID {
		return "", fmt.Errorf(
			"observerclient: api_key belongs to workspace %q but yaml declares observer.workspace_id=%q",
			ws, c.cfg.WorkspaceID)
	}
	if err := writeTokenFile(c.cfg.TokenStatePath, tok); err != nil {
		return "", fmt.Errorf("observerclient: persist token: %w", err)
	}
	return tok, nil
}
```

Note: `loadOrRegister` passes `c.cfg.AgentID` as both `agentID` and `displayName` — the server treats empty display_name as "default to agent_id", but the spec says client-side we pass agent_id explicitly. Either is fine; passing agent_id keeps the wire request informative.

- [ ] **Step 3: Add bootstrap_test.go tests for `loadOrRegister`**

Append to `multi-agent/internal/observerclient/bootstrap_test.go`:

```go
func newTestClient(t *testing.T, cfg Config) *Client {
	t.Helper()
	return &Client{cfg: cfg}
}

func TestLoadOrRegister_WarmStartReusesCachedToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observer.token")
	require.NoError(t, os.WriteFile(path, []byte("cached_token\n"), 0o600))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("register endpoint should not be called when token file exists; got %s", r.URL.Path)
	}))
	defer srv.Close()

	c := newTestClient(t, Config{
		URL: srv.URL, WorkspaceID: "ws-1", AgentID: "agent-1",
		AgentRole: "slave", APIKey: "ak", TokenStatePath: path,
	})
	tok, err := c.loadOrRegister(context.Background())
	require.NoError(t, err)
	require.Equal(t, "cached_token", tok)
}

func TestLoadOrRegister_ColdStartCallsRegisterAndPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observer.token")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"workspace_id":"ws-1","agent_id":"agent-1","role":"slave","token":"tk_fresh"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, Config{
		URL: srv.URL, WorkspaceID: "ws-1", AgentID: "agent-1",
		AgentRole: "slave", APIKey: "ak", TokenStatePath: path,
	})
	tok, err := c.loadOrRegister(context.Background())
	require.NoError(t, err)
	require.Equal(t, "tk_fresh", tok)

	// File should be written with mode 0600 and matching content.
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	got, _ := os.ReadFile(path)
	require.Equal(t, "tk_fresh", string(got))
}

func TestLoadOrRegister_WorkspaceMismatchReturnsErrorAndDoesNotPersist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observer.token")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"workspace_id":"OTHER","token":"tk"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, Config{
		URL: srv.URL, WorkspaceID: "ws-1", AgentID: "agent-1",
		AgentRole: "slave", APIKey: "ak", TokenStatePath: path,
	})
	_, err := c.loadOrRegister(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "OTHER")
	require.Contains(t, err.Error(), "ws-1")
	_, statErr := os.Stat(path)
	require.True(t, os.IsNotExist(statErr), "token file must not be written when workspace mismatches")
}

func TestLoadOrRegister_RegisterErrorPropagatesAndDoesNotPersist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observer.token")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid api key", http.StatusForbidden)
	}))
	defer srv.Close()

	c := newTestClient(t, Config{
		URL: srv.URL, WorkspaceID: "ws-1", AgentID: "agent-1",
		AgentRole: "slave", APIKey: "ak_bad", TokenStatePath: path,
	})
	_, err := c.loadOrRegister(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "403")
	_, statErr := os.Stat(path)
	require.True(t, os.IsNotExist(statErr))
}
```

- [ ] **Step 4: Rewrite `client_test.go` for the new `New` signature**

Overwrite `multi-agent/internal/observerclient/client_test.go` with:

```go
package observerclient

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/observer"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestNewDisabledSkipsRegisterAndDropsEvents(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	defer srv.Close()

	c, err := New(Config{Enabled: false, URL: srv.URL})
	require.NoError(t, err)
	require.False(t, c.Enabled())

	c.Emit(observer.Event{TaskID: "task-1"})
	c.Close()
	require.Equal(t, int32(0), calls.Load())
}

func TestNewColdStartRegistersAndEmits(t *testing.T) {
	received := make(chan observer.Event, 1)
	var lastAuth atomic.Value
	lastAuth.Store("")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/agents/register":
			_, _ = w.Write([]byte(`{"workspace_id":"ws-1","agent_id":"agent-1","role":"slave","token":"tk_issued"}`))
		case "/api/events":
			lastAuth.Store(r.Header.Get("Authorization"))
			var ev observer.Event
			_ = json.NewDecoder(r.Body).Decode(&ev)
			received <- ev
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "observer.token")
	c, err := New(Config{
		Enabled: true, URL: srv.URL, WorkspaceID: "ws-1",
		AgentID: "agent-1", AgentRole: observer.RoleSlave,
		APIKey: "ak_secret", TokenStatePath: path,
	})
	require.NoError(t, err)
	require.True(t, c.Enabled())
	require.Equal(t, "tk_issued", c.Token())

	c.Emit(observer.Event{TaskID: "task-1"})
	c.Close()

	select {
	case ev := <-received:
		require.Equal(t, "agent-1", ev.AgentID)
		require.Equal(t, "ws-1", ev.WorkspaceID)
		require.Equal(t, observer.RoleSlave, ev.AgentRole)
		require.Equal(t, "Bearer tk_issued", lastAuth.Load())
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive event before timeout")
	}
}

func TestNewWarmStartSkipsRegister(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observer.token")
	require.NoError(t, writeTokenFile(path, "tk_cached"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/agents/register" {
			t.Fatalf("register must not be called when token file exists")
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	c, err := New(Config{
		Enabled: true, URL: srv.URL, WorkspaceID: "ws-1",
		AgentID: "agent-1", AgentRole: observer.RoleSlave,
		APIKey: "ak_secret", TokenStatePath: path,
	})
	require.NoError(t, err)
	require.Equal(t, "tk_cached", c.Token())
	c.Close()
}

func TestNewRegisterFailureReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid api key", http.StatusForbidden)
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "observer.token")
	_, err := New(Config{
		Enabled: true, URL: srv.URL, WorkspaceID: "ws-1",
		AgentID: "agent-1", AgentRole: observer.RoleSlave,
		APIKey: "ak_bad", TokenStatePath: path,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "403")
	_, statErr := os.Stat(path)
	require.True(t, os.IsNotExist(statErr))
}

func TestCloseReturnsWhenPostStalls(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observer.token")
	require.NoError(t, writeTokenFile(path, "tk_cached"))

	c, err := New(Config{
		Enabled: true, URL: "http://observer.example", WorkspaceID: "ws-1",
		AgentID: "agent-1", AgentRole: observer.RoleSlave,
		APIKey: "ak", TokenStatePath: path,
	})
	require.NoError(t, err)

	block := make(chan struct{})
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-block
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Status:     "202 Accepted",
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	})

	c.Emit(observer.Event{TaskID: "task-1"})
	done := make(chan struct{})
	go func() { c.Close(); close(done) }()

	select {
	case <-done:
	case <-time.After(4 * time.Second):
		close(block)
		t.Fatal("Close did not return after bounded flush timeout")
	}
	close(block)
}

// os is needed for the IsNotExist check above.
var _ = json.Marshal
```

Add to the imports at the top if missing: `"os"`.

(Yes, the import block already has `os` indirectly via other code? Double-check by running the build below; add it if `go build` complains.)

- [ ] **Step 5: Run tests, verify all pass**

```
cd multi-agent
go test ./internal/observerclient/ -v
```

Expected: all tests PASS (5 from Task 1, 5 from Task 2, 4 loadOrRegister tests from this task, 5 new client tests from this task). Total ~19 tests.

If `go build` complains about the temporary `handle401` stub: confirm the `context` import is at the top of `client.go`; the stub uses it.

- [ ] **Step 6: Commit**

```
cd multi-agent
git add internal/observerclient/
git commit -m "feat(observerclient): cold-start register + token cache in New

New now blocks on either a cached token at cfg.TokenStatePath or a fresh
register call. The signature changes to (*Client, error) so callers can
fail-fast under systemd. Adds a Token() getter for other in-process
consumers (driver ObserverRelay will use it next). handle401 is a stub
until Task 4 wires the 60s cooldown.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: `handle401` with 60-second cooldown

**Files:**
- Modify: `multi-agent/internal/observerclient/bootstrap.go` (replace the stub)
- Modify: `multi-agent/internal/observerclient/client.go` (remove the stub at bottom)
- Modify: `multi-agent/internal/observerclient/client_test.go` (add 401 + cooldown tests)

- [ ] **Step 1: Write the failing tests**

Append to `multi-agent/internal/observerclient/client_test.go`:

```go
func TestPost401TriggersReRegisterAndUpdatesToken(t *testing.T) {
	var eventCalls atomic.Int32
	var regCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/agents/register":
			regCalls.Add(1)
			_, _ = w.Write([]byte(`{"workspace_id":"ws-1","token":"tk_v2"}`))
		case "/api/events":
			n := eventCalls.Add(1)
			if n == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			// Verify second event has the new token
			if r.Header.Get("Authorization") != "Bearer tk_v2" {
				t.Errorf("second event Authorization want Bearer tk_v2, got %s", r.Header.Get("Authorization"))
			}
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "observer.token")
	require.NoError(t, writeTokenFile(path, "tk_v1"))

	c, err := New(Config{
		Enabled: true, URL: srv.URL, WorkspaceID: "ws-1",
		AgentID: "agent-1", AgentRole: observer.RoleSlave,
		APIKey: "ak", TokenStatePath: path,
	})
	require.NoError(t, err)
	require.Equal(t, "tk_v1", c.Token())

	c.Emit(observer.Event{TaskID: "t1"})
	c.Emit(observer.Event{TaskID: "t2"})
	c.Close()

	require.Equal(t, int32(1), regCalls.Load(), "expected exactly one re-register")
	require.Equal(t, "tk_v2", c.Token())

	// Persisted token file should reflect the rotated value.
	got, _ := os.ReadFile(path)
	require.Equal(t, "tk_v2", string(got))
}

func TestPost401WithinCooldownSkipsReRegister(t *testing.T) {
	var regCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/agents/register":
			regCalls.Add(1)
			_, _ = w.Write([]byte(`{"workspace_id":"ws-1","token":"tk_v2"}`))
		case "/api/events":
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "observer.token")
	require.NoError(t, writeTokenFile(path, "tk_v1"))

	c, err := New(Config{
		Enabled: true, URL: srv.URL, WorkspaceID: "ws-1",
		AgentID: "agent-1", AgentRole: observer.RoleSlave,
		APIKey: "ak", TokenStatePath: path,
	})
	require.NoError(t, err)

	// Five rapid events all return 401; only the first should trigger a register call.
	for i := 0; i < 5; i++ {
		c.Emit(observer.Event{TaskID: "t"})
	}
	c.Close()

	require.Equal(t, int32(1), regCalls.Load(),
		"expected exactly one register call under cooldown; got %d", regCalls.Load())
}

func TestPost403DoesNotTriggerReRegister(t *testing.T) {
	var regCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/agents/register":
			regCalls.Add(1)
			_, _ = w.Write([]byte(`{"workspace_id":"ws-1","token":"tk_v2"}`))
		case "/api/events":
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "observer.token")
	require.NoError(t, writeTokenFile(path, "tk_v1"))

	c, err := New(Config{
		Enabled: true, URL: srv.URL, WorkspaceID: "ws-1",
		AgentID: "agent-1", AgentRole: observer.RoleSlave,
		APIKey: "ak", TokenStatePath: path,
	})
	require.NoError(t, err)
	c.Emit(observer.Event{TaskID: "t"})
	c.Close()

	require.Equal(t, int32(0), regCalls.Load(), "403 must not trigger re-register")
}
```

- [ ] **Step 2: Run tests, verify they fail with the stub behavior**

```
cd multi-agent
go test ./internal/observerclient/ -run 'TestPost401|TestPost403' -v
```

Expected: FAIL on the 401 cases (the stub `handle401` doesn't actually re-register). The 403 test will PASS since `post()` already routes 403 to a no-op log.

- [ ] **Step 3: Replace the stub in `client.go`**

In `multi-agent/internal/observerclient/client.go`, **delete** these lines at the bottom:

```go
// Temporary stub — replaced by the real handle401 in Task 4.
func (c *Client) handle401(_ context.Context) {
	fmt.Fprintln(os.Stderr, "observerclient: ingest 401 — re-register handler not yet wired")
}
```

- [ ] **Step 4: Implement real `handle401` in `bootstrap.go`**

Append to `multi-agent/internal/observerclient/bootstrap.go`:

```go
import "time"
```

(Merge with the existing import block — `time` is new for this file.)

Then append:

```go
// handle401 reacts to a 401 from /api/events by re-registering, swapping
// the in-memory token, and overwriting the token file. A 60-second cooldown
// per process prevents an unbounded re-register storm if the api-key itself
// has been revoked server-side.
func (c *Client) handle401(ctx context.Context) {
	c.tokenMu.Lock()
	now := time.Now()
	if now.Sub(c.lastReRegister) < reRegisterCoolDur {
		c.tokenMu.Unlock()
		fmt.Fprintln(os.Stderr, "observerclient: ingest 401 within cooldown; not re-registering")
		return
	}
	c.lastReRegister = now
	c.tokenMu.Unlock()

	regCtx, cancel := context.WithTimeout(ctx, registerTimeout)
	defer cancel()

	httpc := &http.Client{Timeout: registerTimeout}
	tok, _, err := register(regCtx, httpc, c.cfg.URL, c.cfg.APIKey,
		c.cfg.AgentID, c.cfg.AgentRole, c.cfg.AgentID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "observerclient: ingest 401 → re-register failed: %v\n", err)
		return
	}

	c.tokenMu.Lock()
	c.token = tok
	c.tokenMu.Unlock()

	if writeErr := writeTokenFile(c.cfg.TokenStatePath, tok); writeErr != nil {
		fmt.Fprintf(os.Stderr,
			"observerclient: token rotated in-memory but file write failed: %v\n", writeErr)
	}
	fmt.Fprintln(os.Stderr, "observerclient: ingest 401 → re-registered successfully")
}
```

- [ ] **Step 5: Run tests, verify all pass**

```
cd multi-agent
go test ./internal/observerclient/ -v
```

Expected: all observerclient tests PASS (including the 3 new 401/403 tests).

- [ ] **Step 6: Commit**

```
cd multi-agent
git add internal/observerclient/
git commit -m "feat(observerclient): handle401 — auto re-register with 60s cooldown

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: `internal/config` — Observer field migration + strict yaml

**Files:**
- Modify: `multi-agent/internal/config/config.go`
- Modify: `multi-agent/internal/config/config_test.go`

- [ ] **Step 1: Update `config.go`**

In `multi-agent/internal/config/config.go`:

Replace the `Observer` struct (currently lines 69-75):

```go
type Observer struct {
	Enabled        bool   `yaml:"enabled"`
	URL            string `yaml:"url"`
	WorkspaceID    string `yaml:"workspace_id"`
	AgentID        string `yaml:"agent_id"`
	APIKey         string `yaml:"api_key"`
	TokenStatePath string `yaml:"token_state_path"`
}
```

(`Token` field removed.)

Update `Load()` to use the strict yaml decoder. Replace the `yaml.Unmarshal` block (current lines 87-90) with:

```go
dec := yaml.NewDecoder(bytes.NewReader(data))
dec.KnownFields(true)
var c Config
if err := dec.Decode(&c); err != nil {
	return nil, fmt.Errorf("parse %s: %w", path, err)
}
```

Add `"bytes"` to the import block at the top of the file.

Replace the validator branch for `c.Observer.Enabled` (current lines 117-130) with:

```go
if c.Observer.Enabled {
	if c.Observer.URL == "" {
		return nil, fmt.Errorf("observer.url is required when observer.enabled is true")
	}
	if c.Observer.WorkspaceID == "" {
		return nil, fmt.Errorf("observer.workspace_id is required when observer.enabled is true")
	}
	if c.Observer.AgentID == "" {
		return nil, fmt.Errorf("observer.agent_id is required when observer.enabled is true")
	}
	if c.Observer.APIKey == "" {
		return nil, fmt.Errorf("observer.api_key is required when observer.enabled is true")
	}
	if c.Observer.TokenStatePath == "" {
		return nil, fmt.Errorf("observer.token_state_path is required when observer.enabled is true")
	}
	if !filepath.IsAbs(c.Observer.TokenStatePath) {
		return nil, fmt.Errorf("observer.token_state_path must be an absolute path (got %q)", c.Observer.TokenStatePath)
	}
	parent := filepath.Dir(c.Observer.TokenStatePath)
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("observer.token_state_path parent directory %q must exist", parent)
	}
	probe := filepath.Join(parent, ".observer-write-probe")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("observer.token_state_path parent directory %q must be writable: %w", parent, err)
	}
	_ = f.Close()
	_ = os.Remove(probe)
}
```

Add `"path/filepath"` to the import block.

- [ ] **Step 2: Update `config_test.go`**

Open `multi-agent/internal/config/config_test.go`. The test at lines 161-178 (`TestLoad_ObserverEnabledRequiresDeliveryFields`) currently expects the error message to mention `observer.workspace_id`. The simplest update: leave its yaml unchanged and just verify the error still mentions `observer.workspace_id` (validator still fires on the workspace_id check before reaching api_key). Then add a new test below it covering the new required fields and the strict-mode rejection.

Append to `multi-agent/internal/config/config_test.go`:

```go
import (
	"path/filepath"
	"strings"
)

func TestLoad_ObserverEnabledRequiresAPIKeyAndTokenStatePath(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Join(dir, "state")
	require.NoError(t, os.Mkdir(parent, 0o755))
	tokenPath := filepath.Join(parent, "observer.token")

	cases := []struct {
		name    string
		extra   string
		wantSub string
	}{
		{
			name:    "missing api_key",
			extra:   "  token_state_path: " + tokenPath + "\n",
			wantSub: "observer.api_key",
		},
		{
			name:    "missing token_state_path",
			extra:   "  api_key: ak\n",
			wantSub: "observer.token_state_path",
		},
		{
			name:    "relative token_state_path",
			extra:   "  api_key: ak\n  token_state_path: relative/path\n",
			wantSub: "must be an absolute path",
		},
		{
			name:    "missing parent dir",
			extra:   "  api_key: ak\n  token_state_path: /nonexistent_dir_xyz/observer.token\n",
			wantSub: "parent directory",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "c.yaml")
			body := "server:\n  url: https://example.com\n  name: m\ndiscovery:\n  display_name: a\nobserver:\n  enabled: true\n  url: https://observer.example\n  workspace_id: ws\n  agent_id: a\n" + tc.extra
			require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
			_, err := Load(path)
			require.Error(t, err)
			require.True(t, strings.Contains(err.Error(), tc.wantSub),
				"want error containing %q, got %v", tc.wantSub, err)
		})
	}
}

func TestLoad_ObserverRejectsObsoleteTokenField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
server:
  url: https://example.com
  name: m
discovery:
  display_name: a
observer:
  enabled: true
  url: https://observer.example
  workspace_id: ws
  agent_id: a
  token: leftover-token
`), 0o600))
	_, err := Load(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "token",
		"strict yaml decoder should reject the obsolete token field")
}

func TestLoad_ObserverSuccessWithAPIKeyAndPath(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	require.NoError(t, os.Mkdir(stateDir, 0o755))
	tokenPath := filepath.Join(stateDir, "observer.token")

	path := filepath.Join(t.TempDir(), "c.yaml")
	body := "server:\n  url: https://example.com\n  name: m\ndiscovery:\n  display_name: a\nobserver:\n  enabled: true\n  url: https://observer.example\n  workspace_id: ws\n  agent_id: a\n  api_key: ak_x\n  token_state_path: " + tokenPath + "\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	c, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, "ak_x", c.Observer.APIKey)
	require.Equal(t, tokenPath, c.Observer.TokenStatePath)
}
```

- [ ] **Step 3: Run tests, verify they pass**

```
cd multi-agent
go test ./internal/config/ -v
```

Expected: all PASS. The 2 existing observer tests (`TestLoad_ObserverURLWithEnabledFalseKeepsDisabledAndDefaultsAgentID`, `TestLoad_ObserverEnabledRequiresDeliveryFields`) should still PASS unchanged — `Enabled: false` keeps validator skipped, and the `workspace_id`-missing case errors out before reaching the new fields.

- [ ] **Step 4: Commit**

```
cd multi-agent
git add internal/config/
git commit -m "feat(config): observer.api_key + observer.token_state_path

Drops the deprecated observer.token field. yaml decoder now runs in
strict mode so leftover token: lines surface as a clear error during
migration. Validator additionally requires absolute token_state_path
with a writable parent directory.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: `internal/driver/config.go` — mirror Observer migration

**Files:**
- Modify: `multi-agent/internal/driver/config.go`
- Modify: `multi-agent/internal/driver/config_test.go`

`internal/driver` has its own parallel `Config` because `cmd/driver-agent` loads its config independently from `internal/config`. Apply the same migration.

- [ ] **Step 1: Read the current driver Observer block**

The current shape at `multi-agent/internal/driver/config.go:60-66`:

```go
type Observer struct {
	Enabled     bool   `yaml:"enabled"`
	URL         string `yaml:"url"`
	WorkspaceID string `yaml:"workspace_id"`
	AgentID     string `yaml:"agent_id"`
	Token       string `yaml:"token"`
}
```

- [ ] **Step 2: Apply the same changes as Task 5**

In `multi-agent/internal/driver/config.go`:

Replace the `Observer` struct:

```go
type Observer struct {
	Enabled        bool   `yaml:"enabled"`
	URL            string `yaml:"url"`
	WorkspaceID    string `yaml:"workspace_id"`
	AgentID        string `yaml:"agent_id"`
	APIKey         string `yaml:"api_key"`
	TokenStatePath string `yaml:"token_state_path"`
}
```

In `LoadConfig` (around line 69-80), switch the yaml unmarshal to a strict decoder. Find the block that calls `yaml.Unmarshal` and replace it with:

```go
dec := yaml.NewDecoder(bytes.NewReader(b))
dec.KnownFields(true)
var c Config
if err := dec.Decode(&c); err != nil {
	return nil, fmt.Errorf("parse config: %w", err)
}
```

Add `"bytes"` to the imports if not already present.

Replace the validator branch for `c.Observer.Enabled` (around lines 113-126). Use the same body as Task 5's replacement, with `path/filepath` and `os` already in scope (verify imports — `os` is already imported, add `"path/filepath"` if missing):

```go
if c.Observer.Enabled {
	if c.Observer.URL == "" {
		return nil, fmt.Errorf("observer.url is required when observer.enabled is true")
	}
	if c.Observer.WorkspaceID == "" {
		return nil, fmt.Errorf("observer.workspace_id is required when observer.enabled is true")
	}
	if c.Observer.AgentID == "" {
		return nil, fmt.Errorf("observer.agent_id is required when observer.enabled is true")
	}
	if c.Observer.APIKey == "" {
		return nil, fmt.Errorf("observer.api_key is required when observer.enabled is true")
	}
	if c.Observer.TokenStatePath == "" {
		return nil, fmt.Errorf("observer.token_state_path is required when observer.enabled is true")
	}
	if !filepath.IsAbs(c.Observer.TokenStatePath) {
		return nil, fmt.Errorf("observer.token_state_path must be an absolute path (got %q)", c.Observer.TokenStatePath)
	}
	parent := filepath.Dir(c.Observer.TokenStatePath)
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("observer.token_state_path parent directory %q must exist", parent)
	}
	probe := filepath.Join(parent, ".observer-write-probe")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("observer.token_state_path parent directory %q must be writable: %w", parent, err)
	}
	_ = f.Close()
	_ = os.Remove(probe)
}
```

- [ ] **Step 3: Add the strict-mode rejection test**

`internal/driver/config_test.go` currently has no Observer-block tests (verified by `grep -n 'Observer.Token\|observer.token\|^\s*token:' internal/driver/config_test.go` returning nothing), so no existing fixture migration is needed here.

Append to `multi-agent/internal/driver/config_test.go`:

```go
func TestLoadConfig_ObserverRejectsObsoleteTokenField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
server:
  url: https://example.com
  name: m
discovery:
  display_name: driver-display
observer:
  enabled: true
  url: https://observer.example
  workspace_id: ws
  agent_id: a
  token: leftover-token
`), 0o600))
	_, err := LoadConfig(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "token")
}
```

(Imports needed: `"path/filepath"`, `"os"`, `"testing"`, `"github.com/stretchr/testify/require"`.)

- [ ] **Step 4: Run tests, verify they pass**

```
cd multi-agent
go test ./internal/driver/ -run 'TestLoadConfig' -v
```

Expected: existing driver config tests PASS; new test PASS. (Tools tests will still fail on the `Observer.Token` field that's gone — that's Task 8.)

- [ ] **Step 5: Commit**

```
cd multi-agent
git add internal/driver/config.go internal/driver/config_test.go
git commit -m "feat(driver/config): mirror observer api_key migration

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 7: `internal/driver/observer_relay.go` — read token from observerclient

**Files:**
- Modify: `multi-agent/internal/driver/observer_relay.go`
- Modify: `multi-agent/internal/driver/tools.go` (call-site adjustment)

- [ ] **Step 1: Update `observer_relay.go`**

In `multi-agent/internal/driver/observer_relay.go`:

Add a tiny interface at the top of the file (just after imports):

```go
// TokenSource exposes a live observer Bearer token. *observerclient.Client
// satisfies this; the driver test suite supplies a stub implementation.
type TokenSource interface {
	Token() string
}
```

Replace the `ObserverRelay` struct:

```go
type ObserverRelay struct {
	baseURL string
	src     TokenSource
	http    *http.Client
}
```

(Drop the static `token string` field.)

Replace `NewObserverRelay`:

```go
func NewObserverRelay(cfg *Config, src TokenSource) *ObserverRelay {
	if cfg == nil || !cfg.Observer.Enabled || cfg.Observer.URL == "" || src == nil {
		return nil
	}
	return &ObserverRelay{
		baseURL: strings.TrimRight(cfg.Observer.URL, "/"),
		src:     src,
		http:    http.DefaultClient,
	}
}
```

Find every occurrence of `r.token` inside `ObserverRelay`'s methods (e.g., `req.Header.Set("Authorization", "Bearer "+r.token)`) and replace with `r.src.Token()`. The simplest correct edit is a global search-and-replace across the file from `r.token` to `r.src.Token()`.

- [ ] **Step 2: Update `tools.go` call sites**

In `multi-agent/internal/driver/tools.go`:

Line 49 — `NewTools` constructor — already has `obs ObserverSink` parameter. Adapt the `relay:` field initialization:

```go
return &Tools{
	reg:      reg,
	audit:    audit,
	sdk:      sdk,
	cfg:      cfg,
	observer: obs,
	relay:    NewObserverRelay(cfg, toTokenSource(obs)),
}
```

Line ~95 — the lazy `observerRelay()` accessor:

```go
func (t *Tools) observerRelay() *ObserverRelay {
	if t.relay == nil {
		t.relay = NewObserverRelay(t.cfg, toTokenSource(t.observer))
	}
	return t.relay
}
```

Add the `toTokenSource` helper at the bottom of `tools.go`:

```go
// toTokenSource adapts an ObserverSink (1-method Emit interface) into
// the broader TokenSource the relay needs. The concrete *observerclient.Client
// satisfies both, but consumers like tests may pass a sink that does not —
// in which case the relay degrades to nil (no relay calls).
func toTokenSource(s ObserverSink) TokenSource {
	if s == nil {
		return nil
	}
	if ts, ok := s.(TokenSource); ok {
		return ts
	}
	return nil
}
```

- [ ] **Step 3: Verify compile**

```
cd multi-agent
go build ./internal/driver/
```

Expected: success (no test fixture in the way at this layer). `internal/driver/tools_test.go` won't compile yet — that's Task 8.

- [ ] **Step 4: Commit**

```
cd multi-agent
git add internal/driver/observer_relay.go internal/driver/tools.go
git commit -m "refactor(driver/relay): read live token from observerclient via TokenSource

Adds a one-method TokenSource interface so the relay no longer holds a
snapshot of the static token. NewObserverRelay now takes the same obs
that's already wired into NewTools; token rotation in observerclient
propagates to relay requests automatically.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 8: `internal/driver/tools_test.go` — fixture migration

**Files:**
- Modify: `multi-agent/internal/driver/tools_test.go`

The test fixtures set `tools.cfg.Observer.Token = "..."` directly at exactly these lines (verified by grep at plan-writing time):

- `internal/driver/tools_test.go:522` — `tools.cfg.Observer.Token = "observer-token"`
- `internal/driver/tools_test.go:523` — `tools.relay = NewObserverRelay(tools.cfg)`
- `internal/driver/tools_test.go:563` — `tools.cfg.Observer.Token = "observer-token"`
- `internal/driver/tools_test.go:564` — `tools.relay = NewObserverRelay(tools.cfg)`
- `internal/driver/tools_test.go:633` — `tools.cfg.Observer.Token = "driver-token"`
- `internal/driver/tools_test.go:1058` — `tools.cfg.Observer.Token = "observer-token"`
- `internal/driver/tools_test.go:1101` — `tools.cfg.Observer.Token = "observer-token"`

Five `Token = ...` assignments and two `NewObserverRelay(tools.cfg)` calls. (Line numbers may have drifted by ±a few lines after earlier edits — verify with grep first.)

- [ ] **Step 1: Locate current line numbers**

```
cd multi-agent
grep -n "tools.cfg.Observer.Token\|tools.relay = NewObserverRelay" internal/driver/tools_test.go
```

- [ ] **Step 2: Add a stub TokenSource at the top of the file**

Open `multi-agent/internal/driver/tools_test.go`. Right after the existing package-level test helpers (search for the first `func ` to find a safe spot above), insert:

```go
type stubTokenSource string

func (s stubTokenSource) Token() string { return string(s) }
```

- [ ] **Step 3: Migrate each `Observer.Token` assignment**

For **every** line where `tools.cfg.Observer.Token = "<value>"` appears, replace it with the equivalent api-key shape. Since these tests don't actually exercise observer ingest, the values only need to be non-empty placeholders that satisfy `NewObserverRelay`'s nil-checks. The minimal change:

```go
// Before:
tools.cfg.Observer.Token = "observer-token"

// After:
tools.cfg.Observer.APIKey = "ak-test"
tools.cfg.Observer.TokenStatePath = "/tmp/test-observer-token"
```

For the two `tools.relay = NewObserverRelay(tools.cfg)` lines, change to:

```go
tools.relay = NewObserverRelay(tools.cfg, stubTokenSource("test-token"))
```

- [ ] **Step 3: Verify compile + tests pass**

```
cd multi-agent
go test ./internal/driver/...
```

Expected: PASS on the full driver test suite.

- [ ] **Step 4: Commit**

```
cd multi-agent
git add internal/driver/tools_test.go
git commit -m "test(driver): migrate fixtures off Observer.Token onto stubTokenSource

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 9: Update the 3 agent `main.go` constructors

**Files:**
- Modify: `multi-agent/cmd/driver-agent/main.go`
- Modify: `multi-agent/cmd/master-agent/main.go`
- Modify: `multi-agent/cmd/slave-agent/main.go`

- [ ] **Step 1: driver-agent/main.go**

In `multi-agent/cmd/driver-agent/main.go`, find the existing `obs := observerclient.New(observerclient.Config{...})` block (around line 140) and replace with:

```go
obs, err := observerclient.New(observerclient.Config{
	Enabled:        cfg.Observer.Enabled,
	URL:            cfg.Observer.URL,
	WorkspaceID:    cfg.Observer.WorkspaceID,
	AgentID:        cfg.Observer.AgentID,
	AgentRole:      observer.RoleDriver,
	APIKey:         cfg.Observer.APIKey,
	TokenStatePath: cfg.Observer.TokenStatePath,
})
if err != nil {
	log.Fatalf("observerclient: %v", err)
}
defer obs.Close()
```

Also find the `go driver.NewObserverRelay(cfg).ServePendingLoop(...)` line (around line 162) and change to:

```go
go driver.NewObserverRelay(cfg, obs).ServePendingLoop(ctx, reg, audit, 2*time.Second)
```

Confirm `"log"` is in the imports at the top (it usually is in main.go; if not, add it).

- [ ] **Step 2: master-agent/main.go**

In `multi-agent/cmd/master-agent/main.go`, replace the `observerclient.New(...)` block (around line 67):

```go
obs, err := observerclient.New(observerclient.Config{
	Enabled:        cfg.Observer.Enabled,
	URL:            cfg.Observer.URL,
	WorkspaceID:    cfg.Observer.WorkspaceID,
	AgentID:        cfg.Observer.AgentID,
	AgentRole:      observer.RoleMaster,
	APIKey:         cfg.Observer.APIKey,
	TokenStatePath: cfg.Observer.TokenStatePath,
})
if err != nil {
	log.Fatalf("observerclient: %v", err)
}
defer obs.Close()
```

(Master uses `internal/config`, so `cfg.Observer` already has the new fields after Task 5.)

- [ ] **Step 3: slave-agent/main.go**

In `multi-agent/cmd/slave-agent/main.go`, replace the `observerclient.New(...)` block (around line 91):

```go
obs, err := observerclient.New(observerclient.Config{
	Enabled:        cfg.Observer.Enabled,
	URL:            cfg.Observer.URL,
	WorkspaceID:    cfg.Observer.WorkspaceID,
	AgentID:        cfg.Observer.AgentID,
	AgentRole:      observer.RoleSlave,
	APIKey:         cfg.Observer.APIKey,
	TokenStatePath: cfg.Observer.TokenStatePath,
})
if err != nil {
	log.Fatalf("observerclient: %v", err)
}
defer obs.Close()
```

- [ ] **Step 4: Verify compile**

```
cd multi-agent
go build ./cmd/...
```

Expected: all three binaries build.

- [ ] **Step 5: Commit**

```
cd multi-agent
git add cmd/driver-agent/main.go cmd/master-agent/main.go cmd/slave-agent/main.go
git commit -m "feat(agents): bootstrap observer client via api_key + token_state_path

All three agent binaries now switch on the error returned by
observerclient.New so a failed cold-start register short-circuits to
log.Fatal and lets systemd Restart=on-failure retry. driver-agent
additionally wires the live Client into NewObserverRelay so the
artifact/contract/snapshot path picks up rotated tokens.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 10: Migrate the 3 `config.example.yaml` files

**Files:**
- Modify: `multi-agent/cmd/driver-agent/config.example.yaml`
- Modify: `multi-agent/cmd/master-agent/config.example.yaml`
- Modify: `multi-agent/cmd/slave-agent/config.example.yaml`

- [ ] **Step 1: driver-agent example**

In `multi-agent/cmd/driver-agent/config.example.yaml`, replace the `observer:` block with:

```yaml
observer:
  enabled: false
  url: http://39.104.86.73:8090
  workspace_id: ws-prod
  agent_id: driver-local
  api_key: ak_REPLACE_ME
  token_state_path: /var/lib/loom/driver-local/observer.token
```

- [ ] **Step 2: master-agent example**

In `multi-agent/cmd/master-agent/config.example.yaml`, replace with:

```yaml
observer:
  enabled: false
  url: http://39.104.86.73:8090
  workspace_id: ws-prod
  agent_id: master-local
  api_key: ak_REPLACE_ME
  token_state_path: /var/lib/loom/master-local/observer.token
```

- [ ] **Step 3: slave-agent example**

In `multi-agent/cmd/slave-agent/config.example.yaml`, replace with:

```yaml
observer:
  enabled: false
  url: http://39.104.86.73:8090
  workspace_id: ws-prod
  agent_id: slave-local
  api_key: ak_REPLACE_ME
  token_state_path: /var/lib/loom/slave-local/observer.token
```

- [ ] **Step 4: Commit**

```
cd multi-agent
git add cmd/driver-agent/config.example.yaml cmd/master-agent/config.example.yaml cmd/slave-agent/config.example.yaml
git commit -m "docs(agents): example yaml shows api_key + token_state_path

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 11: Module-wide sanity check

**Files:** none (verification only)

- [ ] **Step 1: `go build ./...`**

```
cd multi-agent
go build ./...
```

Expected: success.

- [ ] **Step 2: `go vet ./...`**

```
cd multi-agent
go vet ./...
```

Expected: clean (no output).

- [ ] **Step 3: Full module tests**

```
cd multi-agent
go test ./...
```

Expected: PASS. Likely failure points to investigate if anything breaks outside the touched packages:

- `tests/scripts/distributed_compose_test.go` — has historically asserted on example yaml shape; will likely need updating analogously to the observer-server migration that already landed there.
- `internal/dispatch/`, `internal/orchestrator/` — use `observer.Event` constants but should not touch the new fields.

If `tests/scripts/distributed_compose_test.go` fails, open it and update the expected substrings to the new api-key shape (`api_key:`, `token_state_path:`) using the same `strings.Contains` pattern that the file already uses.

- [ ] **Step 4: Commit any incidental fixups**

```
cd multi-agent
git status
# If only verification was needed:
echo "nothing to commit; implementation complete"
# If a follow-up test alignment was required:
git add -A
git commit -m "test: align scripts test with api_key agent yaml

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Spec coverage notes

Two spec items intentionally not in this plan as standalone tasks:

- **End-to-end smoke** ("spin up observer-server + agent, verify register-then-ingest works") — spec calls it out as a manual / follow-up. The unit-level integration is captured by `TestNewColdStartRegistersAndEmits` (Task 3) which exercises register-then-ingest against an `httptest.Server` faking both endpoints.
- **README / docs migration paragraph** — operator-facing prose. Skip in this plan; add to a docs follow-up if it turns out the migration is non-obvious.

## Out of scope — followups for separate plans

1. **Production rollout to 39.104.86.73** (or wherever agents end up
   running): build the new binaries from merged master, edit each
   environment yaml, run `mkdir -p /var/lib/loom/<agent-id>`, restart.
   Pure ops.
2. **Token file encryption at rest** / dead-letter queue / pre-flight
   whoami — spec deferred these explicitly.
3. **Spec amendment** to mention `internal/driver/observer_relay.go` +
   `TokenSource`. Optional documentation cleanup.
