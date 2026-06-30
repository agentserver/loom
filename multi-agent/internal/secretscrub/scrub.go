// Package secretscrub is the single source of truth for redacting raw
// secrets out of free-form audit / trace text written to observer storage.
//
// Two packages need the same blacklist applied at two boundaries:
//
//   - internal/dispatch.FinalizeAndEmit runs it on RouteDecision.ReasonText
//     before handing the value to the writer (primary call site).
//   - internal/observerstore.WriteRouteReason runs it again at the writer
//     boundary as defense-in-depth, so a future caller that bypasses
//     dispatch (e.g. the spec'd follow-up observer HTTP handler) cannot
//     land a raw secret in route_reasons.reason_text.
//
// Owning the blacklist here means both call sites cannot drift: there is
// only one regex literal, only one expvar counter, only one truncate
// rule. Adding a new pattern is a one-line change here that automatically
// benefits both sites and the matching dispatch test cases.
//
// This package has zero non-stdlib dependencies and imports no other
// internal package, so it sits below both dispatch and observerstore in
// the import graph (neither pulls a cycle back through it).
package secretscrub

import (
	"expvar"
	"regexp"
	"strings"
)

// RedactedTotal counts every Sanitize call that performed at least one
// redaction. Exposed via expvar (var name route_reason_redacted_total)
// so operators can smoke-check "did sanitize ever fire?" across both the
// dispatch finalize gate and the observerstore writer boundary — the two
// call sites share this counter so writer-only redactions are visible
// the same way as dispatch-side ones.
var RedactedTotal = expvar.NewInt("route_reason_redacted_total")

// secretRE matches common API-token / credential / private-key shapes.
// Patterns are deliberately a touch broader than strict vendor-spec to
// catch test-shaped tokens too; false positives only cost a harmless
// [REDACTED] string in the persisted ReasonText.
//
// The pattern list is NOT exhaustive — Sanitize is defense-in-depth,
// not the primary auth boundary. Adding a new family is a one-line
// change; the corresponding test case lives in TestSanitize_AllKnownPrefixes.
var secretRE = regexp.MustCompile(
	// OpenAI / Anthropic and similar `sk-` family (covers `sk-ant-...`).
	`sk-[A-Za-z0-9_\-]{8,}|` +
		// JWT (header.payload.signature shape).
		`eyJ[A-Za-z0-9_\-\.]{16,}|` +
		// AWS access key.
		`AKIA[A-Z0-9]{12,}|` +
		// GitHub legacy/PAT/server/refresh/user/app tokens.
		`gh[opsruA-Z]_[A-Za-z0-9]{20,}|` +
		// GitHub fine-grained personal access tokens.
		`github_pat_[A-Za-z0-9_]{20,}|` +
		// GitLab personal access tokens.
		`glpat-[A-Za-z0-9_\-]{20,}|` +
		// Google API keys.
		`AIza[A-Za-z0-9_\-]{20,}|` +
		// Slack tokens.
		`xox[baprs]-[A-Za-z0-9-]{8,}|` +
		// PEM-armored private keys.
		`-----BEGIN [A-Z ]*PRIVATE KEY-----`,
)

// maxRunes is the rune-counted upper bound on Sanitize's return value
// before the truncation marker is appended. 256 is the limit set by
// docs/specs/wt1-routing-trace.spec.md §6(a).
const maxRunes = 256

// truncatedSuffix is appended when the redacted output exceeds maxRunes.
const truncatedSuffix = "...[truncated]"

// Sanitize replaces every secret-shaped substring with "[REDACTED]" and
// rune-truncates the result to at most maxRunes (appending
// truncatedSuffix on overflow). Always safe to call; no-op on empty
// input.
//
// Idempotent: `Sanitize(Sanitize(x)) == Sanitize(x)` for every input x,
// and the second call never bumps RedactedTotal. This holds because:
//
//   - Once every secret substring is replaced by the literal `[REDACTED]`
//     (which contains no prefix matched by secretRE), the regex has no
//     more matches on the second pass.
//   - The truncate step is guarded by isAtTruncationFixedPoint, which
//     requires BOTH `HasSuffix(out, truncatedSuffix)` AND that the body
//     (pre-suffix) is already ≤ maxRunes runes. A well-formed truncated
//     output `<body up to 256 runes> + truncatedSuffix` is therefore a
//     fixed point, but a caller-supplied string with an oversized body
//     ending in the marker is NOT — it still gets truncated to enforce
//     the maxRunes cap.
//
// Round-6 P0: the sanitize regex MUST run on the raw input regardless
// of the truncation guard — otherwise an adversarial caller could
// supply `"leaked sk-... ...[truncated]"` and land the raw secret in
// the DB because both the sanitize AND the truncate would be skipped.
// The regex is a cheap no-op on genuinely already-redacted text
// (`[REDACTED]` matches nothing in secretRE). See
// TestSanitize_SuffixLookalike_DoesNotBypassRegex.
//
// Round-8 P1: the truncation guard ALSO checks body length. Without
// this, `"AAA...(10000 As)... ...[truncated]"` would bypass the cap
// (regex runs → no match → HasSuffix true → return 10014 runes). See
// TestSanitize_LongInputEndingInSuffix_StillTruncated.
//
// On any redaction (one or more pattern hits) RedactedTotal is
// incremented by one — not by the number of hits — so the counter
// reflects "calls with at least one redaction" across both the dispatch
// and observerstore call sites.
func Sanitize(s string) string {
	if s == "" {
		return s
	}
	redacted := false
	out := secretRE.ReplaceAllStringFunc(s, func(_ string) string {
		redacted = true
		return "[REDACTED]"
	})
	if redacted {
		RedactedTotal.Add(1)
	}
	// Idempotence guard for the truncate step ONLY: an already-truncated
	// string (body ≤ maxRunes runes + literal suffix) is a fixed point.
	// If we re-truncated we'd lop runes off the body to re-append the
	// suffix, breaking idempotence. Note this guard is placed AFTER the
	// regex scan (round-6 review) and requires the body-length check
	// (round-8 review) so a long input ending in the marker still gets
	// truncated.
	if isAtTruncationFixedPoint(out) {
		return out
	}
	// Fast path: byte length is an upper bound on rune count (every rune is
	// at least 1 byte). If we're already within the cap, avoid the []rune
	// allocation — Sanitize runs on every observer trace write (twice on
	// the dispatch→writer path) so this matters under load.
	if len(out) <= maxRunes {
		return out
	}
	runes := []rune(out)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes]) + truncatedSuffix
	}
	return out
}

// isAtTruncationFixedPoint reports whether s is already the output of a
// prior Sanitize truncate: it ends in truncatedSuffix AND the pre-suffix
// body is ≤ maxRunes runes. Both conditions are required — HasSuffix
// alone would let a caller-supplied long string ending in the marker
// bypass the cap (round-8 P1).
func isAtTruncationFixedPoint(s string) bool {
	if !strings.HasSuffix(s, truncatedSuffix) {
		return false
	}
	body := strings.TrimSuffix(s, truncatedSuffix)
	// Byte-length fast path: if body is short in bytes, it's short in
	// runes too (bytes >= runes always).
	if len(body) <= maxRunes {
		return true
	}
	// Only allocate []rune when the byte-count didn't already answer.
	return len([]rune(body)) <= maxRunes
}
