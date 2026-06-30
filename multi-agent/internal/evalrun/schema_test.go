package evalrun

import (
	"bytes"
	"context"
	"errors"
	"log"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yourorg/multi-agent/internal/ablation"
)

// sampleSchema returns a Schema with every field filled to a value the
// spec §2.1 lists as the canonical fixture for that field. The two
// times are deliberately set to round-trippable RFC3339Nano-compatible
// UTC values.
func sampleSchema() Schema {
	start := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	end := start.Add(7 * time.Minute)
	return Schema{
		RunID:                  "run-2026-06-30T12-00-00-E1-wl01-full",
		WorkloadID:             "wl-credential-bound-model",
		ClaimID:                "C1",
		ExperimentID:           "E1",
		BaselineOrAblation:     "full",
		LoomCommit:             "17f2c3cdec8c891c96ea155351600eb76292b269",
		AgentserverCommit:      "0000000000000000000000000000000000000001",
		ModelserverCommit:      "0000000000000000000000000000000000000002",
		AppCommit:              "0000000000000000000000000000000000000003",
		MachineTopology:        "linux/amd64 1xdriver 3xslave",
		ContextGroundTruth:     "(driver,inspect_capabilities)",
		CapabilitySnapshotHash: "",
		TaskContractHash:       "",
		DynamicMCPRegistryHash: "",
		SelectedContext:        "(driver,select_default)",
		GroundTruthContext:     "(driver,select_default)",
		StartTime:              start,
		EndTime:                end,
		SuccessOracleResult:    "pass",
		FailureCategory:        "",
		HumanInterventionCount: 0,
		ArtifactHashes: []string{
			"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
		ObserverTracePath: "runs/run-2026-06-30T12-00-00-E1-wl01-full/observer/trace.jsonl",
		ModelTraceID:      "modeltrace-001",
	}
}

// expectedColumnNames is the canonical column ordering, used by tests
// 1, 17, 22 to cross-pin spec §2.1 ↔ DDL §3 ↔ writer §5.
var expectedColumnNames = []string{
	"run_id",
	"workload_id",
	"claim_id",
	"experiment_id",
	"baseline_or_ablation",
	"loom_commit",
	"agentserver_commit",
	"modelserver_commit",
	"app_commit",
	"machine_topology",
	"context_ground_truth",
	"capability_snapshot_hash",
	"task_contract_hash",
	"dynamic_mcp_registry_hash",
	"selected_context",
	"ground_truth_context",
	"start_time",
	"end_time",
	"success_oracle_result",
	"failure_category",
	"human_intervention_count",
	"artifact_hashes",
	"observer_trace_path",
	"model_trace_id",
}

// Test 15: TestRegisteredOnAblation — package init() registered NoObserver.
func TestRegisteredOnAblation(t *testing.T) {
	found := false
	for _, name := range ablation.Default.List() {
		if name == ablation.NoObserver {
			found = true
		}
	}
	if !found {
		t.Fatalf("ablation.Default.List() does not contain NoObserver; got %v", ablation.Default.List())
	}
	// Successful init implies InitError() == nil for the test binary.
	if err := InitError(); err != nil {
		t.Fatalf("InitError() should be nil after a clean init, got %v", err)
	}
}

// Test 15a: TestRegistrationCollision_DetectableByOtherPackage.
// Mirrors the failure mode the fresh-review flagged: if a second
// package competes for the same FlagName, ablation.Default rejects the
// second Register. We can't replay package init in-process, but we can
// prove the registry surfaces ErrAlreadyRegistered, which is what
// initRegistrationErr would capture if a colliding init had run first.
func TestRegistrationCollision_DetectableByOtherPackage(t *testing.T) {
	// Register a fresh target into Default for NoObserver — must
	// collide with this package's existing registration.
	var competing bool
	err := ablation.Default.Register(ablation.NoObserver, &competing)
	if !errors.Is(err, ablation.ErrAlreadyRegistered) {
		t.Fatalf("expected ErrAlreadyRegistered for second registration, got %v", err)
	}
}

// Test 16a: ArtifactHashes slice length cap.
func TestInsert_RejectsTooManyArtifactHashes(t *testing.T) {
	w, _ := newTestWriter(t)
	hashes := make([]string, maxArtifactHashes+1)
	for i := range hashes {
		hashes[i] = strings.Repeat("a", 64)
	}
	s := sampleSchema()
	s.ArtifactHashes = hashes
	err := w.Insert(context.Background(), s)
	if !errors.Is(err, ErrTooManyArtifactHashes) {
		t.Fatalf("want ErrTooManyArtifactHashes, got %v", err)
	}
	// Boundary: exactly maxArtifactHashes is accepted.
	hashes = hashes[:maxArtifactHashes]
	s.RunID = "run-cap-boundary-12345"
	s.ArtifactHashes = hashes
	if err := w.Insert(context.Background(), s); err != nil {
		t.Fatalf("boundary length must be accepted, got %v", err)
	}
}

// Test 6a: failure_category invariant — must be empty iff result==pass.
func TestInsert_RejectsFailureCategoryPassMismatch(t *testing.T) {
	w, _ := newTestWriter(t)
	t.Run("pass_with_category", func(t *testing.T) {
		s := sampleSchema()
		s.RunID = "run-fc-pass-with-cat"
		s.SuccessOracleResult = "pass"
		s.FailureCategory = "FailNetwork"
		err := w.Insert(context.Background(), s)
		if !errors.Is(err, ErrFailureCategoryOnPass) {
			t.Fatalf("want ErrFailureCategoryOnPass, got %v", err)
		}
	})
	t.Run("fail_without_category", func(t *testing.T) {
		s := sampleSchema()
		s.RunID = "run-fc-fail-no-cat"
		s.SuccessOracleResult = "fail"
		s.FailureCategory = ""
		err := w.Insert(context.Background(), s)
		if !errors.Is(err, ErrFailureCategoryOnPass) {
			t.Fatalf("want ErrFailureCategoryOnPass (same sentinel covers the inverse), got %v", err)
		}
	})
	t.Run("fail_with_category_ok", func(t *testing.T) {
		s := sampleSchema()
		s.RunID = "run-fc-fail-with-cat"
		s.SuccessOracleResult = "fail"
		s.FailureCategory = "FailNetwork"
		if err := w.Insert(context.Background(), s); err != nil {
			t.Fatalf("fail+category must be accepted, got %v", err)
		}
	})
	t.Run("pass_without_category_ok", func(t *testing.T) {
		s := sampleSchema()
		s.RunID = "run-fc-pass-no-cat"
		s.SuccessOracleResult = "pass"
		s.FailureCategory = ""
		if err := w.Insert(context.Background(), s); err != nil {
			t.Fatalf("pass+empty must be accepted, got %v", err)
		}
	})
}

// Test 4: TestInsert_RejectsBadRunIDFormat.
//
// Validation-only test — it exercises Insert via an in-memory DB via the
// helper from writer_test.go, but the assertions check sentinel
// returns. Sub-cases live here to keep validation-only failures
// together.
func TestInsert_RejectsBadRunIDFormat(t *testing.T) {
	cases := map[string]string{
		"slash":            "run/with/slash00",
		"dotdot":           "run..traversal",
		"too_short_7":      "abc1234",
		"too_long_129":     strings.Repeat("a", 129),
		"whitespace_lead":  " run-with-lead",
		"whitespace_inner": "run with space",
		"sql_quote":        "run-'name'-1234",
	}
	w, _ := newTestWriter(t)
	for name, badID := range cases {
		t.Run(name, func(t *testing.T) {
			s := sampleSchema()
			s.RunID = badID
			err := w.Insert(context.Background(), s)
			if !errors.Is(err, ErrInvalidRunID) {
				t.Fatalf("RunID=%q: want ErrInvalidRunID, got %v", badID, err)
			}
		})
	}
}

// Test 3: TestInsert_RejectsBadArtifactHash.
func TestInsert_RejectsBadArtifactHash(t *testing.T) {
	w, _ := newTestWriter(t)
	cases := map[string][]string{
		"non_hex": {
			"ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ",
		},
		"length_63": {
			strings.Repeat("a", 63),
		},
		"empty_string": {
			"",
		},
		"uppercase_hex": { // we mandate lowercase per the regex
			strings.Repeat("A", 64),
		},
		"path_disguised_as_hash": {
			"../etc/passwd" + strings.Repeat("a", 51), // length 64 but not all hex
		},
	}
	for name, hashes := range cases {
		t.Run(name, func(t *testing.T) {
			s := sampleSchema()
			s.ArtifactHashes = hashes
			err := w.Insert(context.Background(), s)
			if !errors.Is(err, ErrInvalidArtifactHash) {
				t.Fatalf("hashes=%v: want ErrInvalidArtifactHash, got %v", hashes, err)
			}
			if !strings.Contains(err.Error(), "artifact_hashes[") {
				t.Fatalf("hashes=%v: error message %q must name artifact_hashes[<i>]", hashes, err)
			}
		})
	}
}

// Test 5: TestInsert_RejectsOversizedField.
func TestInsert_RejectsOversizedField(t *testing.T) {
	w, _ := newTestWriter(t)
	big := strings.Repeat("a", maxFieldBytes+1)
	at := strings.Repeat("a", maxFieldBytes) // boundary — accepted
	cases := []struct {
		name     string
		mutate   func(*Schema)
		wantErr  error
		wantWord string // substring required in error.Error()
	}{
		{"WorkloadID_overlimit", func(s *Schema) { s.WorkloadID = big }, ErrOversizedField, "workload_id"},
		{"MachineTopology_overlimit", func(s *Schema) { s.MachineTopology = big }, ErrOversizedField, "machine_topology"},
		{"SelectedContext_overlimit", func(s *Schema) { s.SelectedContext = big }, ErrOversizedField, "selected_context"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := sampleSchema()
			c.mutate(&s)
			err := w.Insert(context.Background(), s)
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("want %v, got %v", c.wantErr, err)
			}
			if !strings.Contains(err.Error(), c.wantWord) {
				t.Fatalf("error %q must name %q", err, c.wantWord)
			}
		})
	}
	// Boundary value passes.
	t.Run("WorkloadID_at_boundary", func(t *testing.T) {
		s := sampleSchema()
		s.WorkloadID = at
		if err := w.Insert(context.Background(), s); err != nil {
			t.Fatalf("boundary value (%d bytes) must be accepted, got %v", maxFieldBytes, err)
		}
	})
}

// Test 6: TestInsert_RejectsBadOracleResult.
func TestInsert_RejectsBadOracleResult(t *testing.T) {
	w, _ := newTestWriter(t)
	for _, bad := range []string{"PASS", "ok", "", "success", "p"} {
		t.Run("bad_"+bad, func(t *testing.T) {
			s := sampleSchema()
			s.SuccessOracleResult = bad
			err := w.Insert(context.Background(), s)
			if !errors.Is(err, ErrInvalidOracleResult) {
				t.Fatalf("want ErrInvalidOracleResult, got %v", err)
			}
		})
	}
	for _, good := range []string{"pass", "fail", "timeout"} {
		t.Run("good_"+good, func(t *testing.T) {
			// Use a unique RunID per case so the second/third inserts
			// don't collide on PRIMARY KEY. The failure_category
			// invariant requires a non-empty category for fail/timeout
			// (and an empty one for pass).
			s := sampleSchema()
			s.RunID = "run-" + good + "-1234567"
			s.SuccessOracleResult = good
			if good == "pass" {
				s.FailureCategory = ""
			} else {
				s.FailureCategory = "FailNetwork"
			}
			if err := w.Insert(context.Background(), s); err != nil {
				t.Fatalf("oracle=%q must be accepted, got %v", good, err)
			}
		})
	}
}

// Test 7: TestInsert_RejectsZeroTime.
func TestInsert_RejectsZeroTime(t *testing.T) {
	w, _ := newTestWriter(t)
	t.Run("StartTime_zero", func(t *testing.T) {
		s := sampleSchema()
		s.StartTime = time.Time{}
		err := w.Insert(context.Background(), s)
		if !errors.Is(err, ErrInvalidTime) {
			t.Fatalf("want ErrInvalidTime, got %v", err)
		}
	})
	t.Run("EndTime_zero", func(t *testing.T) {
		s := sampleSchema()
		s.EndTime = time.Time{}
		err := w.Insert(context.Background(), s)
		if !errors.Is(err, ErrInvalidTime) {
			t.Fatalf("want ErrInvalidTime, got %v", err)
		}
	})
}

// Test 8: TestNoObserver_DroppedRunLogged.
func TestNoObserver_DroppedRunLogged(t *testing.T) {
	w, _ := newTestWriter(t)
	prevOut := log.Writer()
	prevFlags := log.Flags()
	buf := &bytes.Buffer{}
	log.SetOutput(buf)
	log.SetFlags(0) // no date prefix; assertion is on the body text only
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
		DisableTelemetry = false
	})
	DisableTelemetry = true
	s := sampleSchema()
	if err := w.Insert(context.Background(), s); err != nil {
		t.Fatalf("Insert with DisableTelemetry must return nil, got %v", err)
	}
	want := "[ablation] NoObserver: dropped run_id=" + s.RunID
	if !strings.Contains(buf.String(), want) {
		t.Fatalf("log output %q must contain %q", buf.String(), want)
	}
	// And the DB MUST be untouched: 0 rows.
	var n int
	if err := getDB(t, w).QueryRow("SELECT COUNT(*) FROM runs").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("DB should have 0 rows when DisableTelemetry=true, got %d", n)
	}
}

// Test 9: TestNoObserver_StillRejectsInvalid.
func TestNoObserver_StillRejectsInvalid(t *testing.T) {
	w, _ := newTestWriter(t)
	t.Cleanup(func() { DisableTelemetry = false })
	DisableTelemetry = true
	s := sampleSchema()
	s.RunID = "bad/id"
	err := w.Insert(context.Background(), s)
	if !errors.Is(err, ErrInvalidRunID) {
		t.Fatalf("validation must still run under DisableTelemetry, got %v", err)
	}
}

// Test 16: TestArtifactHashes_RoundtripJSON.
func TestArtifactHashes_RoundtripJSON(t *testing.T) {
	w, db := newTestWriter(t)
	cases := map[string][]string{
		"nil":      nil,
		"empty":    {},
		"three":    {strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)},
		"singular": {strings.Repeat("0", 64)},
	}
	for name, hashes := range cases {
		t.Run(name, func(t *testing.T) {
			s := sampleSchema()
			s.RunID = "run-rt-" + name + "-xxxx"
			s.ArtifactHashes = hashes
			if err := w.Insert(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			var raw string
			if err := db.QueryRow("SELECT artifact_hashes FROM runs WHERE run_id = ?", s.RunID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			// Nil and empty both serialise as the literal "[]".
			if hashes == nil || len(hashes) == 0 {
				if raw != "[]" {
					t.Fatalf("nil/empty must round-trip as `[]`, got %q", raw)
				}
				return
			}
			// Non-empty case: parse back and compare.
			got, err := decodeArtifactHashes(raw)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, hashes) {
				t.Fatalf("roundtrip: want %v, got %v", hashes, got)
			}
		})
	}
}
