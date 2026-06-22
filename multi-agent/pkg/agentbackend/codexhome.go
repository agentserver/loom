package agentbackend

import (
	"os"
	"path/filepath"
)

// ResolveCodexHome resolves the per-agent codex data dir from deploy inputs.
// Order: codexHome (explicit) → <loomStateDir>/<shortID>/.codex when shortID
// is known → "" (caller falls back to $HOME/.codex via EffectiveCodexHome).
// loomStateDir = loomHome → $LOOM_HOME env → $HOME/.cache/multi-agent.
func ResolveCodexHome(codexHome, loomHome, shortID string) string {
	if codexHome != "" {
		return codexHome
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
