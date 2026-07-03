package promotionpipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yourorg/multi-agent/internal/buildspec"
	"github.com/yourorg/multi-agent/internal/observer"
	"github.com/yourorg/multi-agent/internal/promotionaudit"
)

// recAudit is a promotionaudit.Writer-shaped in-memory recorder.
type recAudit struct {
	mu   sync.Mutex
	rows []promotionaudit.AuditFields
	err  error
}

func (r *recAudit) write(_ context.Context, f promotionaudit.AuditFields) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.rows = append(r.rows, f)
	return nil
}

func (r *recAudit) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.rows)
}

func (r *recAudit) snapshot() []promotionaudit.AuditFields {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]promotionaudit.AuditFields, len(r.rows))
	copy(out, r.rows)
	return out
}

// recEvents captures emitted observer events.
type recEvents struct {
	mu     sync.Mutex
	events []observer.Event
}

func (r *recEvents) emit(ev observer.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *recEvents) snapshot() []observer.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]observer.Event, len(r.events))
	copy(out, r.events)
	return out
}

func validSpec() buildspec.Spec {
	return buildspec.Normalize(buildspec.Spec{
		Name:        "mytool",
		Description: "d",
		Tools: []buildspec.ToolSpec{
			{Name: "op", Description: "d", ArgsSchema: json.RawMessage(`{"type":"object"}`), ResultDescription: "r"},
		},
	})
}

func validRequest() Request {
	return Request{
		Spec:                  validSpec(),
		CasesPath:             "tests/eval/golden/csv-profiler/acceptance/cases.jsonl",
		SlaveAgentID:          "slave-b",
		SlaveDisplayName:      "slave-b",
		WorkspaceID:           "ws-abc12345",
		PromotedByUserID:      "user_abcdef",
		DriverThreadID:        "thread_01_a",
		PromotionReason:       promotionaudit.ReasonExplicitUserRequest,
		CandidateSourceTaskID: "task_12345678",
	}
}

func newHappyPipe(t *testing.T, aud *recAudit, ev *recEvents) *Pipeline {
	t.Helper()
	p, err := New(Deps{
		Delegate: func(_ context.Context, _, _, skill, _ string, _ int) (string, error) {
			switch skill {
			case "mcp-acceptance":
				return `{"acceptance_exit_code":0}`, nil
			default:
				return `{}`, nil
			}
		},
		RegisterCall: func(_ context.Context, _ buildspec.Spec, _, _, _ string, _ int) (string, error) {
			return strings.Repeat("a", 64), nil
		},
		AuditWrite: aud.write,
		EventEmit:  ev.emit,
		Now:        func() time.Time { return time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPipeline_HappyPath_ThreeStageRowsThreeEvents(t *testing.T) {
	aud := &recAudit{}
	ev := &recEvents{}
	p := newHappyPipe(t, aud, ev)

	outcomes, err := p.Run(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(outcomes) != 3 {
		t.Fatalf("outcomes len=%d want 3", len(outcomes))
	}
	for i, o := range outcomes {
		if !o.Success {
			t.Errorf("stage %d (%s) not success: %v", i, o.Stage, o.Error)
		}
	}
	if aud.count() != 3 {
		t.Fatalf("audit rows = %d, want 3", aud.count())
	}
	if len(ev.snapshot()) != 3 {
		t.Fatalf("events = %d, want 3", len(ev.snapshot()))
	}
	// Verify canonical stage order.
	rows := aud.snapshot()
	for i, want := range []Stage{StageScaffold, StageAcceptance, StageRegister} {
		if rows[i].Stage != string(want) {
			t.Errorf("row %d stage = %q, want %q", i, rows[i].Stage, want)
		}
	}
}

func TestPipeline_HardRejectsRegisterOnAcceptanceFail(t *testing.T) {
	aud := &recAudit{}
	ev := &recEvents{}
	registerCalled := false
	p, err := New(Deps{
		Delegate: func(_ context.Context, _, _, skill, _ string, _ int) (string, error) {
			if skill == "mcp-acceptance" {
				return `{"acceptance_exit_code":1}`, nil
			}
			return `{}`, nil
		},
		RegisterCall: func(_ context.Context, _ buildspec.Spec, _, _, _ string, _ int) (string, error) {
			registerCalled = true
			return "", nil
		},
		AuditWrite: aud.write,
		EventEmit:  ev.emit,
	})
	if err != nil {
		t.Fatal(err)
	}
	outcomes, err := p.Run(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if registerCalled {
		t.Fatal("register MUST NOT be called after acceptance fail — §7 (a)")
	}
	if len(outcomes) != 3 {
		t.Fatalf("outcomes len=%d want 3 (with skip)", len(outcomes))
	}
	if outcomes[0].Success != true || outcomes[1].Success != false || !outcomes[2].Skipped {
		t.Fatalf("stage shape wrong: %+v", outcomes)
	}
	// Audit rows: scaffold ok + acceptance fail = 2. Register was
	// skipped and MUST NOT produce a row.
	if aud.count() != 2 {
		t.Fatalf("audit rows = %d, want 2 (no skipped register row)", aud.count())
	}
}

func TestPipeline_AcceptanceGateAblation_StillInvokesAcceptance(t *testing.T) {
	aud := &recAudit{}
	ev := &recEvents{}
	acceptanceSkillCalled := false
	p, err := New(Deps{
		Delegate: func(_ context.Context, _, _, skill, _ string, _ int) (string, error) {
			if skill == "mcp-acceptance" {
				acceptanceSkillCalled = true
				return `{"acceptance_exit_code":1}`, nil // fail per-case
			}
			return `{}`, nil
		},
		RegisterCall: func(_ context.Context, _ buildspec.Spec, _, _, _ string, _ int) (string, error) {
			return strings.Repeat("b", 64), nil
		},
		AuditWrite:               aud.write,
		EventEmit:                ev.emit,
		IsAcceptanceGateDisabled: func() bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	outcomes, err := p.Run(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !acceptanceSkillCalled {
		t.Fatal("acceptance skill MUST be invoked even under NoAcceptanceGate — §7 (a)")
	}
	// All three succeed under the ablation.
	for i, o := range outcomes {
		if !o.Success {
			t.Errorf("stage %d (%s) should succeed under NoAcceptanceGate: %v", i, o.Stage, o.Error)
		}
	}
	if aud.count() != 3 {
		t.Fatalf("audit rows = %d, want 3", aud.count())
	}
}

func TestPipeline_UserPromotionPathAblation_RefusedAtBoundary(t *testing.T) {
	aud := &recAudit{}
	ev := &recEvents{}
	delegateCalled := false
	p, err := New(Deps{
		Delegate: func(_ context.Context, _, _, _, _ string, _ int) (string, error) {
			delegateCalled = true
			return `{}`, nil
		},
		RegisterCall:            func(_ context.Context, _ buildspec.Spec, _, _, _ string, _ int) (string, error) { return "", nil },
		AuditWrite:              aud.write,
		EventEmit:               ev.emit,
		IsPromotionPathDisabled: func() bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Run(context.Background(), validRequest())
	if !errors.Is(err, ErrPromotionPathDisabled) {
		t.Fatalf("want ErrPromotionPathDisabled, got %v", err)
	}
	if delegateCalled {
		t.Fatal("delegate MUST NOT be called under NoUserPromotionPath")
	}
	if aud.count() != 0 {
		t.Fatalf("audit rows = %d, want 0", aud.count())
	}
}

func TestPipeline_UserPromotionPathPredicateNil_RunsWithWarnLog(t *testing.T) {
	aud := &recAudit{}
	ev := &recEvents{}
	// Capture logs.
	var buf bytes.Buffer
	restoreW := log.Writer()
	restoreFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(restoreW); log.SetFlags(restoreFlags) })

	p := newHappyPipe(t, aud, ev)
	// The predicate is nil (Deps.IsPromotionPathDisabled unset).
	_, err := p.Run(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	logs := buf.String()
	wantLine := "[warn] NoUserPromotionPath predicate unwired — pipeline running unguarded until B1 lands"
	if !strings.Contains(logs, wantLine) {
		t.Fatalf("expected WARN line %q in logs; got:\n%s", wantLine, logs)
	}
	// Verify the once-only guard: run twice, second run does NOT
	// emit a second WARN.
	buf.Reset()
	if _, err := p.Run(context.Background(), validRequest()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), wantLine) {
		t.Fatalf("second run should not re-emit the WARN; got:\n%s", buf.String())
	}
}

func TestPipeline_DryRunRegisterAuditRowMarker(t *testing.T) {
	aud := &recAudit{}
	ev := &recEvents{}
	registerCalled := false
	p, err := New(Deps{
		Delegate: func(_ context.Context, _, _, skill, _ string, _ int) (string, error) {
			if skill == "mcp-acceptance" {
				return `{"acceptance_exit_code":0}`, nil
			}
			return `{}`, nil
		},
		RegisterCall: func(_ context.Context, _ buildspec.Spec, _, _, _ string, _ int) (string, error) {
			registerCalled = true
			return "", errors.New("register must not be called in dry-run")
		},
		AuditWrite: aud.write,
		EventEmit:  ev.emit,
	})
	if err != nil {
		t.Fatal(err)
	}
	r := validRequest()
	r.DryRunRegister = true
	outcomes, err := p.Run(context.Background(), r)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if registerCalled {
		t.Fatal("RegisterCall MUST NOT be invoked in dry-run mode")
	}
	// Stage-3 outcome: NOT success (didn't register), stage_note=dry_run.
	if outcomes[2].Success {
		t.Fatal("dry-run register outcome should have Success=false")
	}
	rows := aud.snapshot()
	if len(rows) != 3 {
		t.Fatalf("audit rows = %d, want 3", len(rows))
	}
	regRow := rows[2]
	if regRow.StageResult != promotionaudit.StageResultFail {
		t.Fatalf("register row stage_result = %q, want fail", regRow.StageResult)
	}
	if regRow.StageNote != "dry_run" {
		t.Fatalf("register row stage_note = %q, want dry_run", regRow.StageNote)
	}
	if regRow.RegistryHashAfter != emptyBytesSHA256Hex {
		t.Fatalf("register row registry_hash_after = %q, want empty-bytes sha256", regRow.RegistryHashAfter)
	}
}

func TestPipeline_DryRunTagsAllThreeStageRows(t *testing.T) {
	aud := &recAudit{}
	ev := &recEvents{}
	p := newHappyPipe(t, aud, ev)
	r := validRequest()
	r.DryRunRegister = true
	if _, err := p.Run(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	rows := aud.snapshot()
	if len(rows) != 3 {
		t.Fatalf("rows = %d want 3", len(rows))
	}
	for i, row := range rows {
		if row.StageNote != "dry_run" {
			t.Errorf("row %d (%s): stage_note = %q, want dry_run", i, row.Stage, row.StageNote)
		}
	}
}

func TestPipeline_ConsumerViewJoinKeyPresentOnAllStages(t *testing.T) {
	aud := &recAudit{}
	ev := &recEvents{}
	p := newHappyPipe(t, aud, ev)
	if _, err := p.Run(context.Background(), validRequest()); err != nil {
		t.Fatal(err)
	}
	rows := aud.snapshot()
	for i, row := range rows {
		if row.CandidateSourceTaskID != "task_12345678" {
			t.Errorf("row %d (%s): candidate_source_task_id mismatch", i, row.Stage)
		}
	}
}

func TestPipeline_EventPayloadDoesNotContainUserOrThreadID(t *testing.T) {
	aud := &recAudit{}
	ev := &recEvents{}
	p := newHappyPipe(t, aud, ev)
	if _, err := p.Run(context.Background(), validRequest()); err != nil {
		t.Fatal(err)
	}
	for _, e := range ev.snapshot() {
		body := string(e.Payload)
		if strings.Contains(body, "user_abcdef") {
			t.Fatalf("event payload leaks user_id: %s", body)
		}
		if strings.Contains(body, "thread_01_a") {
			t.Fatalf("event payload leaks thread_id: %s", body)
		}
	}
}

func TestPipeline_ProperlyReadsCasesPath_NoDirectoryTraversal(t *testing.T) {
	badPaths := []string{
		"../evil",
		"safe/../evil",
		"a/../b",
		"./..",
		"/etc/passwd",
		"%2e%2e/foo",
	}
	for _, bp := range badPaths {
		t.Run(bp, func(t *testing.T) {
			aud := &recAudit{}
			ev := &recEvents{}
			p := newHappyPipe(t, aud, ev)
			r := validRequest()
			r.CasesPath = bp
			_, err := p.Run(context.Background(), r)
			if !errors.Is(err, ErrInvalidCasesPath) {
				t.Fatalf("want ErrInvalidCasesPath for %q, got %v", bp, err)
			}
			if aud.count() != 0 {
				t.Fatalf("audit rows = %d for rejected path %q, want 0", aud.count(), bp)
			}
		})
	}
	// Good path.
	aud := &recAudit{}
	ev := &recEvents{}
	p := newHappyPipe(t, aud, ev)
	r := validRequest()
	r.CasesPath = "tests/eval/golden/csv-profiler/acceptance/cases.jsonl"
	if _, err := p.Run(context.Background(), r); err != nil {
		t.Fatalf("well-formed cases path rejected: %v", err)
	}
}

func TestPipeline_AuditWriteFailureDegradesButPipelineProceeds(t *testing.T) {
	failing := &recAudit{err: errors.New("audit blip")}
	ev := &recEvents{}
	registerCalled := false
	p, err := New(Deps{
		Delegate: func(_ context.Context, _, _, skill, _ string, _ int) (string, error) {
			if skill == "mcp-acceptance" {
				return `{"acceptance_exit_code":0}`, nil
			}
			return `{}`, nil
		},
		RegisterCall: func(_ context.Context, _ buildspec.Spec, _, _, _ string, _ int) (string, error) {
			registerCalled = true
			return strings.Repeat("c", 64), nil
		},
		AuditWrite: failing.write,
		EventEmit:  ev.emit,
	})
	if err != nil {
		t.Fatal(err)
	}
	outcomes, err := p.Run(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// All 3 outcomes should be success — audit failure did not stop
	// the pipeline.
	for i, o := range outcomes {
		if !o.Success {
			t.Errorf("stage %d (%s): expected success despite audit failure, got %v", i, o.Stage, o.Error)
		}
	}
	if !registerCalled {
		t.Fatal("register MUST run even when scaffold-stage audit-write failed")
	}
}

// TestEmptyBytesSHA256HexMatchesSHA256OfEmpty — pinning check so the
// duplicated constant in this package never drifts from the actual
// sha256 of the empty byte string. If this test fails, either
// (a) internal/driver.EmptyBytesSHA256Hex was changed (accidentally),
// or (b) the promotionpipeline local copy was changed independently.
// Either way the constant must match sha256("").
func TestEmptyBytesSHA256HexMatchesSHA256OfEmpty(t *testing.T) {
	// Recompute at runtime and compare — cheap, catches drift.
	h := sha256.Sum256(nil)
	want := hex.EncodeToString(h[:])
	if emptyBytesSHA256Hex != want {
		t.Fatalf("emptyBytesSHA256Hex drifted from sha256(\"\"): got %q, want %q", emptyBytesSHA256Hex, want)
	}
}

// TestPipeline_DryRunRegisterLogsPrefix — spec §7 (b). Guards
// against removal of the `[dry-run]` prefix in the stage-3 register
// log line (operator greps for it).
func TestPipeline_DryRunRegisterLogsPrefix(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(prev); log.SetFlags(prevFlags) })

	aud := &recAudit{}
	ev := &recEvents{}
	p := newHappyPipe(t, aud, ev)
	r := validRequest()
	r.DryRunRegister = true
	if _, err := p.Run(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "[dry-run] register skipped for spec="+r.Spec.Name) {
		t.Fatalf("expected [dry-run] log line, got:\n%s", buf.String())
	}
}

// TestPipeline_DryRunDoesNotUpdateRegistryView — spec §7 (b) /
// pipeline §2.4: dry-run must NOT publish a new
// driver.LastRegistryHash (that would pollute D1 for the eval
// runner).
//
// Because internal/promotionpipeline stays free of the
// internal/driver import (would cycle), the pipeline can't call
// driver.LastRegistryHash directly. The invariant is enforced
// STATICALLY: the pipeline's dry-run branch does not invoke
// RegisterCall, and RegisterCall is the only path that reaches
// driver.PublishRegisterAndCompute (which updates LastRegistryHash).
// So this test asserts that RegisterCall is NOT invoked in dry-run
// mode — an equivalent guarantee, structurally.
func TestPipeline_DryRunDoesNotUpdateRegistryView(t *testing.T) {
	aud := &recAudit{}
	ev := &recEvents{}
	registerCalled := false
	p, err := New(Deps{
		Delegate: func(_ context.Context, _, _, skill, _ string, _ int) (string, error) {
			if skill == "mcp-acceptance" {
				return `{"acceptance_exit_code":0}`, nil
			}
			return `{}`, nil
		},
		RegisterCall: func(_ context.Context, _ buildspec.Spec, _, _, _ string, _ int) (string, error) {
			registerCalled = true
			return strings.Repeat("h", 64), nil
		},
		AuditWrite: aud.write,
		EventEmit:  ev.emit,
	})
	if err != nil {
		t.Fatal(err)
	}
	r := validRequest()
	r.DryRunRegister = true
	if _, err := p.Run(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if registerCalled {
		t.Fatal("dry-run MUST NOT invoke RegisterCall — the only path that could update driver.LastRegistryHash")
	}
}

// TestPipeline_NoObserverAblation_AuditDroppedWithLogEventsUnaffected
// — spec §7 (j). Under NoObserver (flipped via
// evalrun.DisableTelemetry), the audit writer drops rows silently
// (each drop produces a `[ablation] NoObserver: dropped ...` log
// line — that log actually lives in the promotionaudit writer). The
// observer event pipe is SEPARATE and is not affected by the flag.
//
// The pipeline's Deps.AuditWrite is a caller-supplied function; the
// NoObserver semantics live in promotionaudit.SQLiteWriter, not in
// the pipeline itself. To test the pipeline's obligations here we
// simulate the drop by injecting an AuditWrite that returns nil
// (mirrors the writer's behavior under the ablation) and a normal
// EventEmit. Assert: 0 rows in the caller's capture, 3 events
// emitted.
func TestPipeline_NoObserverAblation_AuditDroppedWithLogEventsUnaffected(t *testing.T) {
	ev := &recEvents{}
	// AuditWrite silently drops (matches NoObserver semantics in the
	// real promotionaudit.SQLiteWriter — spec §7 (j) — the writer
	// returns nil after logging).
	var (
		dropped int
		mu      sync.Mutex
	)
	dropWrite := func(_ context.Context, _ promotionaudit.AuditFields) error {
		mu.Lock()
		defer mu.Unlock()
		dropped++
		return nil
	}
	p, err := New(Deps{
		Delegate: func(_ context.Context, _, _, skill, _ string, _ int) (string, error) {
			if skill == "mcp-acceptance" {
				return `{"acceptance_exit_code":0}`, nil
			}
			return `{}`, nil
		},
		RegisterCall: func(_ context.Context, _ buildspec.Spec, _, _, _ string, _ int) (string, error) {
			return strings.Repeat("i", 64), nil
		},
		AuditWrite: dropWrite,
		EventEmit:  ev.emit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Run(context.Background(), validRequest()); err != nil {
		t.Fatal(err)
	}
	// AuditWrite was called 3 times; each dropped silently.
	if dropped != 3 {
		t.Fatalf("expected AuditWrite called 3 times (dropped), got %d", dropped)
	}
	// Observer events unaffected — 3 stage-transition events.
	if got := len(ev.snapshot()); got != 3 {
		t.Fatalf("expected 3 observer events (NoObserver does not touch event pipe), got %d", got)
	}
}

// TestPipeline_ScaffoldSourcePath_RejectsAbsoluteAndTraversal — PR #71
// review P1-B2-1: a compromised scaffold slave returning an absolute
// or traversal-shaped source_path must NOT flow into RegisterCall.
func TestPipeline_ScaffoldSourcePath_RejectsAbsoluteAndTraversal(t *testing.T) {
	badPaths := []string{
		"/etc/shadow",
		"../../evil.py",
		"generated_mcp/../../etc/passwd",
		"%2e%2e/x.py",
		// PR #71 round-2 review P2-D: additional shapes.
		"foo\x00/evil.py",           // null byte
		"foo\n/etc/passwd",          // newline injection
		"C:\\evil.py",               // windows drive
		`\\server\share\evil.py`,    // UNC
		"safe/..\\evil.py",          // mixed forward + back
	}
	for _, bp := range badPaths {
		t.Run(bp, func(t *testing.T) {
			aud := &recAudit{}
			ev := &recEvents{}
			registerCalled := false
			p, err := New(Deps{
				Delegate: func(_ context.Context, _, _, skill, _ string, _ int) (string, error) {
					if skill == "scaffold-mcp-server" {
						return `{"source_path":"` + bp + `"}`, nil
					}
					if skill == "mcp-acceptance" {
						return `{"acceptance_exit_code":0}`, nil
					}
					return `{}`, nil
				},
				RegisterCall: func(_ context.Context, _ buildspec.Spec, _, _, _ string, _ int) (string, error) {
					registerCalled = true
					return "", nil
				},
				AuditWrite: aud.write,
				EventEmit:  ev.emit,
			})
			if err != nil {
				t.Fatal(err)
			}
			outcomes, err := p.Run(context.Background(), validRequest())
			if err != nil {
				t.Fatal(err)
			}
			if registerCalled {
				t.Fatalf("register MUST NOT run when scaffold returned untrusted source_path %q", bp)
			}
			if outcomes[2].Success {
				t.Fatalf("register stage should be marked failed for source_path %q", bp)
			}
			if !errors.Is(outcomes[2].Error, ErrInvalidScaffoldSourcePath) {
				t.Fatalf("expected ErrInvalidScaffoldSourcePath, got %v", outcomes[2].Error)
			}
		})
	}
}

// TestPipeline_AcceptanceMarkerInDebugStringDoesNotSpoof — regression
// guard: a slave whose debug/log field embeds the marker as substring
// inside another JSON string must NOT be mis-parsed as PASS. The
// parser is JSON-typed now, not substring-based. PR #71 round-2
// asked for wider attack-shape coverage (P2-D related).
func TestPipeline_AcceptanceMarkerInDebugStringDoesNotSpoof(t *testing.T) {
	spoofs := []struct {
		name string
		body string
	}{
		{"nested_string_kv", `{"debug":"acceptance_exit_code\":0 was returned by helper"}`},
		{"array_of_strings", `["acceptance_exit_code:0 in this string"]`},
		{"unrelated_key", `{"other_field":"nothing here","logs":"acceptance_exit_code=0 message"}`},
		{"non_json_text", `unparseable text acceptance_exit_code:0 xyz`},
		{"empty_body", ``},
		{"empty_object", `{}`},
	}
	for _, tc := range spoofs {
		t.Run(tc.name, func(t *testing.T) {
			aud := &recAudit{}
			ev := &recEvents{}
			registerCalled := false
			p, err := New(Deps{
				Delegate: func(_ context.Context, _, _, skill, _ string, _ int) (string, error) {
					if skill == "mcp-acceptance" {
						return tc.body, nil
					}
					return `{}`, nil
				},
				RegisterCall: func(_ context.Context, _ buildspec.Spec, _, _, _ string, _ int) (string, error) {
					registerCalled = true
					return "", nil
				},
				AuditWrite: aud.write,
				EventEmit:  ev.emit,
			})
			if err != nil {
				t.Fatal(err)
			}
			outcomes, err := p.Run(context.Background(), validRequest())
			if err != nil {
				t.Fatal(err)
			}
			if registerCalled {
				t.Fatalf("register MUST NOT run for spoof %q — §7 (a) C3 tool-poisoning surface", tc.name)
			}
			if outcomes[1].Success {
				t.Fatalf("acceptance stage should be marked failed for spoof %q", tc.name)
			}
		})
	}
}

// TestPipeline_AcceptanceMissingMarker_TreatsAsFail — spec §7 (a).
// A missing acceptance_exit_code marker is HARD-FAIL, not silent-pass.
func TestPipeline_AcceptanceMissingMarker_TreatsAsFail(t *testing.T) {
	aud := &recAudit{}
	ev := &recEvents{}
	registerCalled := false
	p, err := New(Deps{
		Delegate: func(_ context.Context, _, _, skill, _ string, _ int) (string, error) {
			if skill == "mcp-acceptance" {
				return `{"something_else": true}`, nil // no acceptance_exit_code
			}
			return `{}`, nil
		},
		RegisterCall: func(_ context.Context, _ buildspec.Spec, _, _, _ string, _ int) (string, error) {
			registerCalled = true
			return "", nil
		},
		AuditWrite: aud.write,
		EventEmit:  ev.emit,
	})
	if err != nil {
		t.Fatal(err)
	}
	outcomes, err := p.Run(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if registerCalled {
		t.Fatal("register MUST NOT run when acceptance marker is missing — §7 (a)")
	}
	if outcomes[1].Success {
		t.Fatal("acceptance stage should be marked failed when marker missing")
	}
}

// TestPipeline_AcceptanceMissingMarker_BypassedUnderNoAcceptanceGate
// — under the ablation, missing marker is bypassed with a log line.
func TestPipeline_AcceptanceMissingMarker_BypassedUnderNoAcceptanceGate(t *testing.T) {
	aud := &recAudit{}
	ev := &recEvents{}
	registerCalled := false
	p, err := New(Deps{
		Delegate: func(_ context.Context, _, _, skill, _ string, _ int) (string, error) {
			if skill == "mcp-acceptance" {
				return `{}`, nil
			}
			return `{}`, nil
		},
		RegisterCall: func(_ context.Context, _ buildspec.Spec, _, _, _ string, _ int) (string, error) {
			registerCalled = true
			return strings.Repeat("e", 64), nil
		},
		AuditWrite:               aud.write,
		EventEmit:                ev.emit,
		IsAcceptanceGateDisabled: func() bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Run(context.Background(), validRequest()); err != nil {
		t.Fatal(err)
	}
	if !registerCalled {
		t.Fatal("register should run under NoAcceptanceGate even with missing marker")
	}
}

// TestPipeline_UsesSourcePathFromScaffoldResponse — spec §2.2 stage 1
// "record source_path from slave result body".
func TestPipeline_UsesSourcePathFromScaffoldResponse(t *testing.T) {
	aud := &recAudit{}
	ev := &recEvents{}
	var registerSourcePath string
	p, err := New(Deps{
		Delegate: func(_ context.Context, _, _, skill, _ string, _ int) (string, error) {
			if skill == "scaffold-mcp-server" {
				return `{"source_path":"custom/path/server.js"}`, nil
			}
			if skill == "mcp-acceptance" {
				return `{"acceptance_exit_code":0}`, nil
			}
			return `{}`, nil
		},
		RegisterCall: func(_ context.Context, _ buildspec.Spec, sp, _, _ string, _ int) (string, error) {
			registerSourcePath = sp
			return strings.Repeat("f", 64), nil
		},
		AuditWrite: aud.write,
		EventEmit:  ev.emit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Run(context.Background(), validRequest()); err != nil {
		t.Fatal(err)
	}
	if registerSourcePath != "custom/path/server.js" {
		t.Fatalf("register received source_path = %q, want scaffold-supplied path", registerSourcePath)
	}
}

// TestPipeline_FallbackSourcePathWhenScaffoldOmitsIt — scaffold that
// does not surface source_path → fall back to convention + warn.
func TestPipeline_FallbackSourcePathWhenScaffoldOmitsIt(t *testing.T) {
	aud := &recAudit{}
	ev := &recEvents{}
	var registerSourcePath string
	p, err := New(Deps{
		Delegate: func(_ context.Context, _, _, skill, _ string, _ int) (string, error) {
			if skill == "mcp-acceptance" {
				return `{"acceptance_exit_code":0}`, nil
			}
			return `{}`, nil // no source_path in scaffold response
		},
		RegisterCall: func(_ context.Context, _ buildspec.Spec, sp, _, _ string, _ int) (string, error) {
			registerSourcePath = sp
			return strings.Repeat("g", 64), nil
		},
		AuditWrite: aud.write,
		EventEmit:  ev.emit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Run(context.Background(), validRequest()); err != nil {
		t.Fatal(err)
	}
	// Fallback shape from pipeline.go: generated_mcp/<name>/server.py
	if !strings.HasPrefix(registerSourcePath, "generated_mcp/") {
		t.Fatalf("register received source_path = %q, want fallback under generated_mcp/", registerSourcePath)
	}
}

func TestPipeline_TimeoutIsPerStageNotTotal(t *testing.T) {
	// The pipeline passes timeout_sec verbatim to each Delegate call,
	// so a 500ms scaffold + 500ms acceptance + 500ms register total 1.5s
	// still complete when timeout_sec=1 (interpreted as "per-stage").
	aud := &recAudit{}
	ev := &recEvents{}
	p, err := New(Deps{
		Delegate: func(_ context.Context, _, _, skill, _ string, ts int) (string, error) {
			if ts != 1 {
				return "", fmt.Errorf("timeout_sec should be 1 per stage, got %d", ts)
			}
			time.Sleep(50 * time.Millisecond) // simulate work; well under per-stage cap
			if skill == "mcp-acceptance" {
				return `{"acceptance_exit_code":0}`, nil
			}
			return `{}`, nil
		},
		RegisterCall: func(_ context.Context, _ buildspec.Spec, _, _, _ string, ts int) (string, error) {
			if ts != 1 {
				return "", fmt.Errorf("register timeout_sec = %d, want 1", ts)
			}
			time.Sleep(50 * time.Millisecond)
			return strings.Repeat("d", 64), nil
		},
		AuditWrite: aud.write,
		EventEmit:  ev.emit,
	})
	if err != nil {
		t.Fatal(err)
	}
	r := validRequest()
	r.TimeoutSec = 1
	outcomes, err := p.Run(context.Background(), r)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, o := range outcomes {
		if !o.Success {
			t.Errorf("stage %s failed: %v", o.Stage, o.Error)
		}
	}
}
