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
	if got.Path != filepath.Join(workdir, "in.txt") {
		t.Fatalf("path = %q, want %q", got.Path, filepath.Join(workdir, "in.txt"))
	}
}

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
