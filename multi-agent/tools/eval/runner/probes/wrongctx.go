package probes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const selectedCtxFile = ".probes/selected_context.txt"
const selectedCtxMaxBytes = 512
const labelsMaxBytes = 8 * 1024

var wrongClasses = map[string]bool{
	"missing_file":        true,
	"missing_tool":        true,
	"wrong_os":            true,
	"missing_credential":  true,
	"network_unreachable": true,
}

// EmitWrongContext emits MetricWrongContextFailureRate per spec §3.6.
// Never blocks; returns nil-value with an unavailable_reason label when
// either the selected-context file or the ground-truth labels file is
// absent. Diagnostics go through emitter.Warn (§7(a)).
//
// labelsDir is the parent directory of `workloads/<id>.labels.json`
// (§F4 label tree). The runner constructs it from the workload dir's
// sibling `labels/` — kept as a separate parameter so this helper
// stays testable in isolation without assuming a filesystem layout.
//
// The trailing `_ io.Writer` parameter is retained for call-site
// symmetry with EmitSetupMetrics / EmitHumanCount.
func EmitWrongContext(ctx context.Context, e *Emitter, wsRoot, labelsDir, workloadID string, out OracleOutput, _ io.Writer) {
	selected, selOK := readSmallFile(e, filepath.Join(wsRoot, selectedCtxFile), selectedCtxMaxBytes)
	if !selOK {
		_ = e.Emit(ctx, MetricWrongContextFailureRate, nil, map[string]string{
			"unavailable_reason": "no_selected_context_file",
		})
		return
	}
	gt, gtOK := readGroundTruth(e, labelsDir, workloadID)
	if !gtOK {
		_ = e.Emit(ctx, MetricWrongContextFailureRate, nil, map[string]string{
			"unavailable_reason": "no_ground_truth_labels",
		})
		return
	}
	labels := map[string]string{
		"selected":     selected,
		"ground_truth": gt,
	}
	// Rule #1: a passing run cannot be a wrong-context failure.
	if out.Passed {
		labels["oracle_failure_class_source"] = "n/a_passed"
		_ = e.Emit(ctx, MetricWrongContextFailureRate, false, labels)
		return
	}
	// Rule #2: structural signal — selected == gt means the context
	// choice was right; failure came from somewhere else.
	if selected == gt {
		labels["oracle_failure_class_source"] = "n/a_selected_equals_gt"
		_ = e.Emit(ctx, MetricWrongContextFailureRate, false, labels)
		return
	}
	// Rule #3: oracle-side classification (when available).
	var m map[string]any
	if out.MetricsJSON != "" {
		_ = json.Unmarshal([]byte(out.MetricsJSON), &m)
	}
	if fc, ok := m["failure_class"].(string); ok && fc != "" {
		labels["oracle_failure_class"] = fc
		labels["oracle_failure_class_source"] = "oracle_metrics"
		// Only wrong-context classes count as wrong-context failures.
		_ = e.Emit(ctx, MetricWrongContextFailureRate, wrongClasses[fc], labels)
		return
	}
	labels["oracle_failure_class_source"] = "fallback_structural_only"
	_ = e.Emit(ctx, MetricWrongContextFailureRate, true, labels)
}

func readSmallFile(e *Emitter, path string, cap int) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	buf := make([]byte, cap+1)
	read, err := io.ReadFull(f, buf)
	switch err {
	case nil:
		e.Warn(fmt.Sprintf("probes: %s exceeds %d bytes", path, cap))
		return "", false
	case io.ErrUnexpectedEOF, io.EOF:
		buf = buf[:read]
	default:
		e.Warn(fmt.Sprintf("probes: %s read: %v", path, err))
		return "", false
	}
	line := string(buf)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	return strings.TrimSpace(line), true
}

// readGroundTruth reads `<labelsDir>/workloads/<workloadID>.labels.json`
// and returns the `ground_truth_context.context_id` string (matches
// the §F4 schema in `tests/eval/labels/workloads/*.labels.json`, e.g.
// `{"ground_truth_context": {"agent_role": "...", "context_id": "..."}}`).
// Backward-compat: if the top-level `ground_truth_context` is a bare
// string (older draft), that is accepted verbatim.
func readGroundTruth(e *Emitter, labelsDir, workloadID string) (string, bool) {
	path := filepath.Join(labelsDir, "workloads", workloadID+".labels.json")
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	buf := make([]byte, labelsMaxBytes+1)
	read, err := io.ReadFull(f, buf)
	switch err {
	case nil:
		e.Warn(fmt.Sprintf("probes: labels %s exceeds %d bytes", path, labelsMaxBytes))
		return "", false
	case io.ErrUnexpectedEOF, io.EOF:
		buf = buf[:read]
	default:
		e.Warn(fmt.Sprintf("probes: labels %s read: %v", path, err))
		return "", false
	}
	var m map[string]any
	if err := json.Unmarshal(buf, &m); err != nil {
		e.Warn(fmt.Sprintf("probes: labels %s unmarshal: %v", path, err))
		return "", false
	}
	raw, ok := m["ground_truth_context"]
	if !ok {
		return "", false
	}
	// Preferred shape: object with .context_id string (F4 schema).
	if obj, ok := raw.(map[string]any); ok {
		if cid, ok := obj["context_id"].(string); ok && cid != "" {
			return cid, true
		}
		return "", false
	}
	// Back-compat: bare string.
	if s, ok := raw.(string); ok && s != "" {
		return s, true
	}
	return "", false
}
