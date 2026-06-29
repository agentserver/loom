package driver

import (
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
