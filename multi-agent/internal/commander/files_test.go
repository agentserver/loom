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
