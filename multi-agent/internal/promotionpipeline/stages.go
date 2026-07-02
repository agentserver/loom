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
