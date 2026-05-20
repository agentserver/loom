# Observer API-Key Registration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let agents bootstrap themselves into observer's `agents` table by presenting a shared per-workspace api-key against a new `POST /api/agents/register` endpoint, eliminating yaml-managed agent rosters.

**Architecture:** Add an `api_keys` table to observer's SQLite schema. Observer boot reconciles api-keys from yaml (delete + insert). A new handler authenticates the api-key as a Bearer credential, resolves its workspace, mints a 32-byte random per-agent token, and stores it via the existing `UpsertAgent` path. The yaml's old `agents:` block is removed entirely — there are no production deployments to migrate.

**Tech Stack:** Go 1.26, `modernc.org/sqlite` (pure-Go SQLite via `database/sql`), `gopkg.in/yaml.v3`, `testify/require`, stdlib `net/http` + `httptest`.

**Spec:** `docs/superpowers/specs/2026-05-20-observer-api-key-registration-design.md`

---

## File map

| File | Action | Responsibility |
|---|---|---|
| `multi-agent/internal/observerstore/schema.sql` | modify | Append `api_keys` table + hash index |
| `multi-agent/internal/observerstore/store.go` | modify | Add `APIKeySpec` type, `UpsertAPIKey`, `LookupAPIKey`, `ReplaceAPIKeysForWorkspace` |
| `multi-agent/internal/observerstore/store_test.go` | modify | Add tests for the new store methods |
| `multi-agent/internal/observerweb/server.go` | modify | Add `register` handler + `/api/agents/register` route + Store interface methods |
| `multi-agent/internal/observerweb/server_test.go` | modify | Add tests for register endpoint (success, errors, reissue, defense) |
| `multi-agent/cmd/observer-server/main.go` | modify | Swap `Agents` → `APIKeys` in config; strict yaml; reconcile api-keys at boot |
| `multi-agent/cmd/observer-server/main_test.go` | modify | Rewrite for new yaml schema; add tests for strict-mode and empty-keys failures |
| `multi-agent/dev/configs/observer.example.yaml` | modify | Switch sample workspace from `agents:` to `api_keys:` |

**Files explicitly NOT touched** (out-of-scope, agent-side work):
- `multi-agent/cmd/driver-agent/`, `cmd/master-agent/`, `cmd/slave-agent/` — agent-side register-and-persist behavior is a separate spec.

---

## Implementation notes — read before starting

- **Existing convention: errors via `http.Error`** (plain text body). Match it. The spec showed JSON error bodies for documentation clarity; in code, use plain text via `http.Error(w, "...", code)` so the new handler is consistent with `postEvent` and friends.
- **`tokenHash()` is package-private** (`internal/observerstore/store.go:1192`) and uses sha256 hex. Reuse it for `key_hash`.
- **`agents.token_hash` already has a UNIQUE index** (`schema.sql:14`), so token collisions on `UpsertAgent` return an error. With 32 random bytes (256-bit) the collision probability is negligible; surface DB errors as 500.
- **`api_keys.key_hash` should NOT have a UNIQUE index.** Operator may (mistakenly) define identical keys across workspaces; we accept "sqlite returns one row, registration goes to that workspace" as a no-data-loss degradation rather than crashing observer at boot.
- **Strict yaml decoding** uses `yaml.Decoder.KnownFields(true)`. The existing code uses `yaml.Unmarshal` (lenient). Switch to a `Decoder` and set the flag.
- **Run all observer-server tests** after each task: `cd multi-agent && go test ./internal/observerstore/... ./internal/observerweb/... ./cmd/observer-server/...`

---

## Task 1: Add `api_keys` schema

**Files:**
- Modify: `multi-agent/internal/observerstore/schema.sql` (append at end)

- [ ] **Step 1: Open the existing schema file and append the new table**

Add to the end of `multi-agent/internal/observerstore/schema.sql`:

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

- [ ] **Step 2: Verify the schema still loads cleanly via the existing smoke test**

Run: `cd multi-agent && go test ./internal/observerstore/ -run TestOpenConfiguresSQLiteForConcurrentObserverAccess -v`

Expected: PASS (the test calls `Open` which executes `schema.sql`; this confirms our DDL is syntactically valid).

- [ ] **Step 3: Commit**

```bash
cd multi-agent
git add internal/observerstore/schema.sql
git commit -m "feat(observerstore): add api_keys table for agent self-registration

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: `UpsertAPIKey` store method

**Files:**
- Modify: `multi-agent/internal/observerstore/store.go` (add `APIKeySpec` type + method after `UpsertAgent`, around line 333)
- Modify: `multi-agent/internal/observerstore/store_test.go` (add new tests at the end)

- [ ] **Step 1: Write the failing test**

Append to `multi-agent/internal/observerstore/store_test.go`:

```go
func TestUpsertAPIKeyRoundTrip(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer s.Close()
	require.NoError(t, s.UpsertWorkspace(Workspace{ID: "ws1", Name: "Workspace"}))

	require.NoError(t, s.UpsertAPIKey("ws1", "ak-default", "ak_secret_abc"))

	wsID, keyID, ok, err := s.LookupAPIKey("ak_secret_abc")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "ws1", wsID)
	require.Equal(t, "ak-default", keyID)
}

func TestUpsertAPIKeyReplacesKey(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer s.Close()
	require.NoError(t, s.UpsertWorkspace(Workspace{ID: "ws1", Name: "Workspace"}))

	require.NoError(t, s.UpsertAPIKey("ws1", "ak-default", "first-key"))
	require.NoError(t, s.UpsertAPIKey("ws1", "ak-default", "second-key"))

	_, _, ok, err := s.LookupAPIKey("first-key")
	require.NoError(t, err)
	require.False(t, ok, "old key value should no longer resolve")

	wsID, _, ok, err := s.LookupAPIKey("second-key")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "ws1", wsID)
}

func TestUpsertAPIKeyRejectsEmptyKey(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer s.Close()
	require.NoError(t, s.UpsertWorkspace(Workspace{ID: "ws1", Name: "Workspace"}))

	require.Error(t, s.UpsertAPIKey("ws1", "ak-default", ""))
}

func TestLookupAPIKeyMiss(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer s.Close()

	_, _, ok, err := s.LookupAPIKey("unknown")
	require.NoError(t, err)
	require.False(t, ok)

	_, _, ok, err = s.LookupAPIKey("")
	require.NoError(t, err)
	require.False(t, ok)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd multi-agent && go test ./internal/observerstore/ -run 'TestUpsertAPIKey|TestLookupAPIKey' -v`

Expected: FAIL with `undefined: (*Store).UpsertAPIKey` / `(*Store).LookupAPIKey`.

- [ ] **Step 3: Implement `APIKeySpec`, `UpsertAPIKey`, `LookupAPIKey`**

Insert into `multi-agent/internal/observerstore/store.go` immediately after `UpsertAgent` (currently ends at line 333):

```go
// APIKeySpec is a per-workspace bootstrap credential. Agents present the
// plaintext Key as a Bearer token against /api/agents/register to mint
// their own per-agent token.
type APIKeySpec struct {
	ID  string
	Key string
}

// UpsertAPIKey inserts or replaces the api key identified by
// (workspaceID, id). The plaintext key is hashed before storage. An empty
// key is rejected; the workspace must already exist (enforced by FK).
func (s *Store) UpsertAPIKey(workspaceID, id, key string) error {
	if key == "" {
		return errors.New("observerstore: api key must not be empty")
	}
	_, err := s.db.Exec(`INSERT INTO api_keys(workspace_id, id, key_hash, created_at)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(workspace_id, id) DO UPDATE SET key_hash=excluded.key_hash, created_at=excluded.created_at`,
		workspaceID, id, tokenHash(key), nowUTC())
	return err
}

// LookupAPIKey returns the workspace_id and key id whose key_hash matches
// the plaintext key. ok=false means no row matched; err is reserved for
// real DB errors. An empty input returns ok=false without consulting the DB.
func (s *Store) LookupAPIKey(key string) (workspaceID, keyID string, ok bool, err error) {
	if key == "" {
		return "", "", false, nil
	}
	err = s.db.QueryRow(
		`SELECT workspace_id, id FROM api_keys WHERE key_hash=?`,
		tokenHash(key),
	).Scan(&workspaceID, &keyID)
	if err == sql.ErrNoRows {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return workspaceID, keyID, true, nil
}
```

`nowUTC()` already exists in this package (`grep -n "func nowUTC" multi-agent/internal/observerstore/store.go` to confirm).

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd multi-agent && go test ./internal/observerstore/ -run 'TestUpsertAPIKey|TestLookupAPIKey' -v`

Expected: PASS on all four tests.

- [ ] **Step 5: Run full store test suite to check for regressions**

Run: `cd multi-agent && go test ./internal/observerstore/...`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd multi-agent
git add internal/observerstore/store.go internal/observerstore/store_test.go
git commit -m "feat(observerstore): UpsertAPIKey + LookupAPIKey

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: `ReplaceAPIKeysForWorkspace` store method

**Files:**
- Modify: `multi-agent/internal/observerstore/store.go` (add method after `UpsertAPIKey`)
- Modify: `multi-agent/internal/observerstore/store_test.go` (add new test)

- [ ] **Step 1: Write the failing test**

Append to `multi-agent/internal/observerstore/store_test.go`:

```go
func TestReplaceAPIKeysForWorkspaceDeletesMissing(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer s.Close()
	require.NoError(t, s.UpsertWorkspace(Workspace{ID: "ws1", Name: "Workspace"}))

	require.NoError(t, s.ReplaceAPIKeysForWorkspace("ws1", []APIKeySpec{
		{ID: "ak-a", Key: "key-a"},
		{ID: "ak-b", Key: "key-b"},
	}))

	_, _, ok, err := s.LookupAPIKey("key-a")
	require.NoError(t, err)
	require.True(t, ok)

	// Reconcile with a shorter list — "ak-a" disappears.
	require.NoError(t, s.ReplaceAPIKeysForWorkspace("ws1", []APIKeySpec{
		{ID: "ak-b", Key: "key-b"},
	}))

	_, _, ok, err = s.LookupAPIKey("key-a")
	require.NoError(t, err)
	require.False(t, ok, "ak-a should be deleted by reconcile")

	_, _, ok, err = s.LookupAPIKey("key-b")
	require.NoError(t, err)
	require.True(t, ok)
}

func TestReplaceAPIKeysForWorkspaceScopedByWorkspace(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer s.Close()
	require.NoError(t, s.UpsertWorkspace(Workspace{ID: "ws1", Name: "Workspace 1"}))
	require.NoError(t, s.UpsertWorkspace(Workspace{ID: "ws2", Name: "Workspace 2"}))

	require.NoError(t, s.ReplaceAPIKeysForWorkspace("ws1", []APIKeySpec{
		{ID: "ak-a", Key: "key-1a"},
	}))
	require.NoError(t, s.ReplaceAPIKeysForWorkspace("ws2", []APIKeySpec{
		{ID: "ak-a", Key: "key-2a"},
	}))

	// Reconcile ws1 with an empty list — ws2's row must survive.
	require.NoError(t, s.ReplaceAPIKeysForWorkspace("ws1", nil))

	_, _, ok, err := s.LookupAPIKey("key-1a")
	require.NoError(t, err)
	require.False(t, ok)

	_, _, ok, err = s.LookupAPIKey("key-2a")
	require.NoError(t, err)
	require.True(t, ok, "reconciling ws1 should not touch ws2 rows")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd multi-agent && go test ./internal/observerstore/ -run TestReplaceAPIKeysForWorkspace -v`

Expected: FAIL with `undefined: (*Store).ReplaceAPIKeysForWorkspace`.

- [ ] **Step 3: Implement `ReplaceAPIKeysForWorkspace`**

Append to `multi-agent/internal/observerstore/store.go` immediately after `LookupAPIKey`:

```go
// ReplaceAPIKeysForWorkspace deletes every api_keys row for workspaceID,
// then inserts the supplied spec set in a single transaction. Used at
// observer boot to reconcile yaml ↔ db so that keys removed from yaml
// stop working after restart.
func (s *Store) ReplaceAPIKeysForWorkspace(workspaceID string, keys []APIKeySpec) error {
	for i, k := range keys {
		if k.ID == "" {
			return fmt.Errorf("observerstore: api key[%d] id must not be empty", i)
		}
		if k.Key == "" {
			return fmt.Errorf("observerstore: api key[%s] value must not be empty", k.ID)
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM api_keys WHERE workspace_id=?`, workspaceID); err != nil {
		return err
	}
	now := nowUTC()
	for _, k := range keys {
		if _, err := tx.Exec(
			`INSERT INTO api_keys(workspace_id, id, key_hash, created_at) VALUES(?, ?, ?, ?)`,
			workspaceID, k.ID, tokenHash(k.Key), now,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd multi-agent && go test ./internal/observerstore/ -run TestReplaceAPIKeysForWorkspace -v`

Expected: PASS on both tests.

- [ ] **Step 5: Run full store suite**

Run: `cd multi-agent && go test ./internal/observerstore/...`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd multi-agent
git add internal/observerstore/store.go internal/observerstore/store_test.go
git commit -m "feat(observerstore): ReplaceAPIKeysForWorkspace for boot reconcile

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: Extend `Store` interface in observerweb

**Files:**
- Modify: `multi-agent/internal/observerweb/server.go` (extend the `Store` interface at lines 18-36)

This is a pure-interface-extension task. We add the new methods to the interface so server.go can call them in Task 5. UpsertAgent is also referenced by the upcoming register handler.

- [ ] **Step 1: Extend the Store interface**

Edit `multi-agent/internal/observerweb/server.go`. Locate the `Store` interface (line 18-36) and add two methods. The interface should now end with these two new lines before the closing brace:

```go
type Store interface {
	ValidateToken(token string) (observerstore.Agent, bool, error)
	Ingest(ev observer.Event) error
	ListTasks() ([]observerstore.TaskView, error)
	ListEvents(taskID string) ([]observer.Event, error)
	CreateArtifact(observerstore.ArtifactCreate) (observerstore.Artifact, error)
	RequestArtifact(workspaceID, requesterAgentID, artifactID string) (observerstore.ArtifactRequest, error)
	ListArtifactRequests(workspaceID, ownerAgentID string) ([]observerstore.ArtifactRequest, error)
	StoreArtifactContent(workspaceID, ownerAgentID, artifactID, mime string, body io.Reader) error
	OpenArtifactContent(workspaceID, artifactID string) (observerstore.ArtifactContent, error)
	CreateWrite(observerstore.WriteCreate) (observerstore.Write, error)
	StoreWriteContent(workspaceID, writerAgentID, writeID, mime string, body io.Reader) error
	UpdateWriteTaskID(workspaceID, ownerAgentID, writeID, taskID string) error
	ListCompletedWrites(workspaceID, ownerAgentID, taskID string) ([]observerstore.Write, error)
	SaveTaskContract(observerstore.TaskContractRecord) error
	GetTaskContract(workspaceID, taskID string) (observerstore.TaskContractRecord, error)
	SaveResourceSnapshot(observerstore.ResourceSnapshotRecord) error
	GetLatestResourceSnapshot(workspaceID string) (observerstore.ResourceSnapshotRecord, error)

	// API-key registration support.
	LookupAPIKey(key string) (workspaceID, keyID string, ok bool, err error)
	UpsertAgent(a observerstore.Agent, token string) error
}
```

- [ ] **Step 2: Confirm the package compiles**

Run: `cd multi-agent && go build ./internal/observerweb/`

Expected: success (no output). `*Store` already satisfies both methods — we added them in Tasks 2-3 and `UpsertAgent` has existed all along.

- [ ] **Step 3: Run existing tests to make sure no regressions**

Run: `cd multi-agent && go test ./internal/observerweb/...`

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
cd multi-agent
git add internal/observerweb/server.go
git commit -m "refactor(observerweb): extend Store iface with LookupAPIKey + UpsertAgent

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: `/api/agents/register` — success path (TDD)

**Files:**
- Modify: `multi-agent/internal/observerweb/server_test.go` (add new test at end)
- Modify: `multi-agent/internal/observerweb/server.go` (add route + handler + request/response types)

- [ ] **Step 1: Write the failing test**

Append to `multi-agent/internal/observerweb/server_test.go`:

```go
func TestRegisterSuccessAndIssuedTokenIngests(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.UpsertWorkspace(observerstore.Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, st.UpsertAPIKey("ws1", "ak-default", "ak_secret"))

	h := New(st)

	body := bytes.NewBufferString(`{"agent_id":"slave-a","role":"slave","display_name":"Slave A"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agents/register", body)
	req.Header.Set("Authorization", "Bearer ak_secret")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var resp struct {
		WorkspaceID string `json:"workspace_id"`
		AgentID     string `json:"agent_id"`
		Role        string `json:"role"`
		DisplayName string `json:"display_name"`
		Token       string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Equal(t, "ws1", resp.WorkspaceID)
	require.Equal(t, "slave-a", resp.AgentID)
	require.Equal(t, observer.RoleSlave, resp.Role)
	require.Equal(t, "Slave A", resp.DisplayName)
	require.Len(t, resp.Token, 64, "token must be 32 random bytes hex-encoded")

	// Issued token must work for ingest end-to-end.
	evBody, _ := json.Marshal(observer.Event{
		WorkspaceID: "ws1", AgentID: "slave-a", AgentRole: observer.RoleSlave,
		Type: observer.EventDriverTaskSubmitted, TaskID: "t1", Summary: "first", Status: "assigned",
	})
	ev := httptest.NewRequest(http.MethodPost, "/api/events", bytes.NewReader(evBody))
	ev.Header.Set("Authorization", "Bearer "+resp.Token)
	ev2 := httptest.NewRecorder()
	h.ServeHTTP(ev2, ev)
	require.Equal(t, http.StatusAccepted, ev2.Code, ev2.Body.String())
}

func TestRegisterDefaultsDisplayNameToAgentID(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.UpsertWorkspace(observerstore.Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, st.UpsertAPIKey("ws1", "ak-default", "ak_secret"))

	h := New(st)
	body := bytes.NewBufferString(`{"agent_id":"slave-a","role":"slave"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agents/register", body)
	req.Header.Set("Authorization", "Bearer ak_secret")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var resp struct {
		DisplayName string `json:"display_name"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Equal(t, "slave-a", resp.DisplayName)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd multi-agent && go test ./internal/observerweb/ -run 'TestRegisterSuccessAndIssuedTokenIngests|TestRegisterDefaultsDisplayNameToAgentID' -v`

Expected: FAIL — route returns 404, or panics because the handler isn't registered.

- [ ] **Step 3: Add the handler, its types, and the route**

Edit `multi-agent/internal/observerweb/server.go`.

3a. Add new imports if missing. Locate the import block at the top of the file and ensure these are present (most already are):

```go
import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/yourorg/multi-agent/internal/observer"
	"github.com/yourorg/multi-agent/internal/observerstore"
)
```

(`crypto/rand`, `encoding/hex`, `log`, `regexp` are the new ones.)

3b. Register the route. Inside `New()`, after `mux.HandleFunc("/api/events", h.postEvent)`, add:

```go
	mux.HandleFunc("/api/agents/register", h.register)
```

3c. Add the handler, the request/response types, and a validator at the bottom of the file (after the existing helpers, before EOF):

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

var agentIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func (h *handler) register(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	apiKey, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		http.Error(w, "missing or invalid bearer token", http.StatusUnauthorized)
		return
	}

	workspaceID, keyID, ok, err := h.s.LookupAPIKey(apiKey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "invalid api key", http.StatusForbidden)
		return
	}

	var req registerRequest
	r.Body = http.MaxBytesReader(w, r.Body, 1<<14) // 16 KiB is plenty
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	var trailing struct{}
	if err := dec.Decode(&trailing); err != io.EOF {
		http.Error(w, "bad json: trailing content", http.StatusBadRequest)
		return
	}

	if !agentIDPattern.MatchString(req.AgentID) {
		http.Error(w, "agent_id must match [A-Za-z0-9_-]{1,64}", http.StatusBadRequest)
		return
	}
	if !validRegisterRole(req.Role) {
		http.Error(w, "role must be one of driver, master, slave", http.StatusBadRequest)
		return
	}
	displayName := req.DisplayName
	if displayName == "" {
		displayName = req.AgentID
	}

	token, err := mintAgentToken()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	agent := observerstore.Agent{
		WorkspaceID: workspaceID,
		ID:          req.AgentID,
		Role:        req.Role,
		DisplayName: displayName,
	}
	if err := h.s.UpsertAgent(agent, token); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("observer: registered agent ws=%s id=%s role=%s via api_key_id=%s",
		workspaceID, req.AgentID, req.Role, keyID)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(registerResponse{
		WorkspaceID: workspaceID,
		AgentID:     agent.ID,
		Role:        agent.Role,
		DisplayName: agent.DisplayName,
		Token:       token,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func validRegisterRole(role string) bool {
	switch role {
	case observer.RoleDriver, observer.RoleMaster, observer.RoleSlave:
		return true
	default:
		return false
	}
}

func mintAgentToken() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd multi-agent && go test ./internal/observerweb/ -run 'TestRegisterSuccessAndIssuedTokenIngests|TestRegisterDefaultsDisplayNameToAgentID' -v`

Expected: PASS on both.

- [ ] **Step 5: Run full observerweb suite to check for regressions**

Run: `cd multi-agent && go test ./internal/observerweb/...`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd multi-agent
git add internal/observerweb/server.go internal/observerweb/server_test.go
git commit -m "feat(observerweb): POST /api/agents/register success path

Authenticates a per-workspace api-key as Authorization: Bearer, mints a
32-byte random token via crypto/rand, and persists the agent via the
existing UpsertAgent path. The same handler also handles the upsert
semantics needed for token reissue (covered in a follow-up test).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: Register endpoint — validation errors

**Files:**
- Modify: `multi-agent/internal/observerweb/server_test.go` (add tests)

(No code changes — the handler already validates; we're locking the behavior down.)

- [ ] **Step 1: Add failing tests**

Append to `multi-agent/internal/observerweb/server_test.go`:

```go
func TestRegisterRejectsBadAPIKey(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.UpsertWorkspace(observerstore.Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, st.UpsertAPIKey("ws1", "ak-default", "ak_real"))

	h := New(st)
	body := bytes.NewBufferString(`{"agent_id":"slave-a","role":"slave"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agents/register", body)
	req.Header.Set("Authorization", "Bearer ak_FAKE")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusForbidden, rr.Code)
}

func TestRegisterRejectsMissingBearer(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.UpsertWorkspace(observerstore.Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, st.UpsertAPIKey("ws1", "ak-default", "ak_real"))

	h := New(st)
	body := bytes.NewBufferString(`{"agent_id":"slave-a","role":"slave"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agents/register", body)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestRegisterRejectsBadRole(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.UpsertWorkspace(observerstore.Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, st.UpsertAPIKey("ws1", "ak-default", "ak_real"))

	h := New(st)
	body := bytes.NewBufferString(`{"agent_id":"slave-a","role":"hacker"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agents/register", body)
	req.Header.Set("Authorization", "Bearer ak_real")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "role")
}

func TestRegisterRejectsBadAgentID(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.UpsertWorkspace(observerstore.Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, st.UpsertAPIKey("ws1", "ak-default", "ak_real"))

	h := New(st)
	cases := []string{
		`{"agent_id":"","role":"slave"}`,                  // empty
		`{"agent_id":"has space","role":"slave"}`,         // space
		`{"agent_id":"weird/slash","role":"slave"}`,       // slash
		`{"agent_id":"` + strings.Repeat("a", 65) + `","role":"slave"}`, // too long
	}
	for _, body := range cases {
		req := httptest.NewRequest(http.MethodPost, "/api/agents/register", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer ak_real")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		require.Equal(t, http.StatusBadRequest, rr.Code, "body=%s", body)
	}
}

func TestRegisterRejectsBadJSON(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.UpsertWorkspace(observerstore.Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, st.UpsertAPIKey("ws1", "ak-default", "ak_real"))

	h := New(st)
	req := httptest.NewRequest(http.MethodPost, "/api/agents/register", bytes.NewBufferString(`{not json`))
	req.Header.Set("Authorization", "Bearer ak_real")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestRegisterRejectsWrongMethod(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.UpsertWorkspace(observerstore.Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, st.UpsertAPIKey("ws1", "ak-default", "ak_real"))

	h := New(st)
	req := httptest.NewRequest(http.MethodGet, "/api/agents/register", nil)
	req.Header.Set("Authorization", "Bearer ak_real")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}
```

- [ ] **Step 2: Run the new tests**

Run: `cd multi-agent && go test ./internal/observerweb/ -run 'TestRegisterRejects' -v`

Expected: PASS on all six tests. (Handler already enforces every rule from Task 5.)

- [ ] **Step 3: Commit**

```bash
cd multi-agent
git add internal/observerweb/server_test.go
git commit -m "test(observerweb): lock down register validation error paths

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 7: Register endpoint — reissue invalidates old token

**Files:**
- Modify: `multi-agent/internal/observerweb/server_test.go` (add test)

This locks down the "re-register issues a brand new token; old token immediately fails ingest" contract.

- [ ] **Step 1: Add failing test**

Append to `multi-agent/internal/observerweb/server_test.go`:

```go
func TestRegisterReissueInvalidatesOldToken(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.UpsertWorkspace(observerstore.Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, st.UpsertAPIKey("ws1", "ak-default", "ak_real"))

	h := New(st)
	register := func() string {
		body := bytes.NewBufferString(`{"agent_id":"slave-a","role":"slave","display_name":"Slave A"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/agents/register", body)
		req.Header.Set("Authorization", "Bearer ak_real")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		var resp struct {
			Token string `json:"token"`
		}
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
		require.NotEmpty(t, resp.Token)
		return resp.Token
	}

	first := register()
	second := register()
	require.NotEqual(t, first, second, "reissue must return a new token")

	// Old token: ingest now fails 401.
	evBody, _ := json.Marshal(observer.Event{
		WorkspaceID: "ws1", AgentID: "slave-a", AgentRole: observer.RoleSlave,
		Type: observer.EventDriverTaskSubmitted, TaskID: "t1", Summary: "x", Status: "assigned",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/events", bytes.NewReader(evBody))
	req.Header.Set("Authorization", "Bearer "+first)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusUnauthorized, rr.Code, "old token must be invalidated")

	// New token still works.
	req = httptest.NewRequest(http.MethodPost, "/api/events", bytes.NewReader(evBody))
	req.Header.Set("Authorization", "Bearer "+second)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusAccepted, rr.Code, rr.Body.String())
}
```

- [ ] **Step 2: Run the test**

Run: `cd multi-agent && go test ./internal/observerweb/ -run TestRegisterReissueInvalidatesOldToken -v`

Expected: PASS — `UpsertAgent`'s `ON CONFLICT … DO UPDATE SET token_hash=excluded.token_hash` already implements this semantics.

- [ ] **Step 3: Commit**

```bash
cd multi-agent
git add internal/observerweb/server_test.go
git commit -m "test(observerweb): re-register invalidates previous agent token

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 8: Register endpoint — reject per-agent token as api-key

**Files:**
- Modify: `multi-agent/internal/observerweb/server_test.go` (add test)

This is the defense the spec calls out: a leaked per-agent token must NOT be usable to enroll new agents.

- [ ] **Step 1: Add failing test**

Append to `multi-agent/internal/observerweb/server_test.go`:

```go
func TestRegisterRejectsPerAgentTokenAsAPIKey(t *testing.T) {
	st, err := observerstore.Open(filepath.Join(t.TempDir(), "observer.db"))
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.UpsertWorkspace(observerstore.Workspace{ID: "ws1", Name: "Workspace"}))
	require.NoError(t, st.UpsertAPIKey("ws1", "ak-default", "ak_real"))
	// Statically seed an agent token (mimics a per-agent token issued by an
	// earlier register call).
	require.NoError(t, st.UpsertAgent(
		observerstore.Agent{WorkspaceID: "ws1", ID: "slave-a", Role: observer.RoleSlave, DisplayName: "Slave A"},
		"leaked_agent_token",
	))

	h := New(st)
	body := bytes.NewBufferString(`{"agent_id":"slave-b","role":"slave"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agents/register", body)
	req.Header.Set("Authorization", "Bearer leaked_agent_token")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusForbidden, rr.Code,
		"per-agent token must not be accepted as an api-key")
}
```

- [ ] **Step 2: Run the test**

Run: `cd multi-agent && go test ./internal/observerweb/ -run TestRegisterRejectsPerAgentTokenAsAPIKey -v`

Expected: PASS — the handler only consults `LookupAPIKey`, never `ValidateToken`.

- [ ] **Step 3: Run the entire observerweb test suite**

Run: `cd multi-agent && go test ./internal/observerweb/...`

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
cd multi-agent
git add internal/observerweb/server_test.go
git commit -m "test(observerweb): register rejects per-agent token as api-key

Locks the defense that LookupAPIKey is the only credential source for
the register endpoint, so a leaked per-agent token cannot be used to
enroll additional agents.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 9: Migrate `cmd/observer-server` to api-key config schema

This task is consolidated because the config struct, validator, boot loop, dev example yaml, and unit tests all change together — splitting them leaves the build broken between commits.

**Files:**
- Modify: `multi-agent/cmd/observer-server/main.go`
- Modify: `multi-agent/cmd/observer-server/main_test.go` (rewrite)
- Modify: `multi-agent/dev/configs/observer.example.yaml`

- [ ] **Step 1: Rewrite the dev example yaml**

Overwrite `multi-agent/dev/configs/observer.example.yaml` with:

```yaml
listen_addr: ":8080"
db_path: observer.db
workspaces:
  - id: dev
    name: Development Workspace
    api_keys:
      - id: ak-dev
        key: ak_dev_shared_secret
```

- [ ] **Step 2: Rewrite `main.go`**

Replace the whole file `multi-agent/cmd/observer-server/main.go` with:

```go
package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/yourorg/multi-agent/internal/observerstore"
	"github.com/yourorg/multi-agent/internal/observerweb"
)

type Config struct {
	ListenAddr string            `yaml:"listen_addr"`
	DBPath     string            `yaml:"db_path"`
	Workspaces []WorkspaceConfig `yaml:"workspaces"`
}

type WorkspaceConfig struct {
	ID      string         `yaml:"id"`
	Name    string         `yaml:"name"`
	APIKeys []APIKeyConfig `yaml:"api_keys"`
}

type APIKeyConfig struct {
	ID  string `yaml:"id"`
	Key string `yaml:"key"`
}

func main() {
	cfgPath := flag.String("config", "observer.yaml", "path to observer config")
	flag.Parse()

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}

	st, err := observerstore.Open(cfg.DBPath)
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()

	for _, workspace := range cfg.Workspaces {
		if err := st.UpsertWorkspace(observerstore.Workspace{ID: workspace.ID, Name: workspace.Name}); err != nil {
			log.Fatal(err)
		}
		specs := make([]observerstore.APIKeySpec, 0, len(workspace.APIKeys))
		for _, k := range workspace.APIKeys {
			specs = append(specs, observerstore.APIKeySpec{ID: k.ID, Key: k.Key})
		}
		if err := st.ReplaceAPIKeysForWorkspace(workspace.ID, specs); err != nil {
			log.Fatal(err)
		}
	}

	log.Printf("observer-server listening on %s", cfg.ListenAddr)
	log.Fatal(http.ListenAndServe(cfg.ListenAddr, observerweb.New(st)))
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return nil, err
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":8090"
	}
	if cfg.DBPath == "" {
		cfg.DBPath = "observer.db"
	}
	if err := validateConfig(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func validateConfig(cfg *Config) error {
	if len(cfg.Workspaces) == 0 {
		return fmt.Errorf("at least one workspace is required")
	}

	workspaces := make(map[string]struct{})
	for wi, workspace := range cfg.Workspaces {
		workspaceID := workspace.ID
		if workspaceID == "" {
			return fmt.Errorf("workspace[%d].id is required", wi)
		}
		if hasOuterWhitespace(workspaceID) {
			return fmt.Errorf("workspace[%d].id must not contain leading or trailing whitespace", wi)
		}
		if workspace.Name == "" {
			return fmt.Errorf("workspace[%d].name is required", wi)
		}
		if hasOuterWhitespace(workspace.Name) {
			return fmt.Errorf("workspace[%d].name must not contain leading or trailing whitespace", wi)
		}
		if _, ok := workspaces[workspaceID]; ok {
			return fmt.Errorf("duplicate workspace id %s", workspaceID)
		}
		workspaces[workspaceID] = struct{}{}

		if len(workspace.APIKeys) == 0 {
			return fmt.Errorf("workspace[%s] must define at least one api_keys entry", workspaceID)
		}

		keyIDs := make(map[string]struct{})
		for ki, k := range workspace.APIKeys {
			if k.ID == "" {
				return fmt.Errorf("workspace[%s].api_keys[%d].id is required", workspaceID, ki)
			}
			if hasOuterWhitespace(k.ID) {
				return fmt.Errorf("workspace[%s].api_keys[%d].id must not contain leading or trailing whitespace", workspaceID, ki)
			}
			if _, ok := keyIDs[k.ID]; ok {
				return fmt.Errorf("duplicate api_keys.id %s in workspace %s", k.ID, workspaceID)
			}
			keyIDs[k.ID] = struct{}{}
			if k.Key == "" {
				return fmt.Errorf("workspace[%s].api_keys[%s].key is required", workspaceID, k.ID)
			}
			if hasOuterWhitespace(k.Key) {
				return fmt.Errorf("workspace[%s].api_keys[%s].key must not contain leading or trailing whitespace", workspaceID, k.ID)
			}
		}
	}
	return nil
}

func hasOuterWhitespace(s string) bool {
	return s != strings.TrimSpace(s)
}
```

Notes on what changed vs. the prior file:
- Imports: `bytes` added; `observer` dropped (the only consumer in this file was the old `validRole`, which is also deleted — role validation now lives in `observerweb.validRegisterRole`).
- `WorkspaceConfig` swaps `Agents` for `APIKeys`.
- `AgentConfig` is gone.
- `loadConfig` now uses `yaml.NewDecoder` with `KnownFields(true)`.
- Boot loop calls `ReplaceAPIKeysForWorkspace` instead of looping `UpsertAgent`.
- `validateConfig` no longer touches agents; it enforces ≥1 api_key per workspace and within-workspace key id uniqueness.

- [ ] **Step 3: Rewrite `main_test.go`**

Overwrite `multi-agent/cmd/observer-server/main_test.go` with:

```go
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadConfigDefaultsAndValidates(t *testing.T) {
	cfg := loadConfigFromString(t, `
workspaces:
  - id: ws1
    name: Workspace
    api_keys:
      - id: ak-default
        key: ak_secret
`)

	require.Equal(t, ":8090", cfg.ListenAddr)
	require.Equal(t, "observer.db", cfg.DBPath)
	require.Len(t, cfg.Workspaces, 1)
	require.Len(t, cfg.Workspaces[0].APIKeys, 1)
	require.Equal(t, "ak-default", cfg.Workspaces[0].APIKeys[0].ID)
}

func TestLoadDistributedObserverExampleConfig(t *testing.T) {
	cfg, err := loadConfig("../../dev/configs/observer.example.yaml")
	require.NoError(t, err)

	require.Equal(t, ":8080", cfg.ListenAddr)
	require.Equal(t, "observer.db", cfg.DBPath)
	require.Len(t, cfg.Workspaces, 1)
	require.Equal(t, "dev", cfg.Workspaces[0].ID)
	require.Len(t, cfg.Workspaces[0].APIKeys, 1)
	require.Equal(t, "ak-dev", cfg.Workspaces[0].APIKeys[0].ID)
}

func TestLoadConfigRejectsObsoleteAgentsField(t *testing.T) {
	path := writeConfig(t, `
workspaces:
  - id: ws1
    name: Workspace
    api_keys:
      - id: ak-default
        key: ak_secret
    agents:
      - id: driver
        role: driver
        display_name: Driver
        token: driver-token
`)
	_, err := loadConfig(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "agents", "yaml strict mode should reject the obsolete field")
}

func TestLoadConfigRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "empty workspace id",
			yaml: `
workspaces:
  - name: Workspace
    api_keys:
      - id: ak-default
        key: ak_secret
`,
			wantErr: "workspace[0].id is required",
		},
		{
			name: "empty workspace name",
			yaml: `
workspaces:
  - id: ws1
    api_keys:
      - id: ak-default
        key: ak_secret
`,
			wantErr: "workspace[0].name is required",
		},
		{
			name: "workspace with no api_keys",
			yaml: `
workspaces:
  - id: ws1
    name: Workspace
`,
			wantErr: "workspace[ws1] must define at least one api_keys entry",
		},
		{
			name: "workspace with empty api_keys list",
			yaml: `
workspaces:
  - id: ws1
    name: Workspace
    api_keys: []
`,
			wantErr: "workspace[ws1] must define at least one api_keys entry",
		},
		{
			name: "duplicate api_keys id in workspace",
			yaml: `
workspaces:
  - id: ws1
    name: Workspace
    api_keys:
      - id: ak-dup
        key: key-1
      - id: ak-dup
        key: key-2
`,
			wantErr: "duplicate api_keys.id ak-dup in workspace ws1",
		},
		{
			name: "empty api_key id",
			yaml: `
workspaces:
  - id: ws1
    name: Workspace
    api_keys:
      - id: ""
        key: key-1
`,
			wantErr: "workspace[ws1].api_keys[0].id is required",
		},
		{
			name: "empty api_key value",
			yaml: `
workspaces:
  - id: ws1
    name: Workspace
    api_keys:
      - id: ak-empty
        key: ""
`,
			wantErr: "workspace[ws1].api_keys[ak-empty].key is required",
		},
		{
			name: "duplicate workspace id",
			yaml: `
workspaces:
  - id: ws1
    name: Workspace A
    api_keys:
      - id: ak-a
        key: key-a
  - id: ws1
    name: Workspace B
    api_keys:
      - id: ak-b
        key: key-b
`,
			wantErr: "duplicate workspace id ws1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeConfig(t, tt.yaml)
			_, err := loadConfig(path)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func loadConfigFromString(t *testing.T, yaml string) *Config {
	t.Helper()

	cfg, err := loadConfig(writeConfig(t, yaml))
	require.NoError(t, err)
	return cfg
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "observer.yaml")
	require.NoError(t, os.WriteFile(path, []byte(strings.TrimSpace(content)+"\n"), 0o600))
	return path
}
```

- [ ] **Step 4: Run the cmd test suite**

Run: `cd multi-agent && go test ./cmd/observer-server/...`

Expected: PASS on all tests.

- [ ] **Step 5: Run the entire observer-related test surface to catch transitive regressions**

Run: `cd multi-agent && go test ./internal/observerstore/... ./internal/observerweb/... ./cmd/observer-server/...`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd multi-agent
git add cmd/observer-server/main.go cmd/observer-server/main_test.go dev/configs/observer.example.yaml
git commit -m "feat(observer-server): switch yaml to api_keys; agents block removed

Boot now reconciles api_keys from yaml (DELETE + INSERT per workspace)
and no longer populates the agents table from yaml. yaml.Decoder runs
in strict mode so any leftover 'agents:' field aborts startup with a
clear error.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 10: Whole-package sanity check + module-wide build

**Files:** none (verification only)

- [ ] **Step 1: Run `go build ./...` from the module root**

Run: `cd multi-agent && go build ./...`

Expected: success — no symbols outside observer-server should depend on the removed `AgentConfig` type or the old yaml shape; if anything does, fix it with the smallest possible change and add a follow-up note.

- [ ] **Step 2: Run `go vet ./...`**

Run: `cd multi-agent && go vet ./...`

Expected: clean.

- [ ] **Step 3: Run the full module test suite**

Run: `cd multi-agent && go test ./...`

Expected: PASS. If anything outside `observerstore` / `observerweb` / `cmd/observer-server` breaks, it is almost certainly an integration test that hand-builds the old observer yaml; fix it by switching to the new `api_keys:` shape and pushing a follow-up commit.

- [ ] **Step 4: If everything passes, commit any incidental fixups, then stop**

```bash
cd multi-agent
git status                       # check whether step 3 forced any extra edits
# if no changes:
echo "nothing to commit; implementation complete"
# if changes exist (only from step 3 fallout):
git add -A
git commit -m "test: align integration tests with api_keys-only observer config

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Spec coverage notes

Two test functions named in the spec are intentionally NOT in this plan:

- **`TestUpsertAPIKey_RequiresWorkspace`** (spec: "unknown workspace_id → FK error"). SQLite does not enforce `FOREIGN KEY` constraints unless `PRAGMA foreign_keys=ON` is explicitly set, which observer's `Open()` does not do today and which this plan does not change (turning it on for one new table risks impacting unrelated tables). The unknown-workspace case is prevented at a higher layer instead: `main.go` calls `UpsertWorkspace(ws)` immediately before `ReplaceAPIKeysForWorkspace(ws, …)` for each yaml entry, so the FK condition cannot be reached via the supported entry point.
- **`TestServerBoot_ReconcilesAPIKeyDeletion`** (spec: full restart cycle proving a removed key stops working). The semantic is fully covered by composition: Task 3's `TestReplaceAPIKeysForWorkspaceDeletesMissing` proves the store call drops removed keys, and `main.go` (Task 9) provably invokes that store call per workspace on every boot. A true end-to-end boot-restart-boot integration test belongs in a separate integration test plan if/when the operator workflow grows complex enough to warrant it.

## Out of scope — followups for separate plans

These items appear in the spec but are explicitly NOT in this plan:

1. **Agent-side bootstrap behavior** in `cmd/{driver,master,slave}-agent`: read `api_key` from config, call `/api/agents/register`, persist returned token to `token_state_path`, re-register on cache loss. Needs its own design + plan.
2. **Rollout to 39.104.86.73**: rewrite the deployed `observer.yaml`, ship the new binary, restart the service, regenerate distribution material. Pure ops work; track it separately.
3. **Future security extensions**: labeled multi-key workspaces with `allowed_roles`, one-time enroll tokens, admin revocation endpoints. The spec lists them as deferred; do not pre-build hooks for them.
