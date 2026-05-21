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
