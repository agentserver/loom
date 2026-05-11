package driver

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestRegistry_RegisterFile_ComputesSHAAndDedupes(t *testing.T) {
	r := NewFileRegistry(50000)
	dir := t.TempDir()
	p := filepath.Join(dir, "x.txt")
	body := []byte("hello world")
	os.WriteFile(p, body, 0o644)

	sha1, size1, mime1, err := r.RegisterFile(p)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(body)
	wantHex := hex.EncodeToString(want[:])
	if sha1 != wantHex {
		t.Errorf("sha: got %s want %s", sha1, wantHex)
	}
	if size1 != int64(len(body)) {
		t.Errorf("size: %d", size1)
	}
	if mime1 == "" {
		t.Errorf("mime empty")
	}

	// Re-registering same file returns same sha; lookup works by sha.
	sha2, _, _, err := r.RegisterFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if sha1 != sha2 {
		t.Error("dedupe failed")
	}
	gotPath, ok := r.LookupBlob(sha1)
	if !ok || gotPath != p {
		t.Errorf("LookupBlob: ok=%v path=%s", ok, gotPath)
	}
}

func TestRegistry_RegisterDir_ReturnsTokenAndLookupReturnsRoot(t *testing.T) {
	r := NewFileRegistry(50000)
	dir := t.TempDir()
	tok := r.RegisterDir(dir)
	if tok == "" {
		t.Fatal("empty token")
	}
	root, ok := r.LookupDir(tok)
	if !ok || root != dir {
		t.Errorf("LookupDir: ok=%v root=%s want=%s", ok, root, dir)
	}
}

func TestRegistry_RegisterWrite_TokenIsSingleUse(t *testing.T) {
	r := NewFileRegistry(50000)
	tok := r.RegisterWrite("/tmp/out.txt", true, "task-1")
	if tok == "" {
		t.Fatal("empty token")
	}
	got, ok := r.ConsumeWriteToken(tok)
	if !ok || got.Path != "/tmp/out.txt" || !got.Overwrite || got.TaskID != "task-1" {
		t.Errorf("first consume: ok=%v got=%+v", ok, got)
	}
	if _, ok := r.ConsumeWriteToken(tok); ok {
		t.Error("second consume should fail (single-use)")
	}
}

func TestRegistry_DirSHACache_RoundTrip(t *testing.T) {
	r := NewFileRegistry(50000)
	tok := r.RegisterDir("/some/root")
	r.SetDirEntrySHA(tok, "sub/file.txt", "abc123", 100)
	sha, size, ok := r.GetDirEntrySHA(tok, "sub/file.txt")
	if !ok || sha != "abc123" || size != 100 {
		t.Errorf("cache: ok=%v sha=%s size=%d", ok, sha, size)
	}
}

func TestRegistry_PendingTask_TracksWrittenFiles(t *testing.T) {
	r := NewFileRegistry(50000)
	r.TrackTask("t-1", []string{"tok-a", "tok-b"})
	r.RecordWritten("t-1", WrittenFile{Path: "/out/a", Bytes: 10, SHA256: "x", WrittenAt: "2026-05-09T00:00:00Z"})
	written := r.WrittenFiles("t-1")
	if len(written) != 1 || written[0].Path != "/out/a" {
		t.Errorf("WrittenFiles: %+v", written)
	}
	r.ForgetTask("t-1")
	if w := r.WrittenFiles("t-1"); len(w) != 0 {
		t.Errorf("ForgetTask did not clear: %+v", w)
	}
}

func TestRegistry_RebindWriteTokenTaskID(t *testing.T) {
	r := NewFileRegistry(50000)
	tok := r.RegisterWrite("/p", true, "")
	r.RebindWriteTokenTaskID(tok, "task-x")
	got, ok := r.ConsumeWriteToken(tok)
	if !ok || got.TaskID != "task-x" {
		t.Errorf("rebind: ok=%v got=%+v", ok, got)
	}
}

func TestRegistry_TaskWritten_BoundedEviction(t *testing.T) {
	r := NewFileRegistry(50000)
	for i := 0; i < 300; i++ {
		r.TrackTask(fmt.Sprintf("t-%d", i), nil)
	}
	// first 44 should be evicted (300 - 256 = 44)
	if w := r.WrittenFiles("t-0"); w != nil {
		t.Errorf("t-0 should be evicted: %+v", w)
	}
	// t-299 should still be present (empty slice for key that exists)
	if w := r.WrittenFiles("t-299"); len(w) != 0 {
		t.Errorf("t-299 unexpected written files: %+v", w)
	}
}
