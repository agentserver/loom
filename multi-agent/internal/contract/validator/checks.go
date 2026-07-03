package validator

import (
	"fmt"
	"log"
	"path"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/yourorg/multi-agent/internal/capability"
	"github.com/yourorg/multi-agent/internal/contract"
)

// ---------------------------------------------------------------------
// §3.1 — checkMissingFile
// ---------------------------------------------------------------------

// checkMissingFile emits one block per file-shaped ReadArtifact whose
// Name does not path.Match against any Snapshot.Files[].PathPattern.
// Malformed patterns are logged (never surfaced in Block.Detail per
// §7(f)) and treated as no-match.
func checkMissingFile(tc contract.TaskContract, snap capability.Snapshot) []Block {
	var out []Block
	for i, art := range tc.DataContract.ReadArtifacts {
		if !isFileArtifact(art) {
			continue
		}
		if matchAnyFile(snap.Files, art.Name) {
			continue
		}
		out = append(out, newBlock(
			KindMissingFile,
			fmt.Sprintf("data_contract.read_artifacts[%d].name", i),
			art.Name,
			"no snapshot file resource matches",
		))
	}
	return out
}

// isFileArtifact identifies file-shaped artifacts per §3.1: explicit
// Kind=="file", OR empty Kind with a Name that contains a slash or a
// dot (heuristic for "looks like a path"). A plain identifier slips
// through as NOT-a-file.
func isFileArtifact(art contract.ArtifactRef) bool {
	if art.Kind == "file" {
		return true
	}
	if art.Kind != "" {
		return false
	}
	return strings.ContainsAny(art.Name, "/.")
}

// matchAnyFile returns true iff any FileResource's PathPattern matches
// name via stdlib path.Match. A pattern-syntax error is logged and
// treated as no-match; the pattern itself never enters the returned
// blocks (§7(f)).
func matchAnyFile(files []capability.FileResource, name string) bool {
	for _, fr := range files {
		ok, err := path.Match(fr.PathPattern, name)
		if err != nil {
			// KindDetail is one of the 4 enum values → safe to log.
			// PathPattern is caller-controlled; NEVER include it in
			// logs OR Block.Detail — a malformed pattern is a
			// snapshot data-quality issue, not the artifact's fault.
			log.Printf("[validator] malformed path pattern in snapshot kind=%s (pattern hidden per §7(f))", fr.KindDetail)
			continue
		}
		if ok {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------
// §3.2 — checkWrongVersion (§7(b) semver library, not split-by-dot)
// ---------------------------------------------------------------------

// checkWrongVersion reads both legacy Tools []string (presence-only)
// and versioned ToolRequirements. Comparison uses
// golang.org/x/mod/semver — NEVER a hand-rolled split-by-dot (see
// spec §7(b) rc-prerelease rationale).
func checkWrongVersion(tc contract.TaskContract, snap capability.Snapshot) []Block {
	var out []Block

	// Build a set of tool names that already appear in
	// ToolRequirements so the legacy Tools pass can skip them —
	// otherwise a contract that lists the same name in both slices
	// would emit two blocks for one missing binary, inflating
	// blocks_total and confusing human review. Round-5 fresh review P2.
	versioned := make(map[string]struct{}, len(tc.CapabilityRequirements.ToolRequirements))
	for _, req := range tc.CapabilityRequirements.ToolRequirements {
		versioned[req.Name] = struct{}{}
	}

	// Presence-only pass over legacy Tools []string.
	for i, name := range tc.CapabilityRequirements.Tools {
		if _, dup := versioned[name]; dup {
			// The ToolRequirements pass below will emit a more
			// informative block for this name.
			continue
		}
		if _, ok := lookupTool(snap.Tools, name); ok {
			continue
		}
		out = append(out, newBlock(
			KindWrongVersion,
			fmt.Sprintf("capability_requirements.tools[%d]", i),
			"<any version installed>",
			"<not installed>",
		))
	}

	// Versioned pass over ToolRequirements.
	for i, req := range tc.CapabilityRequirements.ToolRequirements {
		field := fmt.Sprintf("capability_requirements.tool_requirements[%d]", i)

		got, ok := lookupTool(snap.Tools, req.Name)
		if !ok {
			expected := req.MinVersion
			if expected == "" {
				expected = "<any version installed>"
			} else {
				expected = ">=" + expected
			}
			out = append(out, newBlock(KindWrongVersion, field, expected, "<not installed>"))
			continue
		}
		if req.MinVersion == "" {
			continue // presence satisfied, no version constraint
		}

		canonReq := semver.Canonical(ensureVPrefix(req.MinVersion))
		if canonReq == "" {
			out = append(out, newBlock(
				KindWrongVersion, field, req.MinVersion,
				"<contract min_version unparseable: "+req.MinVersion+">",
			))
			continue
		}
		canonGot := semver.Canonical(ensureVPrefix(got.Version))
		if canonGot == "" {
			out = append(out, newBlock(
				KindWrongVersion, field, ">="+req.MinVersion,
				"<unparseable: "+got.Version+">",
			))
			continue
		}
		if semver.Compare(canonGot, canonReq) < 0 {
			out = append(out, newBlock(KindWrongVersion, field, ">="+req.MinVersion, got.Version))
		}
	}
	return out
}

// lookupTool returns the ToolVersion whose Name equals name
// (case-sensitive), and a boolean present flag.
func lookupTool(tools []capability.ToolVersion, name string) (capability.ToolVersion, bool) {
	for _, tv := range tools {
		if tv.Name == name {
			return tv, true
		}
	}
	return capability.ToolVersion{}, false
}

// ensureVPrefix prepends 'v' if missing. Required because
// semver.Canonical("1.22.0") returns "" — the mod/semver package only
// accepts the leading-v grammar. Callers pass user-supplied version
// strings that usually lack the v.
func ensureVPrefix(s string) string {
	if s == "" || s[0] == 'v' || s[0] == 'V' {
		return s
	}
	return "v" + s
}

// ---------------------------------------------------------------------
// §3.3 — checkForbiddenCred (§7(e) exact case-sensitive equality)
// ---------------------------------------------------------------------

// checkForbiddenCred emits one block per contract-side forbidden alias
// whose EXACT case-sensitive string form appears in snap.Credentials.
// Any substring / prefix / case-insensitive comparison would let an
// attacker rename a forbidden alias with a suffix and bypass the check.
func checkForbiddenCred(tc contract.TaskContract, snap capability.Snapshot) []Block {
	var out []Block
	for i, want := range tc.CapabilityRequirements.ForbiddenAliases {
		field := fmt.Sprintf("capability_requirements.forbidden_aliases[%d]", i)

		// §7(e): validate the contract-side alias shape so operators
		// get an actionable block instead of a silent pass.
		if _, err := capability.NewCredentialAlias(want); err != nil {
			out = append(out, newBlock(KindForbiddenCred, field, want, "<malformed alias>"))
			continue
		}

		for _, have := range snap.Credentials {
			// EXACT MATCH ONLY. Do not touch: strings.HasPrefix,
			// strings.Contains, strings.EqualFold. See §7(e).
			if string(have) == want {
				out = append(out, newBlock(KindForbiddenCred, field, "<absent>", string(have)))
				break // one block per forbidden entry is enough
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------
// §3.4 — checkPolicyViolation
// ---------------------------------------------------------------------

// checkPolicyViolation ensures snap.Network is AT LEAST
// tc.ExecutionPolicy.RequiredReach on the semantic ladder
// none < loopback-only < intranet < internet.
func checkPolicyViolation(tc contract.TaskContract, snap capability.Snapshot) []Block {
	req := tc.ExecutionPolicy.RequiredReach
	if req == "" {
		return nil
	}
	reqRank := reachRank(req)
	if reqRank < 0 {
		// The contract carries an unknown reach — surface as a
		// policy_violation so the operator fixes it instead of
		// silently passing.
		return []Block{newBlock(
			KindPolicyViolation,
			"execution_policy.required_reach",
			string(req),
			"<contract required_reach unknown>",
		)}
	}
	gotRank := reachRank(snap.Network)
	if gotRank < 0 {
		return []Block{newBlock(
			KindPolicyViolation,
			"execution_policy.required_reach",
			string(req),
			string(snap.Network),
		)}
	}
	if gotRank < reqRank {
		return []Block{newBlock(
			KindPolicyViolation,
			"execution_policy.required_reach",
			string(req),
			string(snap.Network),
		)}
	}
	return nil
}

// reachRank returns the semantic-ladder position of a NetworkReach.
// Returns -1 for values outside the WT-1 enum.
func reachRank(r capability.NetworkReach) int {
	switch r {
	case capability.NetworkNone:
		return 0
	case capability.NetworkLoopbackOnly:
		return 1
	case capability.NetworkIntranet:
		return 2
	case capability.NetworkInternet:
		return 3
	default:
		return -1
	}
}
