package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// NewSecret returns a 32-byte hex string used as the HMAC key for the lifetime
// of a single stub process. Restart ⇒ new secret ⇒ previously issued tokens
// stop validating. That is intentional — eval-runner owns the stub lifecycle.
func NewSecret() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is unrecoverable for this process.
		panic(fmt.Errorf("agentserver-stub: read crypto/rand: %w", err))
	}
	return hex.EncodeToString(b[:])
}

// deriveToken returns a stable 32-hex-char token derived from
// (secret, field, role, short_id, workspace_id). Same inputs ⇒ same output
// within one process, so callers can re-issue idempotently.
func deriveToken(secret, field, role, shortID, workspaceID string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	// Pipe-separated input keeps field boundaries unambiguous.
	fmt.Fprintf(mac, "%s|%s|%s|%s", field, role, shortID, workspaceID)
	sum := mac.Sum(nil)
	return hex.EncodeToString(sum[:16]) // 32 hex chars
}
