# Slave Security §1.4 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close 5 slave-side RCE/SSRF/data-exfil holes from review §1.4 (#14-#18). FileExecutor jails to WorkDir; HTTP MCP gets timeout + size cap; permissions patch fields whitelisted; driver `source_path` jailed; `/files/put` gets body cap + O_EXCL + parent fsync.

**Architecture:** Each fix is local to one file + new helper. Two new package-level helpers (`assertInJail` in executor, `AssertReadableSource` in driver/safe_paths) carry the EvalSymlinks+Rel pattern. No new dependencies.

**Tech Stack:** Go stdlib only.

**Spec:** `docs/superpowers/specs/2026-06-13-slave-security-1.4-design.md`

**Worktree:** `/root/multi-agent/.worktrees/fix-slave-security-1.4/multi-agent/`, branch `worktree-fix-slave-security-1.4`. Baseline `go test ./internal/executor/... ./cmd/slave-agent/... ./internal/driver/... ./internal/mcpmarket/...` passes.

---

## File structure

- Modify `internal/executor/file.go` — add `workDir` field + `assertInJail` helper + `resolveExistingPrefix`; call assertInJail in doRead/doWrite/doStat
- Test `internal/executor/file_test.go` — 4 new tests
- Modify `internal/executor/mcp.go` — http.Client timeout + LimitReader
- Test `internal/executor/mcp_test.go` — 2 new tests (or new file)
- Modify `cmd/slave-agent/permissions_executor.go` — call new `validatePermissionsPatch`
- Test `cmd/slave-agent/permissions_executor_test.go` — 3 new tests
- Modify `internal/driver/safe_paths.go` — add `AssertReadableSource` + `resolveExistingPrefix` (shared with executor — but they're in different packages so duplicate; OR put in a shared spot)
- Modify `internal/driver/config.go` — `DriverDefaults` gains `SourcePathReadRoots []string`
- Modify `internal/driver/slave_file_tools.go:314-324` — call AssertReadableSource on source_path
- Test `internal/driver/slave_file_tools_test.go` — 3 new tests
- Modify `internal/driver/files_handler.go:222-283` — MaxBytesReader + O_EXCL + parent fsync
- Test `internal/driver/files_handler_test.go` — 2 new tests

---

## Task 1: shared `resolveExistingPrefix` helper

Both #14 and #17 need an "EvalSymlinks-tolerant-of-non-existent-leaf" helper. Living in two packages so we add it in each (small enough — ~12 LOC) rather than introducing a new shared package.

### Files
- Modify `internal/executor/file.go` — add unexported helper near top
- Modify `internal/driver/safe_paths.go` — add unexported helper near top

- [ ] **Step 1: Write helper in `internal/executor/file.go`**

After the imports block (around line 16), add:

```go
// resolveExistingPrefix returns filepath.EvalSymlinks(p) if p exists.
// If p doesn't exist, it evaluates the longest existing prefix and
// rejoins the non-existent tail unchanged. This lets jail checks work
// even when the caller is about to *create* a file at p.
func resolveExistingPrefix(p string) (string, error) {
    abs, err := filepath.Abs(p)
    if err != nil {
        return "", err
    }
    cur := abs
    var tail []string
    for {
        real, err := filepath.EvalSymlinks(cur)
        if err == nil {
            if len(tail) == 0 {
                return real, nil
            }
            // Rebuild: real + tail (reverse the suffix we peeled off).
            parts := append([]string{real}, reverse(tail)...)
            return filepath.Join(parts...), nil
        }
        if !os.IsNotExist(err) {
            return "", err
        }
        // Peel off one segment and retry on parent.
        parent, leaf := filepath.Split(cur)
        parent = filepath.Clean(parent)
        if parent == cur {
            // Hit root and still not-exist. Shouldn't normally happen
            // because root exists; bail with the original absolute.
            return abs, nil
        }
        tail = append(tail, leaf)
        cur = parent
    }
}

func reverse(s []string) []string {
    out := make([]string, len(s))
    for i, v := range s {
        out[len(s)-1-i] = v
    }
    return out
}
```

Verify `path/filepath` and `os` already imported (likely yes).

- [ ] **Step 2: Write helper in `internal/driver/safe_paths.go`**

Same helper, but the function name slightly differs to avoid clashing if a developer later puts both packages in one tree:

```go
// resolveExistingPrefix returns filepath.EvalSymlinks(p) if p exists.
// For not-yet-existing leaf paths, it evaluates the longest existing
// prefix and rejoins the non-existent tail unchanged. Lets jail checks
// reason about would-be-written files.
func resolveExistingPrefix(p string) (string, error) {
    abs, err := filepath.Abs(p)
    if err != nil {
        return "", err
    }
    cur := abs
    var tail []string
    for {
        real, err := filepath.EvalSymlinks(cur)
        if err == nil {
            if len(tail) == 0 {
                return real, nil
            }
            parts := append([]string{real}, reverseStrings(tail)...)
            return filepath.Join(parts...), nil
        }
        if !os.IsNotExist(err) {
            return "", err
        }
        parent, leaf := filepath.Split(cur)
        parent = filepath.Clean(parent)
        if parent == cur {
            return abs, nil
        }
        tail = append(tail, leaf)
        cur = parent
    }
}

func reverseStrings(s []string) []string {
    out := make([]string, len(s))
    for i, v := range s {
        out[len(s)-1-i] = v
    }
    return out
}
```

Verify `path/filepath` and `os` imported (likely yes — file already does `filepath.IsAbs`).

- [ ] **Step 3: Build**

```bash
go build ./...
```
Expected: clean.

- [ ] **Step 4: Commit**

```bash
git add internal/executor/file.go internal/driver/safe_paths.go
git commit -m "feat: resolveExistingPrefix helper (jail for not-yet-existing leaf)

Both upcoming jail enforcement points (executor FileExecutor write
target; driver write_slave_file source_path) need to reason about
paths whose leaf doesn't exist yet. filepath.EvalSymlinks errors on
non-existent. Helper evaluates the longest existing prefix and rejoins
the tail unchanged.

Spec: docs/superpowers/specs/2026-06-13-slave-security-1.4-design.md

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: FileExecutor jail (Bug #14)

**Files:**
- Modify `internal/executor/file.go` — `workDir` field; `assertInJail`; call in doRead/doWrite/doStat
- Test `internal/executor/file_test.go` — 4 tests

### Existing structure
- `FileExecutor` struct has `cfg` field (look at first 30 lines of file.go for shape)
- `resolvePath` at line 83: returns `filepath.Join(e.cfg.WorkDir, p)` for relative, returns `p` for absolute
- `doRead`/`doWrite`/`doStat` at lines 94, 161, 245

### Step 1: Write failing tests

Look at existing `file_test.go` (or any executor test) for the constructor pattern — likely `NewFileExecutor(cfg)`. Use temp dirs.

Append to `internal/executor/file_test.go` (or create if missing):

```go
// TestFileExecutor_RejectsAbsolutePathOutsideJail pins §1.4 #14 invariant:
// LLM cannot read /etc/passwd / /root/.ssh/authorized_keys even with
// an absolute path.
func TestFileExecutor_RejectsAbsolutePathOutsideJail(t *testing.T) {
    workDir := t.TempDir()
    e := NewFileExecutor(FileCfg{WorkDir: workDir})
    // Try to read a fixed absolute path that is NOT under workDir.
    outside := "/etc/passwd"
    req := fileRequest{Op: "read", Path: outside}
    raw, _ := json.Marshal(req)
    _, err := e.Run(context.Background(), executor.Task{Prompt: string(raw)}, executor.NoopSink{})
    require.Error(t, err)
    require.Contains(t, err.Error(), "jail")
}

// TestFileExecutor_RejectsSymlinkLeapingOutOfJail: create a symlink inside
// the jail that points OUT of the jail. Reading through it must fail.
func TestFileExecutor_RejectsSymlinkLeapingOutOfJail(t *testing.T) {
    workDir := t.TempDir()
    outsideDir := t.TempDir()
    outsideFile := filepath.Join(outsideDir, "secret.txt")
    require.NoError(t, os.WriteFile(outsideFile, []byte("secret"), 0o644))
    linkPath := filepath.Join(workDir, "link")
    require.NoError(t, os.Symlink(outsideFile, linkPath))

    e := NewFileExecutor(FileCfg{WorkDir: workDir})
    req := fileRequest{Op: "read", Path: "link"}
    raw, _ := json.Marshal(req)
    _, err := e.Run(context.Background(), executor.Task{Prompt: string(raw)}, executor.NoopSink{})
    require.Error(t, err, "symlink leaping out of jail must be rejected")
    require.Contains(t, err.Error(), "jail")
}

// TestFileExecutor_AcceptsRelativeInsideJail (positive case)
func TestFileExecutor_AcceptsRelativeInsideJail(t *testing.T) {
    workDir := t.TempDir()
    require.NoError(t, os.WriteFile(filepath.Join(workDir, "hello.txt"), []byte("hi"), 0o644))
    e := NewFileExecutor(FileCfg{WorkDir: workDir})
    req := fileRequest{Op: "read", Path: "hello.txt"}
    raw, _ := json.Marshal(req)
    res, err := e.Run(context.Background(), executor.Task{Prompt: string(raw)}, executor.NoopSink{})
    require.NoError(t, err)
    require.Contains(t, res.Summary, "hi")
}

// TestFileExecutor_AcceptsAbsoluteInsideJail (positive case for paths
// that happen to be absolute but live under WorkDir)
func TestFileExecutor_AcceptsAbsoluteInsideJail(t *testing.T) {
    workDir := t.TempDir()
    abs := filepath.Join(workDir, "inside.txt")
    require.NoError(t, os.WriteFile(abs, []byte("hi"), 0o644))
    e := NewFileExecutor(FileCfg{WorkDir: workDir})
    req := fileRequest{Op: "read", Path: abs}
    raw, _ := json.Marshal(req)
    res, err := e.Run(context.Background(), executor.Task{Prompt: string(raw)}, executor.NoopSink{})
    require.NoError(t, err, "absolute path inside jail must be allowed")
    require.Contains(t, res.Summary, "hi")
}
```

Imports needed: `context`, `encoding/json`, `os`, `filepath`, `testing`, `require`, `executor` package itself (the inner one — likely already auto since we're in same package).

If `NewFileExecutor` constructor takes different name, adapt. Check `grep -n "func NewFile" internal/executor/file.go`.

Also `executor.NoopSink{}` — verify it exists in executor package; if not use a local stub `&captureSink{}` that satisfies the Sink interface.

### Step 2: Run tests — expect RED

```bash
go test ./internal/executor/ -run "TestFileExecutor_(Rejects|Accepts)" -v -count=1
```

Expected: 4 FAIL. Specifically: `RejectsAbsolutePathOutsideJail` and `RejectsSymlinkLeapingOutOfJail` should FAIL because current code reads /etc/passwd successfully (or reads through symlink). The Accepts tests should pass even now (positive cases unaffected).

### Step 3: Add workDir + assertInJail

In `internal/executor/file.go`:

1. Find `FileExecutor` struct. Add field:
```go
workDir string
```

2. Find `NewFileExecutor` constructor. Initialize `workDir`:
```go
func NewFileExecutor(cfg FileCfg) *FileExecutor {
    wd := cfg.WorkDir
    if wd == "" {
        if cwd, err := os.Getwd(); err == nil {
            wd = cwd
        }
    }
    if abs, err := filepath.Abs(wd); err == nil {
        wd = abs
    }
    return &FileExecutor{cfg: cfg, workDir: wd}
}
```

If `FileExecutor` struct is constructed inline (not via `NewFileExecutor`), put the initialization inside that path. Read the file to confirm.

3. Add `assertInJail` method anywhere in file.go (e.g. just below `resolvePath`):

```go
// assertInJail rejects paths that resolve (after symlinks) outside e.workDir.
// Lets the LLM-driven file tool stay inside the configured jail even when
// the path is absolute, contains "..", or is a symlink that points out.
// Fixes §1.4 #14 of docs/review-2026-06-13.md.
func (e *FileExecutor) assertInJail(abs string) error {
    real, err := resolveExistingPrefix(abs)
    if err != nil {
        return fmt.Errorf("resolve %s: %w", abs, err)
    }
    rel, err := filepath.Rel(e.workDir, real)
    if err != nil {
        return fmt.Errorf("path %s outside jail %s: %w", abs, e.workDir, err)
    }
    if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
        return fmt.Errorf("path %s escapes jail %s (rel=%s)", abs, e.workDir, rel)
    }
    return nil
}
```

Verify `strings` imported.

4. Call assertInJail BEFORE each op's body. Find the Run/dispatch function that does `case "read"`/`"write"`/`"stat"` (around line 70-80). The block looks like:

```go
abs := e.resolvePath(req.Path)
switch req.Op {
case "read":
    return e.doRead(req, abs, sink)
case "write":
    return e.doWrite(req, abs, sink)
case "stat":
    return e.doStat(req, abs, sink)
default:
    return Result{}, fmt.Errorf("unknown file op %q", req.Op)
}
```

Insert jail check between resolvePath and switch:

```go
abs := e.resolvePath(req.Path)
if err := e.assertInJail(abs); err != nil {
    return Result{}, err
}
switch req.Op { ... }
```

### Step 4: Run tests — expect GREEN

```bash
go test ./internal/executor/ -run "TestFileExecutor_(Rejects|Accepts)" -v -count=1
```

Expected: 4 PASS.

### Step 5: Full executor package + race

```bash
go test ./internal/executor/... -race -count=1
```

Expected: PASS. Existing tests should not regress (they're typically tmpdir-based, well within jail).

### Step 6: Commit

```bash
git add internal/executor/file.go internal/executor/file_test.go
git commit -m "fix(executor): FileExecutor jails file ops to WorkDir (Bug #14)

resolvePath previously accepted absolute paths directly and didn't
follow symlinks. LLM-controlled paths like /etc/passwd or
'./link' → /etc/shadow worked unconditionally.

assertInJail uses resolveExistingPrefix (EvalSymlinks tolerant of
non-existent leaf) then filepath.Rel against the cached absolute
WorkDir; rejects '..' escapes. Called between resolvePath and the
op dispatch so read/write/stat all gate on it.

WorkDir is cached as absolute at NewFileExecutor; default to cwd
if cfg.WorkDir is empty.

Fixes §1.4 #14 of docs/review-2026-06-13.md.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: HTTP MCP timeout + size cap (Bug #15)

**Files:**
- Modify `internal/executor/mcp.go` — http.Client timeout + LimitReader on io.ReadAll
- Test `internal/executor/mcp_test.go` (or new file `mcp_http_test.go`)

### Step 1: Write failing tests

Look at existing mcp_test.go for patterns. The HTTP path is exercised via `MCPServerCfg{Transport: "http", URL: ...}`. Test setup uses `httptest.NewServer`.

Append (or create `mcp_http_test.go`):

```go
// TestMCPExecutor_HTTPTimeoutFiresWithinExpectedWindow pins §1.4 #15
// timeout invariant: a server that never replies must not hang the
// dispatcher forever. Default timeout is 30s; we override via a small
// custom-cfg shape if available, otherwise we live with 30s wall time.
func TestMCPExecutor_HTTPTimeoutFiresWithinExpectedWindow(t *testing.T) {
    // Server that holds the connection open without writing a response.
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // never write; let the client's timeout fire
        <-r.Context().Done()
    }))
    defer srv.Close()

    cfg := map[string]MCPServerCfg{
        "slow": {Transport: "http", URL: srv.URL},
    }
    e := NewMCPExecutor(cfg)
    defer e.Close()

    prompt := mcpPrompt{Server: "slow", Tool: "anything", Args: map[string]interface{}{}}
    promptBytes, _ := json.Marshal(prompt)

    start := time.Now()
    _, err := e.Run(context.Background(), executor.Task{Prompt: string(promptBytes)}, &captureSink{})
    elapsed := time.Since(start)

    require.Error(t, err, "slow server must time out")
    // Default is 30s; allow generous slack (35s) so CI is not flaky.
    require.Less(t, elapsed, 35*time.Second, "timeout did not fire; elapsed=%v", elapsed)
}

// TestMCPExecutor_HTTPResponseSizeCapEnforced pins §1.4 #15 size cap:
// a server returning a huge body must not OOM the slave.
func TestMCPExecutor_HTTPResponseSizeCapEnforced(t *testing.T) {
    huge := make([]byte, 17*1024*1024) // 17 MiB > 16 MiB cap
    for i := range huge {
        huge[i] = 'A'
    }
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(200)
        _, _ = w.Write(huge)
    }))
    defer srv.Close()

    cfg := map[string]MCPServerCfg{
        "huge": {Transport: "http", URL: srv.URL},
    }
    e := NewMCPExecutor(cfg)
    defer e.Close()

    prompt := mcpPrompt{Server: "huge", Tool: "anything", Args: map[string]interface{}{}}
    promptBytes, _ := json.Marshal(prompt)

    _, err := e.Run(context.Background(), executor.Task{Prompt: string(promptBytes)}, &captureSink{})
    require.Error(t, err, "oversized response must be rejected")
    require.Contains(t, strings.ToLower(err.Error()), "response")
}
```

Helper: if `captureSink` doesn't exist, add a local one that implements the Sink interface (just collects Write calls — look at existing tests in mcp_test.go for reference).

Imports: `context`, `encoding/json`, `net/http`, `net/http/httptest`, `strings`, `testing`, `time`, `require`.

### Step 2: Run tests — expect RED

```bash
go test ./internal/executor/ -run "TestMCPExecutor_HTTP" -v -count=1 -timeout 60s
```

Expected:
- `TestMCPExecutor_HTTPTimeoutFiresWithinExpectedWindow`: with current `&http.Client{}` (no timeout), the test would hang past 35s — likely fails at the 60s -timeout cap.
- `TestMCPExecutor_HTTPResponseSizeCapEnforced`: current `io.ReadAll` swallows 17 MiB silently, then succeeds (no error); test fails because err is nil.

### Step 3: Add constants + apply timeout + LimitReader

In `internal/executor/mcp.go`:

1. Add constants near top (after imports):

```go
const (
    mcpHTTPTimeout      = 30 * time.Second
    mcpMaxResponseBytes = 16 * 1024 * 1024 // 16 MiB
)
```

Verify `time` imported.

2. In `NewMCPExecutor`, set timeout:

```go
httpCli: &http.Client{Timeout: mcpHTTPTimeout},
```

3. Find `io.ReadAll(resp.Body)` (line 270). Replace with:

```go
raw, err := io.ReadAll(io.LimitReader(resp.Body, mcpMaxResponseBytes+1))
if err != nil {
    return Result{}, fmt.Errorf("read mcp http response: %w", err)
}
if int64(len(raw)) > mcpMaxResponseBytes {
    return Result{}, fmt.Errorf("mcp http response exceeds %d bytes; refusing to buffer", mcpMaxResponseBytes)
}
```

(The existing error handling for `io.ReadAll` may already exist — preserve its surrounding error wrapping.)

### Step 4: Run tests — expect GREEN

```bash
go test ./internal/executor/ -run "TestMCPExecutor_HTTP" -v -count=1 -timeout 60s
```

Expected: 2 PASS. Timeout test should fire in ~30s wall time.

### Step 5: Full executor package + race

```bash
go test ./internal/executor/... -race -count=1 -timeout 60s
```

Expected: PASS.

### Step 6: Commit

```bash
git add internal/executor/mcp.go internal/executor/mcp_test.go
# or if new file:
# git add internal/executor/mcp_http_test.go
git commit -m "fix(executor): HTTP MCP gets 30s timeout + 16 MiB cap (Bug #15)

httpCli was &http.Client{} with no Timeout, and io.ReadAll had no size
limit. A slow MCP server hung the dispatcher forever; a malicious one
could return arbitrary bytes and OOM the slave.

Adds mcpHTTPTimeout (30s) on the shared http.Client and wraps response
reads in io.LimitReader(body, max+1) followed by a size check. Both
limits hardcoded for v1; cfg knobs deferred until needed.

Fixes §1.4 #15 of docs/review-2026-06-13.md.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: permissions patch field whitelist (Bug #16)

**Files:**
- Modify `cmd/slave-agent/permissions_executor.go`
- Test `cmd/slave-agent/permissions_executor_test.go`

### Existing struct (from pkg/agentbackend/backend.go)

```go
type Patch struct {
    Presets     []string `json:"presets,omitempty"`
    AllowAdd    []string `json:"allow_add,omitempty"`
    AllowRemove []string `json:"allow_remove,omitempty"`
    DenyAdd     []string `json:"deny_add,omitempty"`
    DenyRemove  []string `json:"deny_remove,omitempty"`
    Mode        string   `json:"mode,omitempty"`
}
```

Note: there is NO `Remove`/`AskAdd` (spec was wrong). The fields are the 6 above. Whitelist applies to the 6 named fields; "any other JSON field" is already rejected at JSON-unmarshal time because the struct doesn't have it (assuming `DisallowUnknownFields` is used somewhere — verify, see below).

### What validation is actually needed

1. Reject `*` wildcards in any list (Presets/AllowAdd/AllowRemove/DenyAdd/DenyRemove). The danger is broadening allow or narrowing deny via wildcard.
2. Reject empty-string entries (whitespace gibberish).
3. Reject Mode values not in a known set (e.g. allowed: "strict", "lenient" — read `agentbackend` to confirm).
4. (Already enforced by Go json decode into typed struct: unknown fields silently ignored unless `DisallowUnknownFields`. Add `DisallowUnknownFields` if not present — search how req.Patch is unmarshaled in permissions_executor.go.)

### Step 1: Write failing tests

Look at existing `permissions_executor_test.go` (may not exist; check `ls cmd/slave-agent/`). If absent, create.

Look at how `Run` is called in tests of similar shape (other executors). The Run signature is `func Run(ctx, task executor.Task, sink executor.Sink)` — task.Prompt is JSON.

```go
package main

import (
    "context"
    "encoding/json"
    "testing"

    "github.com/stretchr/testify/require"
    "github.com/yourorg/multi-agent/internal/executor"
    "github.com/yourorg/multi-agent/pkg/agentbackend"
)

type fakeStore struct {
    state    agentbackend.State
    err      error
    patchSeen agentbackend.Patch
}

func (f *fakeStore) Get(_ context.Context) (agentbackend.State, error) {
    return f.state, f.err
}
func (f *fakeStore) Patch(_ context.Context, p agentbackend.Patch) (agentbackend.State, error) {
    f.patchSeen = p
    return f.state, f.err
}

type noopSink struct{}

func (noopSink) Write(_, _ string) {}
func (noopSink) Close()             {}

// TestPermissionsPatch_RejectsStarWildcard pins §1.4 #16 invariant:
// LLM cannot pass '*' to broaden allow / narrow deny.
func TestPermissionsPatch_RejectsStarWildcard(t *testing.T) {
    store := &fakeStore{}
    e := newPermissionsExecutor(store, nil)

    req := permissionsRequest{
        Op: "patch",
        Patch: agentbackend.Patch{AllowAdd: []string{"*"}},
    }
    raw, _ := json.Marshal(req)
    _, err := e.Run(context.Background(), executor.Task{Prompt: string(raw)}, noopSink{})
    require.Error(t, err)
    require.Contains(t, err.Error(), "wildcard")
    // The store must NOT have been called.
    require.Empty(t, store.patchSeen.AllowAdd)
}

// TestPermissionsPatch_RejectsEmptyString sanity (single-line whitespace
// shouldn't accidentally match nothing/everything depending on downstream).
func TestPermissionsPatch_RejectsEmptyString(t *testing.T) {
    store := &fakeStore{}
    e := newPermissionsExecutor(store, nil)

    req := permissionsRequest{
        Op: "patch",
        Patch: agentbackend.Patch{DenyAdd: []string{""}},
    }
    raw, _ := json.Marshal(req)
    _, err := e.Run(context.Background(), executor.Task{Prompt: string(raw)}, noopSink{})
    require.Error(t, err)
    require.Contains(t, err.Error(), "empty")
}

// TestPermissionsPatch_AcceptsKnownPatch (positive case)
func TestPermissionsPatch_AcceptsKnownPatch(t *testing.T) {
    store := &fakeStore{state: agentbackend.State{}}
    refreshCalled := false
    refresh := func(_ context.Context, _ string) error {
        refreshCalled = true
        return nil
    }
    e := newPermissionsExecutor(store, refresh)

    req := permissionsRequest{
        Op: "patch",
        Patch: agentbackend.Patch{AllowAdd: []string{"Read(./*)"}},
    }
    raw, _ := json.Marshal(req)
    _, err := e.Run(context.Background(), executor.Task{Prompt: string(raw)}, noopSink{})
    require.NoError(t, err)
    require.True(t, refreshCalled, "refresh should fire on successful patch")
    require.Equal(t, []string{"Read(./*)"}, store.patchSeen.AllowAdd)
}
```

**Note about test constructor**: the actual constructor in permissions_executor.go might not be `newPermissionsExecutor`. Read the file first to find: a) struct name (`permissionsExecutor`?), b) constructor name (`newPermissionsExecutor(store, refresh)`?). Adapt test code.

Also: `permissionsRequest` struct may be named differently. Read existing file.

### Step 2: Run tests — expect RED

```bash
go test ./cmd/slave-agent/ -run "TestPermissionsPatch_" -v -count=1
```

Expected: 2 of 3 FAIL (Rejects tests; current Patch path accepts any input).

### Step 3: Add validatePermissionsPatch

In `cmd/slave-agent/permissions_executor.go`, add (above the Run method):

```go
// validatePermissionsPatch enforces a strict allowlist on the patch
// fields and rejects '*' wildcards / empty-string entries before
// reaching the persistence layer. The struct fields themselves are
// fixed (any other JSON key is silently ignored by json.Unmarshal into
// a typed struct), so this validator handles value-level sanitation.
// Fixes §1.4 #16 of docs/review-2026-06-13.md.
func validatePermissionsPatch(p agentbackend.Patch) error {
    lists := map[string][]string{
        "presets":      p.Presets,
        "allow_add":    p.AllowAdd,
        "allow_remove": p.AllowRemove,
        "deny_add":     p.DenyAdd,
        "deny_remove":  p.DenyRemove,
    }
    for name, list := range lists {
        for _, item := range list {
            if item == "*" {
                return fmt.Errorf("permissions patch %s contains '*' wildcard; reject", name)
            }
            if strings.TrimSpace(item) == "" {
                return fmt.Errorf("permissions patch %s contains empty entry; reject", name)
            }
        }
    }
    // Mode is free-form today (whatever the agentbackend accepts); leave
    // its validation to the backend. If it ever becomes an enum, validate
    // here too.
    return nil
}
```

Verify `fmt` and `strings` imported.

Then in the `case "patch":` branch (around line 39), insert the validation before `store.Patch`:

```go
case "patch":
    if err := validatePermissionsPatch(req.Patch); err != nil {
        return executor.Result{}, err
    }
    state, err = e.store.Patch(ctx, req.Patch)
    if err == nil && e.refresh != nil {
        err = e.refresh(ctx, "permission update")
    }
```

### Step 4: Run tests — expect GREEN

```bash
go test ./cmd/slave-agent/ -run "TestPermissionsPatch_" -v -count=1
```

Expected: 3 PASS.

### Step 5: Full cmd/slave-agent + race

```bash
go test ./cmd/slave-agent/... -race -count=1
```

Expected: PASS.

### Step 6: Commit

```bash
git add cmd/slave-agent/permissions_executor.go cmd/slave-agent/permissions_executor_test.go
git commit -m "fix(slave-agent): permissions patch rejects '*' wildcard + empty entries (Bug #16)

agentbackend.Patch was passed through to store.Patch with zero
value-level validation. An LLM could send {\"allow_add\":[\"*\"]} or
{\"deny_remove\":[\"*\"]} to fully broaden access; empty-string entries
risked matching unintended capabilities downstream.

validatePermissionsPatch iterates Presets / AllowAdd / AllowRemove /
DenyAdd / DenyRemove rejecting '*' and empty/whitespace. Called before
store.Patch in the patch op path. Mode field is intentionally left to
backend validation.

Unknown top-level JSON fields are already silently dropped by Unmarshal
into the typed Patch struct (no need for DisallowUnknownFields here).

Fixes §1.4 #16 of docs/review-2026-06-13.md.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: driver `source_path` jail (Bug #17)

**Files:**
- Modify `internal/driver/safe_paths.go` — add `AssertReadableSource`
- Modify `internal/driver/config.go` — `DriverDefaults` gains `SourcePathReadRoots []string`
- Modify `internal/driver/slave_file_tools.go:314` — call AssertReadableSource
- Test `internal/driver/slave_file_tools_test.go`

### Step 1: Add AssertReadableSource

In `internal/driver/safe_paths.go` (Task 1 already added `resolveExistingPrefix` there), add:

```go
// AssertReadableSource enforces that source_path is either under the
// driver's WorkDir or in cfg-declared SourcePathReadRoots. Stops the
// LLM from using write_slave_file as a two-stage RCE channel (read
// arbitrary driver-host file → base64 push to slave). Fixes §1.4 #17.
//
// workDir is the driver's working directory (e.g. cfg.Claude.WorkDir
// or process cwd). allowedRoots are operator-declared additional roots
// that are explicitly safe to read from (e.g. /var/lib/loom/inputs).
func AssertReadableSource(p, workDir string, allowedRoots []string) error {
    abs, err := filepath.Abs(p)
    if err != nil {
        return fmt.Errorf("resolve source_path %s: %w", p, err)
    }
    real, err := resolveExistingPrefix(abs)
    if err != nil {
        return fmt.Errorf("resolve source_path %s: %w", p, err)
    }
    roots := append([]string{workDir}, allowedRoots...)
    for _, root := range roots {
        if root == "" {
            continue
        }
        rootAbs, err := filepath.Abs(root)
        if err != nil {
            continue
        }
        realRoot, err := resolveExistingPrefix(rootAbs)
        if err != nil {
            realRoot = rootAbs
        }
        rel, err := filepath.Rel(realRoot, real)
        if err != nil {
            continue
        }
        if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
            return nil
        }
    }
    return fmt.Errorf("source_path %s is outside driver workdir and allowed roots", p)
}
```

### Step 2: Add SourcePathReadRoots to DriverDefaults

In `internal/driver/config.go` find `type DriverDefaults struct {` (around line 66). Add:

```go
    // SourcePathReadRoots is the optional allowlist of additional roots
    // that write_slave_file may read source_path from, beyond the driver's
    // claude/codex WorkDir. Empty by default — operators with legitimate
    // need to ingest from elsewhere (e.g. /var/lib/loom/inputs) opt in.
    SourcePathReadRoots []string `yaml:"source_path_read_roots,omitempty"`
```

### Step 3: Pick the "workDir" for source_path jail

Read `internal/driver/slave_file_tools.go` to see what `w.t.cfg.*` fields are available. We need a reasonable "driver workdir". Likely candidates:
- `w.t.cfg.Claude.WorkDir` if `Agent.Kind == "claude"`
- `w.t.cfg.Codex.WorkDir` if `Agent.Kind == "codex"`
- Or just `os.Getwd()` fallback

Pick the matching one based on agent kind:

```go
func (t *Tools) driverWorkDir() string {
    switch t.cfg.Agent.Kind {
    case "codex":
        if t.cfg.Codex.WorkDir != "" {
            return t.cfg.Codex.WorkDir
        }
    default:
        if t.cfg.Claude.WorkDir != "" {
            return t.cfg.Claude.WorkDir
        }
    }
    cwd, _ := os.Getwd()
    return cwd
}
```

Add this helper in `slave_file_tools.go` (or a sibling file). Then call it.

### Step 4: Wire AssertReadableSource into write_slave_file

At slave_file_tools.go:314-321, change:

```go
case args.SourcePath != "":
    body, err := os.ReadFile(args.SourcePath)
    if err != nil { ... }
    // ...
```

to:

```go
case args.SourcePath != "":
    if err := AssertReadableSource(args.SourcePath, w.t.driverWorkDir(), w.t.cfg.DriverDefaults.SourcePathReadRoots); err != nil {
        return nil, &MCPToolError{Message: err.Error()}
    }
    body, err := os.ReadFile(args.SourcePath)
    if err != nil { ... }
    // ...
```

### Step 5: Write failing tests

`internal/driver/slave_file_tools_test.go` — look at existing tests in the file for the Tools constructor pattern (likely `newTestTools(t, ...)` returning a populated Tools). Append:

```go
// TestWriteSlaveFile_SourcePathOutsideJailRejected pins §1.4 #17: driver
// can't be used as a two-stage RCE channel where LLM picks an arbitrary
// host file (e.g. /etc/shadow) and ships it to a slave.
func TestWriteSlaveFile_SourcePathOutsideJailRejected(t *testing.T) {
    workDir := t.TempDir()
    // Create a file outside the jail.
    outsideDir := t.TempDir()
    outsideFile := filepath.Join(outsideDir, "secret")
    require.NoError(t, os.WriteFile(outsideFile, []byte("sensitive"), 0o644))

    tools := newTestTools(t, &fakeSDK{ ... })
    tools.cfg.Claude.WorkDir = workDir  // jail set

    args, _ := json.Marshal(map[string]any{
        "target_display_name": "slave-a",
        "path":                "/dst.txt",
        "source_path":         outsideFile,
    })
    _, err := toolByName(t, tools, "write_slave_file").Call(context.Background(), args)
    require.Error(t, err)
    require.Contains(t, err.Error(), "outside")
}

// TestWriteSlaveFile_SourcePathInsideJailAccepted (positive)
func TestWriteSlaveFile_SourcePathInsideJailAccepted(t *testing.T) {
    workDir := t.TempDir()
    inside := filepath.Join(workDir, "data.txt")
    require.NoError(t, os.WriteFile(inside, []byte("ok"), 0o644))

    // Need an SDK that mocks DelegateTask + WaitForTask so the path actually
    // gets through. Look at TestWriteSlaveFile_* existing tests for the
    // fakeSDK setup that returns successful task results.
    sdk := &fakeSDK{ /* delegateFunc returns ok */ }
    tools := newTestTools(t, sdk)
    tools.cfg.Claude.WorkDir = workDir

    args, _ := json.Marshal(map[string]any{
        "target_display_name": "slave-a",
        "path":                "/dst.txt",
        "source_path":         inside,
    })
    _, err := toolByName(t, tools, "write_slave_file").Call(context.Background(), args)
    require.NoError(t, err)  // path passed jail check
}

// TestWriteSlaveFile_SourcePathInExtraAllowedRoot verifies SourcePathReadRoots
// is honored (positive — operator opt-in works).
func TestWriteSlaveFile_SourcePathInExtraAllowedRoot(t *testing.T) {
    workDir := t.TempDir()
    extraRoot := t.TempDir()
    inside := filepath.Join(extraRoot, "input.txt")
    require.NoError(t, os.WriteFile(inside, []byte("ok"), 0o644))

    sdk := &fakeSDK{ /* delegateFunc returns ok */ }
    tools := newTestTools(t, sdk)
    tools.cfg.Claude.WorkDir = workDir
    tools.cfg.DriverDefaults.SourcePathReadRoots = []string{extraRoot}

    args, _ := json.Marshal(map[string]any{
        "target_display_name": "slave-a",
        "path":                "/dst.txt",
        "source_path":         inside,
    })
    _, err := toolByName(t, tools, "write_slave_file").Call(context.Background(), args)
    require.NoError(t, err)
}
```

Implementer: adapt the `fakeSDK` to whatever shape the existing tests use; the goal is just to get the path past delegation. The Rejected test asserts the jail check fires BEFORE any SDK call.

### Step 6: Run tests — expect RED→GREEN

```bash
go test ./internal/driver/ -run "TestWriteSlaveFile_SourcePath" -v -count=1
```

Expected RED first (3 FAIL: jail not enforced), GREEN after wiring.

### Step 7: Full driver package + race

```bash
go test ./internal/driver/... -race -count=1
```

Expected: PASS.

### Step 8: Commit

```bash
git add internal/driver/safe_paths.go internal/driver/config.go internal/driver/slave_file_tools.go internal/driver/slave_file_tools_test.go
git commit -m "fix(driver): write_slave_file source_path jail (Bug #17)

source_path was read with os.ReadFile and zero path validation.
LLM could pick any host file (/etc/shadow / /root/.ssh/...) and push
it to a slave via write_slave_file — a two-stage exfiltration channel.

AssertReadableSource enforces source_path resolves under the driver's
Claude/Codex WorkDir or a cfg-declared SourcePathReadRoots allowlist
(default empty). Both checks use resolveExistingPrefix so symlinks are
followed before the jail check.

DriverDefaults gains source_path_read_roots []string YAML field for
operators with legitimate need to ingest from a controlled directory.

Fixes §1.4 #17 of docs/review-2026-06-13.md.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: /files/put MaxBytesReader + O_EXCL + parent fsync (Bug #18)

**Files:**
- Modify `internal/driver/files_handler.go:222-283`
- Test `internal/driver/files_handler_test.go`

### Step 1: Write failing tests

Look at existing files_handler_test.go for the test server pattern. Probably uses httptest or direct handler call.

```go
// TestFilesHandler_PutBodyOverMaxBytesRejected pins §1.4 #18 size cap.
func TestFilesHandler_PutBodyOverMaxBytesRejected(t *testing.T) {
    // Build a FilesHandler with a registered write token, then PUT a body
    // that exceeds maxPutBytes.
    //
    // For sanity, override maxPutBytes via a test-local constant or use
    // the default (1 GiB — too large for a test). Either:
    //   (a) Introduce a small testable cap (export maxPutBytes as a var
    //       a test can set via t.Cleanup, or a constructor param).
    //   (b) Send slightly-larger-than-real cap via a stub Reader that
    //       generates 1 GiB+1 of dummy data — slow but works.
    //
    // Easier: refactor maxPutBytes to a var (not const) so the test can
    // override it. Implementation hint: change "const maxPutBytes" to
    // "var maxPutBytes" (no observable behavior change outside tests).

    prev := maxPutBytes
    maxPutBytes = 10 // ridiculously small for test
    t.Cleanup(func() { maxPutBytes = prev })

    // ... build handler, register write token, PUT body of 100 bytes ...
    // assert 413 Request Entity Too Large
}

// TestFilesHandler_PutWithExclTmpDoesNotClobberExistingTmp verifies
// O_EXCL prevents a local-user attacker from pre-creating .tmp.* to
// race-control the file. Test creates the tmp file first, then attempts
// PUT.
//
// This is delicate to test because randSuffix() is random; the test
// would need to either control the suffix (refactor) or accept "best
// effort by inducing collisions multiple times". Simplest test:
//   - Pre-create a file at the EXACT target path with O_EXCL
//   - PUT a different content
//   - With O_EXCL on tmp, randSuffix collision is astronomically unlikely
//     so the test doesn't actually catch this. Skip or test differently.
//
// Instead: cover by inspection only — focus the test on body cap and
// the parent fsync (also hard to test directly). For O_EXCL, the change
// itself is small enough to be reviewed visually.
```

Implementer: focus the test on the body-cap branch since it's the most observable. For O_EXCL and parent fsync, code inspection in code review is enough; add a regression test only if straightforward.

### Step 2: Refactor maxPutBytes to var + add cap to handlePut

`internal/driver/files_handler.go` near top:

```go
// maxPutBytes caps body size for /files/put. Var (not const) so tests
// can override; production callers should not mutate. Fixes §1.4 #18.
var maxPutBytes int64 = 1 << 30 // 1 GiB
```

In `handlePut` (line 222), after the parent dir check and before `OpenFile`, wrap body:

```go
body := http.MaxBytesReader(w, r.Body, maxPutBytes)
defer body.Close()
```

Change `OpenFile` to use O_EXCL:

```go
out, err := os.OpenFile(tmpName, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
```

Change `io.Copy(mw, r.Body)` to use the wrapped body, and handle MaxBytesError:

```go
written, copyErr := io.Copy(mw, body)
if copyErr != nil {
    out.Close()
    os.Remove(tmpName)
    var maxErr *http.MaxBytesError
    if errors.As(copyErr, &maxErr) {
        http.Error(w, fmt.Sprintf("body exceeds %d bytes", maxPutBytes), http.StatusRequestEntityTooLarge)
        return
    }
    http.Error(w, copyErr.Error(), http.StatusInternalServerError)
    return
}
```

Verify `errors` imported.

After `os.Rename(tmpName, target)`, add parent fsync:

```go
if err := os.Rename(tmpName, target); err != nil {
    http.Error(w, err.Error(), http.StatusInternalServerError)
    return
}

// Parent dir fsync ensures the new dirent survives a crash.
if parentFd, openErr := os.Open(parent); openErr == nil {
    _ = parentFd.Sync()
    parentFd.Close()
}
```

### Step 3: Run tests — expect GREEN

```bash
go test ./internal/driver/ -run "TestFilesHandler_Put" -v -count=1
```

Expected: PASS.

### Step 4: Full driver package + race

```bash
go test ./internal/driver/... -race -count=1
```

Expected: PASS.

### Step 5: Commit

```bash
git add internal/driver/files_handler.go internal/driver/files_handler_test.go
git commit -m "fix(driver): /files/put body cap + O_EXCL tmp + parent fsync (Bug #18)

handlePut had three hardening gaps:
  (1) No body size limit — io.Copy would happily fill disk.
  (2) tmp file opened with O_TRUNC, so a local user could pre-create
      the .tmp.<suffix> and race-control the write.
  (3) After Rename, parent dir was not fsynced — a crash could lose
      the new dirent even though the inode landed.

Fix:
  (1) http.MaxBytesReader cap at 1 GiB (var, tunable in tests).
  (2) os.O_CREATE|O_WRONLY|O_EXCL on tmp; 0o600 mode (not 0o644 —
      tmp need not be world-readable).
  (3) Open(parent).Sync() after Rename (best-effort).

Returns 413 Request Entity Too Large on body cap exceeded so callers
distinguish 'too big' from other 500s.

Fixes §1.4 #18 of docs/review-2026-06-13.md.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 7: Final regression + e2e prep

- [ ] **Step 1: full build + vet**

```bash
go build ./... && go vet ./...
```

Expected: clean.

- [ ] **Step 2: full test + race**

```bash
go test ./... -race -count=1 -timeout 600s
```

Expected: PASS.

- [ ] **Step 3: Verify master path untouched**

```bash
git diff --stat master..HEAD -- 'cmd/master-agent/**' 'internal/orchestrator/**' 'internal/orchestration/**'
```

Expected: empty output (no master path changes).

- [ ] **Step 4: e2e**

Follow [[e2e_prod_test_codex_local]] runbook. Rebuild binaries, restart observer + slave, run codex 5-step prompt. Document outcome in `docs/superpowers/specs/2026-06-13-slave-security-1.4-e2e-evidence.md`.

The §1.4 fixes are mostly **defensive** (guard the failure case) — happy-path e2e won't trigger them, but they must not break the happy path.

- [ ] **Step 5: `git log --oneline master..HEAD`** — should have 1 docs (spec) + 1 plan + 1 helper (Task 1) + 5 fix (Tasks 2-6) + 1 final test/evidence = 8-9 commits.

---

## Verification checklist

- [ ] All 5 §1.4 bugs map to commits
- [ ] All new tests pass (race-clean)
- [ ] Existing tests not regressed
- [ ] Master path untouched (`git diff --stat master..HEAD -- 'cmd/master-agent/**' 'internal/orchestrator/**' 'internal/orchestration/**'` empty)
- [ ] e2e evidence committed
