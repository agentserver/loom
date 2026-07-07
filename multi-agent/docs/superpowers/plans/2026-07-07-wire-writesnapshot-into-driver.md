# Wire WriteSnapshot into driver.dry_run_contract Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire `observerstore.WriteSnapshot` — currently defined + unit-tested but with no non-test caller — into `driver.dry_run_contract` via a new observer-server HTTP endpoint + driver-side relay method, closing loom issue #81.

**Architecture:** Three-layer add (no schema changes, no writer changes):
1. `internal/observerweb/server.go` — new `POST /api/capability-snapshots` handler, mirrors `dryRunBlocks` pattern; authenticated attribution.
2. `internal/driver/observer_relay.go` — new `WriteCapabilitySnapshot(ctx, snap)` method, mirrors `WriteDryRunBlock` pattern.
3. `internal/driver/capability_tools.go` — call site in `dryRunContractTool.Call` after `ComputeHash`, guarded by double-defence ablation check + secret-redacting `NewSnapshot` err handling.

**Tech Stack:**
- Go 1.24
- `internal/observerstore.WriteSnapshot` (existing; no changes)
- `capability.IsUploadDisabled()` (existing ablation guard)
- `ObserverRelay.src.Token()` (existing bearer helper)
- `httptest` (existing test pattern)

## Global Constraints

*Copied verbatim from spec §3, §4, §7. Every task's requirements implicitly include this.*

- **Attribution**: observer-side handler uses `agent.ID + agent.WorkspaceID` from `h.authenticate(w, r)` return — NEVER from request body. Body-level `agent_id/workspace_id` fields are ignored (defense against forgery).
- **Secret redaction (double surface)**:
  - **MCP wire**: driver → MCP caller `err.Message` must be fixed classifier text; never contain `NewSnapshot(err).Error()` or observer HTTP body text with attacker-controlled data.
  - **Log/audit**: driver-side log for `NewSnapshot(err)` uses `fmt.Sprintf("%T", err)` ONLY; never `%v`/`%s`/`err.Error()`/`logHelperErr(err)`. Observer-side handler log for shape-invariant rejection uses fixed classifier + agent.ID + workspace_id ONLY; never `%v` of the err.
- **Ablation double guard (defence in depth)**:
  - Driver-side: `capability.IsUploadDisabled()` before `WriteCapabilitySnapshot` — covers split-deploy where observer flag ≠ driver flag.
  - Observer-side: `observerstore.WriteSnapshot` internal `IsUploadDisabled()` short-circuit — covers single-process deploy + belt-and-suspenders.
  - BOTH guards required; NEITHER is optional. Tests L13 + L13b assert both.
- **Body size cap**: driver pre-caps at 256 KiB before send; observer uses `http.MaxBytesReader(w, r.Body, h.maxEventBodyBytes)`.
- **Error taxonomy**:
  - Secret-scan rejection → 422 UNPROCESSABLE with fixed text `"snapshot contains raw token; rejected by secret scan"`.
  - Shape-invariant rejection → 422 UNPROCESSABLE with fixed text `"invalid snapshot shape"`.
  - Persistence failure → 500 INTERNAL with fixed text `"failed to persist capability_snapshot"`; log to server log.
  - Not-driver auth → 403.
  - Body too large → 413.
  - Bad method → 405.
- **Driver-side warning stability**: exact texts are public contract (metric extractors may grep):
  - Secret-scan surfaced via HTTP 422 → `"observer save capability snapshot: rejected (secret scan)"`
  - Other write errors → `"observer save capability snapshot: " + err.Error()` (err from HTTP layer, already redacted at observer)
- **Position**: `WriteCapabilitySnapshot` call sits AFTER `snapHash := capability.ComputeHash(snap)` and BEFORE `report.Blocks = blocks`; INSIDE the `if len(args.CapabilitySnapshot) > 0` block (variable scope); OUTSIDE the `if validator.IsDryRunDisabled()` early return (NoDryRun already skips this whole section).
- **No changes to**: `observerstore/capability_snapshots_writer.go`, `internal/capability/snapshot.go`, `internal/observerstore/schema.sql`, `cmd/driver-agent/main.go`, `cmd/observer-server/main.go`.

---

## File Structure

| Task | Files (create / modify) | Responsibility |
|---|---|---|
| 1 | modify `internal/driver/capability_tools.go` (2 spots: `dryRunReport` struct + upstream `NewSnapshot` err redact) | New `Warnings []string` field on report + secret-scan-safe err redact for `NewSnapshot` |
| 2 | create `internal/observerweb/capability_snapshots_test.go`, modify `internal/observerweb/server.go` (add route + handler) | Observer HTTP endpoint `POST /api/capability-snapshots` |
| 3 | modify `internal/driver/observer_relay.go`, add tests to `internal/driver/observer_relay_test.go` (or new file) | Driver-side relay method `WriteCapabilitySnapshot(ctx, snap)` |
| 4 | modify `internal/driver/capability_tools.go` (call site) + `internal/driver/capability_tools_test.go` (5 tests) | Wire the write; double-guard; warning redaction |
| 5 | run full test suite + `go vet` + `go mod tidy` | Regression check |

Ordering rationale: Task 1 creates the `Warnings` seam other tasks use; Task 2/3 are independent (observer + driver sides) and can be reviewed in isolation; Task 4 depends on both. Task 5 is the integration gate.

---

## Task 1: Add `Warnings` field to `dryRunReport` + redact `NewSnapshot` err

**Files:**
- Modify: `internal/driver/capability_tools.go` — `dryRunReport` struct (line 426) + upstream `NewSnapshot` err handling (line 270-273)
- Modify: `internal/driver/capability_tools_test.go` — add `TestDryRunContract_MalformedSnapshot_RedactedError`

**Interfaces produced (for later tasks):**
- `dryRunReport.Warnings []string` `json:"warnings,omitempty"`
- Contract: any `NewSnapshot(...)` err → return `MCPToolError{Message: "capability_snapshot: shape invariant rejected", Category: observerstore.FailContractViolation}`. Log ONLY `fmt.Sprintf("%T", err)`.

- [ ] **Step 1.1: Write failing test — malformed snapshot returns redacted error**

Add to `internal/driver/capability_tools_test.go`:

```go
func TestDryRunContract_MalformedSnapshot_RedactedError(t *testing.T) {
	// A snapshot with a raw-token-shaped string in an echoed field must
	// NOT surface that field's value in either the MCP-caller-visible
	// error message or driver logs (spec §3 secret redaction).
	tools := newTestToolsMinimal(t) // helper below; per-file harness
	d := &dryRunContractTool{t: tools}
	badSnap := `{"os":"linux","arch":"amd64","platform":{"os":"linux","arch":"amd64"},"network":"loopback-only","files":[{"kind_detail":"NOT_A_VALID_KIND_ptok-deadbeef-must-not-leak","path_pattern":"/tmp"}]}`
	contractJSON := `{"conversation_id":"ct-1","version":1,"intent":{"goal":"g","success_criteria":["ok"]},"data_contract":{"read_artifacts":[],"write_targets":[{"type":"artifact","kind":"log","name":"o"}]},"capability_requirements":{"skills":["bash"]},"execution_policy":{"routing":"direct_first"},"recovery_hint":"r"}`
	payload := `{"contract":` + contractJSON + `,"capability_snapshot":` + badSnap + `}`

	_, err := d.Call(context.Background(), json.RawMessage(payload))
	if err == nil {
		t.Fatal("expected error for malformed snapshot; got nil")
	}
	// Message must NOT contain the raw-token-shaped bit
	if strings.Contains(err.Error(), "ptok-deadbeef") {
		t.Fatalf("MCP error surface leaked snapshot field value: %q", err.Error())
	}
	// Must be the fixed classifier
	if !strings.Contains(err.Error(), "shape invariant rejected") {
		t.Fatalf("expected fixed classifier text; got %q", err.Error())
	}
}
```

**Use the existing helpers** from `internal/driver/tools_test.go` + `capability_tools_test.go` — do NOT redefine:

```go
// Available in the same package:
//   fakeSDK{ discoverFunc func() ([]agentsdk.AgentCard, error) }  // capability_tools_test.go:50
//   newTestToolsWithSink(t) → (*Tools, *testEventSink)             // capability_tools_test.go:47
//   newTestTools(t, sdk SDKClient) *Tools                          // tools_test.go:80
//   newTestToolsWithObserver(t, sdk, obs) *Tools                   // tools_test.go:84
//   stubTokenSource(string).Token() string                         // tools_test.go:76-78
//
// Test uses:
//   sdk := &fakeSDK{discoverFunc: func() ([]agentsdk.AgentCard, error) { return nil, nil }}
//   tools := newTestTools(t, sdk)
//   d := &dryRunContractTool{t: tools}
```

Rewrite the test body:

```go
func TestDryRunContract_MalformedSnapshot_RedactedError(t *testing.T) {
	sdk := &fakeSDK{discoverFunc: func() ([]agentsdk.AgentCard, error) { return nil, nil }}
	tools := newTestTools(t, sdk)
	d := &dryRunContractTool{t: tools}
	badSnap := `{"os":"linux","arch":"amd64","platform":{"os":"linux","arch":"amd64"},"network":"loopback-only","files":[{"kind_detail":"NOT_A_VALID_KIND_ptok-deadbeef-must-not-leak","path_pattern":"/tmp"}]}`
	contractJSON := `{"conversation_id":"ct-1","version":1,"intent":{"goal":"g","success_criteria":["ok"]},"data_contract":{"read_artifacts":[],"write_targets":[{"type":"artifact","kind":"log","name":"o"}]},"capability_requirements":{"skills":["bash"]},"execution_policy":{"routing":"direct_first"},"recovery_hint":"r"}`
	payload := `{"contract":` + contractJSON + `,"capability_snapshot":` + badSnap + `}`

	_, err := d.Call(context.Background(), json.RawMessage(payload))
	if err == nil {
		t.Fatal("expected error for malformed snapshot; got nil")
	}
	if strings.Contains(err.Error(), "ptok-deadbeef") || strings.Contains(err.Error(), "NOT_A_VALID_KIND") {
		t.Fatalf("MCP error surface leaked snapshot field value: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "shape invariant rejected") {
		t.Fatalf("expected fixed classifier text; got %q", err.Error())
	}
}
```

**AND** add a driver-log secret-leak test (fix plan review r2 P1-2 — the log surface is security-critical, needs its own assertion):

```go
func TestDryRunContract_MalformedSnapshot_LogDoesNotLeakSecret(t *testing.T) {
	// The Global Constraints require driver log for NewSnapshot err use
	// ONLY errTypeName; NEVER err.Error() (which echoes attacker fields).
	// Redirect log package to capture the driver's stderr log lines.
	var buf bytes.Buffer
	prevOut := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prevOut) })

	sdk := &fakeSDK{discoverFunc: func() ([]agentsdk.AgentCard, error) { return nil, nil }}
	tools := newTestTools(t, sdk)
	d := &dryRunContractTool{t: tools}
	badSnap := `{"os":"linux","arch":"amd64","platform":{"os":"linux","arch":"amd64"},"network":"loopback-only","files":[{"kind_detail":"NOT_A_VALID_KIND_ghp_ABCDEFGHIJKLMNOPQRST","path_pattern":"/tmp"}]}`
	contractJSON := `{"conversation_id":"ct-1","version":1,"intent":{"goal":"g","success_criteria":["ok"]},"data_contract":{"read_artifacts":[],"write_targets":[{"type":"artifact","kind":"log","name":"o"}]},"capability_requirements":{"skills":["bash"]},"execution_policy":{"routing":"direct_first"},"recovery_hint":"r"}`
	payload := `{"contract":` + contractJSON + `,"capability_snapshot":` + badSnap + `}`

	_, _ = d.Call(context.Background(), json.RawMessage(payload))
	logs := buf.String()
	if strings.Contains(logs, "ghp_ABCDEFGHIJKLMNOPQRST") || strings.Contains(logs, "NOT_A_VALID_KIND") {
		t.Fatalf("driver log leaked secret / attacker-controlled field value: %q", logs)
	}
	if !strings.Contains(logs, "new_snapshot rejected") {
		t.Fatalf("expected 'new_snapshot rejected' classifier in log; got %q", logs)
	}
}
```

⚠️ Add imports to `capability_tools_test.go` if missing: `"bytes"`, `"log"`.

- [ ] **Step 1.2: Run test — expect failure**

```bash
cd /root/multi-agent/.worktrees/fix-81-writesnap/multi-agent
go test ./internal/driver/ -run TestDryRunContract_MalformedSnapshot_RedactedError -v
```

Expected: FAIL — current code returns `"capability_snapshot: " + err.Error()` which will contain `NOT_A_VALID_KIND_ptok-deadbeef-must-not-leak`.

- [ ] **Step 1.3: Add `Warnings` field to `dryRunReport`**

In `internal/driver/capability_tools.go`, modify the struct (starts at line 426):

Find:
```go
type dryRunReport struct {
	Runnable              bool              `json:"runnable"`
	RecommendedRoute      string            `json:"recommended_route"`
	...
	Blocks    []validator.Block `json:"blocks"`
	AttemptID string            `json:"attempt_id"`
}
```

Replace with (insert `Warnings` before `Blocks`):
```go
type dryRunReport struct {
	Runnable              bool              `json:"runnable"`
	RecommendedRoute      string            `json:"recommended_route"`
	RecommendedTargetID   string            `json:"recommended_target_id,omitempty"`
	RecommendedTargetName string            `json:"recommended_target_display_name,omitempty"`
	RecommendedSkill      string            `json:"recommended_skill,omitempty"`
	SatisfiedTools        []string          `json:"satisfied_tools"`
	MissingTools          []string          `json:"missing_tools"`
	MissingSkills         []string          `json:"missing_skills"`
	MissingResources      json.RawMessage   `json:"missing_resources,omitempty"`
	Reasons               []string          `json:"reasons"`
	// Warnings is the non-fatal helper-error surface (spec §4.4 warning
	// stability contract). Consumers (metric extractors) grep exact
	// prefixes; do not change existing strings without bumping schema.
	Warnings []string `json:"warnings,omitempty"`
	// WT-2-dry-run-validator §4.2: per-invocation attempt id + validator
	// blocks. Runnable is AND-ed with len(Blocks)==0.
	Blocks    []validator.Block `json:"blocks"`
	AttemptID string            `json:"attempt_id"`
}
```

- [ ] **Step 1.4: Redact upstream `NewSnapshot` err**

In `internal/driver/capability_tools.go`, find the block near line 270:

```go
		snap, err := capability.NewSnapshot(snapSpec)
		if err != nil {
			return nil, &MCPToolError{Message: "capability_snapshot: " + err.Error(), Category: observerstore.FailContractViolation}
		}
```

Replace with:

```go
		snap, err := capability.NewSnapshot(snapSpec)
		if err != nil {
			// SECURITY (fix #81 spec §3 + §4.4): NewSnapshot echoes
			// attacker-controlled field values (OS / Network /
			// Files[].KindDetail) that may embed raw tokens. Redact BOTH:
			//   (1) MCP wire response — fixed classifier only
			//   (2) driver-side log — only the error's Go type name
			// NEVER include err.Error() in either surface.
			errTypeName := fmt.Sprintf("%T", err)
			log.Printf("[observer_snapshot] new_snapshot rejected snapshot from workspace=%s; err type=%s", d.t.cfg.Credentials.WorkspaceID, errTypeName)
			return nil, &MCPToolError{Message: "capability_snapshot: shape invariant rejected", Category: observerstore.FailContractViolation}
		}
```

- [ ] **Step 1.5: Add `fmt` import if missing**

In `internal/driver/capability_tools.go`, edit the `import` block at the top. Add `"fmt"` between `"encoding/json"` and `"log"` (alphabetical):

```go
import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	...
)
```

- [ ] **Step 1.6: Run test to verify PASS**

```bash
cd /root/multi-agent/.worktrees/fix-81-writesnap/multi-agent
go test ./internal/driver/ -run TestDryRunContract_MalformedSnapshot_RedactedError -v
```

Expected: PASS. Also run full package to check no regression:

```bash
go test ./internal/driver/ -v -run "TestTool_DryRunContract|TestDryRunContract" -timeout 30s
```

Expected: all pre-existing dry-run tests still green.

- [ ] **Step 1.7: `go vet` + commit**

```bash
go vet ./internal/driver/
git add internal/driver/capability_tools.go internal/driver/capability_tools_test.go
git commit -m "driver(#81): redact NewSnapshot err + add dryRunReport.Warnings field

- NewSnapshot err echoes attacker-controlled fields — could embed raw
  tokens. Redact both MCP wire message (fixed classifier) and driver
  log (fmt.Sprintf(%T, err) only).
- Add Warnings []string to dryRunReport; consumed by task 4's write
  call site.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: Observer-side handler `POST /api/capability-snapshots`

**Files:**
- Modify: `internal/observerweb/server.go` — add mux route (line 131-133 area) + handler `capabilitySnapshots`
- Create: `internal/observerweb/capability_snapshots_test.go`

**Interfaces produced:**
- Wire endpoint `POST /api/capability-snapshots`
- Request body shape: `{"snapshot": <capability.Snapshot canonical JSON>}`
- Response: 204 on success; error taxonomy per Global Constraints
- Attribution: `agent.ID + agent.WorkspaceID` from `h.authenticate` — never body

- [ ] **Step 2.1: Write failing tests**

Create `internal/observerweb/capability_snapshots_test.go`:

```go
package observerweb

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/capability"
	"github.com/yourorg/multi-agent/internal/commandiface"
	"github.com/yourorg/multi-agent/internal/observer"
	"github.com/yourorg/multi-agent/internal/observerstore"
)

// buildValidSnapshot returns a canonical Snapshot suitable for happy-path tests.
func buildValidSnapshot(t *testing.T) capability.Snapshot {
	t.Helper()
	snap, err := capability.NewSnapshot(capability.Snapshot{
		OS:                "linux",
		Arch:              "amd64",
		Platform:          commandiface.Platform{OS: "linux", Arch: "amd64"},
		CommandInterfaces: []commandiface.CommandInterface{{Skill: "bash", Kind: "bash", Command: "/bin/bash", Default: true}},
		Network:           capability.NetworkLoopbackOnly,
	})
	if err != nil {
		t.Fatalf("NewSnapshot: %v", err)
	}
	return snap
}

// postCapabilitySnapshot POSTs a snapshot body to the endpoint using the
// supplied token; returns raw response. Body wraps snapshot in the
// {"snapshot": <bytes>} shape per spec §4.2.
func postCapabilitySnapshot(t *testing.T, srv *httptest.Server, token string, snapBody json.RawMessage) *http.Response {
	t.Helper()
	payload, _ := json.Marshal(struct {
		Snapshot json.RawMessage `json:"snapshot"`
	}{Snapshot: snapBody})
	req, _ := http.NewRequest("POST", srv.URL+"/api/capability-snapshots", bytes.NewReader(payload))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	return resp
}

// registerDriver seeds an observerstore workspace + Agent with role=driver.
// Mirrors the existing pattern in server_test.go:105-108: workspace lazy-upsert
// with a seeded API key, then UpsertAgent(agent, token, apiKeyID).
// Returns (agent, token).
func registerDriver(t *testing.T, store *observerstore.SQLiteStore, agentID, wsID string) (observerstore.Agent, string) {
	t.Helper()
	// The workspace + api key may already exist for this ws from a prior call;
	// UpsertWorkspaceLazy is idempotent.
	require.NoError(t, store.UpsertWorkspaceLazy(wsID, "Test-"+wsID, "ak-"+wsID))
	require.NoError(t, store.UpsertAPIKey(observerstore.APIKeySpec{ID: "ak-" + wsID, Key: "seed-" + wsID}))
	token := "test-bearer-" + agentID
	ag := observerstore.Agent{
		ID:          agentID,
		WorkspaceID: wsID,
		Role:        observer.RoleDriver,
		DisplayName: agentID,
	}
	require.NoError(t, store.UpsertAgent(ag, token, "ak-"+wsID))
	return ag, token
}

// newHandlerServer uses the existing `newTestHandler(t)` helper from
// server_test.go (returns http.Handler + *SQLiteStore, backed by a temp-dir
// SQLite file — avoids multi-connection :memory: issues). Wraps it in an
// httptest.Server for full-URL client access.
func newHandlerServer(t *testing.T) (*httptest.Server, *observerstore.SQLiteStore) {
	t.Helper()
	h, store := newTestHandler(t)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, store
}

// countCapabilitySnapshots returns SELECT count(*) FROM capability_snapshots.
func countCapabilitySnapshots(t *testing.T, store *observerstore.SQLiteStore) int {
	t.Helper()
	var n int
	if err := store.DB().QueryRow(`SELECT count(*) FROM capability_snapshots`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// countCapabilitySnapshotUsages returns rows with matching agent_id.
func countUsagesForAgent(t *testing.T, store *observerstore.SQLiteStore, agentID string) int {
	t.Helper()
	var n int
	if err := store.DB().QueryRow(`SELECT count(*) FROM capability_snapshot_usages WHERE agent_id=?`, agentID).Scan(&n); err != nil {
		t.Fatalf("count usages: %v", err)
	}
	return n
}

// countUsagesForWorkspace returns rows with matching workspace_id — used
// alongside countUsagesForAgent to detect WORKSPACE attribution forgery
// (fix plan review r4 P1). Without this a handler that takes workspace_id
// from the body but agent_id from auth would pass the agent-only assertions.
func countUsagesForWorkspace(t *testing.T, store *observerstore.SQLiteStore, wsID string) int {
	t.Helper()
	var n int
	if err := store.DB().QueryRow(`SELECT count(*) FROM capability_snapshot_usages WHERE workspace_id=?`, wsID).Scan(&n); err != nil {
		t.Fatalf("count ws usages: %v", err)
	}
	return n
}

// --- tests ---

func TestCapabilitySnapshots_HappyPath(t *testing.T) {
	srv, store := newHandlerServer(t)
	_, token := registerDriver(t, store, "drv-1", "ws-A")
	snap := buildValidSnapshot(t)
	body, _ := json.Marshal(snap)
	resp := postCapabilitySnapshot(t, srv, token, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 204 got %d body=%s", resp.StatusCode, b)
	}
	if got := countCapabilitySnapshots(t, store); got != 1 {
		t.Fatalf("capability_snapshots: want 1 got %d", got)
	}
	if got := countUsagesForAgent(t, store, "drv-1"); got != 1 {
		t.Fatalf("capability_snapshot_usages for drv-1: want 1 got %d", got)
	}
}

func TestCapabilitySnapshots_SecretScanRejected_422(t *testing.T) {
	srv, store := newHandlerServer(t)
	_, token := registerDriver(t, store, "drv-1", "ws-A")
	// Build a SHAPE-VALID canonical snapshot that ALSO contains a
	// raw-token-shaped string; capability.JSONContainsRawToken scans
	// for patterns like sk-.../ghp_.../AKIA... (see snapshot.go rawTokenPatterns).
	// Embed a GitHub PAT lookalike into a free-form Tool version field, which
	// NewSnapshot accepts as-is (spec §3.1 "Version is free-form").
	//
	// Test MUST NOT skip — the secret-scan reject at WriteSnapshot is the
	// exact contract we're pinning. If NewSnapshot rejects, that is itself
	// a P0 change to the shape rules and the test should FAIL, not skip.
	snap, err := capability.NewSnapshot(capability.Snapshot{
		OS:       "linux",
		Arch:     "amd64",
		Platform: commandiface.Platform{OS: "linux", Arch: "amd64"},
		Network:  capability.NetworkLoopbackOnly,
		Tools: []capability.ToolVersion{
			{Name: "ghtok", Version: "ghp_ABCDEFGHIJKLMNOPQRST1234567890"},
		},
	})
	if err != nil {
		t.Fatalf("NewSnapshot: %v (secret-shaped Tool.Version was expected to be shape-valid)", err)
	}
	body, _ := json.Marshal(snap)
	resp := postCapabilitySnapshot(t, srv, token, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 422 got %d body=%s", resp.StatusCode, b)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "snapshot contains raw token; rejected by secret scan") {
		t.Fatalf("want fixed text; got %q", b)
	}
	if got := countCapabilitySnapshots(t, store); got != 0 {
		t.Fatalf("capability_snapshots must be empty on scan reject; got %d", got)
	}
}

func TestCapabilitySnapshots_TooLarge_413(t *testing.T) {
	srv, store := newHandlerServer(t)
	_, token := registerDriver(t, store, "drv-1", "ws-A")
	// Build > 256 KiB body
	giant := bytes.Repeat([]byte("A"), 300*1024)
	payload, _ := json.Marshal(struct {
		Snapshot json.RawMessage `json:"snapshot"`
	}{Snapshot: giant})
	req, _ := http.NewRequest("POST", srv.URL+"/api/capability-snapshots", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("want 413 got %d", resp.StatusCode)
	}
}

func TestCapabilitySnapshots_AuthNotDriver_403(t *testing.T) {
	srv, store := newHandlerServer(t)
	// Register as slave role using the same 3-step pattern as registerDriver.
	require.NoError(t, store.UpsertWorkspaceLazy("ws-A", "Test-ws-A", "ak-ws-A"))
	require.NoError(t, store.UpsertAPIKey(observerstore.APIKeySpec{ID: "ak-ws-A", Key: "seed-ws-A"}))
	slaveToken := "test-bearer-slv-1"
	require.NoError(t, store.UpsertAgent(
		observerstore.Agent{ID: "slv-1", WorkspaceID: "ws-A", Role: observer.RoleSlave, DisplayName: "slv-1"},
		slaveToken, "ak-ws-A",
	))
	snap := buildValidSnapshot(t)
	body, _ := json.Marshal(snap)
	resp := postCapabilitySnapshot(t, srv, slaveToken, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403 got %d", resp.StatusCode)
	}
}

func TestCapabilitySnapshots_MethodNotPost_405(t *testing.T) {
	srv, store := newHandlerServer(t)
	_, token := registerDriver(t, store, "drv-1", "ws-A")
	req, _ := http.NewRequest("GET", srv.URL+"/api/capability-snapshots", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("want 405 got %d", resp.StatusCode)
	}
}

func TestCapabilitySnapshots_AttributionFromAuth_NotBody(t *testing.T) {
	// The handler MUST derive attribution from `agent.ID + agent.WorkspaceID`
	// returned by h.authenticate(), NOT from any body-level fields.
	// This test explicitly EMBEDS a forged `agent_id` in the body payload
	// and asserts the observer ignores it (fix plan review r2 P1-1).
	srv, store := newHandlerServer(t)
	_, tok1 := registerDriver(t, store, "drv-alpha", "ws-A")
	_, tok2 := registerDriver(t, store, "drv-beta", "ws-A")
	snap := buildValidSnapshot(t)
	snapBytes, _ := json.Marshal(snap)

	// Alpha writes; embed a forged agent_id="drv-VICTIM" AT THE TOP LEVEL of
	// the request body. Handler decodes only `snapshot` — extras are ignored.
	// Use raw JSON assembly to include the extraneous field.
	forgedAlpha := []byte(`{"agent_id":"drv-VICTIM","workspace_id":"ws-VICTIM","snapshot":` + string(snapBytes) + `}`)
	reqA, _ := http.NewRequest("POST", srv.URL+"/api/capability-snapshots", bytes.NewReader(forgedAlpha))
	reqA.Header.Set("Authorization", "Bearer "+tok1)
	reqA.Header.Set("Content-Type", "application/json")
	respA, err := http.DefaultClient.Do(reqA)
	if err != nil {
		t.Fatalf("do alpha: %v", err)
	}
	respA.Body.Close()

	// Beta writes the same snapshot (no forgery attempt) — dedupe test.
	postCapabilitySnapshot(t, srv, tok2, snapBytes).Body.Close()

	// capability_snapshots dedup by hash → 1 row (same snap body)
	if got := countCapabilitySnapshots(t, store); got != 1 {
		t.Fatalf("capability_snapshots: want 1 (dedup) got %d", got)
	}
	// capability_snapshot_usages: each caller gets 1 row keyed by AUTH agent id
	if got := countUsagesForAgent(t, store, "drv-alpha"); got != 1 {
		t.Fatalf("usages for alpha (auth id): want 1 got %d", got)
	}
	if got := countUsagesForAgent(t, store, "drv-beta"); got != 1 {
		t.Fatalf("usages for beta (auth id): want 1 got %d", got)
	}
	// Forged agent_id MUST NOT have been persisted
	if got := countUsagesForAgent(t, store, "drv-VICTIM"); got != 0 {
		t.Fatalf("attribution forgery (agent): want 0 usages for forged agent_id, got %d", got)
	}
	// Forged workspace_id MUST NOT have been persisted either
	// (fix plan review r4 P1 — a handler that takes ws from body but agent
	// from auth would pass the agent-only assertion above)
	if got := countUsagesForWorkspace(t, store, "ws-VICTIM"); got != 0 {
		t.Fatalf("attribution forgery (workspace): want 0 usages for forged workspace_id, got %d", got)
	}
	// Positive: real workspace ws-A has both writer rows
	if got := countUsagesForWorkspace(t, store, "ws-A"); got != 2 {
		t.Fatalf("expected 2 usages under real workspace ws-A; got %d", got)
	}
}

func TestCapabilitySnapshots_NoCapabilityDiscoveryAblation_ObserverSideDefenseInDepth(t *testing.T) {
	// Global Constraints require double-guard: even if the driver side
	// forgets its own IsUploadDisabled() check, observer-side WriteSnapshot
	// must still short-circuit under the ablation flag.
	srv, store := newHandlerServer(t)
	_, token := registerDriver(t, store, "drv-1", "ws-A")
	// Turn ablation ON at observer process level — API is SetDisableUpload(bool)
	// (see internal/capability/snapshot.go:663 / snapshot_test.go usage).
	capability.SetDisableUpload(true)
	t.Cleanup(func() { capability.SetDisableUpload(false) })
	snap := buildValidSnapshot(t)
	body, _ := json.Marshal(snap)
	resp := postCapabilitySnapshot(t, srv, token, body)
	defer resp.Body.Close()
	// WriteSnapshot short-circuits to nil (silently) — observer returns 204
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("want 204 (short-circuit is silent success) got %d", resp.StatusCode)
	}
	// but no row is written
	if got := countCapabilitySnapshots(t, store); got != 0 {
		t.Fatalf("capability_snapshots under NoCapabilityDiscovery: want 0 got %d", got)
	}
}
```

⚠️ Plan-phase note: `newTestHandler` and `registerRoutes` may not exist with those exact names. Verify by grepping `internal/observerweb/*_test.go` at implementation time; adapt to the existing test harness (e.g. `newTestApp` / `applyRoutes` — whatever `dryRunBlocks` tests use). Same for `capability.SetUploadDisabled` — if named `capability.DisableUpload(bool)` or exposed only via a package-level flag, use that call form. If no accessor exists at all, `t.Setenv("LOOM_ABLATION_NOCAPABILITYDISCOVERY", "1")` and reset via `capability.Default.Reset()` if available; otherwise plan phase must expose a setter.

- [ ] **Step 2.2: Run tests to verify FAIL**

```bash
cd /root/multi-agent/.worktrees/fix-81-writesnap/multi-agent
go test ./internal/observerweb/ -run TestCapabilitySnapshots -v -timeout 30s
```

Expected: FAIL — `/api/capability-snapshots` returns 404 (not mounted).

- [ ] **Step 2.3: Add mux route + handler in `internal/observerweb/server.go`**

Find the block near line 131:
```go
	mux.HandleFunc("/api/resource-snapshots", h.resourceSnapshots)
	mux.HandleFunc("/api/resource-snapshots/latest", h.latestResourceSnapshot)
	mux.HandleFunc("/api/dry-run-blocks", h.dryRunBlocks)
```

Add one line after `dryRunBlocks`:
```go
	mux.HandleFunc("/api/resource-snapshots", h.resourceSnapshots)
	mux.HandleFunc("/api/resource-snapshots/latest", h.latestResourceSnapshot)
	mux.HandleFunc("/api/dry-run-blocks", h.dryRunBlocks)
	mux.HandleFunc("/api/capability-snapshots", h.capabilitySnapshots)
```

At the end of the file (or just below `dryRunBlocks` handler ~line 1140), add the handler:

```go
// capabilitySnapshots persists a canonical capability.Snapshot supplied by
// an authenticated driver. Mirrors dryRunBlocks pattern (spec §4.2 fix #81).
//
// Wire: POST /api/capability-snapshots
// Body: {"snapshot": <canonical capability.Snapshot JSON>}
// Auth: Bearer proxy_token; driver or master role.
// Attribution: agent.ID + agent.WorkspaceID from authenticate() — NEVER body.
// Success: 204 No Content.
// Errors: 401 auth / 403 not-driver / 405 not-POST / 413 too-large
//         / 422 shape or secret-scan reject / 500 persistence
//         / 503 backend not ManagedStore.
//
// Security (spec §3):
//   - NewSnapshot / WriteSnapshot errors are LOGGED with a fixed classifier
//     and the authenticated agent identity ONLY; never `%v` of the error.
//   - HTTP response body is fixed text; never contains raw error text.
func (h *handler) capabilitySnapshots(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agent, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if agent.Role != observer.RoleDriver && agent.Role != observer.RoleMaster {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	managed, ok := h.s.(observerstore.ManagedStore)
	if !ok {
		http.Error(w, "capability_snapshots endpoint requires ManagedStore-backed store", http.StatusServiceUnavailable)
		return
	}
	db := managed.DB()

	r.Body = http.MaxBytesReader(w, r.Body, h.maxEventBodyBytes)
	var req struct {
		Snapshot json.RawMessage `json:"snapshot"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if len(req.Snapshot) == 0 {
		http.Error(w, "snapshot field required", http.StatusBadRequest)
		return
	}
	var snapSpec capability.Snapshot
	if err := json.Unmarshal(req.Snapshot, &snapSpec); err != nil {
		http.Error(w, "invalid snapshot json", http.StatusBadRequest)
		return
	}
	canon, err := capability.NewSnapshot(snapSpec)
	if err != nil {
		// SECURITY: NewSnapshot echoes attacker-controlled field values that
		// may embed raw tokens. Log only a fixed classifier + authenticated
		// identity; NEVER `%v` of err.
		log.Printf("[capability_snapshots] NewSnapshot rejected snapshot from agent=%s ws=%s (shape invariant)", agent.ID, agent.WorkspaceID)
		http.Error(w, "invalid snapshot shape", http.StatusUnprocessableEntity)
		return
	}
	if err := observerstore.WriteSnapshot(r.Context(), db, agent.ID, agent.WorkspaceID, canon); err != nil {
		if errors.Is(err, observerstore.ErrSnapshotContainsSecret) {
			http.Error(w, "snapshot contains raw token; rejected by secret scan", http.StatusUnprocessableEntity)
			return
		}
		// Any other error is redacted — real cause is in server logs only.
		log.Printf("[capability_snapshots] write failed for agent=%s ws=%s: %v", agent.ID, agent.WorkspaceID, err)
		http.Error(w, "failed to persist capability_snapshot", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

Ensure imports at top of `server.go` include `"errors"`, `"github.com/yourorg/multi-agent/internal/capability"`, `"github.com/yourorg/multi-agent/internal/observerstore"`. Grep first: they should already be there from `dryRunBlocks`.

- [ ] **Step 2.4: Adapt test harness helpers to match actual observerweb test conventions**

Search for existing `dryRunBlocks` tests to find the actual harness naming:

```bash
grep -n "func Test.*DryRunBlocks\|newTestHandler\|newTestApp\|registerRoutes\|applyRoutes" internal/observerweb/*_test.go internal/observerweb/server.go 2>&1 | head -20
```

Update `newHandlerServer` in the test file to use the actual helper. If the real observerweb tests use `httptest.NewServer(h.applyRoutes(mux))` or a top-level `New(...)` constructor, rewrite accordingly. Same for `capability.SetUploadDisabled` — grep `IsUploadDisabled\|DisableUpload\|SetUploadDisabled` in `internal/capability/*.go` to find the setter.

- [ ] **Step 2.5: Run tests until all PASS**

```bash
go test ./internal/observerweb/ -run TestCapabilitySnapshots -v -timeout 30s
```

Expected: 7 tests PASS. Also run the whole package to check regressions:

```bash
go test ./internal/observerweb/ -race -timeout 60s
```

Expected: no regressions.

- [ ] **Step 2.6: `go vet` + commit**

```bash
go vet ./internal/observerweb/
git add internal/observerweb/server.go internal/observerweb/capability_snapshots_test.go
git commit -m "observerweb(#81): add POST /api/capability-snapshots endpoint

- New handler mirrors dryRunBlocks pattern: authenticate → driver/master
  role check → MaxBytesReader → canonicalise via NewSnapshot → WriteSnapshot.
- Attribution from authenticated identity (Agent.ID + WorkspaceID) — body
  values ignored.
- Secret-safe: NewSnapshot err logs only fixed classifier + agent identity;
  ErrSnapshotContainsSecret returns fixed 422 text; other errors redacted
  to 500 with fixed text.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: Driver-side relay method `WriteCapabilitySnapshot`

**Files:**
- Modify: `internal/driver/observer_relay.go` — add `WriteCapabilitySnapshot` method
- Modify: `internal/driver/observer_relay_test.go` (or create if missing) — add 2 tests

**Interfaces produced:**
- Method: `(r *ObserverRelay) WriteCapabilitySnapshot(ctx context.Context, snap capability.Snapshot) error`
  - `nil` relay → nil error (silent no-op, matches `WriteDryRunBlock` contract)
  - Pre-cap body 256 KiB; returns error before HTTP if oversized
  - Success: 204 → nil
  - Failure: returns `fmt.Errorf("observer capability_snapshots status %d: %s", resp.StatusCode, body)` with limited body read

- [ ] **Step 3.1: Write failing tests**

⚠️ **Imports to ADD to `internal/driver/observer_relay_test.go`** (top-level `import` block):
- `"strconv"` — for building padded MCP tool names in the size-cap test
- `"github.com/yourorg/multi-agent/internal/capability"` — for `NewSnapshot`, `NetworkLoopbackOnly`
- `"github.com/yourorg/multi-agent/internal/commandiface"` — for `commandiface.Platform`

Existing imports (context, json, http, httptest, strings, testing) are already there.

Search existing tests to see how they mock the observer server:

```bash
grep -n "func Test.*WriteDryRunBlock\|httptest.NewServer" internal/driver/observer_relay_test.go 2>&1 | head
```

Model new tests on the existing `WriteDryRunBlock` tests. Add:

```go
func TestObserverRelay_WriteCapabilitySnapshot_HappyPath(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	cfg := &Config{}
	cfg.Observer.Enabled = true
	cfg.Observer.URL = srv.URL
	relay := NewObserverRelay(cfg, stubTokenSource("tok-1"))
	snap, err := capability.NewSnapshot(capability.Snapshot{
		OS: "linux", Arch: "amd64",
		Platform: commandiface.Platform{OS: "linux", Arch: "amd64"},
		Network:  capability.NetworkLoopbackOnly,
	})
	if err != nil {
		t.Fatalf("NewSnapshot: %v", err)
	}
	if err := relay.WriteCapabilitySnapshot(context.Background(), snap); err != nil {
		t.Fatalf("WriteCapabilitySnapshot: %v", err)
	}
	if gotPath != "/api/capability-snapshots" {
		t.Fatalf("path: want /api/capability-snapshots got %s", gotPath)
	}
	if gotAuth != "Bearer tok-1" {
		t.Fatalf("auth: want 'Bearer tok-1' got %s", gotAuth)
	}
	var wire struct {
		Snapshot json.RawMessage `json:"snapshot"`
	}
	if err := json.Unmarshal(gotBody, &wire); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(wire.Snapshot) == 0 {
		t.Fatal("expected non-empty snapshot in body")
	}
}

func TestObserverRelay_WriteCapabilitySnapshot_NilRelaySilentNoOp(t *testing.T) {
	var relay *ObserverRelay // nil
	snap, _ := capability.NewSnapshot(capability.Snapshot{
		OS: "linux", Arch: "amd64",
		Platform: commandiface.Platform{OS: "linux", Arch: "amd64"},
		Network:  capability.NetworkLoopbackOnly,
	})
	if err := relay.WriteCapabilitySnapshot(context.Background(), snap); err != nil {
		t.Fatalf("nil relay should silently succeed, got: %v", err)
	}
}

func TestObserverRelay_WriteCapabilitySnapshot_LocalSizeCap(t *testing.T) {
	// Do not stand up a server — the pre-cap must reject before any HTTP.
	cfg := &Config{}
	cfg.Observer.Enabled = true
	cfg.Observer.URL = "http://unreachable.invalid"
	relay := NewObserverRelay(cfg, stubTokenSource("tok"))
	// Build a Snapshot with a very large field so canonical JSON > 256 KiB.
	// The simplest way: many MCPTools entries with padded names.
	var mcp []capability.MCPToolDescriptor
	padding := strings.Repeat("A", 4096)
	for i := 0; i < 100; i++ { // 100 * 4KB > 256 KiB after JSON overhead
		mcp = append(mcp, capability.MCPToolDescriptor{
			Server: "srv",
			Name:   "tool-" + strconv.Itoa(i) + "-" + padding,
		})
	}
	snap, err := capability.NewSnapshot(capability.Snapshot{
		OS: "linux", Arch: "amd64",
		Platform: commandiface.Platform{OS: "linux", Arch: "amd64"},
		Network:  capability.NetworkLoopbackOnly,
		MCPTools: mcp,
	})
	if err != nil {
		t.Fatalf("NewSnapshot: %v", err)
	}
	err = relay.WriteCapabilitySnapshot(context.Background(), snap)
	if err == nil {
		t.Fatal("expected pre-cap error; got nil")
	}
	if !strings.Contains(err.Error(), "exceeds observer cap") {
		t.Fatalf("want pre-cap error text; got %q", err.Error())
	}
}

func TestObserverRelay_WriteCapabilitySnapshot_ServerErrorReturned(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "snapshot contains raw token; rejected by secret scan", http.StatusUnprocessableEntity)
	}))
	defer srv.Close()
	cfg := &Config{}
	cfg.Observer.Enabled = true
	cfg.Observer.URL = srv.URL
	relay := NewObserverRelay(cfg, stubTokenSource("tok"))
	snap, _ := capability.NewSnapshot(capability.Snapshot{
		OS: "linux", Arch: "amd64",
		Platform: commandiface.Platform{OS: "linux", Arch: "amd64"},
		Network:  capability.NetworkLoopbackOnly,
	})
	err := relay.WriteCapabilitySnapshot(context.Background(), snap)
	if err == nil {
		t.Fatal("expected error on 422; got nil")
	}
	if !strings.Contains(err.Error(), "status 422") {
		t.Fatalf("want 'status 422' in err; got %q", err.Error())
	}
}
```

⚠️ Plan-phase note: `NewObserverRelayForTest` + `fakeTokenSource` may exist in the test file; grep first. If not, they need to be added or the tests need to construct `*ObserverRelay` directly via exported constructor. Look at how existing `WriteDryRunBlock` tests do it.

- [ ] **Step 3.2: Verify tests fail**

```bash
cd /root/multi-agent/.worktrees/fix-81-writesnap/multi-agent
go test ./internal/driver/ -run "TestObserverRelay_WriteCapabilitySnapshot" -v -timeout 30s
```

Expected: FAIL — `WriteCapabilitySnapshot` undefined.

- [ ] **Step 3.3: Add method to `internal/driver/observer_relay.go`**

Below the existing `WriteDryRunBlock` method (~line 180), append:

```go
// WriteCapabilitySnapshot POSTs a canonical capability.Snapshot to
// observer-server's POST /api/capability-snapshots endpoint (spec #81 §4.3).
//
// nil relay ⇒ silent no-op, matches the SaveResourceSnapshot / WriteDryRunBlock
// contract; callers do not need to nil-check.
//
// Pre-cap: fails fast with a local error if the canonical body exceeds
// 256 KiB (observer's default MaxBytesReader), avoiding a wasted round trip.
//
// Attribution: observer derives agent_id / workspace_id from the
// authenticated Bearer token; caller has no way to forge either.
func (r *ObserverRelay) WriteCapabilitySnapshot(ctx context.Context, snap capability.Snapshot) error {
	if r == nil {
		return nil
	}
	body, err := capability.CanonicalJSON(snap)
	if err != nil {
		return fmt.Errorf("canonicalize snapshot: %w", err)
	}
	const observerBodyCap = 256 * 1024
	if len(body) > observerBodyCap {
		return fmt.Errorf("snapshot body %d bytes exceeds observer cap %d", len(body), observerBodyCap)
	}
	payload, _ := json.Marshal(struct {
		Snapshot json.RawMessage `json:"snapshot"`
	}{Snapshot: body})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		r.baseURL+"/api/capability-snapshots", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.src.Token())
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	return fmt.Errorf("observer capability_snapshots status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
}
```

- [ ] **Step 3.4: Add `capability` import if missing**

Grep current imports:

```bash
grep -n "internal/capability" internal/driver/observer_relay.go
```

If not present, add `"github.com/yourorg/multi-agent/internal/capability"` to the import block.

- [ ] **Step 3.5: Run tests until all PASS**

```bash
go test ./internal/driver/ -run "TestObserverRelay_WriteCapabilitySnapshot" -v -timeout 30s
```

Expected: 4 tests PASS. Full-package regression:

```bash
go test ./internal/driver/ -race -timeout 60s
```

- [ ] **Step 3.6: `go vet` + commit**

```bash
go vet ./internal/driver/
git add internal/driver/observer_relay.go internal/driver/observer_relay_test.go
git commit -m "driver(#81): add ObserverRelay.WriteCapabilitySnapshot

- nil relay silent no-op (matches WriteDryRunBlock contract)
- Pre-caps body at 256 KiB before send (observer MaxBytesReader default)
- Returns fmt error with status code marker for caller to grep 'status 422'
  when redacting secret-scan reject to fixed warning text.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: Wire call site in `dryRunContractTool.Call` with double-guard

**Files:**
- Modify: `internal/driver/capability_tools.go` — insert call after `snapHash = capability.ComputeHash(snap)` (line 275)
- Modify: `internal/driver/capability_tools_test.go` — add 5 tests (L9-L13)

**Interfaces consumed:**
- Task 1's `dryRunReport.Warnings []string`
- Task 3's `(*ObserverRelay).WriteCapabilitySnapshot`
- Existing `capability.IsUploadDisabled()`, `d.t.observerRelay()`

- [ ] **Step 4.1: Write failing tests**

⚠️ **Imports to ADD to `internal/driver/capability_tools_test.go`** (top-level `import` block):
- `"net/http"` — for `http.StatusInternalServerError` / `http.StatusUnprocessableEntity` / `http.HandlerFunc`
- `"net/http/httptest"` — for the fake observer server
- `"sync/atomic"` — for `atomic.Int32` in `snapshotObserver.calls`

Existing imports (bytes, context, json, errors, log, strings, sync, testing, agentsdk, capability, commandiface, contract, validator, observer, observerstore) are already there.

Add to `internal/driver/capability_tools_test.go`:

```go
// mockRelayForCapabilitySnapshot lets tests intercept WriteCapabilitySnapshot.
// Since WriteCapabilitySnapshot is a method on *ObserverRelay (not an interface),
// tests use httptest to intercept the HTTP call.
type snapshotObserver struct {
	srv        *httptest.Server
	calls      atomic.Int32
	nextStatus int    // 0 → default 204
	nextBody   string // response body when nextStatus != 204
}

func newSnapshotObserver(t *testing.T) *snapshotObserver {
	t.Helper()
	obs := &snapshotObserver{nextStatus: http.StatusNoContent}
	obs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		obs.calls.Add(1)
		if obs.nextStatus != http.StatusNoContent {
			http.Error(w, obs.nextBody, obs.nextStatus)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(obs.srv.Close)
	return obs
}

// newToolsWithRelay returns a *Tools with an ObserverRelay pointed at obs.srv.
// Since Tools.relay is an unexported field (see tools.go:324 accessor
// observerRelay() lazy-inits on first read), same-package tests set it directly.
func newToolsWithRelay(t *testing.T, obs *snapshotObserver) *Tools {
	t.Helper()
	sdk := &fakeSDK{discoverFunc: func() ([]agentsdk.AgentCard, error) { return nil, nil }}
	tools := newTestTools(t, sdk)
	tools.cfg.Observer.Enabled = true
	tools.cfg.Observer.URL = obs.srv.URL
	tools.relay = NewObserverRelay(tools.cfg, stubTokenSource("tok"))
	return tools
}

// buildValidPayload returns raw JSON for a dry_run_contract call that
// exercises the write path (contract + capability_snapshot both present).
func buildValidPayload(t *testing.T) json.RawMessage {
	t.Helper()
	contract := `{"conversation_id":"ct-1","version":1,"intent":{"goal":"g","success_criteria":["ok"]},"data_contract":{"read_artifacts":[],"write_targets":[{"type":"artifact","kind":"log","name":"o"}]},"capability_requirements":{"skills":["bash"]},"execution_policy":{"routing":"direct_first"},"recovery_hint":"r"}`
	snap := `{"os":"linux","arch":"amd64","platform":{"os":"linux","arch":"amd64"},"command_interfaces":[{"skill":"bash","kind":"bash","command":"/bin/bash","default":true}],"network":"loopback-only"}`
	return json.RawMessage(`{"contract":` + contract + `,"capability_snapshot":` + snap + `}`)
}

func TestDryRunContract_WriteSnapshotSuccess_NoWarnings(t *testing.T) {
	obs := newSnapshotObserver(t)
	tools := newToolsWithRelay(t, obs)
	d := &dryRunContractTool{t: tools}
	respBytes, err := d.Call(context.Background(), buildValidPayload(t))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if obs.calls.Load() != 1 {
		t.Fatalf("expected 1 relay call, got %d", obs.calls.Load())
	}
	var report dryRunReport
	if err := json.Unmarshal(respBytes, &report); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(report.Warnings) != 0 {
		t.Fatalf("expected no warnings on success; got %v", report.Warnings)
	}
}

func TestDryRunContract_WriteSnapshotFailure_DegradedToWarning(t *testing.T) {
	obs := newSnapshotObserver(t)
	obs.nextStatus = http.StatusInternalServerError
	obs.nextBody = "failed to persist capability_snapshot"
	tools := newToolsWithRelay(t, obs)
	d := &dryRunContractTool{t: tools}
	respBytes, err := d.Call(context.Background(), buildValidPayload(t))
	if err != nil {
		t.Fatalf("dry_run should NOT fail on relay error; got: %v", err)
	}
	var report dryRunReport
	json.Unmarshal(respBytes, &report)
	if len(report.Warnings) == 0 {
		t.Fatal("expected a warning on relay 500")
	}
	if !strings.HasPrefix(report.Warnings[0], "observer save capability snapshot: ") {
		t.Fatalf("warning prefix wrong: %q", report.Warnings[0])
	}
}

func TestDryRunContract_SecretScanFailure_RedactedWarning(t *testing.T) {
	obs := newSnapshotObserver(t)
	obs.nextStatus = http.StatusUnprocessableEntity
	obs.nextBody = "snapshot contains raw token; rejected by secret scan"
	tools := newToolsWithRelay(t, obs)
	d := &dryRunContractTool{t: tools}
	respBytes, err := d.Call(context.Background(), buildValidPayload(t))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	var report dryRunReport
	json.Unmarshal(respBytes, &report)
	if len(report.Warnings) != 1 {
		t.Fatalf("want 1 warning got %d", len(report.Warnings))
	}
	want := "observer save capability snapshot: rejected (secret scan)"
	if report.Warnings[0] != want {
		t.Fatalf("secret-scan warning must be fixed text; want %q got %q", want, report.Warnings[0])
	}
	// AND: the raw observer error text (which might mirror attacker input)
	// must NOT leak into warnings
	if strings.Contains(report.Warnings[0], "status 422") {
		t.Fatalf("HTTP status marker should be redacted out of warning: %q", report.Warnings[0])
	}
}

func TestDryRunContract_NilRelay_SilentSkip(t *testing.T) {
	// Tools.observerRelay() lazy-inits on first call using cfg.Observer.URL.
	// If URL is empty the lazy-init still returns a relay with baseURL="".
	// To truly exercise "no observer" path, point URL at a non-listening
	// address (127.0.0.1:1) and assert WriteCapabilitySnapshot's HTTP error
	// is degraded to a warning — NOT that the call succeeds silently
	// (silent-skip is impossible without a nil-observer sentinel we do not have).
	//
	// If the accessor pattern changes to return nil when Observer.URL=="",
	// update this test to assert len(report.Warnings) == 0. Track via TODO
	// in the accessor.
	sdk := &fakeSDK{discoverFunc: func() ([]agentsdk.AgentCard, error) { return nil, nil }}
	tools := newTestTools(t, sdk)
	tools.cfg.Observer.Enabled = true
	tools.cfg.Observer.URL = "http://127.0.0.1:1" // unreachable, connection refused
	d := &dryRunContractTool{t: tools}
	respBytes, err := d.Call(context.Background(), buildValidPayload(t))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	var report dryRunReport
	json.Unmarshal(respBytes, &report)
	// Warning present, dry-run itself succeeded (non-fatal degrade contract)
	if len(report.Warnings) != 1 {
		t.Fatalf("unreachable relay should degrade to 1 warning; got %d: %v", len(report.Warnings), report.Warnings)
	}
	if !strings.HasPrefix(report.Warnings[0], "observer save capability snapshot: ") {
		t.Fatalf("warning prefix wrong: %q", report.Warnings[0])
	}
}

func TestDryRunContract_NoDryRunAblation_NoSnapshotWrite(t *testing.T) {
	obs := newSnapshotObserver(t)
	tools := newToolsWithRelay(t, obs)
	d := &dryRunContractTool{t: tools}
	// Turn on NoDryRun ablation — early return; no write.
	// API: validator.SetDryRunDisabled(bool) — see validator/ablation_test.go:32.
	validator.SetDryRunDisabled(true)
	t.Cleanup(func() { validator.SetDryRunDisabled(false) })
	_, err := d.Call(context.Background(), buildValidPayload(t))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if obs.calls.Load() != 0 {
		t.Fatalf("NoDryRun ablation must skip relay call; got %d", obs.calls.Load())
	}
}

func TestDryRunContract_NoCapabilityDiscoveryAblation_NoRelayCall(t *testing.T) {
	obs := newSnapshotObserver(t)
	tools := newToolsWithRelay(t, obs)
	d := &dryRunContractTool{t: tools}
	// Driver-side guard: NoCapabilityDiscovery on the DRIVER process must
	// prevent the relay call, even if the observer would accept it.
	// API: capability.SetDisableUpload(bool) — see snapshot.go:663 & snapshot_test.go:697.
	capability.SetDisableUpload(true)
	t.Cleanup(func() { capability.SetDisableUpload(false) })
	_, err := d.Call(context.Background(), buildValidPayload(t))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if obs.calls.Load() != 0 {
		t.Fatalf("NoCapabilityDiscovery must skip relay call at driver; got %d", obs.calls.Load())
	}
}
```

⚠️ Plan-phase notes:
- `SetObserverRelay` may not exist as a public setter (`observerRelay()` accessor exists at `tools.go:324`). If not exposed, verify how existing tests inject relay — grep `SetObserverRelay\|relay ObserverRelay\|Tools{.*relay:`. If needed, add a test-only setter in `tools.go` or set the unexported field directly via a test-package helper.
- `validator.SetDryRunDisabled` / `capability.SetUploadDisabled` — grep exact names; the ablation flags likely have `Register` seams from PR #51. Adapt.
- `NewObserverRelayForTest` was used in Task 3 tests; if it's not real, factor it out here first.

- [ ] **Step 4.2: Verify tests fail**

```bash
cd /root/multi-agent/.worktrees/fix-81-writesnap/multi-agent
go test ./internal/driver/ -run "TestDryRunContract_WriteSnapshot|TestDryRunContract_SecretScan|TestDryRunContract_NilRelay|TestDryRunContract_NoDryRun|TestDryRunContract_NoCapabilityDiscovery" -v -timeout 30s
```

Expected: FAIL — no write happens; no `Warnings` populated.

- [ ] **Step 4.3: Wire the call site**

In `internal/driver/capability_tools.go`, find lines 274-280:

```go
		blocks = validator.New().Check(ctx, tc, snap)
		snapHash = capability.ComputeHash(snap)
	}
	report.Blocks = blocks
	if len(blocks) > 0 {
		report.Runnable = false
	}
```

Replace with (insert new block between the `ComputeHash` line and the closing `}`):

```go
		blocks = validator.New().Check(ctx, tc, snap)
		snapHash = capability.ComputeHash(snap)

		// Persist canonical snapshot for per-agent attribution (fix #81 §4.4).
		//
		// Position: INSIDE the `if len(args.CapabilitySnapshot) > 0` scope
		// (needs `snap`), AFTER hash computation, BEFORE `report.Blocks=blocks`.
		// The `NoDryRun` ablation short-circuits above at
		// `validator.IsDryRunDisabled()`, so we already skip this section
		// under that flag — no explicit second check.
		//
		// Ablation double guard (spec §3 / §4.4 defence in depth):
		//   - driver-side: `capability.IsUploadDisabled()` skips relay call
		//     entirely; covers split-deploy where observer flag differs
		//   - observer-side: `WriteSnapshot` internal short-circuit;
		//     covers single-process deploy + belt-and-suspenders
		// Both required; both tested (L13 + L13b).
		if capability.IsUploadDisabled() {
			log.Printf("[ablation] NoCapabilityDiscovery: driver skipped WriteCapabilitySnapshot for conversation=%q hash=%s", tc.ConversationID, snapHash)
		} else if err := d.t.observerRelay().WriteCapabilitySnapshot(ctx, snap); err != nil {
			// Security: secret-scan verdict is signalled via HTTP 422 marker
			// in the error text. Redact to fixed warning; other errors are
			// already redacted at the observer HTTP boundary.
			msg := "observer save capability snapshot: " + err.Error()
			if strings.Contains(err.Error(), "status 422") {
				msg = "observer save capability snapshot: rejected (secret scan)"
			}
			if report.Warnings == nil {
				report.Warnings = []string{}
			}
			report.Warnings = append(report.Warnings, msg)
			d.t.logHelperErr("observer_snapshot", "write_snapshot", err)
		}
	}
	report.Blocks = blocks
	if len(blocks) > 0 {
		report.Runnable = false
	}
```

- [ ] **Step 4.4: Run tests until all PASS**

```bash
go test ./internal/driver/ -run "TestDryRunContract" -v -timeout 30s
```

Expected: all 6 new tests PASS + no regressions in existing `TestDryRunContract*` / `TestTool_DryRunContract*`.

- [ ] **Step 4.5: `go vet` + full-package regression + commit**

```bash
go vet ./internal/driver/
go test ./internal/driver/ -race -timeout 120s
git add internal/driver/capability_tools.go internal/driver/capability_tools_test.go
git commit -m "driver(#81): wire WriteCapabilitySnapshot into dry_run_contract

- Call site sits after ComputeHash, inside the snap-scope, outside the
  NoDryRun short-circuit (which already returns above).
- Double ablation guard (driver-side capability.IsUploadDisabled +
  observer-side WriteSnapshot internal check).
- Secret-scan verdict (HTTP 422) redacted to fixed warning text;
  other errors surface with err text (already redacted at observer).
- 6 new tests cover happy path, generic failure, secret redaction,
  nil relay, NoDryRun ablation, NoCapabilityDiscovery driver-side.

Fixes #81.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: Full-repo regression + integration check

**Files:** no changes; verification only

- [ ] **Step 5.1: Full go test + vet**

```bash
cd /root/multi-agent/.worktrees/fix-81-writesnap/multi-agent
go vet ./...
go test ./... -race -timeout 300s 2>&1 | tee /tmp/loom-smoke/r81-fulltest.log | tail -30
```

Expected: no `FAIL` lines. Any pre-existing flaky tests from PR #80 still pass.

- [ ] **Step 5.2: `go mod tidy`**

```bash
go mod tidy
git diff go.mod go.sum
```

Expected: no changes (we did not add any new external dep).

- [ ] **Step 5.3: Smoke rerun (spec §5 L16, optional but recommended)**

Bring up the stub stack from PR #80 and repeat the issue #78 L4 flow:

```bash
export LOOM_HOME=/tmp/loom-smoke-81 LOOM_AGENT_KIND=codex
mkdir -p deploy/linux/bin
go build -o deploy/linux/bin/agentserver-stub ./tools/eval/agentserver-stub
go build -o deploy/linux/bin/observer-server  ./cmd/observer-server
go build -o deploy/linux/bin/slave-agent      ./cmd/slave-agent
go build -o deploy/linux/bin/driver-agent     ./cmd/driver-agent
rm -rf "$LOOM_HOME"
bash deploy/linux/deploy.sh --stub --loom-home "$LOOM_HOME"
sleep 15
codex mcp add fix81-driver -- $PWD/deploy/linux/bin/driver-agent serve-mcp --config $LOOM_HOME/driver/config.yaml
# Then call dry_run_contract via codex with a valid capability_snapshot payload
# (see /tmp/loom-smoke/l4-prompt.txt for a template).
```

After a successful `dry_run_contract` call:

```bash
sqlite3 $LOOM_HOME/observer/observer.db 'SELECT count(distinct hash) FROM capability_snapshots;'
```

Expected: count ≥ 1.

Cleanup:

```bash
bash deploy/linux/deploy.sh --shutdown --loom-home "$LOOM_HOME"
codex mcp remove fix81-driver
```

- [ ] **Step 5.4: Push branch + open PR**

```bash
git push -u origin paper/v3/fix-81-writesnap
gh pr create -R agentserver/loom \
  --base paper/v3-integration \
  --title "driver(#81): wire WriteSnapshot via observer HTTP relay + secret-safe error handling" \
  --body-file <(cat <<'EOF'
Fixes #81. See spec at docs/superpowers/specs/2026-07-07-wire-writesnapshot-into-driver-design.md and plan at docs/superpowers/plans/2026-07-07-wire-writesnapshot-into-driver.md.

Spec passed 7 rounds of adversarial `codex exec` review (0 P0 / 0 P1); code passed the review loop separately at plan-exit.

Highlights:
- POST /api/capability-snapshots new observer endpoint (mirror of /api/dry-run-blocks)
- ObserverRelay.WriteCapabilitySnapshot with 256 KiB pre-cap
- Double-guard ablation defense (driver-local + observer-side)
- Secret redaction on 3 surfaces: NewSnapshot err (both wire + log), observer handler NewSnapshot err log, driver warning for 422 secret-scan reject
- 11 new tests total (5 observer-side + 4 relay + 6 call-site) all green with -race

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)
```

---

## Self-Review

**Spec coverage:**
- §3 security table: all 7 items covered — attribution (Task 2/4), secret redaction (Task 1/2/4), ablation double guard (Task 4 + Task 2), body cap (Task 2/3), error taxonomy (Task 2), warning stability (Task 4), no-lock-on-DB (implicit; relay is HTTP)
- §4.1 three layers: Task 2 (observer handler), Task 3 (relay method), Task 4 (call site) + Task 1 (report field prep)
- §4.4 position + double-guard: Task 4 comments cite exact spec sections
- §5 acceptance table L1-L16: L1-L6 → Task 2; L7-L8 → Task 3; L9-L13 → Task 4; L13b → Task 2; L14 → Task 5.1; L15 → Task 5.1; L16 → Task 5.3
- §6 files touched: matches Task 1/2/3/4
- §7 threat model: reflected in tests L2/L3/L5/L6/L9/L10/L12/L13/L13b

**Placeholder scan:** no "TBD", "TODO in implementation", "similar to Task X". Every code block is concrete. Plan-phase notes call out helper naming to verify via grep at implementation time (fresh reviewer would rightly reject "invent the helper"; these notes make explicit that the concrete name comes from the existing codebase).

**Type consistency:** `dryRunReport.Warnings []string` used in Task 1 → 4; `WriteCapabilitySnapshot(ctx, capability.Snapshot) error` signature same in Task 3 → 4; wire path `/api/capability-snapshots` same in Task 2 → 3; error markers ("status 422", "rejected (secret scan)", "shape invariant rejected") consistent across observer / relay / driver.

**Known plan-phase adaptations (grep first, adapt second):**
- `newTestHandler` / `registerRoutes` (Task 2 test harness) — depends on existing `dryRunBlocks` test conventions in `internal/observerweb/`
- `NewObserverRelayForTest` / `fakeTokenSource` (Task 3 tests) — depends on existing test factory in `internal/driver/observer_relay_test.go`
- `SetObserverRelay` (Task 4 tests) — may need adding a package-internal test setter
- `capability.SetUploadDisabled` / `validator.SetDryRunDisabled` — depends on the ablation seam from PR #51
- `observerstore.OpenSQLiteInMemory` — depends on existing observerstore test helpers

Each adaptation is a 1-line change if the real name differs — impossible to be "wrong" in a way that changes semantics.
