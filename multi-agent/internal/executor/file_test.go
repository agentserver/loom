package executor

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
	// e.workDir is filepath.EvalSymlinks-resolved at constructor time so
	// the jail check and the candidate path share a reference frame. On
	// Windows runners workdir is often an 8.3 short name like RUNNER~1
	// that resolves to RUNNERADMIN; resolve the same way for comparison.
	wantWorkDir, err := filepath.EvalSymlinks(workdir)
	if err != nil {
		wantWorkDir = workdir
	}
	if got.Path != filepath.Join(wantWorkDir, "in.txt") {
		t.Fatalf("path = %q, want %q", got.Path, filepath.Join(wantWorkDir, "in.txt"))
	}
}

func TestFileExecutor_ReadWithOffsetAndLength(t *testing.T) {
	workdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workdir, "in.txt"), []byte("abcdefghij"), 0o644); err != nil {
		t.Fatal(err)
	}
	exec := NewFileExecutor(FileConfig{WorkDir: workdir})
	res, err := exec.Run(context.Background(), Task{
		Prompt: `{"op":"read","path":"in.txt","offset":2,"length":4}`,
	}, noopSink{})
	if err != nil {
		t.Fatal(err)
	}
	var got FileReadResult
	if err := json.Unmarshal([]byte(res.Summary), &got); err != nil {
		t.Fatalf("summary not FileReadResult JSON: %v\n%s", err, res.Summary)
	}
	if got.Content != "cdef" || got.Bytes != 4 || got.EOF {
		t.Fatalf("got %+v", got)
	}
}

func TestFileExecutor_ReadBase64BinarySafe(t *testing.T) {
	workdir := t.TempDir()
	raw := []byte{0x00, 0xff, 0x10, 0x80}
	if err := os.WriteFile(filepath.Join(workdir, "bin"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	exec := NewFileExecutor(FileConfig{WorkDir: workdir})
	res, err := exec.Run(context.Background(), Task{
		Prompt: `{"op":"read","path":"bin","encoding":"base64"}`,
	}, noopSink{})
	if err != nil {
		t.Fatal(err)
	}
	var got FileReadResult
	if err := json.Unmarshal([]byte(res.Summary), &got); err != nil {
		t.Fatalf("summary not FileReadResult JSON: %v\n%s", err, res.Summary)
	}
	decoded, _ := base64.StdEncoding.DecodeString(got.Content)
	if !bytes.Equal(decoded, raw) {
		t.Fatalf("base64 roundtrip failed: %v vs %v", decoded, raw)
	}
}

func TestFileExecutor_ReadInvalidUTF8Rejected(t *testing.T) {
	workdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workdir, "bad"), []byte{0xff, 0xfe}, 0o644); err != nil {
		t.Fatal(err)
	}
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
	if err := os.WriteFile(filepath.Join(workdir, "big"), big, 0o644); err != nil {
		t.Fatal(err)
	}
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
	// Absolute paths that resolve inside the configured WorkDir are allowed;
	// abs paths outside the jail are rejected by TestFileExecutor_RejectsAbsolutePathOutsideJail.
	workdir := t.TempDir()
	abs := filepath.Join(workdir, "abs.txt")
	if err := os.WriteFile(abs, []byte("xyz"), 0o644); err != nil {
		t.Fatal(err)
	}
	exec := NewFileExecutor(FileConfig{WorkDir: workdir})
	res, err := exec.Run(context.Background(), Task{
		Prompt: fmt.Sprintf(`{"op":"read","path":%q}`, abs),
	}, noopSink{})
	if err != nil {
		t.Fatal(err)
	}
	var got FileReadResult
	if err := json.Unmarshal([]byte(res.Summary), &got); err != nil {
		t.Fatalf("summary not FileReadResult JSON: %v\n%s", err, res.Summary)
	}
	// Same EvalSymlinks normalization as TestFileExecutor_ReadWholeFile_UTF8;
	// got.Path is reported in the symlink-resolved frame (e.workDir).
	wantPath := abs
	if resolved, err := filepath.EvalSymlinks(workdir); err == nil {
		wantPath = filepath.Join(resolved, "abs.txt")
	}
	if got.Path != wantPath || got.Content != "xyz" {
		t.Fatalf("got %+v want path %q", got, wantPath)
	}
}

// TestFileExecutor_RejectsAbsolutePathOutsideJail pins §1.4 #14:
// LLM cannot read /etc/passwd / /root/.ssh/authorized_keys via abs path.
func TestFileExecutor_RejectsAbsolutePathOutsideJail(t *testing.T) {
	workdir := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	exec := NewFileExecutor(FileConfig{WorkDir: workdir})
	_, err := exec.Run(context.Background(), Task{
		ID: "t", Skill: "file",
		Prompt: fmt.Sprintf(`{"op":"read","path":%q}`, outsideFile),
	}, noopSink{})
	if err == nil {
		t.Fatal("expected error reading outside-jail abs path via FileExecutor; got nil")
	}
	if !strings.Contains(err.Error(), "jail") && !strings.Contains(err.Error(), "escape") {
		t.Fatalf("expected jail-related error, got %v", err)
	}
}

// TestFileExecutor_RejectsSymlinkLeapingOutOfJail: a symlink inside the
// jail that points outside must be rejected when read through.
func TestFileExecutor_RejectsSymlinkLeapingOutOfJail(t *testing.T) {
	workdir := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(workdir, "link")
	if err := os.Symlink(outsideFile, linkPath); err != nil {
		t.Fatal(err)
	}
	exec := NewFileExecutor(FileConfig{WorkDir: workdir})
	_, err := exec.Run(context.Background(), Task{
		ID: "t", Skill: "file",
		Prompt: `{"op":"read","path":"link"}`,
	}, noopSink{})
	if err == nil {
		t.Fatal("expected error reading symlink that leaps out of jail; got nil")
	}
	if !strings.Contains(err.Error(), "jail") && !strings.Contains(err.Error(), "escape") {
		t.Fatalf("expected jail error, got %v", err)
	}
}

// TestFileExecutor_AcceptsAbsolutePathInsideJail (positive): an absolute
// path that resolves under WorkDir is OK.
func TestFileExecutor_AcceptsAbsolutePathInsideJail(t *testing.T) {
	workdir := t.TempDir()
	abs := filepath.Join(workdir, "inside.txt")
	if err := os.WriteFile(abs, []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exec := NewFileExecutor(FileConfig{WorkDir: workdir})
	res, err := exec.Run(context.Background(), Task{
		ID: "t", Skill: "file",
		Prompt: fmt.Sprintf(`{"op":"read","path":%q}`, abs),
	}, noopSink{})
	if err != nil {
		t.Fatalf("absolute path under jail should be allowed: %v", err)
	}
	if !strings.Contains(res.Summary, "ok") {
		t.Fatalf("read result: %q", res.Summary)
	}
}

// TestFileExecutor_AcceptsReadWhenWorkDirIsSymlink pins the Critical fix
// from PR #14 review: WorkDir that resolves through a symlink (common:
// macOS /tmp→/private/tmp; Linux /bin→/usr/bin etc.) must not reject
// legitimate inside-jail reads.
func TestFileExecutor_AcceptsReadWhenWorkDirIsSymlink(t *testing.T) {
	realDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(realDir, "inside.txt"), []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Create a symlink elsewhere pointing AT realDir; pass the symlink
	// path as WorkDir.
	linkParent := t.TempDir()
	linkDir := filepath.Join(linkParent, "workdir-link")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatal(err)
	}
	exec := NewFileExecutor(FileConfig{WorkDir: linkDir})
	res, err := exec.Run(context.Background(), Task{
		ID: "t", Skill: "file",
		Prompt: `{"op":"read","path":"inside.txt"}`,
	}, noopSink{})
	if err != nil {
		t.Fatalf("read via symlinked WorkDir should succeed: %v", err)
	}
	if !strings.Contains(res.Summary, "ok") {
		t.Fatalf("read result: %q", res.Summary)
	}
}

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
	if err := json.Unmarshal([]byte(res.Summary), &got); err != nil {
		t.Fatalf("summary not FileWriteResult JSON: %v\n%s", err, res.Summary)
	}
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
	if err := os.WriteFile(filepath.Join(workdir, "log"), []byte("line1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
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
	if err := os.WriteFile(filepath.Join(workdir, "exist"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
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
	if err := os.WriteFile(filepath.Join(workdir, "f"), []byte("AAAAAAAAAA"), 0o644); err != nil {
		t.Fatal(err)
	}
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

func TestFileExecutor_WritePatchWithZeroOffsetOK(t *testing.T) {
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

func TestFileExecutor_StatExisting(t *testing.T) {
	workdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workdir, "f"), []byte("abcde"), 0o644); err != nil {
		t.Fatal(err)
	}
	exec := NewFileExecutor(FileConfig{WorkDir: workdir})
	res, err := exec.Run(context.Background(), Task{
		Prompt: `{"op":"stat","path":"f"}`,
	}, noopSink{})
	if err != nil {
		t.Fatal(err)
	}
	var got FileStatResult
	if err := json.Unmarshal([]byte(res.Summary), &got); err != nil {
		t.Fatalf("summary not FileStatResult JSON: %v\n%s", err, res.Summary)
	}
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
	if err := json.Unmarshal([]byte(res.Summary), &got); err != nil {
		t.Fatalf("summary not FileStatResult JSON: %v\n%s", err, res.Summary)
	}
	if got.Exists {
		t.Fatalf("expected exists=false, got %+v", got)
	}
}

func TestFileExecutor_StatDirectory(t *testing.T) {
	workdir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workdir, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	exec := NewFileExecutor(FileConfig{WorkDir: workdir})
	res, err := exec.Run(context.Background(), Task{
		Prompt: `{"op":"stat","path":"d"}`,
	}, noopSink{})
	if err != nil {
		t.Fatal(err)
	}
	var got FileStatResult
	if err := json.Unmarshal([]byte(res.Summary), &got); err != nil {
		t.Fatalf("summary not FileStatResult JSON: %v\n%s", err, res.Summary)
	}
	if !got.Exists || !got.IsDir {
		t.Fatalf("got %+v", got)
	}
}
