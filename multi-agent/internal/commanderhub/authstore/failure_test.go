package authstore

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// allFailureValues is the canonical Go enumeration. Tests that probe
// drift (FailureEnumMatchesSchema in migrate_test.go, plus ValidFailure
// coverage below) read from this single source of truth.
var allFailureValues = []Failure{
	FailureAuthorizationDenied,
	FailureAuthorizationExpired,
	FailureUpstreamTimeout,
	FailureIDTokenInvalid,
	FailureDeviceFlow,
	FailureStoreUnavailable,
}

type wrappedErr struct{ inner error }

func (w *wrappedErr) Error() string { return "wrapped: " + w.inner.Error() }
func (w *wrappedErr) Unwrap() error { return w.inner }

func TestSanitizeFailure_EnumOnly(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want Failure
	}{
		{"nil → DeviceFlow defensive", nil, FailureDeviceFlow},
		{"ErrAuthorizationDenied", ErrAuthorizationDenied, FailureAuthorizationDenied},
		{"ErrAuthorizationDenied wrapped", &wrappedErr{ErrAuthorizationDenied}, FailureAuthorizationDenied},
		{"ErrAuthorizationExpired", ErrAuthorizationExpired, FailureAuthorizationExpired},
		{"ErrIDTokenInvalid", ErrIDTokenInvalid, FailureIDTokenInvalid},
		{"context.DeadlineExceeded", context.DeadlineExceeded, FailureUpstreamTimeout},
		{"random unknown error containing token shape",
			errors.New("upstream returned access_token=eyJxxx.yyy.zzz and Bearer abc123"),
			FailureDeviceFlow},
		{"raw JSON body unknown error",
			errors.New(`{"error":"slow_down","raw_token":"super-secret"}`),
			FailureDeviceFlow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeFailure(tc.err)
			require.Equal(t, tc.want, got)
			// Belt-and-suspenders: result MUST be in the declared enum.
			require.True(t, ValidFailure(got), "SanitizeFailure returned non-enum %q", got)
		})
	}
}

func TestFailureEnumLengthSanity(t *testing.T) {
	// schema CHECK requires <= 256
	for _, f := range allFailureValues {
		require.LessOrEqual(t, len(string(f)), 256)
	}
}

func TestValidFailure_RejectsRawString(t *testing.T) {
	require.False(t, ValidFailure(Failure("anything custom")), "unforged enum values must be rejected")
	require.False(t, ValidFailure(Failure("")), "empty must be rejected")
	require.False(t, ValidFailure(Failure("authorization-denied")), "near-miss must be rejected")
	for _, f := range allFailureValues {
		require.True(t, ValidFailure(f), "%q is in allowlist but ValidFailure said false", f)
	}
}
