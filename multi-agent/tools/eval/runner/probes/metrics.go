package probes

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// OracleOutput is the probes-package projection of the runner's parsed
// oracle stdout. Runner passes fields into this at spec §5.1 Edit 3.
// Kept as a plain struct (not an interface) so the probes package
// has no back-import into the runner's `package main`.
type OracleOutput struct {
	Passed      bool
	MetricsJSON string
	ExitCode    int
	StdoutBytes int
}

// EmitOracleMetrics emits TaskSuccessRate, LifecycleClosureRate,
// ArtifactCorrectnessRate per spec §3.3 rows #1/#2/#6. Callers invoke
// this exactly once per run, right after parseOracleStdout.
func EmitOracleMetrics(ctx context.Context, e *Emitter, out OracleOutput) {
	// #1 TaskSuccessRate
	_ = e.Emit(ctx, MetricTaskSuccessRate, out.Passed, map[string]string{
		"oracle_exit_code":    strconv.Itoa(out.ExitCode),
		"oracle_stdout_bytes": strconv.Itoa(out.StdoutBytes),
	})
	// Parse metrics JSON best-effort — missing / malformed → fallback.
	var m map[string]any
	if out.MetricsJSON != "" {
		_ = json.Unmarshal([]byte(out.MetricsJSON), &m)
	}
	emitBoolWithFallback(ctx, e, MetricLifecycleClosureRate, "lifecycle_closed", m, out.Passed)
	emitBoolWithFallback(ctx, e, MetricArtifactCorrectnessRate, "artifact_correct", m, out.Passed)
}

func emitBoolWithFallback(ctx context.Context, e *Emitter, metric MetricKey, key string, m map[string]any, fallback bool) {
	if raw, ok := m[key]; ok {
		if b, isBool := raw.(bool); isBool {
			_ = e.Emit(ctx, metric, b, map[string]string{"source": "oracle_metrics"})
			return
		}
	}
	_ = e.Emit(ctx, metric, fallback, map[string]string{"source": "fallback_passed"})
}

// EmitTimeToCompletion emits the monotonic delta since startedAt.
// Uses time.Since which subtracts monotonic readings and is immune
// to wall-clock jumps (§7(c)).
func EmitTimeToCompletion(ctx context.Context, e *Emitter, startedAt time.Time) {
	dur := time.Since(startedAt).Nanoseconds()
	_ = e.Emit(ctx, MetricTimeToCompletion, dur, map[string]string{
		"wall_start_unix": strconv.FormatInt(startedAt.Unix(), 10),
		"wall_end_unix":   strconv.FormatInt(time.Now().Unix(), 10),
	})
}

// RunRowLike is implemented by writer.go's *RunRow (Task 6) — this
// interface keeps the probes package free of a back-import into the
// runner's `package main`.
type RunRowLike interface {
	SetProbe(metric MetricKey, value any)
	SetProbeNotes(json string)
}

// MergeIntoRow folds probe records into the row + serialises the
// labels-carrying subset into probe_notes_json. Last write wins on
// duplicate metrics; row implementations MAY log the collision.
// Returns any encoder warnings (empty on the happy path).
func MergeIntoRow(row RunRowLike, records []Record) []string {
	if row == nil {
		return nil
	}
	var warns []string
	notes := map[string]map[string]string{}
	for _, r := range records {
		row.SetProbe(r.Metric, r.Value)
		if len(r.Labels) > 0 {
			notes[string(r.Metric)] = r.Labels
		}
	}
	if len(notes) == 0 {
		row.SetProbeNotes("{}")
		return warns
	}
	b, err := json.Marshal(notes)
	if err != nil {
		// JSON of map[string]map[string]string cannot fail via
		// user data — this is defensive against encoder bugs.
		row.SetProbeNotes("{}")
		warns = append(warns, fmt.Sprintf("probes: json.Marshal notes failed: %v", err))
		return warns
	}
	row.SetProbeNotes(string(b))
	return warns
}
