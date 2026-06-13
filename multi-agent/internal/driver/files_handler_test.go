package driver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestHandler(t *testing.T) (*FilesHandler, *FileRegistry, *AuditLog) {
	t.Helper()
	dir := t.TempDir()
	a, err := NewAuditLog(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	r := NewFileRegistry(50000)
	h := NewFilesHandler(r, a)
	return h, r, a
}

func reqWithPeer(method, path string, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, path, body)
	r.Header.Set("X-Agentserver-Peer-Short-Id", "slv-test")
	return r
}

func TestFilesHandler_Blob_Streams(t *testing.T) {
	h, r, _ := newTestHandler(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "x.bin")
	body := []byte("hello world")
	os.WriteFile(p, body, 0o644)
	sha, _, _, _ := r.RegisterFile(p)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, reqWithPeer("GET", "/files/blob/"+sha, nil))
	if w.Code != 200 {
		t.Fatalf("status: %d (body=%s)", w.Code, w.Body.String())
	}
	if w.Body.String() != "hello world" {
		t.Errorf("body: %q", w.Body.String())
	}
}

func TestFilesHandler_Blob_404(t *testing.T) {
	h, _, _ := newTestHandler(t)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, reqWithPeer("GET", "/files/blob/nope", nil))
	if w.Code != 404 {
		t.Errorf("status: %d", w.Code)
	}
}

func TestFilesHandler_RequiresPeerHeader(t *testing.T) {
	h, _, _ := newTestHandler(t)
	r := httptest.NewRequest("GET", "/files/blob/x", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Errorf("missing-peer should be 401, got %d", w.Code)
	}
}

func TestFilesHandler_Dir_ListRecursive(t *testing.T) {
	h, r, _ := newTestHandler(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi"), 0o644)
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "sub", "b.txt"), []byte("yo"), 0o644)
	tok := r.RegisterDir(dir)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, reqWithPeer("GET", "/files/dir/"+tok+"?recursive=true", nil))
	if w.Code != 200 {
		t.Fatalf("status: %d (body=%s)", w.Code, w.Body.String())
	}
	var got struct {
		Root    string `json:"root"`
		Entries []struct {
			RelPath string `json:"relpath"`
			IsDir   bool   `json:"is_dir"`
			SHA256  string `json:"sha256"`
			Size    int64  `json:"size"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Root != dir {
		t.Errorf("root: %s", got.Root)
	}
	rels := map[string]bool{}
	for _, e := range got.Entries {
		rels[e.RelPath] = true
	}
	if !rels["a.txt"] || !rels[filepath.Join("sub", "b.txt")] {
		t.Errorf("entries missing: %+v", got.Entries)
	}
}

func TestFilesHandler_Dir_NonRecursiveOnlyTopLevel(t *testing.T) {
	h, r, _ := newTestHandler(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi"), 0o644)
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "sub", "b.txt"), []byte("yo"), 0o644)
	tok := r.RegisterDir(dir)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, reqWithPeer("GET", "/files/dir/"+tok, nil))
	var got struct {
		Entries []struct{ RelPath string `json:"relpath"` } `json:"entries"`
	}
	json.Unmarshal(w.Body.Bytes(), &got)
	for _, e := range got.Entries {
		if strings.Contains(e.RelPath, string(filepath.Separator)) {
			t.Errorf("non-recursive returned nested entry: %s", e.RelPath)
		}
	}
}

func TestFilesHandler_DirBlob_RejectsEscape(t *testing.T) {
	h, r, _ := newTestHandler(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi"), 0o644)
	tok := r.RegisterDir(dir)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, reqWithPeer("GET", "/files/dir/"+tok+"/blob?path=../etc/passwd", nil))
	if w.Code != http.StatusBadRequest {
		t.Errorf("escape: status %d (body=%s)", w.Code, w.Body.String())
	}
}

func TestFilesHandler_DirBlob_StreamsHappy(t *testing.T) {
	h, r, _ := newTestHandler(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi"), 0o644)
	tok := r.RegisterDir(dir)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, reqWithPeer("GET", "/files/dir/"+tok+"/blob?path=a.txt", nil))
	if w.Code != 200 {
		t.Fatalf("status %d (body=%s)", w.Code, w.Body.String())
	}
	if w.Body.String() != "hi" {
		t.Errorf("body: %q", w.Body.String())
	}
}

func TestFilesHandler_Put_Atomic(t *testing.T) {
	h, r, _ := newTestHandler(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "out.txt")
	tok := r.RegisterWrite(target, true, "task-1")
	r.TrackTask("task-1", []string{tok})

	w := httptest.NewRecorder()
	h.ServeHTTP(w, reqWithPeer("PUT", "/files/put/"+tok, strings.NewReader("hello world")))
	if w.Code != 200 {
		t.Fatalf("status %d (body=%s)", w.Code, w.Body.String())
	}
	got, _ := os.ReadFile(target)
	if string(got) != "hello world" {
		t.Errorf("file: %q", got)
	}
	written := r.WrittenFiles("task-1")
	if len(written) != 1 || written[0].Bytes != 11 {
		t.Errorf("WrittenFiles: %+v", written)
	}
	want := sha256.Sum256([]byte("hello world"))
	if written[0].SHA256 != hex.EncodeToString(want[:]) {
		t.Errorf("sha mismatch: %s", written[0].SHA256)
	}
}

func TestFilesHandler_Put_OverwriteFalse_Conflict(t *testing.T) {
	h, r, _ := newTestHandler(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "out.txt")
	os.WriteFile(target, []byte("existing"), 0o644)
	tok := r.RegisterWrite(target, false, "task-1")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, reqWithPeer("PUT", "/files/put/"+tok, strings.NewReader("new")))
	if w.Code != http.StatusConflict {
		t.Fatalf("status %d", w.Code)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "existing" {
		t.Errorf("file overwritten: %q", got)
	}
}

func TestFilesHandler_Put_TokenSingleUse(t *testing.T) {
	h, r, _ := newTestHandler(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "out.txt")
	tok := r.RegisterWrite(target, true, "task-1")

	w1 := httptest.NewRecorder()
	h.ServeHTTP(w1, reqWithPeer("PUT", "/files/put/"+tok, strings.NewReader("x")))
	if w1.Code != 200 {
		t.Fatalf("first put: %d", w1.Code)
	}
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, reqWithPeer("PUT", "/files/put/"+tok, strings.NewReader("y")))
	if w2.Code != http.StatusGone {
		t.Errorf("second put: want 410, got %d", w2.Code)
	}
}

// TestFilesHandler_Put_BodyOverMaxBytesRejected pins §1.4 #18: a caller
// streaming more than maxPutBytes is rejected with 413 and neither the
// target nor the tmp file survives on disk.
func TestFilesHandler_Put_BodyOverMaxBytesRejected(t *testing.T) {
	h, r, _ := newTestHandler(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "out.txt")
	tok := r.RegisterWrite(target, true, "task-1")

	// Shrink the cap so we can exercise overflow with a tiny body
	// (avoids allocating gigabytes in the test).
	h.maxPutBytes = 64

	// 100 bytes — strictly greater than the 64-byte cap.
	body := strings.Repeat("A", 100)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, reqWithPeer("PUT", "/files/put/"+tok, strings.NewReader(body)))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized PUT: want 413, got %d (body=%s)", w.Code, w.Body.String())
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("target file should not exist after rejected PUT: err=%v", err)
	}
	// Scan parent for leftover *.tmp.* siblings.
	matches, _ := filepath.Glob(filepath.Join(dir, "out.txt.tmp.*"))
	if len(matches) != 0 {
		t.Errorf("tmp leftovers after rejected PUT: %v", matches)
	}
}

func TestFilesHandler_Put_MissingParent(t *testing.T) {
	h, r, _ := newTestHandler(t)
	target := "/nonexistent/dir/out.txt"
	tok := r.RegisterWrite(target, true, "task-1")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, reqWithPeer("PUT", "/files/put/"+tok, strings.NewReader("x")))
	if w.Code != http.StatusConflict {
		t.Errorf("missing parent: status %d", w.Code)
	}
}

func TestFilesHandler_DirBlob_RejectsRootPath(t *testing.T) {
	h, r, _ := newTestHandler(t)
	dir := t.TempDir()
	tok := r.RegisterDir(dir)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, reqWithPeer("GET", "/files/dir/"+tok+"/blob?path=.", nil))
	if w.Code != http.StatusBadRequest {
		t.Errorf("path=. should return 400, got %d (body=%s)", w.Code, w.Body.String())
	}
}

func TestFilesHandler_Blob_RejectsSymlinkLeaf(t *testing.T) {
	h, r, _ := newTestHandler(t)
	dir := t.TempDir()
	real := filepath.Join(dir, "real.txt")
	os.WriteFile(real, []byte("contents"), 0o644)
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks unsupported on this fs")
	}

	// Register the symlink path. RegisterFile dereferences via os.Open, so
	// sha is computed from the target's contents. The blob path stored in
	// the registry is the symlink path itself.
	sha, _, _, err := r.RegisterFile(link)
	if err != nil {
		t.Fatalf("RegisterFile: %v", err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, reqWithPeer("GET", "/files/blob/"+sha, nil))
	if w.Code != http.StatusForbidden {
		t.Errorf("symlink leaf: status %d (body=%s, want 403)", w.Code, w.Body.String())
	}
}
