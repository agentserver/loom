package observerstore

import "testing"

// TestFailureCategoryConstants pins the wire-format string literals for the 11
// fixed failure categories. Downstream worktrees depend on these exact values;
// changing a literal here is a semver break for failure analytics.
func TestFailureCategoryConstants(t *testing.T) {
	cases := []struct {
		got, want FailureCategory
	}{
		{FailWrongContext, "wrong_context"},
		{FailMissingFile, "missing_file"},
		{FailWrongVersion, "wrong_version"},
		{FailForbiddenCred, "forbidden_cred"},
		{FailSlaveDisconnect, "slave_disconnect"},
		{FailDriverRestart, "driver_restart"},
		{FailTimeout, "timeout"},
		{FailPolicyViolation, "policy_violation"},
		{FailContractViolation, "contract_violation"},
		{FailDuplicateWrite, "duplicate_write"},
		{FailStaleCapability, "stale_capability"},
		{FailUnknown, "unknown"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("failure category literal drift: got %q, want %q", tc.got, tc.want)
		}
	}
}

func TestAllCategoriesLength(t *testing.T) {
	got := AllCategories()
	if len(got) != 11 {
		t.Fatalf("AllCategories: want 11 known categories, got %d (%v)", len(got), got)
	}
	// Ensure FailUnknown is NOT in AllCategories — it is a sentinel, not part
	// of the taxonomy.
	for _, c := range got {
		if c == FailUnknown {
			t.Errorf("AllCategories must not include FailUnknown sentinel")
		}
	}
}

func TestAllCategoriesUnique(t *testing.T) {
	seen := map[FailureCategory]bool{}
	for _, c := range AllCategories() {
		if seen[c] {
			t.Errorf("AllCategories contains duplicate %q", c)
		}
		seen[c] = true
	}
}

func TestIsKnown(t *testing.T) {
	for _, c := range AllCategories() {
		if !IsKnown(c) {
			t.Errorf("IsKnown(%q) = false, want true", c)
		}
	}
	if IsKnown(FailUnknown) {
		t.Errorf("IsKnown(FailUnknown) = true, want false (sentinel is not a known category)")
	}
	if IsKnown(FailureCategory("not_a_real_category_xyz")) {
		t.Errorf("IsKnown of arbitrary string = true, want false")
	}
	if IsKnown(FailureCategory("")) {
		t.Errorf("IsKnown(\"\") = true, want false")
	}
}
