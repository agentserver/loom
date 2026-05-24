package claude

import (
	"context"
	"fmt"
	"os/exec"
)

func detect(ctx context.Context, bin string) error {
	if bin == "" {
		bin = "claude"
	}
	if _, err := exec.LookPath(bin); err != nil {
		return fmt.Errorf("claude bin %q not on PATH: %w", bin, err)
	}
	// Best-effort: a `claude --version` should exit 0 quickly. Don't gate on
	// login state — `claude login` may not have been run yet.
	_ = ctx
	return nil
}
