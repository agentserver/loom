# Observer Agentserver Identity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement observer-side agentserver identity alignment through Phase 2 of `docs/superpowers/specs/2026-06-03-observer-agentserver-identity-design.md`, leaving production behavior unchanged unless `identity.agentserver.enabled` is explicitly configured.

**Architecture:** Add a small `internal/identity` boundary with `Identity`, `Resolver`, `Chain`, and cache behavior. Wire observerweb through a resolver while preserving the legacy api_keys/token path as the default. Add the agentserver `/api/agent/whoami` resolver behind configuration and startup probing.

**Tech Stack:** Go 1.26, `net/http`, SQLite via `modernc.org/sqlite`, `golang.org/x/sync/singleflight`, existing observerstore/observerweb/userspace packages.

---

## Merge Review - 2026-06-07

`master` now contains this identity work through merge commit `eb0575f`
(`Merge branch 'observer-agentserver-identity'`). Treat this plan as the
historical implementation checklist and treat current `master` as the identity
baseline for later observer work.

Integration decisions after merging into the PostgreSQL/K8s/object-store
worktree:

- Production default remains legacy observer `api_keys`: `identity.legacy_api_keys.enabled` defaults to `true`, and `identity.agentserver.enabled` defaults to `false`.
- Do not enable agentserver identity in production unless `identity.agentserver.enabled: true` and `identity.agentserver.url` are explicitly configured.
- When legacy API keys are disabled, observer self-registration is disabled and `/api/agents/register` returns 404.
- The PostgreSQL/K8s/object-store branch must keep the external identity audit columns in both SQLite and PostgreSQL schemas.
- `/api/events` must require both a resolved agent identity and `X-Loom-Telemetry-Key`; agentserver identity alone is not authorization to submit telemetry.
- Driver, master, and slave clients may use `credentials.proxy_token` as the observer bearer credential, but task telemetry remains opt-in through `observer.telemetry_enabled: true` plus `observer.telemetry_api_key`.

Post-merge verification target:

```bash
cd multi-agent
go test ./internal/identity ./internal/identity/static ./internal/identity/agentserver ./internal/observerstore ./internal/observerstore/postgres ./internal/observerweb ./internal/userspace ./cmd/observer-server ./internal/config ./internal/driver ./internal/observerclient ./tests/scripts -count=1
go test ./... -count=1
```

### Task 1: Add Additive Schema Columns

**Files:**
- Modify: `internal/observerstore/schema.sql`
- Modify: `internal/userspace/schema.sql`
- Modify: `internal/observerstore/store.go`
- Modify: `internal/observerstore/store_test.go`
- Modify: `internal/userspace/store.go`
- Modify: `internal/userspace/store_test.go`

- [ ] **Step 1: Write failing observerstore schema/storage tests**

Add tests proving new databases and existing rows expose default external audit fields, and that agentserver-sourced upserts can populate them.

Run: `go test ./internal/observerstore -run 'TestSchemaIncludesExternalIdentityColumns|TestUpsertAgentRecordsExternalIdentity' -count=1`

Expected: FAIL because columns and APIs do not exist.

- [ ] **Step 2: Implement observerstore schema and API changes**

Add `external_user_id` to `workspaces`, add `external_sandbox_id` and `external_user_id` to `agents`, extend `Agent` with `ExternalSandboxID` and `ExternalUserID`, and add optional methods:

```go
func (s *Store) UpsertWorkspaceLazyWithExternalUser(id, name, apiKeyID, externalUserID string) error
func (s *Store) UpsertAgentWithExternalIdentity(a Agent, token, apiKeyID string) error
```

Keep existing `UpsertWorkspaceLazy` and `UpsertAgent` as wrappers that pass empty external fields.

- [ ] **Step 3: Verify observerstore tests pass**

Run: `go test ./internal/observerstore -count=1`

Expected: PASS.

- [ ] **Step 4: Write failing userspace visibility schema tests**

Add tests proving `userspace_package_versions.visibility` defaults to `workspace`, `created_by_user_id` defaults to empty, and `VersionRow` round-trips both fields.

Run: `go test ./internal/userspace -run 'TestSchemaIncludesVisibilityColumns|TestVersionVisibilityRoundTrip' -count=1`

Expected: FAIL because columns and row fields do not exist.

- [ ] **Step 5: Implement userspace schema/store changes**

Add `visibility TEXT NOT NULL DEFAULT 'workspace'` and `created_by_user_id TEXT NOT NULL DEFAULT ''`. Extend `VersionRow` with:

```go
Visibility      string
CreatedByUserID string
```

Default empty visibility to `workspace` in `InsertVersion`, insert/select the new columns in all version queries.

- [ ] **Step 6: Verify userspace tests pass**

Run: `go test ./internal/userspace -count=1`

Expected: PASS.

- [ ] **Step 7: Commit schema phase**

Run:

```bash
git add internal/observerstore internal/userspace
git commit -m "feat(observer): add identity audit schema"
```

Expected: commit succeeds.

### Task 2: Implement `internal/identity` Core

**Files:**
- Create: `internal/identity/identity.go`
- Create: `internal/identity/chain.go`
- Create: `internal/identity/cache.go`
- Create: `internal/identity/chain_test.go`
- Create: `internal/identity/cache_test.go`
- Modify: `go.mod`
- Modify: `go.sum`

- [ ] **Step 1: Write failing Chain tests**

Cover invalid fallback, fatal error bubbling, and empty chain panic.

Run: `go test ./internal/identity -run TestChain -count=1`

Expected: FAIL because package does not exist.

- [ ] **Step 2: Implement Identity, Resolver, errors, and Chain**

Define the design doc API exactly:

```go
type Identity struct {
    UserID        string
    WorkspaceID   string
    WorkspaceName string
    AgentID       string
    SandboxID     string
    Role          string
    Source        string
}

type Resolver interface {
    Resolve(ctx context.Context, token string) (Identity, error)
}
```

Add `ErrInvalid`, `ErrRevoked`, `ErrUpstream`, `NewChain(resolvers ...Resolver) Resolver`, and chain behavior where only `ErrInvalid` advances.

- [ ] **Step 3: Verify Chain tests pass**

Run: `go test ./internal/identity -run TestChain -count=1`

Expected: PASS.

- [ ] **Step 4: Write failing cache tests**

Cover fresh hit, stale fail-open on `ErrUpstream`, no stale fallback for invalid/revoked, expired beyond grace eviction, and singleflight dedup for same token.

Run: `go test ./internal/identity -run 'TestCache' -count=1`

Expected: FAIL because cache does not exist.

- [ ] **Step 5: Implement cache resolver**

Add `CacheConfig`, `NewCache(delegate Resolver, cfg CacheConfig) Resolver`, SHA-256 token keys, no plaintext token storage, capacity LRU eviction, fresh TTL jitter ±20%, stale grace fail-open on `ErrUpstream`, and `singleflight.Group` dedup.

- [ ] **Step 6: Verify identity tests pass**

Run: `go test ./internal/identity -count=1`

Expected: PASS.

- [ ] **Step 7: Commit identity core**

Run:

```bash
git add go.mod go.sum internal/identity
git commit -m "feat(identity): add resolver chain and cache"
```

Expected: commit succeeds.

### Task 3: Add Legacy Static Resolver

**Files:**
- Create: `internal/identity/static/resolver.go`
- Create: `internal/identity/static/resolver_test.go`

- [ ] **Step 1: Write failing static resolver tests**

Cover valid observer token mapping to local identity and unknown token returning `identity.ErrInvalid`.

Run: `go test ./internal/identity/static -count=1`

Expected: FAIL because package does not exist.

- [ ] **Step 2: Implement static resolver**

Wrap the existing `ValidateToken(token) (observerstore.Agent, bool, error)` shape and map valid agents to:

```go
identity.Identity{
    WorkspaceID: agent.WorkspaceID,
    AgentID: agent.ID,
    Role: agent.Role,
    Source: "local",
}
```

Return `identity.ErrInvalid` for unknown tokens and bubble store errors.

- [ ] **Step 3: Verify static resolver tests pass**

Run: `go test ./internal/identity/static -count=1`

Expected: PASS.

- [ ] **Step 4: Commit static resolver**

Run:

```bash
git add internal/identity/static
git commit -m "feat(identity): add legacy static resolver"
```

Expected: commit succeeds.

### Task 4: Add Agentserver Whoami Resolver

**Files:**
- Create: `internal/identity/agentserver/resolver.go`
- Create: `internal/identity/agentserver/resolver_test.go`

- [ ] **Step 1: Write failing agentserver resolver tests**

Use `httptest.Server` for `/api/agent/whoami`. Cover 200 parse, 401 invalid, 403 revoked, 5xx upstream, network/timeout upstream, malformed JSON upstream, and Authorization header forwarding.

Run: `go test ./internal/identity/agentserver -count=1`

Expected: FAIL because package does not exist.

- [ ] **Step 2: Implement agentserver resolver**

Add a resolver with URL normalization, request timeout, and response mapping:

```go
type Config struct {
    BaseURL string
    Timeout time.Duration
    Client  *http.Client
}
```

`Resolve` sends `GET <base>/api/agent/whoami` with `Authorization: Bearer <token>`, maps `short_id` to `Identity.AgentID`, maps `sandbox_id`, `workspace_id`, `workspace_name`, `user_id`, `role`, and sets `Source: "agentserver"`.

- [ ] **Step 3: Verify agentserver resolver tests pass**

Run: `go test ./internal/identity/agentserver -count=1`

Expected: PASS.

- [ ] **Step 4: Commit agentserver resolver**

Run:

```bash
git add internal/identity/agentserver
git commit -m "feat(identity): add agentserver whoami resolver"
```

Expected: commit succeeds.

### Task 5: Wire Observerweb Through Identity Resolver

**Files:**
- Modify: `internal/observerweb/server.go`
- Modify: `internal/observerweb/server_test.go`

- [ ] **Step 1: Write failing observerweb resolver tests**

Add a fake resolver and tests for agentserver-sourced event ingest, `ErrRevoked` → 403, `ErrUpstream` → 503 plus `Retry-After: 5`, and mixed legacy/static behavior through `New`.

Run: `go test ./internal/observerweb -run 'TestPostEventAgentserverIdentity|TestAuthenticateIdentityErrors|TestLegacyTokensStillWork' -count=1`

Expected: FAIL because observerweb does not accept an identity resolver.

- [ ] **Step 2: Implement observerweb identity wiring**

Add `NewWithResolver(s Store, usHandler *userspace.Handler, resolver identity.Resolver) http.Handler`, keep `New` as a compatibility wrapper using `static.New(s)`, change `handler.authenticate` to return `identity.Identity`, and map identity errors to documented HTTP statuses.

Preserve `AgentFromRequest` return shape `(workspaceID, agentID string, ok bool)` and add a resolver-based variant for userspace wiring.

- [ ] **Step 3: Verify observerweb tests pass**

Run: `go test ./internal/observerweb -count=1`

Expected: PASS.

- [ ] **Step 4: Commit observerweb wiring**

Run:

```bash
git add internal/observerweb
git commit -m "feat(observerweb): authenticate via identity resolver"
```

Expected: commit succeeds.

### Task 6: Wire Userspace Identity Visibility

**Files:**
- Modify: `internal/userspace/api.go`
- Modify: `internal/userspace/store.go`
- Modify: `internal/userspace/api_test.go`
- Modify: `internal/userspace/store_test.go`
- Modify: `cmd/observer-server/main.go`

- [ ] **Step 1: Write failing userspace visibility tests**

Cover workspace default behavior, `visibility=user` visible only to matching `Identity.UserID` across workspaces, and `visibility=public` visible to everyone.

Run: `go test ./internal/userspace -run 'TestVisibility' -count=1`

Expected: FAIL because resolver only exposes workspace and agent ID.

- [ ] **Step 2: Implement userspace identity-aware resolver**

Introduce:

```go
type Identity struct {
    UserID      string
    WorkspaceID string
    AgentID     string
}
type AgentResolver func(r *http.Request) (Identity, bool)
```

Update handlers to pass full identity into store queries while preserving default `workspace` visibility for pushes.

- [ ] **Step 3: Implement store visibility filtering**

Change search/list/version/install lookups to include rows when:

```sql
visibility = 'public'
OR (visibility = 'workspace' AND created_in_workspace = ?)
OR (visibility = 'user' AND created_by_user_id = ? AND created_by_user_id <> '')
```

Keep push default as `visibility='workspace'` and `created_by_user_id=identity.UserID`.

- [ ] **Step 4: Verify userspace tests pass**

Run: `go test ./internal/userspace -count=1`

Expected: PASS.

- [ ] **Step 5: Commit userspace visibility**

Run:

```bash
git add internal/userspace cmd/observer-server/main.go
git commit -m "feat(userspace): add identity visibility filtering"
```

Expected: commit succeeds.

### Task 7: Add Observer Identity Configuration

**Files:**
- Modify: `cmd/observer-server/main.go`
- Modify: `cmd/observer-server/main_test.go`
- Modify: `cmd/observer-server/config.example.yaml`
- Modify: `dev/configs/observer.example.yaml`

- [ ] **Step 1: Write failing config tests**

Cover default legacy api keys enabled, both sources disabled rejected, legacy disabled with agentserver enabled accepted, missing agentserver URL rejected when enabled, and unknown legacy-only behavior preserved.

Run: `go test ./cmd/observer-server -run 'TestLoadConfigIdentity' -count=1`

Expected: FAIL because identity config does not exist.

- [ ] **Step 2: Implement config types and resolver construction**

Add `IdentityConfig`, `AgentserverIdentityConfig`, `LegacyAPIKeysConfig`, defaults, duration parsing, cache settings, resolver chain assembly, and startup probe. Keep default config behavior equivalent to today: legacy enabled and agentserver disabled.

- [ ] **Step 3: Verify cmd tests pass**

Run: `go test ./cmd/observer-server -count=1`

Expected: PASS.

- [ ] **Step 4: Commit config wiring**

Run:

```bash
git add cmd/observer-server dev/configs/observer.example.yaml
git commit -m "feat(observer): configure identity resolvers"
```

Expected: commit succeeds.

### Task 8: Add Observer-Agentserver Script E2E

**Files:**
- Create: `cmd/whoami-stub/main.go`
- Create: `tests/scripts/observer_agentserver_e2e_test.go`

- [ ] **Step 1: Write failing script test**

Create the Go test that starts `whoami-stub`, starts observer-server with `identity.agentserver.enabled: true`, and checks dashboard 404, agentserver token 202 ingest, cross-workspace 404, revoked 403, upstream 503 with `Retry-After: 5`, and legacy token acceptance when both sources are enabled.

Run: `go test ./tests/scripts -run TestObserverAgentserverIdentityE2E -count=1`

Expected: FAIL because stub and config support do not exist.

- [ ] **Step 2: Implement whoami stub**

Add a small binary accepting `--listen` and `--config` JSON mapping tokens to identity rows plus status behavior. It must only serve `/api/agent/whoami`.

- [ ] **Step 3: Verify script test passes**

Run: `go test ./tests/scripts -run TestObserverAgentserverIdentityE2E -count=1`

Expected: PASS.

- [ ] **Step 4: Commit E2E test**

Run:

```bash
git add cmd/whoami-stub tests/scripts
git commit -m "test(observer): cover agentserver identity e2e"
```

Expected: commit succeeds.

### Task 9: Final Verification

**Files:**
- All modified files

- [ ] **Step 1: Run focused package tests**

Run:

```bash
go test ./internal/identity ./internal/identity/static ./internal/identity/agentserver ./internal/observerstore ./internal/observerweb ./internal/userspace ./cmd/observer-server ./tests/scripts -count=1
```

Expected: PASS.

- [ ] **Step 2: Run full Go suite**

Run:

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 3: Inspect git status**

Run:

```bash
git status --short
git log --oneline --decorate -8
```

Expected: only intentional tracked changes are present; commits show each task phase.
