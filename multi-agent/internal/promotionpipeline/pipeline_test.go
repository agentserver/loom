package promotionpipeline

import (
	"bytes"
	"context"
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
