package probes

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLabels(t *testing.T, workloadRoot, id, gt string) {
	t.Helper()
	dir := filepath.Join(workloadRoot, "labels", "workloads")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".labels.json"),
		[]byte(`{"ground_truth_context":"`+gt+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeSelected(t *testing.T, ws, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(ws, ".probes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".probes/selected_context.txt"), []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestEmitWrongContext_StructuralOnlyFail(t *testing.T) {
	ws, wl := t.TempDir(), t.TempDir()
	writeSelected(t, ws, "ctxA")
	writeLabels(t, wl, "wl-1", "ctxB")
	e := NewEmitter(0, io.Discard)
	EmitWrongContext(context.Background(), e, ws, wl, "wl-1",
		OracleOutput{Passed: false, MetricsJSON: `{}`}, io.Discard)
	recs, _ := e.Close()
	if len(recs) != 1 || recs[0].Value != true {
		t.Fatalf("record: %#v", recs)
	}
	if recs[0].Labels["oracle_failure_class_source"] != "fallback_structural_only" {
		t.Fatalf("label: %#v", recs[0].Labels)
	}
}

func TestEmitWrongContext_OracleClassifiedFail(t *testing.T) {
	ws, wl := t.TempDir(), t.TempDir()
	writeSelected(t, ws, "ctxA")
	writeLabels(t, wl, "wl-1", "ctxB")
	e := NewEmitter(0, io.Discard)
	EmitWrongContext(context.Background(), e, ws, wl, "wl-1",
		OracleOutput{Passed: false, MetricsJSON: `{"failure_class":"missing_tool"}`}, io.Discard)
	recs, _ := e.Close()
	if len(recs) != 1 || recs[0].Value != true {
		t.Fatalf("record: %#v", recs)
	}
	if recs[0].Labels["oracle_failure_class"] != "missing_tool" ||
		recs[0].Labels["oracle_failure_class_source"] != "oracle_metrics" {
		t.Fatalf("labels: %#v", recs[0].Labels)
	}
}

func TestEmitWrongContext_PassShortCircuitsToFalse(t *testing.T) {
	ws, wl := t.TempDir(), t.TempDir()
	writeSelected(t, ws, "ctxA")
	writeLabels(t, wl, "wl-1", "ctxB")
	e := NewEmitter(0, io.Discard)
	EmitWrongContext(context.Background(), e, ws, wl, "wl-1",
		OracleOutput{Passed: true, MetricsJSON: `{}`}, io.Discard)
	recs, _ := e.Close()
	if len(recs) != 1 || recs[0].Value != false {
		t.Fatalf("passing run should be false, got %#v", recs)
	}
}

func TestEmitWrongContext_MissingSelectedFile(t *testing.T) {
	ws, wl := t.TempDir(), t.TempDir()
	writeLabels(t, wl, "wl-1", "ctxB")
	e := NewEmitter(0, io.Discard)
	EmitWrongContext(context.Background(), e, ws, wl, "wl-1",
		OracleOutput{Passed: false, MetricsJSON: `{}`}, io.Discard)
	recs, _ := e.Close()
	if recs[0].Value != nil || recs[0].Labels["unavailable_reason"] != "no_selected_context_file" {
		t.Fatalf("%#v", recs[0])
	}
}

func TestEmitWrongContext_MissingGroundTruthLabels(t *testing.T) {
	ws, wl := t.TempDir(), t.TempDir()
	writeSelected(t, ws, "ctxA")
	e := NewEmitter(0, io.Discard)
	EmitWrongContext(context.Background(), e, ws, wl, "wl-1",
		OracleOutput{Passed: false, MetricsJSON: `{}`}, io.Discard)
	recs, _ := e.Close()
	if recs[0].Value != nil || recs[0].Labels["unavailable_reason"] != "no_ground_truth_labels" {
		t.Fatalf("%#v", recs[0])
	}
}

func TestEmitWrongContext_SanitizesContextStrings(t *testing.T) {
	ws, wl := t.TempDir(), t.TempDir()
	writeSelected(t, ws, "srv-a sk-ABCDEFGHIJKLMNOP")
	writeLabels(t, wl, "wl-1", "ctxB")
	e := NewEmitter(0, io.Discard)
	EmitWrongContext(context.Background(), e, ws, wl, "wl-1",
		OracleOutput{Passed: false, MetricsJSON: `{}`}, io.Discard)
	recs, _ := e.Close()
	if !strings.Contains(recs[0].Labels["selected"], "[REDACTED]") {
		t.Fatalf("selected label not sanitized: %q", recs[0].Labels["selected"])
	}
}
