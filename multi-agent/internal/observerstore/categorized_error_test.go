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

// fakeCategorized lets us construct a Categorized whose Category is empty
// without going through Categorize() (which always sets a tag).
type fakeCategorized struct {
	cat FailureCategory
	err error
}

func (f *fakeCategorized) Error() string                  { return f.err.Error() }
func (f *fakeCategorized) Unwrap() error                  { return f.err }
func (f *fakeCategorized) FailureCategory() FailureCategory { return f.cat }

func TestCategoryOf_EmptyOuterFallsThroughToInner(t *testing.T) {
	// Pin: an outer Categorized with empty Category must NOT shadow an inner
	// one that has a real tag. Otherwise a struct-style carrier (like
	// driver.MCPToolError with an unset Category) wrapping a Categorize()
	// result would drop the inner tag on the floor.
	inner := Categorize(errors.New("io err"), FailMissingFile)
	outer := &fakeCategorized{cat: "", err: inner}
	if got := CategoryOf(outer); got != FailMissingFile {
		t.Errorf("CategoryOf(empty-outer over tagged-inner) = %q, want %q", got, FailMissingFile)
	}
}

func TestCategoryOf_EmptyChainIsUnknown(t *testing.T) {
	// All-empty chain (or no Categorized at all in chain) → FailUnknown.
	chain := &fakeCategorized{cat: "", err: errors.New("plain")}
	if got := CategoryOf(chain); got != FailUnknown {
		t.Errorf("CategoryOf(empty-chain) = %q, want %q", got, FailUnknown)
	}
}
