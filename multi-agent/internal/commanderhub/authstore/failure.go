// Package authstore persists commander login + session state across
// observer-server replicas. See docs/superpowers/specs/2026-06-26-commander-state-persistence-design.md.
package authstore

import (
	"context"
	"errors"
)

// Failure is the only string type accepted into commander_logins.failure.
// The DB enforces `failure IN (...enum values...)`. SanitizeFailure is the
// blessed constructor; ValidFailure is the runtime allowlist check that
// Store implementations use to defend the persistence boundary.
//
// Go's type system cannot make a string newtype unforgeable
// (`Failure("anything")` compiles), so the security guard lives at three
// layers: SanitizeFailure as the only blessed producer, ValidFailure as
// the store-side reject, and the Postgres CHECK constraint as the last
// line of defense.
type Failure string

const (
	FailureAuthorizationDenied  Failure = "authorization denied"
	FailureAuthorizationExpired Failure = "authorization expired"
	FailureUpstreamTimeout      Failure = "upstream timeout"
	FailureIDTokenInvalid       Failure = "id token invalid"
	FailureDeviceFlow           Failure = "device flow error"
	FailureStoreUnavailable     Failure = "store unavailable"
)

// Sentinel errors the deviceFlow.PollOnce path returns. Authenticator wraps
// upstream responses (access_denied, expired_token, ...) and id-token parse
// failures into one of these — never propagating raw HTTP body or token text.
var (
	ErrAuthorizationDenied  = errors.New("authstore: authorization denied")
	ErrAuthorizationExpired = errors.New("authstore: authorization expired")
	ErrIDTokenInvalid       = errors.New("authstore: id token invalid")

	// ErrInvalidFailure is returned by Store.MarkLoginFailed when the input
	// Failure value is not in ValidFailure().
	ErrInvalidFailure = errors.New("authstore: invalid failure value")
)

// SanitizeFailure maps an upstream / id-token / context error into one of the
// six enum Failure constants. Fail-closed: unknown errors degrade to
// FailureDeviceFlow rather than echoing the original text.
func SanitizeFailure(err error) Failure {
	switch {
	case err == nil:
		return FailureDeviceFlow
	case errors.Is(err, context.DeadlineExceeded):
		return FailureUpstreamTimeout
	case errors.Is(err, ErrAuthorizationDenied):
		return FailureAuthorizationDenied
	case errors.Is(err, ErrAuthorizationExpired):
		return FailureAuthorizationExpired
	case errors.Is(err, ErrIDTokenInvalid):
		return FailureIDTokenInvalid
	default:
		return FailureDeviceFlow
	}
}

// ValidFailure is the runtime allowlist for Failure values. Store
// implementations call it as the first statement of MarkLoginFailed; non-enum
// inputs return ErrInvalidFailure without touching persistent state.
func ValidFailure(f Failure) bool {
	switch f {
	case FailureAuthorizationDenied, FailureAuthorizationExpired,
		FailureUpstreamTimeout, FailureIDTokenInvalid,
		FailureDeviceFlow, FailureStoreUnavailable:
		return true
	}
	return false
}
