package commanderhub

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// signForward / verifyForward
// ---------------------------------------------------------------------------

func TestSignForward_Deterministic(t *testing.T) {
	// Same inputs produce the same output.
	sig1 := signForward([]byte("secret"), 1700000000, "aabbccdd00112233aabbccdd00112233", []byte("body"))
	sig2 := signForward([]byte("secret"), 1700000000, "aabbccdd00112233aabbccdd00112233", []byte("body"))
	require.Equal(t, sig1, sig2)
}

func TestSignForward_OutputIsHex64(t *testing.T) {
	sig := signForward([]byte("secret"), 1700000000, "aabbccdd00112233aabbccdd00112233", []byte("body"))
	require.Len(t, sig, 64, "HMAC-SHA256 hex is 64 chars")
	for _, c := range sig {
		ok := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
		require.True(t, ok, "char %q not lower-case hex", c)
	}
}

func TestSignForward_DifferentSecrets(t *testing.T) {
	sig1 := signForward([]byte("secret1"), 1700000000, "aabbccdd00112233aabbccdd00112233", []byte("body"))
	sig2 := signForward([]byte("secret2"), 1700000000, "aabbccdd00112233aabbccdd00112233", []byte("body"))
	require.NotEqual(t, sig1, sig2)
}

func TestSignForward_DifferentTimestamps(t *testing.T) {
	sig1 := signForward([]byte("secret"), 1700000000, "aabbccdd00112233aabbccdd00112233", []byte("body"))
	sig2 := signForward([]byte("secret"), 1700000001, "aabbccdd00112233aabbccdd00112233", []byte("body"))
	require.NotEqual(t, sig1, sig2)
}

func TestVerifyForward_ValidCurrentSecret(t *testing.T) {
	secret := []byte("test-secret")
	ts := int64(1700000000)
	nonce := "aabbccdd00112233aabbccdd00112233"
	body := []byte("hello world")

	header := signForward(secret, ts, nonce, body)
	key, ok := verifyForward(header, secret, nil, ts, nonce, body)
	require.True(t, ok)
	require.Equal(t, 0, key)
}

func TestVerifyForward_ValidPrevSecret(t *testing.T) {
	prevSecret := []byte("old-secret")
	ts := int64(1700000000)
	nonce := "aabbccdd00112233aabbccdd00112233"
	body := []byte("hello world")

	header := signForward(prevSecret, ts, nonce, body)
	key, ok := verifyForward(header, []byte("new-secret"), prevSecret, ts, nonce, body)
	require.True(t, ok)
	require.Equal(t, 1, key)
}

func TestVerifyForward_WrongSecret(t *testing.T) {
	ts := int64(1700000000)
	nonce := "aabbccdd00112233aabbccdd00112233"
	body := []byte("hello world")

	header := signForward([]byte("attacker-secret"), ts, nonce, body)
	key, ok := verifyForward(header, []byte("server-secret"), []byte("server-prev-secret"), ts, nonce, body)
	require.False(t, ok)
	require.Equal(t, -1, key)
}

func TestVerifyForward_BodyMismatch(t *testing.T) {
	secret := []byte("test-secret")
	ts := int64(1700000000)
	nonce := "aabbccdd00112233aabbccdd00112233"

	header := signForward(secret, ts, nonce, []byte("original-body"))
	key, ok := verifyForward(header, secret, nil, ts, nonce, []byte("tampered-body"))
	require.False(t, ok)
	require.Equal(t, -1, key)
}

func TestVerifyForward_TimestampMismatch(t *testing.T) {
	secret := []byte("test-secret")
	nonce := "aabbccdd00112233aabbccdd00112233"
	body := []byte("hello")

	header := signForward(secret, 1700000000, nonce, body)
	key, ok := verifyForward(header, secret, nil, 1700000001, nonce, body)
	require.False(t, ok)
	require.Equal(t, -1, key)
}

func TestVerifyForward_NonceMismatch(t *testing.T) {
	secret := []byte("test-secret")
	ts := int64(1700000000)
	body := []byte("hello")

	header := signForward(secret, ts, "aabbccdd00112233aabbccdd00112233", body)
	key, ok := verifyForward(header, secret, nil, ts, "aabbccdd00112233aabbccdd00112234", body)
	require.False(t, ok)
	require.Equal(t, -1, key)
}

// TestVerifyForward_RejectsMalformedAuthHeader covers three sub-cases:
// wrong length, non-hex characters, and empty string.
func TestVerifyForward_RejectsMalformedAuthHeader(t *testing.T) {
	secret := []byte("test-secret")
	ts := int64(1700000000)
	nonce := "aabbccdd00112233aabbccdd00112233"
	body := []byte("body")

	t.Run("wrong_length", func(t *testing.T) {
		// 63 chars (one short of the expected 64).
		header := strings.Repeat("a", 63)
		key, ok := verifyForward(header, secret, nil, ts, nonce, body)
		require.False(t, ok)
		require.Equal(t, -1, key)
	})

	t.Run("non_hex", func(t *testing.T) {
		// 64 chars but contains 'z' which is not a hex digit.
		header := strings.Repeat("z", 64)
		key, ok := verifyForward(header, secret, nil, ts, nonce, body)
		require.False(t, ok)
		require.Equal(t, -1, key)
	})

	t.Run("empty", func(t *testing.T) {
		key, ok := verifyForward("", secret, nil, ts, nonce, body)
		require.False(t, ok)
		require.Equal(t, -1, key)
	})
}

func TestVerifyForward_BothSecretsEmpty(t *testing.T) {
	// Neither key configured => always reject.
	sig := signForward([]byte("some-secret"), 1700000000, "aabbccdd00112233aabbccdd00112233", []byte("body"))
	key, ok := verifyForward(sig, nil, nil, 1700000000, "aabbccdd00112233aabbccdd00112233", []byte("body"))
	require.False(t, ok)
	require.Equal(t, -1, key)
}

// ---------------------------------------------------------------------------
// parseHMACTimestamp
// ---------------------------------------------------------------------------

func TestParseHMACTimestamp_Valid(t *testing.T) {
	ts, err := parseHMACTimestamp("1700000000")
	require.NoError(t, err)
	require.Equal(t, int64(1700000000), ts)
}

func TestParseHMACTimestamp_Empty(t *testing.T) {
	_, err := parseHMACTimestamp("")
	require.Error(t, err)
}

func TestParseHMACTimestamp_NonDecimal(t *testing.T) {
	_, err := parseHMACTimestamp("0xDEAD")
	require.Error(t, err)
}

func TestParseHMACTimestamp_Negative(t *testing.T) {
	// Negative timestamps are valid integers — caller decides freshness.
	ts, err := parseHMACTimestamp("-1")
	require.NoError(t, err)
	require.Equal(t, int64(-1), ts)
}

// ---------------------------------------------------------------------------
// parseHMACNonce
// ---------------------------------------------------------------------------

func TestParseHMACNonce_Valid(t *testing.T) {
	require.NoError(t, parseHMACNonce("aabbccdd00112233aabbccdd00112233"))
}

func TestParseHMACNonce_Empty(t *testing.T) {
	require.Error(t, parseHMACNonce(""))
}

func TestParseHMACNonce_TooShort(t *testing.T) {
	require.Error(t, parseHMACNonce("aabb"))
}

func TestParseHMACNonce_TooLong(t *testing.T) {
	require.Error(t, parseHMACNonce(strings.Repeat("a", 33)))
}

func TestParseHMACNonce_NonHex(t *testing.T) {
	// Replace one char with 'z'.
	nonce := "aabbccdd00112233aabbccdd0011223z"
	require.Error(t, parseHMACNonce(nonce))
}

func TestParseHMACNonce_UppercaseHexAllowed(t *testing.T) {
	// Uppercase hex chars are acceptable.
	nonce := "AABBCCDD00112233AABBCCDD00112233"
	require.NoError(t, parseHMACNonce(nonce))
}

// ---------------------------------------------------------------------------
// freshNonce
// ---------------------------------------------------------------------------

func TestFreshNonce_Length(t *testing.T) {
	n, err := freshNonce()
	require.NoError(t, err)
	require.Len(t, n, 32, "freshNonce must produce 32 hex chars")
}

func TestFreshNonce_IsHex(t *testing.T) {
	n, err := freshNonce()
	require.NoError(t, err)
	for _, c := range n {
		ok := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
		require.True(t, ok, "char %q not lower-case hex", c)
	}
}

func TestFreshNonce_Unique(t *testing.T) {
	n1, err := freshNonce()
	require.NoError(t, err)
	n2, err := freshNonce()
	require.NoError(t, err)
	require.NotEqual(t, n1, n2, "two consecutive nonces should differ")
}

// ---------------------------------------------------------------------------
// insertNonce (sqlmock)
// ---------------------------------------------------------------------------

func TestInsertNonce_Inserted(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	nonce := "aabbccdd00112233aabbccdd00112233"
	mock.ExpectExec(insertNonceSQL).
		WithArgs(nonce).
		WillReturnResult(sqlmock.NewResult(1, 1))

	inserted, err := insertNonce(context.Background(), db, nonce)
	require.NoError(t, err)
	require.True(t, inserted, "nonce should be fresh (inserted)")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestInsertNonce_Replay(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	nonce := "aabbccdd00112233aabbccdd00112233"
	// ON CONFLICT DO NOTHING → 0 rows affected.
	mock.ExpectExec(insertNonceSQL).
		WithArgs(nonce).
		WillReturnResult(sqlmock.NewResult(0, 0))

	inserted, err := insertNonce(context.Background(), db, nonce)
	require.NoError(t, err)
	require.False(t, inserted, "nonce already seen (replay)")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestInsertNonce_DBError(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	nonce := "aabbccdd00112233aabbccdd00112233"
	dbErr := errors.New("connection refused")
	mock.ExpectExec(insertNonceSQL).
		WithArgs(nonce).
		WillReturnError(dbErr)

	inserted, err := insertNonce(context.Background(), db, nonce)
	require.Error(t, err)
	require.False(t, inserted, "on error, inserted must be false (fail closed)")
	require.NoError(t, mock.ExpectationsWereMet())
}

// ---------------------------------------------------------------------------
// timestampWithinWindow
// ---------------------------------------------------------------------------

func TestTimestampWithinWindow_Exact(t *testing.T) {
	now := time.Unix(1700000000, 0)
	require.True(t, timestampWithinWindow(1700000000, now, 30*time.Second))
}

func TestTimestampWithinWindow_JustInside(t *testing.T) {
	now := time.Unix(1700000000, 0)
	require.True(t, timestampWithinWindow(1699999970, now, 30*time.Second))
}

func TestTimestampWithinWindow_JustOutside(t *testing.T) {
	now := time.Unix(1700000000, 0)
	// 31 seconds in the past.
	require.False(t, timestampWithinWindow(1699999969, now, 30*time.Second))
}

func TestTimestampWithinWindow_FutureWithinWindow(t *testing.T) {
	now := time.Unix(1700000000, 0)
	// 10 seconds in the future — clock skew.
	require.True(t, timestampWithinWindow(1700000010, now, 30*time.Second))
}

func TestTimestampWithinWindow_FutureOutsideWindow(t *testing.T) {
	now := time.Unix(1700000000, 0)
	require.False(t, timestampWithinWindow(1700000031, now, 30*time.Second))
}

// ---------------------------------------------------------------------------
// Integration: sign → verify → insert nonce round trip (sqlmock)
// ---------------------------------------------------------------------------

func TestForwardAuth_SignVerifyInsertRoundTrip(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db.Close()

	secret := []byte("integration-secret")
	ts := time.Now().Unix()
	nonce, err := freshNonce()
	require.NoError(t, err)
	body := []byte(`{"hello":"world"}`)

	// Sign.
	header := signForward(secret, ts, nonce, body)
	require.Len(t, header, 64)

	// Verify.
	key, ok := verifyForward(header, secret, nil, ts, nonce, body)
	require.True(t, ok)
	require.Equal(t, 0, key)

	// Insert nonce (fresh).
	mock.ExpectExec(insertNonceSQL).
		WithArgs(nonce).
		WillReturnResult(sqlmock.NewResult(1, 1))
	inserted, err := insertNonce(context.Background(), db, nonce)
	require.NoError(t, err)
	require.True(t, inserted)
	require.NoError(t, mock.ExpectationsWereMet())

	// Replay attempt: insert same nonce again.
	db2, mock2, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer db2.Close()

	mock2.ExpectExec(insertNonceSQL).
		WithArgs(nonce).
		WillReturnResult(sqlmock.NewResult(0, 0))
	inserted2, err := insertNonce(context.Background(), db2, nonce)
	require.NoError(t, err)
	require.False(t, inserted2, "second insert must report replay")
	require.NoError(t, mock2.ExpectationsWereMet())
}

// ---------------------------------------------------------------------------
// signForward canonical message format
// ---------------------------------------------------------------------------

func TestSignForward_CanonicalFormat(t *testing.T) {
	// Ensure the canonical format is purpose + "\n" + ts + "\n" + nonce + "\n" + body.
	// Different nonces with same ts and body must differ (nonce is included).
	sig1 := signForward([]byte("k"), 0, "00000000000000000000000000000000", nil)
	sig2 := signForward([]byte("k"), 0, "00000000000000000000000000000001", nil)
	require.Len(t, sig1, 64)
	require.NotEqual(t, sig1, sig2, "nonce must be part of the signed message")

	// Body is also included.
	sig3 := signForward([]byte("k"), 0, "00000000000000000000000000000000", []byte("x"))
	require.NotEqual(t, sig1, sig3, "body must be part of the signed message")

	// Purpose separation: signForward and signDrain must produce different sigs.
	sig4 := signDrain([]byte("k"), 0, "00000000000000000000000000000000", nil)
	require.NotEqual(t, sig1, sig4, "forward and drain signatures must differ (purpose separation)")
}

// ---------------------------------------------------------------------------
// verifyForward: both secrets checked (not short-circuit on empty)
// ---------------------------------------------------------------------------

func TestVerifyForward_OnlyPrevSecretSet(t *testing.T) {
	// current secret is empty, only prev is set.
	ts := int64(1700000000)
	nonce := "aabbccdd00112233aabbccdd00112233"
	body := []byte("data")

	header := signForward([]byte("prev"), ts, nonce, body)
	key, ok := verifyForward(header, nil, []byte("prev"), ts, nonce, body)
	require.True(t, ok)
	require.Equal(t, 1, key)
}

// Ensure the SQL constant has the right shape (matches insertNonceSQL const).
func TestInsertNonceSQL_Shape(t *testing.T) {
	require.Contains(t, insertNonceSQL, "commander_forward_nonces")
	require.Contains(t, insertNonceSQL, "ON CONFLICT")
	require.Contains(t, insertNonceSQL, "DO NOTHING")
	// Verify placeholder count.
	require.Equal(t, 1, strings.Count(insertNonceSQL, "$1"))
}

// Compile-time: ensure the signature of insertNonce matches what callers expect.
var _ func(context.Context, *sql.DB, string) (bool, error) = insertNonce

// Compile-time: ensure signForward returns a string.
var _ = fmt.Sprintf("%s", signForward([]byte("k"), 0, "n", []byte("b")))
