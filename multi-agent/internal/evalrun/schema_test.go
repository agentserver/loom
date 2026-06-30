package evalrun

import (
	"bytes"
	"context"
	"errors"
	"log"
	"reflect"
	"strings"
	"sync"
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
	// Successful init implies the sticky-error sentinel is nil.
	if initRegistrationErr != nil {
		t.Fatalf("initRegistrationErr should be nil after a clean init, got %v", initRegistrationErr)
	}
}

// Test 15a: TestRegistrationCollision_RecoveryPathLogsOnFirstInsert.
// Exercises the failure mode spec §7(c) names directly: init() lost
// the race so initRegistrationErr is non-nil, DisableTelemetry is wired
// to nobody, and the operator runs --ablation NoObserver in the dark.
// Insert MUST emit the loud WARNING via initWarnOnce so a stderr log
// makes the divergence observable. Asserts: (1) WARNING substring in
// log buffer on first Insert; (2) WARNING fires only ONCE across
// repeated Inserts (the sync.Once contract); (3) row still writes
// successfully (the WARNING is informational, not blocking).
func TestRegistrationCollision_RecoveryPathLogsOnFirstInsert(t *testing.T) {
	// Save + restore package-private state. The test is in the same
	// package, so we can poke initRegistrationErr and reset
	// initWarnOnce via a fresh sync.Once instance; without these
	// resets the test would be order-dependent on test shuffling.
	// sync.Once can't be copied (vet enforces noCopy), so we swap
	// pointer-style: assign a new zero-value Once and restore via a
	// second new Once at cleanup (the prior Once may already have
	// fired and is unrecoverable — what matters is that subsequent
	// test runs see a not-yet-fired Once).
	prevErr := initRegistrationErr
	prevOnce := initWarnOnce
	prevOut := log.Writer()
	prevFlags := log.Flags()
	buf := &bytes.Buffer{}
	log.SetOutput(buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		initRegistrationErr = prevErr
		initWarnOnce = prevOnce
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})
	initRegistrationErr = errors.New("simulated collision")
	initWarnOnce = &sync.Once{}

	w, _ := newTestWriter(t)
	s := sampleSchema()
	s.RunID = "run-collision-warn-01"
	if err := w.Insert(context.Background(), s); err != nil {
		t.Fatalf("Insert under simulated collision must still succeed, got %v", err)
	}
	first := buf.String()
	if !strings.Contains(first, "WARNING") || !strings.Contains(first, "NoObserver") {
		t.Fatalf("first Insert under collision must log WARNING about NoObserver; got %q", first)
	}
	// Second Insert: the WARNING must NOT repeat (sync.Once).
	s.RunID = "run-collision-warn-02"
	if err := w.Insert(context.Background(), s); err != nil {
		t.Fatalf("second Insert: %v", err)
	}
	second := strings.TrimPrefix(buf.String(), first)
	if strings.Contains(second, "WARNING") {
		t.Fatalf("second Insert must NOT repeat the WARNING (sync.Once violated); got %q", second)
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

// Test 6a: failure_category invariants. Covers both the pass/category
// compatibility rule AND the 11-class taxonomy membership rule.
func TestInsert_RejectsBadFailureCategory(t *testing.T) {
	w, _ := newTestWriter(t)
	t.Run("pass_with_category", func(t *testing.T) {
		s := sampleSchema()
		s.RunID = "run-fc-pass-with-cat"
		s.SuccessOracleResult = "pass"
		s.FailureCategory = "slave_disconnect"
		err := w.Insert(context.Background(), s)
		if !errors.Is(err, ErrInvalidFailureCategory) {
			t.Fatalf("want ErrInvalidFailureCategory, got %v", err)
		}
	})
	t.Run("fail_without_category", func(t *testing.T) {
		s := sampleSchema()
		s.RunID = "run-fc-fail-no-cat"
		s.SuccessOracleResult = "fail"
		s.FailureCategory = ""
		err := w.Insert(context.Background(), s)
		if !errors.Is(err, ErrInvalidFailureCategory) {
			t.Fatalf("want ErrInvalidFailureCategory (same sentinel covers the inverse), got %v", err)
		}
	})
	t.Run("timeout_without_category", func(t *testing.T) {
		s := sampleSchema()
		s.RunID = "run-fc-timeout-no-cat"
		s.SuccessOracleResult = "timeout"
		s.FailureCategory = ""
		err := w.Insert(context.Background(), s)
		if !errors.Is(err, ErrInvalidFailureCategory) {
			t.Fatalf("want ErrInvalidFailureCategory; timeout requires a category (use \"unknown\" if unclassifiable), got %v", err)
		}
	})
	t.Run("fail_with_unknown_taxonomy", func(t *testing.T) {
		s := sampleSchema()
		s.RunID = "run-fc-fail-bogus"
		s.SuccessOracleResult = "fail"
		s.FailureCategory = "FailNetwork" // not in observerstore.AllCategories()
		err := w.Insert(context.Background(), s)
		if !errors.Is(err, ErrInvalidFailureCategory) {
			t.Fatalf("want ErrInvalidFailureCategory for off-taxonomy value, got %v", err)
		}
	})
	t.Run("fail_with_uppercase_taxonomy", func(t *testing.T) {
		s := sampleSchema()
		s.RunID = "run-fc-fail-upper"
		s.SuccessOracleResult = "fail"
		s.FailureCategory = "TIMEOUT" // wrong case
		err := w.Insert(context.Background(), s)
		if !errors.Is(err, ErrInvalidFailureCategory) {
			t.Fatalf("want ErrInvalidFailureCategory for case-mismatch, got %v", err)
		}
	})
	// Accepted cases.
	t.Run("pass_without_category_ok", func(t *testing.T) {
		s := sampleSchema()
		s.RunID = "run-fc-pass-no-cat"
		s.SuccessOracleResult = "pass"
		s.FailureCategory = ""
		if err := w.Insert(context.Background(), s); err != nil {
			t.Fatalf("pass+empty must be accepted, got %v", err)
		}
	})
	t.Run("fail_with_taxonomy_category_ok", func(t *testing.T) {
		s := sampleSchema()
		s.RunID = "run-fc-fail-with-cat"
		s.SuccessOracleResult = "fail"
		s.FailureCategory = "slave_disconnect"
		if err := w.Insert(context.Background(), s); err != nil {
			t.Fatalf("fail+slave_disconnect must be accepted, got %v", err)
		}
	})
	t.Run("timeout_with_unknown_sentinel_ok", func(t *testing.T) {
		s := sampleSchema()
		s.RunID = "run-fc-timeout-unknown"
		s.SuccessOracleResult = "timeout"
		s.FailureCategory = "unknown" // FailUnknown escape hatch
		if err := w.Insert(context.Background(), s); err != nil {
			t.Fatalf("timeout+unknown must be accepted, got %v", err)
		}
	})
	t.Run("fail_with_unknown_sentinel_ok", func(t *testing.T) {
		s := sampleSchema()
		s.RunID = "run-fc-fail-unknown"
		s.SuccessOracleResult = "fail"
		s.FailureCategory = "unknown"
		if err := w.Insert(context.Background(), s); err != nil {
			t.Fatalf("fail+unknown must be accepted, got %v", err)
		}
	})
}

// Test 6b: invalid UTF-8 in any string field is rejected.
func TestInsert_RejectsInvalidUTF8(t *testing.T) {
	w, _ := newTestWriter(t)
	badUTF8 := "valid_prefix_\xff\xfe_invalid"
	cases := []struct {
		name   string
		mutate func(*Schema)
		word   string
	}{
		{"workload_id", func(s *Schema) { s.WorkloadID = badUTF8 }, "workload_id"},
		{"machine_topology", func(s *Schema) { s.MachineTopology = badUTF8 }, "machine_topology"},
		{"selected_context", func(s *Schema) { s.SelectedContext = badUTF8 }, "selected_context"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := sampleSchema()
			s.RunID = "run-utf8-" + c.name + "-1"
			c.mutate(&s)
			err := w.Insert(context.Background(), s)
			if !errors.Is(err, ErrInvalidUTF8) {
				t.Fatalf("want ErrInvalidUTF8 for %s, got %v", c.name, err)
			}
			if !strings.Contains(err.Error(), c.word) {
				t.Fatalf("error must name field %s: %v", c.word, err)
			}
		})
	}
}

// Test 1b: time roundtrip preserves chronological order under
// lexicographic sort across mixed-precision values. Without the
// fixed-9-digit nanosecond format, two times that differ only in
// fractional precision would lex-sort in the wrong order (".5Z" sorts
// before "Z" because '.' < 'Z'). The fixed-precision format makes lex
// order == chrono order, a property downstream `ORDER BY start_time`
// and `BETWEEN` queries depend on.
func TestInsert_TimeFormatPreservesLexOrder(t *testing.T) {
	w, db := newTestWriter(t)
	base := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		runID string
		t     time.Time
	}{
		{"run-time-A-12345678", base},                             // exact second
		{"run-time-B-12345678", base.Add(500 * time.Millisecond)}, // +0.5s
		{"run-time-C-12345678", base.Add(time.Second)},            // exact next second
	}
	for _, c := range cases {
		s := sampleSchema()
		s.RunID = c.runID
		s.StartTime = c.t
		s.EndTime = c.t.Add(time.Second)
		if err := w.Insert(context.Background(), s); err != nil {
			t.Fatalf("insert %s: %v", c.runID, err)
		}
	}
	rows, err := db.Query("SELECT run_id, start_time FROM runs ORDER BY start_time")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var order []string
	for rows.Next() {
		var id, st string
		if err := rows.Scan(&id, &st); err != nil {
			t.Fatal(err)
		}
		order = append(order, id)
		// Every stored time MUST have the fixed-9-digit nanosecond
		// field, regardless of input precision.
		if !strings.Contains(st, ".000000000Z") && !strings.Contains(st, ".500000000Z") {
			t.Errorf("start_time %q must use fixed 9-digit nanos", st)
		}
	}
	want := []string{"run-time-A-12345678", "run-time-B-12345678", "run-time-C-12345678"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("ORDER BY start_time: got %v, want %v (mixed-precision RFC3339Nano would mis-sort B before A)", order, want)
	}
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
				s.FailureCategory = "slave_disconnect"
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
