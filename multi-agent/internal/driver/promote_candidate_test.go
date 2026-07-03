package driver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/internal/observer"
)

// recPromoWriter is a driver-facing PromoteCandidatesWriter mock.
type recPromoWriter struct {
	mu       sync.Mutex
	inserts  []PromoteCandidateRow
	updates  []struct{ CID, Dec, At string }
	expires  []string
	err      error
}

func (r *recPromoWriter) InsertPromoteCandidate(_ context.Context, row PromoteCandidateRow) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.inserts = append(r.inserts, row)
	return nil
}
func (r *recPromoWriter) UpdatePromoteCandidateDecision(_ context.Context, cid, dec, at string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updates = append(r.updates, struct{ CID, Dec, At string }{cid, dec, at})
	return nil
}
func (r *recPromoWriter) ExpirePromoteCandidates(_ context.Context, cutoff string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expires = append(r.expires, cutoff)
	return 0, nil
}
func (r *recPromoWriter) snapshot() []PromoteCandidateRow {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]PromoteCandidateRow, len(r.inserts))
	copy(out, r.inserts)
	return out
}

// recEvents2 — separate name to avoid clash with promotion pipeline's recEvents in
// pipeline_test.go (different package though; here just an ObserverSink shim).
type recEvents2 struct {
	mu sync.Mutex
	evs []observer.Event
}

func (r *recEvents2) Emit(ev observer.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = append(r.evs, ev)
}

func setupPromo(t *testing.T, w PromoteCandidatesWriter, ev *recEvents2) {
	t.Helper()
	resetPromoteCandidateDepsForTest()
	resetNoUserPromotionPathForTest()
	resetCurrentRunIDForTest()
	resetFamilyCountsForTest()
	SetCurrentRunID("run-testxyz001")
	deps := PromoteCandidateDeps{
		Writer: w,
		Now:    func() time.Time { return time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC) },
	}
	if ev != nil {
		deps.Events = ev
	}
	SetPromoteCandidateDeps(deps)
}

func validSignal() CandidateSignal {
	return CandidateSignal{
		Family:        "csv_profile",
		SourceTaskIDs: []string{"task_11111111", "task_22222222"},
		SurfacedBy:    "similarity_signal",
		WorkspaceID:   "ws-abc12345",
	}
}

// --- §7 (a) ---

func TestSurfacePromoteCandidate_RejectsEmptyTaskIDs(t *testing.T) {
	setupPromo(t, &recPromoWriter{}, nil)
	sig := validSignal()
	sig.SourceTaskIDs = nil
	_, err := SurfacePromoteCandidate(context.Background(), sig)
	if !errors.Is(err, ErrEmptySourceTaskIDs) {
		t.Fatalf("want ErrEmptySourceTaskIDs, got %v", err)
	}
}

func TestSurfacePromoteCandidate_RejectsMalformedTaskID(t *testing.T) {
	setupPromo(t, &recPromoWriter{}, nil)
	sig := validSignal()
	sig.SourceTaskIDs = []string{"short", "task_22222222"}
	_, err := SurfacePromoteCandidate(context.Background(), sig)
	if !errors.Is(err, ErrInvalidSourceTaskID) {
		t.Fatalf("want ErrInvalidSourceTaskID, got %v", err)
	}
}

// --- §7 (b) ---

func TestSurfacePromoteCandidate_RejectsBadFamily(t *testing.T) {
	setupPromo(t, &recPromoWriter{}, nil)
	for _, bad := range []string{"CSV", "csv profile", "1csv", "csv/x"} {
		sig := validSignal()
		sig.Family = bad
		_, err := SurfacePromoteCandidate(context.Background(), sig)
		if !errors.Is(err, ErrInvalidFamily) {
			t.Errorf("want ErrInvalidFamily for %q, got %v", bad, err)
		}
	}
}

// --- §7 (c) ---

func TestSurfacePromoteCandidate_SurfacedAtIsUTC(t *testing.T) {
	w := &recPromoWriter{}
	setupPromo(t, w, nil)
	_, err := SurfacePromoteCandidate(context.Background(), validSignal())
	if err != nil {
		t.Fatal(err)
	}
	rows := w.snapshot()
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if !strings.HasSuffix(rows[0].SurfacedAt, "Z") {
		t.Fatalf("surfaced_at not UTC: %q", rows[0].SurfacedAt)
	}
}

// --- §7 (e) ablation short-circuit ---

func TestSurfacePromoteCandidate_NoUserPromotionPathAblationSuppressesAndLogs(t *testing.T) {
	buf := captureLogs(t)
	w := &recPromoWriter{}
	ev := &recEvents2{}
	setupPromo(t, w, ev)
	noUserPromotionPath = true
	defer resetNoUserPromotionPathForTest()

	cid, err := SurfacePromoteCandidate(context.Background(), validSignal())
	if err != nil {
		t.Fatal(err)
	}
	if cid != "" {
		t.Fatalf("ablation should return empty cid, got %q", cid)
	}
	if len(w.snapshot()) != 0 {
		t.Fatalf("ablation must not write rows, got %d", len(w.snapshot()))
	}
	if len(ev.evs) != 0 {
		t.Fatalf("ablation must not emit events, got %d", len(ev.evs))
	}
	if !strings.Contains(buf.String(), "[ablation] NoUserPromotionPath: candidate suppressed") {
		t.Fatalf("expected ablation log, got:\n%s", buf.String())
	}
}

func TestSurfacePromoteCandidate_NoUserPromotionPathAblationSideEffectFree(t *testing.T) {
	// Regression guard: ablation short-circuit runs BEFORE validation.
	// A malformed signal under ablation should not surface a validation
	// error (silent-open is preferable to leaking that the flag was
	// checked).
	w := &recPromoWriter{}
	setupPromo(t, w, nil)
	noUserPromotionPath = true
	defer resetNoUserPromotionPathForTest()

	sig := validSignal()
	sig.SourceTaskIDs = nil // would normally fail validation
	_, err := SurfacePromoteCandidate(context.Background(), sig)
	if err != nil {
		t.Fatalf("ablated call must not report validation errors: %v", err)
	}
}

// --- §7 (f) idempotent decision ---

func TestRecordCandidateDecision_IdempotentSecondUpdate(t *testing.T) {
	w := &recPromoWriter{}
	setupPromo(t, w, nil)
	_ = RecordCandidateDecision(context.Background(), "cand_x", DecisionPromoted, time.Now().UTC())
	_ = RecordCandidateDecision(context.Background(), "cand_x", DecisionDeclined, time.Now().UTC())
	// Both calls are accepted by the writer; the WHERE decision=''
	// clause in the SQL makes the second a no-op at the DB level.
	// Assert both were recorded on the mock (mock doesn't emulate
	// WHERE; the invariant is that the caller doesn't error).
	if len(w.updates) != 2 {
		t.Fatalf("want 2 updates recorded on mock, got %d", len(w.updates))
	}
}

// --- §7 (h) perf conditional ---

func TestSurfacePromoteCandidate_PerfBench_ConditionalOnShort(t *testing.T) {
	w := &recPromoWriter{}
	setupPromo(t, w, nil)
	// Warmup.
	for i := 0; i < 5; i++ {
		_, _ = SurfacePromoteCandidate(context.Background(), validSignal())
	}
	if testing.Short() {
		t.Skip("short mode; wall-clock skipped")
	}
	start := time.Now()
	for i := 0; i < 100; i++ {
		_, _ = SurfacePromoteCandidate(context.Background(), validSignal())
	}
	if elapsed := time.Since(start); elapsed > 20*time.Millisecond {
		t.Fatalf("100 surfacings took %v > 20ms", elapsed)
	}
}

// --- §7 (i) run-scoped candidate_id ---

func TestCandidateID_DerivedFromFamilyAndSortedTaskIDs(t *testing.T) {
	a := computeCandidateID("r1", "fam", []string{"t1", "t2"})
	b := computeCandidateID("r1", "fam", []string{"t2", "t1"})
	if a != b {
		t.Fatalf("order-dependent: %q vs %q", a, b)
	}
	c := computeCandidateID("r1", "other", []string{"t1", "t2"})
	if a == c {
		t.Fatalf("different family should differ")
	}
}

func TestCandidateID_DifferentRunsProduceDifferentIDs(t *testing.T) {
	a := computeCandidateID("run-a", "fam", []string{"t1"})
	b := computeCandidateID("run-b", "fam", []string{"t1"})
	if a == b {
		t.Fatalf("different runs should produce different ids: %q == %q", a, b)
	}
}

// --- Metric / happy path ---

func TestSurfacePromoteCandidate_WritesRowAndEvent(t *testing.T) {
	w := &recPromoWriter{}
	ev := &recEvents2{}
	setupPromo(t, w, ev)
	cid, err := SurfacePromoteCandidate(context.Background(), validSignal())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cid, "cand_") {
		t.Fatalf("candidate_id shape: %q", cid)
	}
	if len(w.snapshot()) != 1 {
		t.Fatalf("expected 1 row, got %d", len(w.snapshot()))
	}
	if len(ev.evs) != 1 {
		t.Fatalf("expected 1 event, got %d", len(ev.evs))
	}
	// Event payload MUST NOT include source_task_ids per §4.4.
	body := string(ev.evs[0].Payload)
	if strings.Contains(body, "task_11111111") {
		t.Fatalf("event payload leaks source_task_ids: %s", body)
	}
	// Row must carry the source task ids as JSON.
	var tids []string
	if err := json.Unmarshal([]byte(w.snapshot()[0].SourceTaskIDs), &tids); err != nil {
		t.Fatalf("source_task_ids not valid JSON: %v", err)
	}
	if len(tids) != 2 {
		t.Fatalf("expected 2 task ids in row, got %+v", tids)
	}
}

// --- run_id propagation ---

func TestSurfacePromoteCandidate_WritesRunIDFromCurrentRunID(t *testing.T) {
	w := &recPromoWriter{}
	setupPromo(t, w, nil)
	SetCurrentRunID("run-xyz01234")
	_, err := SurfacePromoteCandidate(context.Background(), validSignal())
	if err != nil {
		t.Fatal(err)
	}
	if w.snapshot()[0].RunID != "run-xyz01234" {
		t.Fatalf("row run_id = %q, want run-xyz01234", w.snapshot()[0].RunID)
	}
}

// --- Init-error surfacing ---

func TestSurfacePromoteCandidate_InitErrorSurfacedOnFirstCall(t *testing.T) {
	buf := captureLogs(t)
	setupPromo(t, &recPromoWriter{}, nil)
	resetNoUserPromotionPathForTest()
	noUserPromotionPathInitErr = errors.New("simulated register clash")

	// Two calls; one error log.
	_, _ = SurfacePromoteCandidate(context.Background(), validSignal())
	_, _ = SurfacePromoteCandidate(context.Background(), validSignal())
	want := "[error] NoUserPromotionPath ablation wiring inert"
	if strings.Count(buf.String(), want) != 1 {
		t.Fatalf("want exactly one %q, got %d in:\n%s", want, strings.Count(buf.String(), want), buf.String())
	}
}

// --- Detector (§4.2) ---

func TestRecordAdHocScriptTask_FiresCandidateAfterSecondFamily(t *testing.T) {
	w := &recPromoWriter{}
	setupPromo(t, w, nil)

	RecordAdHocScriptTask(context.Background(), "csvfam", "task_11111111", "ws-abc12345")
	if len(w.snapshot()) != 0 {
		t.Fatalf("first call should not fire, got %d rows", len(w.snapshot()))
	}
	RecordAdHocScriptTask(context.Background(), "csvfam", "task_22222222", "ws-abc12345")
	if len(w.snapshot()) != 1 {
		t.Fatalf("second call should fire, got %d rows", len(w.snapshot()))
	}
	// Third call with same task doesn't double-fire (dedup on task_id).
	RecordAdHocScriptTask(context.Background(), "csvfam", "task_22222222", "ws-abc12345")
	if len(w.snapshot()) != 1 {
		t.Fatalf("dup task should not re-fire, got %d rows", len(w.snapshot()))
	}
	// PR #71 round-2 review P1-A: THIRD unique task must NOT
	// re-fire — we want ONE candidate per (run, family) per session,
	// not one per unique task in the family. Earlier code inflated
	// PromotionCandidateSurfacingRate linearly with per-family task
	// count.
	RecordAdHocScriptTask(context.Background(), "csvfam", "task_33333333", "ws-abc12345")
	if len(w.snapshot()) != 1 {
		t.Fatalf("third unique task must NOT re-fire (once-per-family-per-session invariant); got %d rows", len(w.snapshot()))
	}
	// Fourth unique task: same.
	RecordAdHocScriptTask(context.Background(), "csvfam", "task_44444444", "ws-abc12345")
	if len(w.snapshot()) != 1 {
		t.Fatalf("fourth unique task must NOT re-fire; got %d rows", len(w.snapshot()))
	}
	// A DIFFERENT family does fire (its own once-per-session).
	RecordAdHocScriptTask(context.Background(), "logfam", "task_55555555", "ws-abc12345")
	RecordAdHocScriptTask(context.Background(), "logfam", "task_66666666", "ws-abc12345")
	if len(w.snapshot()) != 2 {
		t.Fatalf("second family should fire once; got %d rows", len(w.snapshot()))
	}
}

// TestRecordAdHocScriptTask_FamilyCountsBounded — PR #71 round-2
// review P1-D. Streaming 100 unique task_ids in the same family
// must not grow the internal slice unbounded; it caps at
// familyCountsPerFamilyCap (FIFO evict).
func TestRecordAdHocScriptTask_FamilyCountsBounded(t *testing.T) {
	w := &recPromoWriter{}
	setupPromo(t, w, nil)
	// Wire an env override to force the family so the
	// FamilyOfTaskSummary path doesn't affect this test.
	for i := 0; i < 100; i++ {
		// Pad each task_id to satisfy the >=8 char regex.
		tid := "task_bnd"
		if i < 10 {
			tid = tid + "00" + string(rune('0'+i))
		} else if i < 100 {
			tid = tid + "0" + string(rune('0'+i/10)) + string(rune('0'+i%10))
		}
		RecordAdHocScriptTask(context.Background(), "boundfam", tid, "ws-abc12345")
	}
	familyCountsMu.Lock()
	size := len(familyCounts["boundfam"])
	familyCountsMu.Unlock()
	if size > familyCountsPerFamilyCap {
		t.Fatalf("familyCounts[boundfam] len %d > cap %d — bound not enforced", size, familyCountsPerFamilyCap)
	}
}

func TestRecordAdHocScriptTask_UnderNoUserPromotionPath_NoCandidate(t *testing.T) {
	buf := captureLogs(t)
	w := &recPromoWriter{}
	setupPromo(t, w, nil)
	noUserPromotionPath = true
	defer resetNoUserPromotionPathForTest()

	for i, tid := range []string{"task_11111111", "task_22222222", "task_33333333", "task_44444444", "task_55555555"} {
		_ = i
		RecordAdHocScriptTask(context.Background(), "csvfam", tid, "ws-abc12345")
	}
	if len(w.snapshot()) != 0 {
		t.Fatalf("ablated detector must not fire rows, got %d", len(w.snapshot()))
	}
	// Exactly one suppression log — the 2→3-unique-tasks transition
	// is the ONLY call that reaches SurfacePromoteCandidate under the
	// once-per-family invariant (round-2 review P1-A). Subsequent
	// unique tasks (3, 4, 5) don't reach Surface at all, so they
	// don't produce ablation logs either.
	suppressions := strings.Count(buf.String(), "[ablation] NoUserPromotionPath: candidate suppressed")
	if suppressions != 1 {
		t.Fatalf("want 1 suppression log (once-per-family), got %d in:\n%s", suppressions, buf.String())
	}
}

func TestRecordAdHocScriptTask_LOOMEvalTaskFamilyEnvOverride(t *testing.T) {
	w := &recPromoWriter{}
	setupPromo(t, w, nil)
	t.Setenv("LOOM_EVAL_TASK_FAMILY", "envforced_fam")

	RecordAdHocScriptTask(context.Background(), "actualfam", "task_11111111", "ws-abc12345")
	RecordAdHocScriptTask(context.Background(), "actualfam", "task_22222222", "ws-abc12345")
	rows := w.snapshot()
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Family != "envforced_fam" {
		t.Fatalf("family should be env override, got %q", rows[0].Family)
	}
}

func TestFamilyOfTaskSummary(t *testing.T) {
	cases := []struct{ in, want string }{
		{"csv profile a file", "csv"},
		// PR #71 round-2 review P2-C: hyphens must survive the map
		// since the family regex allows them.
		{"CSV-PROFILER", "csv-profiler"},
		{"csv-profile run", "csv-profile"},
		{"", ""},
		{"1csv", ""}, // starts with digit → regex rejects
		{"csv_1", "csv_1"},
	}
	for _, tc := range cases {
		got := FamilyOfTaskSummary(tc.in)
		if got != tc.want {
			t.Errorf("FamilyOfTaskSummary(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// --- B6 link ---

func TestRegisterSlaveMCP_UnderNoUserPromotionPath_RefusesAllExceptExplicitUser(t *testing.T) {
	// Set ablation.
	resetNoUserPromotionPathForTest()
	noUserPromotionPath = true
	defer resetNoUserPromotionPathForTest()

	// Test refusals.
	for _, reason := range []string{"driver_agent_inferred", "batch_import", "ci_seed"} {
		t.Run("refuses_"+reason, func(t *testing.T) {
			// Use the guard SDK from register_mcp_tool_test.go — same
			// package. It fails the test if delegate is called.
			gsdk := registerAuditGuardSDK(t)
			tool := toolByName(t, registerAuditGuardTools(t, gsdk), "register_slave_mcp")
			// The batch_import / ci_seed reasons require the candidate
			// sentinel per B6 §7 (g); use the SentinelCandidateTaskID
			// pattern so the audit precheck doesn't reject first.
			candID := "task_12345678"
			if reason == "batch_import" {
				candID = "batch-import-ws-abc12345"
			}
			if reason == "ci_seed" {
				candID = "ci-seed-ws-abc12345"
			}
			args := `{"target_display_name":"slave-x","spec":` + validTestSpecJSON() +
				`,"source_path":"dist/x.js","promoted_by_user_id":"user_abcdef","driver_thread_id":"thread_01_a","promotion_reason":"` + reason + `","candidate_source_task_id":"` + candID + `"}`
			_, err := tool.Call(context.Background(), []byte(args))
			if err == nil {
				t.Fatalf("expected error for reason=%s", reason)
			}
			if !strings.Contains(err.Error(), "NoUserPromotionPath") {
				t.Fatalf("expected NoUserPromotionPath in err for %s, got %v", reason, err)
			}
		})
	}
	// Explicit user request bypasses the third gate. The register
	// call proceeds to delegate; use a permissive SDK so we can
	// verify that the ablation gate itself does NOT block.
	t.Run("passes_explicit_user_request", func(t *testing.T) {
		var delegated bool
		sdk := &fakeSDK{
			discoverFunc: func() ([]agentsdk.AgentCard, error) {
				return []agentsdk.AgentCard{
					{AgentID: "slave-x", DisplayName: "slave-x", Status: "available", Card: json.RawMessage(`{"skills":["register_mcp"]}`)},
				}, nil
			},
			delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
				delegated = true
				return &agentsdk.DelegateTaskResponse{TaskID: "task-ok"}, nil
			},
			getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
				return &agentsdk.TaskInfo{TaskID: id, Status: "completed", Output: "{}"}, nil
			},
		}
		tools := newTestTools(t, sdk)
		tools.cfg.Observer.WorkspaceID = "ws-abc12345"
		tool := toolByName(t, tools, "register_slave_mcp")
		args := `{"target_display_name":"slave-x","spec":` + validTestSpecJSON() +
			`,"source_path":"dist/x.js","promoted_by_user_id":"user_abcdef","driver_thread_id":"thread_01_a","promotion_reason":"explicit_user_request","candidate_source_task_id":"task_12345678"}`
		_, err := tool.Call(context.Background(), []byte(args))
		if err != nil {
			if strings.Contains(err.Error(), "NoUserPromotionPath") {
				t.Fatalf("explicit_user_request must not be blocked by NoUserPromotionPath, got: %v", err)
			}
			t.Fatalf("unexpected error: %v", err)
		}
		if !delegated {
			t.Fatal("explicit_user_request should proceed to delegate")
		}
	})
}
