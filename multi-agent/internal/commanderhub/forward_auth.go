package commanderhub

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// insertNonceSQL is the atomic INSERT used to detect replay attacks.
// ON CONFLICT DO NOTHING means inserted=false iff the nonce row
// already exists. PG error (e.g. network, pool exhausted) → caller
// must fail closed.
const insertNonceSQL = `INSERT INTO commander_forward_nonces (nonce, received_at) VALUES ($1, now()) ON CONFLICT (nonce) DO NOTHING`

// signPurpose computes the HMAC-SHA256 of the canonical message
//
//	purpose + "\n" + ts + "\n" + nonce + "\n" + body
//
// using secret and returns the result as a lower-case hex string.
// The purpose prefix domain-separates /forward from /drain, preventing
// cross-endpoint replay attacks.
func signPurpose(secret []byte, purpose string, ts int64, nonce string, body []byte) string {
	h := hmac.New(sha256.New, secret)
	fmt.Fprintf(h, "%s\n%d\n%s\n", purpose, ts, nonce)
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// signForward signs the request body for /forward calls (purpose-bound).
// drain uses signDrain to prevent cross-endpoint replay attacks.
func signForward(secret []byte, ts int64, nonce string, body []byte) string {
	return signPurpose(secret, "forward", ts, nonce, body)
}

// signDrain signs the request body for /drain calls (purpose-bound).
// forward uses signForward to prevent cross-endpoint replay attacks.
func signDrain(secret []byte, ts int64, nonce string, body []byte) string {
	return signPurpose(secret, "drain", ts, nonce, body)
}

// verifyPurpose checks headerHex against HMAC signatures derived from
// secret (matchedKey=0) and prevSecret (matchedKey=1) for a given purpose.
// It returns matchedKey=-1, ok=false on any failure.
//
// Security design:
//   - Rejects on length BEFORE hex.Decode to avoid allocating a
//     partial slice for timing-oracle attacks.
//   - Compares via hmac.Equal on fixed-size [sha256.Size]byte arrays, not
//     on []byte slices, to prevent length-based timing leaks.
func verifyPurpose(headerHex, purpose string, secret, prevSecret []byte, ts int64, nonce string, body []byte) (matchedKey int, ok bool) {
	// sha256.Size bytes = 32 bytes = 64 hex chars.
	const wantHexLen = sha256.Size * 2
	if len(headerHex) != wantHexLen {
		return -1, false
	}

	// Decode the header into a fixed-size array.
	var gotArr [sha256.Size]byte
	if _, err := hex.Decode(gotArr[:], []byte(headerHex)); err != nil {
		return -1, false
	}

	// Helper: sign into a fixed-size array.
	computeArr := func(key []byte) [sha256.Size]byte {
		h := hmac.New(sha256.New, key)
		fmt.Fprintf(h, "%s\n%d\n%s\n", purpose, ts, nonce)
		h.Write(body)
		var arr [sha256.Size]byte
		copy(arr[:], h.Sum(nil))
		return arr
	}

	// Check current secret (matchedKey=0).
	if len(secret) > 0 {
		wantArr := computeArr(secret)
		if hmac.Equal(gotArr[:], wantArr[:]) {
			return 0, true
		}
	}

	// Check previous secret (matchedKey=1) — key rotation grace period.
	if len(prevSecret) > 0 {
		wantArr := computeArr(prevSecret)
		if hmac.Equal(gotArr[:], wantArr[:]) {
			return 1, true
		}
	}

	return -1, false
}

// verifyForward checks headerHex against HMAC signatures for /forward calls.
// Uses purpose="forward" to domain-separate from /drain.
func verifyForward(headerHex string, secret, prevSecret []byte, ts int64, nonce string, body []byte) (matchedKey int, ok bool) {
	return verifyPurpose(headerHex, "forward", secret, prevSecret, ts, nonce, body)
}

// verifyDrain checks headerHex against HMAC signatures for /drain calls.
// Uses purpose="drain" to domain-separate from /forward.
func verifyDrain(headerHex string, secret, prevSecret []byte, ts int64, nonce string, body []byte) (matchedKey int, ok bool) {
	return verifyPurpose(headerHex, "drain", secret, prevSecret, ts, nonce, body)
}

// parseHMACTimestamp parses a decimal Unix-seconds timestamp from the
// X-Forward-Ts header value. Returns an error on empty or non-decimal input.
func parseHMACTimestamp(s string) (int64, error) {
	if s == "" {
		return 0, errors.New("forward auth: missing timestamp header")
	}
	ts, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("forward auth: invalid timestamp %q: %w", s, err)
	}
	return ts, nil
}

// parseHMACNonce validates the nonce header value. Returns an error if the
// nonce is empty or contains characters outside the hex alphabet.
//
// We validate here so that the insertNonce step sees only well-formed values
// and never leaks DB behaviour on pathological input.
func parseHMACNonce(s string) error {
	if s == "" {
		return errors.New("forward auth: missing nonce header")
	}
	// 32 random hex chars = 16 bytes = 128 bits of entropy.
	const wantLen = 32
	if len(s) != wantLen {
		return fmt.Errorf("forward auth: nonce must be exactly %d hex chars, got %d", wantLen, len(s))
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return fmt.Errorf("forward auth: nonce contains non-hex character %q", c)
		}
	}
	return nil
}

// freshNonce generates a new 32-character lower-case hex nonce (16 random
// bytes). It propagates errors from the crypto/rand reader.
func freshNonce() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("forward auth: freshNonce rand: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// insertNonce performs an atomic INSERT of nonce into commander_forward_nonces.
// inserted=true means the nonce was new (not a replay).
// inserted=false means the nonce already existed (replay attempt).
// A PG error returns (false, err) — the caller MUST fail closed.
func insertNonce(ctx context.Context, db *sql.DB, nonce string) (inserted bool, err error) {
	res, err := db.ExecContext(ctx, insertNonceSQL, nonce)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// noncePrefix returns the first 8 hex characters of a nonce for use in
// audit log lines. Emitting the full nonce in logs is prohibited (spec v19 §7:
// "operator-visible logs must never contain auth material"). The 8-char prefix
// gives operators a correlation handle without exposing the full 128-bit secret.
// If the nonce is shorter than 8 chars (malformed input), the whole string is
// returned — callers already rejected it before reaching the log line.
func noncePrefix(nonce string) string {
	const prefixLen = 8
	if len(nonce) <= prefixLen {
		return nonce
	}
	return nonce[:prefixLen]
}

// timestampWithinWindow reports whether ts (Unix seconds) is within
// window of now.
func timestampWithinWindow(ts int64, now time.Time, window time.Duration) bool {
	diff := now.Unix() - ts
	if diff < 0 {
		diff = -diff
	}
	return time.Duration(diff)*time.Second <= window
}
