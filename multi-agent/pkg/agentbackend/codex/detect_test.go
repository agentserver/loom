package codex

import (
	"context"
	"path/filepath"
	"testing"
)

func TestDetectFailsWhenBinMissing(t *testing.T) {
	if err := detect(context.Background(), filepath.Join(t.TempDir(), "no-such")); err == nil {
		t.Fatal("expected error")
	}
}
func TestDetectPassesWhenBinExists(t *testing.T) {
	bin := buildFakeCodex(t, `package main

func main() {}
`)
	if err := detect(context.Background(), bin); err != nil {
		t.Fatal(err)
	}
}
