package contract

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"unicode/utf8"
)

// Sentinels — use errors.Is on the rejection path. See spec §2.5.
var (
	// ErrMissingFields covers the §2.2 + §2.2.1 required-field check.
	// Returned (wrapped inside *ValidationError.Causes) whenever any
	// lifecycle or trace field is missing. The exact missing fields are
	// in *ValidationError.Missing, in §2.2 table order.
	ErrMissingFields = errors.New("task contract: required fields missing")

	// ErrRecoveryHintTooLong is the §2.4 (a) cap. We count runes, not
	// bytes, so a hint of N multi-byte runes is judged the same as N
	// ASCII runes.
	ErrRecoveryHintTooLong = errors.New("task contract: recovery_hint exceeds 4096 runes")

	// ErrRecoveryHintContainsControlChar is the §2.4 (b) ban. Tab,
	// newline, and carriage return are explicitly permitted; other
	// control characters (BEL, ESC, DEL, etc.) are rejected.
	ErrRecoveryHintContainsControlChar = errors.New("task contract: recovery_hint contains control character")

	// ErrRecoveryHintLooksLikeHTML is the §2.4 (c) substring ban. Match
	// is case-insensitive against an 8-prefix list (see
	// recoveryHintHTMLPrefixes). Pure substring check — no regex, to
	// avoid a redos surface on attacker-controlled input.
	ErrRecoveryHintLooksLikeHTML = errors.New("task contract: recovery_hint contains HTML/script/javascript: prefix")

	// ErrContractFormalizationDisabled is returned by EnforceContract
	// when DisableContractEntirely is true. The entry tool is expected
	// to errors.Is-check this sentinel and fall back to the
	// natural-language delegation path (see contract_tools.go's
	// callNaturalLanguageFallback per spec §3.2 / §4).
	ErrContractFormalizationDisabled = errors.New("task contract: formalization disabled by NoContractFormalization ablation")
)

// recoveryHintMaxRunes is the §2.4 (a) length cap. Tests T10/T11 pin the
// exact boundary (4096 passes, 4097 rejects).
const recoveryHintMaxRunes = 4096

// recoveryHintHTMLPrefixes is the §2.4 (c) substring ban list. Exactly 8
// entries — frozen at v1 per the spec. All comparisons happen against
// strings.ToLower(hint), so the entries here are lowercase.
var recoveryHintHTMLPrefixes = []string{
	"<script",
	"<iframe",
	"<object",
	"<embed",
	"<svg",
	"<img",
	"javascript:",
	"data:text/html",
}

// ValidationError is the typed rejection from Validate. Use errors.Is
// against the package sentinels (ErrMissingFields,
// ErrRecoveryHintTooLong, etc.) — string contents are not part of the
// API contract.
//
// Error() format MUST include the legacy substring "<field> is required"
// for each Missing entry that names a required field, so the pre-WT-1
// `strings.Contains(err.Error(), "intent.goal is required")` assertions
// in internal/contract/contract_test.go continue to pass.
type ValidationError struct {
	// Missing lists every required field that failed §2.2 / §2.2.1, in
	// §2.2 table order (lifecycle 1..7 then trace T1..T2). Stable order
	// matters for test assertions and reproducible operator diagnostics.
	Missing []string

	// Causes is the slice of package sentinels that apply.
	// errors.Is(ve, ErrMissingFields) succeeds iff ErrMissingFields is
	// in this slice. (Go 1.20+ multi-unwrap.)
	Causes []error
}

func (e *ValidationError) Error() string {
	var parts []string
	for _, name := range e.Missing {
		parts = append(parts, "task contract: "+name+" is required")
	}
	for _, c := range e.Causes {
		// Skip ErrMissingFields here — its per-field "is required" lines
		// were already produced by the loop above. Other sentinels
		// (recovery_hint length / control / HTML) carry their own
		// message text.
		if errors.Is(c, ErrMissingFields) {
			continue
		}
		parts = append(parts, c.Error())
	}
	if len(parts) == 0 {
		// Defensive: a ValidationError with no Missing and no other
		// causes is a bug (constructor should have returned nil). Surface
		// loudly via an explanatory message rather than silently being
		// an empty-string error.
		return "task contract: validation failed with no specific cause (programmer error)"
	}
	return strings.Join(parts, "; ")
}

// Unwrap returns Causes for multi-unwrap (Go 1.20+). errors.Is walks the
// slice and matches against any registered sentinel.
func (e *ValidationError) Unwrap() []error {
	return e.Causes
}

// ApplyDefaults populates ExecutionPolicy enum fields and pointer
// defaults. Mutates in place via pointer receiver. Existing semantics
// preserved — this worktree does not change defaulting behaviour.
func (tc *TaskContract) ApplyDefaults() {
	if tc.Version == 0 {
		tc.Version = Version
	}
	if tc.ExecutionPolicy.Routing == "" {
		tc.ExecutionPolicy.Routing = RoutingDirectFirst
	}
	if tc.ExecutionPolicy.AllowMaster == nil {
		tc.ExecutionPolicy.AllowMaster = Bool(true)
	}
	if tc.ExecutionPolicy.AllowCodeArtifacts == nil {
		tc.ExecutionPolicy.AllowCodeArtifacts = Bool(true)
	}
	if tc.ExecutionPolicy.CodePersistence == "" {
		tc.ExecutionPolicy.CodePersistence = CodePersistenceObserverArtifactStore
	}
	if tc.ExecutionPolicy.ExposeCodeToUser == "" {
		tc.ExecutionPolicy.ExposeCodeToUser = ExposeCodeOnRequest
	}
	if tc.ExecutionPolicy.WriteMode == "" {
		tc.ExecutionPolicy.WriteMode = WriteModeArtifactOnly
	}
	if tc.ExecutionPolicy.MaxDAGNodes == nil {
		tc.ExecutionPolicy.MaxDAGNodes = Int(6)
	}
	if tc.ExecutionPolicy.MaxDepth == nil {
		tc.ExecutionPolicy.MaxDepth = Int(3)
	}
	if tc.ExecutionPolicy.MaxConcurrency == nil {
		tc.ExecutionPolicy.MaxConcurrency = Int(3)
	}
}

// Validate enforces spec §2.2 (7 lifecycle + 2 trace required fields),
// §2.4 (recovery_hint content rules), and the pre-existing policy /
// write-target enum checks. Emits one ContractCompletenessEvent on
// success (per §2.6 emission gate); emits nothing on rejection.
//
// Pure function: no I/O, no goroutines beyond the synchronous sink
// call. Safe to fuzz (§7 (e)).
//
// Behaviour under ablation: see spec §3.1. With DisableSchemaEnforce
// true, the §2.2 required-field check and the §2.4 recovery_hint
// content checks are SKIPPED, but the policy / write-target enum
// checks STILL fire (they are not part of the schema-enforce
// ablation), and the version-required check on T1 STILL fires
// (unversioned envelopes break the decoder downstream — unrelated
// concern).
func (tc TaskContract) Validate() error {
	skipEnforce := DisableSchemaEnforce

	var missing []string
	if !skipEnforce {
		missing = collectMissing(tc)
	} else {
		// Version (T1) is still required under ablation — see method
		// docstring. The other 8 fields are skipped.
		if tc.Version != Version {
			missing = append(missing, "version")
		}
	}

	var causes []error
	if len(missing) > 0 {
		causes = append(causes, ErrMissingFields)
	}

	if !skipEnforce {
		causes = append(causes, validateRecoveryHintContent(tc.RecoveryHint)...)
	}

	if len(missing) > 0 || len(causes) > 0 {
		// Surface the structured rejection. Policy enum checks below are
		// suppressed in this branch so a missing-field rejection isn't
		// muddied with a downstream-defaulted-but-not-yet-applied policy
		// error. Callers that ApplyDefaults first (the standard path)
		// see the policy check on the next call once the missing field
		// is supplied.
		return &ValidationError{Missing: missing, Causes: causes}
	}

	// Version check applies on the non-skipped path too; collectMissing
	// already covered it, so this branch is just the policy + write-target
	// enum tail (existing semantics — kept verbatim from pre-WT-1 code).
	if tc.Version != Version {
		// Reached only under DisableSchemaEnforce with a valid version
		// AND no other missing fields — defensive.
		return fmt.Errorf("unsupported contract version: %d", tc.Version)
	}

	for i, wt := range tc.DataContract.WriteTargets {
		if wt.Type == "" {
			return fmt.Errorf("data_contract.write_targets[%d].type is required", i)
		}
		if wt.Type != WriteTargetArtifact {
			return fmt.Errorf("data_contract.write_targets[%d].type %q is not supported", i, wt.Type)
		}
		if wt.Kind == "" {
			return fmt.Errorf("data_contract.write_targets[%d].kind is required", i)
		}
		if wt.Name == "" {
			return fmt.Errorf("data_contract.write_targets[%d].name is required", i)
		}
		if wt.Kind == "code" && !tc.ExecutionPolicy.AllowsCodeArtifacts() {
			return fmt.Errorf("data_contract.write_targets[%d] code write target requires execution_policy.allow_code_artifacts", i)
		}
	}
	if err := validatePolicy(tc.ExecutionPolicy); err != nil {
		return err
	}

	// Success path: emit the completeness event. Per §2.6 emission gate,
	// this fires iff Validate returns nil; if DisableSchemaEnforce is
	// true the bitmap will reflect the truth (e.g. 3/7), NOT a fake 7/7.
	emitCompleteness(tc)
	return nil
}

// collectMissing walks the §2.2 + §2.2.1 + T1/T2 required-field table in
// order and returns the JSON-path names that are missing. Sets out via a
// fresh slice so the caller may mutate it.
func collectMissing(tc TaskContract) []string {
	var missing []string

	// §2.2 #1 — intent.goal
	if strings.TrimSpace(tc.Intent.Goal) == "" {
		missing = append(missing, "intent.goal")
	}
	// §2.2 #2 — intent.success_criteria (the success_oracle, per §A2)
	if !hasNonEmptyEntry(tc.Intent.SuccessCriteria) {
		missing = append(missing, "intent.success_criteria")
	}
	// §2.2 #3 — data_contract.read_artifacts (nil-vs-empty per §2.2.1)
	if tc.DataContract.ReadArtifacts == nil {
		missing = append(missing, "data_contract.read_artifacts")
	}
	// §2.2 #4 — data_contract.write_targets
	if len(tc.DataContract.WriteTargets) == 0 {
		missing = append(missing, "data_contract.write_targets")
	}
	// §2.2 #5 — capability_requirements
	if !capabilityRequirementsPresent(tc.CapabilityRequirements) {
		missing = append(missing, "capability_requirements")
	}
	// §2.2 #6 — execution_policy
	if !executionPolicyPresent(tc.ExecutionPolicy) {
		missing = append(missing, "execution_policy")
	}
	// §2.2 #7 — recovery_hint
	if strings.TrimSpace(tc.RecoveryHint) == "" {
		missing = append(missing, "recovery_hint")
	}
	// T1 — version
	if tc.Version != Version {
		missing = append(missing, "version")
	}
	// T2 — conversation_id
	if strings.TrimSpace(tc.ConversationID) == "" {
		missing = append(missing, "conversation_id")
	}
	return missing
}

// validateRecoveryHintContent runs the §2.4 (a) length cap, (b)
// control-char ban, and (c) HTML-prefix ban on hint. Returns the set of
// applicable sentinels (zero, one, or more — the checks are independent;
// a hint that's both too long AND contains a script tag triggers two
// sentinels). An empty hint is fine here — the §2.2 #7 missing check
// owns that case.
func validateRecoveryHintContent(hint string) []error {
	if hint == "" {
		return nil
	}
	var causes []error
	if utf8.RuneCountInString(hint) > recoveryHintMaxRunes {
		causes = append(causes, ErrRecoveryHintTooLong)
	}
	for _, r := range hint {
		if r == '\t' || r == '\n' || r == '\r' {
			continue
		}
		if r < 0x20 || r == 0x7f {
			causes = append(causes, ErrRecoveryHintContainsControlChar)
			break
		}
	}
	lower := strings.ToLower(hint)
	for _, prefix := range recoveryHintHTMLPrefixes {
		if strings.Contains(lower, prefix) {
			causes = append(causes, ErrRecoveryHintLooksLikeHTML)
			break
		}
	}
	return causes
}

// EnforceContract is the entry-tool guard wrapper per spec §2.7. The
// submit_contract_task handler MUST call this on its first non-trivial
// line (after json.Unmarshal), with no DiscoverAgents / observer-relay
// / log call between them. The §7 (a) tests pin this.
//
// The caller passes *TaskContract because ApplyDefaults is a pointer
// receiver and must mutate in place; Validate is a value-receiver and
// is called via Go's implicit deref on the dereferenced value.
//
// Behaviour:
//
//  1. If DisableContractEntirely == true, returns
//     ErrContractFormalizationDisabled. Caller is expected to errors.Is-
//     check and fall back to natural-language delegation per §3.2.
//  2. Otherwise calls (*tc).ApplyDefaults() to populate enum defaults.
//  3. If DisableSchemaEnforce == true, logs the §3.1 skip line and
//     returns nil (skipping the §2.2 required-field check; Validate
//     itself also honors the flag — see §3.1).
//  4. Otherwise calls (*tc).Validate() and returns its error verbatim.
func EnforceContract(tc *TaskContract) error {
	if DisableContractEntirely {
		return ErrContractFormalizationDisabled
	}
	tc.ApplyDefaults()
	if DisableSchemaEnforce {
		// §3.1: per-call log line with conversation ID for postmortem
		// correlation. The literal "[ablation] NoTypedContracts:" is
		// the substring T24 greps for; do not soften this wording
		// without updating the test.
		//
		// Use %q (Go-quoted, escapes \n / \r / control chars / non-
		// printable) instead of %s to defeat log-injection via an
		// attacker-controlled conversation_id containing newlines:
		// a literal newline in conversation_id would otherwise let
		// the operator forge a second "[ablation] ..." line in the
		// audit trail. See PR #52 round-3 review P1-2.
		log.Printf("[ablation] NoTypedContracts: skipped enforce on conversation=%q", tc.ConversationID)
		// Validate still runs to handle policy enum checks and the
		// T1 version requirement; ablation just skips the §2.2 +
		// §2.4 paths.
		return tc.Validate()
	}
	return tc.Validate()
}

// Bool and Int are small helpers used by tests and ApplyDefaults to
// build *bool / *int defaults.
func Bool(v bool) *bool { return &v }
func Int(v int) *int    { return &v }

func (p ExecutionPolicy) AllowsMaster() bool {
	return p.AllowMaster == nil || *p.AllowMaster
}

func (p ExecutionPolicy) AllowsCodeArtifacts() bool {
	return p.AllowCodeArtifacts == nil || *p.AllowCodeArtifacts
}

func (p ExecutionPolicy) DAGNodeLimit() int {
	if p.MaxDAGNodes == nil {
		return 6
	}
	return *p.MaxDAGNodes
}

func (p ExecutionPolicy) DepthLimit() int {
	if p.MaxDepth == nil {
		return 3
	}
	return *p.MaxDepth
}

func (p ExecutionPolicy) ConcurrencyLimit() int {
	if p.MaxConcurrency == nil {
		return 3
	}
	return *p.MaxConcurrency
}

func validatePolicy(p ExecutionPolicy) error {
	switch p.Routing {
	case RoutingDirectFirst, RoutingMasterOnly:
	default:
		return fmt.Errorf("execution_policy.routing %q is not supported", p.Routing)
	}
	if p.CodePersistence != CodePersistenceObserverArtifactStore {
		return fmt.Errorf("execution_policy.code_persistence %q is not supported", p.CodePersistence)
	}
	if p.ExposeCodeToUser != ExposeCodeOnRequest {
		return fmt.Errorf("execution_policy.expose_code_to_user %q is not supported", p.ExposeCodeToUser)
	}
	switch p.WriteMode {
	case WriteModeArtifactOnly, WriteModePatch, WriteModeRepoCommit:
	default:
		return fmt.Errorf("execution_policy.write_mode %q is not supported", p.WriteMode)
	}
	if p.WriteMode == WriteModeRepoCommit && !p.RequireUserApprovalForRepoWrites {
		return fmt.Errorf("repo_commit requires execution_policy.require_user_approval_for_repo_writes")
	}
	if p.MaxDAGNodes != nil && *p.MaxDAGNodes < 1 {
		return fmt.Errorf("execution_policy.max_dag_nodes must be >= 1")
	}
	if p.MaxDepth != nil && *p.MaxDepth < 1 {
		return fmt.Errorf("execution_policy.max_depth must be >= 1")
	}
	if p.MaxConcurrency != nil && *p.MaxConcurrency < 1 {
		return fmt.Errorf("execution_policy.max_concurrency must be >= 1")
	}
	return nil
}
