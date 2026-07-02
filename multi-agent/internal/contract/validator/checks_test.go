package validator

import (
	"context"
	"strings"
	"testing"

	"github.com/yourorg/multi-agent/internal/capability"
	"github.com/yourorg/multi-agent/internal/contract"
)

// ---------------------------------------------------------------------
// Task 3 — checkMissingFile (§3.1)
// ---------------------------------------------------------------------

func TestCheckMissingFile_EmitsBlock_WhenNoFileResourceMatches(t *testing.T) {
	tc := contract.TaskContract{
		DataContract: contract.DataContract{
			ReadArtifacts: []contract.ArtifactRef{
				{Kind: "file", Name: "configs/prod.yaml"},
			},
		},
	}
	snap := capability.Snapshot{
		Files: []capability.FileResource{
			{KindDetail: "repo", PathPattern: "src/*.go"},
		},
	}
	got := New().Check(context.Background(), tc, snap)
	if len(got) != 1 || got[0].Kind != KindMissingFile {
		t.Fatalf("expected 1 missing_file block; got %d: %+v", len(got), got)
	}
	if !strings.Contains(got[0].Field, "read_artifacts[0]") {
		t.Errorf("Field must anchor at the offending index: got %q", got[0].Field)
	}
	if got[0].Expected != "configs/prod.yaml" {
		t.Errorf("Expected: got %q", got[0].Expected)
	}
}

func TestCheckMissingFile_NoBlock_WhenPathMatches(t *testing.T) {
	tc := contract.TaskContract{
		DataContract: contract.DataContract{
			ReadArtifacts: []contract.ArtifactRef{
				{Kind: "file", Name: "configs/prod.yaml"},
			},
		},
	}
	snap := capability.Snapshot{
		Files: []capability.FileResource{
			{KindDetail: "config", PathPattern: "configs/*.yaml"},
		},
	}
	got := New().Check(context.Background(), tc, snap)
	for _, b := range got {
		if b.Kind == KindMissingFile {
			t.Errorf("expected no missing_file block; got %+v", b)
		}
	}
}

// §3.1 shape heuristic: kind empty + name looks path-shaped → still checked.
func TestCheckMissingFile_ImpliedFileArtifact(t *testing.T) {
	tc := contract.TaskContract{
		DataContract: contract.DataContract{
			ReadArtifacts: []contract.ArtifactRef{
				{Kind: "", Name: "spec.md"},              // has extension → file
				{Kind: "", Name: "vendor/lib/mod.go"},    // has slash → file
				{Kind: "", Name: "just-an-identifier"}, // no slash, no dot → NOT a file
			},
		},
	}
	got := New().Check(context.Background(), tc, capability.Snapshot{})
	var missing int
	for _, b := range got {
		if b.Kind == KindMissingFile {
			missing++
		}
	}
	if missing != 2 {
		t.Fatalf("expected 2 implied-file blocks; got %d (all blocks: %+v)", missing, got)
	}
}

// Malformed pattern: path.Match returns an error. checkMissingFile must
// treat this as no-match (still emit the block for the artifact) and
// NOT surface the pattern in Block.Detail (§7(f)).
func TestCheckMissingFile_MalformedPatternDoesNotLeak(t *testing.T) {
	tc := contract.TaskContract{
		DataContract: contract.DataContract{
			ReadArtifacts: []contract.ArtifactRef{{Kind: "file", Name: "x.txt"}},
		},
	}
	snap := capability.Snapshot{
		Files: []capability.FileResource{
			{KindDetail: "repo", PathPattern: "[malformed"},
		},
	}
	got := New().Check(context.Background(), tc, snap)
	if len(got) != 1 || got[0].Kind != KindMissingFile {
		t.Fatalf("expected 1 missing_file block; got %+v", got)
	}
	if strings.Contains(got[0].Detail, "[malformed") {
		t.Errorf("Detail must not leak the malformed pattern; got %q", got[0].Detail)
	}
}

// ---------------------------------------------------------------------
// Task 4 — checkWrongVersion (§3.2, §7(b))
// ---------------------------------------------------------------------

func TestCheckWrongVersion_EmitsBlock_WhenSnapshotBelowMin(t *testing.T) {
	tc := contract.TaskContract{
		CapabilityRequirements: contract.CapabilityRequirements{
			ToolRequirements: []contract.ToolRequirement{{Name: "go", MinVersion: "1.22.0"}},
		},
	}
	snap := capability.Snapshot{Tools: []capability.ToolVersion{{Name: "go", Version: "1.18.4"}}}
	got := New().Check(context.Background(), tc, snap)
	if len(got) != 1 || got[0].Kind != KindWrongVersion {
		t.Fatalf("expected 1 wrong_version block; got %+v", got)
	}
	if got[0].Actual != "1.18.4" {
		t.Errorf("Actual: got %q want %q", got[0].Actual, "1.18.4")
	}
}

func TestCheckWrongVersion_NoBlock_WhenSnapshotAtOrAboveMin(t *testing.T) {
	tc := contract.TaskContract{
		CapabilityRequirements: contract.CapabilityRequirements{
			ToolRequirements: []contract.ToolRequirement{{Name: "go", MinVersion: "1.22.0"}},
		},
	}
	for _, snapVer := range []string{"1.22.0", "1.22.5", "2.0.0"} {
		t.Run(snapVer, func(t *testing.T) {
			snap := capability.Snapshot{Tools: []capability.ToolVersion{{Name: "go", Version: snapVer}}}
			for _, b := range New().Check(context.Background(), tc, snap) {
				if b.Kind == KindWrongVersion {
					t.Errorf("unexpected wrong_version block: %+v", b)
				}
			}
		})
	}
}

// §7(b) THE CRITICAL NEGATIVE: 1.22.0-rc1 is a PRE-release; semver
// specifies it as LESS THAN 1.22.0. A hand-rolled "split by dot"
// comparator would incorrectly judge rc1 as ≥ 1.22.0 and let unstable
// pre-release binaries pass the dry-run.
func TestCheckWrongVersion_RCPrereleaseBelowRelease(t *testing.T) {
	tc := contract.TaskContract{
		CapabilityRequirements: contract.CapabilityRequirements{
			ToolRequirements: []contract.ToolRequirement{{Name: "go", MinVersion: "1.22.0"}},
		},
	}
	snap := capability.Snapshot{Tools: []capability.ToolVersion{{Name: "go", Version: "1.22.0-rc1"}}}
	got := New().Check(context.Background(), tc, snap)
	var wv int
	for _, b := range got {
		if b.Kind == KindWrongVersion {
			wv++
		}
	}
	if wv != 1 {
		t.Fatalf("§7(b) regression: rc1 must be < 1.22.0 (semver rule); got %d wrong_version blocks — did someone hand-roll semver? blocks=%+v", wv, got)
	}
}

func TestCheckWrongVersion_MissingSnapshotEntry(t *testing.T) {
	tc := contract.TaskContract{
		CapabilityRequirements: contract.CapabilityRequirements{
			ToolRequirements: []contract.ToolRequirement{{Name: "sqlite3", MinVersion: "3.40.0"}},
		},
	}
	got := New().Check(context.Background(), tc, capability.Snapshot{})
	if len(got) != 1 || got[0].Kind != KindWrongVersion {
		t.Fatalf("expected 1 wrong_version block; got %+v", got)
	}
	if got[0].Actual != "<not installed>" {
		t.Errorf("Actual sentinel: got %q", got[0].Actual)
	}
}

// Legacy Tools []string — presence-only. Absent snapshot ⇒ still a
// wrong_version block (§3.2 final paragraph).
func TestCheckWrongVersion_LegacyToolsPresenceOnly(t *testing.T) {
	tc := contract.TaskContract{
		CapabilityRequirements: contract.CapabilityRequirements{
			Tools: []string{"jq"},
		},
	}
	got := New().Check(context.Background(), tc, capability.Snapshot{})
	if len(got) != 1 || got[0].Kind != KindWrongVersion {
		t.Fatalf("expected 1 presence-only wrong_version block; got %+v", got)
	}
	if got[0].Expected != "<any version installed>" {
		t.Errorf("Expected sentinel: got %q", got[0].Expected)
	}
}

func TestCheckWrongVersion_UnparseableSnapshotVersion(t *testing.T) {
	tc := contract.TaskContract{
		CapabilityRequirements: contract.CapabilityRequirements{
			ToolRequirements: []contract.ToolRequirement{{Name: "python", MinVersion: "3.10.0"}},
		},
	}
	snap := capability.Snapshot{Tools: []capability.ToolVersion{{Name: "python", Version: "banana"}}}
	got := New().Check(context.Background(), tc, snap)
	if len(got) != 1 || got[0].Kind != KindWrongVersion {
		t.Fatalf("expected 1 wrong_version block; got %+v", got)
	}
	if !strings.HasPrefix(got[0].Actual, "<unparseable:") {
		t.Errorf("Actual sentinel: got %q", got[0].Actual)
	}
}

// ---------------------------------------------------------------------
// Task 5 — checkForbiddenCred (§3.3, §7(e))
// ---------------------------------------------------------------------

func TestCheckForbiddenCred_ExactMatch_Blocks(t *testing.T) {
	tc := contract.TaskContract{
		CapabilityRequirements: contract.CapabilityRequirements{
			ForbiddenAliases: []string{"openai_for_glm"},
		},
	}
	snap := capability.Snapshot{
		Credentials: []capability.CredentialAlias{"openai_for_glm", "internal_ci"},
	}
	got := New().Check(context.Background(), tc, snap)
	var fc int
	for _, b := range got {
		if b.Kind == KindForbiddenCred {
			fc++
			if b.Actual != "openai_for_glm" {
				t.Errorf("Actual: got %q want openai_for_glm", b.Actual)
			}
		}
	}
	if fc != 1 {
		t.Fatalf("expected 1 forbidden_cred block; got %d (all: %+v)", fc, got)
	}
}

// §7(e) THE CRITICAL BYPASS MATRIX: NONE of these variants may match
// `openai_for_glm`. If they do, attacker gets forbidden creds through
// by renaming.
func TestCheckForbiddenCred_ExactMatch_RejectsBypasses(t *testing.T) {
	tc := contract.TaskContract{
		CapabilityRequirements: contract.CapabilityRequirements{
			ForbiddenAliases: []string{"openai_for_glm"},
		},
	}
	cases := []capability.CredentialAlias{
		"openai_for_glm_backup",  // suffix bypass
		"real_openai_for_glm",    // prefix bypass
		"my_openai_for_glm_prod", // sandwich bypass
	}
	for _, c := range cases {
		t.Run(string(c), func(t *testing.T) {
			snap := capability.Snapshot{Credentials: []capability.CredentialAlias{c}}
			got := New().Check(context.Background(), tc, snap)
			for _, b := range got {
				if b.Kind == KindForbiddenCred {
					t.Errorf("§7(e) regression: exact-match forbidden_cred wrongly flagged %q against forbidden %q", c, "openai_for_glm")
				}
			}
		})
	}
}

// §7(e) CASE-SENSITIVITY regression bait: forbidden alias is well-formed
// lowercase (passes NewCredentialAlias), snapshot carries a case-different
// variant. An implementation using strings.EqualFold would flag these
// as forbidden; the spec §7(e) requires exact `==` and therefore NO block.
func TestCheckForbiddenCred_CaseSensitiveAgainstEqualFoldRegression(t *testing.T) {
	tc := contract.TaskContract{
		CapabilityRequirements: contract.CapabilityRequirements{
			ForbiddenAliases: []string{"openai_for_glm"},
		},
	}
	// CredentialAlias is a plain string type; a hand-crafted snapshot
	// (bypassing NewCredentialAlias, which the WT-1 spec permits for
	// direct construction) can carry ANY string. The validator must
	// still evaluate the CASE-SENSITIVE == against it.
	cases := []capability.CredentialAlias{
		"Openai_For_Glm",
		"OPENAI_FOR_GLM",
		"OpenAI_for_GLM",
	}
	for _, c := range cases {
		t.Run(string(c), func(t *testing.T) {
			snap := capability.Snapshot{Credentials: []capability.CredentialAlias{c}}
			got := New().Check(context.Background(), tc, snap)
			for _, b := range got {
				if b.Kind == KindForbiddenCred {
					t.Errorf("§7(e) regression: case-insensitive match — EqualFold in use? snapshot=%q forbidden=%q block=%+v",
						c, "openai_for_glm", b)
				}
			}
		})
	}
}

// Malformed alias in contract: NewCredentialAlias would reject it, but
// the contract's ForbiddenAliases is a plain []string that operators
// hand-author. checkForbiddenCred surfaces this as an operator-facing
// block, not a silent pass.
func TestCheckForbiddenCred_MalformedContractAlias(t *testing.T) {
	tc := contract.TaskContract{
		CapabilityRequirements: contract.CapabilityRequirements{
			ForbiddenAliases: []string{"NotAValidAlias!"}, // fails aliasShapeRe
		},
	}
	got := New().Check(context.Background(), tc, capability.Snapshot{})
	var fc int
	for _, b := range got {
		if b.Kind == KindForbiddenCred {
			fc++
			if b.Actual != "<malformed alias>" {
				t.Errorf("Actual sentinel: got %q", b.Actual)
			}
		}
	}
	if fc != 1 {
		t.Fatalf("expected 1 malformed-alias block; got %d", fc)
	}
}

func TestCheckForbiddenCred_NoBlock_WhenAbsent(t *testing.T) {
	tc := contract.TaskContract{
		CapabilityRequirements: contract.CapabilityRequirements{
			ForbiddenAliases: []string{"openai_for_glm"},
		},
	}
	snap := capability.Snapshot{Credentials: []capability.CredentialAlias{"internal_ci"}}
	for _, b := range New().Check(context.Background(), tc, snap) {
		if b.Kind == KindForbiddenCred {
			t.Errorf("unexpected forbidden_cred block: %+v", b)
		}
	}
}

// ---------------------------------------------------------------------
// Task 6 — checkPolicyViolation (§3.4)
// ---------------------------------------------------------------------

func TestCheckPolicyViolation_EmitsBlock_WhenSnapshotReachTooLow(t *testing.T) {
	tc := contract.TaskContract{
		ExecutionPolicy: contract.ExecutionPolicy{RequiredReach: capability.NetworkInternet},
	}
	snap := capability.Snapshot{Network: capability.NetworkIntranet}
	got := New().Check(context.Background(), tc, snap)
	var pv int
	for _, b := range got {
		if b.Kind == KindPolicyViolation {
			pv++
			if b.Expected != string(capability.NetworkInternet) {
				t.Errorf("Expected: got %q", b.Expected)
			}
			if b.Actual != string(capability.NetworkIntranet) {
				t.Errorf("Actual: got %q", b.Actual)
			}
		}
	}
	if pv != 1 {
		t.Fatalf("expected 1 policy_violation block; got %d", pv)
	}
}

func TestCheckPolicyViolation_NoBlock_WhenEqualOrGreater(t *testing.T) {
	tc := contract.TaskContract{
		ExecutionPolicy: contract.ExecutionPolicy{RequiredReach: capability.NetworkLoopbackOnly},
	}
	for _, snapReach := range []capability.NetworkReach{
		capability.NetworkLoopbackOnly, capability.NetworkIntranet, capability.NetworkInternet,
	} {
		t.Run(string(snapReach), func(t *testing.T) {
			snap := capability.Snapshot{Network: snapReach}
			for _, b := range New().Check(context.Background(), tc, snap) {
				if b.Kind == KindPolicyViolation {
					t.Errorf("unexpected policy_violation block: %+v", b)
				}
			}
		})
	}
}

func TestCheckPolicyViolation_NoConstraint_WhenRequiredReachEmpty(t *testing.T) {
	tc := contract.TaskContract{ExecutionPolicy: contract.ExecutionPolicy{}}
	snap := capability.Snapshot{Network: capability.NetworkNone}
	for _, b := range New().Check(context.Background(), tc, snap) {
		if b.Kind == KindPolicyViolation {
			t.Errorf("empty RequiredReach must impose no constraint; got %+v", b)
		}
	}
}

// Defensive: hand-crafted snapshot bypasses NewSnapshot invariants
// (invalid NetworkReach). Validator must reject it as a policy
// violation instead of silently letting a garbage host through.
func TestCheckPolicyViolation_UnknownReach_Blocks(t *testing.T) {
	tc := contract.TaskContract{
		ExecutionPolicy: contract.ExecutionPolicy{RequiredReach: capability.NetworkIntranet},
	}
	snap := capability.Snapshot{Network: capability.NetworkReach("worldwide")}
	got := New().Check(context.Background(), tc, snap)
	var pv int
	for _, b := range got {
		if b.Kind == KindPolicyViolation {
			pv++
			if b.Actual != "worldwide" {
				t.Errorf("Actual: got %q want worldwide", b.Actual)
			}
		}
	}
	if pv != 1 {
		t.Fatalf("expected 1 policy_violation block; got %d", pv)
	}
}

// ---------------------------------------------------------------------
// Task 7 — composite validator checkpoint (§4)
// ---------------------------------------------------------------------

// Composite: one inject per class, all firing. Assert (a) count == 4,
// (b) deterministic order matches spec §2.
func TestNew_AllFourChecksFire_InOrder(t *testing.T) {
	tc := contract.TaskContract{
		DataContract: contract.DataContract{
			ReadArtifacts: []contract.ArtifactRef{{Kind: "file", Name: "missing.yaml"}},
		},
		CapabilityRequirements: contract.CapabilityRequirements{
			ToolRequirements: []contract.ToolRequirement{{Name: "go", MinVersion: "1.22.0"}},
			ForbiddenAliases: []string{"openai_for_glm"},
		},
		ExecutionPolicy: contract.ExecutionPolicy{RequiredReach: capability.NetworkInternet},
	}
	snap := capability.Snapshot{
		OS: "linux", Arch: "amd64",
		Network:     capability.NetworkIntranet,
		Tools:       []capability.ToolVersion{{Name: "go", Version: "1.18.4"}},
		Credentials: []capability.CredentialAlias{"openai_for_glm"},
		// No Files → missing_file fires.
	}
	got := New().Check(context.Background(), tc, snap)
	if len(got) != 4 {
		t.Fatalf("expected 4 blocks (one per class); got %d: %+v", len(got), got)
	}
	wantOrder := []Kind{KindMissingFile, KindWrongVersion, KindForbiddenCred, KindPolicyViolation}
	for i, w := range wantOrder {
		if got[i].Kind != w {
			t.Errorf("blocks[%d].Kind: got %q want %q", i, got[i].Kind, w)
		}
	}
	for _, b := range got {
		if b.Severity != SeverityBlock {
			t.Errorf("Severity: got %q want %q", b.Severity, SeverityBlock)
		}
	}
}
