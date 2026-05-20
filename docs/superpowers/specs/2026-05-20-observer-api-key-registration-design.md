# Observer API-Key Registration Design

Date: 2026-05-20

## Context

The `observer-server` ingests telemetry (events, artifacts) from `driver-agent`,
`master-agent`, and `slave-agent` processes. Today every agent must be listed
ahead of time in `observer.yaml` with a hand-issued bearer token:

```yaml
workspaces:
  - id: ws-prod
    name: Production Workspace
    agents:
      - { id: slave-a, role: slave, display_name: Slave A, token: <hex> }
      - { id: slave-b, role: slave, display_name: Slave B, token: <hex> }
      - { id: slave-c, role: slave, display_name: Slave C, token: <hex> }
```

This is friction whenever a new slave joins the fleet: the operator has to
generate a unique token, edit observer's yaml, restart observer, then push the
token to that one agent. Sharing a single token across slaves is not a
workaround — `ValidateToken` is a `QueryRow` over a non-unique `token_hash`
column, so all events would be attributed to whichever row sqlite returns
first.

## Goal

Let agents bootstrap themselves into the `agents` table at startup using a
**shared per-workspace API key**, similar to Kubernetes node bootstrap tokens or
Vault AppRole. After the first successful register call each agent holds a
private per-agent token and uses it for every subsequent ingest request — the
existing rate-limit, attribution, and audit story is unchanged.

This change is scoped to **observer only**. It does not touch agentserver, the
agentserver workspace registry, or the way agents authenticate against
agentserver.

## Non-goals

- TTLs, refresh tokens, or revocation endpoints. Tokens are rotated by
  re-registering; an `agent_id` only ever holds one valid token at a time.
- Multi-key role policies (`allowed_roles: [slave]`), per-key labels beyond a
  human-readable id, or admin endpoints to mint/list/revoke api-keys at
  runtime. All current management is via yaml + restart.
- Backwards compatibility with the old `agents:` yaml block. There are no
  production deployments using it.

## Architecture

```
┌────────────────────────────────────────────────────────────────────┐
│ bootstrap (operator, one-time)                                     │
│   • generate api_key locally (openssl rand -hex 32)                │
│   • write to /etc/observer/observer.yaml under workspaces[].api_keys│
│   • distribute the same api_key to each agent's config             │
└────────────────────────────────────────────────────────────────────┘
                              │ observer start
                              ▼
┌────────────────────────────────────────────────────────────────────┐
│ observer-server                                                    │
│   main.go:                                                         │
│     • UpsertWorkspace(...)                                         │
│     • ReplaceAPIKeysForWorkspace(workspaceID, yamlKeys)            │
│         (DELETE FROM api_keys WHERE workspace_id=? then INSERT)    │
│   HTTP:                                                            │
│     POST /api/agents/register                                      │
│       Authorization: Bearer <api_key>                              │
│       body: { agent_id, role, display_name? }                      │
│     existing ingest endpoints unchanged                            │
└────────────────────────────────────────────────────────────────────┘
                              │ first agent start
                              ▼
┌────────────────────────────────────────────────────────────────────┐
│ agent                                                              │
│   • read api_key from local config                                 │
│   • POST /api/agents/register → receive { token, ... }             │
│   • persist token to local state dir                               │
│   • use Bearer <token> for all subsequent observer calls           │
│   • on lost/missing token file → re-register, old token invalidates│
└────────────────────────────────────────────────────────────────────┘
```

## Component changes

### `internal/observerstore/schema.sql`

Append one new table, no migrations on existing tables.

```sql
CREATE TABLE IF NOT EXISTS api_keys (
    workspace_id TEXT NOT NULL,
    id           TEXT NOT NULL,
    key_hash     TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    PRIMARY KEY (workspace_id, id),
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id)
);

CREATE INDEX IF NOT EXISTS idx_api_keys_hash ON api_keys(key_hash);
```

`key_hash` reuses the existing `tokenHash()` helper so all hashed secrets share
one algorithm.

### `internal/observerstore/store.go`

New methods:

```go
// UpsertAPIKey inserts or replaces the api key identified by (workspaceID, id).
// Empty key is rejected. workspaceID must already exist (FK).
func (s *Store) UpsertAPIKey(workspaceID, id, key string) error

// ReplaceAPIKeysForWorkspace deletes all api_keys rows for workspaceID, then
// inserts the supplied set in a single transaction. Used by main.go at boot
// to reconcile yaml ↔ db.
func (s *Store) ReplaceAPIKeysForWorkspace(workspaceID string, keys []APIKeySpec) error

// LookupAPIKey returns the workspace_id and key id whose key_hash matches the
// supplied plaintext key. ok=false when no row matches; err is reserved for
// real db errors.
func (s *Store) LookupAPIKey(key string) (workspaceID, keyID string, ok bool, err error)
```

```go
type APIKeySpec struct {
    ID  string
    Key string
}
```

`UpsertAgent` is unchanged and remains the path that mutates the `agents` table
— it is now called only by the register handler.

### `cmd/observer-server/main.go`

- yaml decoder switches to **strict mode** (`yaml.Decoder.KnownFields(true)`) so
  the obsolete `agents:` field becomes a startup error with a clear message
  rather than being silently ignored.
- New `WorkspaceConfig` shape:

  ```go
  type WorkspaceConfig struct {
      ID      string         `yaml:"id"`
      Name    string         `yaml:"name"`
      APIKeys []APIKeyConfig `yaml:"api_keys"`
  }
  type APIKeyConfig struct {
      ID  string `yaml:"id"`
      Key string `yaml:"key"`
  }
  ```
- Boot loop becomes:

  ```go
  for _, ws := range cfg.Workspaces {
      if len(ws.APIKeys) == 0 {
          log.Fatalf("workspace %q has no api_keys configured", ws.ID)
      }
      if err := st.UpsertWorkspace(...); err != nil { ... }
      if err := st.ReplaceAPIKeysForWorkspace(ws.ID, toSpecs(ws.APIKeys)); err != nil { ... }
  }
  ```
- The old `agents` loop is deleted.

### `internal/observerweb/server.go`

Register the new route:

```go
mux.HandleFunc("/api/agents/register", h.register)
```

Handler outline:

```go
func (h *handler) register(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, "method not allowed", http.StatusMethodNotAllowed); return
    }
    apiKey, ok := bearerToken(r.Header.Get("Authorization"))
    if !ok { http.Error(w, "missing or invalid bearer token", 401); return }

    workspaceID, keyID, ok, err := h.s.LookupAPIKey(apiKey)
    if err != nil { http.Error(w, "internal", 500); return }
    if !ok { http.Error(w, "invalid api key", 403); return }

    var req registerRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, "bad json", 400); return
    }
    if err := req.validate(); err != nil {
        http.Error(w, err.Error(), 400); return
    }

    token := mustRandomHex(32)               // 64 hex chars
    agent := observerstore.Agent{
        WorkspaceID: workspaceID,
        ID:          req.AgentID,
        Role:        req.Role,
        DisplayName: orDefault(req.DisplayName, req.AgentID),
    }
    if err := h.s.UpsertAgent(agent, token); err != nil {
        http.Error(w, "internal", 500); return
    }
    log.Printf("observer: registered agent ws=%s id=%s role=%s via api_key_id=%s",
        workspaceID, req.AgentID, req.Role, keyID)

    writeJSON(w, 200, registerResponse{
        WorkspaceID: workspaceID,
        AgentID:     agent.ID,
        Role:        agent.Role,
        DisplayName: agent.DisplayName,
        Token:       token,
    })
}
```

Validation rules:

- `agent_id`: required, must match `^[A-Za-z0-9_-]{1,64}$`
- `role`: required, must be one of `driver|master|slave` (use existing
  `observer.Role*` constants)
- `display_name`: optional; defaults to `agent_id`

The bearer parser is the existing `bearerToken()` helper. The new handler
**does not** fall through to `ValidateToken` — only `LookupAPIKey` is consulted,
so a per-agent token cannot be used to enroll another agent.

### Agent-side config (`cmd/{driver,master,slave}-agent/config.example.yaml`)

The `observer:` block changes from holding a static token to holding the
api-key plus a path where the agent persists its issued token:

```yaml
observer:
  enabled: true
  url: http://39.104.86.73:8090
  workspace_id: ws-prod
  agent_id: slave-a            # operator-assigned identity
  api_key: ak_4f9c8e7d…        # shared per-workspace bootstrap
  token_state_path: /var/lib/loom/slave-a/observer.token
```

Behavior at agent startup:

1. If `token_state_path` exists and is non-empty → use it as Bearer for
   ingest.
2. Otherwise call `POST /api/agents/register`, write returned `token` to
   `token_state_path` (0600), continue.
3. On 401 from any ingest call → invalidate cached token file, re-run step 2.

The agent-side implementation is **out of scope for this spec**; it gets its
own design + plan once the observer-side endpoint is in place. This spec lists
the agent contract only so that the observer side is built against a concrete
client.

## Endpoint contract

```
POST /api/agents/register
Authorization: Bearer <api_key>
Content-Type: application/json

{
  "agent_id":     "slave-a",
  "role":         "slave",
  "display_name": "Slave A"
}
```

Successful response (200):

```json
{
  "workspace_id": "ws-prod",
  "agent_id":     "slave-a",
  "role":         "slave",
  "display_name": "Slave A",
  "token":        "<64 hex chars>"
}
```

Error responses:

| HTTP | Trigger | Body |
|---|---|---|
| 400 | malformed JSON, missing/invalid `agent_id`, invalid `role` | `{"error":"<reason>"}` |
| 401 | missing `Authorization` header, not `Bearer ` prefix | `{"error":"missing or invalid bearer token"}` |
| 403 | api-key well-formed but not found | `{"error":"invalid api key"}` |
| 405 | method != POST | `method not allowed` |
| 500 | db error | `{"error":"internal"}` (details only in server log) |

Logging:

- Success line includes `workspace_id`, `agent_id`, `role`, and the matching
  `api_key_id` so operators can trace which bootstrap key was used.
- Failures log the reason plus `api_key_id` when known.
- Plaintext `token` and `api_key` are never logged.

## Behavior matrix

| Scenario | Result |
|---|---|
| New agent registers for the first time | 200, new token, new row in `agents` |
| Same agent restarts with same `agent_id` | 200, brand-new token, old token invalidated by `ON CONFLICT … DO UPDATE SET token_hash=excluded.token_hash` |
| Attacker uses api-key to register an existing `agent_id` | 200, real agent's token is invalidated — accepted trade-off of a shared bootstrap credential |
| api-key removed from yaml + observer restart | `ReplaceAPIKeysForWorkspace` deletes the row; later registers with that key → 403 |
| Workspace declared with empty `api_keys` | observer exits at boot with explicit log message |
| yaml carries the obsolete `agents:` field | observer exits at boot — yaml decoder strict mode catches the unknown field |

## Security considerations

The api-key is a **shared bootstrap credential**. The threat model is:

- **Possession of the api-key ⇒ ability to register as any `agent_id` in the
  workspace**, including stealing an existing agent's identity (the previous
  token is invalidated and ingest will start failing 401).
- The api-key is stored in plaintext in `observer.yaml` and in each agent's
  config. Treat both files as secrets (`mode 0640 root:observer` on the
  observer side, equivalent restrictive perms on each agent).
- Per-agent tokens never leave the `agents` table in plaintext after issuance
  — `UpsertAgent` stores `tokenHash(token)`. Returning the plaintext token in
  the register response is the one and only opportunity for the client to
  capture it; agents must persist it themselves.

Out-of-scope mitigations (left as follow-up if the threat model demands):

1. Multiple labeled api-keys per workspace, scoped by `allowed_roles`.
2. One-time enrollment tokens minted by an admin endpoint (true K8s
   bootstrap-token model).
3. mTLS or signed JWT instead of bearer tokens.

## Testing plan (TDD)

Tests are written first; each item below is one test function.

`internal/observerstore/store_test.go`:

- `TestUpsertAPIKey_RoundTrip` — insert then `LookupAPIKey` returns the right
  workspace_id and key id.
- `TestUpsertAPIKey_Replaces` — re-inserting `(ws, id)` with a new key value:
  the new key resolves; the old key no longer resolves.
- `TestUpsertAPIKey_RejectsEmptyKey` — empty string fails fast.
- `TestUpsertAPIKey_RequiresWorkspace` — unknown workspace_id → FK error.
- `TestLookupAPIKey_Miss` — unknown key → `ok=false`, no error.
- `TestReplaceAPIKeysForWorkspace_DeletesMissing` — running reconcile with a
  shorter list drops the rows no longer present.

`internal/observerweb/server_test.go`:

- `TestRegister_Success` — valid api-key + valid body → 200, token is 64 hex
  chars, `agents` row created with matching fields.
- `TestRegister_TokenWorksForIngest` — POST a sample event using the returned
  token → 200 (end-to-end verification that issuance and ingest agree).
- `TestRegister_ReissueInvalidatesOldToken` — register twice for the same
  `agent_id`; the first token used for ingest → 401.
- `TestRegister_BadAPIKey` — unknown api-key → 403.
- `TestRegister_MissingBearer` — no `Authorization` header → 401.
- `TestRegister_RejectsPerAgentTokenAsAPIKey` — present a known per-agent
  token in `Authorization: Bearer` → 403 (defense against confusing the two
  credential types).
- `TestRegister_BadRole` — `role:"hacker"` → 400.
- `TestRegister_BadAgentID` — disallowed characters or oversize → 400.
- `TestRegister_MethodNotAllowed` — GET → 405.
- `TestRegister_BadJSON` — malformed body → 400.

`cmd/observer-server/main_test.go`:

- `TestServerBoot_AcceptsAPIKeyOnlyConfig` — yaml with only `workspaces +
  api_keys` boots cleanly; `agents` table starts empty.
- `TestServerBoot_FailsWithEmptyAPIKeys` — workspace declared without keys →
  non-zero exit, log message references the workspace.
- `TestServerBoot_FailsOnObsoleteAgentsField` — yaml containing `agents:`
  field → decoder error, non-zero exit.
- `TestServerBoot_ReconcilesAPIKeyDeletion` — start observer, register an
  agent, edit yaml to drop the api-key, restart; the dropped key fails
  registration with 403 while the previously-issued agent token still works.

## Rollout

The 39.104.86.73 observer currently runs the old static-agents schema. After
this change ships, the operator migration is:

1. Build & scp the new binary to `/opt/observer/observer-server`.
2. Rewrite `/etc/observer/observer.yaml`:
   - Remove the `agents:` block.
   - Add one `api_keys:` entry per workspace
     (`openssl rand -hex 32` for each `key` value).
3. `sudo systemctl restart observer`.
4. Update each agent's config to swap `observer.token` for `observer.api_key`
   + `observer.token_state_path`; first-start auto-registers and writes the
   per-agent token.

There is no in-flight production data to preserve.
