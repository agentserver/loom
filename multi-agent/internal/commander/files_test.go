package commander

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

func handlerForFileRoot(root string) *Handler {
	return &Handler{Backend: &fakeBackend{
		getFn: func(context.Context, string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
			return agentbackend.Session{ID: "s1", WorkingDir: root}, nil, nil
		},
	}}
}

func TestHandlerListFilesUsesSessionWorkingDirRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "internal"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "hidden.txt"), []byte("no recursion\n"), 0644); err != nil {
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
	if len(got.Entries) != 2 {
		t.Fatalf("entries=%+v", got.Entries)
	}
	if got.Entries[0].Name != "internal" || got.Entries[0].Kind != "dir" || got.Entries[0].Path != "internal" {
		t.Fatalf("first entry=%+v", got.Entries[0])
	}
	if got.Entries[1].Name != "go.mod" || got.Entries[1].Kind != "file" || got.Entries[1].Path != "go.mod" {
		t.Fatalf("second entry=%+v", got.Entries[1])
	}
	if got.Entries[0].ModTime == "" || got.Entries[1].ModTime == "" {
		t.Fatalf("mod times missing: %+v", got.Entries)
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

func TestHandlerReadFileRejectsAbsolutePath(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "README.md")
	if err := os.WriteFile(path, []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	h := handlerForFileRoot(root)

	_, err := h.ReadFile(context.Background(), "s1", path)

	if err == nil || !strings.Contains(err.Error(), "outside session root") {
		t.Fatalf("err=%v want outside session root", err)
	}
}

func TestHandlerReadFileRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "link.txt")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable on windows: %v", err)
		}
		t.Fatal(err)
	}
	h := handlerForFileRoot(root)

	_, err := h.ReadFile(context.Background(), "s1", "link.txt")

	if err == nil || !strings.Contains(err.Error(), "outside session root") {
		t.Fatalf("err=%v want outside session root", err)
	}
}

func TestHandlerReadFileRejectsDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "internal"), 0755); err != nil {
		t.Fatal(err)
	}
	h := handlerForFileRoot(root)

	_, err := h.ReadFile(context.Background(), "s1", "internal")

	if err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("err=%v want directory error", err)
	}
}

func TestHandlerReadFileCapsPreviewAtTwoMB(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "large.log")
	if err := os.WriteFile(path, make([]byte, int(MaxFilePreviewBytes)+1), 0644); err != nil {
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
	if got.Binary {
		t.Fatalf("too-large result should not be marked binary: %+v", got)
	}
}

func TestHandlerReadFileAllowsExactPreviewCap(t *testing.T) {
	// Use a file small enough that the JSON-encoded FileReadResult stays
	// under maxEncodedFileResponse (768 KiB). Pure ASCII expands 1:1 in JSON,
	// so 400 KiB of 'a' bytes encodes well under the 768 KiB wire cap.
	root := t.TempDir()
	contentSize := 400 * 1024 // 400 KiB — fits within maxEncodedFileResponse
	content := bytes.Repeat([]byte("a"), contentSize)
	if err := os.WriteFile(filepath.Join(root, "exact.txt"), content, 0644); err != nil {
		t.Fatal(err)
	}
	h := handlerForFileRoot(root)

	got, err := h.ReadFile(context.Background(), "s1", "exact.txt")
	if err != nil {
		t.Fatal(err)
	}

	if got.TooLarge || got.Binary || got.Size != int64(contentSize) || len(got.Content) != contentSize {
		t.Fatalf("too_large=%v binary=%v size=%d content_len=%d", got.TooLarge, got.Binary, got.Size, len(got.Content))
	}
}

func TestHandlerReadFileRejectsNonRegularFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket mode is not portable to windows")
	}
	root := t.TempDir()
	socketPath := filepath.Join(root, "preview.sock")
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	h := handlerForFileRoot(root)

	_, err = h.ReadFile(context.Background(), "s1", "preview.sock")

	if err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("err=%v want regular file error", err)
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

func TestHandlerReadFileReturnsTextContent(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	h := &Handler{Backend: &fakeBackend{
		getFn: func(context.Context, string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
			return agentbackend.Session{ID: "s1", WorkingDir: root}, nil, nil
		},
	}}

	got, err := h.ReadFile(context.Background(), "s1", "README.md")
	if err != nil {
		t.Fatal(err)
	}

	if got.Content != "# hello\n" || got.Binary || got.TooLarge || got.Size != int64(len("# hello\n")) {
		t.Fatalf("result=%+v", got)
	}
	if got.MIME == "" {
		t.Fatalf("MIME missing: %+v", got)
	}
}

func TestHandlerReadFileRejectsUnknownWorkingDir(t *testing.T) {
	h := &Handler{Backend: &fakeBackend{
		getFn: func(context.Context, string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
			return agentbackend.Session{ID: "s1"}, nil, nil
		},
	}}

	_, err := h.ReadFile(context.Background(), "s1", "README.md")

	if err == nil || !strings.Contains(err.Error(), "working directory") {
		t.Fatalf("err=%v want working directory error", err)
	}
}

func TestHandlerListFilesListsNestedRelativeDirectory(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "internal", "commander")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "files.go"), []byte("package commander\n"), 0644); err != nil {
		t.Fatal(err)
	}
	h := handlerForFileRoot(root)

	got, err := h.ListFiles(context.Background(), "s1", "./internal/commander/../commander")
	if err != nil {
		t.Fatal(err)
	}

	if got.Path != "internal/commander" {
		t.Fatalf("path=%q want internal/commander", got.Path)
	}
	if len(got.Entries) != 1 || got.Entries[0].Path != "internal/commander/files.go" {
		t.Fatalf("entries=%+v", got.Entries)
	}
}

func TestHandlerListFilesSortsDirsBeforeFilesCaseInsensitive(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"beta", "Alpha"} {
		if err := os.Mkdir(filepath.Join(root, name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"Zoo.txt", "apple.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	h := handlerForFileRoot(root)

	got, err := h.ListFiles(context.Background(), "s1", ".")
	if err != nil {
		t.Fatal(err)
	}

	var names []string
	for _, entry := range got.Entries {
		names = append(names, entry.Name)
	}
	want := []string{"Alpha", "beta", "apple.txt", "Zoo.txt"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("names=%v want %v", names, want)
	}
}

func TestReadFile_EncodedSizeCapPreventsControlByteBlowup(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "tricky.txt")
	tricky := bytes.Repeat([]byte{0x01}, 1024*1024)
	if err := os.WriteFile(path, tricky, 0o644); err != nil {
		t.Fatal(err)
	}

	h := handlerForFileRoot(root)
	res, err := h.ReadFile(context.Background(), "s1", "tricky.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !res.TooLarge {
		t.Fatalf("expected TooLarge=true; got Content len=%d, Binary=%v", len(res.Content), res.Binary)
	}
	if res.Content != "" {
		t.Fatalf("expected Content empty when TooLarge; got len=%d", len(res.Content))
	}

	out, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if int64(len(out)) > 1<<20 {
		t.Fatalf("encoded FileReadResult = %d bytes exceeds 1 MiB cap", len(out))
	}
}
