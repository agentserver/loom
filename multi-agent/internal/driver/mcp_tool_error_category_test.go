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

// MCPToolError with empty Category that wraps a tagged Categorize() inner
// must surface the inner tag, not FailUnknown. Pins the per-step walk in
// observerstore.CategoryOf.
func TestMCPToolError_EmptyCategoryFallsThroughToInner(t *testing.T) {
	inner := observerstore.Categorize(fmt.Errorf("io"), observerstore.FailMissingFile)
	e := &MCPToolError{Message: "wrapping", Category: ""}
	// Make e wrap inner by way of fmt.Errorf — driver doesn't currently use
	// this idiom but Phase 1+ helpers might, and the semantics must hold.
	wrapped := fmt.Errorf("%s: %w", e.Error(), inner)
	if got := observerstore.CategoryOf(wrapped); got != observerstore.FailMissingFile {
		t.Errorf("CategoryOf(empty-MCPToolError over tagged-inner) = %q, want %q", got, observerstore.FailMissingFile)
	}
}
