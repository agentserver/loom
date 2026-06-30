package main

import (
	"strings"
	"testing"
)

// TestEmailRedact_StablePerCall — same address always → same 8-hex; different
// addresses → different hex. Reviewers rely on "same author across CSVs has
// the same column value" so cross-run comparisons survive.
func TestEmailRedact_StablePerCall(t *testing.T) {
	t.Parallel()

	const a = "alice@example.com"
	const b = "bob@example.com"

	got1 := RedactEmail(a)
	got2 := RedactEmail(a)
	if got1 != got2 {
		t.Fatalf("redact(%q) not deterministic: %q vs %q", a, got1, got2)
	}
	if len(got1) != 8 {
		t.Fatalf("redact length = %d, want 8", len(got1))
	}
	if RedactEmail(b) == got1 {
		t.Fatalf("redact collision for distinct addresses")
	}
}

// TestEmailRedact_CaseAndWhitespaceInsensitive — alice@x and ALICE@x and
// "  alice@x " must all hash identically; otherwise a git config change
// from "Alice <alice@x>" to "alice <Alice@X>" would silently rotate the
// pseudo-id in the CSV and break longitudinal analysis.
func TestEmailRedact_CaseAndWhitespaceInsensitive(t *testing.T) {
	t.Parallel()

	want := RedactEmail("alice@example.com")
	for _, in := range []string{"ALICE@EXAMPLE.COM", "Alice@Example.com", "  alice@example.com  "} {
		if got := RedactEmail(in); got != want {
			t.Errorf("redact(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestEmailRedact_NoAtSignPassthrough — non-email values (empty string,
// commit_meta's "N/A: ..." placeholders) must pass through unchanged so the
// CSV records "we tried and the upstream said N/A" instead of "we hashed
// a sentinel string".
func TestEmailRedact_NoAtSignPassthrough(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"":                                      "",
		"N/A: not present at /root/agentserver": "N/A: not present at /root/agentserver",
		"<no email>":                            "<no email>",
		"   ":                                   "",
	}
	for in, want := range cases {
		if got := RedactEmail(in); got != want {
			t.Errorf("redact(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestEmailRedact_NeverContainsAtSign — defense in depth: regardless of input
// shape, the output of a successful redaction must not contain "@". This
// guards against a future refactor that, e.g., starts returning the local
// part of the address for "improved debuggability".
func TestEmailRedact_NeverContainsAtSign(t *testing.T) {
	t.Parallel()

	for _, in := range []string{
		"alice@example.com",
		"a.b+c@sub.example.co.uk",
		"\"quoted@local\"@example.com",
	} {
		got := RedactEmail(in)
		if strings.ContainsRune(got, '@') {
			t.Errorf("redact(%q) leaked '@': %q", in, got)
		}
		if len(got) != 8 {
			t.Errorf("redact(%q) length = %d, want 8", in, len(got))
		}
	}
}
