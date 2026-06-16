# Commander UI Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the approved `/commander` three-pane workbench with daemon-grouped sessions, accurate turn state, cwd file browsing, and a React/Vite frontend served by Go embed.

**Architecture:** Keep daemon-owned data on the daemon and expose it through additive commander commands. The observer owns UI view models, short-lived caches, and turn state, while the React app renders stable JSON models and never infers backend semantics from raw command payloads.

**Tech Stack:** Go commander/commanderhub, existing gorilla WebSocket protocol, React + TypeScript + Vite, Vitest/Testing Library, committed Vite `dist` served by `go:embed`.

---

## File Structure

Create and modify these units:

- Modify `pkg/agentbackend/backend.go`: add `Session.Title`.
- Modify `pkg/agentbackend/claude/sessions.go`, `pkg/agentbackend/codex/sessions.go`, `pkg/agentbackend/opencode/sessions.go`: fill `Session.Title` from first user prompt.
- Modify backend session tests in `pkg/agentbackend/{claude,codex,opencode}/sessions_test.go`: pin title extraction.
- Modify `internal/commander/protocol.go`: add register capabilities and file command payload/result types.
- Modify `internal/commander/protocol_test.go`: pin JSON shapes for capabilities and file commands.
- Create `internal/commander/files.go`: safe cwd-rooted directory listing, 2MB read-only preview helpers, and `*Handler` file methods.
- Create `internal/commander/files_test.go`: root sandbox, lazy listing, too-large, binary, unknown session tests.
- Modify `internal/commander/wsclient.go`: dispatch `list_files` and `read_file`.
- Modify `internal/commander/wsclient_test.go`: verify file command dispatch and unknown command behavior.
- Modify `internal/commanderhub/registry.go` and `internal/commanderhub/hub.go`: store daemon capabilities and last seen time.
- Create `internal/commanderhub/turn_state.go`: in-memory turn state store keyed by owner/daemon/session.
- Create `internal/commanderhub/tree.go`: enriched daemon/session tree view model and per-daemon session cache.
- Create `internal/commanderhub/tree_test.go`: grouping, sorting, title fallback, cache invalidation tests.
- Modify `internal/commanderhub/proxy.go`: add file command proxy helpers and cache invalidation hooks.
- Modify `internal/commanderhub/http.go`: add `/tree`, `/files`, `/files/content`, turn 409/state updates.
- Modify `internal/commanderhub/http_test.go`: route tests for tree, turn state, file proxy.
- Replace `internal/commanderhub/assets/app.js`, `style.css`, `index.html` usage with Vite `dist`.
- Create `internal/commanderhub/webapp/*`: React app, API client, components, tests.
- Modify `internal/commanderhub/web.go` and `web_test.go`: embed and serve `assets/dist`.
- Create `internal/commanderhub/assets/dist/*`: committed Vite build output.

Implementation should use focused commits after each task. Do not squash during execution; the review checkpoints need small diffs.

---

### Task 1: Add Session Titles at the Backend Boundary

**Files:**
- Modify: `pkg/agentbackend/backend.go`
- Modify: `pkg/agentbackend/claude/sessions.go`
- Modify: `pkg/agentbackend/codex/sessions.go`
- Modify: `pkg/agentbackend/opencode/sessions.go`
- Test: `pkg/agentbackend/claude/sessions_test.go`
- Test: `pkg/agentbackend/codex/sessions_test.go`
- Test: `pkg/agentbackend/opencode/sessions_test.go`

- [ ] **Step 1: Add failing tests for title extraction**

Add one assertion to each backend's existing "returns known sessions" or "returns messages" test. Use the backend's existing fixture helper and assert the first user prompt becomes `Session.Title`.

For Codex, add this assertion in `TestListSessions_ReturnsKnownSessions` after the session with fixture user input is found:

```go
if gotByID["aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"].Title != "please inspect the repo" {
	t.Fatalf("Title=%q want first user prompt", gotByID["aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"].Title)
}
```

For Claude, add the equivalent assertion against the fixture session that contains a user prompt:

```go
if gotByID["aaaa1111-bbbb-2222-cccc-333333333333"].Title != "please inspect the repo" {
	t.Fatalf("Title=%q want first user prompt", gotByID["aaaa1111-bbbb-2222-cccc-333333333333"].Title)
}
```

For opencode, add the equivalent assertion against `ses_a`:

```go
if gotByID["ses_a"].Title != "please inspect the repo" {
	t.Fatalf("Title=%q want first user prompt", gotByID["ses_a"].Title)
}
```

- [ ] **Step 2: Run backend session tests and verify they fail**

Run:

```bash
go test ./pkg/agentbackend/claude ./pkg/agentbackend/codex ./pkg/agentbackend/opencode -run 'Test.*Sessions|TestGetSession' -count=1
```

Expected: FAIL with `Title="" want first user prompt` in each backend that has the new assertion.

- [ ] **Step 3: Add the `Title` field to `agentbackend.Session`**

In `pkg/agentbackend/backend.go`, add `Title` after `WorkingDir`:

```go
// Title is a short human-readable name for the session. Backends set it to
// the first user prompt when available. UIs may fall back to Preview or ID.
Title string
```

- [ ] **Step 4: Set `Title` while scanning backend sessions**

In each scanner, track the first non-empty user text and assign a capped single-line title. Use this helper shape in each backend package:

```go
func titleFromUserText(s string) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if s == "" {
		return ""
	}
	if len(s) <= agentbackend.SessionPreviewMaxBytes {
		return s
	}
	return truncatePreview(s)
}
```

When a user message is parsed:

```go
if role == "user" && res.session.Title == "" {
	res.session.Title = titleFromUserText(text)
}
```

For scanner code that does not use a `role` variable, place the assignment in the `user_input` / user-message branch immediately after extracting `text`.

- [ ] **Step 5: Run backend session tests and verify they pass**

Run:

```bash
go test ./pkg/agentbackend/claude ./pkg/agentbackend/codex ./pkg/agentbackend/opencode ./pkg/agentbackend -count=1
```

Expected: PASS for all listed packages.

- [ ] **Step 6: Commit**

```bash
git add pkg/agentbackend/backend.go pkg/agentbackend/claude/sessions.go pkg/agentbackend/claude/sessions_test.go pkg/agentbackend/codex/sessions.go pkg/agentbackend/codex/sessions_test.go pkg/agentbackend/opencode/sessions.go pkg/agentbackend/opencode/sessions_test.go
git commit -m "feat(agentbackend): expose session titles"
```

---

### Task 2: Add Commander File Protocol Types and Capabilities

**Files:**
- Modify: `internal/commander/protocol.go`
- Test: `internal/commander/protocol_test.go`

- [ ] **Step 1: Add failing protocol tests**

Append these tests to `internal/commander/protocol_test.go`:

```go
func TestEnvelope_RegisterCarriesCapabilities(t *testing.T) {
	in := Envelope{
		Type: "register",
		Payload: mustMarshal(t, RegisterPayload{
			SchemaVersion: SchemaVersion,
			Kind:          "codex",
			Capabilities:  []string{"sessions", "turn", "files"},
		}),
	}
	b, _ := json.Marshal(in)
	var out Envelope
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	var pl RegisterPayload
	if err := json.Unmarshal(out.Payload, &pl); err != nil {
		t.Fatal(err)
	}
	if len(pl.Capabilities) != 3 || pl.Capabilities[2] != "files" {
		t.Fatalf("capabilities=%v", pl.Capabilities)
	}
}

func TestEnvelope_FileCommandsRoundTrip(t *testing.T) {
	listArgs := FileListArgs{ID: "s1", Path: "internal"}
	readArgs := FileReadArgs{ID: "s1", Path: "go.mod"}
	for name, args := range map[string]any{
		"list_files": listArgs,
		"read_file":  readArgs,
	} {
		env := Envelope{
			Type: "command",
			ID:   "cmd-file",
			Payload: mustMarshal(t, CommandPayload{
				Command: name,
				Args:    mustMarshal(t, args),
			}),
		}
		b, _ := json.Marshal(env)
		var out Envelope
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatal(err)
		}
		var cp CommandPayload
		if err := json.Unmarshal(out.Payload, &cp); err != nil {
			t.Fatal(err)
		}
		if cp.Command != name {
			t.Fatalf("command=%q want %q", cp.Command, name)
		}
	}
}
```

- [ ] **Step 2: Run protocol tests and verify they fail**

Run:

```bash
go test ./internal/commander -run 'TestEnvelope_RegisterCarriesCapabilities|TestEnvelope_FileCommandsRoundTrip' -count=1
```

Expected: FAIL with undefined `Capabilities`, `FileListArgs`, or `FileReadArgs`.

- [ ] **Step 3: Add protocol fields and file payload types**

In `internal/commander/protocol.go`, add:

```go
const (
	CapabilitySessions = "sessions"
	CapabilityTurn     = "turn"
	CapabilityFiles    = "files"
)
```

Extend `RegisterPayload`:

```go
Capabilities []string `json:"capabilities,omitempty"`
```

Add file payload/result types:

```go
const MaxFilePreviewBytes int64 = 2 * 1024 * 1024

type FileListArgs struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

type FileEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Kind    string `json:"kind"` // "file" or "dir"
	Size    int64  `json:"size,omitempty"`
	ModTime string `json:"mod_time,omitempty"`
}

type FileListResult struct {
	Root    string      `json:"root"`
	Path    string      `json:"path"`
	Entries []FileEntry `json:"entries"`
}

type FileReadArgs struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

type FileReadResult struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	MIME     string `json:"mime,omitempty"`
	Binary   bool   `json:"binary,omitempty"`
	TooLarge bool   `json:"too_large,omitempty"`
	Content  string `json:"content,omitempty"`
}
```

- [ ] **Step 4: Run protocol tests and verify they pass**

Run:

```bash
go test ./internal/commander -run 'TestEnvelope_RegisterCarriesCapabilities|TestEnvelope_FileCommandsRoundTrip|TestSchemaVersion_IsOne' -count=1
```

Expected: PASS. `TestSchemaVersion_IsOne` must still pass; this is an additive capability, not a schema bump.

- [ ] **Step 5: Commit**

```bash
git add internal/commander/protocol.go internal/commander/protocol_test.go
git commit -m "feat(commander): advertise file capabilities"
```

---

### Task 3: Implement Daemon-Side Safe File Listing and Preview

**Files:**
- Create: `internal/commander/files.go`
- Create: `internal/commander/files_test.go`

- [ ] **Step 1: Add failing file helper tests**

Create `internal/commander/files_test.go` with these tests:

```go
package commander

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

func TestHandlerListFilesUsesSessionWorkingDirRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "internal"), 0755); err != nil {
		t.Fatal(err)
	}
	h := &Handler{Backend: &fakeBackend{
		getFn: func(context.Context, string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
			return agentbackend.Session{ID: "s1", WorkingDir: root}, nil, nil
		},
	}}
	got, err := h.ListFiles(context.Background(), "s1", ".")
	if err != nil {
		t.Fatal(err)
	}
	if got.Root != root || got.Path != "." {
		t.Fatalf("root/path=%q/%q", got.Root, got.Path)
	}
	names := map[string]string{}
	for _, ent := range got.Entries {
		names[ent.Name] = ent.Kind
	}
	if names["go.mod"] != "file" || names["internal"] != "dir" {
		t.Fatalf("entries=%+v", got.Entries)
	}
}

func TestHandlerReadFileRejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	h := &Handler{Backend: &fakeBackend{
		getFn: func(context.Context, string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
			return agentbackend.Session{ID: "s1", WorkingDir: root}, nil, nil
		},
	}}
	_, err := h.ReadFile(context.Background(), "s1", "../secret.txt")
	if err == nil || !strings.Contains(err.Error(), "outside session root") {
		t.Fatalf("err=%v want outside session root", err)
	}
}

func TestHandlerReadFileCapsPreviewAtTwoMB(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "large.log")
	if err := os.WriteFile(path, make([]byte, MaxFilePreviewBytes+1), 0644); err != nil {
		t.Fatal(err)
	}
	h := &Handler{Backend: &fakeBackend{
		getFn: func(context.Context, string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
			return agentbackend.Session{ID: "s1", WorkingDir: root}, nil, nil
		},
	}}
	got, err := h.ReadFile(context.Background(), "s1", "large.log")
	if err != nil {
		t.Fatal(err)
	}
	if !got.TooLarge || got.Content != "" || got.Size != MaxFilePreviewBytes+1 {
		t.Fatalf("result=%+v", got)
	}
}

func TestHandlerReadFileDetectsBinary(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "blob.bin"), []byte{0, 1, 2, 3}, 0644); err != nil {
		t.Fatal(err)
	}
	h := &Handler{Backend: &fakeBackend{
		getFn: func(context.Context, string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
			return agentbackend.Session{ID: "s1", WorkingDir: root}, nil, nil
		},
	}}
	got, err := h.ReadFile(context.Background(), "s1", "blob.bin")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Binary || got.Content != "" {
		t.Fatalf("result=%+v", got)
	}
}
```

- [ ] **Step 2: Run file tests and verify they fail**

Run:

```bash
go test ./internal/commander -run 'TestHandler(ListFiles|ReadFile)' -count=1
```

Expected: FAIL with undefined `ListFiles`, `ReadFile`, or `MaxFilePreviewBytes`.

- [ ] **Step 3: Implement file helpers**

Create `internal/commander/files.go`:

```go
package commander

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

var errPathOutsideRoot = errors.New("path outside session root")

func (h *Handler) ListFiles(ctx context.Context, sessionID, rel string) (FileListResult, error) {
	root, target, cleanRel, err := h.sessionFileTarget(ctx, sessionID, rel)
	if err != nil {
		return FileListResult{}, err
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return FileListResult{}, err
	}
	out := FileListResult{Root: root, Path: cleanRel, Entries: make([]FileEntry, 0, len(entries))}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return FileListResult{}, err
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		kind := "file"
		if entry.IsDir() {
			kind = "dir"
		}
		childRel := filepath.ToSlash(filepath.Join(cleanRel, entry.Name()))
		if cleanRel == "." {
			childRel = entry.Name()
		}
		out.Entries = append(out.Entries, FileEntry{
			Name:    entry.Name(),
			Path:    childRel,
			Kind:    kind,
			Size:    info.Size(),
			ModTime: info.ModTime().UTC().Format(time.RFC3339Nano),
		})
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		if out.Entries[i].Kind != out.Entries[j].Kind {
			return out.Entries[i].Kind == "dir"
		}
		return strings.ToLower(out.Entries[i].Name) < strings.ToLower(out.Entries[j].Name)
	})
	return out, nil
}

func (h *Handler) ReadFile(ctx context.Context, sessionID, rel string) (FileReadResult, error) {
	_, target, cleanRel, err := h.sessionFileTarget(ctx, sessionID, rel)
	if err != nil {
		return FileReadResult{}, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return FileReadResult{}, err
	}
	if info.IsDir() {
		return FileReadResult{}, fmt.Errorf("path is a directory: %s", cleanRel)
	}
	res := FileReadResult{Path: cleanRel, Size: info.Size()}
	if info.Size() > MaxFilePreviewBytes {
		res.TooLarge = true
		return res, nil
	}
	body, err := os.ReadFile(target)
	if err != nil {
		return FileReadResult{}, err
	}
	res.MIME = http.DetectContentType(body)
	if bytes.IndexByte(body, 0) >= 0 || !utf8.Valid(body) {
		res.Binary = true
		return res, nil
	}
	res.Content = string(body)
	return res, nil
}

func (h *Handler) sessionFileTarget(ctx context.Context, sessionID, rel string) (string, string, string, error) {
	if h == nil || h.Backend == nil {
		return "", "", "", errors.New("backend unavailable")
	}
	sess, _, err := h.Backend.GetSession(ctx, sessionID)
	if errors.Is(err, agentbackend.ErrSessionNotFound) {
		return "", "", "", err
	}
	if err != nil {
		return "", "", "", err
	}
	root := sess.WorkingDir
	if root == "" {
		return "", "", "", errors.New("session working directory unknown")
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", "", "", err
	}
	cleanRel := filepath.Clean(filepath.FromSlash(rel))
	if cleanRel == "" {
		cleanRel = "."
	}
	if filepath.IsAbs(cleanRel) {
		return "", "", "", errPathOutsideRoot
	}
	target := filepath.Join(root, cleanRel)
	target, err = filepath.Abs(target)
	if err != nil {
		return "", "", "", err
	}
	if target != root && !strings.HasPrefix(target, root+string(os.PathSeparator)) {
		return "", "", "", errPathOutsideRoot
	}
	return root, target, filepath.ToSlash(cleanRel), nil
}
```

- [ ] **Step 4: Keep file ownership focused**

No interface declaration is needed; `ListFiles` and `ReadFile` are methods on `*Handler` implemented in `internal/commander/files.go`. Do not edit `internal/commander/handler.go` in this task.

- [ ] **Step 5: Run commander tests and verify they pass**

Run:

```bash
go test ./internal/commander -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/commander/files.go internal/commander/files_test.go
git commit -m "feat(commander): read session workspace files"
```

---

### Task 4: Dispatch File Commands Over the Daemon WebSocket

**Files:**
- Modify: `internal/commander/wsclient.go`
- Test: `internal/commander/wsclient_test.go`

- [ ] **Step 1: Add failing WS dispatch tests**

Append tests to `internal/commander/wsclient_test.go` using the existing `fakeObserver` harness:

```go
func TestWSClient_DispatchesListFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	fo := newFakeObserver(t)
	fo.sendAck = true
	srv := httptest.NewServer(fo.handler())
	defer srv.Close()
	c := NewWSClient(WSConfig{
		URL:        observerWSURL(srv),
		ProxyToken: "t",
		Register:   RegisterPayload{SchemaVersion: SchemaVersion, Kind: "codex"},
		Handler: &Handler{Backend: &fakeBackend{
			getFn: func(context.Context, string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
				return agentbackend.Session{ID: "s1", WorkingDir: root}, nil, nil
			},
		}},
		HeartbeatInt:   10 * time.Second,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     50 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()
	defer func() { cancel(); fo.closeAll(); <-errCh }()
	waitFor(t, c.Linked, time.Second)

	require.NoError(t, fo.Send(Envelope{
		Type: "command", ID: "files-1",
		Payload: jsonRaw(t, CommandPayload{Command: "list_files", Args: jsonRaw(t, FileListArgs{ID: "s1", Path: "."})}),
	}))
	waitFor(t, func() bool {
		for _, env := range fo.frames() {
			if env.Type == "command_result" && env.ID == "files-1" && strings.Contains(string(env.Payload), "go.mod") {
				return true
			}
		}
		return false
	}, time.Second)
}
```

Add imports used by the new test:

```go
import (
	"os"
	"path/filepath"
)
```

- [ ] **Step 2: Run the test and verify it fails**

Run:

```bash
go test ./internal/commander -run TestWSClient_DispatchesListFiles -count=1
```

Expected: FAIL with unknown command `list_files`.

- [ ] **Step 3: Dispatch `list_files` and `read_file`**

In `dispatchCommand` in `internal/commander/wsclient.go`, add cases before `session_turn`:

```go
case "list_files":
	var args FileListArgs
	if err := json.Unmarshal(cmd.Args, &args); err != nil {
		_ = write(errorEnvelope(env.ID, ErrCodeInternal, "bad list_files args: "+err.Error()))
		return
	}
	result, err := c.cfg.Handler.ListFiles(ctx, args.ID, args.Path)
	if errors.Is(err, agentbackend.ErrSessionNotFound) {
		_ = write(errorEnvelope(env.ID, ErrCodeSessionNotFound, "session not found"))
		return
	}
	if err != nil {
		_ = write(errorEnvelope(env.ID, ErrCodeBackendUnavailable, err.Error()))
		return
	}
	payload, _ := json.Marshal(result)
	_ = write(Envelope{Type: "command_result", ID: env.ID, Payload: payload})

case "read_file":
	var args FileReadArgs
	if err := json.Unmarshal(cmd.Args, &args); err != nil {
		_ = write(errorEnvelope(env.ID, ErrCodeInternal, "bad read_file args: "+err.Error()))
		return
	}
	result, err := c.cfg.Handler.ReadFile(ctx, args.ID, args.Path)
	if errors.Is(err, agentbackend.ErrSessionNotFound) {
		_ = write(errorEnvelope(env.ID, ErrCodeSessionNotFound, "session not found"))
		return
	}
	if err != nil {
		_ = write(errorEnvelope(env.ID, ErrCodeBackendUnavailable, err.Error()))
		return
	}
	payload, _ := json.Marshal(result)
	_ = write(Envelope{Type: "command_result", ID: env.ID, Payload: payload})
```

- [ ] **Step 4: Run commander tests**

Run:

```bash
go test ./internal/commander -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/commander/wsclient.go internal/commander/wsclient_test.go
git commit -m "feat(commander): dispatch workspace file commands"
```

---

### Task 5: Track Daemon Capabilities and Last Seen in the Hub

**Files:**
- Modify: `internal/commanderhub/registry.go`
- Modify: `internal/commanderhub/hub.go`
- Test: `internal/commanderhub/hub_test.go`
- Test: `internal/commanderhub/registry_test.go`

- [ ] **Step 1: Add failing tests**

Add this test to `internal/commanderhub/registry_test.go`:

```go
func TestRegistryDaemonInfoIncludesCapabilities(t *testing.T) {
	r := newRegistry()
	o := owner{userID: "alice", workspaceID: "W1"}
	r.add(&daemonConn{
		id:           "d1",
		owner:        o,
		displayName:  "prod-codex",
		kind:         "codex",
		capabilities: map[string]bool{commander.CapabilityFiles: true},
	})
	got := r.daemons(o)
	require.Len(t, got, 1)
	require.Contains(t, got[0].Capabilities, commander.CapabilityFiles)
}
```

Ensure `registry_test.go` imports:

```go
import "github.com/yourorg/multi-agent/internal/commander"
```

- [ ] **Step 2: Run registry test and verify it fails**

Run:

```bash
go test ./internal/commanderhub -run TestRegistryDaemonInfoIncludesCapabilities -count=1
```

Expected: FAIL with missing `capabilities` fields.

- [ ] **Step 3: Extend daemon info structs**

In `internal/commanderhub/registry.go`, extend `DaemonInfo`:

```go
Capabilities  []string `json:"capabilities,omitempty"`
LastSeenAt    string   `json:"last_seen_at,omitempty"`
SessionCount  int      `json:"session_count,omitempty"`
ActiveCount   int      `json:"active_count,omitempty"`
TurnCount     int      `json:"turn_count,omitempty"`
```

Add fields to `daemonConn`:

```go
metaMu       sync.Mutex
capabilities map[string]bool
lastSeenAt   time.Time
```

Update imports for `sort`, `sync`, and `time`.

Update `info()`:

```go
func (dc *daemonConn) info() DaemonInfo {
	dc.metaMu.Lock()
	defer dc.metaMu.Unlock()
	caps := make([]string, 0, len(dc.capabilities))
	for cap := range dc.capabilities {
		caps = append(caps, cap)
	}
	sort.Strings(caps)
	var lastSeen string
	if !dc.lastSeenAt.IsZero() {
		lastSeen = dc.lastSeenAt.UTC().Format(time.RFC3339Nano)
	}
	return DaemonInfo{
		DaemonID:      dc.id,
		DisplayName:   dc.displayName,
		Kind:          dc.kind,
		DriverVersion: dc.driverVersion,
		Capabilities:  caps,
		LastSeenAt:    lastSeen,
	}
}
```

- [ ] **Step 4: Parse capabilities during register**

In `internal/commanderhub/hub.go`, after `rp` is decoded:

```go
caps := map[string]bool{
	commander.CapabilitySessions: true,
	commander.CapabilityTurn:     true,
}
for _, cap := range rp.Capabilities {
	if cap != "" {
		caps[cap] = true
	}
}
dc.metaMu.Lock()
dc.capabilities = caps
dc.lastSeenAt = time.Now().UTC()
dc.metaMu.Unlock()
```

In `readLoop`, after a successful read:

```go
dc.metaMu.Lock()
dc.lastSeenAt = time.Now().UTC()
dc.metaMu.Unlock()
```

The `metaMu` lock above must be used for both reads and writes of `capabilities` and `lastSeenAt`.

- [ ] **Step 5: Run hub tests**

Run:

```bash
go test ./internal/commanderhub -run 'TestRegistryDaemonInfoIncludesCapabilities|TestHub_AcksRegisterAndAdmitsDaemon' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/commanderhub/registry.go internal/commanderhub/hub.go internal/commanderhub/hub_test.go internal/commanderhub/registry_test.go
git commit -m "feat(commanderhub): track daemon capabilities"
```

---

### Task 6: Add Observer Turn State and Concurrency Guard

**Files:**
- Create: `internal/commanderhub/turn_state.go`
- Create: `internal/commanderhub/turn_state_test.go`
- Modify: `internal/commanderhub/proxy.go`
- Modify: `internal/commanderhub/http.go`
- Test: `internal/commanderhub/http_test.go`

- [ ] **Step 1: Add failing turn state tests**

Create `internal/commanderhub/turn_state_test.go`:

```go
package commanderhub

import "testing"

func TestTurnStateStoreRejectsConcurrentTurn(t *testing.T) {
	s := newTurnStateStore()
	key := turnKey{owner: owner{"alice", "W1"}, daemonID: "d1", sessionID: "s1"}
	if !s.begin(key) {
		t.Fatal("first begin should succeed")
	}
	if s.begin(key) {
		t.Fatal("second begin should be rejected")
	}
	s.finish(key, turnStateDone)
	if !s.begin(key) {
		t.Fatal("begin after done should succeed")
	}
}

func TestTurnStateStoreSnapshot(t *testing.T) {
	s := newTurnStateStore()
	key := turnKey{owner: owner{"alice", "W1"}, daemonID: "d1", sessionID: "s1"}
	s.begin(key)
	s.set(key, turnStateAnswering)
	got := s.get(key)
	if got.State != turnStateAnswering || !got.InFlight {
		t.Fatalf("snapshot=%+v", got)
	}
}
```

- [ ] **Step 2: Run tests and verify they fail**

Run:

```bash
go test ./internal/commanderhub -run TestTurnStateStore -count=1
```

Expected: FAIL with undefined `newTurnStateStore`.

- [ ] **Step 3: Implement the store**

Create `internal/commanderhub/turn_state.go`:

```go
package commanderhub

import "sync"

type turnState string

const (
	turnStateIdle             turnState = "idle"
	turnStateQueued           turnState = "queued"
	turnStateStarting         turnState = "starting"
	turnStateAnswering        turnState = "answering"
	turnStateDone             turnState = "done"
	turnStateError            turnState = "error"
	turnStateAwaitingApproval turnState = "awaiting_approval"
	turnStateDisconnected     turnState = "disconnected"
)

type turnKey struct {
	owner     owner
	daemonID  string
	sessionID string
}

type turnSnapshot struct {
	State            turnState `json:"turn_state"`
	InFlight         bool      `json:"-"`
	AwaitingApproval bool      `json:"awaiting_approval"`
	ActiveWorker     bool      `json:"active_worker"`
	Message          string    `json:"turn_message,omitempty"`
}

type turnStateStore struct {
	mu sync.Mutex
	m  map[turnKey]turnSnapshot
}

func newTurnStateStore() *turnStateStore {
	return &turnStateStore{m: make(map[turnKey]turnSnapshot)}
}

func (s *turnStateStore) begin(key turnKey) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.m[key]
	if cur.InFlight {
		return false
	}
	s.m[key] = turnSnapshot{State: turnStateQueued, InFlight: true}
	return true
}

func (s *turnStateStore) set(key turnKey, state turnState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.m[key]
	cur.State = state
	cur.InFlight = state == turnStateQueued || state == turnStateStarting || state == turnStateAnswering
	s.m[key] = cur
}

func (s *turnStateStore) finish(key turnKey, state turnState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.m[key]
	cur.State = state
	cur.InFlight = false
	cur.AwaitingApproval = state == turnStateAwaitingApproval
	s.m[key] = cur
}

func (s *turnStateStore) fail(key turnKey, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.m[key]
	cur.State = turnStateError
	cur.InFlight = false
	cur.Message = msg
	s.m[key] = cur
}

func (s *turnStateStore) get(key turnKey) turnSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	if snap, ok := s.m[key]; ok {
		return snap
	}
	return turnSnapshot{State: turnStateIdle}
}
```

- [ ] **Step 4: Wire the store into `Hub`**

In `internal/commanderhub/hub.go`, add to `Hub`:

```go
turns *turnStateStore
```

Initialize it in `NewHub`:

```go
turns: newTurnStateStore(),
```

- [ ] **Step 5: Guard `POST .../turn`**

In `internal/commanderhub/http.go` `turn`, after owner lookup and body decode:

```go
key := turnKey{owner: o, daemonID: daemonID, sessionID: sid}
if !ch.hub.turns.begin(key) {
	http.Error(w, "turn already in flight", http.StatusConflict)
	return
}
defer func() {
	if r.Context().Err() != nil {
		ch.hub.turns.fail(key, r.Context().Err().Error())
	}
}()
```

While forwarding SSE frames, parse event/status/done/error and update store:

```go
switch env.Type {
case "event":
	var ep commander.EventPayload
	_ = json.Unmarshal(env.Payload, &ep)
	switch ep.EventKind {
	case "status":
		switch ep.Text {
		case "queued on daemon", "accepted by daemon":
			ch.hub.turns.set(key, turnStateQueued)
		case "starting codex":
			ch.hub.turns.set(key, turnStateStarting)
		case "codex running":
			ch.hub.turns.set(key, turnStateAnswering)
		}
	case "chunk":
		ch.hub.turns.set(key, turnStateAnswering)
	}
case "command_result":
	if payloadAwaitingUser(env.Payload) {
		ch.hub.turns.finish(key, turnStateAwaitingApproval)
	} else {
		ch.hub.turns.finish(key, turnStateDone)
	}
case "error":
	ch.hub.turns.fail(key, errorMessage(env.Payload))
}
```

Add helper:

```go
func payloadAwaitingUser(payload []byte) bool {
	var body struct {
		Result struct {
			AwaitingUser any `json:"awaiting_user"`
		} `json:"result"`
	}
	_ = json.Unmarshal(payload, &body)
	return body.Result.AwaitingUser != nil
}
```

- [ ] **Step 6: Add HTTP 409 test**

Add a test to `internal/commanderhub/http_test.go` that starts one blocking turn and verifies a second POST returns 409:

```go
func TestHTTP_TurnRejectsConcurrentSameSession(t *testing.T) {
	block := make(chan struct{})
	started := make(chan struct{})
	resolver := &fakeResolver{mu: map[string]identity.Identity{"tok-alice": {UserID: "alice", WorkspaceID: "W1"}}}
	srv, hub, _, o, cookie, cleanup := commanderSetup(t, resolver, "tok-alice", &tbBackend{
		resumeFn: func(ctx context.Context, _, _ string, sink executor.Sink) (executor.Result, error) {
			close(started)
			select {
			case <-block:
				return executor.Result{Summary: "done"}, nil
			case <-ctx.Done():
				return executor.Result{}, ctx.Err()
			}
		},
	})
	defer cleanup()
	defer close(block)
	daemonID := hub.reg.daemons(o)[0].DaemonID
	url := srv.URL + "/api/commander/daemons/" + daemonID + "/sessions/s1/turn"
	req1, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(`{"prompt":"one"}`))
	req1.Header.Set("Content-Type", "application/json")
	req1.AddCookie(cookie)
	respCh := make(chan *http.Response, 1)
	go func() {
		resp, _ := http.DefaultClient.Do(req1)
		respCh <- resp
	}()
	<-started
	req2, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(`{"prompt":"two"}`))
	req2.Header.Set("Content-Type", "application/json")
	req2.AddCookie(cookie)
	resp2, err := http.DefaultClient.Do(req2)
	require.NoError(t, err)
	defer resp2.Body.Close()
	require.Equal(t, http.StatusConflict, resp2.StatusCode)
}
```

- [ ] **Step 7: Run commanderhub tests**

Run:

```bash
go test ./internal/commanderhub -run 'TestTurnStateStore|TestHTTP_TurnRejectsConcurrentSameSession|TestHTTP_TurnStreamsSSE' -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/commanderhub/turn_state.go internal/commanderhub/turn_state_test.go internal/commanderhub/hub.go internal/commanderhub/http.go internal/commanderhub/http_test.go
git commit -m "feat(commanderhub): track session turn state"
```

---

### Task 7: Build the Commander Tree View Model and Session Cache

**Files:**
- Create: `internal/commanderhub/tree.go`
- Create: `internal/commanderhub/tree_test.go`
- Modify: `internal/commanderhub/proxy.go`
- Modify: `internal/commanderhub/http.go`

- [ ] **Step 1: Add failing tree tests**

Create `internal/commanderhub/tree_test.go`:

```go
package commanderhub

import (
	"testing"
	"time"
)

func TestSessionTitleFallback(t *testing.T) {
	cases := []struct {
		name    string
		title   string
		preview string
		id      string
		want    string
	}{
		{name: "title", title: "first prompt", preview: "preview", id: "abcdef123456", want: "first prompt"},
		{name: "preview", preview: "recent answer", id: "abcdef123456", want: "recent answer"},
		{name: "id", id: "abcdef123456", want: "abcdef12"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sessionTitle(tc.title, tc.preview, tc.id)
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestSortSessionRowsNewestFirst(t *testing.T) {
	now := time.Now()
	rows := []SessionRow{
		{SessionID: "old", UpdatedAt: now.Add(-time.Hour)},
		{SessionID: "new", UpdatedAt: now},
	}
	sortSessionRows(rows)
	if rows[0].SessionID != "new" || rows[1].SessionID != "old" {
		t.Fatalf("rows=%+v", rows)
	}
}
```

- [ ] **Step 2: Run tests and verify they fail**

Run:

```bash
go test ./internal/commanderhub -run 'TestSessionTitleFallback|TestSortSessionRowsNewestFirst' -count=1
```

Expected: FAIL with undefined `sessionTitle` or `SessionRow`.

- [ ] **Step 3: Implement tree types and helpers**

Create `internal/commanderhub/tree.go`:

```go
package commanderhub

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

type CommanderTree struct {
	Daemons []DaemonTree `json:"daemons"`
}

type DaemonTree struct {
	DaemonInfo
	Status   string       `json:"status"`
	Error    string       `json:"error,omitempty"`
	Sessions []SessionRow `json:"sessions,omitempty"`
}

type SessionRow struct {
	DaemonID         string    `json:"daemon_id"`
	SessionID        string    `json:"session_id"`
	Kind             string    `json:"kind"`
	Title            string    `json:"title"`
	WorkingDir       string    `json:"working_dir,omitempty"`
	UpdatedAt        time.Time `json:"updated_at,omitempty"`
	MessageCount     int       `json:"message_count,omitempty"`
	Preview          string    `json:"preview,omitempty"`
	TurnState        string    `json:"turn_state"`
	ActiveWorker     bool      `json:"active_worker"`
	AwaitingApproval bool      `json:"awaiting_approval"`
}

type sessionListCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[cacheKey]sessionCacheEntry
}

type cacheKey struct {
	owner    owner
	daemonID string
}

type sessionCacheEntry struct {
	expires time.Time
	rows    []SessionRow
}

func newSessionListCache(ttl time.Duration) *sessionListCache {
	return &sessionListCache{ttl: ttl, entries: make(map[cacheKey]sessionCacheEntry)}
}

func sessionTitle(title, preview, id string) string {
	for _, s := range []string{title, preview} {
		s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
		if s != "" {
			return s
		}
	}
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func sessionRowFromBackend(daemonID string, sess agentbackend.Session, snap turnSnapshot) SessionRow {
	state := string(snap.State)
	if state == "" {
		state = string(turnStateIdle)
	}
	return SessionRow{
		DaemonID:         daemonID,
		SessionID:        sess.ID,
		Kind:             string(sess.Kind),
		Title:            sessionTitle(sess.Title, sess.Preview, sess.ID),
		WorkingDir:       sess.WorkingDir,
		UpdatedAt:        sess.UpdatedAt,
		MessageCount:     sess.MessageCount,
		Preview:          sess.Preview,
		TurnState:        state,
		ActiveWorker:     snap.ActiveWorker,
		AwaitingApproval: snap.AwaitingApproval,
	}
}

func sortSessionRows(rows []SessionRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].UpdatedAt.After(rows[j].UpdatedAt)
	})
}
```

- [ ] **Step 4: Add cache to `Hub`**

In `internal/commanderhub/hub.go`, add:

```go
sessionCache *sessionListCache
```

Initialize:

```go
sessionCache: newSessionListCache(10 * time.Second),
```

- [ ] **Step 5: Add tree builder methods**

In `tree.go`, add:

```go
func (h *Hub) CommanderTree(ctx context.Context, o owner) CommanderTree {
	infos := h.reg.daemons(o)
	out := CommanderTree{Daemons: make([]DaemonTree, 0, len(infos))}
	for _, info := range infos {
		row := DaemonTree{DaemonInfo: info, Status: "ok"}
		sessions, err := h.cachedSessionRows(ctx, o, info)
		if err != nil {
			row.Status = "error"
			row.Error = err.Error()
		} else {
			row.Sessions = sessions
			row.SessionCount = len(sessions)
			for _, s := range sessions {
				if s.ActiveWorker {
					row.ActiveCount++
				}
				if s.TurnState == string(turnStateQueued) || s.TurnState == string(turnStateStarting) || s.TurnState == string(turnStateAnswering) {
					row.TurnCount++
				}
			}
		}
		out.Daemons = append(out.Daemons, row)
	}
	return out
}
```

Also add `cachedSessionRows`, `invalidateDaemonSessions`, and `refreshSessionRows` by reusing `SendCommand(list_sessions)`:

```go
func (h *Hub) cachedSessionRows(ctx context.Context, o owner, info DaemonInfo) ([]SessionRow, error) {
	key := cacheKey{owner: o, daemonID: info.DaemonID}
	now := time.Now()
	h.sessionCache.mu.Lock()
	if ent, ok := h.sessionCache.entries[key]; ok && now.Before(ent.expires) {
		rows := append([]SessionRow(nil), ent.rows...)
		h.sessionCache.mu.Unlock()
		return rows, nil
	}
	h.sessionCache.mu.Unlock()
	rows, err := h.refreshSessionRows(ctx, o, info)
	if err != nil {
		return nil, err
	}
	h.sessionCache.mu.Lock()
	h.sessionCache.entries[key] = sessionCacheEntry{expires: now.Add(h.sessionCache.ttl), rows: append([]SessionRow(nil), rows...)}
	h.sessionCache.mu.Unlock()
	return rows, nil
}

func (h *Hub) invalidateDaemonSessions(o owner, daemonID string) {
	h.sessionCache.mu.Lock()
	delete(h.sessionCache.entries, cacheKey{owner: o, daemonID: daemonID})
	h.sessionCache.mu.Unlock()
}

func (h *Hub) refreshSessionRows(ctx context.Context, o owner, info DaemonInfo) ([]SessionRow, error) {
	payload, err := h.SendCommand(ctx, o, info.DaemonID, "list_sessions", nil)
	if err != nil {
		return nil, err
	}
	var body struct {
		Sessions []agentbackend.Session `json:"sessions"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		return nil, err
	}
	rows := make([]SessionRow, 0, len(body.Sessions))
	for _, sess := range body.Sessions {
		snap := h.turns.get(turnKey{owner: o, daemonID: info.DaemonID, sessionID: sess.ID})
		rows = append(rows, sessionRowFromBackend(info.DaemonID, sess, snap))
	}
	sortSessionRows(rows)
	return rows, nil
}
```

Add imports:

```go
import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)
```

For every session, merge:

```go
snap := h.turns.get(turnKey{owner: o, daemonID: info.DaemonID, sessionID: sess.ID})
rows = append(rows, sessionRowFromBackend(info.DaemonID, sess, snap))
```

- [ ] **Step 6: Add `/tree` route**

In `internal/commanderhub/http.go`, mount:

```go
mux.HandleFunc("/api/commander/tree", ch.tree)
```

Add handler:

```go
func (ch *commanderHandlers) tree(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	o, ok := ch.ownerOf(w, r)
	if !ok {
		return
	}
	writeJSON(w, ch.hub.CommanderTree(r.Context(), o))
}
```

- [ ] **Step 7: Add HTTP tree test**

Add to `internal/commanderhub/http_test.go`:

```go
func TestHTTP_TreeGroupsAndSortsSessions(t *testing.T) {
	now := time.Now()
	resolver := &fakeResolver{mu: map[string]identity.Identity{"tok-alice": {UserID: "alice", WorkspaceID: "W1"}}}
	srv, _, _, _, cookie, cleanup := commanderSetup(t, resolver, "tok-alice", &tbBackend{
		listFn: func(context.Context) ([]agentbackend.Session, error) {
			return []agentbackend.Session{
				{ID: "old-session", Kind: agentbackend.KindCodex, Title: "old prompt", UpdatedAt: now.Add(-time.Hour)},
				{ID: "new-session", Kind: agentbackend.KindCodex, Title: "new prompt", UpdatedAt: now},
			}, nil
		},
	})
	defer cleanup()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/commander/tree", nil)
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	require.Less(t, strings.Index(string(body), "new-session"), strings.Index(string(body), "old-session"))
	require.Contains(t, string(body), `"title":"new prompt"`)
}
```

- [ ] **Step 8: Run commanderhub tests**

Run:

```bash
go test ./internal/commanderhub -run 'TestSessionTitleFallback|TestSortSessionRowsNewestFirst|TestHTTP_TreeGroupsAndSortsSessions' -count=1
```

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/commanderhub/tree.go internal/commanderhub/tree_test.go internal/commanderhub/hub.go internal/commanderhub/http.go internal/commanderhub/http_test.go
git commit -m "feat(commanderhub): expose commander tree model"
```

---

### Task 8: Add Observer File Proxy Routes

**Files:**
- Modify: `internal/commanderhub/proxy.go`
- Modify: `internal/commanderhub/http.go`
- Test: `internal/commanderhub/http_test.go`

- [ ] **Step 1: Add failing HTTP file route test**

Add to `internal/commanderhub/http_test.go`:

```go
func TestHTTP_FileRoutesProxyToDaemon(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0644))
	resolver := &fakeResolver{mu: map[string]identity.Identity{"tok-alice": {UserID: "alice", WorkspaceID: "W1"}}}
	srv, hub, _, o, cookie, cleanup := commanderSetup(t, resolver, "tok-alice", &tbBackend{
		getFn: func(context.Context, string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
			return agentbackend.Session{ID: "s1", WorkingDir: root}, nil, nil
		},
	})
	defer cleanup()
	daemonID := hub.reg.daemons(o)[0].DaemonID
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/commander/daemons/"+daemonID+"/sessions/s1/files?path=.", nil)
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	require.Contains(t, string(body), "go.mod")

	req2, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/commander/daemons/"+daemonID+"/sessions/s1/files/content?path=go.mod", nil)
	req2.AddCookie(cookie)
	resp2, err := http.DefaultClient.Do(req2)
	require.NoError(t, err)
	defer resp2.Body.Close()
	require.Equal(t, http.StatusOK, resp2.StatusCode)
	body2, _ := io.ReadAll(resp2.Body)
	require.Contains(t, string(body2), "module x")
}
```

Add imports:

```go
import (
	"os"
	"path/filepath"
)
```

- [ ] **Step 2: Run test and verify it fails**

Run:

```bash
go test ./internal/commanderhub -run TestHTTP_FileRoutesProxyToDaemon -count=1
```

Expected: FAIL with 404 for the new file routes.

- [ ] **Step 3: Add proxy helper methods**

In `internal/commanderhub/proxy.go`, add:

```go
func (h *Hub) ListFiles(ctx context.Context, o owner, daemonID, sessionID, path string) (json.RawMessage, error) {
	args, _ := json.Marshal(commander.FileListArgs{ID: sessionID, Path: path})
	return h.SendCommand(ctx, o, daemonID, "list_files", args)
}

func (h *Hub) ReadFile(ctx context.Context, o owner, daemonID, sessionID, path string) (json.RawMessage, error) {
	args, _ := json.Marshal(commander.FileReadArgs{ID: sessionID, Path: path})
	return h.SendCommand(ctx, o, daemonID, "read_file", args)
}
```

- [ ] **Step 4: Route files in `daemonScoped`**

In `internal/commanderhub/http.go`, extend the `sessions/{sid}` branch:

```go
case tail == "files":
	ch.listFiles(w, r, id, sid)
case tail == "files/content":
	ch.readFile(w, r, id, sid)
```

Add handlers:

```go
func (ch *commanderHandlers) listFiles(w http.ResponseWriter, r *http.Request, daemonID, sid string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	o, ok := ch.ownerOf(w, r)
	if !ok {
		return
	}
	payload, err := ch.hub.ListFiles(r.Context(), o, daemonID, sid, r.URL.Query().Get("path"))
	if err != nil {
		writeSendCmdError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(payload)
}

func (ch *commanderHandlers) readFile(w http.ResponseWriter, r *http.Request, daemonID, sid string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	o, ok := ch.ownerOf(w, r)
	if !ok {
		return
	}
	payload, err := ch.hub.ReadFile(r.Context(), o, daemonID, sid, r.URL.Query().Get("path"))
	if err != nil {
		writeSendCmdError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(payload)
}
```

- [ ] **Step 5: Run file route test**

Run:

```bash
go test ./internal/commanderhub -run TestHTTP_FileRoutesProxyToDaemon -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/commanderhub/proxy.go internal/commanderhub/http.go internal/commanderhub/http_test.go
git commit -m "feat(commanderhub): proxy session file routes"
```

---

### Task 9: Scaffold React/Vite Commander Webapp

**Files:**
- Create: `internal/commanderhub/webapp/package.json`
- Create: `internal/commanderhub/webapp/package-lock.json`
- Create: `internal/commanderhub/webapp/index.html`
- Create: `internal/commanderhub/webapp/vite.config.ts`
- Create: `internal/commanderhub/webapp/tsconfig.json`
- Create: `internal/commanderhub/webapp/src/main.tsx`
- Create: `internal/commanderhub/webapp/src/CommanderApp.tsx`
- Create: `internal/commanderhub/webapp/src/api/types.ts`
- Create: `internal/commanderhub/webapp/src/api/client.ts`
- Create: `internal/commanderhub/webapp/src/styles.css`

- [ ] **Step 1: Create package manifest**

In `internal/commanderhub/webapp/package.json`:

```json
{
  "name": "commander-webapp",
  "private": true,
  "version": "0.0.0",
  "type": "module",
  "scripts": {
    "dev": "vite",
    "build": "tsc -b && vite build",
    "test": "vitest run",
    "e2e": "playwright test",
    "preview": "vite preview --host 127.0.0.1"
  },
  "dependencies": {},
  "devDependencies": {}
}
```

- [ ] **Step 2: Install dependencies**

Run:

```bash
cd internal/commanderhub/webapp
npm install
npm install react react-dom lucide-react react-markdown remark-gfm rehype-sanitize
npm install -D @vitejs/plugin-react vite typescript vitest jsdom @testing-library/react @testing-library/jest-dom @types/react @types/react-dom @playwright/test
```

Expected: `package-lock.json` is created and `npm` exits 0.

- [ ] **Step 3: Add Vite config**

Create `vite.config.ts`:

```ts
import react from '@vitejs/plugin-react';
import { defineConfig } from 'vite';

export default defineConfig({
  base: '/commander/',
  plugins: [react()],
  build: {
    outDir: '../assets/dist',
    emptyOutDir: true,
  },
  test: {
    environment: 'jsdom',
    setupFiles: './src/test/setup.ts',
  },
});
```

Create `tsconfig.json`:

```json
{
  "compilerOptions": {
    "target": "ES2022",
    "useDefineForClassFields": true,
    "lib": ["ES2022", "DOM", "DOM.Iterable"],
    "allowJs": false,
    "skipLibCheck": true,
    "esModuleInterop": true,
    "allowSyntheticDefaultImports": true,
    "strict": true,
    "forceConsistentCasingInFileNames": true,
    "module": "ESNext",
    "moduleResolution": "Node",
    "resolveJsonModule": true,
    "isolatedModules": true,
    "noEmit": true,
    "jsx": "react-jsx"
  },
  "include": ["src"],
  "references": []
}
```

- [ ] **Step 4: Add minimal app and API types**

Create `src/api/types.ts`:

```ts
export type TurnState = 'idle' | 'queued' | 'starting' | 'answering' | 'done' | 'error' | 'awaiting_approval' | 'disconnected';

export interface SessionRow {
  daemon_id: string;
  session_id: string;
  kind: string;
  title: string;
  working_dir?: string;
  updated_at?: string;
  message_count?: number;
  preview?: string;
  turn_state: TurnState;
  active_worker: boolean;
  awaiting_approval: boolean;
}

export interface DaemonTree {
  daemon_id: string;
  display_name: string;
  kind: string;
  driver_version?: string;
  capabilities?: string[];
  status: string;
  error?: string;
  sessions?: SessionRow[];
}

export interface CommanderTree {
  daemons: DaemonTree[];
}

export interface SessionMessage {
  Role?: string;
  role?: string;
  Text?: string;
  text?: string;
  Ts?: string;
  ts?: string;
}
```

Create `src/api/client.ts`:

```ts
export async function apiGet<T>(path: string): Promise<T> {
  const res = await fetch(path, { credentials: 'include' });
  if (res.status === 401) throw new Error('unauthorized');
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return (await res.json()) as T;
}
```

Create `src/main.tsx`:

```tsx
import React from 'react';
import { createRoot } from 'react-dom/client';
import { CommanderApp } from './CommanderApp';
import './styles.css';

createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <CommanderApp />
  </React.StrictMode>,
);
```

Create `src/CommanderApp.tsx`:

```tsx
import { useEffect, useState } from 'react';
import { apiGet } from './api/client';
import type { CommanderTree } from './api/types';

export function CommanderApp() {
  const [tree, setTree] = useState<CommanderTree | null>(null);
  const [error, setError] = useState<string>('');

  useEffect(() => {
    apiGet<CommanderTree>('/api/commander/tree')
      .then(setTree)
      .catch((err: Error) => setError(err.message));
  }, []);

  if (error === 'unauthorized') return <div className="login-shell">用 agentserver 登录</div>;
  if (error) return <div className="login-shell">加载失败: {error}</div>;
  if (!tree) return <div className="login-shell">加载中</div>;
  return <div className="commander-shell">{tree.daemons.length} daemons</div>;
}
```

Create `index.html`:

```html
<!doctype html>
<html lang="zh-CN">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>Commander</title>
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/src/main.tsx"></script>
  </body>
</html>
```

Create `src/styles.css` with the base shell:

```css
:root {
  color: #1f2937;
  background: #f5f7fa;
  font-family: Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
}

* { box-sizing: border-box; }
body { margin: 0; min-width: 320px; min-height: 100vh; }
button, textarea { font: inherit; }

.login-shell {
  min-height: 100vh;
  display: grid;
  place-items: center;
  color: #5b6678;
}

.commander-shell {
  height: 100vh;
  display: grid;
  grid-template-columns: minmax(280px, 360px) minmax(420px, 1fr) minmax(280px, 380px);
  background: #f5f7fa;
}
```

- [ ] **Step 5: Add test setup**

Create `src/test/setup.ts`:

```ts
import '@testing-library/jest-dom/vitest';
```

- [ ] **Step 6: Run frontend build and tests**

Run:

```bash
cd internal/commanderhub/webapp
npm test
npm run build
```

Expected: both commands exit 0 and `internal/commanderhub/assets/dist` is generated.

- [ ] **Step 7: Commit**

```bash
git add internal/commanderhub/webapp internal/commanderhub/assets/dist
git commit -m "feat(commanderhub): scaffold react commander app"
```

---

### Task 10: Implement React Tree, Chat, Status, and File Components

**Files:**
- Create: `internal/commanderhub/webapp/src/components/DaemonSessionTree.tsx`
- Create: `internal/commanderhub/webapp/src/components/ChatWorkspace.tsx`
- Create: `internal/commanderhub/webapp/src/components/MessageRenderer.tsx`
- Create: `internal/commanderhub/webapp/src/components/FileExplorerPanel.tsx`
- Create: `internal/commanderhub/webapp/src/components/StatusBadge.tsx`
- Modify: `internal/commanderhub/webapp/src/CommanderApp.tsx`
- Modify: `internal/commanderhub/webapp/src/api/types.ts`
- Modify: `internal/commanderhub/webapp/src/api/client.ts`
- Modify: `internal/commanderhub/webapp/src/styles.css`
- Test: `internal/commanderhub/webapp/src/components/*.test.tsx`

- [ ] **Step 1: Add status-outside-message test**

Create `src/components/ChatWorkspace.test.tsx`:

```tsx
import { render, screen } from '@testing-library/react';
import { ChatWorkspace } from './ChatWorkspace';

test('renders turn state outside assistant message body', () => {
  render(
    <ChatWorkspace
      daemonID="d1"
      sessionID="s1"
      session={{
        session: { ID: 's1', Title: 'Fix cache', WorkingDir: '/repo' },
        messages: [{ Role: 'assistant', Text: 'real codex answer' }],
      }}
      turnState="answering"
      onSend={async () => {}}
    />,
  );
  expect(screen.getByTestId('turn-status')).toHaveTextContent('Codex 正在回答');
  expect(screen.getByTestId('message-list')).toHaveTextContent('real codex answer');
  expect(screen.getByTestId('message-list')).not.toHaveTextContent('Codex 正在回答');
});
```

- [ ] **Step 2: Add tree grouping test**

Create `src/components/DaemonSessionTree.test.tsx`:

```tsx
import { render, screen } from '@testing-library/react';
import { DaemonSessionTree } from './DaemonSessionTree';

test('groups sessions under daemon rows', () => {
  render(
    <DaemonSessionTree
      daemons={[{
        daemon_id: 'd1',
        display_name: 'prod-codex',
        kind: 'codex',
        status: 'ok',
        sessions: [{
          daemon_id: 'd1',
          session_id: 's1',
          kind: 'codex',
          title: 'Fix session cache',
          turn_state: 'answering',
          active_worker: false,
          awaiting_approval: false,
        }],
      }]}
      selected={{ daemonID: 'd1', sessionID: 's1' }}
      onSelect={() => {}}
    />,
  );
  expect(screen.getByText('prod-codex')).toBeInTheDocument();
  expect(screen.getByText('Fix session cache')).toBeInTheDocument();
  expect(screen.getByText('answering')).toBeInTheDocument();
});
```

- [ ] **Step 3: Add file preview state test**

Create `src/components/FileExplorerPanel.test.tsx`:

```tsx
import { render, screen } from '@testing-library/react';
import { FilePreview } from './FileExplorerPanel';

test('shows too-large file metadata instead of content', () => {
  render(<FilePreview preview={{ path: 'large.log', size: 3_000_000, too_large: true }} />);
  expect(screen.getByText('large.log')).toBeInTheDocument();
  expect(screen.getByText(/2MB/)).toBeInTheDocument();
});
```

- [ ] **Step 4: Run component tests and verify they fail**

Run:

```bash
cd internal/commanderhub/webapp
npm test
```

Expected: FAIL with missing components.

- [ ] **Step 5: Implement components**

Create `StatusBadge.tsx`:

```tsx
export function StatusBadge({ state }: { state: string }) {
  return <span className={`status-badge status-${state}`}>{state}</span>;
}
```

Create `DaemonSessionTree.tsx`:

```tsx
import type { DaemonTree } from '../api/types';
import { StatusBadge } from './StatusBadge';

export function DaemonSessionTree({
  daemons,
  selected,
  onSelect,
}: {
  daemons: DaemonTree[];
  selected: { daemonID: string; sessionID: string } | null;
  onSelect: (daemonID: string, sessionID: string) => void;
}) {
  return (
    <aside className="daemon-tree">
      {daemons.map((daemon) => (
        <section className="daemon-group" key={daemon.daemon_id}>
          <div className="daemon-row">
            <span className="online-dot" />
            <strong>{daemon.display_name || daemon.daemon_id}</strong>
            <span>{daemon.kind}</span>
          </div>
          <div className="session-list">
            {(daemon.sessions || []).map((session) => (
              <button
                key={session.session_id}
                className={selected?.daemonID === daemon.daemon_id && selected.sessionID === session.session_id ? 'session-row selected' : 'session-row'}
                onClick={() => onSelect(daemon.daemon_id, session.session_id)}
              >
                <span className="session-title">{session.title}</span>
                <span className="session-meta">{session.working_dir || ''}</span>
                <StatusBadge state={session.turn_state} />
              </button>
            ))}
          </div>
        </section>
      ))}
    </aside>
  );
}
```

Create `MessageRenderer.tsx`:

```tsx
import ReactMarkdown from 'react-markdown';
import rehypeSanitize from 'rehype-sanitize';
import remarkGfm from 'remark-gfm';

export function MessageRenderer({ text }: { text: string }) {
  return (
    <div className="message-markdown">
      <ReactMarkdown remarkPlugins={[remarkGfm]} rehypePlugins={[rehypeSanitize]}>
        {text}
      </ReactMarkdown>
    </div>
  );
}
```

Create `ChatWorkspace.tsx` with props used by the test:

```tsx
import { MessageRenderer } from './MessageRenderer';
import type { SessionMessage, TurnState } from '../api/types';

export interface SessionDetail {
  session: { ID?: string; Title?: string; WorkingDir?: string; id?: string; title?: string; working_dir?: string };
  messages: SessionMessage[];
}

function displayTurnState(state: TurnState | string) {
  if (state === 'starting') return '正在启动 Codex';
  if (state === 'answering') return 'Codex 正在回答';
  if (state === 'awaiting_approval') return '需人工审批';
  if (state === 'done') return '已回答完毕';
  if (state === 'error') return '出错';
  return '';
}

export function ChatWorkspace({
  session,
  turnState,
  onSend,
}: {
  daemonID: string;
  sessionID: string;
  session: SessionDetail | null;
  turnState: TurnState | string;
  onSend: (prompt: string) => Promise<void>;
}) {
  const title = session?.session.Title || session?.session.title || 'Session';
  const cwd = session?.session.WorkingDir || session?.session.working_dir || '';
  const disabled = ['queued', 'starting', 'answering', 'awaiting_approval'].includes(turnState);
  return (
    <main className="chat-workspace">
      <header className="chat-header">
        <div>
          <h1>{title}</h1>
          <p>{cwd}</p>
        </div>
        <span data-testid="turn-status" className="turn-status">{displayTurnState(turnState)}</span>
      </header>
      <div data-testid="message-list" className="message-list">
        {(session?.messages || []).map((msg, index) => {
          const role = msg.Role || msg.role || 'assistant';
          const text = msg.Text || msg.text || '';
          return (
            <article key={index} className={`message message-${role}`}>
              <MessageRenderer text={text} />
            </article>
          );
        })}
      </div>
      <form className="composer" onSubmit={(event) => {
        event.preventDefault();
        const form = event.currentTarget;
        const input = form.elements.namedItem('prompt') as HTMLTextAreaElement;
        void onSend(input.value);
        input.value = '';
      }}>
        <textarea name="prompt" disabled={disabled} />
        <button type="submit" disabled={disabled}>发送</button>
      </form>
    </main>
  );
}
```

Create `FileExplorerPanel.tsx`:

```tsx
export interface FilePreviewData {
  path: string;
  size: number;
  mime?: string;
  binary?: boolean;
  too_large?: boolean;
  content?: string;
}

export function FilePreview({ preview }: { preview: FilePreviewData | null }) {
  if (!preview) return <div className="file-preview-empty">No file selected</div>;
  if (preview.too_large) {
    return <div className="file-preview"><strong>{preview.path}</strong><p>文件超过 2MB, 不预览。</p></div>;
  }
  if (preview.binary) {
    return <div className="file-preview"><strong>{preview.path}</strong><p>二进制文件 · {preview.size} bytes</p></div>;
  }
  return <pre className="file-preview"><code>{preview.content || ''}</code></pre>;
}

export function FileExplorerPanel() {
  return <aside className="file-panel"><FilePreview preview={null} /></aside>;
}
```

- [ ] **Step 6: Wire components in `CommanderApp`**

Replace the app shell with stateful selection:

```tsx
const [selected, setSelected] = useState<{ daemonID: string; sessionID: string } | null>(null);
```

Render:

```tsx
return (
  <div className="commander-shell">
    <DaemonSessionTree daemons={tree.daemons} selected={selected} onSelect={(daemonID, sessionID) => setSelected({ daemonID, sessionID })} />
    <ChatWorkspace daemonID={selected?.daemonID || ''} sessionID={selected?.sessionID || ''} session={null} turnState="idle" onSend={async () => {}} />
    <FileExplorerPanel />
  </div>
);
```

- [ ] **Step 7: Add CSS for three-pane layout and overflow safety**

Append to `styles.css`:

```css
.daemon-tree, .file-panel {
  min-width: 0;
  overflow: auto;
  border-right: 1px solid #d9e1ec;
  background: #fbfcfe;
}
.file-panel { border-right: 0; border-left: 1px solid #d9e1ec; }
.daemon-group { padding: 10px; }
.daemon-row { display: flex; align-items: center; gap: 8px; height: 32px; color: #253348; }
.online-dot { width: 8px; height: 8px; border-radius: 50%; background: #259566; }
.session-list { display: grid; gap: 6px; }
.session-row {
  display: grid;
  gap: 4px;
  width: 100%;
  text-align: left;
  border: 1px solid #dce4ef;
  border-radius: 8px;
  background: #fff;
  padding: 8px;
  color: #26364d;
}
.session-row.selected { border-color: #1e7894; background: #eef8fb; }
.session-title, .session-meta { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.session-title { font-weight: 650; }
.session-meta { color: #69768a; font-size: 12px; }
.status-badge { width: fit-content; border-radius: 999px; padding: 2px 7px; font-size: 11px; background: #e8edf4; color: #506074; }
.status-answering { background: #e4f2fb; color: #176c9c; }
.status-awaiting_approval { background: #fff2da; color: #8d5b12; }
.status-error { background: #fde8e8; color: #a33b3b; }
.chat-workspace { min-width: 0; display: grid; grid-template-rows: auto 1fr auto; background: #f7f9fc; }
.chat-header { min-width: 0; display: flex; justify-content: space-between; gap: 16px; padding: 14px 18px; border-bottom: 1px solid #d9e1ec; background: #fff; }
.chat-header h1 { margin: 0; font-size: 16px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.chat-header p { margin: 3px 0 0; color: #69768a; font-size: 12px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.turn-status { color: #176c9c; font-size: 13px; white-space: nowrap; }
.message-list { overflow: auto; padding: 18px; display: grid; align-content: start; gap: 12px; }
.message { max-width: min(760px, 88%); border: 1px solid #dfe6ef; border-radius: 10px; background: #fff; padding: 10px 12px; }
.message-user { justify-self: end; background: #f1f6fb; }
.message-markdown pre, .file-preview { overflow: auto; }
.composer { display: grid; grid-template-columns: 1fr auto; gap: 10px; padding: 12px 18px; border-top: 1px solid #d9e1ec; background: #fff; }
.composer textarea { min-height: 42px; resize: vertical; border: 1px solid #cbd5e1; border-radius: 8px; padding: 9px; }
.composer button { border: 0; border-radius: 8px; padding: 0 14px; color: #fff; background: #1e7894; }
.composer button:disabled, .composer textarea:disabled { opacity: .55; cursor: not-allowed; }
@media (max-width: 900px) {
  .commander-shell { grid-template-columns: 1fr; }
  .daemon-tree, .file-panel { display: none; }
}
```

- [ ] **Step 8: Run frontend tests and build**

Run:

```bash
cd internal/commanderhub/webapp
npm test
npm run build
```

Expected: PASS and build exits 0.

- [ ] **Step 9: Commit**

```bash
git add internal/commanderhub/webapp internal/commanderhub/assets/dist
git commit -m "feat(commanderhub): render commander workbench shell"
```

---

### Task 11: Wire Real Session Detail, SSE Turn Flow, and File Loading in React

**Files:**
- Modify: `internal/commanderhub/webapp/src/api/client.ts`
- Modify: `internal/commanderhub/webapp/src/api/types.ts`
- Modify: `internal/commanderhub/webapp/src/CommanderApp.tsx`
- Modify: `internal/commanderhub/webapp/src/components/ChatWorkspace.tsx`
- Modify: `internal/commanderhub/webapp/src/components/FileExplorerPanel.tsx`
- Test: `internal/commanderhub/webapp/src/api/client.test.ts`
- Test: `internal/commanderhub/webapp/src/components/ChatWorkspace.test.tsx`

- [ ] **Step 1: Add SSE parser test**

Create `src/api/client.test.ts`:

```ts
import { parseSSEBlock } from './client';

test('parses status and chunk blocks', () => {
  expect(parseSSEBlock('event: status\ndata: {"text":"codex running"}')).toEqual({ event: 'status', data: { text: 'codex running' } });
  expect(parseSSEBlock('event: chunk\ndata: {"text":"hi"}')).toEqual({ event: 'chunk', data: { text: 'hi' } });
});
```

- [ ] **Step 2: Run test and verify it fails**

Run:

```bash
cd internal/commanderhub/webapp
npm test -- --run src/api/client.test.ts
```

Expected: FAIL with undefined `parseSSEBlock`.

- [ ] **Step 3: Implement API client functions**

In `src/api/client.ts`, add:

```ts
export function parseSSEBlock(block: string): { event: string; data: unknown } {
  let event = 'message';
  let data = {};
  for (const line of block.split('\n')) {
    if (line.startsWith('event: ')) event = line.slice(7);
    if (line.startsWith('data: ')) data = JSON.parse(line.slice(6));
  }
  return { event, data };
}

export async function postTurn(
  daemonID: string,
  sessionID: string,
  prompt: string,
  onEvent: (event: string, data: any) => void,
) {
  const res = await fetch(`/api/commander/daemons/${encodeURIComponent(daemonID)}/sessions/${encodeURIComponent(sessionID)}/turn`, {
    method: 'POST',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ prompt }),
  });
  if (res.status === 409) throw new Error('turn already in flight');
  if (!res.ok || !res.body) throw new Error(`HTTP ${res.status}`);
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    let idx = buffer.indexOf('\n\n');
    while (idx >= 0) {
      const block = buffer.slice(0, idx);
      buffer = buffer.slice(idx + 2);
      const parsed = parseSSEBlock(block);
      onEvent(parsed.event, parsed.data);
      idx = buffer.indexOf('\n\n');
    }
  }
}
```

Add detail/file helpers:

```ts
export const sessionPath = (daemonID: string, sessionID: string) =>
  `/api/commander/daemons/${encodeURIComponent(daemonID)}/sessions/${encodeURIComponent(sessionID)}`;

export const filesPath = (daemonID: string, sessionID: string, path: string) =>
  `${sessionPath(daemonID, sessionID)}/files?path=${encodeURIComponent(path)}`;

export const fileContentPath = (daemonID: string, sessionID: string, path: string) =>
  `${sessionPath(daemonID, sessionID)}/files/content?path=${encodeURIComponent(path)}`;
```

- [ ] **Step 4: Extend types**

In `src/api/types.ts`, add:

```ts
export interface SessionDetail {
  session: Record<string, unknown>;
  messages: SessionMessage[];
}

export interface FileEntry {
  name: string;
  path: string;
  kind: 'file' | 'dir';
  size?: number;
  mod_time?: string;
}

export interface FileListResult {
  root: string;
  path: string;
  entries: FileEntry[];
}

export interface FileReadResult {
  path: string;
  size: number;
  mime?: string;
  binary?: boolean;
  too_large?: boolean;
  content?: string;
}
```

- [ ] **Step 5: Load session detail on selection**

In `CommanderApp.tsx`, add state:

```tsx
const [sessionDetail, setSessionDetail] = useState<SessionDetail | null>(null);
const [turnState, setTurnState] = useState<TurnState>('idle');
```

Add effect:

```tsx
useEffect(() => {
  if (!selected) return;
  apiGet<SessionDetail>(sessionPath(selected.daemonID, selected.sessionID))
    .then(setSessionDetail)
    .catch((err: Error) => setError(err.message));
}, [selected]);
```

Handle send:

```tsx
async function sendPrompt(prompt: string) {
  if (!selected || !prompt.trim()) return;
  setTurnState('queued');
  await postTurn(selected.daemonID, selected.sessionID, prompt, (event, data) => {
    if (event === 'status') {
      const text = String((data as { text?: string }).text || '');
      if (text === 'starting codex') setTurnState('starting');
      else if (text === 'codex running') setTurnState('answering');
      else setTurnState('queued');
    }
    if (event === 'chunk') setTurnState('answering');
    if (event === 'done') {
      const awaiting = Boolean((data as { result?: { awaiting_user?: unknown } }).result?.awaiting_user);
      setTurnState(awaiting ? 'awaiting_approval' : 'done');
    }
    if (event === 'error') setTurnState('error');
  });
}
```

- [ ] **Step 6: Load file tree and preview**

In `FileExplorerPanel.tsx`, replace the static panel with props:

```tsx
export function FileExplorerPanel({ daemonID, sessionID }: { daemonID: string; sessionID: string }) {
  const [entries, setEntries] = useState<FileEntry[]>([]);
  const [preview, setPreview] = useState<FileReadResult | null>(null);
  useEffect(() => {
    if (!daemonID || !sessionID) return;
    apiGet<FileListResult>(filesPath(daemonID, sessionID, '.')).then((res) => setEntries(res.entries));
  }, [daemonID, sessionID]);
  return (
    <aside className="file-panel">
      <div className="file-list">
        {entries.map((entry) => (
          <button key={entry.path} className="file-row" onClick={() => {
            if (entry.kind === 'file') apiGet<FileReadResult>(fileContentPath(daemonID, sessionID, entry.path)).then(setPreview);
          }}>
            {entry.kind === 'dir' ? '▸' : ' '} {entry.name}
          </button>
        ))}
      </div>
      <FilePreview preview={preview} />
    </aside>
  );
}
```

- [ ] **Step 7: Run frontend tests and build**

Run:

```bash
cd internal/commanderhub/webapp
npm test
npm run build
```

Expected: PASS and build exits 0.

- [ ] **Step 8: Commit**

```bash
git add internal/commanderhub/webapp internal/commanderhub/assets/dist
git commit -m "feat(commanderhub): wire commander app data flows"
```

---

### Task 12: Serve Vite Build Output from Go Embed

**Files:**
- Modify: `internal/commanderhub/web.go`
- Modify: `internal/commanderhub/web_test.go`
- Delete: `internal/commanderhub/assets/app.js`
- Delete: `internal/commanderhub/assets/style.css`
- Remove: `internal/commanderhub/assets/index.html`

- [ ] **Step 1: Add failing asset serving test**

Modify `TestWeb_CommanderPageAndAssets` in `internal/commanderhub/web_test.go` so it checks `/commander` contains the Vite root and that an asset under `/commander/assets/` can be served:

```go
require.Contains(t, string(body[:n]), `id="root"`)
```

Replace the old asset list loop with a check that discovers one built asset from `assets/dist/assets` and requests it through `/commander/assets/<name>`.

- [ ] **Step 2: Run web test and verify it fails before `web.go` changes**

Run:

```bash
go test ./internal/commanderhub -run TestWeb_CommanderPageAndAssets -count=1
```

Expected: FAIL if `web.go` still serves old `app.js` / `style.css` paths.

- [ ] **Step 3: Change embed to `assets/dist`**

In `internal/commanderhub/web.go`:

```go
//go:embed assets/dist/*
//go:embed assets/dist/assets/*
var assetsFS embed.FS
```

Serve `/commander` from `assets/dist/index.html`:

```go
data, err := assetsFS.ReadFile("assets/dist/index.html")
```

Serve built assets:

```go
sub, _ := fs.Sub(assetsFS, "assets/dist")
fileServer := http.StripPrefix("/commander/", http.FileServer(http.FS(sub)))
mux.Handle("/commander/assets/", fileServer)
```

- [ ] **Step 4: Remove old vanilla assets**

Delete:

```bash
git rm internal/commanderhub/assets/app.js internal/commanderhub/assets/style.css
```

Keep only built Vite output under `internal/commanderhub/assets/dist`.

- [ ] **Step 5: Run Go and frontend verification**

Run:

```bash
cd internal/commanderhub/webapp
npm run build
cd ../../..
go test ./internal/commanderhub -run 'TestWeb_CommanderPageAndAssets|TestHTTP_TreeGroupsAndSortsSessions' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/commanderhub/web.go internal/commanderhub/web_test.go internal/commanderhub/assets internal/commanderhub/webapp
git commit -m "feat(commanderhub): serve vite commander app"
```

---

### Task 13: Add Playwright Layout and Screenshot Coverage

**Files:**
- Create: `internal/commanderhub/webapp/playwright.config.ts`
- Create: `internal/commanderhub/webapp/src/e2e/commander.spec.ts`
- Modify: `internal/commanderhub/webapp/src/CommanderApp.tsx`
- Modify: `internal/commanderhub/webapp/src/components/DaemonSessionTree.tsx`
- Modify: `internal/commanderhub/webapp/src/components/ChatWorkspace.tsx`
- Modify: `internal/commanderhub/webapp/src/components/FileExplorerPanel.tsx`

- [ ] **Step 1: Add stable test ids to the app shell**

Add these attributes in the React components:

```tsx
<div className="commander-shell" data-testid="commander-shell">
```

```tsx
<aside className="daemon-tree" data-testid="daemon-tree">
```

```tsx
<main className="chat-workspace" data-testid="chat-workspace">
```

```tsx
<aside className="file-panel" data-testid="file-panel">
```

- [ ] **Step 2: Add Playwright config**

Create `internal/commanderhub/webapp/playwright.config.ts`:

```ts
import { defineConfig, devices } from '@playwright/test';

export default defineConfig({
  testDir: './src/e2e',
  timeout: 30_000,
  expect: { timeout: 5_000 },
  use: {
    baseURL: 'http://127.0.0.1:4173',
    trace: 'on-first-retry',
  },
  webServer: {
    command: 'npm run build && npm run preview -- --port 4173',
    url: 'http://127.0.0.1:4173/commander/',
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
  projects: [
    { name: 'chromium-desktop', use: { ...devices['Desktop Chrome'], viewport: { width: 1440, height: 960 } } },
    { name: 'chromium-mobile', use: { ...devices['Pixel 7'] } },
  ],
});
```

- [ ] **Step 3: Add visual E2E test**

Create `internal/commanderhub/webapp/src/e2e/commander.spec.ts`:

```ts
import { expect, test } from '@playwright/test';

const treePayload = {
  daemons: [{
    daemon_id: 'd1',
    display_name: 'prod-codex',
    kind: 'codex',
    driver_version: 'v0.1.0',
    status: 'ok',
    sessions: [{
      daemon_id: 'd1',
      session_id: 's1',
      kind: 'codex',
      title: 'Fix commander session cache latency with a long title that must not overflow',
      working_dir: '/root/multi-agent/multi-agent/tests/prod_test/driver-codex',
      updated_at: '2026-06-16T12:00:00Z',
      message_count: 18,
      preview: 'I will add cache invalidation.',
      turn_state: 'answering',
      active_worker: false,
      awaiting_approval: false,
    }],
  }],
};

test.beforeEach(async ({ page }) => {
  await page.route('**/api/commander/tree', async (route) => {
    await route.fulfill({ json: treePayload });
  });
  await page.route('**/api/commander/daemons/d1/sessions/s1', async (route) => {
    await route.fulfill({
      json: {
        session: { ID: 's1', Title: treePayload.daemons[0].sessions[0].title, WorkingDir: treePayload.daemons[0].sessions[0].working_dir },
        messages: [
          { Role: 'user', Text: '为什么每次 list session 都这么卡？' },
          { Role: 'assistant', Text: '```go\nfunc cache() {}\n```' },
        ],
      },
    });
  });
  await page.route('**/api/commander/daemons/d1/sessions/s1/files?path=.', async (route) => {
    await route.fulfill({ json: { root: '/root/project', path: '.', entries: [{ name: 'go.mod', path: 'go.mod', kind: 'file', size: 40 }] } });
  });
});

test('desktop three-pane workbench is stable', async ({ page }, testInfo) => {
  await page.goto('/commander/');
  await expect(page.getByTestId('commander-shell')).toBeVisible();
  if (testInfo.project.name.includes('desktop')) {
    await expect(page.getByTestId('daemon-tree')).toBeVisible();
    await expect(page.getByTestId('chat-workspace')).toBeVisible();
    await expect(page.getByTestId('file-panel')).toBeVisible();
    await expect(page).toHaveScreenshot('commander-desktop.png', { fullPage: true });
  }
});

test('mobile prioritizes chat without horizontal overflow', async ({ page }, testInfo) => {
  await page.goto('/commander/');
  if (testInfo.project.name.includes('mobile')) {
    await expect(page.getByTestId('chat-workspace')).toBeVisible();
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth);
    expect(overflow).toBe(false);
  }
});
```

- [ ] **Step 4: Install Playwright browser**

Run:

```bash
cd internal/commanderhub/webapp
npx playwright install chromium
```

Expected: chromium browser is installed for Playwright.

- [ ] **Step 5: Generate baseline screenshot**

Run:

```bash
cd internal/commanderhub/webapp
npm run e2e -- --update-snapshots
```

Expected: PASS and a `commander-desktop.png` snapshot is created under the Playwright snapshot directory.

- [ ] **Step 6: Verify E2E without updating snapshots**

Run:

```bash
cd internal/commanderhub/webapp
npm run e2e
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/commanderhub/webapp internal/commanderhub/assets/dist
git commit -m "test(commanderhub): cover commander workbench layout"
```

---

### Task 14: Add Full Verification and Documentation Update

**Files:**
- Create: `docs/superpowers/specs/2026-06-16-commander-ui-redesign-evidence.md`

- [ ] **Step 1: Run targeted Go tests**

Run:

```bash
go test ./internal/commander ./internal/commanderhub ./pkg/agentbackend/... -count=1
```

Expected: PASS.

- [ ] **Step 2: Run frontend tests and build**

Run:

```bash
cd internal/commanderhub/webapp
npm test
npm run build
npm run e2e
git diff --exit-code ../assets/dist
```

Expected: tests PASS, build exits 0, Playwright exits 0, and `git diff --exit-code` exits 0 after the committed dist is up to date.

- [ ] **Step 3: Run race-sensitive commanderhub tests**

Run:

```bash
go test -race ./internal/commanderhub -run 'TestHTTP_TurnRejectsConcurrentSameSession|TestProxy_SendCommandStreamTurn|TestHub_AcksRegisterAndAdmitsDaemon' -count=1
```

Expected: PASS with no race reports.

- [ ] **Step 4: Write evidence doc**

Create `docs/superpowers/specs/2026-06-16-commander-ui-redesign-evidence.md`:

```markdown
# Commander UI Redesign Evidence

- Date: 2026-06-16
- Branch: commander-ui-redesign-design

## Verification

| Command | Result |
|---|---|
| `go test ./internal/commander ./internal/commanderhub ./pkg/agentbackend/... -count=1` | PASS |
| `cd internal/commanderhub/webapp && npm test` | PASS |
| `cd internal/commanderhub/webapp && npm run build` | PASS |
| `cd internal/commanderhub/webapp && npm run e2e` | PASS |
| `git diff --exit-code internal/commanderhub/assets/dist` | PASS |
| `go test -race ./internal/commanderhub -run 'TestHTTP_TurnRejectsConcurrentSameSession|TestProxy_SendCommandStreamTurn|TestHub_AcksRegisterAndAdmitsDaemon' -count=1` | PASS |

## Manual UI Notes

- `/commander` renders the three-pane shell.
- Turn lifecycle status appears outside assistant message content.
- File preview refuses content larger than 2MB.
```

- [ ] **Step 5: Final status check**

Run:

```bash
git status --short
```

Expected: only the evidence doc and any intentional final files are modified.

- [ ] **Step 6: Commit**

```bash
git add docs/superpowers/specs/2026-06-16-commander-ui-redesign-evidence.md
git commit -m "docs(commander): record ui redesign evidence"
```

---

## Final Acceptance Checklist

- [ ] `/commander` uses React/Vite build output served by Go embed.
- [ ] Left pane groups sessions under daemons and uses `(daemon_id, session_id)` as the selection identity.
- [ ] Sessions sort by `UpdatedAt desc`.
- [ ] Session titles use first user prompt via `agentbackend.Session.Title`, with preview/id fallback.
- [ ] Chat header/composer shows turn state outside message content.
- [ ] Composer is disabled while queued/starting/answering/awaiting approval.
- [ ] Observer rejects concurrent turns for the same daemon/session.
- [ ] File routes list cwd-rooted directories and preview text files up to 2MB.
- [ ] File path traversal is rejected daemon-side.
- [ ] Daemons without `files` capability remain usable and can be handled by UI degradation.
- [ ] Frontend tests cover daemon grouping, status/message separation, and file preview too-large state.
- [ ] Go tests cover protocol, file commands, tree model, turn state, and HTTP routes.
- [ ] Vite `dist` is committed and CI/build verification can detect drift.
