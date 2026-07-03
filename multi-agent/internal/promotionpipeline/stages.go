// Package promotionpipeline implements the three-stage
// scaffold → acceptance → register pipeline described in
// docs/specs/wt2-driver-promotion-chain-B2.spec.md. The pipeline is
// invoked by the driver-side `promotion_pipeline` MCP tool (and by
// the tests/scripts/scaffold_acceptance_register_e2e.sh smoke); it
// writes one promotion_audit row per stage using the shared B6 audit
// table.
package promotionpipeline

import (
	"errors"
	"net/url"
	"path/filepath"
	"strings"
)

// Stage identifies which pipeline step a StageOutcome corresponds to.
type Stage string

const (
	StageScaffold   Stage = "scaffold"
	StageAcceptance Stage = "acceptance"
	StageRegister   Stage = "register"
)

// Stages returns the canonical stage order.
func Stages() []Stage {
	return []Stage{StageScaffold, StageAcceptance, StageRegister}
}

// ErrStageSkipped is returned in a StageOutcome for stages that were
// skipped because an earlier stage failed.
var ErrStageSkipped = errors.New("promotionpipeline: stage skipped due to earlier failure")

// ErrAcceptanceGateFail is returned when the acceptance stage runs but
// its --cases pass rate is below the gate. The pipeline maps this to
// stage_result='fail' and refuses to invoke stage 3. Even under
// NoAcceptanceGate the acceptance skill is STILL invoked (spec §7 (a));
// the flag only masks the pass/fail decision.
var ErrAcceptanceGateFail = errors.New("promotionpipeline: acceptance gate rejected the MCP")

// ErrPromotionPathDisabled is returned when NoUserPromotionPath is on;
// the tool boundary maps this to FailPolicyViolation.
var ErrPromotionPathDisabled = errors.New("promotionpipeline: driver-initiated promotion path disabled by NoUserPromotionPath")

// ErrInvalidCasesPath is returned when the caller-supplied
// cases_path is refused by the traversal guard.
var ErrInvalidCasesPath = errors.New("promotionpipeline: cases_path contains path traversal or leads outside the workspace")

// ErrInvalidScaffoldSourcePath is returned when the scaffold slave's
// returned source_path fails validation. A compromised or buggy slave
// could return an absolute path or a traversal-shaped value; stage 3
// refuses to hand that to register. See PR #71 review P1-B2-1.
var ErrInvalidScaffoldSourcePath = errors.New("promotionpipeline: scaffold source_path is absolute or contains traversal — refusing to register")

// validateScaffoldSourcePath enforces the same "must be workspace-
// relative, no `..` segments" invariant that
// internal/driver.AssertSafeRelPath enforces for driver-local read
// paths. Copied inline rather than imported so this package stays
// free of the internal/driver import (which would create a cycle
// through promotion_pipeline_tool.go).
//
// Rules (matching driver.AssertSafeRelPath):
//   - Empty is accepted (a scaffold that legitimately omits the
//     field lets stage 3 fall back to the convention path — see
//     pipeline.go extractSourcePath fallback).
//   - Absolute paths (leading `/`) are refused.
//   - Any `..` segment after Clean is refused.
//   - URL-encoded traversals are refused via a percent-decoded
//     re-check (defence-in-depth even though slaves rarely encode
//     paths).
func validateScaffoldSourcePath(p string) error {
	if p == "" {
		return nil
	}
	// PR #71 round-2 review P2-D: also refuse control chars,
	// backslash-shaped paths, and null bytes — matches the
	// register_slave_mcp tool-boundary guard in
	// driver.validateRegisterSourcePath.
	for _, r := range p {
		if r < 0x20 {
			return ErrInvalidScaffoldSourcePath
		}
	}
	if strings.HasPrefix(p, "/") {
		return ErrInvalidScaffoldSourcePath
	}
	if strings.Contains(p, `\`) {
		return ErrInvalidScaffoldSourcePath
	}
	decoded, err := url.PathUnescape(p)
	if err == nil && decoded != p && strings.Contains(decoded, "..") {
		return ErrInvalidScaffoldSourcePath
	}
	clean := filepath.Clean(p)
	for _, seg := range strings.Split(clean, "/") {
		if seg == ".." {
			return ErrInvalidScaffoldSourcePath
		}
	}
	if strings.Contains(p, "..") {
		return ErrInvalidScaffoldSourcePath
	}
	return nil
}

// validateCasesPath enforces §7 (h): reject any `..` segment (leading
// or mid-path), URL-encoded traversal, and absolute paths under
// /etc/. Accepts relative paths whose cleaned form does not touch
// parent directories. Empty input is a caller mistake (missing arg)
// but this function only enforces path shape — the caller checks
// non-empty separately.
func validateCasesPath(p string) error {
	if p == "" {
		return nil // caller checks required-ness separately
	}
	// Reject URL-encoded traversal by percent-decoding once and
	// re-checking. A single decoding pass is enough because we don't
	// pass the decoded string on — we only check it for the `..`
	// pattern.
	decoded, err := url.PathUnescape(p)
	if err == nil && decoded != p {
		if strings.Contains(decoded, "..") {
			return ErrInvalidCasesPath
		}
	}
	// Absolute /etc/ path — always refused (traversal-shaped even
	// without ..)
	if strings.HasPrefix(p, "/etc/") {
		return ErrInvalidCasesPath
	}
	// Clean and re-inspect. Any `..` segment (leading or mid-path)
	// after Clean means the path escaped the workspace root.
	clean := filepath.Clean(p)
	// Split on either OS separator; on Linux we operate with `/`.
	segments := strings.Split(clean, "/")
	for _, seg := range segments {
		if seg == ".." {
			return ErrInvalidCasesPath
		}
	}
	// Also refuse any raw `..` in the pre-clean form (defense-in-depth
	// for a Windows-path input mis-parsed by filepath.Clean).
	if strings.Contains(p, "..") {
		return ErrInvalidCasesPath
	}
	return nil
}
