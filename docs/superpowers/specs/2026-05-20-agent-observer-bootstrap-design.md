# Agent-side Observer Bootstrap Design

Date: 2026-05-20

## Context

The companion spec `2026-05-20-observer-api-key-registration-design.md`
delivered the server side: `observer-server` now ships a
`POST /api/agents/register` endpoint that authenticates an api-key bearer
credential, mints a 32-byte random per-agent token, and persists the agent
row via `UpsertAgent`. That work merged to `master` and is live at
`http://39.104.86.73:8090`.

The agent side was explicitly out of scope. Today the three agent binaries
(`cmd/driver-agent`, `cmd/master-agent`, `cmd/slave-agent`) still hold a
static `observer.token` in yaml and pass it straight into
`observerclient.New(Config{... Token: ...})`. The new endpoint exists but
no client calls it.

This spec wires up the agent side end-to-end: agents present a shared
per-workspace api-key on first start, exchange it for a private token,
persist that token locally, use it for ingest, and auto-recover when the
token is invalidated (re-registration triggered by 401).

## Goal

After this change, an operator deploys agents by:

1. Putting a shared api-key into each agent's yaml.
2. Pointing `token_state_path` at a writable file location.
3. Starting the binary. First start blocks on `register`, writes the
   issued token to disk, then begins normal telemetry. Subsequent starts
   reuse the cached token without contacting `/api/agents/register`.

No more hand-issuing static tokens, no more pre-listing agents in
observer yaml.

## Scope

**In scope:**

- Internal package `internal/observerclient` (extended).
- `internal/config` `Observer` struct + validator.
- `cmd/driver-agent/main.go`, `cmd/master-agent/main.go`,
  `cmd/slave-agent/main.go` — constructor call sites and `log.Fatal`
  paths.
- `cmd/{driver,master,slave}-agent/config.example.yaml` — sample shape.
- `cmd/agent-runtime` / Dockerfile note: the bind-mount or
  `StateDirectory=` that owns the `token_state_path` parent.

**Out of scope:**

- `observer-server`, `observerstore`, `observerweb` — no changes.
- Event schema, ingest contract, dashboard, artifact endpoints.
- Multi-key api-keys, role-scoped api-keys, admin revoke endpoints
  (deferred from the server-side spec; agent side will not pre-build
  hooks for them).
- Existing static-token deployments — there are no production agents
  using the old shape that need a backwards-compat window.

## Decisions (locked during brainstorming)

| Decision | Choice |
|---|---|
| Old `observer.token` field | Removed; yaml strict mode rejects it |
| `token_state_path` policy | Required absolute path; parent dir must exist + be writable |
| 401-during-ingest recovery | Auto re-register with 60s cooldown |
| First-start register failure | Fail-fast; let systemd `Restart=on-failure` retry |
| `agent_id` source | yaml-declared (preserve current convention) |
| Where bootstrap lives | Inside `observerclient` (one package owns the whole protocol) |

## Architecture

```
┌──────────────────────────────────────────────────────────────┐
│ operator (one-time)                                          │
│   • observer-server already has an api-key in its yaml       │
│   • distribute the SAME api-key string to each agent's yaml  │
│   • ensure token_state_path parent exists + writable         │
└──────────────────────────────────────────────────────────────┘
                              │ agent start
                              ▼
┌──────────────────────────────────────────────────────────────┐
│ cmd/<role>-agent/main.go                                     │
│   obs, err := observerclient.New(observerclient.Config{      │
│       Enabled, URL, WorkspaceID, AgentID, AgentRole,         │
│       APIKey, TokenStatePath,                                │
│   })                                                         │
│   if err != nil { log.Fatal(err) }   // systemd restart      │
│   defer obs.Close()                                          │
└──────────────────────────────────────────────────────────────┘
                              │ inside observerclient.New
                              ▼
┌──────────────────────────────────────────────────────────────┐
│ internal/observerclient                                      │
│   1. if !Enabled → return disabled client                    │
│   2. loadOrRegister():                                       │
│        • os.ReadFile(TokenStatePath)                         │
│          - exists, non-empty → token = trimmed content       │
│          - missing/empty    → register() then writeTokenFile │
│   3. cross-check resp.workspace_id == cfg.WorkspaceID        │
│   4. start async post goroutine (existing)                   │
│   5. on 401 from /api/events:                                │
│      handle401() = check 60s cooldown, register again, swap  │
│                    token in-memory + on disk                 │
└──────────────────────────────────────────────────────────────┘
```

## Component changes

### `internal/observerclient/client.go`

`Config` field changes:

```go
type Config struct {
    Enabled        bool
    URL            string
    WorkspaceID    string
    AgentID        string
    AgentRole      string
    APIKey         string  // NEW — bootstrap credential
    TokenStatePath string  // NEW — absolute path; parent must exist + be writable
    // Token field REMOVED
}
```

`New` signature change (this is a breaking change, affecting only the 3
caller sites in `cmd/*-agent/main.go`):

```go
// Old: func New(cfg Config) *Client                  (never errors)
// New:
func New(cfg Config) (*Client, error)
```

`New` is now synchronous on cold start: it may block for up to the
register HTTP timeout (5s) and may return an error. The 3 callers
respond with `log.Fatal(err)`.

New private members on `Client`:

```go
type Client struct {
    cfg     Config
    url     string                // existing — /api/events
    regURL  string                // new     — /api/agents/register
    enabled bool
    queue   chan observer.Event
    http    *http.Client          // shared timeout 2s — keep for ingest

    tokenMu        sync.Mutex     // guards token + lastReRegister
    token          string
    lastReRegister time.Time

    mu     sync.Mutex             // existing — guards closed
    closed bool
    wg     sync.WaitGroup
}
```

New private methods:

```go
// loadOrRegister handles the cold-start sequence: read cached token
// from TokenStatePath if present, otherwise call register synchronously
// and persist the response. Returns the token to seed c.token.
func (c *Client) loadOrRegister(ctx context.Context) (string, error)

// register POSTs to /api/agents/register with Authorization: Bearer APIKey.
// Returns the issued per-agent token plus the workspace_id reported by
// the server (used for the cross-check). Uses a 5-second timeout.
func (c *Client) register(ctx context.Context) (token, workspaceID string, err error)

// writeTokenFile writes the plaintext token to TokenStatePath with
// mode 0600, using O_WRONLY|O_CREATE|O_TRUNC.
func (c *Client) writeTokenFile(token string) error

// handle401 is called by the post loop when an ingest returns 401.
// Enforces a 60-second cooldown; if elapsed, re-registers and swaps
// the in-memory + on-disk token.
func (c *Client) handle401(ctx context.Context)

// currentToken returns the current Bearer credential under the token mutex.
func (c *Client) currentToken() string
```

`post` is modified to:

1. Snapshot `currentToken()` before building the request.
2. On response status 401, call `handle401(ctx)` and drop the current
   event. (The event is unrecoverable — its `ev.WorkspaceID/AgentID` may
   not yet be set if the snapshot path changed, and we have no way to
   resurface it. Live with the loss; the next event will use the new
   token.)
3. On 403, log "workspace or agent mismatch — check observer.workspace_id"
   and drop. **Do not** trigger re-register: this is a yaml config
   error, not a token issue.
4. On other non-2xx, log and drop (existing behavior).

### `internal/config/config.go`

```go
type Observer struct {
    Enabled        bool   `yaml:"enabled"`
    URL            string `yaml:"url"`
    WorkspaceID    string `yaml:"workspace_id"`
    AgentID        string `yaml:"agent_id"`
    APIKey         string `yaml:"api_key"`           // NEW
    TokenStatePath string `yaml:"token_state_path"`  // NEW
    // Token field REMOVED
}
```

Loader switches to strict yaml decoder (consistent with observer-side):

```go
dec := yaml.NewDecoder(bytes.NewReader(data))
dec.KnownFields(true)
```

A leftover `token:` field surfaces as a clear decoder error rather than
silent drop.

Validator (when `Observer.Enabled`):

| Rule | Failure message |
|---|---|
| `URL` non-empty | `observer.url is required` |
| `WorkspaceID` non-empty | `observer.workspace_id is required` |
| `AgentID` non-empty + `^[A-Za-z0-9_-]{1,64}$` | `observer.agent_id must match [A-Za-z0-9_-]{1,64}` |
| `APIKey` non-empty | `observer.api_key is required` |
| `TokenStatePath` non-empty + `filepath.IsAbs` | `observer.token_state_path must be an absolute path` |
| `filepath.Dir(TokenStatePath)` exists + writable | `observer.token_state_path parent directory %q must exist and be writable` |

The "parent exists + writable" check uses `os.Stat` and a tentative
`os.OpenFile` write probe to a sibling tempfile that is deleted right
after; this catches the common operator mistake of forgetting the
StateDirectory.

### `cmd/{driver,master,slave}-agent/main.go`

Each main moves from:

```go
obs := observerclient.New(observerclient.Config{
    Enabled:     cfg.Observer.Enabled,
    URL:         cfg.Observer.URL,
    WorkspaceID: cfg.Observer.WorkspaceID,
    AgentID:     cfg.Observer.AgentID,
    AgentRole:   observer.RoleSlave,  // or Driver / Master
    Token:       cfg.Observer.Token,
})
```

to:

```go
obs, err := observerclient.New(observerclient.Config{
    Enabled:        cfg.Observer.Enabled,
    URL:            cfg.Observer.URL,
    WorkspaceID:    cfg.Observer.WorkspaceID,
    AgentID:        cfg.Observer.AgentID,
    AgentRole:      observer.RoleSlave,  // or Driver / Master
    APIKey:         cfg.Observer.APIKey,
    TokenStatePath: cfg.Observer.TokenStatePath,
})
if err != nil {
    log.Fatalf("observerclient: %v", err)
}
defer obs.Close()
```

### `cmd/{driver,master,slave}-agent/config.example.yaml`

The `observer:` block becomes:

```yaml
observer:
  enabled: false
  url: http://39.104.86.73:8090
  workspace_id: ws-prod
  agent_id: slave-local
  api_key: ak_REPLACE_ME
  token_state_path: /var/lib/loom/slave-local/observer.token
```

The `Enabled: false` default is preserved — operators must explicitly
turn it on after they have provisioned the api-key + state dir.

## State machine

### Startup (synchronous, blocking)

```
                ┌─────────────┐
                │  New(cfg)   │
                └──────┬──────┘
                       ▼
              cfg.Enabled == false ?
              ├─ yes ──▶ return disabled client (Emit no-op), nil
              └─ no
                       ▼
        os.ReadFile(TokenStatePath) non-empty & no error?
        ├─ yes ──▶ token = bytes.TrimSpace(content)
        └─ no  ──▶ register(ctx) — synchronous, 5s timeout
                   ├─ HTTP/network error  ──▶ return nil, err  → main fail-fast
                   ├─ HTTP 4xx (401/400/…) ──▶ return nil, err with status + body
                   └─ HTTP 200             ──▶ token = resp.token
                                              cross-check resp.workspace_id == cfg.WorkspaceID
                                              ├─ mismatch ──▶ return nil, err
                                              │              ("api_key belongs to workspace X, yaml declares Y")
                                              └─ match    ──▶ writeTokenFile(token) — 0600
                       ▼
              launch async post goroutine (existing semantics)
              return *Client, nil
```

### Runtime — ingest with auto-recovery

```
post(ev):
  body  := json.Marshal(ev)
  req   := POST URL /api/events
  req.Header.Set("Authorization", "Bearer " + c.currentToken())
  resp  := c.http.Do(req)

  switch resp.StatusCode {
  case 2xx:
      // success, continue draining queue
  case 401:
      c.handle401(ctx)            // see below
      // current event dropped; next event in queue will use new token
  case 403:
      log "workspace or agent mismatch — check observer.workspace_id"
      // do NOT trigger re-register
  case other:
      log status; drop event
  }
```

### `handle401` — 60-second cooldown

```
c.tokenMu.Lock()
now := time.Now()
if now.Sub(c.lastReRegister) < 60*time.Second {
    c.tokenMu.Unlock()
    log "ingest 401 within cooldown; not re-registering"
    return
}
c.lastReRegister = now
c.tokenMu.Unlock()

newToken, _, err := c.register(ctx)
if err != nil {
    log "ingest 401 → re-register failed: %v", err
    return  // next 401 will hit the cooldown branch
}

c.tokenMu.Lock()
c.token = newToken
c.tokenMu.Unlock()

if writeErr := c.writeTokenFile(newToken); writeErr != nil {
    log warning "token rotated but file write failed: %v", writeErr
    // in-memory token is fine; next cold start will register fresh
}

log "ingest 401 → re-registered successfully"
```

The cooldown ensures that if the api-key is revoked (server-side
operator action), the agent does not hammer `/api/agents/register` —
it logs and degrades to dropped events.

## Behavior matrix

| Scenario | Result |
|---|---|
| `Enabled=false` | `New` returns disabled client, `Emit` is a no-op (preserves today's behavior) |
| Cold start, no token file | `register` runs; on success token file is written 0600; agent proceeds |
| Cold start, token file present & non-empty | File content reused; no `register` call; agent proceeds |
| Cold start, token file present but blank | Treated as missing; `register` runs |
| Cold start, token file content is garbage | `New` succeeds (no validation of file content); first `post` returns 401 → `handle401` re-registers and overwrites file |
| Cold start, register returns 401 (bad api-key) | `New` returns error containing observer's 403 body; `main` fail-fast |
| Cold start, register times out (observer down) | `New` returns error; `main` fail-fast; systemd `Restart=on-failure` retries |
| Cold start, server workspace_id ≠ yaml workspace_id | `New` returns error with both ids surfaced; `main` fail-fast |
| Cold start, `token_state_path` parent missing | Caught at config validation; `main` fail-fast before `observerclient.New` is even called |
| Runtime 401 (token invalidated, attacker stole agent_id, …) | `handle401` re-registers (cooldown permitting), swaps token, drops current event, continues |
| Runtime 401 again within 60s | Cooldown branch: log + drop, no extra register call |
| Runtime 403 (workspace mismatch — yaml is wrong) | Log "check observer.workspace_id", drop event, no re-register |
| Runtime 5xx | Log status, drop event (existing behavior) |
| Token file write fails during re-register | In-memory token is fine; warning logged; next cold start will register again |
| yaml still contains the deprecated `token:` field | Strict decoder rejects with `field token not found in type config.Observer`; `main` fail-fast |

## Migration story

There are no production agents on the old static-token shape, but dev
deployments and image builds do need an update:

1. **Operator action per agent yaml:**
   - Remove `observer.token: <hex>`.
   - Add `observer.api_key: <the shared bootstrap key>` (from
     `/etc/observer/observer.yaml`).
   - Add `observer.token_state_path: <absolute path>`.
   - Ensure parent directory exists with the right owner (`mkdir -p`,
     `chown <agent-user>:<agent-group>`).

2. **Runtime image:** add a `StateDirectory=loom` to the systemd unit
   (or a writable bind-mount for containerised deployments) so the
   agent process can write `/var/lib/loom/<agent-id>/observer.token`.

3. **First start** triggers `register` and writes the token file. Subsequent restarts reuse it.

4. **Rollback:** the previous binary still works against the same
   observer (the static tokens were never deleted from the
   `agents` table during the observer-side migration). To roll back to
   the old shape, redeploy the old binary plus a yaml that uses
   `observer.token` again — but this leaves the api-key path dormant
   (no harm done).

The deployed observer at `39.104.86.73` already has the api-key path
live. The first agent that adopts this spec will populate its row in
the `agents` table on first start.

## Security considerations

- The api-key remains the shared bootstrap credential. Everything the
  server-side spec said about its threat model applies unchanged
  (possession ⇒ ability to enroll as any `agent_id` in the workspace).
- The per-agent token now lives on disk in the agent's
  `token_state_path`. The agent must protect this file:
  - `0600` permissions enforced by `writeTokenFile`.
  - Parent directory owned by the agent's runtime user.
- Token is never logged in plaintext. `handle401` logs "re-registered"
  without the value; `register` logs only the agent_id + workspace_id.
- Token file is written via `O_WRONLY|O_CREATE|O_TRUNC` — no append,
  no leak of previous contents.

Out-of-scope mitigations (deferred):

- Token file encryption at rest.
- Forwarding 401-after-cooldown events to a dead-letter queue.
- Pre-flight `whoami`-style RPC to revalidate cached tokens before the
  first ingest (would avoid the "first event after warm start was lost
  to 401").

## Testing plan (TDD)

`internal/observerclient/client_test.go` (extended; today's file is
short — it will roughly double in size):

- `TestNew_DisabledReturnsNoopClient` — `Enabled=false` ⇒ `New` does
  not contact server, Emit is a no-op.
- `TestNew_ColdStartRegistersAndPersists` — httptest observer serves a
  register response; `New` calls register, returned token = response
  token, file at `TokenStatePath` exists with mode `0600` and matching
  content.
- `TestNew_WarmStartReusesCachedToken` — file pre-seeded; `New` does
  not contact server (use a fail-on-call httptest server that
  registers a `Helper().Fatalf` on any request).
- `TestNew_RegisterFailureReturnsError` — server returns 401; `New`
  returns error with status + body; token file is NOT written.
- `TestNew_RegisterNetworkFailureReturnsError` — server unreachable;
  `New` returns error.
- `TestNew_WorkspaceMismatchReturnsError` — register response
  workspace_id ≠ `cfg.WorkspaceID`; `New` returns error mentioning
  both values.
- *(Parent-dir-missing is owned by the config validator, not
  observerclient — no observerclient test needed; see
  `config_test.go` test below.)*
- `TestPost_NormalIngest` — existing path, regression.
- `TestPost_401TriggersReRegisterAndUpdatesToken` — first response
  401, server then accepts the new token; verify second event is sent
  with new Bearer; token file on disk updated.
- `TestPost_401WithinCooldownSkipsReRegister` — two 401s within 60s →
  only one register call.
- `TestPost_403DoesNotTriggerReRegister` — 403 from ingest → no
  register call; token file unchanged.
- `TestTokenFilePermissions` — after `writeTokenFile`, `os.Stat` mode =
  `0600`.

`internal/config/config_test.go` (extended):

- `TestObserver_RequiresAPIKeyAndTokenStatePath` — yaml omitting either
  field fails validation.
- `TestObserver_RejectsObsoleteTokenField` — yaml with `token:` is
  rejected by the strict decoder.
- `TestObserver_RejectsRelativeTokenStatePath` — non-absolute path
  fails validation.
- `TestObserver_RejectsMissingParentDir` — `token_state_path` whose
  parent doesn't exist fails validation.

`cmd/{driver,master,slave}-agent` smoke (manual / follow-up): build
each binary, point at the staged observer, confirm cold start writes a
token file and subsequent restart reuses it.

## Rollout

After this spec ships:

1. Build the three agent binaries from the merged master.
2. Update each environment's yaml (per the migration steps above).
3. Restart each agent. First start blocks until register completes;
   logs show "registered agent ws=ws-prod id=<agent-id> role=<role>
   via api_key_id=ak-default" on the observer side, and the agent
   logs the post goroutine starting.
4. On rollback (if needed): redeploy old binary + old yaml; the static
   tokens are still present in the observer's `agents` table.

## Open questions

None outstanding. Brainstorming closed all 5 design dimensions and the
remaining sub-decisions (cooldown duration, register timeout, token
file permission) are pinned in the relevant sections above.
