package codex

import (
	"context"
	"fmt"
	"os/exec"
)

func detect(ctx context.Context, bin string) error {
	if bin == "" {
		bin = "codex"
	}
	if _, err := exec.LookPath(bin); err != nil {
		return fmt.Errorf("codex bin %q not on PATH: %w", bin, err)
	}
	_ = ctx
	return nil
}
