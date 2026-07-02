package promotionaudit

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Action is the audit action recorded for a promotion event. The three
// values match the DB CHECK constraint in observerstore/schema.sql.
type Action string

const (
	ActionRegister   Action = "register"
	ActionUnregister Action = "unregister"
	ActionInstall    Action = "install"
)

// StageResult is the string value of AuditFields.StageResult. Reserved
// for B2 acceptance pipeline; B6 always writes "".
type StageResult string

const (
	StageResultEmpty StageResult = ""
	StageResultOK    StageResult = "ok"
	StageResultFail  StageResult = "fail"
)

// Regex constants — see spec §7 (a). The user/thread/candidate regex is
// intentionally reused (single grep-friendly pattern family across the
// eval plane, matching WT-1-run-schema RunID bounds). The MCP name
// regex mirrors buildspec.Validate so the two layers reject the same
// name shape. The stage regex is looser (any lowercase snake_case up
// to 64 chars) because B2 owns the enumeration.
var (
	userIDRE   = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)
	threadIDRE = userIDRE
	candIDRE   = userIDRE
	mcpNameRE  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	stageRE    = regexp.MustCompile(`^[a-z_]{1,64}$`)
	// registryHashRE — exactly 64 lowercase hex chars (sha256 hex).
	registryHashRE = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// Sentinel error values. Callers should test with errors.Is; the
// human-readable prefix in wrapped errors names the field for
// operator triage but the sentinel identity is the API contract.
var (
	ErrMissingField           = errors.New("promotionaudit: required field is empty")
	ErrFieldFormat            = errors.New("promotionaudit: field does not match required format")
	ErrUnknownAction          = errors.New("promotionaudit: action must be register|unregister|install")
	ErrInvalidRegistryHash    = errors.New("promotionaudit: registry_hash_after must be 64 lowercase hex chars")
	ErrMissingJoinKey         = errors.New("promotionaudit: candidate_source_task_id is required (join key for consumer-view metrics)")
	ErrSentinelReasonMismatch = errors.New("promotionaudit: candidate_source_task_id sentinel is only valid for ci_seed / batch_import reasons")
	ErrInvalidStage           = errors.New("promotionaudit: stage must match ^[a-z_]{1,64}$ or be empty")
	ErrInvalidStageResult     = errors.New("promotionaudit: stage_result must be '', 'ok', or 'fail'")
	ErrInvalidWorkspaceID     = errors.New("promotionaudit: register/unregister actions require a non-empty workspace_id")
)

// AuditFields is the row shape written to the promotion_audit table.
// Callers construct this and pass it to Writer.Write; the writer runs
// Validate before touching the DB.
type AuditFields struct {
	// WorkspaceID may be "" ONLY for ActionInstall — the offline
	// install pathway per spec §2.3. register / unregister actions
	// MUST have a workspace.
	WorkspaceID string

	// MCPName matches ^[a-z][a-z0-9_]{0,31}$ (mirrors buildspec.Validate).
	MCPName string

	// Action ∈ {ActionRegister, ActionUnregister, ActionInstall}.
	Action Action

	// PromotedByUserID / DriverThreadID / CandidateSourceTaskID all
	// match ^[A-Za-z0-9_-]{8,128}$. See spec §7 (a) for rationale.
	PromotedByUserID      string
	DriverThreadID        string
	CandidateSourceTaskID string

	// PromotionReason is the typed enum; free-string paths are
	// rejected at the caller boundary via Parse.
	PromotionReason Reason

	// RegistryHashAfter is the sha256 hex of the driver's per-slave
	// registry view AFTER this action. Empty only allowed for
	// ActionInstall (offline install has no registry effect).
	RegistryHashAfter string

	// Stage / StageResult — reserved for B2 acceptance pipeline. B6
	// callers pass "" for both. Validate still enforces the shape now
	// so B2 gets a clean surface.
	Stage       string
	StageResult StageResult

	// TS is caller-supplied. The writer serialises via
	// t.UTC().Format(time.RFC3339Nano); a zero TS is not accepted (the
	// writer supplies time.Now().UTC() only in Write, not here — Validate
	// operates on caller-provided values).
	TS time.Time
}

// SentinelCandidateTaskID builds the reserved sentinel value used when
// there is no genuine candidate — spec §7 (g). Both ci_seed and
// batch_import reasons use this shape. Kept as a helper so the format
// only lives in one place; callers use this rather than string-format
// themselves.
func SentinelCandidateTaskID(reason Reason, workspaceID string) (string, error) {
	if workspaceID == "" {
		return "", fmt.Errorf("promotionaudit: SentinelCandidateTaskID needs workspace_id: %w", ErrMissingField)
	}
	// The sentinel MUST match candIDRE. workspace_id may be short/long;
	// truncate/pad to fit the 8-128 window while staying deterministic.
	var prefix string
	switch reason {
	case ReasonCISeed:
		prefix = "ci-seed-"
	case ReasonBatchImport:
		prefix = "batch-import-"
	default:
		return "", fmt.Errorf("promotionaudit: sentinel not valid for reason %q: %w", reason, ErrSentinelReasonMismatch)
	}
	out := prefix + workspaceID
	// Bounds: pad short input, truncate long input while keeping
	// deterministic prefix + suffix hash pattern would be overkill;
	// enforce the regex length here and error if the workspace can't
	// fit. workspace_id already satisfies its own regex upstream so
	// this is a defensive rail.
	if len(out) < 8 {
		out += strings.Repeat("x", 8-len(out))
	}
	if len(out) > 128 {
		out = out[:128]
	}
	if !candIDRE.MatchString(out) {
		return "", fmt.Errorf("promotionaudit: constructed sentinel %q does not match candidate id regex: %w", out, ErrFieldFormat)
	}
	return out, nil
}

// Validate runs all invariant checks on f BEFORE any DB call. Returns
// a wrapped sentinel error so callers can test with errors.Is; the
// wrap prefix names the failing field for operator triage.
func Validate(f AuditFields) error {
	// Required-field checks first (cheapest).
	if f.MCPName == "" {
		return wrap("mcp_name", ErrMissingField)
	}
	if !mcpNameRE.MatchString(f.MCPName) {
		return wrap("mcp_name", ErrFieldFormat)
	}
	if f.Action != ActionRegister && f.Action != ActionUnregister && f.Action != ActionInstall {
		return wrap(fmt.Sprintf("action=%q", f.Action), ErrUnknownAction)
	}
	if f.Action != ActionInstall && f.WorkspaceID == "" {
		return wrap("workspace_id", ErrInvalidWorkspaceID)
	}
	// PromotedByUserID
	if f.PromotedByUserID == "" {
		return wrap("promoted_by_user_id", ErrMissingField)
	}
	if !userIDRE.MatchString(f.PromotedByUserID) {
		return wrap("promoted_by_user_id", ErrFieldFormat)
	}
	// DriverThreadID
	if f.DriverThreadID == "" {
		return wrap("driver_thread_id", ErrMissingField)
	}
	if !threadIDRE.MatchString(f.DriverThreadID) {
		return wrap("driver_thread_id", ErrFieldFormat)
	}
	// Reason — must be one of the four typed constants (defensive: a
	// caller may still assign a Reason("garbage") directly).
	if _, err := Parse(string(f.PromotionReason)); err != nil {
		return wrap("promotion_reason", err)
	}
	// CandidateSourceTaskID — §7 (g) join key required.
	if f.CandidateSourceTaskID == "" {
		return wrap("candidate_source_task_id", ErrMissingJoinKey)
	}
	if !candIDRE.MatchString(f.CandidateSourceTaskID) {
		return wrap("candidate_source_task_id", ErrFieldFormat)
	}
	// Sentinel-vs-reason invariant (§7 (g), both directions).
	isSentinel := strings.HasPrefix(f.CandidateSourceTaskID, "ci-seed-") ||
		strings.HasPrefix(f.CandidateSourceTaskID, "batch-import-")
	sentinelReason := f.PromotionReason == ReasonCISeed || f.PromotionReason == ReasonBatchImport
	if isSentinel != sentinelReason {
		return wrap("candidate_source_task_id/promotion_reason", ErrSentinelReasonMismatch)
	}
	// RegistryHashAfter
	if f.RegistryHashAfter == "" {
		if f.Action != ActionInstall {
			return wrap("registry_hash_after", ErrInvalidRegistryHash)
		}
	} else if !registryHashRE.MatchString(f.RegistryHashAfter) {
		return wrap("registry_hash_after", ErrInvalidRegistryHash)
	}
	// Stage / StageResult
	if f.Stage != "" && !stageRE.MatchString(f.Stage) {
		return wrap("stage", ErrInvalidStage)
	}
	switch f.StageResult {
	case StageResultEmpty, StageResultOK, StageResultFail:
	default:
		return wrap(fmt.Sprintf("stage_result=%q", f.StageResult), ErrInvalidStageResult)
	}
	return nil
}

func wrap(field string, sentinel error) error {
	return fmt.Errorf("promotionaudit: %s: %w", field, sentinel)
}
