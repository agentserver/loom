package contract

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

// spySink captures emitted events. Safe for concurrent emits (the
// T35 sink-swap test uses this).
type spySink struct {
	mu     sync.Mutex
	events []ContractCompletenessEvent
}

func (s *spySink) EmitContractCompleteness(ev ContractCompletenessEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
}

func (s *spySink) snapshot() []ContractCompletenessEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ContractCompletenessEvent, len(s.events))
	copy(out, s.events)
	return out
}

// freshSink replaces the package-level sink with a fresh spy and
// arranges t.Cleanup to restore the prior. Tests that emit events MUST
// use this helper; without it sink state would leak between tests.
func freshSink(t *testing.T) *spySink {
	t.Helper()
	spy := &spySink{}
	prior := RegisterCompletenessSink(spy)
	t.Cleanup(func() { RegisterCompletenessSink(prior) })
	return spy
}

// withAblationFlag sets *flag to v and arranges t.Cleanup to restore.
// Tests that touch ablation MUST NOT t.Parallel() — the flag is a
// package-level var the production hot path reads unsynchronized.
func withAblationFlag(t *testing.T, flag *bool, v bool) {
	t.Helper()
	prior := *flag
	*flag = v
	t.Cleanup(func() { *flag = prior })
}

// T2 (split half — full 7/7 fixture validates and emits with ratio 1.0).
// The matching error-path companion is in contract_test.go's existing
// negative tests.
func TestValidate_FullContract_OK(t *testing.T) {
	spy := freshSink(t)

	tc := TaskContract{
		Version:        1,
		ConversationID: "conv-1",
		Intent: IntentSpec{
			Goal:            "do the thing",
			SuccessCriteria: []string{"thing is done"},
		},
		DataContract: DataContract{
			ReadArtifacts: []ArtifactRef{},
			WriteTargets:  []WriteTarget{{Type: WriteTargetArtifact, Kind: "code", Name: "x.go"}},
		},
		CapabilityRequirements: CapabilityRequirements{Skills: []string{"chat"}},
		RecoveryHint:           "hint",
	}
	tc.ApplyDefaults()
	if err := tc.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	events := spy.snapshot()
	if len(events) != 1 {
		t.Fatalf("emitted events = %d, want 1", len(events))
	}
	if got, want := events[0].CompletenessRatio, 1.0; got != want {
		t.Errorf("ratio = %v, want %v", got, want)
	}
	if got, want := len(events[0].PresentFields), 7; got != want {
		t.Errorf("len(PresentFields) = %d, want %d (fields=%v)", got, want, events[0].PresentFields)
	}
}

// T3 — missing recovery_hint → ErrMissingFields with recovery_hint listed.
func TestValidate_MissingRecoveryHint_Reject(t *testing.T) {
	freshSink(t) // no event expected, but pin the sink anyway
	tc := TaskContract{
		Version:        1,
		ConversationID: "conv-1",
		Intent: IntentSpec{
			Goal:            "do",
			SuccessCriteria: []string{"done"},
		},
		DataContract: DataContract{
			ReadArtifacts: []ArtifactRef{},
			WriteTargets:  []WriteTarget{{Type: WriteTargetArtifact, Kind: "code", Name: "x.go"}},
		},
		CapabilityRequirements: CapabilityRequirements{Skills: []string{"chat"}},
		// RecoveryHint intentionally empty
	}
	tc.ApplyDefaults()
	err := tc.Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
	var ve *ValidationError
	if !errorAs(err, &ve) {
		t.Fatalf("err type = %T, want *ValidationError", err)
	}
	if len(ve.Missing) != 1 || ve.Missing[0] != "recovery_hint" {
		t.Errorf("Missing = %v, want [recovery_hint]", ve.Missing)
	}
	if !errorIs(err, ErrMissingFields) {
		t.Errorf("errors.Is(err, ErrMissingFields) = false")
	}
}

// T4 — multiple missing fields → Missing slice contains ALL in §2.2 order.
func TestValidate_MissingMultipleFields_AllReported(t *testing.T) {
	freshSink(t)
	// Construct: goal missing, success_criteria missing, recovery_hint missing,
	// read_artifacts nil. Keep version/conv_id set and write_targets set so
	// we test specifically the multi-missing path on the lifecycle fields.
	tc := TaskContract{
		Version:        1,
		ConversationID: "conv-1",
		DataContract: DataContract{
			// ReadArtifacts nil
			WriteTargets: []WriteTarget{{Type: WriteTargetArtifact, Kind: "code", Name: "x.go"}},
		},
		CapabilityRequirements: CapabilityRequirements{Skills: []string{"chat"}},
	}
	tc.ApplyDefaults()
	err := tc.Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
	var ve *ValidationError
	if !errorAs(err, &ve) {
		t.Fatalf("err type = %T, want *ValidationError", err)
	}
	want := []string{"intent.goal", "intent.success_criteria", "data_contract.read_artifacts", "recovery_hint"}
	if !equalSlices(ve.Missing, want) {
		t.Errorf("Missing = %v, want %v (order matters — must follow §2.2 table)", ve.Missing, want)
	}
}

// T5 — backward-compat substring "is required" preserved per spec §2.5.
func TestValidate_MissingFieldsErrorString_BackwardCompat(t *testing.T) {
	freshSink(t)
	tc := TaskContract{
		Version:        1,
		ConversationID: "conv-1",
		// goal missing
		DataContract: DataContract{
			ReadArtifacts: []ArtifactRef{},
			WriteTargets:  []WriteTarget{{Type: WriteTargetArtifact, Kind: "code", Name: "x.go"}},
		},
		CapabilityRequirements: CapabilityRequirements{Skills: []string{"chat"}},
		RecoveryHint:           "hint",
	}
	tc.ApplyDefaults()
	err := tc.Validate()
	if err == nil || !strings.Contains(err.Error(), "intent.goal is required") {
		t.Fatalf("Error() = %v; want substring \"intent.goal is required\" (legacy test compatibility)", err)
	}
}

// T6 — Go-literal contract with ReadArtifacts nil → fails.
func TestValidate_ReadArtifacts_NilFails(t *testing.T) {
	freshSink(t)
	tc := TaskContract{
		Version:        1,
		ConversationID: "conv-1",
		Intent: IntentSpec{
			Goal:            "do",
			SuccessCriteria: []string{"done"},
		},
		DataContract: DataContract{
			// ReadArtifacts nil
			WriteTargets: []WriteTarget{{Type: WriteTargetArtifact, Kind: "code", Name: "x.go"}},
		},
		CapabilityRequirements: CapabilityRequirements{Skills: []string{"chat"}},
		RecoveryHint:           "hint",
	}
	tc.ApplyDefaults()
	err := tc.Validate()
	if err == nil {
		t.Fatal("expected error for nil ReadArtifacts")
	}
	var ve *ValidationError
	if !errorAs(err, &ve) || !containsString(ve.Missing, "data_contract.read_artifacts") {
		t.Errorf("Missing must contain data_contract.read_artifacts; got %v", missing(err))
	}
}

// T7 — Go-literal with ReadArtifacts: []ArtifactRef{} passes.
func TestValidate_ReadArtifacts_EmptyExplicitPasses(t *testing.T) {
	freshSink(t)
	tc := TaskContract{
		Version:        1,
		ConversationID: "conv-1",
		Intent: IntentSpec{
			Goal:            "do",
			SuccessCriteria: []string{"done"},
		},
		DataContract: DataContract{
			ReadArtifacts: []ArtifactRef{}, // explicit empty
			WriteTargets:  []WriteTarget{{Type: WriteTargetArtifact, Kind: "code", Name: "x.go"}},
		},
		CapabilityRequirements: CapabilityRequirements{Skills: []string{"chat"}},
		RecoveryHint:           "hint",
	}
	tc.ApplyDefaults()
	if err := tc.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// T8 — JSON "read_artifacts": [] decodes to non-nil empty slice and passes.
func TestValidate_ReadArtifacts_JSONExplicitEmptyPasses(t *testing.T) {
	freshSink(t)
	raw := []byte(`{
		"version": 1,
		"conversation_id": "conv-1",
		"intent": {"goal": "do", "success_criteria": ["done"]},
		"data_contract": {
			"read_artifacts": [],
			"write_targets": [{"type":"artifact","kind":"code","name":"x.go"}]
		},
		"capability_requirements": {"skills":["chat"]},
		"recovery_hint": "hint"
	}`)
	var tc TaskContract
	if err := json.Unmarshal(raw, &tc); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	tc.ApplyDefaults()
	if err := tc.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// T9 — JSON missing the key decodes to nil and fails.
func TestValidate_ReadArtifacts_JSONMissingFails(t *testing.T) {
	freshSink(t)
	raw := []byte(`{
		"version": 1,
		"conversation_id": "conv-1",
		"intent": {"goal": "do", "success_criteria": ["done"]},
		"data_contract": {
			"write_targets": [{"type":"artifact","kind":"code","name":"x.go"}]
		},
		"capability_requirements": {"skills":["chat"]},
		"recovery_hint": "hint"
	}`)
	var tc TaskContract
	if err := json.Unmarshal(raw, &tc); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	tc.ApplyDefaults()
	err := tc.Validate()
	if err == nil {
		t.Fatal("expected error for missing read_artifacts key")
	}
	if !containsString(missing(err), "data_contract.read_artifacts") {
		t.Errorf("Missing must contain data_contract.read_artifacts; got %v", missing(err))
	}
}

// T10 — recovery_hint length cap. 4097 fails; 4096 passes.
func TestValidate_RecoveryHint_TooLong_Reject(t *testing.T) {
	freshSink(t)
	for _, tc := range []struct {
		name    string
		runes   int
		wantErr bool
	}{
		{"4096 OK", 4096, false},
		{"4097 reject", 4097, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := validContract()
			c.RecoveryHint = strings.Repeat("a", tc.runes)
			err := c.Validate()
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected too-long error")
				}
				if !errorIs(err, ErrRecoveryHintTooLong) {
					t.Errorf("errors.Is(err, ErrRecoveryHintTooLong) = false; err=%v", err)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}
		})
	}
}

// T11 — rune-count, not byte-count. 4096 4-byte runes pass even though
// the byte length is 16384.
func TestValidate_RecoveryHint_TooLong_UsesRuneCount(t *testing.T) {
	freshSink(t)
	c := validContract()
	c.RecoveryHint = strings.Repeat("𝟘", 4096) // each rune is 4 bytes in UTF-8
	if err := c.Validate(); err != nil {
		t.Fatalf("4096 multi-byte runes should pass; got: %v", err)
	}
}

// T12–T14 — control-char rejections.
func TestValidate_RecoveryHint_ControlChar_Reject(t *testing.T) {
	freshSink(t)
	for name, ch := range map[string]rune{
		"BEL (0x07)": '\x07',
		"ESC (0x1b)": '\x1b',
		"DEL (0x7f)": '\x7f',
	} {
		t.Run(name, func(t *testing.T) {
			c := validContract()
			c.RecoveryHint = "ok " + string(ch) + " bad"
			err := c.Validate()
			if err == nil {
				t.Fatalf("expected control-char rejection")
			}
			if !errorIs(err, ErrRecoveryHintContainsControlChar) {
				t.Errorf("errors.Is(err, ErrRecoveryHintContainsControlChar) = false; err=%v", err)
			}
		})
	}
}

// T15 — \t\n\r explicitly allowed.
func TestValidate_RecoveryHint_ControlChar_AllowsTabNewlineCR(t *testing.T) {
	freshSink(t)
	for name, ch := range map[string]rune{"tab": '\t', "newline": '\n', "cr": '\r'} {
		t.Run(name, func(t *testing.T) {
			c := validContract()
			c.RecoveryHint = "first" + string(ch) + "second"
			if err := c.Validate(); err != nil {
				t.Fatalf("%s should be allowed: %v", name, err)
			}
		})
	}
}

// T16 — each of the 8 HTML/script prefixes (case-insensitive).
func TestValidate_RecoveryHint_HTMLPrefix_Reject(t *testing.T) {
	freshSink(t)
	for _, prefix := range recoveryHintHTMLPrefixes {
		for _, sample := range []string{prefix, strings.ToUpper(prefix), strings.Title(prefix)} {
			t.Run(sample, func(t *testing.T) {
				c := validContract()
				c.RecoveryHint = "safe text then " + sample + " evil"
				err := c.Validate()
				if err == nil {
					t.Fatalf("expected HTML-prefix rejection for %q", sample)
				}
				if !errorIs(err, ErrRecoveryHintLooksLikeHTML) {
					t.Errorf("errors.Is(err, ErrRecoveryHintLooksLikeHTML) = false; err=%v", err)
				}
			})
		}
	}
}

// T17 — backticks are NOT an escape hatch (rule is strict).
func TestValidate_RecoveryHint_LooksLikeXSSInBackticks_StillRejected(t *testing.T) {
	freshSink(t)
	c := validContract()
	c.RecoveryHint = "see `<script>` for an example"
	err := c.Validate()
	if err == nil {
		t.Fatal("expected rejection — spec §2.4 (c) is a strict substring ban; backticks are not an escape hatch")
	}
	if !errorIs(err, ErrRecoveryHintLooksLikeHTML) {
		t.Errorf("expected ErrRecoveryHintLooksLikeHTML; got %v", err)
	}
}

// T18 — policy validation still fires (recovery_hint OK but max_dag_nodes negative).
func TestValidate_RecoveryHint_PolicyValidationStillFires(t *testing.T) {
	freshSink(t)
	c := validContract()
	c.RecoveryHint = "fine"
	c.ExecutionPolicy.MaxDAGNodes = Int(-1)
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "max_dag_nodes must be >= 1") {
		t.Fatalf("expected policy error; got %v", err)
	}
	// Policy errors are NOT routed through ValidationError per spec §2.5.
	var ve *ValidationError
	if errorAs(err, &ve) {
		t.Errorf("policy error must not be a *ValidationError; got %#v", ve)
	}
}

// T21 — EnforceContract applies defaults then validates.
func TestEnforceContract_AppliesDefaultsThenValidates(t *testing.T) {
	freshSink(t)
	c := TaskContract{
		Version:        1,
		ConversationID: "conv-1",
		Intent: IntentSpec{
			Goal:            "do",
			SuccessCriteria: []string{"done"},
		},
		DataContract: DataContract{
			ReadArtifacts: []ArtifactRef{},
			WriteTargets:  []WriteTarget{{Type: WriteTargetArtifact, Kind: "code", Name: "x.go"}},
		},
		CapabilityRequirements: CapabilityRequirements{Skills: []string{"chat"}},
		RecoveryHint:           "hint",
	}
	// ExecutionPolicy.Routing is empty — ApplyDefaults must fill before Validate.
	if c.ExecutionPolicy.Routing != "" {
		t.Fatalf("test precondition: Routing should start empty; got %q", c.ExecutionPolicy.Routing)
	}
	if err := EnforceContract(&c); err != nil {
		t.Fatalf("EnforceContract: %v", err)
	}
	if c.ExecutionPolicy.Routing != RoutingDirectFirst {
		t.Errorf("Routing after EnforceContract = %q, want %q", c.ExecutionPolicy.Routing, RoutingDirectFirst)
	}
}

// T22 — EnforceContract validates after defaults; missing goal still rejected.
func TestEnforceContract_ValidatesAfterDefaults(t *testing.T) {
	freshSink(t)
	c := TaskContract{
		Version:        1,
		ConversationID: "conv-1",
		// goal missing
		DataContract: DataContract{
			ReadArtifacts: []ArtifactRef{},
			WriteTargets:  []WriteTarget{{Type: WriteTargetArtifact, Kind: "code", Name: "x.go"}},
		},
		CapabilityRequirements: CapabilityRequirements{Skills: []string{"chat"}},
		RecoveryHint:           "hint",
	}
	err := EnforceContract(&c)
	if err == nil || !containsString(missing(err), "intent.goal") {
		t.Fatalf("expected missing-goal rejection; got %v", err)
	}
}

// T29 — completeness event payload contains ONLY bitmap + ratio + conv_id.
// Uses distinctive UUIDs in the contract body and asserts none appear in
// the marshalled event JSON. Spec §7 (d).
func TestContractCompleteness_OnlyBitmap_NoBody(t *testing.T) {
	spy := freshSink(t)
	const (
		goalUUID         = "550e8400-e29b-41d4-a716-446655440001"
		successUUID      = "550e8400-e29b-41d4-a716-446655440002"
		recoveryUUID     = "550e8400-e29b-41d4-a716-446655440003"
		writeTargetUUID  = "550e8400-e29b-41d4-a716-446655440004"
		readArtifactUUID = "550e8400-e29b-41d4-a716-446655440005"
	)
	c := TaskContract{
		Version:        1,
		ConversationID: "conv-uuid-test",
		Intent: IntentSpec{
			Goal:            goalUUID,
			SuccessCriteria: []string{successUUID},
		},
		DataContract: DataContract{
			ReadArtifacts: []ArtifactRef{{ArtifactID: readArtifactUUID}},
			WriteTargets:  []WriteTarget{{Type: WriteTargetArtifact, Kind: "code", Name: writeTargetUUID}},
		},
		CapabilityRequirements: CapabilityRequirements{Skills: []string{"chat"}},
		RecoveryHint:           recoveryUUID,
	}
	c.ApplyDefaults()
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	events := spy.snapshot()
	if len(events) != 1 {
		t.Fatalf("emitted events = %d", len(events))
	}
	out, _ := json.Marshal(events[0])
	for _, leak := range []string{goalUUID, successUUID, recoveryUUID, writeTargetUUID, readArtifactUUID} {
		if strings.Contains(string(out), leak) {
			t.Errorf("event JSON leaks contract body fragment %q; payload=%s", leak, string(out))
		}
	}
	// Positive: the conversation ID and field names ARE present.
	if !strings.Contains(string(out), "conv-uuid-test") {
		t.Errorf("expected conversation_id in event payload; payload=%s", string(out))
	}
}

// T30 — denominator regression pin (four assertions).
func TestContractCompleteness_RatioDenominator7(t *testing.T) {
	spy := freshSink(t)

	// (iii) constant pin — fails before any ratio math if denominator changes.
	if completenessDenominator != 7 {
		t.Fatalf("completenessDenominator = %d, want 7 — spec §1.1 / §2.6 says the denominator is fixed at 7", completenessDenominator)
	}

	// (i) + (ii) — full 7/7 fixture.
	c := TaskContract{
		Version:        1,
		ConversationID: "conv-1",
		Intent: IntentSpec{
			Goal:            "do",
			SuccessCriteria: []string{"done"},
		},
		DataContract: DataContract{
			ReadArtifacts: []ArtifactRef{},
			WriteTargets:  []WriteTarget{{Type: WriteTargetArtifact, Kind: "code", Name: "x.go"}},
		},
		CapabilityRequirements: CapabilityRequirements{Skills: []string{"chat"}},
		RecoveryHint:           "hint",
	}
	c.ApplyDefaults()
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	events := spy.snapshot()
	if len(events) != 1 {
		t.Fatalf("emitted events = %d", len(events))
	}
	if got, want := events[0].CompletenessRatio, 1.0; got != want {
		t.Errorf("(i) ratio = %v, want %v", got, want)
	}
	if got, want := len(events[0].PresentFields), 7; got != want {
		t.Errorf("(ii) len(PresentFields) = %d, want %d", got, want)
	}

	// (iv) — 4/7 fixture: goal + success + write_targets + execution_policy
	// present; no read_artifacts, no capability_requirements, no recovery_hint.
	// We use ablation to bypass enforce so Validate returns nil and the event
	// fires.
	withAblationFlag(t, &DisableSchemaEnforce, true)
	spy2 := freshSink(t)
	c2 := TaskContract{
		Version:        1,
		ConversationID: "conv-4-7",
		Intent: IntentSpec{
			Goal:            "do",
			SuccessCriteria: []string{"done"},
		},
		DataContract: DataContract{
			// ReadArtifacts nil
			WriteTargets: []WriteTarget{{Type: WriteTargetArtifact, Kind: "code", Name: "x.go"}},
		},
		// CapabilityRequirements zero
		// RecoveryHint empty
	}
	c2.ApplyDefaults() // populates execution_policy
	if err := c2.Validate(); err != nil {
		t.Fatalf("Validate (4/7): %v", err)
	}
	events2 := spy2.snapshot()
	if len(events2) != 1 {
		t.Fatalf("emitted events (4/7) = %d", len(events2))
	}
	want := 4.0 / 7.0
	diff := events2[0].CompletenessRatio - want
	if diff < 0 {
		diff = -diff
	}
	if diff > 1e-9 {
		t.Errorf("(iv) ratio = %v, want %v ± 1e-9 (fields=%v)", events2[0].CompletenessRatio, want, events2[0].PresentFields)
	}
}

// T31 — nil slice not in bitmap.
func TestPresentFields_NilSliceAbsent(t *testing.T) {
	c := validContract()
	c.DataContract.ReadArtifacts = nil
	got := presentFields(c)
	if containsString(got, lifecycleReadArtifacts) {
		t.Errorf("nil ReadArtifacts must NOT be in bitmap; got %v", got)
	}
}

// T32 — explicit empty slice IS in bitmap.
func TestPresentFields_EmptyExplicitSlicePresent(t *testing.T) {
	c := validContract()
	c.DataContract.ReadArtifacts = []ArtifactRef{}
	got := presentFields(c)
	if !containsString(got, lifecycleReadArtifacts) {
		t.Errorf("explicit empty ReadArtifacts must be in bitmap; got %v", got)
	}
}

// T33 — struct all-zero not in bitmap.
func TestPresentFields_StructAllZeroAbsent(t *testing.T) {
	c := validContract()
	c.CapabilityRequirements = CapabilityRequirements{}
	got := presentFields(c)
	if containsString(got, lifecycleCapabilityRequirements) {
		t.Errorf("zero CapabilityRequirements must NOT be in bitmap; got %v", got)
	}
}

// T34 — whitespace-only string not in bitmap.
func TestPresentFields_TrimmedStringAbsent(t *testing.T) {
	c := validContract()
	c.Intent.Goal = "  \t  "
	got := presentFields(c)
	if containsString(got, lifecycleIntentGoal) {
		t.Errorf("whitespace-only Goal must NOT be in bitmap; got %v", got)
	}
}

// TestPresentFields_WriteTargetsCarveOut pins the §2.6 carve-out for
// DataContract.WriteTargets: unlike ReadArtifacts (where non-nil empty
// IS a declaration of "no inputs"), an empty write_targets slice
// declares a task that produces nothing observable, which collectMissing
// rejects under schema-enforce. The bitmap mirrors that rejection.
//
// Regression guard: under DisableSchemaEnforce, a contract with
// WriteTargets=[]WriteTarget{} previously emitted a 7/7 completeness
// event (presentFields reported write_targets as "present" while
// collectMissing would have rejected it). The two signals must agree.
func TestPresentFields_WriteTargetsCarveOut(t *testing.T) {
	t.Run("empty slice is absent", func(t *testing.T) {
		c := validContract()
		c.DataContract.WriteTargets = []WriteTarget{}
		got := presentFields(c)
		if containsString(got, lifecycleWriteTargets) {
			t.Errorf("empty WriteTargets must NOT be in bitmap (productive-field carve-out); got %v", got)
		}
	})
	t.Run("nil slice is absent", func(t *testing.T) {
		c := validContract()
		c.DataContract.WriteTargets = nil
		got := presentFields(c)
		if containsString(got, lifecycleWriteTargets) {
			t.Errorf("nil WriteTargets must NOT be in bitmap; got %v", got)
		}
	})
	t.Run("one entry is present", func(t *testing.T) {
		c := validContract() // already has one entry
		got := presentFields(c)
		if !containsString(got, lifecycleWriteTargets) {
			t.Errorf("WriteTargets with one entry must be in bitmap; got %v", got)
		}
	})
}

// TestPresentFields_BitmapAgreesWithCollectMissingForWriteTargets pins
// the invariant that presentFields and collectMissing always agree on
// write_targets: a contract is rejected by collectMissing iff
// presentFields counts the field as absent. Without this pin, the two
// could drift again on a future refactor.
func TestPresentFields_BitmapAgreesWithCollectMissingForWriteTargets(t *testing.T) {
	cases := []struct {
		name         string
		writeTargets []WriteTarget
	}{
		{"nil", nil},
		{"empty", []WriteTarget{}},
		{"one entry", []WriteTarget{{Type: WriteTargetArtifact, Kind: "code", Name: "x.go"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validContract()
			c.DataContract.WriteTargets = tc.writeTargets
			missing := collectMissing(c)
			inBitmap := containsString(presentFields(c), lifecycleWriteTargets)
			rejected := containsString(missing, "data_contract.write_targets")
			if rejected == inBitmap {
				t.Errorf("collectMissing.rejected=%v but presentFields.inBitmap=%v — signals must agree (write_targets=%v)", rejected, inBitmap, tc.writeTargets)
			}
		})
	}
}

// TestPresentFields_SuccessCriteriaCarveOut pins the §2.6 carve-out for
// Intent.SuccessCriteria: unlike ReadArtifacts (where non-nil empty IS
// a declaration), an empty success_criteria is a meaningless oracle and
// counts as ABSENT. A future refactor that "unifies" the slice rule
// across all slice-typed fields would silently drift the completeness
// signal; this test fails loudly in that case.
func TestPresentFields_SuccessCriteriaCarveOut(t *testing.T) {
	t.Run("empty slice is absent", func(t *testing.T) {
		c := validContract()
		c.Intent.SuccessCriteria = []string{}
		got := presentFields(c)
		if containsString(got, lifecycleSuccessCriteria) {
			t.Errorf("empty SuccessCriteria must NOT be in bitmap (oracle carve-out); got %v", got)
		}
	})
	t.Run("whitespace-only entries are absent", func(t *testing.T) {
		c := validContract()
		c.Intent.SuccessCriteria = []string{"  ", "\t\n"}
		got := presentFields(c)
		if containsString(got, lifecycleSuccessCriteria) {
			t.Errorf("whitespace-only SuccessCriteria must NOT be in bitmap; got %v", got)
		}
	})
	t.Run("one real entry is present", func(t *testing.T) {
		c := validContract()
		c.Intent.SuccessCriteria = []string{"  ", "real criterion"}
		got := presentFields(c)
		if !containsString(got, lifecycleSuccessCriteria) {
			t.Errorf("SuccessCriteria with one real entry must be in bitmap; got %v", got)
		}
	})
}

// T35 — sink swap under concurrency is race-free AND visibility-correct.
//
// Split into two parts:
//
//  1. Concurrent fan-out — many goroutines hammer Validate while a
//     swap happens mid-run; `-race` catches torn reads/writes and
//     the total-events assertion catches a swap that drops events.
//  2. Deterministic visibility — explicit before/after sequencing to
//     prove the swap is observable: events emitted AFTER the swap
//     land in the new sink, events emitted BEFORE land in the old.
//
// Without part 2, a regression like "Swap stores the new sink but
// currentSink reads from a stale cache" would still pass the
// total-only assertion of the old version of this test.
func TestEventSink_SwapAtomic(t *testing.T) {
	t.Run("concurrent fan-out is race-free and lossless", func(t *testing.T) {
		old := RegisterCompletenessSink(nil)
		t.Cleanup(func() { RegisterCompletenessSink(old) })

		spyA := &spySink{}
		spyB := &spySink{}
		RegisterCompletenessSink(spyA)

		const N = 100
		var wg sync.WaitGroup
		wg.Add(N + 1)
		c := validContract()
		go func() {
			defer wg.Done()
			RegisterCompletenessSink(spyB)
		}()
		for i := 0; i < N; i++ {
			go func() {
				defer wg.Done()
				_ = c.Validate()
			}()
		}
		wg.Wait()

		total := len(spyA.snapshot()) + len(spyB.snapshot())
		if total != N {
			t.Errorf("total events across both spies = %d, want %d", total, N)
		}
	})

	t.Run("swap visibility is sequenced — before-swap events land in old sink, after-swap in new", func(t *testing.T) {
		old := RegisterCompletenessSink(nil)
		t.Cleanup(func() { RegisterCompletenessSink(old) })

		spyA := &spySink{}
		spyB := &spySink{}
		c := validContract()

		// Emit one event before the swap.
		RegisterCompletenessSink(spyA)
		if err := c.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
		// Swap. Returned value is the prior sink.
		prior := RegisterCompletenessSink(spyB)
		if prior != spyA {
			t.Errorf("RegisterCompletenessSink returned prior=%v, want spyA=%v", prior, spyA)
		}
		// Emit one event after the swap.
		if err := c.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}

		if got := len(spyA.snapshot()); got != 1 {
			t.Errorf("spyA received %d events, want 1 (event emitted before swap must land in spyA)", got)
		}
		if got := len(spyB.snapshot()); got != 1 {
			t.Errorf("spyB received %d events, want 1 (event emitted after swap must land in spyB)", got)
		}
	})
}

// T36 — nil sink is a no-op (no panic).
func TestEventSink_NilNoOp(t *testing.T) {
	old := RegisterCompletenessSink(nil)
	t.Cleanup(func() { RegisterCompletenessSink(old) })
	c := validContract()
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate with nil sink: %v", err)
	}
}

// --- helpers ---

// validContract returns a fully-defaulted, schema-passing contract that
// individual tests then mutate one field at a time.
func validContract() TaskContract {
	c := TaskContract{
		Version:        1,
		ConversationID: "conv-1",
		Intent: IntentSpec{
			Goal:            "do",
			SuccessCriteria: []string{"done"},
		},
		DataContract: DataContract{
			ReadArtifacts: []ArtifactRef{},
			WriteTargets:  []WriteTarget{{Type: WriteTargetArtifact, Kind: "code", Name: "x.go"}},
		},
		CapabilityRequirements: CapabilityRequirements{Skills: []string{"chat"}},
		RecoveryHint:           "hint",
	}
	c.ApplyDefaults()
	return c
}

func errorIs(err, target error) bool { return errors.Is(err, target) }

func errorAs(err error, target **ValidationError) bool { return errors.As(err, target) }

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// missing returns the ValidationError.Missing slice if err is one, else nil.
func missing(err error) []string {
	var ve *ValidationError
	if errors.As(err, &ve) && ve != nil {
		return ve.Missing
	}
	return nil
}
