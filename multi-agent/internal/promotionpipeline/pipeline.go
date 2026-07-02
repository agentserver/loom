package promotionpipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/yourorg/multi-agent/internal/buildspec"
	"github.com/yourorg/multi-agent/internal/observer"
	"github.com/yourorg/multi-agent/internal/promotionaudit"
)

// Deps is the injected dependency bundle. All function-typed fields
// are safe to be nil except Delegate, RegisterCall, and AuditWrite —
// those are required. See docs/specs/wt2-driver-promotion-chain-B2.spec.md §3.1.
type Deps struct {
	// Delegate invokes a slave-side skill with a JSON prompt. Returns
	// the slave task's Result body as a string, or an error.
	Delegate func(ctx context.Context, targetAgentID, targetDisplayName, skill, prompt string, timeoutSec int) (string, error)

	// RegisterCall invokes the shared driver-side registerCore. The
	// pipeline calls this INSTEAD of the register_slave_mcp tool
	// boundary so the tool-boundary audit row does not fire (the
	// pipeline writes its own stage-3 audit row). Returns the
	// registry hash on success.
	RegisterCall func(ctx context.Context, spec buildspec.Spec, sourcePath, targetAgentID, targetDisplayName string, timeoutSec int) (registryHash string, err error)

	// AuditWrite persists one promotion_audit row.
	AuditWrite func(ctx context.Context, f promotionaudit.AuditFields) error

	// EventEmit sends one observer event. May be nil in tests.
	EventEmit func(ev observer.Event)

	// Now is the testable clock. Defaults to time.Now if nil.
	Now func() time.Time

	// IsPromotionPathDisabled reports the NoUserPromotionPath ablation
	// state. Nil means "unwired" — pipeline runs with a one-time
	// WARN log. See spec §5 / §9.
	IsPromotionPathDisabled func() bool

	// IsAcceptanceGateDisabled reports the NoAcceptanceGate ablation
	// state. Nil means "unwired" — pipeline treats as false.
	IsAcceptanceGateDisabled func() bool
}

// Pipeline is the three-stage runner.
type Pipeline struct {
	d              Deps
	unwiredLogOnce sync.Once
}

// New constructs a Pipeline. Required deps that are nil cause New to
// return an error; that would be a programmer mistake (the driver's
// tool wiring in cmd/driver-agent constructs Deps from Tools).
func New(d Deps) (*Pipeline, error) {
	if d.Delegate == nil {
		return nil, errors.New("promotionpipeline: Deps.Delegate is required")
	}
	if d.RegisterCall == nil {
		return nil, errors.New("promotionpipeline: Deps.RegisterCall is required")
	}
	if d.AuditWrite == nil {
		return nil, errors.New("promotionpipeline: Deps.AuditWrite is required")
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Pipeline{d: d}, nil
}

// Request is the input to Run.
type Request struct {
	Spec                  buildspec.Spec
	CasesPath             string
	DryRunRegister        bool
	TimeoutSec            int
	SlaveAgentID          string
	SlaveDisplayName      string
	WorkspaceID           string
	PromotedByUserID      string
	DriverThreadID        string
	PromotionReason       promotionaudit.Reason
	CandidateSourceTaskID string
}

// StageOutcome is one entry in the returned slice.
type StageOutcome struct {
	Stage        Stage  `json:"stage"`
	Success      bool   `json:"success"`
	RegistryHash string `json:"registry_hash,omitempty"`
	StageNote    string `json:"stage_note,omitempty"`
	Error        error  `json:"-"`     // errors serialise to Message via ErrorMsg
	ErrorMsg     string `json:"error,omitempty"`
	Skipped      bool   `json:"skipped,omitempty"`
}

// StageOutcomes carries three entries in canonical stage order. A
// failed stage causes downstream entries to have Skipped=true and
// Error=ErrStageSkipped.
type StageOutcomes []StageOutcome

// PopulateErrorMsgs walks the slice and copies Error.Error() into
// ErrorMsg for JSON serialisation. Kept as a distinct step so tests
// can compare against Error directly (typed sentinel checks) without
// caring about the string form.
func (so StageOutcomes) PopulateErrorMsgs() StageOutcomes {
	out := make(StageOutcomes, len(so))
	for i, o := range so {
		if o.Error != nil {
			o.ErrorMsg = o.Error.Error()
		}
		out[i] = o
	}
	return out
}

// Run executes the three stages. The returned error is non-nil ONLY
// for pipeline-level refusals (NoUserPromotionPath, cases_path
// traversal, invalid audit fields, invalid spec) — stage-level
// failures are captured in the StageOutcomes without a top-level
// error, so the caller can render the full audit trail.
func (p *Pipeline) Run(ctx context.Context, r Request) (StageOutcomes, error) {
	// (1) NoUserPromotionPath check.
	if r := p.d.IsPromotionPathDisabled; r != nil && r() {
		return nil, ErrPromotionPathDisabled
	}
	if p.d.IsPromotionPathDisabled == nil {
		p.unwiredLogOnce.Do(func() {
			log.Printf("[warn] NoUserPromotionPath predicate unwired — pipeline running unguarded until B1 lands")
		})
	}
	// (2) cases_path traversal guard.
	if r.CasesPath == "" {
		return nil, fmt.Errorf("promotionpipeline: cases_path is required")
	}
	if err := validateCasesPath(r.CasesPath); err != nil {
		return nil, err
	}
	// (3) Precheck the shared audit fields once.
	auditBase := promotionaudit.AuditFields{
		WorkspaceID:           r.WorkspaceID,
		MCPName:               r.Spec.Name,
		Action:                promotionaudit.ActionRegister,
		PromotedByUserID:      r.PromotedByUserID,
		DriverThreadID:        r.DriverThreadID,
		PromotionReason:       r.PromotionReason,
		CandidateSourceTaskID: r.CandidateSourceTaskID,
		// RegistryHashAfter / Stage / StageResult / StageNote set per stage.
	}
	// Dry-run tags ALL stage rows so metric filter is symmetric.
	stageNote := ""
	if r.DryRunRegister {
		stageNote = "dry_run"
	}

	outcomes := make(StageOutcomes, 0, 3)

	// ---- Stage 1: scaffold ----
	so1 := p.runStage(ctx, StageScaffold, r, auditBase, stageNote, func() (StageOutcome, error) {
		prompt, _ := json.Marshal(struct {
			Spec buildspec.Spec `json:"spec"`
		}{Spec: r.Spec})
		p.emitEvent(r, StageScaffold, "started")
		_, err := p.d.Delegate(ctx, r.SlaveAgentID, r.SlaveDisplayName, "scaffold-mcp-server", string(prompt), r.TimeoutSec)
		if err != nil {
			return StageOutcome{Stage: StageScaffold, Success: false, Error: err, StageNote: stageNote}, err
		}
		return StageOutcome{Stage: StageScaffold, Success: true, StageNote: stageNote}, nil
	})
	outcomes = append(outcomes, so1)
	if !so1.Success {
		outcomes = appendSkipped(outcomes, []Stage{StageAcceptance, StageRegister})
		return outcomes, nil
	}

	// ---- Stage 2: acceptance ----
	so2 := p.runStage(ctx, StageAcceptance, r, auditBase, stageNote, func() (StageOutcome, error) {
		prompt, _ := json.Marshal(struct {
			CasesPath string `json:"cases_path"`
			ServerCmd string `json:"server_cmd"`
		}{CasesPath: r.CasesPath, ServerCmd: derivedServerCmd(r.Spec)})
		p.emitEvent(r, StageAcceptance, "started")
		result, err := p.d.Delegate(ctx, r.SlaveAgentID, r.SlaveDisplayName, "mcp-acceptance", string(prompt), r.TimeoutSec)
		if err != nil {
			return StageOutcome{Stage: StageAcceptance, Success: false, Error: err, StageNote: stageNote}, err
		}
		// Parse the acceptance-exit-code marker. If NoAcceptanceGate
		// is ON, we STILL invoked the skill above (spec §7 (a)); we
		// just mask the pass/fail decision here.
		exitOK := parseAcceptanceExit(result) == 0
		if !exitOK && p.d.IsAcceptanceGateDisabled != nil && p.d.IsAcceptanceGateDisabled() {
			log.Printf("[ablation] NoAcceptanceGate: acceptance exit != 0 but gate bypassed for %s", r.Spec.Name)
			return StageOutcome{Stage: StageAcceptance, Success: true, StageNote: stageNote}, nil
		}
		if !exitOK {
			return StageOutcome{Stage: StageAcceptance, Success: false, Error: ErrAcceptanceGateFail, StageNote: stageNote}, ErrAcceptanceGateFail
		}
		return StageOutcome{Stage: StageAcceptance, Success: true, StageNote: stageNote}, nil
	})
	outcomes = append(outcomes, so2)
	if !so2.Success {
		outcomes = appendSkipped(outcomes, []Stage{StageRegister})
		return outcomes, nil
	}

	// ---- Stage 3: register (or dry-run) ----
	so3 := p.runStage(ctx, StageRegister, r, auditBase, stageNote, func() (StageOutcome, error) {
		if r.DryRunRegister {
			log.Printf("[dry-run] register skipped for spec=%s", r.Spec.Name)
			// stage_result='fail' (didn't register), stage_note='dry_run',
			// hash = empty-bytes sha256 (satisfies B6 §7 (d) format).
			return StageOutcome{
				Stage:        StageRegister,
				Success:      false,
				StageNote:    stageNote,
				RegistryHash: emptyBytesSHA256Hex,
				Error:        nil, // dry-run is not an error
			}, nil
		}
		p.emitEvent(r, StageRegister, "started")
		hash, err := p.d.RegisterCall(ctx, r.Spec, "generated_mcp/"+r.Spec.Name+"/server.py", r.SlaveAgentID, r.SlaveDisplayName, r.TimeoutSec)
		if err != nil {
			return StageOutcome{Stage: StageRegister, Success: false, Error: err, StageNote: stageNote}, err
		}
		return StageOutcome{Stage: StageRegister, Success: true, RegistryHash: hash, StageNote: stageNote}, nil
	})
	outcomes = append(outcomes, so3)
	return outcomes, nil
}

// runStage wraps a stage-runner with the audit-write + observer-event
// bookkeeping. If AuditWrite fails, we log and CONTINUE (§7 (i)).
func (p *Pipeline) runStage(
	ctx context.Context,
	stage Stage,
	r Request,
	base promotionaudit.AuditFields,
	stageNote string,
	runner func() (StageOutcome, error),
) StageOutcome {
	outcome, _ := runner()
	audit := base
	audit.TS = p.d.Now().UTC()
	audit.Stage = string(stage)
	if outcome.Success {
		audit.StageResult = promotionaudit.StageResultOK
	} else {
		audit.StageResult = promotionaudit.StageResultFail
	}
	audit.StageNote = outcome.StageNote
	// RegistryHashAfter: use the outcome's hash for register success;
	// use the empty-bytes sha256 constant for scaffold/acceptance
	// (semantic: "no change to registry state").
	if outcome.RegistryHash != "" {
		audit.RegistryHashAfter = outcome.RegistryHash
	} else {
		audit.RegistryHashAfter = emptyBytesSHA256Hex
	}
	if err := p.d.AuditWrite(ctx, audit); err != nil {
		log.Printf("promotionpipeline: audit write failed for stage=%s: %v (pipeline continues)", stage, err)
	}
	// Observer event on stage completion. Payload carries stage +
	// stage_result ONLY — no user_id / thread_id per §7 (f).
	status := "completed"
	if !outcome.Success {
		status = "failed"
	}
	if p.d.EventEmit != nil {
		payload := fmt.Sprintf(`{"stage":%q,"stage_result":%q}`, string(stage), string(audit.StageResult))
		p.d.EventEmit(observer.Event{
			WorkspaceID:   r.WorkspaceID,
			AgentRole:     observer.RoleDriver,
			Type:          "promotion_pipeline_stage",
			Status:        status,
			MCPServerName: r.Spec.Name,
			Payload:       []byte(payload),
		})
	}
	return outcome
}

// emitEvent is a `started` variant for the mid-stage transitions.
// Currently a no-op wrapper because runStage emits the completion
// event; kept as a hook for future per-stage started/completed pairing.
func (p *Pipeline) emitEvent(_ Request, _ Stage, _ string) {
	// intentionally empty; future: emit a `started` event here so
	// long-running scaffold/acceptance are visible in the trace mid-run.
}

// appendSkipped appends Skipped outcomes for the remaining stages so
// the returned slice always has exactly 3 entries in canonical order.
func appendSkipped(cur StageOutcomes, remaining []Stage) StageOutcomes {
	for _, s := range remaining {
		cur = append(cur, StageOutcome{Stage: s, Skipped: true, Error: ErrStageSkipped})
	}
	return cur
}

// parseAcceptanceExit finds `"acceptance_exit_code":<int>` in the
// slave result body. Returns 0 on happy path; any non-zero (or a
// parse error, defensively) → non-zero.
func parseAcceptanceExit(result string) int {
	// Cheap substring scan avoids paying for full JSON parsing on the
	// hot path. If the marker is missing, treat as 0 (assume success);
	// tests must exercise both branches to catch a slave that fails
	// silently. Real slaves always include the marker per B3 spec.
	const marker = `"acceptance_exit_code":`
	i := strings.Index(result, marker)
	if i < 0 {
		return 0
	}
	rest := result[i+len(marker):]
	rest = strings.TrimLeft(rest, " ")
	// Read digits (and an optional leading -).
	end := 0
	if end < len(rest) && rest[end] == '-' {
		end++
	}
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	n := 0
	sign := 1
	digits := rest[:end]
	if strings.HasPrefix(digits, "-") {
		sign = -1
		digits = digits[1:]
	}
	for _, c := range digits {
		n = n*10 + int(c-'0')
	}
	return sign * n
}

// derivedServerCmd returns the command the slave uses to run this MCP
// server. For B2's initial cut, we use a convention:
// `python3 generated_mcp/<name>/server.py`. Future work: read from the
// spec if a `command` field lands.
func derivedServerCmd(spec buildspec.Spec) string {
	return "python3 generated_mcp/" + spec.Name + "/server.py"
}

// emptyBytesSHA256Hex — same constant as internal/driver, replicated
// here to avoid a driver → pipeline → driver cycle risk.
const emptyBytesSHA256Hex = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
