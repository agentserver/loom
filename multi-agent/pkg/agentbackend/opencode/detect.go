package opencode

import (
	"context"
	"fmt"
	"os/exec"
)

// detect resolves bin via PATH lookup. We don't shell out (some bins
// choke on flag-only invocations during health probes); a successful
// LookPath is enough for "the binary exists and is executable".
// Mirrors pkg/agentbackend/codex/detect.go.
func detect(ctx context.Context, bin string) error {
	if bin == "" {
		bin = "opencode"
	}
	if _, err := exec.LookPath(bin); err != nil {
		return fmt.Errorf("opencode bin %q not on PATH: %w", bin, err)
	}
	_ = ctx
	return nil
}
