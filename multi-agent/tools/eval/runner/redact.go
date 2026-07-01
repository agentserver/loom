// Package runner is the Phase 1 (WT-1-eval-runner-skeleton) evaluation
// harness. See docs/specs/wt1-eval-runner-skeleton.spec.md for the contract.
//
// ⚠️  NOT FOR PRODUCTION — purpose-built for the paper's E1 macrobenchmark
// loop. Bypasses OAuth via tools/eval/agentserver-stub.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// RedactEmail hashes an email address to its leading eight hex digits of
// SHA-256 over a normalized form (trim + lower-case). Strings that don't
// look like an email — empty, the literal "N/A: ..." placeholders that
// commit_meta emits when a repo is missing, or any value missing an "@" —
// are returned verbatim so absence signals survive into the CSV.
//
// Security §7(c): plaintext emails must never reach the run row.
func RedactEmail(addr string) string {
	trimmed := strings.TrimSpace(addr)
	if trimmed == "" {
		return ""
	}
	if !strings.ContainsRune(trimmed, '@') {
		// Preserve "N/A: not present at /root/agentserver" style
		// placeholders and any other non-address sentinel verbatim so
		// reviewers can tell "missing" apart from "hashed".
		return trimmed
	}
	sum := sha256.Sum256([]byte(strings.ToLower(trimmed)))
	return hex.EncodeToString(sum[:])[:8]
}
