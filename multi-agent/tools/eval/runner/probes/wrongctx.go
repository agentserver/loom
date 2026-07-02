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
// absent.
func EmitWrongContext(ctx context.Context, e *Emitter, wsRoot, workloadRoot, workloadID string, out OracleOutput, stderr io.Writer) {
	if stderr == nil {
		stderr = io.Discard
	}
	selected, selOK := readSmallFile(filepath.Join(wsRoot, selectedCtxFile), selectedCtxMaxBytes, stderr)
	if !selOK {
		_ = e.Emit(ctx, MetricWrongContextFailureRate, nil, map[string]string{
			"unavailable_reason": "no_selected_context_file",
		})
		return
	}
	gt, gtOK := readGroundTruth(workloadRoot, workloadID, stderr)
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

func readSmallFile(path string, cap int, stderr io.Writer) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	buf := make([]byte, cap+1)
	read, err := io.ReadFull(f, buf)
	switch err {
	case nil:
		fmt.Fprintf(stderr, "probes: %s exceeds %d bytes\n", path, cap)
		return "", false
	case io.ErrUnexpectedEOF, io.EOF:
		buf = buf[:read]
	default:
		fmt.Fprintf(stderr, "probes: %s read: %v\n", path, err)
		return "", false
	}
	line := string(buf)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	return strings.TrimSpace(line), true
}

func readGroundTruth(workloadRoot, workloadID string, stderr io.Writer) (string, bool) {
	path := filepath.Join(workloadRoot, "labels", "workloads", workloadID+".labels.json")
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	buf := make([]byte, labelsMaxBytes+1)
	read, err := io.ReadFull(f, buf)
	switch err {
	case nil:
		fmt.Fprintf(stderr, "probes: labels %s exceeds %d bytes\n", path, labelsMaxBytes)
		return "", false
	case io.ErrUnexpectedEOF, io.EOF:
		buf = buf[:read]
	default:
		fmt.Fprintf(stderr, "probes: labels %s read: %v\n", path, err)
		return "", false
	}
	var m map[string]any
	if err := json.Unmarshal(buf, &m); err != nil {
		fmt.Fprintf(stderr, "probes: labels %s unmarshal: %v\n", path, err)
		return "", false
	}
	if gt, ok := m["ground_truth_context"].(string); ok {
		return gt, true
	}
	return "", false
}
