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

// signForward computes the HMAC-SHA256 of the canonical message
//
//	ts + "\n" + nonce + "\n" + body
//
// using secret and returns the result as a lower-case hex string.
func signForward(secret string, ts int64, nonce, body string) string {
	h := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(h, "%d\n%s\n%s", ts, nonce, body)
	return hex.EncodeToString(h.Sum(nil))
}

// verifyForward checks headerHex against HMAC signatures derived from
// secret (matchedKey=0) and prevSecret (matchedKey=1). It returns
// matchedKey=-1, ok=false on any failure.
//
// Security design:
//   - Rejects on length BEFORE hex.Decode to avoid allocating a
//     partial slice for timing-oracle attacks.
//   - Compares via hmac.Equal on fixed-size [sha256.Size]byte arrays, not
//     on []byte slices, to prevent length-based timing leaks.
func verifyForward(headerHex, secret, prevSecret string, ts int64, nonce, body string) (matchedKey int, ok bool) {
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
	computeArr := func(key string) [sha256.Size]byte {
		h := hmac.New(sha256.New, []byte(key))
		fmt.Fprintf(h, "%d\n%s\n%s", ts, nonce, body)
		var arr [sha256.Size]byte
		copy(arr[:], h.Sum(nil))
		return arr
	}

	// Check current secret (matchedKey=0).
	if secret != "" {
		wantArr := computeArr(secret)
		if hmac.Equal(gotArr[:], wantArr[:]) {
			return 0, true
		}
	}

	// Check previous secret (matchedKey=1) — key rotation grace period.
	if prevSecret != "" {
		wantArr := computeArr(prevSecret)
		if hmac.Equal(gotArr[:], wantArr[:]) {
			return 1, true
		}
	}

	return -1, false
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

// timestampWithinWindow reports whether ts (Unix seconds) is within
// window of now.
func timestampWithinWindow(ts int64, now time.Time, window time.Duration) bool {
	diff := now.Unix() - ts
	if diff < 0 {
		diff = -diff
	}
	return time.Duration(diff)*time.Second <= window
}
