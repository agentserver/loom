package observerstore

import (
	"errors"
	"fmt"
	"testing"
)

// CategorizedError is the canonical helper for tagging an `error` value
// returned from a code-path with one of the 11 stable FailureCategory tags.
// The driver / executor failure-return injection in Phase 0 uses this to
// attach analytics tags without changing control flow.

func TestCategorize_WrapsAndUnwraps(t *testing.T) {
	base := errors.New("dial tcp: connection refused")
	wrapped := Categorize(base, FailSlaveDisconnect)

	if wrapped == nil {
		t.Fatal("Categorize returned nil for non-nil error")
	}
	if !errors.Is(wrapped, base) {
		t.Errorf("errors.Is(wrapped, base) = false; want true (must preserve Unwrap chain)")
	}
	if wrapped.Error() == "" {
		t.Errorf("wrapped error has empty Error() string")
	}
	// The category must be retrievable.
	if got := CategoryOf(wrapped); got != FailSlaveDisconnect {
		t.Errorf("CategoryOf(wrapped) = %q, want %q", got, FailSlaveDisconnect)
	}
}

func TestCategorize_NilStaysNil(t *testing.T) {
	if Categorize(nil, FailUnknown) != nil {
		t.Errorf("Categorize(nil, ...) = non-nil; want nil so callers can `return Categorize(err, cat)` without nil-check")
	}
}

func TestCategoryOf_PlainErrorIsUnknown(t *testing.T) {
	if got := CategoryOf(errors.New("plain")); got != FailUnknown {
		t.Errorf("CategoryOf(plain) = %q, want %q", got, FailUnknown)
	}
	if got := CategoryOf(nil); got != FailUnknown {
		t.Errorf("CategoryOf(nil) = %q, want %q", got, FailUnknown)
	}
}

func TestCategoryOf_WorksThroughFmtErrorf(t *testing.T) {
	// %w must preserve category lookup, otherwise call sites that wrap with
	// extra context lose the tag.
	wrapped := Categorize(errors.New("rpc closed"), FailSlaveDisconnect)
	outer := fmt.Errorf("delegate: %w", wrapped)
	if got := CategoryOf(outer); got != FailSlaveDisconnect {
		t.Errorf("CategoryOf through fmt.Errorf %%w = %q, want %q", got, FailSlaveDisconnect)
	}
}

func TestCategorize_OuterCategoryWins(t *testing.T) {
	// If a caller re-categorizes, the outer tag is what they meant; keep it.
	inner := Categorize(errors.New("x"), FailTimeout)
	outer := Categorize(inner, FailContractViolation)
	if got := CategoryOf(outer); got != FailContractViolation {
		t.Errorf("CategoryOf(re-categorized) = %q, want %q (outer wins)", got, FailContractViolation)
	}
}

func TestCategorizedError_ImplementsError(t *testing.T) {
	var _ error = (*CategorizedError)(nil)
}
