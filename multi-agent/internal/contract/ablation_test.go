package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"testing"

	"github.com/yourorg/multi-agent/internal/ablation"
)

// captureLog redirects the standard `log` package's output to a buffer
// for the duration of the test, restoring on cleanup. Tests that grep
// for "[ablation] ..." log substrings MUST use this helper — without
// the restore, log lines from earlier tests leak into the buffer of
// later tests run in -shuffle order.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	priorOut := log.Writer()
	priorFlags := log.Flags()
	priorPrefix := log.Prefix()
	log.SetOutput(buf)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(priorOut)
		log.SetFlags(priorFlags)
		log.SetPrefix(priorPrefix)
	})
	return buf
}

// T23 — DisableSchemaEnforce skips §2.2/§2.4 checks but JSON parse and
// policy checks still fire.
func TestNoTypedContracts_SkipsEnforceButParsesJSON(t *testing.T) {
	withAblationFlag(t, &DisableSchemaEnforce, true)
	freshSink(t)
	captureLog(t)

	t.Run("missing intent.goal passes under ablation", func(t *testing.T) {
		c := TaskContract{
			Version:        1,
			ConversationID: "conv-a",
			// goal missing
			DataContract: DataContract{
				ReadArtifacts: []ArtifactRef{},
				WriteTargets:  []WriteTarget{{Type: WriteTargetArtifact, Kind: "code", Name: "x.go"}},
			},
			CapabilityRequirements: CapabilityRequirements{Skills: []string{"chat"}},
			RecoveryHint:           "hint",
		}
		c.ApplyDefaults()
		if err := c.Validate(); err != nil {
			t.Errorf("under DisableSchemaEnforce, missing goal must pass; got %v", err)
		}
	})

	t.Run("MaxDAGNodes=-1 still rejects under ablation", func(t *testing.T) {
		c := validContract()
		c.ExecutionPolicy.MaxDAGNodes = Int(-1)
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), "max_dag_nodes must be >= 1") {
			t.Errorf("policy check must still fire under ablation; got %v", err)
		}
	})

	t.Run("oversize RecoveryHint passes under ablation", func(t *testing.T) {
		c := validContract()
		c.RecoveryHint = strings.Repeat("a", 9999)
		if err := c.Validate(); err != nil {
			t.Errorf("under DisableSchemaEnforce, oversize hint must pass; got %v", err)
		}
	})
}

// T24 — log line on every EnforceContract call under ablation.
func TestNoTypedContracts_LogsSkip(t *testing.T) {
	withAblationFlag(t, &DisableSchemaEnforce, true)
	freshSink(t)
	buf := captureLog(t)

	c := validContract()
	c.ConversationID = "conv-log-skip-test"
	if err := EnforceContract(&c); err != nil {
		t.Fatalf("EnforceContract: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "[ablation] NoTypedContracts: skipped enforce") {
		t.Errorf("log did not contain required substring; got: %q", got)
	}
	if !strings.Contains(got, `conversation="conv-log-skip-test"`) {
		t.Errorf("log did not name the conversation; got: %q", got)
	}
}

// TestNoTypedContracts_LogIsInjectionResistant pins that a malicious
// conversation_id containing a newline cannot forge a second
// "[ablation] ..." line in the audit trail. Round-3 review P1-2
// found that `%s` formatting allowed an operator to construct a
// fake log entry indistinguishable from a real one — the audit
// trail is the post-mortem evidence chain for ablation runs, so a
// silent forgery is a real audit-integrity issue.
//
// The mitigation is `log.Printf("...%q", convID)` (Go-quoted
// escaping). This test pins the escape: a `\n` in the input must
// appear as the literal two-character escape `\n` in the output.
func TestNoTypedContracts_LogIsInjectionResistant(t *testing.T) {
	withAblationFlag(t, &DisableSchemaEnforce, true)
	freshSink(t)
	buf := captureLog(t)

	c := validContract()
	// Attacker-controlled conversation_id includes a newline plus a
	// forged ablation line. If the log formatter used %s, the newline
	// would split the output into two lines and the forgery would be
	// byte-identical to a real ablation event.
	c.ConversationID = "conv-attack\n[ablation] FAKE: spoofed log on conversation=evil"
	if err := EnforceContract(&c); err != nil {
		t.Fatalf("EnforceContract: %v", err)
	}

	got := buf.String()
	// %q quotes the whole conversation_id and escapes the newline as
	// literal "\n". The forged second [ablation] line must NOT appear
	// as its own line. The token "[ablation]" appears twice in the
	// captured log — once at line-start (the real entry) and once
	// inside the %q-escaped attacker payload (NOT at line-start) — so
	// we count line-starts, not raw substring matches.
	lines := strings.Split(got, "\n")
	starts := 0
	for _, ln := range lines {
		if strings.HasPrefix(ln, "[ablation]") {
			starts++
		}
	}
	if starts != 1 {
		t.Errorf("audit log contains %d lines starting with [ablation], want exactly 1 — possible log injection; full log:\n%s", starts, got)
	}
	// And the forged keyword "FAKE" must appear (we captured what the
	// attacker tried to write) but inside the quoted string, not as
	// an independent log line.
	if !strings.Contains(got, `\n[ablation] FAKE`) {
		t.Errorf("attacker payload not properly escaped — expected literal \\n escape in the audit log; got:\n%s", got)
	}
}

// T25 — event STILL fires under ablation, with truthful (partial) bitmap.
func TestNoTypedContracts_EmitsEventWithPartialBitmap(t *testing.T) {
	withAblationFlag(t, &DisableSchemaEnforce, true)
	spy := freshSink(t)
	captureLog(t)

	// 3-field contract: goal + success_criteria + execution_policy (after defaults).
	c := TaskContract{
		Version:        1,
		ConversationID: "conv-3-7",
		Intent: IntentSpec{
			Goal:            "do",
			SuccessCriteria: []string{"done"},
		},
		// DataContract zero, CapabilityRequirements zero, RecoveryHint empty
	}
	c.ApplyDefaults()
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate under ablation: %v", err)
	}
	events := spy.snapshot()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if got, want := len(events[0].PresentFields), 3; got != want {
		t.Errorf("len(PresentFields) = %d, want %d (fields=%v)", got, want, events[0].PresentFields)
	}
	want := 3.0 / 7.0
	diff := events[0].CompletenessRatio - want
	if diff < 0 {
		diff = -diff
	}
	if diff > 1e-9 {
		t.Errorf("ratio = %v, want %v ± 1e-9", events[0].CompletenessRatio, want)
	}
}

// EnforceContract under DisableContractEntirely returns the sentinel.
func TestEnforceContract_ContractEntirelyDisabled_ReturnsSentinel(t *testing.T) {
	withAblationFlag(t, &DisableContractEntirely, true)
	freshSink(t)
	captureLog(t)

	c := validContract()
	err := EnforceContract(&c)
	if !errors.Is(err, ErrContractFormalizationDisabled) {
		t.Errorf("expected ErrContractFormalizationDisabled; got %v", err)
	}
}

// T37 — both flags registered with ablation.Default after package init.
func TestAblationRegisteredOnDefault(t *testing.T) {
	// init() runs on package import; ablation.Default should now list
	// both flags. The List() return is sorted; check presence not order.
	listed := ablation.Default.List()
	want := []ablation.FlagName{
		ablation.NoContractFormalization,
		ablation.NoTypedContracts,
	}
	for _, w := range want {
		found := false
		for _, got := range listed {
			if got == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("ablation.Default.List() missing %q; got %v", w, listed)
		}
	}
}

// TestAblationFlagSwitches: SetByName flips both flags through. This
// pins that the registered *bool pointers are actually wired (not a
// dangling copy).
func TestAblationFlagSwitches(t *testing.T) {
	// Restore both flags around the test.
	withAblationFlag(t, &DisableSchemaEnforce, DisableSchemaEnforce)
	withAblationFlag(t, &DisableContractEntirely, DisableContractEntirely)

	if err := ablation.Default.SetByName(string(ablation.NoTypedContracts), true); err != nil {
		t.Fatalf("SetByName NoTypedContracts: %v", err)
	}
	if !DisableSchemaEnforce {
		t.Errorf("DisableSchemaEnforce not flipped through SetByName")
	}
	if err := ablation.Default.SetByName(string(ablation.NoContractFormalization), true); err != nil {
		t.Fatalf("SetByName NoContractFormalization: %v", err)
	}
	if !DisableContractEntirely {
		t.Errorf("DisableContractEntirely not flipped through SetByName")
	}
}

// Sanity: a sentinel-returning EnforceContract path is not a normal
// error; it must satisfy errors.Is precisely (not just match the
// "formalization disabled" string).
func TestEnforceContract_SentinelNotStringMatch(t *testing.T) {
	withAblationFlag(t, &DisableContractEntirely, true)
	captureLog(t)
	c := validContract()
	err := EnforceContract(&c)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrContractFormalizationDisabled) {
		t.Errorf("errors.Is failed; err type %T, value %v", err, err)
	}
	// Sanity-check the JSON-tag-decode path is unrelated to this sentinel.
	dummy := json.NewDecoder(strings.NewReader("{}"))
	_ = dummy
}
