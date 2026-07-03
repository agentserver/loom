package driver

import (
	"errors"
	"net/url"
	"path/filepath"
	"strings"
)

// ErrInvalidRegisterSourcePath is returned when a caller-supplied
// register_slave_mcp source_path fails the driver-side traversal
// guard. Same shape/semantics as
// internal/promotionpipeline.ErrInvalidScaffoldSourcePath — the two
// are parallel guards, one for the pipeline stage-3 register call
// and one for the direct tool-boundary register call, because the
// two-package layout can't share a single validator without adding
// an import cycle (see PR #71 round-2 review P2-A).
var ErrInvalidRegisterSourcePath = errors.New("driver: source_path is absolute or contains traversal — refusing to register")

// validateRegisterSourcePath enforces "must be workspace-relative,
// no `..` segments" on the register_slave_mcp tool boundary. Same
// rules as internal/promotionpipeline.validateScaffoldSourcePath
// (copied because the two packages can't share it without a cycle):
//
//   - Empty is REJECTED here (unlike the scaffold validator, which
//     accepts empty because the pipeline has a fallback path). The
//     register tool separately checks non-empty first, so this
//     validator only sees non-empty input in practice.
//   - Absolute paths (leading `/`) are refused.
//   - Any `..` segment after Clean is refused.
//   - URL-encoded traversals are refused via percent-decode + recheck.
//   - Windows-shape backslash paths are refused defensively (no
//     Linux slave would need `C:\` or `\\server\share\` paths).
//   - Null bytes / newlines / control chars are refused to prevent
//     log-injection or filename-truncation attacks on downstream
//     shell commands.
func validateRegisterSourcePath(p string) error {
	if p == "" {
		return ErrInvalidRegisterSourcePath
	}
	// Control chars — reject early (includes \x00, \n, \r, tab, etc).
	for _, r := range p {
		if r < 0x20 {
			return ErrInvalidRegisterSourcePath
		}
	}
	if strings.HasPrefix(p, "/") {
		return ErrInvalidRegisterSourcePath
	}
	// Windows shapes: `C:\...`, `\\server\share\...`, or any
	// path containing backslash. Slaves in the paper's
	// evaluation are Linux; treating backslash as a literal
	// filename byte is worse than refusing.
	if strings.Contains(p, `\`) {
		return ErrInvalidRegisterSourcePath
	}
	decoded, err := url.PathUnescape(p)
	if err == nil && decoded != p && strings.Contains(decoded, "..") {
		return ErrInvalidRegisterSourcePath
	}
	clean := filepath.Clean(p)
	for _, seg := range strings.Split(clean, "/") {
		if seg == ".." {
			return ErrInvalidRegisterSourcePath
		}
	}
	if strings.Contains(p, "..") {
		return ErrInvalidRegisterSourcePath
	}
	return nil
}
