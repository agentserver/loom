package secretscrub

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// scrubKnownPrefixCases is the authoritative list of secret-shape
// fixtures both internal/dispatch and internal/observerstore tests reuse
// (via secretscrubtest.KnownPrefixCases). Adding a new entry here must
// be paired with adding the matching pattern to secretRE; the package's
// tests will fail if a fixture is missing from the regex (or vice versa
// once the per-callsite tests pin the same fixtures).
var scrubKnownPrefixCases = []struct {
	Name           string
	Input          string
	MustNotContain string
}{
	{"openai-sk", "leaked: sk-abcdefghijklmnopqrstuv", "sk-abcdefghij"},
	{"anthropic-sk-ant", "tok=sk-ant-api03-AbCdEfGhIjKlMnOpQrStUv", "sk-ant-api03"},
	{"jwt", "tok=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.payload", "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"},
	{"aws-akia", "creds AKIAIOSFODNN7EXAMPLE here", "AKIAIOSFODNN7EXAMPLE"},
	{"github-ghp", "leaked ghp_abcdefghijklmnopqrstuv12345 stuff", "ghp_abcdefghijklmnopqrstuv12345"},
	{"github-gho", "oauth gho_abcdefghijklmnopqrstuv12345 stuff", "gho_abcdefghijklmnopqrstuv12345"},
	{"github-ghs", "server ghs_abcdefghijklmnopqrstuv12345 stuff", "ghs_abcdefghijklmnopqrstuv12345"},
	{"github-ghr", "refresh ghr_abcdefghijklmnopqrstuv12345 stuff", "ghr_abcdefghijklmnopqrstuv12345"},
	{"github-ghu", "user ghu_abcdefghijklmnopqrstuv12345 stuff", "ghu_abcdefghijklmnopqrstuv12345"},
	{"github-pat", "tok=github_pat_11ABCDEFG0xyzABCDEFGHIJ stuff", "github_pat_11ABCDEFG0xyz"},
	{"gitlab-pat", "tok=glpat-xxxxxxxxxxxxxxxxxxxx", "glpat-xxxxxxxxxxxxxxxxxxxx"},
	{"google-api", "GOOG_KEY=AIzaSyA0123456789abcdefghij stuff", "AIzaSyA0123456789abcdefghij"},
	{"slack-xoxb", "slack xoxb-12345-67890-abcdef token", "xoxb-12345-67890-abcdef"},
	{"pem-private", "key:\n-----BEGIN RSA PRIVATE KEY-----\nMIIEvQ...\n-----END RSA PRIVATE KEY-----", "-----BEGIN RSA PRIVATE KEY-----"},
}

// TestSanitize_AllKnownPrefixes is the authoritative regex test. Any new
// pattern added to secretRE MUST add a matching fixture to
// scrubKnownPrefixCases, and any fixture added there must be matched by
// the regex — otherwise this test fails for the offending row.
func TestSanitize_AllKnownPrefixes(t *testing.T) {
	for _, c := range scrubKnownPrefixCases {
		t.Run(c.Name, func(t *testing.T) {
			out := Sanitize(c.Input)
			require.NotContains(t, out, c.MustNotContain, "%s prefix must be redacted", c.Name)
			require.Contains(t, out, "[REDACTED]")
		})
	}
}

func TestSanitize_EmptyInput_NoOp(t *testing.T) {
	require.Equal(t, "", Sanitize(""))
}

// NOTE on counter assertions: RedactedTotal is a process-global expvar,
// so in principle a parallel goroutine could bump it between the
// `before` capture and the post-call read. We KEEP exact-equality
// assertions here because:
//
//   1. No test in package secretscrub calls t.Parallel(), so within this
//      binary the counter only moves when the test under inspection
//      moves it. A grep-guard against the introduction of t.Parallel()
//      in this package is the assertion itself: if anyone adds
//      Parallel, these tests will start failing immediately.
//
//   2. The strict "one call → exactly one bump" upper bound is the
//      whole point of the counter's contract (it counts CALLS that
//      redacted, not matches). Without the upper bound, a regression
//      that bumped per-match (3 secrets in one string → 3 bumps) would
//      silently pass every test. The looser GreaterOrEqual form would
//      have lost that guarantee.

func TestSanitize_NoSecret_NoCounterBump(t *testing.T) {
	before := RedactedTotal.Value()
	out := Sanitize("perfectly safe text with no secrets")
	require.Equal(t, "perfectly safe text with no secrets", out)
	require.Equal(t, before, RedactedTotal.Value(),
		"counter must NOT bump when nothing was redacted (and no t.Parallel in this package)")
}

func TestSanitize_RedactionBumpsCounter(t *testing.T) {
	before := RedactedTotal.Value()
	_ = Sanitize("contains sk-abcdefghijklmnop here")
	require.Equal(t, before+1, RedactedTotal.Value(),
		"counter must bump by exactly 1 per Sanitize call that performed any redaction")
}

func TestSanitize_MultipleHitsInOneCallBumpOnce(t *testing.T) {
	before := RedactedTotal.Value()
	_ = Sanitize("sk-abcdefghijklmnop plus ghp_abcdefghijklmnopqrstuv12345 plus AKIAIOSFODNN7EXAMPLE")
	require.Equal(t, before+1, RedactedTotal.Value(),
		"the counter counts CALLS-with-redaction, not redaction occurrences")
}

func TestSanitize_Idempotent(t *testing.T) {
	in := "leaked sk-abcdefghijklmnop here"
	once := Sanitize(in)
	before := RedactedTotal.Value()
	twice := Sanitize(once)
	require.Equal(t, once, twice, "calling Sanitize on an already-sanitized string must be a no-op")
	require.Equal(t, before, RedactedTotal.Value(),
		"second Sanitize must NOT bump the counter (no redaction occurred)")
}

// TestSanitize_Idempotent_OnTruncatedInput covers the specific case
// round-5 review flagged: a re-Sanitize of an already-truncated string
// (which is `256 body runes + "...[truncated]"` = 270 runes total) would
// re-enter the truncate branch and lop 14 body runes off to make room
// for a second suffix, breaking idempotence. The guard on the truncate
// step short-circuits so the string is a fixed point.
func TestSanitize_Idempotent_OnTruncatedInput(t *testing.T) {
	in := strings.Repeat("x", 1024)
	once := Sanitize(in)
	before := RedactedTotal.Value()
	twice := Sanitize(once)
	require.Equal(t, once, twice,
		"Sanitize(Sanitize(x)) must equal Sanitize(x) — even when the first "+
			"pass truncated. Re-truncating would silently lop body runes.")
	require.Equal(t, before, RedactedTotal.Value(),
		"second Sanitize on a truncated string must NOT bump the counter")
}

// TestSanitize_LongInputEndingInSuffix_StillTruncated is the round-8 P1
// regression guard. Round 6 fixed a secret-bypass by moving the
// truncatedSuffix guard from before the regex to after it. But the
// guard's *body-length* condition was never added, so this reviewer
// found: an arbitrarily long input ending in the literal marker
// bypasses the 256-rune cap entirely (regex runs, but the truncate
// step short-circuits regardless of body length).
//
// Reachable via envelope-controlled conversation_id, ReasonText carrying
// a large err.Error() ending in the marker, etc. Impact: DB bloat, log
// flood, spec §6(a) violation ("truncates to at most maxRunes").
//
// Fix: the guard must additionally require that the body (pre-suffix)
// is already within maxRunes.
func TestSanitize_LongInputEndingInSuffix_StillTruncated(t *testing.T) {
	// 10 000 body runes + literal marker. If the guard is placed
	// correctly, output is truncated to 256 + suffix.
	in := strings.Repeat("A", 10000) + truncatedSuffix
	out := Sanitize(in)
	require.LessOrEqual(t, len([]rune(out)), 256+len([]rune(truncatedSuffix)),
		"a long input ending in the truncatedSuffix marker MUST still be "+
			"truncated to the maxRunes cap — the guard must only skip the "+
			"re-append when the body is already at fixed-point length")
	require.True(t, strings.HasSuffix(out, truncatedSuffix),
		"output must still end with the suffix (truncation happened)")
}

// TestSanitize_SuffixLookalike_DoesNotBypassRegex is the round-6 P0
// regression guard. An earlier attempt at the idempotence fix short-
// circuited ALL sanitize logic (regex + truncate) whenever the input
// ended in the literal `...[truncated]` — meaning a caller who supplied
// a string like `"leaked sk-abcdefghijklmnop ...[truncated]"` would get
// the string back verbatim, silently bypassing the secret blacklist.
//
// The correct behavior is to guard ONLY the truncate re-append step;
// the regex must always run because it's a genuine no-op on already-
// redacted text (no `[REDACTED]` substring matches any secret pattern)
// but catches this exact class of adversarial input.
func TestSanitize_SuffixLookalike_DoesNotBypassRegex(t *testing.T) {
	// Case 1: raw secret + literal suffix at end.
	in := "leaked: sk-abcdefghijklmnopqrstuv ...[truncated]"
	out := Sanitize(in)
	require.NotContains(t, out, "sk-abcdefghij",
		"raw secret before a suffix-lookalike tail MUST be redacted — "+
			"the round-5 guard must not short-circuit the regex")
	require.Contains(t, out, "[REDACTED]")
	// Round-7 P2 strengthening: also assert the length cap.
	require.LessOrEqual(t, len([]rune(out)), 256+len([]rune(truncatedSuffix)),
		"output must respect the maxRunes cap even when input ends in the marker")

	// Case 2: PEM key before the suffix.
	pem := "hidden: -----BEGIN RSA PRIVATE KEY-----XYZ ...[truncated]"
	out2 := Sanitize(pem)
	require.NotContains(t, out2, "BEGIN RSA PRIVATE KEY")
	require.Contains(t, out2, "[REDACTED]")
	require.LessOrEqual(t, len([]rune(out2)), 256+len([]rune(truncatedSuffix)))

	// Case 3: clean string that happens to end in the marker is still
	// a valid fixed point — no regex hit, no truncation, verbatim.
	clean := "some legitimate audit note ...[truncated]"
	require.Equal(t, clean, Sanitize(clean),
		"a legitimate string ending in the marker with NO secret is a fixed point")
}

func TestSanitize_TruncateAscii(t *testing.T) {
	in := strings.Repeat("x", 1024)
	out := Sanitize(in)
	require.True(t, strings.HasSuffix(out, "...[truncated]"))
	body := strings.TrimSuffix(out, "...[truncated]")
	require.Equal(t, 256, len([]rune(body)))
}

func TestSanitize_TruncateMultibyteRuneSafe(t *testing.T) {
	// 300 "汉" runes (3 bytes each).
	in := strings.Repeat("汉", 300)
	out := Sanitize(in)
	require.True(t, strings.HasSuffix(out, "...[truncated]"))
	body := strings.TrimSuffix(out, "...[truncated]")
	require.Equal(t, 256, len([]rune(body)), "must truncate to 256 RUNES, not bytes")
	require.Equal(t, strings.Repeat("汉", 256), body, "must not split a UTF-8 codepoint mid-sequence")

	// 200 emoji (4 bytes each) — below limit, unchanged.
	emoji := strings.Repeat("🚀", 200)
	require.Equal(t, emoji, Sanitize(emoji))
}
