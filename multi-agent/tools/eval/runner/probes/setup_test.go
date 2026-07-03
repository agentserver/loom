package probes

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmitSetupMetrics_AbsentFile(t *testing.T) {
	ws := t.TempDir()
	e := NewEmitter(0, io.Discard)
	EmitSetupMetrics(context.Background(), e, ws, io.Discard)
	recs, _ := e.Close()
	if len(recs) != 2 {
		t.Fatalf("want 2 records, got %d", len(recs))
	}
	for _, r := range recs {
		if r.Value != nil {
			t.Fatalf("%s value not nil: %v", r.Metric, r.Value)
		}
		if r.Labels["unavailable_reason"] != "d6c_setup_harness_pending" {
			t.Fatalf("%s label wrong: %#v", r.Metric, r.Labels)
		}
	}
}

func TestEmitSetupMetrics_MalformedFile(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".probes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".probes/setup.json"),
		[]byte(`{"manual_setup_step_count":"three"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf synchronizedBuf
	// Warns flow through the emitter's stderr (§7(a)); the trailing
	// io.Writer arg is a deprecated pass-through.
	e := NewEmitter(0, &buf)
	EmitSetupMetrics(context.Background(), e, ws, io.Discard)
	recs, _ := e.Close()
	for _, r := range recs {
		if r.Value != nil || r.Labels["unavailable_reason"] != "malformed_setup_file" {
			t.Fatalf("%s: %#v", r.Metric, r)
		}
	}
	if !strings.Contains(buf.String(), "setup") {
		t.Fatalf("stderr warn missing: %q", buf.String())
	}
}

func TestEmitSetupMetrics_WellFormed(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".probes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".probes/setup.json"),
		[]byte(`{"manual_setup_step_count":3,"config_touch_count":5}`), 0o644); err != nil {
		t.Fatal(err)
	}
	e := NewEmitter(0, io.Discard)
	EmitSetupMetrics(context.Background(), e, ws, io.Discard)
	recs, _ := e.Close()
	got := map[MetricKey]any{}
	for _, r := range recs {
		got[r.Metric] = r.Value
	}
	if got[MetricManualSetupStepCount] != 3 || got[MetricConfigTouchCount] != 5 {
		t.Fatalf("values wrong: %#v", got)
	}
	for _, r := range recs {
		if r.Labels["source"] != "setup_counter_file" {
			t.Fatalf("%s source: %s", r.Metric, r.Labels["source"])
		}
	}
}
