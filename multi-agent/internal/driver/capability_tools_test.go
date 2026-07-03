package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"sync"
	"testing"

	"github.com/agentserver/agentserver/pkg/agentsdk"

	"github.com/yourorg/multi-agent/internal/capability"
	"github.com/yourorg/multi-agent/internal/commandiface"
	"github.com/yourorg/multi-agent/internal/contract"
	"github.com/yourorg/multi-agent/internal/contract/validator"
	"github.com/yourorg/multi-agent/internal/observer"
	"github.com/yourorg/multi-agent/internal/observerstore"
)

// testEventSink captures every emitted observer.Event.
type testEventSink struct {
	mu     sync.Mutex
	events []observer.Event
}

func (s *testEventSink) Emit(ev observer.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
}

func (s *testEventSink) Events() []observer.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]observer.Event, len(s.events))
	copy(out, s.events)
	return out
}

// newTestToolsWithSink returns Tools wired to a capturing sink, using
// the existing newTestToolsWithObserver helper (tools_test.go:84) and
// a fresh fakeSDK with a no-op DiscoverAgents (the tool calls it
// early in Call; nil discoverFunc would panic).
func newTestToolsWithSink(t *testing.T) (*Tools, *testEventSink) {
	t.Helper()
	sink := &testEventSink{}
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) { return nil, nil },
	}
	tools := newTestToolsWithObserver(t, sdk, sink)
	return tools, sink
}

// makeMinimalValidContract returns a contract that passes .Validate().
func makeMinimalValidContract(t *testing.T) contract.TaskContract {
	t.Helper()
	tc := contract.TaskContract{
		Version:        contract.Version,
		ConversationID: "conv-minimal-1",
		Intent:         contract.IntentSpec{Goal: "x", SuccessCriteria: []string{"ok"}},
		DataContract: contract.DataContract{
			ReadArtifacts: []contract.ArtifactRef{},
			WriteTargets:  []contract.WriteTarget{{Type: contract.WriteTargetArtifact, Kind: "document", Name: "out.md"}},
		},
		RecoveryHint:    "read nothing; retry idempotent",
		ExecutionPolicy: contract.ExecutionPolicy{Routing: contract.RoutingMasterOnly},
		CapabilityRequirements: contract.CapabilityRequirements{
			Skills: []string{},
			Tools:  []string{},
		},
	}
	tc.ApplyDefaults()
	return tc
}

// makeAllFourViolationsContract fires one block per class against the
// snapshot in TestDryRunContractTool_MixedRun_NumeratorsAllOne.
func makeAllFourViolationsContract(t *testing.T) contract.TaskContract {
	t.Helper()
	tc := makeMinimalValidContract(t)
	tc.DataContract.ReadArtifacts = []contract.ArtifactRef{{Kind: "file", Name: "missing.yaml"}}
	tc.CapabilityRequirements.ToolRequirements = []contract.ToolRequirement{{Name: "go", MinVersion: "1.22.0"}}
	tc.CapabilityRequirements.ForbiddenAliases = []string{"openai_for_glm"}
	tc.ExecutionPolicy.RequiredReach = capability.NetworkInternet
	return tc
}

// makeSnapshot returns a validated Snapshot from the given tune.
func makeSnapshot(t *testing.T, reach capability.NetworkReach, tools []capability.ToolVersion, creds []capability.CredentialAlias) capability.Snapshot {
	t.Helper()
	spec := capability.Snapshot{
		OS: "linux", Arch: "amd64",
		Platform:    commandiface.Platform{OS: "linux", Arch: "amd64"},
		Network:     reach,
		Tools:       tools,
		Credentials: creds,
	}
	snap, err := capability.NewSnapshot(spec)
	if err != nil {
		t.Fatalf("NewSnapshot: %v", err)
	}
	return snap
}

// ---------------------------------------------------------------------
// Task 12 — snapshot in → blocks out
// ---------------------------------------------------------------------

func TestDryRunContractTool_SnapshotIn_BlocksOut(t *testing.T) {
	tools, _ := newTestToolsWithSink(t)
	tc := makeMinimalValidContract(t)
	tc.DataContract.ReadArtifacts = []contract.ArtifactRef{{Kind: "file", Name: "missing.yaml"}}
	snap := makeSnapshot(t, capability.NetworkInternet, nil, nil)
	args, err := json.Marshal(map[string]interface{}{"contract": tc, "capability_snapshot": snap})
	if err != nil {
		t.Fatal(err)
	}
	out, err := (&dryRunContractTool{t: tools}).Call(context.Background(), args)
	if err != nil {
		t.Fatalf("dry_run_contract Call: %v", err)
	}
	var report dryRunReport
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if len(report.Blocks) == 0 {
		t.Fatalf("expected at least 1 block; got %+v", report)
	}
	found := false
	for _, b := range report.Blocks {
		if b.Kind == validator.KindMissingFile {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a missing_file block; got %+v", report.Blocks)
	}
	if report.AttemptID == "" {
		t.Errorf("AttemptID must be non-empty")
	}
	if report.Runnable {
		t.Errorf("Runnable must be false when Blocks is non-empty")
	}
}

// ---------------------------------------------------------------------
// Task 13 — 3 events + persistence + ablation
// ---------------------------------------------------------------------

func TestDryRunContractTool_EmitsThreeMetricEvents(t *testing.T) {
	tools, sink := newTestToolsWithSink(t)
	// Clean dry-run (no blocks). All three events must still fire with
	// numerator=0.
	tc := makeMinimalValidContract(t)
	snap := makeSnapshot(t, capability.NetworkInternet, nil, nil)
	args, _ := json.Marshal(map[string]interface{}{"contract": tc, "capability_snapshot": snap})
	if _, err := (&dryRunContractTool{t: tools}).Call(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	var attemptID string
	for _, ev := range sink.Events() {
		switch ev.Type {
		case "PreExecutionFaultCatchRate", "MissingArtifactDetectionRate", "PolicyViolationPreventionRate":
			counts[ev.Type]++
			var p struct {
				AttemptID string `json:"attempt_id"`
				Numerator int    `json:"numerator"`
			}
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				t.Fatal(err)
			}
			if attemptID == "" {
				attemptID = p.AttemptID
			} else if p.AttemptID != attemptID {
				t.Errorf("attempt_id mismatch across metric events: %q vs %q", p.AttemptID, attemptID)
			}
			if p.Numerator != 0 {
				t.Errorf("clean run event %s numerator: got %d want 0", ev.Type, p.Numerator)
			}
		}
	}
	if got, want := counts["PreExecutionFaultCatchRate"], 1; got != want {
		t.Errorf("PreExecutionFaultCatchRate count: got %d want %d", got, want)
	}
	if got, want := counts["MissingArtifactDetectionRate"], 1; got != want {
		t.Errorf("MissingArtifactDetectionRate count: got %d want %d", got, want)
	}
	if got, want := counts["PolicyViolationPreventionRate"], 1; got != want {
		t.Errorf("PolicyViolationPreventionRate count: got %d want %d", got, want)
	}
}

// Mixed run: one block from EACH class → three numerator=1 events AND
// exactly 3 metric events total (no dupes, no drops).
func TestDryRunContractTool_MixedRun_NumeratorsAllOne(t *testing.T) {
	tools, sink := newTestToolsWithSink(t)
	tc := makeAllFourViolationsContract(t)
	snap := makeSnapshot(t, capability.NetworkIntranet,
		[]capability.ToolVersion{{Name: "go", Version: "1.18.4"}},
		[]capability.CredentialAlias{"openai_for_glm"})
	args, _ := json.Marshal(map[string]interface{}{"contract": tc, "capability_snapshot": snap})
	if _, err := (&dryRunContractTool{t: tools}).Call(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	numerators := map[string]int{}
	counts := map[string]int{}
	payloadKeys := map[string]string{
		"PreExecutionFaultCatchRate":    "blocks_total",
		"MissingArtifactDetectionRate":  "missing_files",
		"PolicyViolationPreventionRate": "policy_violations",
	}
	for _, ev := range sink.Events() {
		switch ev.Type {
		case "PreExecutionFaultCatchRate", "MissingArtifactDetectionRate", "PolicyViolationPreventionRate":
			counts[ev.Type]++
			var p map[string]interface{}
			_ = json.Unmarshal(ev.Payload, &p)
			num, _ := p["numerator"].(float64)
			numerators[ev.Type] = int(num)
			if k := payloadKeys[ev.Type]; k != "" {
				if _, ok := p[k]; !ok {
					t.Errorf("event %s missing required payload key %q; got %v", ev.Type, k, p)
				}
			}
		}
	}
	if got, want := counts["PreExecutionFaultCatchRate"], 1; got != want {
		t.Errorf("PreExecutionFaultCatchRate count: got %d want %d", got, want)
	}
	if got, want := counts["MissingArtifactDetectionRate"], 1; got != want {
		t.Errorf("MissingArtifactDetectionRate count: got %d want %d", got, want)
	}
	if got, want := counts["PolicyViolationPreventionRate"], 1; got != want {
		t.Errorf("PolicyViolationPreventionRate count: got %d want %d", got, want)
	}
	total := counts["PreExecutionFaultCatchRate"] + counts["MissingArtifactDetectionRate"] + counts["PolicyViolationPreventionRate"]
	if total != 3 {
		t.Errorf("total metric events: got %d want 3", total)
	}
	if numerators["PreExecutionFaultCatchRate"] != 1 {
		t.Errorf("PreExecutionFaultCatchRate numerator: got %d want 1", numerators["PreExecutionFaultCatchRate"])
	}
	if numerators["MissingArtifactDetectionRate"] != 1 {
		t.Errorf("MissingArtifactDetectionRate numerator: got %d want 1", numerators["MissingArtifactDetectionRate"])
	}
	if numerators["PolicyViolationPreventionRate"] != 1 {
		t.Errorf("PolicyViolationPreventionRate numerator: got %d want 1", numerators["PolicyViolationPreventionRate"])
	}
}

// §7(d) ablation bypass: NoDryRun on → zero events, zero blocks in
// dryRunReport, zero rows in dry_run_blocks, exactly one log line.
func TestDryRunContractTool_NoDryRunAblation_ShortCircuits(t *testing.T) {
	tools, sink := newTestToolsWithSink(t)

	// Inject a real SQLite-backed writer so we can assert row count.
	store, err := observerstore.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	tools.dryRunWriter = observerstore.NewDryRunBlockWriter(store.DB())

	prev := validator.IsDryRunDisabled()
	t.Cleanup(func() { validator.SetDryRunDisabled(prev) })
	validator.SetDryRunDisabled(true)

	var logBuf bytes.Buffer
	origOut := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(origOut) })

	tc := makeAllFourViolationsContract(t)
	tc.ConversationID = "conv-ablation-42"
	snap := makeSnapshot(t, capability.NetworkIntranet,
		[]capability.ToolVersion{{Name: "go", Version: "1.18.4"}},
		[]capability.CredentialAlias{"openai_for_glm"})
	args, _ := json.Marshal(map[string]interface{}{"contract": tc, "capability_snapshot": snap})
	out, err := (&dryRunContractTool{t: tools}).Call(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	var report dryRunReport
	_ = json.Unmarshal(out, &report)
	if len(report.Blocks) != 0 {
		t.Errorf("ablation-on: expected 0 blocks; got %d", len(report.Blocks))
	}
	for _, ev := range sink.Events() {
		switch ev.Type {
		case "PreExecutionFaultCatchRate", "MissingArtifactDetectionRate", "PolicyViolationPreventionRate":
			t.Errorf("ablation-on: unexpected metric event %s", ev.Type)
		}
	}
	var n int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM dry_run_blocks`).Scan(&n); err != nil {
		t.Fatalf("count dry_run_blocks: %v", err)
	}
	if n != 0 {
		t.Errorf("ablation-on: expected 0 dry_run_blocks rows; got %d", n)
	}
	logText := logBuf.String()
	// %q quoting: conv-ablation-42 → "conv-ablation-42" (with quotes).
	want := `[ablation] NoDryRun: skipped conversation="conv-ablation-42"`
	if !strings.Contains(logText, want) {
		t.Errorf("missing ablation log line %q; got:\n%s", want, logText)
	}
	if strings.Count(logText, want) != 1 {
		t.Errorf("expected 1 log line matching %q; got %d in:\n%s", want, strings.Count(logText, want), logText)
	}
}

// §7(d) log-injection defense: an attacker-controlled conversation_id
// carrying a newline + spoofed ablation prefix MUST NOT split the log
// line. %q Go-quoting escapes \n / \r / control chars so the forged
// second line cannot appear. Mirrors
// contract/ablation_test.go:TestNoTypedContracts_LogIsInjectionResistant
// pattern.
func TestDryRunContractTool_NoDryRunLogIsInjectionResistant(t *testing.T) {
	tools, _ := newTestToolsWithSink(t)
	prev := validator.IsDryRunDisabled()
	t.Cleanup(func() { validator.SetDryRunDisabled(prev) })
	validator.SetDryRunDisabled(true)

	var logBuf bytes.Buffer
	origOut := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(origOut) })

	tc := makeMinimalValidContract(t)
	// conversation_id containing an injected newline + spoofed
	// [ablation] prefix. If the tool used %s, this would produce a
	// second real "[ablation] FAKE: ..." line in the audit trail.
	tc.ConversationID = "conv-x\n[ablation] FAKE: spoofed conversation=evil"
	snap := makeSnapshot(t, capability.NetworkInternet, nil, nil)
	args, _ := json.Marshal(map[string]interface{}{"contract": tc, "capability_snapshot": snap})
	// If the contract's Validate rejects newline-carrying conversation_id,
	// this test still confirms the log path is safe when the call
	// reaches it — many contract-side sanitizers exist independently.
	_, err := (&dryRunContractTool{t: tools}).Call(context.Background(), args)
	if err != nil {
		// Contract validation rejected the crafted id. That's a
		// stronger defence — but we still assert the log path
		// doesn't emit a forged line.
	}

	logText := logBuf.String()
	// The word "FAKE" must NOT appear at column 0 on any line — that
	// would prove the newline was interpreted rather than escaped.
	for _, line := range strings.Split(logText, "\n") {
		if strings.HasPrefix(line, "[ablation] FAKE:") {
			t.Errorf("log injection succeeded: forged line %q at column 0", line)
		}
	}
}

// §4.3 "persist one row per block" — with the ablation flag OFF, a
// 4-block mixed dry-run must land 4 rows in dry_run_blocks with the
// right kinds, attempt_id, contract_hash, and capability_snapshot_hash.
func TestDryRunContractTool_PersistsOneRowPerBlock(t *testing.T) {
	tools, _ := newTestToolsWithSink(t)
	store, err := observerstore.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	tools.dryRunWriter = observerstore.NewDryRunBlockWriter(store.DB())

	tc := makeAllFourViolationsContract(t)
	tc.ConversationID = "conv-persist-1"
	snap := makeSnapshot(t, capability.NetworkIntranet,
		[]capability.ToolVersion{{Name: "go", Version: "1.18.4"}},
		[]capability.CredentialAlias{"openai_for_glm"})
	args, _ := json.Marshal(map[string]interface{}{"contract": tc, "capability_snapshot": snap})
	out, err := (&dryRunContractTool{t: tools}).Call(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	var report dryRunReport
	_ = json.Unmarshal(out, &report)

	var n int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM dry_run_blocks WHERE attempt_id=?`, report.AttemptID).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if n != 4 {
		t.Errorf("expected 4 dry_run_blocks rows for attempt %s; got %d", report.AttemptID, n)
	}

	rows, err := store.DB().Query(`SELECT block_kind FROM dry_run_blocks WHERE attempt_id=? ORDER BY block_kind`, report.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		got[k] = true
	}
	for _, want := range []string{"missing_file", "wrong_version", "forbidden_cred", "policy_violation"} {
		if !got[want] {
			t.Errorf("missing persisted block kind %q; got %v", want, got)
		}
	}
}

// ---------------------------------------------------------------------
// Task 14 — backward-compat + writer failure
// ---------------------------------------------------------------------

// §4.1 backward compat: contract-only args (no capability_snapshot) →
// tool succeeds, Blocks empty, recommended_route present. EXACTLY 3
// events fire with numerator=0.
func TestDryRunContractTool_BackwardCompat_NoSnapshot(t *testing.T) {
	tools, sink := newTestToolsWithSink(t)
	tc := makeMinimalValidContract(t)
	args, _ := json.Marshal(map[string]interface{}{"contract": tc})
	out, err := (&dryRunContractTool{t: tools}).Call(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	var report dryRunReport
	_ = json.Unmarshal(out, &report)
	if len(report.Blocks) != 0 {
		t.Errorf("no snapshot: expected 0 blocks; got %d", len(report.Blocks))
	}
	counts := map[string]int{}
	for _, ev := range sink.Events() {
		switch ev.Type {
		case "PreExecutionFaultCatchRate", "MissingArtifactDetectionRate", "PolicyViolationPreventionRate":
			counts[ev.Type]++
			var p struct {
				Numerator int `json:"numerator"`
			}
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				t.Fatal(err)
			}
			if p.Numerator != 0 {
				t.Errorf("no-snapshot %s numerator: got %d want 0", ev.Type, p.Numerator)
			}
		}
	}
	if counts["PreExecutionFaultCatchRate"] != 1 {
		t.Errorf("PreExecutionFaultCatchRate count: got %d want 1", counts["PreExecutionFaultCatchRate"])
	}
	if counts["MissingArtifactDetectionRate"] != 1 {
		t.Errorf("MissingArtifactDetectionRate count: got %d want 1", counts["MissingArtifactDetectionRate"])
	}
	if counts["PolicyViolationPreventionRate"] != 1 {
		t.Errorf("PolicyViolationPreventionRate count: got %d want 1", counts["PolicyViolationPreventionRate"])
	}
	if report.RecommendedRoute == "" {
		t.Error("backward-compat: recommended_route missing")
	}
}

// A dry_run_blocks writer failure must NOT fail the tool call.
func TestDryRunContractTool_WriterFailure_DoesNotFailCall(t *testing.T) {
	tools, _ := newTestToolsWithSink(t)
	tools.dryRunWriter = failingDryRunWriter{}
	tc := makeAllFourViolationsContract(t)
	snap := makeSnapshot(t, capability.NetworkIntranet,
		[]capability.ToolVersion{{Name: "go", Version: "1.18.4"}},
		[]capability.CredentialAlias{"openai_for_glm"})
	args, _ := json.Marshal(map[string]interface{}{"contract": tc, "capability_snapshot": snap})
	out, err := (&dryRunContractTool{t: tools}).Call(context.Background(), args)
	if err != nil {
		t.Fatalf("call failed on writer error: %v", err)
	}
	var report dryRunReport
	_ = json.Unmarshal(out, &report)
	if len(report.Blocks) == 0 {
		t.Error("blocks should still be present in report even when writer failed")
	}
}

type failingDryRunWriter struct{}

func (failingDryRunWriter) WriteDryRunBlock(context.Context, observerstore.DryRunBlockRow) error {
	return errors.New("simulated writer failure")
}

// TestExtractExperimentID_BoundaryMatching — round-5 fresh review P2:
// marker MUST be at start-of-string or preceded by whitespace /
// punctuation. A benign "my_experiment_id=..." must NOT contaminate
// the per-experiment denominator.
func TestExtractExperimentID_BoundaryMatching(t *testing.T) {
	cases := []struct {
		ctx  string
		want string
	}{
		// Legitimate markers.
		{"experiment_id=exp-alpha", "exp-alpha"},
		{"note: experiment_id=exp-alpha", "exp-alpha"},
		{"tag,experiment_id=exp-alpha", "exp-alpha"},
		{"line1\nexperiment_id=exp-alpha", "exp-alpha"},
		{"foo. experiment_id=exp-alpha rest", "exp-alpha"},
		// Partial-word bypasses — must NOT match.
		{"my_experiment_id=leak", ""},
		{"badexperiment_id=leak", ""},
		{"prefixed_experiment_id=leak", ""},
		// No marker.
		{"", ""},
		{"nothing here", ""},
	}
	for _, tc := range cases {
		t.Run(tc.ctx, func(t *testing.T) {
			got := extractExperimentID(contract.TaskContract{
				Intent: contract.IntentSpec{BusinessContext: tc.ctx},
			})
			if got != tc.want {
				t.Errorf("extractExperimentID(%q) = %q, want %q", tc.ctx, got, tc.want)
			}
		})
	}
}
