package driver

import (
	"fmt"
	"testing"

	"github.com/yourorg/multi-agent/internal/observerstore"
)

// MCPToolError carries the FailureCategory so failure analytics can bucket
// every -32000 the driver emits. The zero value (unset Category) maps to
// FailUnknown via observerstore.CategoryOf, preserving backwards behavior
// for any tool that hasn't been tagged yet.

func TestMCPToolError_DefaultCategoryIsUnknown(t *testing.T) {
	e := &MCPToolError{Message: "bad input"}
	if got := observerstore.CategoryOf(e); got != observerstore.FailUnknown {
		t.Errorf("CategoryOf(unset) = %q, want %q", got, observerstore.FailUnknown)
	}
}

func TestMCPToolError_CategoryRoundTrip(t *testing.T) {
	e := &MCPToolError{Message: "x", Category: observerstore.FailPolicyViolation}
	if got := observerstore.CategoryOf(e); got != observerstore.FailPolicyViolation {
		t.Errorf("CategoryOf(tagged) = %q, want %q", got, observerstore.FailPolicyViolation)
	}
}

func TestMCPToolError_MessageUnchanged(t *testing.T) {
	e := &MCPToolError{Message: "invalid args", Category: observerstore.FailContractViolation}
	if e.Error() != "invalid args" {
		t.Errorf("Error() = %q, want %q (Category must not leak into the wire message)", e.Error(), "invalid args")
	}
}

// CategoryOf must dig through fmt.Errorf("...%w", e) so callers that wrap an
// MCPToolError with extra context (a journal helper, a relay, an outer error
// path) don't drop the category tag.
func TestMCPToolError_CategorySurvivesFmtErrorfWrap(t *testing.T) {
	e := &MCPToolError{Message: "x", Category: observerstore.FailTimeout}
	wrapped := fmt.Errorf("outer: %w", e)
	if got := observerstore.CategoryOf(wrapped); got != observerstore.FailTimeout {
		t.Errorf("CategoryOf(fmt.Errorf %%w) = %q, want %q", got, observerstore.FailTimeout)
	}
}

// MCPToolError currently has no Unwrap, so it cannot itself sit "above" a
// Categorize() chain — the only way an MCPToolError-flavored error reaches
// CategoryOf is as the leaf. Pin that: when an MCPToolError is the only
// Categorized in the chain and its Category is empty, the result is
// FailUnknown, NOT some inadvertent inheritance from an enclosing
// fmt.Errorf message.
func TestMCPToolError_EmptyCategoryLeafIsUnknown(t *testing.T) {
	e := &MCPToolError{Message: "no tag", Category: ""}
	if got := observerstore.CategoryOf(e); got != observerstore.FailUnknown {
		t.Errorf("CategoryOf(empty MCPToolError leaf) = %q, want %q", got, observerstore.FailUnknown)
	}
	// And the same wrapped through fmt.Errorf — the fmt.wrapError is the
	// outer, e is reached via Unwrap, e.FailureCategory() returns "" which
	// the per-step walk treats as "no tag here, keep walking" → falls off
	// the chain end → FailUnknown.
	wrapped := fmt.Errorf("outer: %w", e)
	if got := observerstore.CategoryOf(wrapped); got != observerstore.FailUnknown {
		t.Errorf("CategoryOf(fmt.Errorf over empty MCPToolError) = %q, want %q", got, observerstore.FailUnknown)
	}
}
