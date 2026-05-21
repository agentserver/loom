# Slave File Skill Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a first-class, stateless `file` skill on slaves (read/write/stat) plus three driver MCP tools that cache slave bytes in the driver's existing sha256 `FileRegistry` and keep file contents out of the LLM context by default.

**Architecture:** Slave side mirrors the `bash` precedent — a Go `FileExecutor` in `internal/executor/file.go` registered into `routes["file"]` from `slave-agent/main.go` based on `discovery.skills`. The executor parses an `op`-routed JSON prompt and performs one stateless filesystem operation per call. Driver side adds three MCP tools in `internal/driver/slave_file_tools.go` that delegate `skill="file"` tasks via the existing `t.sdk.DelegateTask` plumbing, then write the returned bytes to a `<cache_root>/file-cache/<sha256>` file and register them in the existing `FileRegistry` so other slaves can fetch via the existing `/files/blob/{sha}` peer-proxy. The LLM receives only handles (sha + cache_path + blob_handle), with an inline `content` field only when payload ≤ `inline_max_bytes` (default 4 KiB) and `encoding=utf-8`. Writes accept three mutually-exclusive sources: inline `content` (small), `source_blob` (a sha from a prior tool call — driver reads from its registry, no rehash), or `source_path` (a driver-local path that gets registered then sent).

**Tech Stack:** Go 1.21+, standard library (`os`, `encoding/base64`, `encoding/json`, `crypto/sha256`, `path/filepath`); existing internal packages `internal/executor`, `internal/driver`; testing via `testing` + `github.com/stretchr/testify/require`.

**Spec:** `multi-agent/docs/superpowers/specs/2026-05-21-slave-file-skill-design.md`

---

## Task 1: Slave `FileExecutor` skeleton + `op:"read"` happy path

**Files:**
- Create: `multi-agent/internal/executor/file.go`
- Create: `multi-agent/internal/executor/file_test.go`

The slave-side executor lives next to `bash.go` and mirrors its shape: a config struct, a constructor, and a `Run(ctx, Task, Sink) (Result, error)` method. The constructor takes `FileConfig{WorkDir}` exactly like `BashConfig`. The result is sent through `sink.Write("chunk", ...)` and also returned in `Result.Summary` so downstream task storage gets the same JSON.

- [ ] **Step 1.1: Write failing test for read of an existing file**

Create `multi-agent/internal/executor/file_test.go`:

```go
package executor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestFileExecutor_ReadWholeFile_UTF8(t *testing.T) {
	workdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workdir, "in.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exec := NewFileExecutor(FileConfig{WorkDir: workdir})
	res, err := exec.Run(context.Background(), Task{
		ID: "t-1", Skill: "file",
		Prompt: `{"op":"read","path":"in.txt"}`,
	}, noopSink{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var got FileReadResult
	if err := json.Unmarshal([]byte(res.Summary), &got); err != nil {
		t.Fatalf("summary not FileReadResult JSON: %v\n%s", err, res.Summary)
	}
	if got.Bytes != 6 || got.Content != "hello\n" || got.Encoding != "utf-8" || !got.EOF {
		t.Fatalf("unexpected result: %+v", got)
	}
	if got.Path != filepath.Join(workdir, "in.txt") {
		t.Fatalf("path = %q, want %q", got.Path, filepath.Join(workdir, "in.txt"))
	}
}
```

- [ ] **Step 1.2: Run test to verify it fails**

Run: `cd multi-agent && go test ./internal/executor/ -run TestFileExecutor_ReadWholeFile_UTF8 -v`
Expected: build error `undefined: NewFileExecutor` / `undefined: FileConfig` / `undefined: FileReadResult`.

- [ ] **Step 1.3: Write minimal `file.go` to make the test pass**

Create `multi-agent/internal/executor/file.go`:

```go
package executor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"
)

const fileMaxReadBytes = 8 * 1024 * 1024 // 8 MiB hard cap per read.

type FileConfig struct {
	WorkDir string
}

type FileExecutor struct{ cfg FileConfig }

func NewFileExecutor(cfg FileConfig) *FileExecutor { return &FileExecutor{cfg: cfg} }

type fileRequest struct {
	Op       string `json:"op"`
	Path     string `json:"path"`
	Offset   int64  `json:"offset,omitempty"`
	Length   int64  `json:"length,omitempty"`
	Encoding string `json:"encoding,omitempty"`
	Content  string `json:"content,omitempty"`
	Mode     string `json:"mode,omitempty"`
	Mkdir    bool   `json:"mkdir,omitempty"`
}

type FileReadResult struct {
	Path     string `json:"path"`
	Bytes    int64  `json:"bytes"`
	Encoding string `json:"encoding"`
	Content  string `json:"content"`
	EOF      bool   `json:"eof"`
}

type FileWriteResult struct {
	Path         string `json:"path"`
	BytesWritten int64  `json:"bytes_written"`
	Mode         string `json:"mode"`
	Offset       *int64 `json:"offset,omitempty"`
}

type FileStatResult struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	Size   int64  `json:"size,omitempty"`
	Mode   string `json:"mode,omitempty"`
	IsDir  bool   `json:"is_dir,omitempty"`
	MTime  string `json:"mtime,omitempty"`
}

func (e *FileExecutor) Run(ctx context.Context, t Task, sink Sink) (Result, error) {
	defer sink.Close()
	var req fileRequest
	if err := json.Unmarshal([]byte(t.Prompt), &req); err != nil {
		return Result{}, fmt.Errorf("file prompt must be JSON: %w", err)
	}
	if req.Path == "" {
		return Result{}, errors.New("file path is required")
	}
	abs := e.resolvePath(req.Path)
	switch req.Op {
	case "read":
		return e.doRead(req, abs, sink)
	default:
		return Result{}, fmt.Errorf("unknown file op %q", req.Op)
	}
}

func (e *FileExecutor) resolvePath(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	base := e.cfg.WorkDir
	if base == "" {
		base, _ = os.Getwd()
	}
	return filepath.Join(base, p)
}

func (e *FileExecutor) doRead(req fileRequest, abs string, sink Sink) (Result, error) {
	enc := req.Encoding
	if enc == "" {
		enc = "utf-8"
	}
	if enc != "utf-8" && enc != "base64" {
		return Result{}, fmt.Errorf("encoding must be utf-8 or base64, got %q", enc)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return Result{}, fmt.Errorf("stat %s: %w", abs, err)
	}
	if info.IsDir() {
		return Result{}, fmt.Errorf("read target is a directory: %s", abs)
	}
	size := info.Size()
	if req.Offset < 0 {
		return Result{}, fmt.Errorf("offset must be >= 0")
	}
	remaining := size - req.Offset
	if remaining < 0 {
		remaining = 0
	}
	want := remaining
	if req.Length > 0 && req.Length < want {
		want = req.Length
	}
	if want > fileMaxReadBytes {
		return Result{}, fmt.Errorf("read of %d bytes exceeds %d cap; chunk via offset/length", want, fileMaxReadBytes)
	}
	buf := make([]byte, want)
	if want > 0 {
		f, err := os.Open(abs)
		if err != nil {
			return Result{}, err
		}
		n, err := f.ReadAt(buf, req.Offset)
		f.Close()
		if err != nil && !errors.Is(err, fs.ErrClosed) && n < int(want) {
			// EOF at exactly want bytes is fine; only a short read is a problem.
			if !(err.Error() == "EOF" && n == int(want)) {
				if n == 0 {
					return Result{}, err
				}
			}
		}
		buf = buf[:n]
	}
	content := ""
	switch enc {
	case "utf-8":
		if !utf8.Valid(buf) {
			return Result{}, fmt.Errorf("content is not valid utf-8; retry with encoding=base64")
		}
		content = string(buf)
	case "base64":
		content = base64.StdEncoding.EncodeToString(buf)
	}
	result := FileReadResult{
		Path:     abs,
		Bytes:    int64(len(buf)),
		Encoding: enc,
		Content:  content,
		EOF:      req.Offset+int64(len(buf)) >= size,
	}
	body, _ := json.Marshal(result)
	sink.Write("chunk", string(body))
	return Result{Summary: string(body)}, nil
}

// time.Now is referenced later (stat); import retained.
var _ = time.Now
```

- [ ] **Step 1.4: Run test to verify it passes**

Run: `cd multi-agent && go test ./internal/executor/ -run TestFileExecutor_ReadWholeFile_UTF8 -v`
Expected: PASS.

- [ ] **Step 1.5: Commit**

```bash
git add multi-agent/internal/executor/file.go multi-agent/internal/executor/file_test.go
git -c commit.gpgsign=false commit -m "feat(executor): file skill skeleton with read op (utf-8 whole file)"
```

---

## Task 2: Read op — offset/length, base64, invalid-utf8, 8 MiB cap, missing file

**Files:**
- Modify: `multi-agent/internal/executor/file_test.go`
- Verify: `multi-agent/internal/executor/file.go` (no impl changes expected; Task 1 already covers these branches)

These are coverage tests against the existing Task 1 implementation. If any fail, fix file.go.

- [ ] **Step 2.1: Add the test cases**

Append to `multi-agent/internal/executor/file_test.go`:

```go
func TestFileExecutor_ReadWithOffsetAndLength(t *testing.T) {
	workdir := t.TempDir()
	os.WriteFile(filepath.Join(workdir, "in.txt"), []byte("abcdefghij"), 0o644)
	exec := NewFileExecutor(FileConfig{WorkDir: workdir})
	res, err := exec.Run(context.Background(), Task{
		Prompt: `{"op":"read","path":"in.txt","offset":2,"length":4}`,
	}, noopSink{})
	if err != nil {
		t.Fatal(err)
	}
	var got FileReadResult
	json.Unmarshal([]byte(res.Summary), &got)
	if got.Content != "cdef" || got.Bytes != 4 || got.EOF {
		t.Fatalf("got %+v", got)
	}
}

func TestFileExecutor_ReadBase64BinarySafe(t *testing.T) {
	workdir := t.TempDir()
	raw := []byte{0x00, 0xff, 0x10, 0x80}
	os.WriteFile(filepath.Join(workdir, "bin"), raw, 0o644)
	exec := NewFileExecutor(FileConfig{WorkDir: workdir})
	res, err := exec.Run(context.Background(), Task{
		Prompt: `{"op":"read","path":"bin","encoding":"base64"}`,
	}, noopSink{})
	if err != nil {
		t.Fatal(err)
	}
	var got FileReadResult
	json.Unmarshal([]byte(res.Summary), &got)
	decoded, _ := base64.StdEncoding.DecodeString(got.Content)
	if !bytes.Equal(decoded, raw) {
		t.Fatalf("base64 roundtrip failed: %v vs %v", decoded, raw)
	}
}

func TestFileExecutor_ReadInvalidUTF8Rejected(t *testing.T) {
	workdir := t.TempDir()
	os.WriteFile(filepath.Join(workdir, "bad"), []byte{0xff, 0xfe}, 0o644)
	exec := NewFileExecutor(FileConfig{WorkDir: workdir})
	_, err := exec.Run(context.Background(), Task{
		Prompt: `{"op":"read","path":"bad"}`,
	}, noopSink{})
	if err == nil || !strings.Contains(err.Error(), "utf-8") {
		t.Fatalf("expected utf-8 rejection, got %v", err)
	}
}

func TestFileExecutor_ReadCapEnforced(t *testing.T) {
	workdir := t.TempDir()
	big := make([]byte, fileMaxReadBytes+1)
	os.WriteFile(filepath.Join(workdir, "big"), big, 0o644)
	exec := NewFileExecutor(FileConfig{WorkDir: workdir})
	_, err := exec.Run(context.Background(), Task{
		Prompt: `{"op":"read","path":"big","encoding":"base64"}`,
	}, noopSink{})
	if err == nil || !strings.Contains(err.Error(), "cap") {
		t.Fatalf("expected cap error, got %v", err)
	}
}

func TestFileExecutor_ReadFileNotFound(t *testing.T) {
	exec := NewFileExecutor(FileConfig{WorkDir: t.TempDir()})
	_, err := exec.Run(context.Background(), Task{
		Prompt: `{"op":"read","path":"nope"}`,
	}, noopSink{})
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestFileExecutor_ReadAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "abs.txt")
	os.WriteFile(abs, []byte("xyz"), 0o644)
	exec := NewFileExecutor(FileConfig{WorkDir: "/tmp/elsewhere"})
	res, err := exec.Run(context.Background(), Task{
		Prompt: fmt.Sprintf(`{"op":"read","path":%q}`, abs),
	}, noopSink{})
	if err != nil {
		t.Fatal(err)
	}
	var got FileReadResult
	json.Unmarshal([]byte(res.Summary), &got)
	if got.Path != abs || got.Content != "xyz" {
		t.Fatalf("got %+v", got)
	}
}
```

Also add to the imports at top of the test file:

```go
import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)
```

- [ ] **Step 2.2: Run tests**

Run: `cd multi-agent && go test ./internal/executor/ -run TestFileExecutor_Read -v`
Expected: all six PASS.

- [ ] **Step 2.3: Commit**

```bash
git add multi-agent/internal/executor/file_test.go
git -c commit.gpgsign=false commit -m "test(executor): file read offset/length/base64/cap/missing/absolute"
```

---

## Task 3: Write op — all four modes + mkdir + encoding

**Files:**
- Modify: `multi-agent/internal/executor/file.go`
- Modify: `multi-agent/internal/executor/file_test.go`

- [ ] **Step 3.1: Write failing tests**

Append to `file_test.go`:

```go
func TestFileExecutor_WriteOverwriteCreatesAndReplaces(t *testing.T) {
	workdir := t.TempDir()
	exec := NewFileExecutor(FileConfig{WorkDir: workdir})
	// create
	res, err := exec.Run(context.Background(), Task{
		Prompt: `{"op":"write","path":"out.txt","content":"first"}`,
	}, noopSink{})
	if err != nil {
		t.Fatal(err)
	}
	var got FileWriteResult
	json.Unmarshal([]byte(res.Summary), &got)
	if got.BytesWritten != 5 || got.Mode != "overwrite" || got.Offset != nil {
		t.Fatalf("create result: %+v", got)
	}
	// replace
	_, err = exec.Run(context.Background(), Task{
		Prompt: `{"op":"write","path":"out.txt","content":"second"}`,
	}, noopSink{})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(workdir, "out.txt"))
	if string(body) != "second" {
		t.Fatalf("file = %q, want %q", body, "second")
	}
}

func TestFileExecutor_WriteAppend(t *testing.T) {
	workdir := t.TempDir()
	os.WriteFile(filepath.Join(workdir, "log"), []byte("line1\n"), 0o644)
	exec := NewFileExecutor(FileConfig{WorkDir: workdir})
	_, err := exec.Run(context.Background(), Task{
		Prompt: `{"op":"write","path":"log","content":"line2\n","mode":"append"}`,
	}, noopSink{})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(workdir, "log"))
	if string(body) != "line1\nline2\n" {
		t.Fatalf("file = %q", body)
	}
}

func TestFileExecutor_WriteCreateNew_ErrorsIfExists(t *testing.T) {
	workdir := t.TempDir()
	os.WriteFile(filepath.Join(workdir, "exist"), []byte("x"), 0o644)
	exec := NewFileExecutor(FileConfig{WorkDir: workdir})
	_, err := exec.Run(context.Background(), Task{
		Prompt: `{"op":"write","path":"exist","content":"y","mode":"create_new"}`,
	}, noopSink{})
	if err == nil {
		t.Fatal("expected create_new to error on existing file")
	}
}

func TestFileExecutor_WritePatchInRange(t *testing.T) {
	workdir := t.TempDir()
	os.WriteFile(filepath.Join(workdir, "f"), []byte("AAAAAAAAAA"), 0o644)
	exec := NewFileExecutor(FileConfig{WorkDir: workdir})
	_, err := exec.Run(context.Background(), Task{
		Prompt: `{"op":"write","path":"f","content":"BB","mode":"patch","offset":3}`,
	}, noopSink{})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(workdir, "f"))
	if string(body) != "AAABBAAAAA" {
		t.Fatalf("file = %q", body)
	}
}

func TestFileExecutor_WritePatchPastEOFZeroFills(t *testing.T) {
	workdir := t.TempDir()
	exec := NewFileExecutor(FileConfig{WorkDir: workdir})
	_, err := exec.Run(context.Background(), Task{
		Prompt: `{"op":"write","path":"f","content":"XY","mode":"patch","offset":4,"mkdir":true}`,
	}, noopSink{})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(workdir, "f"))
	if !bytes.Equal(body, []byte{0, 0, 0, 0, 'X', 'Y'}) {
		t.Fatalf("file = %v", body)
	}
}

func TestFileExecutor_WriteRejectsOffsetWithoutPatch(t *testing.T) {
	exec := NewFileExecutor(FileConfig{WorkDir: t.TempDir()})
	_, err := exec.Run(context.Background(), Task{
		Prompt: `{"op":"write","path":"x","content":"y","mode":"overwrite","offset":5}`,
	}, noopSink{})
	if err == nil || !strings.Contains(err.Error(), "offset") {
		t.Fatalf("expected offset rejection, got %v", err)
	}
}

func TestFileExecutor_WritePatchRequiresOffsetTagged(t *testing.T) {
	// patch with offset:0 is allowed; the rejection is "offset on non-patch modes".
	exec := NewFileExecutor(FileConfig{WorkDir: t.TempDir()})
	_, err := exec.Run(context.Background(), Task{
		Prompt: `{"op":"write","path":"f","content":"hi","mode":"patch"}`,
	}, noopSink{})
	if err != nil {
		t.Fatalf("patch with offset 0 should succeed: %v", err)
	}
}

func TestFileExecutor_WriteMkdirCreatesParents(t *testing.T) {
	workdir := t.TempDir()
	exec := NewFileExecutor(FileConfig{WorkDir: workdir})
	_, err := exec.Run(context.Background(), Task{
		Prompt: `{"op":"write","path":"a/b/c/file","content":"hi","mkdir":true}`,
	}, noopSink{})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(workdir, "a", "b", "c", "file"))
	if string(body) != "hi" {
		t.Fatalf("file = %q", body)
	}
}

func TestFileExecutor_WriteBase64Roundtrip(t *testing.T) {
	workdir := t.TempDir()
	raw := []byte{0x00, 0xff, 0x42}
	enc := base64.StdEncoding.EncodeToString(raw)
	exec := NewFileExecutor(FileConfig{WorkDir: workdir})
	_, err := exec.Run(context.Background(), Task{
		Prompt: fmt.Sprintf(`{"op":"write","path":"bin","content":%q,"encoding":"base64"}`, enc),
	}, noopSink{})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(workdir, "bin"))
	if !bytes.Equal(body, raw) {
		t.Fatalf("bytes differ")
	}
}
```

- [ ] **Step 3.2: Run tests to verify they fail**

Run: `cd multi-agent && go test ./internal/executor/ -run TestFileExecutor_Write -v`
Expected: all FAIL with `unknown file op "write"`.

- [ ] **Step 3.3: Implement the write op**

In `file.go`, extend the `Run` switch and add `doWrite`:

```go
func (e *FileExecutor) Run(ctx context.Context, t Task, sink Sink) (Result, error) {
	defer sink.Close()
	var req fileRequest
	if err := json.Unmarshal([]byte(t.Prompt), &req); err != nil {
		return Result{}, fmt.Errorf("file prompt must be JSON: %w", err)
	}
	if req.Path == "" {
		return Result{}, errors.New("file path is required")
	}
	abs := e.resolvePath(req.Path)
	switch req.Op {
	case "read":
		return e.doRead(req, abs, sink)
	case "write":
		return e.doWrite(req, abs, sink)
	default:
		return Result{}, fmt.Errorf("unknown file op %q", req.Op)
	}
}

func (e *FileExecutor) doWrite(req fileRequest, abs string, sink Sink) (Result, error) {
	enc := req.Encoding
	if enc == "" {
		enc = "utf-8"
	}
	if enc != "utf-8" && enc != "base64" {
		return Result{}, fmt.Errorf("encoding must be utf-8 or base64, got %q", enc)
	}
	mode := req.Mode
	if mode == "" {
		mode = "overwrite"
	}
	switch mode {
	case "overwrite", "append", "create_new", "patch":
	default:
		return Result{}, fmt.Errorf("mode must be overwrite|append|create_new|patch, got %q", mode)
	}
	if mode != "patch" && req.Offset != 0 {
		return Result{}, fmt.Errorf("offset is only valid with mode=patch")
	}
	if req.Offset < 0 {
		return Result{}, fmt.Errorf("offset must be >= 0")
	}

	var bytesPayload []byte
	switch enc {
	case "utf-8":
		bytesPayload = []byte(req.Content)
	case "base64":
		decoded, err := base64.StdEncoding.DecodeString(req.Content)
		if err != nil {
			return Result{}, fmt.Errorf("base64 decode: %w", err)
		}
		bytesPayload = decoded
	}

	if req.Mkdir {
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return Result{}, err
		}
	}

	var (
		f   *os.File
		err error
	)
	switch mode {
	case "overwrite":
		f, err = os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	case "append":
		f, err = os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	case "create_new":
		f, err = os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	case "patch":
		f, err = os.OpenFile(abs, os.O_WRONLY|os.O_CREATE, 0o644)
	}
	if err != nil {
		return Result{}, err
	}
	defer f.Close()

	var n int
	if mode == "patch" {
		n, err = f.WriteAt(bytesPayload, req.Offset)
	} else {
		n, err = f.Write(bytesPayload)
	}
	if err != nil {
		return Result{}, err
	}
	result := FileWriteResult{
		Path:         abs,
		BytesWritten: int64(n),
		Mode:         mode,
	}
	if mode == "patch" {
		off := req.Offset
		result.Offset = &off
	}
	body, _ := json.Marshal(result)
	sink.Write("chunk", string(body))
	return Result{Summary: string(body)}, nil
}
```

- [ ] **Step 3.4: Run tests to verify they pass**

Run: `cd multi-agent && go test ./internal/executor/ -run TestFileExecutor_Write -v`
Expected: all PASS.

- [ ] **Step 3.5: Commit**

```bash
git add multi-agent/internal/executor/file.go multi-agent/internal/executor/file_test.go
git -c commit.gpgsign=false commit -m "feat(executor): file write op (overwrite|append|create_new|patch)"
```

---

## Task 4: Stat op

**Files:**
- Modify: `multi-agent/internal/executor/file.go`
- Modify: `multi-agent/internal/executor/file_test.go`

- [ ] **Step 4.1: Write failing tests**

Append to `file_test.go`:

```go
func TestFileExecutor_StatExisting(t *testing.T) {
	workdir := t.TempDir()
	os.WriteFile(filepath.Join(workdir, "f"), []byte("abcde"), 0o644)
	exec := NewFileExecutor(FileConfig{WorkDir: workdir})
	res, err := exec.Run(context.Background(), Task{
		Prompt: `{"op":"stat","path":"f"}`,
	}, noopSink{})
	if err != nil {
		t.Fatal(err)
	}
	var got FileStatResult
	json.Unmarshal([]byte(res.Summary), &got)
	if !got.Exists || got.Size != 5 || got.IsDir {
		t.Fatalf("got %+v", got)
	}
	if got.MTime == "" || got.Mode == "" {
		t.Fatalf("expected mtime and mode populated: %+v", got)
	}
}

func TestFileExecutor_StatMissingReturnsExistsFalse(t *testing.T) {
	exec := NewFileExecutor(FileConfig{WorkDir: t.TempDir()})
	res, err := exec.Run(context.Background(), Task{
		Prompt: `{"op":"stat","path":"nope"}`,
	}, noopSink{})
	if err != nil {
		t.Fatalf("stat on missing path should not error: %v", err)
	}
	var got FileStatResult
	json.Unmarshal([]byte(res.Summary), &got)
	if got.Exists {
		t.Fatalf("expected exists=false, got %+v", got)
	}
}

func TestFileExecutor_StatDirectory(t *testing.T) {
	workdir := t.TempDir()
	os.Mkdir(filepath.Join(workdir, "d"), 0o755)
	exec := NewFileExecutor(FileConfig{WorkDir: workdir})
	res, _ := exec.Run(context.Background(), Task{
		Prompt: `{"op":"stat","path":"d"}`,
	}, noopSink{})
	var got FileStatResult
	json.Unmarshal([]byte(res.Summary), &got)
	if !got.Exists || !got.IsDir {
		t.Fatalf("got %+v", got)
	}
}
```

- [ ] **Step 4.2: Run tests to verify they fail**

Run: `cd multi-agent && go test ./internal/executor/ -run TestFileExecutor_Stat -v`
Expected: FAIL with `unknown file op "stat"`.

- [ ] **Step 4.3: Implement stat**

In `file.go`, extend the switch and add `doStat`:

```go
	case "stat":
		return e.doStat(req, abs, sink)
```

```go
func (e *FileExecutor) doStat(req fileRequest, abs string, sink Sink) (Result, error) {
	info, err := os.Stat(abs)
	if errors.Is(err, fs.ErrNotExist) {
		result := FileStatResult{Path: abs, Exists: false}
		body, _ := json.Marshal(result)
		sink.Write("chunk", string(body))
		return Result{Summary: string(body)}, nil
	}
	if err != nil {
		return Result{}, err
	}
	result := FileStatResult{
		Path:   abs,
		Exists: true,
		Size:   info.Size(),
		Mode:   fmt.Sprintf("%#o", info.Mode().Perm()),
		IsDir:  info.IsDir(),
		MTime:  info.ModTime().UTC().Format(time.RFC3339Nano),
	}
	body, _ := json.Marshal(result)
	sink.Write("chunk", string(body))
	return Result{Summary: string(body)}, nil
}
```

Also remove the now-unused `var _ = time.Now` placeholder from Task 1, since `time` is genuinely used here.

- [ ] **Step 4.4: Run tests**

Run: `cd multi-agent && go test ./internal/executor/ -run TestFileExecutor -v`
Expected: every `TestFileExecutor_*` PASSES (the whole file's tests, including read and write from earlier tasks).

- [ ] **Step 4.5: Commit**

```bash
git add multi-agent/internal/executor/file.go multi-agent/internal/executor/file_test.go
git -c commit.gpgsign=false commit -m "feat(executor): file stat op (existence-tolerant)"
```

---

## Task 5: Wire `file` skill into slave-agent

**Files:**
- Modify: `multi-agent/cmd/slave-agent/main.go:109-111` (next to the existing `bash` registration)
- Modify: `multi-agent/cmd/slave-agent/main_test.go`
- Modify: `multi-agent/cmd/slave-agent/config.example.yaml`

- [ ] **Step 5.1: Extend the `hasSkill` test**

Open `multi-agent/cmd/slave-agent/main_test.go` and add a `file` case alongside the existing `bash` case. Read the file first to see exact current shape; then add:

```go
func TestHasSkill_File(t *testing.T) {
	if !hasSkill([]string{"chat", "file"}, "file") {
		t.Fatal("expected file skill")
	}
	if hasSkill([]string{"chat"}, "file") {
		t.Fatal("did not expect file skill")
	}
}
```

- [ ] **Step 5.2: Run the failing test**

Run: `cd multi-agent && go test ./cmd/slave-agent/ -run TestHasSkill_File -v`
Expected: PASS already (since `hasSkill` is generic). This test just guards future regressions on the skill name string.

- [ ] **Step 5.3: Register the executor in main.go**

In `multi-agent/cmd/slave-agent/main.go`, immediately after the existing `bash` block (currently lines 109-111), add:

```go
	if hasSkill(cfg.Discovery.Skills, "file") {
		routes["file"] = executor.NewFileExecutor(executor.FileConfig{WorkDir: cfg.Claude.WorkDir})
	}
```

- [ ] **Step 5.4: Update config.example.yaml**

In `multi-agent/cmd/slave-agent/config.example.yaml`, under `discovery.skills`, after the existing commented `# - bash` line, add:

```yaml
    # - file  # opt-in stateless file read/write/stat through a native slave-agent executor
```

- [ ] **Step 5.5: Build to confirm wiring compiles**

Run: `cd multi-agent && go build ./cmd/slave-agent/...`
Expected: no errors.

- [ ] **Step 5.6: Run full slave-agent and executor tests**

Run: `cd multi-agent && go test ./cmd/slave-agent/... ./internal/executor/... -count=1`
Expected: all PASS.

- [ ] **Step 5.7: Commit**

```bash
git add multi-agent/cmd/slave-agent/main.go multi-agent/cmd/slave-agent/main_test.go multi-agent/cmd/slave-agent/config.example.yaml
git -c commit.gpgsign=false commit -m "feat(slave-agent): register file skill executor when advertised"
```

---

## Task 6: Driver `read_slave_file` — cache + register + handle return

**Files:**
- Create: `multi-agent/internal/driver/slave_file_tools.go`
- Create: `multi-agent/internal/driver/slave_file_tools_test.go`
- Modify: `multi-agent/internal/driver/tools.go` — append the new tool to `Tools.All()`

The tool delegates `{"op":"read", ...}` via the existing `t.sdk.DelegateTask` + `t.waitDelegatedTask`. The slave's structured `FileReadResult` JSON is the task's `Result`/`output`; this tool unwraps it, writes the bytes to `<cache_root>/file-cache/<sha256>`, calls `t.reg.RegisterFile(absPath)` to publish through the existing `/files/blob/{sha}` peer-proxy, and audit-logs `register_read` with the slave's `short_id` as `peer_short_id`. The LLM-facing return omits the raw content unless `bytes <= inline_max_bytes` AND `encoding == "utf-8"`.

- [ ] **Step 6.1: Write the failing tests**

Create `multi-agent/internal/driver/slave_file_tools_test.go`:

```go
package driver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/stretchr/testify/require"
)

func TestReadSlaveFile_CachesAndRegistersAndReturnsHandle(t *testing.T) {
	var delegated agentsdk.DelegateTaskRequest
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{{
				AgentID: "slave-a", DisplayName: "slave-a", Status: "available",
				Card: json.RawMessage(`{"skills":["file"],"short_id":"sa"}`),
			}}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			delegated = req
			return &agentsdk.DelegateTaskResponse{TaskID: "task-1"}, nil
		},
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			// slave returns hello\n (6 bytes, utf-8)
			return &agentsdk.TaskInfo{
				TaskID: "task-1", Status: "completed",
				Result: json.RawMessage(`"{\"path\":\"/abs/in.txt\",\"bytes\":6,\"encoding\":\"utf-8\",\"content\":\"hello\\n\",\"eof\":true}"`),
			}, nil
		},
	}
	tools := newTestTools(t, sdk)
	tool := toolByName(t, tools, "read_slave_file")
	raw, err := tool.Call(context.Background(), json.RawMessage(`{"target_display_name":"slave-a","path":"in.txt"}`))
	require.NoError(t, err)
	require.Equal(t, "file", delegated.Skill)
	require.JSONEq(t, `{"op":"read","path":"in.txt"}`, delegated.Prompt)

	var out map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &out))
	require.Equal(t, "/abs/in.txt", out["slave_path"])
	require.EqualValues(t, 6, out["size"])
	require.Equal(t, "hello\n", out["content"]) // inline (≤ 4 KiB, utf-8)
	sha, _ := out["sha256"].(string)
	require.NotEmpty(t, sha)
	require.Equal(t, "sha256:"+sha, out["blob_handle"])

	// Cache file exists with that sha as filename.
	cachePath, _ := out["cache_path"].(string)
	require.FileExists(t, cachePath)
	body, _ := os.ReadFile(cachePath)
	require.Equal(t, "hello\n", string(body))

	// FileRegistry has it.
	path, ok := tools.reg.LookupBlob(sha)
	require.True(t, ok)
	require.Equal(t, cachePath, path)
}

func TestReadSlaveFile_OmitsContentWhenLargerThanInlineCap(t *testing.T) {
	// 10-byte file but inline_max_bytes=4 → content omitted.
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{{
				AgentID: "slave-a", DisplayName: "slave-a", Status: "available",
				Card: json.RawMessage(`{"skills":["file"],"short_id":"sa"}`),
			}}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			return &agentsdk.DelegateTaskResponse{TaskID: "task-1"}, nil
		},
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{
				TaskID: "task-1", Status: "completed",
				Result: json.RawMessage(`"{\"path\":\"/abs/big.txt\",\"bytes\":10,\"encoding\":\"utf-8\",\"content\":\"0123456789\",\"eof\":true}"`),
			}, nil
		},
	}
	tool := toolByName(t, newTestTools(t, sdk), "read_slave_file")
	raw, err := tool.Call(context.Background(), json.RawMessage(`{"target_display_name":"slave-a","path":"big.txt","inline_max_bytes":4}`))
	require.NoError(t, err)
	var out map[string]interface{}
	json.Unmarshal(raw, &out)
	_, hasContent := out["content"]
	require.False(t, hasContent, "content must be omitted when size > inline_max_bytes")
}

func TestReadSlaveFile_OmitsContentForBase64EvenWhenSmall(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{{
				AgentID: "slave-a", DisplayName: "slave-a", Status: "available",
				Card: json.RawMessage(`{"skills":["file"],"short_id":"sa"}`),
			}}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			return &agentsdk.DelegateTaskResponse{TaskID: "task-1"}, nil
		},
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			// 3-byte binary, base64 = "AP9C"
			return &agentsdk.TaskInfo{
				TaskID: "task-1", Status: "completed",
				Result: json.RawMessage(`"{\"path\":\"/abs/bin\",\"bytes\":3,\"encoding\":\"base64\",\"content\":\"AP9C\",\"eof\":true}"`),
			}, nil
		},
	}
	tool := toolByName(t, newTestTools(t, sdk), "read_slave_file")
	raw, err := tool.Call(context.Background(), json.RawMessage(`{"target_display_name":"slave-a","path":"bin","encoding":"base64"}`))
	require.NoError(t, err)
	var out map[string]interface{}
	json.Unmarshal(raw, &out)
	_, hasContent := out["content"]
	require.False(t, hasContent, "base64 reads never inline content")
}

func TestReadSlaveFile_RejectsMissingFileSkill(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{{
				AgentID: "slave-a", DisplayName: "slave-a", Status: "available",
				Card: json.RawMessage(`{"skills":["chat"]}`),
			}}, nil
		},
	}
	tool := toolByName(t, newTestTools(t, sdk), "read_slave_file")
	_, err := tool.Call(context.Background(), json.RawMessage(`{"target_display_name":"slave-a","path":"in.txt"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not advertise file")
}

func TestReadSlaveFile_AuditsRegisterReadWithSlaveShortID(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{{
				AgentID: "slave-a", DisplayName: "slave-a", Status: "available",
				Card: json.RawMessage(`{"skills":["file"],"short_id":"sa"}`),
			}}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			return &agentsdk.DelegateTaskResponse{TaskID: "task-1"}, nil
		},
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{
				TaskID: "task-1", Status: "completed",
				Result: json.RawMessage(`"{\"path\":\"/abs/in.txt\",\"bytes\":2,\"encoding\":\"utf-8\",\"content\":\"hi\",\"eof\":true}"`),
			}, nil
		},
	}
	tools := newTestTools(t, sdk)
	tool := toolByName(t, tools, "read_slave_file")
	_, err := tool.Call(context.Background(), json.RawMessage(`{"target_display_name":"slave-a","path":"in.txt"}`))
	require.NoError(t, err)

	// Read the audit log file directly (newTestTools puts it under TempDir).
	// Iterate the test cache dir: audit.log lives at <tmpdir>/audit.log per newTestToolsWithObserver.
	// Walk t.TempDir? Simpler: poke driver_defaults.AuditLogDir if we set it. The default test
	// helper writes to filepath.Join(dir, "audit.log") in dir = t.TempDir(). We don't have dir
	// here, so search for any "audit.log" under the current process's open files is overkill —
	// instead, the helper should expose AuditLogPath. Pull dir from cfg.DriverDefaults.AuditLogDir:
	dir := tools.cfg.DriverDefaults.AuditLogDir
	require.NotEmpty(t, dir, "test helper must set AuditLogDir so this test can find audit.log")
	body, err := os.ReadFile(filepath.Join(dir, "audit.log"))
	require.NoError(t, err)
	require.Contains(t, string(body), `"event":"register_read"`)
	require.Contains(t, string(body), `"peer_short_id":"sa"`)
}
```

This test requires the test helper to populate `cfg.DriverDefaults.AuditLogDir`. Update `tools_test.go::newTestToolsWithObserver`:

```go
func newTestToolsWithObserver(t *testing.T, sdk SDKClient, obs ObserverSink) *Tools {
	t.Helper()
	dir := t.TempDir()
	a, err := NewAuditLog(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	cfg := &Config{}
	cfg.Server.URL = "https://srv.example.com"
	cfg.Credentials.ShortID = "drv-001"
	cfg.Credentials.SandboxID = "sbx-driver"
	cfg.DriverDefaults.TaskTimeoutSec = 600
	cfg.DriverDefaults.AuditLogDir = dir // expose so cache root and audit log path are predictable
	return NewTools(NewFileRegistry(50000), a, sdk, cfg, obs)
}
```

- [ ] **Step 6.2: Run tests to verify they fail**

Run: `cd multi-agent && go test ./internal/driver/ -run TestReadSlaveFile -v`
Expected: FAIL (no `read_slave_file` tool registered yet).

- [ ] **Step 6.3: Implement the tool**

Create `multi-agent/internal/driver/slave_file_tools.go`:

```go
package driver

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/agentserver/agentserver/pkg/agentsdk"
)

const defaultInlineMaxBytes = 4096

// fileCacheRoot returns the directory where read_slave_file caches blobs.
// Mirrors resolveAuditPath's directory choice so all driver-local files share a parent.
func (t *Tools) fileCacheRoot() (string, error) {
	dir := ""
	if t.cfg != nil {
		dir = t.cfg.DriverDefaults.AuditLogDir
	}
	if dir == "" {
		u, err := user.Current()
		if err != nil {
			return "", err
		}
		shortID := ""
		if t.cfg != nil {
			shortID = t.cfg.Credentials.ShortID
		}
		dir = filepath.Join(u.HomeDir, ".cache", "multi-agent", shortID)
	}
	root := filepath.Join(dir, "file-cache")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	return root, nil
}

// cacheBytes writes payload to <root>/<sha256> atomically and registers it in FileRegistry.
// Returns (sha, abs path).
func (t *Tools) cacheBytes(payload []byte) (string, string, error) {
	sum := sha256.Sum256(payload)
	sha := hex.EncodeToString(sum[:])
	root, err := t.fileCacheRoot()
	if err != nil {
		return "", "", err
	}
	abs := filepath.Join(root, sha)
	if _, statErr := os.Stat(abs); os.IsNotExist(statErr) {
		tmp, err := os.CreateTemp(root, "incoming-*")
		if err != nil {
			return "", "", err
		}
		if _, err := tmp.Write(payload); err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			return "", "", err
		}
		tmp.Close()
		if err := os.Rename(tmp.Name(), abs); err != nil {
			os.Remove(tmp.Name())
			return "", "", err
		}
	}
	if _, _, _, err := t.reg.RegisterFile(abs); err != nil {
		return "", "", err
	}
	return sha, abs, nil
}

type readSlaveFileTool struct{ t *Tools }

func (r *readSlaveFileTool) Name() string { return "read_slave_file" }
func (r *readSlaveFileTool) Description() string {
	return "Read a file from a selected slave through the file skill. Bytes are cached in the driver's blob store; the LLM receives a handle plus inline content only if small and utf-8."
}
func (r *readSlaveFileTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
        "target_agent_id":{"type":"string"},
        "target_display_name":{"type":"string"},
        "path":{"type":"string"},
        "offset":{"type":"integer","minimum":0},
        "length":{"type":"integer","minimum":1},
        "encoding":{"type":"string","enum":["utf-8","base64"]},
        "inline_max_bytes":{"type":"integer","minimum":0}
    },"required":["path"],"additionalProperties":false}`)
}
func (r *readSlaveFileTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		TargetAgentID     string `json:"target_agent_id"`
		TargetDisplayName string `json:"target_display_name"`
		Path              string `json:"path"`
		Offset            int64  `json:"offset,omitempty"`
		Length            int64  `json:"length,omitempty"`
		Encoding          string `json:"encoding,omitempty"`
		InlineMaxBytes    *int64 `json:"inline_max_bytes,omitempty"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &MCPToolError{Message: "invalid args: " + err.Error()}
	}
	if args.Path == "" {
		return nil, &MCPToolError{Message: "path is required"}
	}
	card, err := r.t.resolveAvailableAgent(ctx, args.TargetAgentID, args.TargetDisplayName)
	if err != nil {
		return nil, err
	}
	if !hasSkill(card, "file") {
		return nil, &MCPToolError{Message: "target " + card.DisplayName + " does not advertise file"}
	}
	prompt := map[string]interface{}{"op": "read", "path": args.Path}
	if args.Offset > 0 {
		prompt["offset"] = args.Offset
	}
	if args.Length > 0 {
		prompt["length"] = args.Length
	}
	if args.Encoding != "" {
		prompt["encoding"] = args.Encoding
	}
	pb, _ := json.Marshal(prompt)
	resp, err := r.t.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID: card.AgentID, Skill: "file", Prompt: string(pb),
	})
	if err != nil {
		return nil, &MCPToolError{Message: "delegate file read: " + err.Error()}
	}
	waitOut, err := r.t.waitDelegatedTask(ctx, resp.TaskID, 0)
	if err != nil {
		return nil, err
	}
	// waitDelegatedTask wraps the slave summary as a JSON-encoded string in "output".
	// Pull it back out and parse the slave's FileReadResult.
	var wrap struct {
		TaskID string          `json:"task_id"`
		Status string          `json:"status"`
		Output string          `json:"output"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(waitOut, &wrap); err != nil {
		return nil, &MCPToolError{Message: "parse task output: " + err.Error()}
	}
	var slaveRes struct {
		Path     string `json:"path"`
		Bytes    int64  `json:"bytes"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
		EOF      bool   `json:"eof"`
	}
	if err := json.Unmarshal([]byte(wrap.Output), &slaveRes); err != nil {
		return nil, &MCPToolError{Message: "parse slave file result: " + err.Error()}
	}

	// Decode payload, cache, register.
	var payload []byte
	switch slaveRes.Encoding {
	case "utf-8":
		payload = []byte(slaveRes.Content)
	case "base64":
		payload, err = base64.StdEncoding.DecodeString(slaveRes.Content)
		if err != nil {
			return nil, &MCPToolError{Message: "slave returned invalid base64: " + err.Error()}
		}
	default:
		return nil, &MCPToolError{Message: "slave returned unknown encoding: " + slaveRes.Encoding}
	}
	sha, cachePath, err := r.t.cacheBytes(payload)
	if err != nil {
		return nil, &MCPToolError{Message: "cache slave bytes: " + err.Error()}
	}
	shortID := cardShortID(card)
	r.t.audit.Log(AuditEvent{
		Event: "register_read", Path: slaveRes.Path, SHA256: sha,
		Bytes: int64(len(payload)), PeerShortID: shortID, TaskID: resp.TaskID,
	})

	inlineMax := int64(defaultInlineMaxBytes)
	if args.InlineMaxBytes != nil {
		inlineMax = *args.InlineMaxBytes
	}
	out := map[string]interface{}{
		"task_id":             resp.TaskID,
		"target_display_name": card.DisplayName,
		"slave_path":          slaveRes.Path,
		"size":                slaveRes.Bytes,
		"encoding":            slaveRes.Encoding,
		"sha256":              sha,
		"blob_handle":         "sha256:" + sha,
		"cache_path":          cachePath,
		"eof":                 slaveRes.EOF,
	}
	if slaveRes.Encoding == "utf-8" && slaveRes.Bytes <= inlineMax {
		out["content"] = slaveRes.Content
	}
	return json.Marshal(out)
}

var _ = strings.Contains // imported for future helpers; remove if still unused at the end of Task 8.
var _ = fmt.Sprint
```

In `multi-agent/internal/driver/tools.go`, inside `Tools.All()`, add `&readSlaveFileTool{t}` to the slice. Pick a stable place next to the existing slave-control tools (immediately after `&updateSlaveClaudePermissionsTool{t},`):

```go
		&updateSlaveClaudePermissionsTool{t},
		&readSlaveFileTool{t},
```

- [ ] **Step 6.4: Run tests**

Run: `cd multi-agent && go test ./internal/driver/ -run TestReadSlaveFile -v`
Expected: all five PASS.

- [ ] **Step 6.5: Commit**

```bash
git add multi-agent/internal/driver/slave_file_tools.go multi-agent/internal/driver/slave_file_tools_test.go multi-agent/internal/driver/tools.go multi-agent/internal/driver/tools_test.go
git -c commit.gpgsign=false commit -m "feat(driver): read_slave_file with FileRegistry-backed cache"
```

---

## Task 7: Driver `write_slave_file` — three source modes + validation

**Files:**
- Modify: `multi-agent/internal/driver/slave_file_tools.go`
- Modify: `multi-agent/internal/driver/slave_file_tools_test.go`
- Modify: `multi-agent/internal/driver/tools.go`

- [ ] **Step 7.1: Write failing tests**

Append to `slave_file_tools_test.go`:

```go
func newAvailableFileSlaveSDK(t *testing.T, slaveResult string, captured *agentsdk.DelegateTaskRequest) *fakeSDK {
	return &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{{
				AgentID: "slave-b", DisplayName: "slave-b", Status: "available",
				Card: json.RawMessage(`{"skills":["file"],"short_id":"sb"}`),
			}}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			*captured = req
			return &agentsdk.DelegateTaskResponse{TaskID: "task-w1"}, nil
		},
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{
				TaskID: "task-w1", Status: "completed",
				Result: json.RawMessage(`"` + jsonEscape(slaveResult) + `"`),
			}, nil
		},
	}
}

func jsonEscape(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1])
}

func TestWriteSlaveFile_InlineContent(t *testing.T) {
	var captured agentsdk.DelegateTaskRequest
	sdk := newAvailableFileSlaveSDK(t,
		`{"path":"/abs/out.txt","bytes_written":5,"mode":"overwrite"}`, &captured)
	tool := toolByName(t, newTestTools(t, sdk), "write_slave_file")
	raw, err := tool.Call(context.Background(),
		json.RawMessage(`{"target_display_name":"slave-b","path":"out.txt","content":"hello"}`))
	require.NoError(t, err)
	require.Equal(t, "file", captured.Skill)
	require.JSONEq(t,
		`{"op":"write","path":"out.txt","content":"hello","encoding":"utf-8","mode":"overwrite"}`,
		captured.Prompt)
	var out map[string]interface{}
	json.Unmarshal(raw, &out)
	require.Equal(t, "content", out["source"])
	require.EqualValues(t, 5, out["bytes_written"])
}

func TestWriteSlaveFile_RejectsZeroOrMultipleSources(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{{
				AgentID: "slave-b", DisplayName: "slave-b", Status: "available",
				Card: json.RawMessage(`{"skills":["file"]}`),
			}}, nil
		},
	}
	tool := toolByName(t, newTestTools(t, sdk), "write_slave_file")
	// zero
	_, err := tool.Call(context.Background(),
		json.RawMessage(`{"target_display_name":"slave-b","path":"x"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "exactly one")
	// multiple
	_, err = tool.Call(context.Background(),
		json.RawMessage(`{"target_display_name":"slave-b","path":"x","content":"a","source_path":"/p"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "exactly one")
}

func TestWriteSlaveFile_RejectsOffsetWithoutPatch(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{{
				AgentID: "slave-b", DisplayName: "slave-b", Status: "available",
				Card: json.RawMessage(`{"skills":["file"]}`),
			}}, nil
		},
	}
	tool := toolByName(t, newTestTools(t, sdk), "write_slave_file")
	_, err := tool.Call(context.Background(), json.RawMessage(
		`{"target_display_name":"slave-b","path":"x","content":"a","mode":"overwrite","offset":5}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "offset")
}

func TestWriteSlaveFile_RejectsLargeInlineContent(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{{
				AgentID: "slave-b", DisplayName: "slave-b", Status: "available",
				Card: json.RawMessage(`{"skills":["file"]}`),
			}}, nil
		},
	}
	tool := toolByName(t, newTestTools(t, sdk), "write_slave_file")
	big := strings.Repeat("a", 5000) // > 4 KiB default
	args, _ := json.Marshal(map[string]string{
		"target_display_name": "slave-b", "path": "x", "content": big,
	})
	_, err := tool.Call(context.Background(), args)
	require.Error(t, err)
	require.Contains(t, err.Error(), "inline_max_bytes")
}

func TestWriteSlaveFile_SourceBlobLooksUpAndSendsBase64(t *testing.T) {
	var captured agentsdk.DelegateTaskRequest
	sdk := newAvailableFileSlaveSDK(t,
		`{"path":"/abs/out.bin","bytes_written":3,"mode":"overwrite"}`, &captured)
	tools := newTestTools(t, sdk)
	// Seed a blob in the registry by writing a temp file and registering it.
	tmp := filepath.Join(t.TempDir(), "src")
	require.NoError(t, os.WriteFile(tmp, []byte{0x00, 0xff, 0x42}, 0o644))
	sha, _, _, err := tools.reg.RegisterFile(tmp)
	require.NoError(t, err)

	tool := toolByName(t, tools, "write_slave_file")
	raw, err := tool.Call(context.Background(), json.RawMessage(
		`{"target_display_name":"slave-b","path":"out.bin","source_blob":"sha256:`+sha+`"}`))
	require.NoError(t, err)

	// The forwarded prompt must use encoding=base64 regardless of what caller passed.
	var slavePrompt map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(captured.Prompt), &slavePrompt))
	require.Equal(t, "base64", slavePrompt["encoding"])
	decoded, _ := base64.StdEncoding.DecodeString(slavePrompt["content"].(string))
	require.Equal(t, []byte{0x00, 0xff, 0x42}, decoded)

	var out map[string]interface{}
	json.Unmarshal(raw, &out)
	require.Equal(t, "source_blob:sha256:"+sha, out["source"])
}

func TestWriteSlaveFile_SourceBlobUnknownSha(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{{
				AgentID: "slave-b", DisplayName: "slave-b", Status: "available",
				Card: json.RawMessage(`{"skills":["file"]}`),
			}}, nil
		},
	}
	tool := toolByName(t, newTestTools(t, sdk), "write_slave_file")
	_, err := tool.Call(context.Background(), json.RawMessage(
		`{"target_display_name":"slave-b","path":"x","source_blob":"sha256:deadbeef"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "source_blob")
}

func TestWriteSlaveFile_SourcePathRegistersAndUploads(t *testing.T) {
	var captured agentsdk.DelegateTaskRequest
	sdk := newAvailableFileSlaveSDK(t,
		`{"path":"/abs/out","bytes_written":4,"mode":"overwrite"}`, &captured)
	tools := newTestTools(t, sdk)
	src := filepath.Join(t.TempDir(), "src")
	require.NoError(t, os.WriteFile(src, []byte("ABCD"), 0o644))
	tool := toolByName(t, tools, "write_slave_file")
	args, _ := json.Marshal(map[string]string{
		"target_display_name": "slave-b", "path": "out", "source_path": src,
	})
	raw, err := tool.Call(context.Background(), args)
	require.NoError(t, err)
	var slavePrompt map[string]interface{}
	json.Unmarshal([]byte(captured.Prompt), &slavePrompt)
	require.Equal(t, "base64", slavePrompt["encoding"])
	decoded, _ := base64.StdEncoding.DecodeString(slavePrompt["content"].(string))
	require.Equal(t, []byte("ABCD"), decoded)

	var out map[string]interface{}
	json.Unmarshal(raw, &out)
	require.Equal(t, "source_path:"+src, out["source"])

	// Confirm the file was registered.
	sum := sha256.Sum256([]byte("ABCD"))
	_, ok := tools.reg.LookupBlob(hex.EncodeToString(sum[:]))
	require.True(t, ok)
}

func TestWriteSlaveFile_RejectsMissingFileSkill(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{{
				AgentID: "slave-b", DisplayName: "slave-b", Status: "available",
				Card: json.RawMessage(`{"skills":["chat"]}`),
			}}, nil
		},
	}
	tool := toolByName(t, newTestTools(t, sdk), "write_slave_file")
	_, err := tool.Call(context.Background(), json.RawMessage(
		`{"target_display_name":"slave-b","path":"x","content":"hi"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not advertise file")
}
```

Add the new imports near the existing block in `slave_file_tools_test.go`:

```go
import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/stretchr/testify/require"
)
```

- [ ] **Step 7.2: Run tests to verify they fail**

Run: `cd multi-agent && go test ./internal/driver/ -run TestWriteSlaveFile -v`
Expected: FAIL (no `write_slave_file` tool yet).

- [ ] **Step 7.3: Implement the tool**

Append to `multi-agent/internal/driver/slave_file_tools.go`:

```go
type writeSlaveFileTool struct{ t *Tools }

func (w *writeSlaveFileTool) Name() string { return "write_slave_file" }
func (w *writeSlaveFileTool) Description() string {
	return "Write bytes to a path on a selected slave through the file skill. Exactly one of content / source_blob / source_path must be set."
}
func (w *writeSlaveFileTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
        "target_agent_id":{"type":"string"},
        "target_display_name":{"type":"string"},
        "path":{"type":"string"},
        "content":{"type":"string"},
        "source_blob":{"type":"string"},
        "source_path":{"type":"string"},
        "encoding":{"type":"string","enum":["utf-8","base64"]},
        "mode":{"type":"string","enum":["overwrite","append","create_new","patch"]},
        "mkdir":{"type":"boolean"},
        "offset":{"type":"integer","minimum":0}
    },"required":["path"],"additionalProperties":false}`)
}
func (w *writeSlaveFileTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		TargetAgentID     string `json:"target_agent_id"`
		TargetDisplayName string `json:"target_display_name"`
		Path              string `json:"path"`
		Content           *string `json:"content,omitempty"`
		SourceBlob        string `json:"source_blob,omitempty"`
		SourcePath        string `json:"source_path,omitempty"`
		Encoding          string `json:"encoding,omitempty"`
		Mode              string `json:"mode,omitempty"`
		Mkdir             bool   `json:"mkdir,omitempty"`
		Offset            *int64 `json:"offset,omitempty"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &MCPToolError{Message: "invalid args: " + err.Error()}
	}
	if args.Path == "" {
		return nil, &MCPToolError{Message: "path is required"}
	}
	sources := 0
	if args.Content != nil {
		sources++
	}
	if args.SourceBlob != "" {
		sources++
	}
	if args.SourcePath != "" {
		sources++
	}
	if sources != 1 {
		return nil, &MCPToolError{Message: "exactly one of content / source_blob / source_path must be set"}
	}
	mode := args.Mode
	if mode == "" {
		mode = "overwrite"
	}
	if mode != "patch" && args.Offset != nil && *args.Offset != 0 {
		return nil, &MCPToolError{Message: "offset is only valid with mode=patch"}
	}
	card, err := w.t.resolveAvailableAgent(ctx, args.TargetAgentID, args.TargetDisplayName)
	if err != nil {
		return nil, err
	}
	if !hasSkill(card, "file") {
		return nil, &MCPToolError{Message: "target " + card.DisplayName + " does not advertise file"}
	}

	var (
		slaveContent  string
		slaveEncoding string
		sourceLabel   string
	)
	switch {
	case args.Content != nil:
		if int64(len(*args.Content)) > defaultInlineMaxBytes {
			return nil, &MCPToolError{Message: fmt.Sprintf(
				"inline content exceeds inline_max_bytes (%d > %d); use source_blob or source_path",
				len(*args.Content), defaultInlineMaxBytes)}
		}
		slaveContent = *args.Content
		slaveEncoding = args.Encoding
		if slaveEncoding == "" {
			slaveEncoding = "utf-8"
		}
		sourceLabel = "content"
	case args.SourceBlob != "":
		sha := strings.TrimPrefix(args.SourceBlob, "sha256:")
		path, ok := w.t.reg.LookupBlob(sha)
		if !ok {
			return nil, &MCPToolError{Message: "source_blob " + args.SourceBlob + " not in driver FileRegistry"}
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, &MCPToolError{Message: "read source_blob: " + err.Error()}
		}
		slaveContent = base64.StdEncoding.EncodeToString(body)
		slaveEncoding = "base64"
		sourceLabel = "source_blob:" + args.SourceBlob
	case args.SourcePath != "":
		body, err := os.ReadFile(args.SourcePath)
		if err != nil {
			return nil, &MCPToolError{Message: "read source_path: " + err.Error()}
		}
		if _, _, _, err := w.t.reg.RegisterFile(args.SourcePath); err != nil {
			return nil, &MCPToolError{Message: "register source_path: " + err.Error()}
		}
		slaveContent = base64.StdEncoding.EncodeToString(body)
		slaveEncoding = "base64"
		sourceLabel = "source_path:" + args.SourcePath
	}

	prompt := map[string]interface{}{
		"op":       "write",
		"path":     args.Path,
		"content":  slaveContent,
		"encoding": slaveEncoding,
		"mode":     mode,
	}
	if args.Mkdir {
		prompt["mkdir"] = true
	}
	if mode == "patch" && args.Offset != nil {
		prompt["offset"] = *args.Offset
	}
	pb, _ := json.Marshal(prompt)
	resp, err := w.t.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID: card.AgentID, Skill: "file", Prompt: string(pb),
	})
	if err != nil {
		return nil, &MCPToolError{Message: "delegate file write: " + err.Error()}
	}
	waitOut, err := w.t.waitDelegatedTask(ctx, resp.TaskID, 0)
	if err != nil {
		return nil, err
	}
	var wrap struct {
		Output string `json:"output"`
	}
	json.Unmarshal(waitOut, &wrap)
	var slaveRes struct {
		Path         string `json:"path"`
		BytesWritten int64  `json:"bytes_written"`
		Mode         string `json:"mode"`
		Offset       *int64 `json:"offset,omitempty"`
	}
	json.Unmarshal([]byte(wrap.Output), &slaveRes)

	out := map[string]interface{}{
		"task_id":             resp.TaskID,
		"target_display_name": card.DisplayName,
		"slave_path":          slaveRes.Path,
		"bytes_written":       slaveRes.BytesWritten,
		"mode":                slaveRes.Mode,
		"source":              sourceLabel,
	}
	if slaveRes.Offset != nil {
		out["offset"] = *slaveRes.Offset
	}
	return json.Marshal(out)
}
```

Register in `tools.go::Tools.All()` right after `&readSlaveFileTool{t}`:

```go
		&readSlaveFileTool{t},
		&writeSlaveFileTool{t},
```

Now that `fmt` and `strings` are used by Task 7, delete the placeholder `var _ = strings.Contains` / `var _ = fmt.Sprint` lines added in Task 6.

- [ ] **Step 7.4: Run tests**

Run: `cd multi-agent && go test ./internal/driver/ -run TestWriteSlaveFile -v`
Expected: all PASS.

- [ ] **Step 7.5: Commit**

```bash
git add multi-agent/internal/driver/slave_file_tools.go multi-agent/internal/driver/slave_file_tools_test.go multi-agent/internal/driver/tools.go
git -c commit.gpgsign=false commit -m "feat(driver): write_slave_file with content/source_blob/source_path"
```

---

## Task 8: Driver `stat_slave_file`

**Files:**
- Modify: `multi-agent/internal/driver/slave_file_tools.go`
- Modify: `multi-agent/internal/driver/slave_file_tools_test.go`
- Modify: `multi-agent/internal/driver/tools.go`

- [ ] **Step 8.1: Write failing tests**

Append to `slave_file_tools_test.go`:

```go
func TestStatSlaveFile_PassesThroughResult(t *testing.T) {
	var captured agentsdk.DelegateTaskRequest
	sdk := newAvailableFileSlaveSDK(t,
		`{"path":"/abs/f","exists":true,"size":42,"mode":"0644","is_dir":false,"mtime":"2026-05-21T10:00:00Z"}`,
		&captured)
	tool := toolByName(t, newTestTools(t, sdk), "stat_slave_file")
	raw, err := tool.Call(context.Background(),
		json.RawMessage(`{"target_display_name":"slave-b","path":"f"}`))
	require.NoError(t, err)
	require.JSONEq(t, `{"op":"stat","path":"f"}`, captured.Prompt)
	var out map[string]interface{}
	json.Unmarshal(raw, &out)
	require.Equal(t, true, out["exists"])
	require.EqualValues(t, 42, out["size"])
	require.Equal(t, "/abs/f", out["slave_path"])
	require.Equal(t, "task-w1", out["task_id"])
}

func TestStatSlaveFile_RejectsMissingFileSkill(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{{
				AgentID: "slave-b", DisplayName: "slave-b", Status: "available",
				Card: json.RawMessage(`{"skills":["chat"]}`),
			}}, nil
		},
	}
	tool := toolByName(t, newTestTools(t, sdk), "stat_slave_file")
	_, err := tool.Call(context.Background(),
		json.RawMessage(`{"target_display_name":"slave-b","path":"f"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not advertise file")
}
```

- [ ] **Step 8.2: Run tests to verify they fail**

Run: `cd multi-agent && go test ./internal/driver/ -run TestStatSlaveFile -v`
Expected: FAIL (no `stat_slave_file` tool yet).

- [ ] **Step 8.3: Implement the tool**

Append to `slave_file_tools.go`:

```go
type statSlaveFileTool struct{ t *Tools }

func (s *statSlaveFileTool) Name() string { return "stat_slave_file" }
func (s *statSlaveFileTool) Description() string {
	return "Stat a path on a selected slave through the file skill. Returns exists=false for missing paths instead of erroring."
}
func (s *statSlaveFileTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
        "target_agent_id":{"type":"string"},
        "target_display_name":{"type":"string"},
        "path":{"type":"string"}
    },"required":["path"],"additionalProperties":false}`)
}
func (s *statSlaveFileTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		TargetAgentID     string `json:"target_agent_id"`
		TargetDisplayName string `json:"target_display_name"`
		Path              string `json:"path"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &MCPToolError{Message: "invalid args: " + err.Error()}
	}
	if args.Path == "" {
		return nil, &MCPToolError{Message: "path is required"}
	}
	card, err := s.t.resolveAvailableAgent(ctx, args.TargetAgentID, args.TargetDisplayName)
	if err != nil {
		return nil, err
	}
	if !hasSkill(card, "file") {
		return nil, &MCPToolError{Message: "target " + card.DisplayName + " does not advertise file"}
	}
	prompt, _ := json.Marshal(map[string]string{"op": "stat", "path": args.Path})
	resp, err := s.t.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID: card.AgentID, Skill: "file", Prompt: string(prompt),
	})
	if err != nil {
		return nil, &MCPToolError{Message: "delegate file stat: " + err.Error()}
	}
	waitOut, err := s.t.waitDelegatedTask(ctx, resp.TaskID, 0)
	if err != nil {
		return nil, err
	}
	var wrap struct {
		Output string `json:"output"`
	}
	json.Unmarshal(waitOut, &wrap)
	var slaveRes map[string]interface{}
	json.Unmarshal([]byte(wrap.Output), &slaveRes)
	out := map[string]interface{}{
		"task_id":             resp.TaskID,
		"target_display_name": card.DisplayName,
	}
	for k, v := range slaveRes {
		if k == "path" {
			out["slave_path"] = v
			continue
		}
		out[k] = v
	}
	return json.Marshal(out)
}
```

Register in `tools.go::Tools.All()`:

```go
		&writeSlaveFileTool{t},
		&statSlaveFileTool{t},
```

- [ ] **Step 8.4: Run tests**

Run: `cd multi-agent && go test ./internal/driver/ -run TestStatSlaveFile -v && go test ./internal/driver/ -count=1`
Expected: targeted tests PASS, then the full package PASSES.

- [ ] **Step 8.5: Commit**

```bash
git add multi-agent/internal/driver/slave_file_tools.go multi-agent/internal/driver/slave_file_tools_test.go multi-agent/internal/driver/tools.go
git -c commit.gpgsign=false commit -m "feat(driver): stat_slave_file pass-through tool"
```

---

## Task 9: Docs — slave-skills.md, slave README, driver README

**Files:**
- Modify: `skills/multiagent/references/slave-skills.md`
- Modify: `multi-agent/cmd/slave-agent/README.md`
- Modify: `multi-agent/cmd/driver-agent/README.md`

- [ ] **Step 9.1: Add `file` to the slave-skills list and section**

In `skills/multiagent/references/slave-skills.md`, near the top where skills are listed, add a bullet between `bash` and `claude_permissions`:

```markdown
- `file`: stateless file read/write/stat through a native slave-agent executor.
```

Then add a new section before the existing `## bash` section (or right after, the order with `bash` is fine):

````markdown
## `file`

Stateless file I/O through a native `slave-agent` Go executor. Advertised by adding `file` to `discovery.skills`. The prompt is JSON with an `op` discriminator. Same trust model as `bash`: a slave that advertises `file` is granting access to any path its OS user can reach.

### `op: "read"`

```json
{
  "op": "read",
  "path": "data/in.csv",
  "offset": 0,
  "length": 65536,
  "encoding": "utf-8"
}
```

- `path` resolves against `claude.workdir` if relative; absolute paths used as-is.
- `encoding`: `"utf-8"` (default; rejects invalid UTF-8) or `"base64"` (binary-safe).
- `offset` / `length` optional; reads to EOF if `length` unset.
- Hard cap: one read returns ≤ 8 MiB. Chunk by raising `offset`.

Result: `{path, bytes, encoding, content, eof}`.

### `op: "write"`

```json
{
  "op": "write",
  "path": "data/out.txt",
  "content": "hello\n",
  "encoding": "utf-8",
  "mode": "overwrite",
  "mkdir": true,
  "offset": 0
}
```

Modes: `overwrite` (truncate+write), `append` (`O_APPEND`), `create_new` (`O_EXCL`, errors if file exists), `patch` (writes at `offset` without truncating; zero-fills if `offset > size`). `offset` is rejected on non-patch modes.

Result: `{path, bytes_written, mode, offset?}`.

### `op: "stat"`

```json
{"op":"stat","path":"data/out.txt"}
```

Returns `{path, exists, size?, mode?, is_dir?, mtime?}`. Missing paths return `exists:false` (not an error) so callers can probe "should I write here?" cheaply.

### Driver-side tools

The driver exposes `read_slave_file`, `write_slave_file`, and `stat_slave_file`. They keep bytes out of the LLM context: `read_slave_file` caches in the driver's `FileRegistry` and returns a `sha256` / `blob_handle` / `cache_path`; `write_slave_file` accepts `source_blob` (a prior handle) or `source_path` (a driver-local path) so the LLM never carries large payloads as tool arguments.
````

- [ ] **Step 9.2: Add `file` to the slave-agent README skill list**

In `multi-agent/cmd/slave-agent/README.md`, find the YAML block listing skills (around the lines `- chat`, `- mcp`, `- register_mcp`, `- bash`, `- claude_permissions`) and append:

```yaml
    - file
```

Then in the explanatory paragraph below the YAML block (the one starting `register_mcp registers a pre-built MCP server file...`), add a sentence:

> `file` enables stateless deterministic file read/write/stat through a native `slave-agent` executor; same trust model as `bash`.

- [ ] **Step 9.3: Add three new tools to the driver-agent README**

In `multi-agent/cmd/driver-agent/README.md`, under the "Slave control helpers" bullets (`run_slave_bash`, `get_slave_claude_permissions`, `update_slave_claude_permissions`), append:

```markdown
- `read_slave_file`: reads a path on a slave that advertises `file`. Bytes are cached in the driver's blob store and returned as a `sha256` handle plus `cache_path`; inline `content` is included only when ≤ 4 KiB and utf-8.
- `write_slave_file`: writes to a path on a slave that advertises `file`. Pass exactly one of `content` (inline, ≤ 4 KiB), `source_blob` (a sha256 returned from a prior tool), or `source_path` (a driver-local absolute path).
- `stat_slave_file`: stats a path on a slave that advertises `file`. Returns `exists:false` for missing paths rather than erroring.
```

After the existing "Example permission patch" and "Example Bash execution" blocks, add:

````markdown
Example chained slave-A → slave-B file transfer (LLM never carries bytes):

```json
{"target_display_name":"slave-a","path":"data/big.bin","encoding":"base64"}
```
→ returns `{"sha256":"abc...","blob_handle":"sha256:abc...","cache_path":"..."}`

```json
{"target_display_name":"slave-b","path":"incoming/big.bin","source_blob":"sha256:abc...","mkdir":true}
```
````

- [ ] **Step 9.4: Confirm full repo build + tests**

Run: `cd multi-agent && go build ./... && go test ./... -count=1`
Expected: build clean, all tests PASS.

- [ ] **Step 9.5: Commit**

```bash
git add skills/multiagent/references/slave-skills.md multi-agent/cmd/slave-agent/README.md multi-agent/cmd/driver-agent/README.md
git -c commit.gpgsign=false commit -m "docs: document file skill (slave executor + driver tools)"
```

---

## Self-Review Checklist (do this before declaring done)

- **Spec coverage:**
  - `op:read` whole/offset/length/utf8/base64/cap/missing/absolute — Task 1+2. ✓
  - `op:write` overwrite/append/create_new/patch + mkdir + base64 + offset-only-patch — Task 3. ✓
  - `op:stat` exists / missing / dir — Task 4. ✓
  - Slave wiring + config + main_test — Task 5. ✓
  - Driver `read_slave_file` cache + register + handle + inline rules + audit — Task 6. ✓
  - Driver `write_slave_file` 3 source modes + mutual exclusion + inline cap + offset/patch rule + missing skill — Task 7. ✓
  - Driver `stat_slave_file` pass-through + missing skill — Task 8. ✓
  - Docs: slave-skills.md / slave README / driver README — Task 9. ✓
- **Naming consistency:** `FileExecutor`, `FileConfig`, `NewFileExecutor`, `FileReadResult`, `FileWriteResult`, `FileStatResult`, `readSlaveFileTool`, `writeSlaveFileTool`, `statSlaveFileTool`, `fileCacheRoot`, `cacheBytes`, `defaultInlineMaxBytes`. Cross-referenced; no drift.
- **Trust-model statement** in slave-skills.md docs aligns with spec.
- **`var _ = strings.Contains` / `var _ = fmt.Sprint`** placeholders from Task 6 are removed in Task 7 once those imports are genuinely used.
- **Audit `register_read`** uses `PeerShortID = cardShortID(card)` and includes `TaskID` — consistent with the existing fetch_blob / put_blob events that carry the same fields.
