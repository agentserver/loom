package agentbackend

import (
	"os"
	"path/filepath"
)

// ResolveCodexHome resolves the per-agent codex data dir from deploy inputs.
// Order:
//   - codexHome (explicit) → use it as-is.
//   - workDir (typically agent.workdir) set → `<abs(workDir)>/.codex`. The
//     workdir is absolutized first because codex runs with cmd.Dir=workDir;
//     a relative CODEX_HOME would resolve differently in the child vs the
//     parent and silently split session state. If filepath.Abs fails, fall
//     through to the loomHome path.
//   - shortID set → `<loomStateDir>/<shortID>/.codex` legacy path.
//   - otherwise → "" (caller falls back to $HOME/.codex via EffectiveCodexHome).
//
// loomStateDir = loomHome → $LOOM_HOME env → $HOME/.cache/multi-agent.
func ResolveCodexHome(codexHome, loomHome, shortID, workDir string) string {
	if codexHome != "" {
		return codexHome
	}
	if workDir != "" {
		if abs, err := filepath.Abs(workDir); err == nil && abs != "" {
			return filepath.Join(abs, ".codex")
		}
	}
	if shortID == "" {
		return ""
	}
	base := loomHome
	if base == "" {
		base = os.Getenv("LOOM_HOME")
	}
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return ""
		}
		base = filepath.Join(home, ".cache", "multi-agent")
	}
	return filepath.Join(base, shortID, ".codex")
}
