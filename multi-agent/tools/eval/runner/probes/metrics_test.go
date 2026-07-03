package probes

import (
	"context"
	"io"
	"testing"
	"time"
)

func TestEmitOracleMetrics_TaskSuccessRate(t *testing.T) {
	e := NewEmitter(0, io.Discard)
	EmitOracleMetrics(context.Background(), e, OracleOutput{
		Passed: true, ExitCode: 0, StdoutBytes: 42,
		MetricsJSON: `{}`,
	})
	recs, _ := e.Close()
	got := findRecord(recs, MetricTaskSuccessRate)
	if got == nil {
		t.Fatal("no task_success_rate record")
	}
	if got.Value != true {
		t.Fatalf("value = %v, want true", got.Value)
	}
	if got.Labels["oracle_exit_code"] != "0" || got.Labels["oracle_stdout_bytes"] != "42" {
		t.Fatalf("labels missing/wrong: %#v", got.Labels)
	}
}

func TestEmitOracleMetrics_LifecycleClosureRate_FromOracle(t *testing.T) {
	e := NewEmitter(0, io.Discard)
	EmitOracleMetrics(context.Background(), e, OracleOutput{
		Passed: true, MetricsJSON: `{"lifecycle_closed": false}`,
	})
	recs, _ := e.Close()
	got := findRecord(recs, MetricLifecycleClosureRate)
	if got == nil || got.Value != false || got.Labels["source"] != "oracle_metrics" {
		t.Fatalf("wrong lifecycle record: %#v", got)
	}
}

func TestEmitOracleMetrics_LifecycleClosureRate_Fallback(t *testing.T) {
	e := NewEmitter(0, io.Discard)
	EmitOracleMetrics(context.Background(), e, OracleOutput{
		Passed: true, MetricsJSON: `{}`,
	})
	recs, _ := e.Close()
	got := findRecord(recs, MetricLifecycleClosureRate)
	if got == nil || got.Value != true || got.Labels["source"] != "fallback_passed" {
		t.Fatalf("wrong fallback lifecycle: %#v", got)
	}
}

func TestEmitOracleMetrics_ArtifactCorrectness_FromOracle(t *testing.T) {
	e := NewEmitter(0, io.Discard)
	EmitOracleMetrics(context.Background(), e, OracleOutput{
		Passed: false, MetricsJSON: `{"artifact_correct": false}`,
	})
	recs, _ := e.Close()
	got := findRecord(recs, MetricArtifactCorrectnessRate)
	if got == nil || got.Value != false || got.Labels["source"] != "oracle_metrics" {
		t.Fatalf("wrong artifact record: %#v", got)
	}
}

func TestEmitOracleMetrics_ArtifactCorrectness_Fallback(t *testing.T) {
	e := NewEmitter(0, io.Discard)
	EmitOracleMetrics(context.Background(), e, OracleOutput{
		Passed: false, MetricsJSON: `{}`,
	})
	recs, _ := e.Close()
	got := findRecord(recs, MetricArtifactCorrectnessRate)
	if got == nil || got.Value != false || got.Labels["source"] != "fallback_passed" {
		t.Fatalf("wrong artifact fallback: %#v", got)
	}
}

func TestEmitTimeToCompletion_Monotonic(t *testing.T) {
	startedAt := time.Now().Add(-5 * time.Second) // 5 s ago (carries monotonic reading)
	e := NewEmitter(0, io.Discard)
	EmitTimeToCompletion(context.Background(), e, startedAt)
	recs, _ := e.Close()
	got := findRecord(recs, MetricTimeToCompletion)
	if got == nil {
		t.Fatal("no time_to_completion record")
	}
	v, ok := got.Value.(int64)
	if !ok {
		t.Fatalf("value type %T, want int64", got.Value)
	}
	// 5 s ± 100 %
	if v < int64(5*time.Second) || v > int64(10*time.Second) {
		t.Fatalf("duration %d ns out of expected range", v)
	}
	if got.Labels["wall_start_unix"] == "" || got.Labels["wall_end_unix"] == "" {
		t.Fatalf("wall labels missing: %#v", got.Labels)
	}
}

// MergeIntoRow test uses a captured stub of RunRowLike so this Task
// stays independent of writer.go changes (which land in Task 6).
type fakeRow struct {
	metrics map[MetricKey]any
	notes   string
	warns   int
}

func (f *fakeRow) SetProbe(m MetricKey, v any) {
	if f.metrics == nil {
		f.metrics = map[MetricKey]any{}
	}
	if _, dup := f.metrics[m]; dup {
		f.warns++
	}
	f.metrics[m] = v
}
func (f *fakeRow) SetProbeNotes(json string) { f.notes = json }

func TestMergeIntoRow_LastWinsOnDuplicate(t *testing.T) {
	e := NewEmitter(0, io.Discard)
	_ = e.Emit(context.Background(), MetricTaskSuccessRate, true, nil)
	_ = e.Emit(context.Background(), MetricTaskSuccessRate, false, nil)
	recs, _ := e.Close()
	r := &fakeRow{}
	_ = MergeIntoRow(r, recs)
	if r.metrics[MetricTaskSuccessRate] != false {
		t.Fatalf("last-write did not win: %v", r.metrics[MetricTaskSuccessRate])
	}
}

func findRecord(recs []Record, m MetricKey) *Record {
	for i := range recs {
		if recs[i].Metric == m {
			return &recs[i]
		}
	}
	return nil
}
