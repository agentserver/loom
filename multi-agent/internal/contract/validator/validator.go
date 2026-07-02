// Package validator runs the §A3 four-class pre-execution checks against
// a (TaskContract, capability.Snapshot) pair. Every check is a pure
// function; the package MUST NOT import os / net / io / database/sql —
// see the imports_test enforcement and wt2-dry-run-validator.spec.md §7(a).
package validator

import (
	"context"
	"fmt"

	"github.com/yourorg/multi-agent/internal/capability"
	"github.com/yourorg/multi-agent/internal/contract"
)

// Severity is currently a single value; kept as a type so future
// advisory / info classes can extend without an API break.
type Severity string

const SeverityBlock Severity = "block"

type Kind string

const (
	KindMissingFile     Kind = "missing_file"
	KindWrongVersion    Kind = "wrong_version"
	KindForbiddenCred   Kind = "forbidden_cred"
	KindPolicyViolation Kind = "policy_violation"
)

// maxDetailBytes caps every Block.Detail. Enforced twice: here at
// construction and again at the observerstore writer boundary
// (dry_run_blocks_writer.go). See spec §7(c) + §7(f).
const maxDetailBytes = 8192

// detailTruncSentinel is appended when a Detail is cut. Callers grepping
// blocked_at rows can filter for this suffix to detect truncation.
const detailTruncSentinel = "<...truncated>"

// Block is one §A3 violation surfaced by a Validator.
type Block struct {
	Kind     Kind     `json:"kind"`
	Severity Severity `json:"severity"`
	Field    string   `json:"field"`
	Expected string   `json:"expected"`
	Actual   string   `json:"actual"`
	Detail   string   `json:"detail"`
}

// Validator runs every registered check against (tc, snap) and returns
// zero or more blocks. Implementations MUST be pure: no I/O, no
// filesystem access, no network, no goroutines. The context is
// threaded only so a future block-cancellation surface is compatible;
// the default staticValidator never observes it.
type Validator interface {
	Check(ctx context.Context, tc contract.TaskContract, snap capability.Snapshot) []Block
}

// New returns the default validator that runs all four §A3 checks in
// deterministic order (missing_file, wrong_version, forbidden_cred,
// policy_violation). Tasks 3–6 populate the check functions; the
// composite scaffold here returns nil so Task 2 can commit a
// self-consistent skeleton.
func New() Validator { return staticValidator{} }

type staticValidator struct{}

func (staticValidator) Check(_ context.Context, tc contract.TaskContract, snap capability.Snapshot) []Block {
	// Deterministic order (spec §2): missing_file → wrong_version →
	// forbidden_cred → policy_violation. Table tests grep blocks[i]
	// by this order.
	var blocks []Block
	blocks = append(blocks, checkMissingFile(tc, snap)...)
	blocks = append(blocks, checkWrongVersion(tc, snap)...)
	blocks = append(blocks, checkForbiddenCred(tc, snap)...)
	blocks = append(blocks, checkPolicyViolation(tc, snap)...)
	return blocks
}

// newBlock assembles a Block with the required three-field Detail and
// enforces the maxDetailBytes cap. §7(f): callers pass only field name +
// expected + actual — NEVER raw contract / snapshot bodies. §7(c): the
// truncation caps a hand-crafted 40 KiB actual value.
func newBlock(kind Kind, field, expected, actual string) Block {
	detail := fmt.Sprintf("%s: expected %s, actual %s", field, expected, actual)
	if len(detail) > maxDetailBytes {
		// Reserve room for the sentinel so the cap is honoured even
		// after appending. Cut at byte boundary; Detail is not required
		// to be UTF-8-safe (structured fields carry the machine parts).
		cut := maxDetailBytes - len(detailTruncSentinel)
		if cut < 0 {
			cut = 0
		}
		detail = detail[:cut] + detailTruncSentinel
	}
	return Block{
		Kind:     kind,
		Severity: SeverityBlock,
		Field:    field,
		Expected: expected,
		Actual:   actual,
		Detail:   detail,
	}
}
