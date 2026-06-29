package observerstore

// FailureCategory is the canonical, stable wire-format tag attached to a
// failure return point in driver/executor code paths. The string literals are
// part of the persisted analytics contract — downstream worktrees (Phase 1/2
// A4 / A6 / D5 / D8) enumerate these values when bucketing failures, so
// adding, removing, or renaming a literal is a semver break for that schema.
//
// New categories must be appended (never reordered) and AllCategories /
// IsKnown updated in lockstep. FailUnknown is a sentinel for sites that
// cannot be classified at injection time; it is intentionally excluded from
// AllCategories and IsKnown so analytics treat it as "unbucketed".
type FailureCategory string

const (
	// FailWrongContext — agent acted on the wrong workspace/task/branch
	// despite the input being syntactically valid (e.g. resolved a name to
	// the wrong session).
	FailWrongContext FailureCategory = "wrong_context"

	// FailMissingFile — a referenced file/path does not exist at the
	// agent's vantage point (typo, race against deletion, wrong cwd).
	FailMissingFile FailureCategory = "missing_file"

	// FailWrongVersion — version skew between client/server, schema/data,
	// or two cooperating components (driver vs slave protocol, codex CLI
	// vs serve-daemon, etc.).
	FailWrongVersion FailureCategory = "wrong_version"

	// FailForbiddenCred — credential rejected by upstream (403/forbidden
	// from agentserver, expired sandbox token, missing scope).
	FailForbiddenCred FailureCategory = "forbidden_cred"

	// FailSlaveDisconnect — slave tunnel/daemon went away mid-call;
	// driver-side observable as connection closed / EOF / dial refused.
	FailSlaveDisconnect FailureCategory = "slave_disconnect"

	// FailDriverRestart — work interrupted because the driver process
	// itself restarted (crash, deploy, container OOM) and resumed.
	FailDriverRestart FailureCategory = "driver_restart"

	// FailTimeout — deadline exceeded (per-op timeout, context.DeadlineExceeded,
	// LLM stream stall).
	FailTimeout FailureCategory = "timeout"

	// FailPolicyViolation — request rejected by policy/guardrail
	// (importsallowlist, safe-paths, denied tool, capability gate).
	FailPolicyViolation FailureCategory = "policy_violation"

	// FailContractViolation — payload violates an explicit
	// internal/contract schema or invariant the receiver enforces.
	FailContractViolation FailureCategory = "contract_violation"

	// FailDuplicateWrite — observer/store rejected an idempotency
	// collision (same key written twice, optimistic concurrency fail).
	FailDuplicateWrite FailureCategory = "duplicate_write"

	// FailStaleCapability — capability/manifest/token snapshot the caller
	// presented is no longer valid (replaced, revoked, version bumped).
	FailStaleCapability FailureCategory = "stale_capability"

	// FailUnknown — sentinel: failure site exists but cannot be confidently
	// mapped to one of the 11 categories above. Use sparingly and prefer
	// classifying. Not part of AllCategories().
	FailUnknown FailureCategory = "unknown"
)

// allKnownCategories holds the 11 stable taxonomy entries in declaration
// order. FailUnknown is intentionally omitted.
var allKnownCategories = []FailureCategory{
	FailWrongContext,
	FailMissingFile,
	FailWrongVersion,
	FailForbiddenCred,
	FailSlaveDisconnect,
	FailDriverRestart,
	FailTimeout,
	FailPolicyViolation,
	FailContractViolation,
	FailDuplicateWrite,
	FailStaleCapability,
}

// AllCategories returns a fresh copy of the 11 stable failure categories in
// declaration order. The slice is safe for callers to mutate.
func AllCategories() []FailureCategory {
	out := make([]FailureCategory, len(allKnownCategories))
	copy(out, allKnownCategories)
	return out
}

// IsKnown reports whether c is one of the 11 stable failure categories.
// FailUnknown, the empty string, and arbitrary strings all return false.
func IsKnown(c FailureCategory) bool {
	for _, k := range allKnownCategories {
		if k == c {
			return true
		}
	}
	return false
}
