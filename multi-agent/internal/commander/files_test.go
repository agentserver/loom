package commander

import (
	"bytes"
	"context"
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
	root := t.TempDir()
	content := bytes.Repeat([]byte("a"), int(MaxFilePreviewBytes))
	if err := os.WriteFile(filepath.Join(root, "exact.txt"), content, 0644); err != nil {
		t.Fatal(err)
	}
	h := handlerForFileRoot(root)

	got, err := h.ReadFile(context.Background(), "s1", "exact.txt")
	if err != nil {
		t.Fatal(err)
	}

	if got.TooLarge || got.Binary || got.Size != MaxFilePreviewBytes || len(got.Content) != int(MaxFilePreviewBytes) {
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

func TestHandlerReadFileCapsEncodedSizeAtSixMB(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "control.txt")
	// Create a file with many escape-requiring characters (not null bytes, which would make it binary).
	// Use characters like tab (0x09), newline (0x0A), etc. that JSON-encode to \uXXXX.
	// When JSON-encoded, each of these becomes 6 chars, causing ~6x expansion.
	// A 1 MiB file of escape chars becomes ~6 MiB when JSON-encoded.
	content := make([]byte, 1024*1024) // 1 MiB
	for i := 0; i < len(content); i++ {
		// Use tab character (0x09) which needs escaping in JSON and is valid UTF-8
		content[i] = '\t'
	}
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}
	h := &Handler{Backend: &fakeBackend{
		getFn: func(context.Context, string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
			return agentbackend.Session{ID: "s1", WorkingDir: root}, nil, nil
		},
	}}

	got, err := h.ReadFile(context.Background(), "s1", "control.txt")
	if err != nil {
		t.Fatal(err)
	}

	// File should be marked as too large because when JSON-encoded it exceeds the cap.
	// The raw file is 1 MiB of tabs, but when JSON-encoded each tab becomes \t (2 bytes)
	// or more in the worst case, but the estimate counts 6 bytes per char.
	// So estimated size is 1M * 6 + 2 = 6000002 bytes, which exceeds MaxFilePreviewEncodedBytes (6 MiB).
	if !got.TooLarge || got.Content != "" {
		t.Fatalf("result=%+v want too_large=true and empty content", got)
	}
}
