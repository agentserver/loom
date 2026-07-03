package driver

import (
	"regexp"
	"strings"
)

// threadIDCharClass — replicated from promotionaudit.userIDRE so the
// driver-side constructor produces values that pass the audit-writer
// regex. Kept as a local constant to avoid a driver → promotionaudit
// dep purely for a regex string.
const threadIDCharClass = `A-Za-z0-9_-`

var (
	threadIDReClean   = regexp.MustCompile(`[^` + threadIDCharClass + `]`)
	threadIDReCollapse = regexp.MustCompile(`-{2,}`)
)

// DeriveDriverThreadID normalises a codex-side thread identifier into
// a slug that satisfies the promotion_audit driver_thread_id regex
// (`^[A-Za-z0-9_-]{8,128}$`) AND is guaranteed NOT to leak API-key
// shape. See spec §7 (e): the regex character class alone can't reject
// an `sk-` prefixed value, so the constructor prepends a fixed
// `codex-` prefix. The prefix is stable so downstream JOIN queries
// still match — the identity is `codex-<sanitized>`.
//
// Callers pass the codex thread's public id (never a raw API key). If
// the input is empty or contains too few id-safe characters after
// sanitising, DeriveDriverThreadID pads deterministically with `_` up
// to the 8-char minimum. Overlong input is truncated to 128.
//
// The `codex-` prefix is DELIBERATELY a driver-side convention — a
// future backend (opencode, claude-code) would prepend its own prefix
// via its own Derive… wrapper. There is intentionally no `sk-` prefix
// pathway in this file.
func DeriveDriverThreadID(codexThreadID string) string {
	cleaned := threadIDReClean.ReplaceAllString(codexThreadID, "-")
	cleaned = threadIDReCollapse.ReplaceAllString(cleaned, "-")
	cleaned = strings.Trim(cleaned, "-")
	out := "codex-" + cleaned
	// Pad short.
	for len(out) < 8 {
		out += "_"
	}
	// Truncate long.
	if len(out) > 128 {
		out = out[:128]
	}
	return out
}
