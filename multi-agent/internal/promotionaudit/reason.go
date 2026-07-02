// Package promotionaudit records who / from which driver thread / for
// what reason / from which candidate task a dynamic MCP came to be
// registered (or unregistered / installed) in a workspace. See
// docs/specs/wt2-driver-promotion-chain-B6.spec.md for the invariants;
// the four fields are the join key that lets B1 promote-candidate rows
// meet B6 audit rows in the eval-runner (metric
// PromotionAdoptionRate etc).
package promotionaudit

import "errors"

// Reason is the typed identifier of a promotion reason. It is a
// defined string type (not an alias) so that a caller building an
// AuditFields with an untyped string literal is refused at compile
// time — see docs/specs/wt2-driver-promotion-chain-B6.spec.md §2.4.
// The four canonical values match the DB CHECK constraint in
// internal/observerstore/schema.sql `promotion_audit`.
type Reason string

const (
	ReasonExplicitUserRequest Reason = "explicit_user_request"
	ReasonDriverAgentInferred Reason = "driver_agent_inferred"
	ReasonBatchImport         Reason = "batch_import"
	ReasonCISeed              Reason = "ci_seed"
)

// ErrInvalidPromotionReason is returned by Parse when the input string
// is not one of the four canonical reasons. The error message is
// FIXED and DOES NOT echo the caller-supplied input: §7 (f) —
// echoing would let a secret-shaped input leak through the error
// path. Callers may wrap this sentinel; callers must NOT construct a
// new error that interpolates the raw input.
var ErrInvalidPromotionReason = errors.New(
	"promotion_reason must be one of: explicit_user_request, driver_agent_inferred, batch_import, ci_seed",
)

// String returns the canonical string form. Kept for symmetry with
// stdlib enum-style types; equivalent to string(r).
func (r Reason) String() string { return string(r) }

// Parse returns the canonical Reason matching s exactly (byte-for-byte
// equal to one of the four constants). Any other input — case-variant,
// leading/trailing whitespace, UTF-8 lookalike, empty string — returns
// ErrInvalidPromotionReason.
//
// Parse never interpolates s into the returned error (§7 (f)).
func Parse(s string) (Reason, error) {
	switch Reason(s) {
	case ReasonExplicitUserRequest,
		ReasonDriverAgentInferred,
		ReasonBatchImport,
		ReasonCISeed:
		return Reason(s), nil
	default:
		return "", ErrInvalidPromotionReason
	}
}
