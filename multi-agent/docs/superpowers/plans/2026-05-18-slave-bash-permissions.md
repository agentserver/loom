# Slave Bash and Claude Permissions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add opt-in slave Bash execution and driver-managed Claude Code permission inspection/patching for remote slaves.

**Architecture:** Bash execution is a normal slave task route keyed by `skill="bash"`, preserving task status and observer behavior. Claude permission management is a control-plane HTTP API on each slave and is reached by the driver through agentserver peer proxy using the slave card `short_id`.

**Tech Stack:** Go, agentserver `agentsdk`, existing driver MCP tool framework, slave `webui` HTTP handler, `encoding/json`, `os/exec`, `gopkg.in/yaml.v3` only where already used.

---

## File Structure

- Create `multi-agent/internal/executor/bash.go`
  - Implements deterministic Bash task execution and JSON result shape.
- Create `multi-agent/internal/executor/bash_test.go`
  - Unit tests for success, non-zero exit, missing script, and workdir creation.
- Create `multi-agent/internal/claudeperm/store.go`
  - Reads, patches, and atomically writes Claude Code `.claude/settings.local.json`.
- Create `multi-agent/internal/claudeperm/store_test.go`
  - Tests missing-file behavior, idempotent patching, preset expansion, sorting, and unknown-field preservation.
- Modify `multi-agent/internal/webui/server.go`
  - Adds `/claude/permissions` GET/PATCH endpoints and a refresh callback hook.
- Modify `multi-agent/internal/webui/server_test.go`
  - Tests auth, GET, PATCH, and refresh callback invocation.
- Modify `multi-agent/internal/capabilitydoc/doc.go`
  - Adds "Claude Code Permissions" to `CAPABILITIES.md`.
- Modify `multi-agent/internal/capabilitydoc/doc_test.go`
  - Tests rendered allow/deny permission entries.
- Modify `multi-agent/cmd/slave-agent/main.go`
  - Registers `bash` route only when advertised; wires permission refresh callback.
- Modify `multi-agent/cmd/slave-agent/config.example.yaml`
  - Documents `bash` as an optional skill.
- Create `multi-agent/internal/driver/slave_tools.go`
  - Adds `run_slave_bash`, `get_slave_claude_permissions`, and `update_slave_claude_permissions`.
- Create `multi-agent/internal/driver/slave_tools_test.go`
  - Tests target resolution, Bash delegation, waiting, peer-proxy GET/PATCH, and errors.
- Modify `multi-agent/internal/driver/tools.go`
  - Registers the three new driver MCP tools.
- Modify `multi-agent/cmd/driver-agent/README.md`
  - Documents the new MCP tools.
- Modify `multi-agent/tests/runtime/README.md`
  - Adds online E2E instructions for permission patching and Bash execution.

---

### Task 1: Add Bash Executor

**Files:**
- Create: `multi-agent/internal/executor/bash_test.go`
- Create: `multi-agent/internal/executor/bash.go`

- [ ] **Step 1: Write the failing tests**

Create `multi-agent/internal/executor/bash_test.go`:

```go
package executor

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

type noopSink struct{}

func (noopSink) Write(eventType, data string) {}
func (noopSink) Close()                       {}

func TestBashExecutorRunsScriptAndReturnsStructuredOutput(t *testing.T) {
	workdir := t.TempDir()
	exec := NewBashExecutor(BashConfig{WorkDir: workdir})
	res, err := exec.Run(context.Background(), Task{
		ID:     "task-1",
		Skill:  "bash",
		Prompt: `{"script":"pwd\nprintf 'hello stdout\\n'\nprintf 'hello stderr\\n' >&2","timeout_sec":5}`,
	}, noopSink{})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	var got BashResult
	if err := json.Unmarshal([]byte(res.Summary), &got); err != nil {
		t.Fatalf("summary is not BashResult JSON: %v\n%s", err, res.Summary)
	}
	if got.ExitCode != 0 {
		t.Fatalf("exit_code = %d, want 0; result=%+v", got.ExitCode, got)
	}
	if got.Stdout != workdir+"\nhello stdout\n" {
		t.Fatalf("stdout = %q", got.Stdout)
	}
	if got.Stderr != "hello stderr\n" {
		t.Fatalf("stderr = %q", got.Stderr)
	}
	if got.WorkDir != workdir {
		t.Fatalf("workdir = %q", got.WorkDir)
	}
}

func TestBashExecutorFailsOnNonZeroExitWithResult(t *testing.T) {
	exec := NewBashExecutor(BashConfig{WorkDir: t.TempDir()})
	res, err := exec.Run(context.Background(), Task{
		ID:     "task-1",
		Skill:  "bash",
		Prompt: `{"script":"echo before; echo bad >&2; exit 7","timeout_sec":5}`,
	}, noopSink{})
	if err == nil {
		t.Fatal("Run succeeded, want non-zero exit error")
	}
	var got BashResult
	if jsonErr := json.Unmarshal([]byte(res.Summary), &got); jsonErr != nil {
		t.Fatalf("summary is not BashResult JSON: %v\n%s", jsonErr, res.Summary)
	}
	if got.ExitCode != 7 || got.Stdout != "before\n" || got.Stderr != "bad\n" {
		t.Fatalf("unexpected result: %+v", got)
	}
}

func TestBashExecutorRejectsMissingScript(t *testing.T) {
	exec := NewBashExecutor(BashConfig{WorkDir: t.TempDir()})
	_, err := exec.Run(context.Background(), Task{Prompt: `{}`}, noopSink{})
	if err == nil {
		t.Fatal("Run succeeded, want missing script error")
	}
}

func TestBashExecutorCreatesWorkDir(t *testing.T) {
	workdir := filepath.Join(t.TempDir(), "nested", "work")
	exec := NewBashExecutor(BashConfig{WorkDir: workdir})
	res, err := exec.Run(context.Background(), Task{Prompt: `{"script":"pwd"}`}, noopSink{})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	var got BashResult
	if err := json.Unmarshal([]byte(res.Summary), &got); err != nil {
		t.Fatal(err)
	}
	if got.Stdout != workdir+"\n" {
		t.Fatalf("stdout = %q, want pwd output", got.Stdout)
	}
}
```

- [ ] **Step 2: Run the red test**

Run:

```bash
docker run --rm --network host \
  -v /root/multi-agent/.worktrees/slave-bash-tools/multi-agent:/workspace/multi-agent \
  -w /workspace/multi-agent \
  multi-agent-e2e-runtime:latest \
  go test ./internal/executor -run 'TestBashExecutor'
```

Expected: FAIL with undefined `NewBashExecutor`, `BashConfig`, and `BashResult`.

- [ ] **Step 3: Implement Bash executor**

Create `multi-agent/internal/executor/bash.go`:

```go
package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
)

type BashConfig struct {
	WorkDir string
}

type BashExecutor struct {
	cfg BashConfig
}

type BashRequest struct {
	Script     string            `json:"script"`
	TimeoutSec int               `json:"timeout_sec,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
}

type BashResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	WorkDir  string `json:"workdir"`
}

func NewBashExecutor(cfg BashConfig) *BashExecutor {
	return &BashExecutor{cfg: cfg}
}

func (e *BashExecutor) Run(ctx context.Context, t Task, sink Sink) (Result, error) {
	defer sink.Close()
	var req BashRequest
	if err := json.Unmarshal([]byte(t.Prompt), &req); err != nil {
		return Result{}, fmt.Errorf("bash prompt must be JSON: %w", err)
	}
	if req.Script == "" {
		return Result{}, fmt.Errorf("bash script is required")
	}
	workdir := e.cfg.WorkDir
	if workdir == "" {
		var err error
		workdir, err = os.Getwd()
		if err != nil {
			return Result{}, err
		}
	}
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		return Result{}, err
	}
	cmd := exec.CommandContext(ctx, "/bin/bash", "-lc", req.Script)
	cmd.Dir = workdir
	cmd.Env = cmd.Environ()
	for k, v := range req.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		exitCode = 1
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		}
	}
	out := BashResult{ExitCode: exitCode, Stdout: stdout.String(), Stderr: stderr.String(), WorkDir: workdir}
	body, marshalErr := json.Marshal(out)
	if marshalErr != nil {
		return Result{}, marshalErr
	}
	sink.Write("chunk", string(body))
	res := Result{Summary: string(body)}
	if err != nil {
		return res, fmt.Errorf("bash exit %d", exitCode)
	}
	return res, nil
}
```

- [ ] **Step 4: Run the green test**

Run the same `go test ./internal/executor -run 'TestBashExecutor'` command.

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add multi-agent/internal/executor/bash.go multi-agent/internal/executor/bash_test.go
git commit -m "feat: add slave bash executor"
```

---

### Task 2: Add Claude Permission Store

**Files:**
- Create: `multi-agent/internal/claudeperm/store_test.go`
- Create: `multi-agent/internal/claudeperm/store.go`

- [ ] **Step 1: Write the failing tests**

Create `multi-agent/internal/claudeperm/store_test.go`:

```go
package claudeperm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreReadMissingReturnsEmpty(t *testing.T) {
	store := NewStore(t.TempDir())
	got, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Allow) != 0 || len(got.Deny) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestStorePatchExpandsPresetsSortsAndIsIdempotent(t *testing.T) {
	store := NewStore(t.TempDir())
	patch := Patch{
		AllowPresets: []string{"file_write", "curl", "python", "pip"},
		AllowAdd:     []string{"Bash(python3 *)"},
		DenyAdd:      []string{"Bash(rm *)"},
	}
	first, err := store.Patch(patch)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Patch(patch)
	if err != nil {
		t.Fatal(err)
	}
	if !equal(first.Allow, second.Allow) || !equal(first.Deny, second.Deny) {
		t.Fatalf("not idempotent: first=%+v second=%+v", first, second)
	}
	wantAllow := []string{
		"Bash(curl *)",
		"Bash(pip *)",
		"Bash(pip3 *)",
		"Bash(python *)",
		"Bash(python -m pip *)",
		"Bash(python3 *)",
		"Bash(python3 -m pip *)",
		"Edit",
		"Read",
		"Write",
	}
	if !equal(first.Allow, wantAllow) {
		t.Fatalf("allow=%q want=%q", first.Allow, wantAllow)
	}
	if !equal(first.Deny, []string{"Bash(rm *)"}) {
		t.Fatalf("deny=%q", first.Deny)
	}
}

func TestStorePreservesUnknownSettingsFields(t *testing.T) {
	workdir := t.TempDir()
	path := filepath.Join(workdir, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"theme":"dark","permissions":{"allow":["Read"],"deny":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewStore(workdir)
	if _, err := store.Patch(Patch{AllowAdd: []string{"Write"}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if string(got["theme"]) != `"dark"` {
		t.Fatalf("theme not preserved: %s", data)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
```

- [ ] **Step 2: Run the red test**

Run:

```bash
docker run --rm --network host \
  -v /root/multi-agent/.worktrees/slave-bash-tools/multi-agent:/workspace/multi-agent \
  -w /workspace/multi-agent \
  multi-agent-e2e-runtime:latest \
  go test ./internal/claudeperm
```

Expected: FAIL because package `internal/claudeperm` has no implementation.

- [ ] **Step 3: Implement permission store**

Create `multi-agent/internal/claudeperm/store.go` with:

- `type Store struct { workdir string }`
- `func NewStore(workdir string) *Store`
- `func (s *Store) Path() string`
- `func (s *Store) Read() (State, error)`
- `func (s *Store) Patch(Patch) (State, error)`
- `func ExpandPresets([]string) ([]string, error)`

Use this exact public API:

```go
type State struct {
	Path  string   `json:"path"`
	Allow []string `json:"allow"`
	Deny  []string `json:"deny"`
}

type Patch struct {
	AllowPresets []string `json:"allow_presets"`
	AllowAdd     []string `json:"allow_add"`
	AllowRemove  []string `json:"allow_remove"`
	DenyAdd       []string `json:"deny_add"`
	DenyRemove    []string `json:"deny_remove"`
}
```

Implementation requirements:

- Settings path is `<workdir>/.claude/settings.local.json`.
- Missing file returns empty `State`.
- Preserve unknown top-level JSON fields by reading into `map[string]json.RawMessage`.
- Use a nested `permissions` object with `allow` and `deny`.
- Deduplicate and sort string lists.
- Atomic write with `settings.local.json.tmp` then `os.Rename`.
- Unknown preset returns an error containing `unknown permission preset`.

- [ ] **Step 4: Run the green test**

Run `go test ./internal/claudeperm`.

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add multi-agent/internal/claudeperm
git commit -m "feat: add claude permission store"
```

---

### Task 3: Add Slave Permission HTTP Endpoints

**Files:**
- Modify: `multi-agent/internal/webui/server.go`
- Modify: `multi-agent/internal/webui/server_test.go`

- [ ] **Step 1: Write failing endpoint tests**

Append to `multi-agent/internal/webui/server_test.go`:

```go
func TestClaudePermissionsGetAndPatch(t *testing.T) {
	workdir := t.TempDir()
	cfg := &config.Config{
		Credentials: config.Credentials{ProxyToken: "tok"},
		Claude:      config.Claude{WorkDir: workdir},
	}
	refreshCalled := false
	h := NewHandler(openStore(t), t.TempDir(), cfg)
	SetCapabilityRefresh(h, func(ctx context.Context, reason string) error {
		refreshCalled = reason == "claude permission update"
		return nil
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	patchBody := []byte(`{"allow_add":["Bash(python3 *)","Bash(curl *)"],"deny_add":["Bash(rm *)"]}`)
	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/claude/permissions", bytes.NewReader(patchBody))
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)
	require.True(t, refreshCalled)

	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/claude/permissions", nil)
	req.Header.Set("Authorization", "Bearer tok")
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)

	var got struct {
		Allow []string `json:"allow"`
		Deny  []string `json:"deny"`
		Path  string   `json:"path"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.ElementsMatch(t, []string{"Bash(python3 *)", "Bash(curl *)"}, got.Allow)
	require.ElementsMatch(t, []string{"Bash(rm *)"}, got.Deny)
	require.Equal(t, filepath.Join(workdir, ".claude", "settings.local.json"), got.Path)
}

func TestClaudePermissionsRejectsBadAuth(t *testing.T) {
	cfg := &config.Config{
		Credentials: config.Credentials{ProxyToken: "tok"},
		Claude:      config.Claude{WorkDir: t.TempDir()},
	}
	h := NewHandler(openStore(t), t.TempDir(), cfg)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/claude/permissions", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusUnauthorized, rr.Code)
}
```

- [ ] **Step 2: Run the red test**

Run:

```bash
docker run --rm --network host \
  -v /root/multi-agent/.worktrees/slave-bash-tools/multi-agent:/workspace/multi-agent \
  -w /workspace/multi-agent \
  multi-agent-e2e-runtime:latest \
  go test ./internal/webui -run 'TestClaudePermissions'
```

Expected: FAIL because `SetCapabilityRefresh` and `/claude/permissions` do not exist.

- [ ] **Step 3: Implement endpoints**

Modify `multi-agent/internal/webui/server.go`:

- Add import `github.com/yourorg/multi-agent/internal/claudeperm`.
- Add field to `muxWithBridge`:

```go
refresh func(context.Context, string) error
```

- Add function:

```go
func SetCapabilityRefresh(h http.Handler, refresh func(context.Context, string) error) {
	if hh, ok := h.(*muxWithBridge); ok {
		hh.refresh = refresh
	}
}
```

- In `NewHandler`, register:

```go
mux.HandleFunc("/claude/permissions", wrap.claudePermissions)
```

- Add method on `muxWithBridge`:

```go
func (m *muxWithBridge) claudePermissions(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+m.cfg.Credentials.ProxyToken {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	workdir := m.cfg.Claude.WorkDir
	if workdir == "" {
		var err error
		workdir, err = os.Getwd()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	store := claudeperm.NewStore(workdir)
	switch r.Method {
	case http.MethodGet:
		state, err := store.Read()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(state)
	case http.MethodPatch:
		var patch claudeperm.Patch
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		state, err := store.Patch(patch)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if m.refresh != nil {
			if err := m.refresh(r.Context(), "claude permission update"); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(state)
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}
```

- [ ] **Step 4: Run the green test**

Run `go test ./internal/webui -run 'TestClaudePermissions'`.

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add multi-agent/internal/webui/server.go multi-agent/internal/webui/server_test.go
git commit -m "feat: expose slave claude permissions"
```

---

### Task 4: Add Permissions to Capability Document

**Files:**
- Modify: `multi-agent/internal/capabilitydoc/doc.go`
- Modify: `multi-agent/internal/capabilitydoc/doc_test.go`

- [ ] **Step 1: Write failing capability document test**

Add a test to `multi-agent/internal/capabilitydoc/doc_test.go` that creates `<workdir>/.claude/settings.local.json` with:

```json
{"permissions":{"allow":["Bash(curl *)","Read"],"deny":["Bash(rm *)"]}}
```

Then call `Store.Refresh` with `Input{WorkDir: workdir}` and assert `CAPABILITIES.md` contains:

```text
## Claude Code Permissions
- allow: Bash(curl *), Read
- deny: Bash(rm *)
```

- [ ] **Step 2: Run the red test**

Run:

```bash
docker run --rm --network host \
  -v /root/multi-agent/.worktrees/slave-bash-tools/multi-agent:/workspace/multi-agent \
  -w /workspace/multi-agent \
  multi-agent-e2e-runtime:latest \
  go test ./internal/capabilitydoc -run 'Claude Code Permissions'
```

Expected: FAIL because the section is missing.

- [ ] **Step 3: Render permission state**

Modify `multi-agent/internal/capabilitydoc/doc.go`:

- Import `github.com/yourorg/multi-agent/internal/claudeperm`.
- Add `ClaudePermissions claudeperm.State` to `snapshot`.
- In `scan`, if `in.WorkDir != ""`, read `claudeperm.NewStore(in.WorkDir).Read()` and assign on success.
- In `render`, after `## Skills`, add:

```go
fmt.Fprintf(&b, "\n## Claude Code Permissions\n\n")
if len(s.ClaudePermissions.Allow) == 0 && len(s.ClaudePermissions.Deny) == 0 {
	fmt.Fprintf(&b, "- none configured\n")
} else {
	writeList(&b, "allow", s.ClaudePermissions.Allow)
	writeList(&b, "deny", s.ClaudePermissions.Deny)
}
```

- [ ] **Step 4: Run the green test**

Run `go test ./internal/capabilitydoc -run 'Claude Code Permissions'`.

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add multi-agent/internal/capabilitydoc/doc.go multi-agent/internal/capabilitydoc/doc_test.go
git commit -m "feat: document claude permissions"
```

---

### Task 5: Wire Slave Bash Route and Permission Refresh

**Files:**
- Modify: `multi-agent/cmd/slave-agent/main.go`
- Modify: `multi-agent/cmd/slave-agent/config.example.yaml`
- Test: targeted unit or integration coverage through existing package tests.

- [ ] **Step 1: Write failing route-registration test**

If a direct unit seam is not available, add a small helper in `cmd/slave-agent/main.go`:

```go
func hasSkill(skills []string, want string) bool {
	for _, skill := range skills {
		if skill == want {
			return true
		}
	}
	return false
}
```

Then add `multi-agent/cmd/slave-agent/main_test.go` with:

```go
package main

import "testing"

func TestHasSkill(t *testing.T) {
	if !hasSkill([]string{"chat", "bash"}, "bash") {
		t.Fatal("expected bash skill")
	}
	if hasSkill([]string{"chat"}, "bash") {
		t.Fatal("did not expect bash skill")
	}
}
```

- [ ] **Step 2: Run the red test**

Run:

```bash
docker run --rm --network host \
  -v /root/multi-agent/.worktrees/slave-bash-tools/multi-agent:/workspace/multi-agent \
  -w /workspace/multi-agent \
  multi-agent-e2e-runtime:latest \
  go test ./cmd/slave-agent -run TestHasSkill
```

Expected before helper exists: FAIL with undefined `hasSkill`.

- [ ] **Step 3: Wire route and refresh callback**

Modify `multi-agent/cmd/slave-agent/main.go`:

- Use `hasSkill(cfg.Discovery.Skills, "build_mcp")` instead of the local loop.
- After `routes := map[string]executor.Executor{...}`, add:

```go
if hasSkill(cfg.Discovery.Skills, "bash") {
	routes["bash"] = executor.NewBashExecutor(executor.BashConfig{WorkDir: cfg.Claude.WorkDir})
}
```

- After `ui := webui.NewHandler(s, journalDir, cfg)`, wire refresh:

```go
webui.SetCapabilityRefresh(ui, func(ctx context.Context, reason string) error {
	refreshCapabilities(ctx, reason)
	return tn.PublishCard(ctx)
})
```

Move this call to a point after `refreshCapabilities` and `tn` are defined if needed; keep `ui` construction before tunnel creation.

Modify `multi-agent/cmd/slave-agent/config.example.yaml` skills comment or example:

```yaml
discovery:
  skills:
    - chat
    - mcp
    # - bash  # opt-in deterministic shell execution for trusted workspaces
```

- [ ] **Step 4: Run green tests**

Run:

```bash
docker run --rm --network host \
  -v /root/multi-agent/.worktrees/slave-bash-tools/multi-agent:/workspace/multi-agent \
  -w /workspace/multi-agent \
  multi-agent-e2e-runtime:latest \
  go test ./cmd/slave-agent -run TestHasSkill
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add multi-agent/cmd/slave-agent/main.go multi-agent/cmd/slave-agent/main_test.go multi-agent/cmd/slave-agent/config.example.yaml
git commit -m "feat: wire slave bash skill"
```

---

### Task 6: Add Driver MCP Tools

**Files:**
- Create: `multi-agent/internal/driver/slave_tools.go`
- Create: `multi-agent/internal/driver/slave_tools_test.go`
- Modify: `multi-agent/internal/driver/tools.go`

- [ ] **Step 1: Write failing driver tool tests**

Create `multi-agent/internal/driver/slave_tools_test.go` with tests for:

- `run_slave_bash` delegates `skill="bash"` and JSON prompt.
- `run_slave_bash` rejects a target without `bash` skill.
- `get_slave_claude_permissions` calls `PeerProxy(GET, short_id, "/claude/permissions")`.
- `update_slave_claude_permissions` calls `PeerProxy(PATCH, short_id, "/claude/permissions")` with the patch body.

Use the existing `fakeSDK`, `newTestTools`, and `toolByName` helpers from `tools_test.go`. The first test body should follow this shape:

```go
func TestRunSlaveBashDelegatesBashSkillAndWaits(t *testing.T) {
	var delegated agentsdk.DelegateTaskRequest
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-a", DisplayName: "slave-a", Status: "available", Card: json.RawMessage(`{"skills":["bash"],"short_id":"sa"}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			delegated = req
			return &agentsdk.DelegateTaskResponse{TaskID: "task-1"}, nil
		},
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{
				TaskID: "task-1",
				Status: "completed",
				Result: json.RawMessage(`"{\"exit_code\":0,\"stdout\":\"ok\\n\",\"stderr\":\"\",\"workdir\":\"/w\"}"`),
			}, nil
		},
	}
	tool := toolByName(t, newTestTools(t, sdk), "run_slave_bash")
	out, err := tool.Call(context.Background(), json.RawMessage(`{"target_display_name":"slave-a","script":"echo ok","timeout_sec":30}`))
	require.NoError(t, err)
	require.Equal(t, "slave-a", delegated.TargetID)
	require.Equal(t, "bash", delegated.Skill)
	require.JSONEq(t, `{"script":"echo ok","timeout_sec":30}`, delegated.Prompt)
	require.Contains(t, string(out), `"task_id":"task-1"`)
	require.Contains(t, string(out), `"stdout":"ok\n"`)
}
```

- [ ] **Step 2: Run the red test**

Run:

```bash
docker run --rm --network host \
  -v /root/multi-agent/.worktrees/slave-bash-tools/multi-agent:/workspace/multi-agent \
  -w /workspace/multi-agent \
  multi-agent-e2e-runtime:latest \
  go test ./internal/driver -run 'TestRunSlaveBash|TestGetSlaveClaudePermissions|TestUpdateSlaveClaudePermissions'
```

Expected: FAIL because tools are not registered.

- [ ] **Step 3: Implement driver tools**

Create `multi-agent/internal/driver/slave_tools.go`:

- Add helper `resolveAvailableAgent(ctx, targetAgentID, targetDisplayName string) (agentsdk.AgentCard, error)`.
- Require either id or display name; skip driver self.
- Require `agentAvailable(card)`.
- For `run_slave_bash`, require `hasSkill(card, "bash")`.
- Marshal prompt as:

```go
map[string]interface{}{
	"script": args.Script,
	"timeout_sec": args.TimeoutSec,
	"env": args.Env,
}
```

- Submit:

```go
agentsdk.DelegateTaskRequest{
	TargetID: card.AgentID,
	Skill: "bash",
	Prompt: string(promptJSON),
	TimeoutSeconds: timeout,
}
```

- Default `wait` to true by making the args field `*bool`; nil means true.
- Reuse `sdkTaskOutput` to parse `TaskInfo`.
- For permissions, call `t.sdk.PeerProxy(ctx, http.MethodGet/Patch, cardShortID(card), "/claude/permissions", body)`, read the response, close body, return non-2xx as `MCPToolError`.

Modify `multi-agent/internal/driver/tools.go` `All()` to include:

```go
&runSlaveBashTool{t},
&getSlaveClaudePermissionsTool{t},
&updateSlaveClaudePermissionsTool{t},
```

Place them after `inspect_capabilities` so Claude Code sees capability and control-plane tools together.

- [ ] **Step 4: Run the green driver tests**

Run the same `go test ./internal/driver -run ...` command.

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add multi-agent/internal/driver/slave_tools.go multi-agent/internal/driver/slave_tools_test.go multi-agent/internal/driver/tools.go
git commit -m "feat: add driver slave bash tools"
```

---

### Task 7: Documentation and Verification

**Files:**
- Modify: `multi-agent/cmd/driver-agent/README.md`
- Modify: `multi-agent/cmd/slave-agent/README.md`
- Modify: `multi-agent/tests/runtime/README.md`

- [ ] **Step 1: Update docs**

Document:

- `bash` is opt-in via `discovery.skills`.
- Driver tools:
  - `run_slave_bash`
  - `get_slave_claude_permissions`
  - `update_slave_claude_permissions`
- Example permission patch:

```json
{
  "target_display_name": "slave-a-online-dag-160628",
  "allow_presets": ["python", "curl", "file_write"]
}
```

- Example Bash execution:

```json
{
  "target_display_name": "slave-a-online-dag-160628",
  "script": "python3 - <<'PY'\nprint('ok')\nPY",
  "timeout_sec": 60
}
```

- [ ] **Step 2: Run focused test suite**

Run:

```bash
docker run --rm --network host \
  -v /root/multi-agent/.worktrees/slave-bash-tools/multi-agent:/workspace/multi-agent \
  -w /workspace/multi-agent \
  multi-agent-e2e-runtime:latest \
  go test ./internal/executor ./internal/claudeperm ./internal/webui ./internal/capabilitydoc ./internal/driver ./cmd/slave-agent
```

Expected: PASS for all listed packages. If `cmd/slave-agent` capability-doc E2E flakes on timing, rerun once and record the exact failure before changing code.

- [ ] **Step 3: Run manual online E2E**

Use the existing persistent runtime directories. Do not recreate containers or credentials.

From `multi-agent/tests/claude_driver`, ask Claude Code driver to:

```text
Use driver tools to:
1. list agents and choose two slaves with bash capability.
2. update each slave's Claude Code permissions with python, curl, and file_write presets.
3. run a Python matrix multiplication script on slave A using numpy through run_slave_bash.
4. run an equivalent Python matrix multiplication script on slave B using for loops through run_slave_bash.
5. compare the result hashes and report whether they match.
```

Expected:

- Both `run_slave_bash` calls complete.
- Both Bash result JSON objects have `exit_code: 0`.
- Hashes match.

- [ ] **Step 4: Final status and commit docs**

```bash
git add multi-agent/cmd/driver-agent/README.md multi-agent/cmd/slave-agent/README.md multi-agent/tests/runtime/README.md
git commit -m "docs: document slave bash and permissions"
```

---

## Self-Review

- Spec coverage: Bash executor, permission API, driver MCP tools, capability document, docs, and online E2E each map to a task.
- Placeholder scan: no `TBD`, `TODO`, or unspecified implementation steps are left in this plan.
- Type consistency: public names are stable across tasks: `BashConfig`, `BashExecutor`, `BashRequest`, `BashResult`, `claudeperm.State`, `claudeperm.Patch`, `run_slave_bash`, `get_slave_claude_permissions`, and `update_slave_claude_permissions`.
