package authstore

import (
	"context"
	"errors"
	"time"

	"github.com/yourorg/multi-agent/internal/identity"
)

// Sentinels returned by Store methods.
var (
	// ErrNotFound: lookup miss. Authenticator translates this to 404 / 401 /
	// "another pod won" depending on the call site.
	ErrNotFound = errors.New("authstore: not found")

	// ErrCapped: ReserveLogin refused because >= 1024 unexpired logins exist.
	// Authenticator translates to HTTP 429.
	ErrCapped = errors.New("authstore: pending logins cap reached")
)

// MaxActiveLogins is the cap enforced by Store.ReserveLogin.
//
// Replaces the per-process maxPendingLogins=64 used by the old in-memory
// implementation. The DB-shared cap is bigger because it is shared across all
// observer-server replicas, but still caps amplification toward agentserver.
const MaxActiveLogins = 1024

// LoginRecord is the semantic view of a commander_logins row.
//
// State machine:
//
//	reserved: DeviceCode == "" && Failure == "" && SessionIDHash == ""
//	pending:  DeviceCode != "" && Failure == "" && SessionIDHash == ""
//	failed:   Failure != "" (terminal)
//	done:     SessionIDHash != "" (terminal)
//
// Mutual exclusion enforced by commander_logins_terminal_xor CHECK in
// schema_postgres.sql. Sweep removes rows with expires_at < now() regardless
// of state.
type LoginRecord struct {
	LoginID         string
	DeviceCode      string    // "" while reserved
	CodeExpiresAt   time.Time // zero while reserved
	IntervalSeconds int       // > 0 once finalized
	NextPollAt      time.Time
	ExpiresAt       time.Time
	SessionIDHash   string  // hex(sha256(plaintext sid)); terminal=done
	Failure         Failure // terminal=failed
}

// SessionRecord is the semantic view of a commander_sessions row plus
// PlaintextSessionID (used only by MarkLoginDone / GetSession entry; never
// persisted in any form other than its sha256 hash).
type SessionRecord struct {
	PlaintextSessionID string // in-flight only; store hashes before write
	Identity           identity.Identity
	ExpiresAt          time.Time
}

// Store persists commander login + session state across all observer-server
// replicas. All methods must be safe for concurrent use.
type Store interface {
	// -- logins --

	// ReserveLogin atomically:
	//   1. sweep expired rows (preventing zombies from stealing cap slots),
	//   2. check cap (>= MaxActiveLogins → ErrCapped),
	//   3. insert reservation row (DeviceCode="", ExpiresAt = now+ttl).
	//
	// Postgres implementation uses pg_advisory_xact_lock for strict serialization.
	// inmemory implementation uses sync.Mutex.
	ReserveLogin(ctx context.Context, loginID string, now time.Time, ttl time.Duration) error

	// FinalizeReservedLogin fills RequestCode's fields onto a reservation row.
	// Targets WHERE login_id=$lid AND device_code = ''. If the row is not in
	// reserved state (sweep raced, double-call, …) returns ErrNotFound.
	// intervalSeconds is clamped to >= 5 by the implementation.
	FinalizeReservedLogin(ctx context.Context, loginID string,
		deviceCode string, codeExpiresAt time.Time, intervalSeconds int) error

	// DeleteLogin releases a reservation slot. Idempotent: missing → nil.
	// Called only on the post-Reserve failure path (RequestCode err, or
	// client cancelled before Finalize completed).
	DeleteLogin(ctx context.Context, loginID string) error

	// GetLogin returns the current row unchanged. ErrNotFound for missing.
	// Caller decides whether ExpiresAt < now means "expired".
	GetLogin(ctx context.Context, loginID string) (LoginRecord, error)

	// SetPollThrottle updates both interval_seconds and next_poll_at in one
	// SQL. Idempotent: missing lid → nil (best-effort throttle).
	// intervalSeconds is clamped to >= 5 by the implementation.
	SetPollThrottle(ctx context.Context, loginID string,
		intervalSeconds int, nextPollAt time.Time) error

	// MarkLoginDone is a single tx:
	//   1) UPDATE commander_logins SET session_id_hash=$hash, finalized_at=now()
	//        WHERE login_id=$lid
	//          AND session_id_hash IS NULL AND failure IS NULL
	//          AND device_code != '' AND expires_at > now()
	//   2) RowsAffected = 0 → ROLLBACK, return ErrNotFound
	//   3) INSERT INTO commander_sessions (session_id_hash, ...) ...
	//   4) COMMIT
	//
	// session.PlaintextSessionID is hashed by the store; the caller keeps the
	// plaintext to Set-Cookie. ctx is expected to be Authenticator.writeCtx
	// (i.e. WithoutCancel + 5s timeout) so a client disconnect cannot leave a
	// session row without its login row (or vice versa).
	MarkLoginDone(ctx context.Context, loginID string, session SessionRecord) error

	// MarkLoginFailed sets failure + finalized_at in one statement
	// WHERE session_id_hash IS NULL AND failure IS NULL AND expires_at > now().
	// Terminal / missing / expired → ErrNotFound.
	// sanitizedFailure must satisfy ValidFailure(); otherwise ErrInvalidFailure
	// is returned without any persistent state change.
	MarkLoginFailed(ctx context.Context, loginID string, sanitizedFailure Failure) error

	// ConsumeLogin: atomic SELECT + DELETE. One-shot semantics.
	// Postgres: DELETE FROM commander_logins WHERE login_id=$1 RETURNING …
	// inmemory: lock + map lookup + delete + return.
	// ErrNotFound means another pod already consumed, or the row never existed.
	// Caller (ServeLoginPoll [B] / [A3]) decides per-state HTTP response.
	ConsumeLogin(ctx context.Context, loginID string) (LoginRecord, error)

	// -- sessions --

	// GetSession looks up by sha256_hex(plaintextSessionID) WHERE expires_at > now().
	// The store hashes internally; plaintext sid is never written to a SQL parameter.
	// Expired or missing → ErrNotFound.
	GetSession(ctx context.Context, plaintextSessionID string) (SessionRecord, error)

	// DeleteSession hashes the plaintext and DELETEs that row. Idempotent.
	DeleteSession(ctx context.Context, plaintextSessionID string) error

	// -- sweep --

	// SweepExpired DELETEs rows with expires_at < now() from both tables.
	// Safe to run concurrently across pods (each statement is atomic).
	// Returns per-table deletion counts and the first error encountered.
	SweepExpired(ctx context.Context) (loginsDeleted, sessionsDeleted int64, err error)
}

// MinIntervalSeconds is the floor applied by ReserveLogin's caller and by
// FinalizeReservedLogin / SetPollThrottle internally. Prevents an upstream
// `Interval=0` from violating the commander_logins_interval_positive CHECK.
const MinIntervalSeconds = 5

// ClampIntervalSeconds returns max(n, MinIntervalSeconds). Exported for
// store implementations and the Authenticator to keep semantics consistent.
func ClampIntervalSeconds(n int) int {
	if n < MinIntervalSeconds {
		return MinIntervalSeconds
	}
	return n
}

// hashSID is implemented in inmemory.go and re-used by postgres.go.
// Plaintext sid is never persisted nor passed as a SQL parameter; only the
// hash hex string ever reaches the DB.
