package opencode

import (
	"context"
	"path/filepath"
	"testing"
)

// TestDetectFailsWhenBinMissing pins Backend.Detect()'s contract: a
// missing binary surfaces an error operators can act on.
func TestDetectFailsWhenBinMissing(t *testing.T) {
	if err := detect(context.Background(), filepath.Join(t.TempDir(), "no-such")); err == nil {
		t.Fatal("expected error")
	}
}

// TestDetectPassesWhenBinExists pins the happy path: any executable
// at the configured path satisfies Detect (we don't try to run it
// because some bins choke on flag-only invocations).
func TestDetectPassesWhenBinExists(t *testing.T) {
	bin := goBuildFake(t, `package main

func main() {}
`, "opencode")
	if err := detect(context.Background(), bin); err != nil {
		t.Fatal(err)
	}
}
