package harness

import (
	"bytes"
	"testing"
)

func TestValidateOpts_RequiresWorkload(t *testing.T) {
	var stderr bytes.Buffer
	opts := &Opts{OutCSV: "/tmp/x.csv", BaselineName: "manual_ssh"}
	if code := ValidateOpts(opts, &stderr); code != 2 {
		t.Errorf("want exit 2 for missing workload, got %d", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("--workload")) {
		t.Errorf("stderr should mention --workload; got %q", stderr.String())
	}
}

func TestValidateOpts_RequiresOut(t *testing.T) {
	var stderr bytes.Buffer
	opts := &Opts{WorkloadID: "cross-device-code-mod", BaselineName: "manual_ssh"}
	if code := ValidateOpts(opts, &stderr); code != 2 {
		t.Errorf("want exit 2 for missing --out, got %d", code)
	}
}

func TestValidateOpts_RejectsInvalidBaselineName(t *testing.T) {
	var stderr bytes.Buffer
	opts := &Opts{
		WorkloadID:   "cross-device-code-mod",
		OutCSV:       "/tmp/x.csv",
		BaselineName: "BAD NAME",
	}
	if code := ValidateOpts(opts, &stderr); code != 2 {
		t.Errorf("want exit 2 for bad baseline name, got %d", code)
	}
}

func TestNewFlagSet_ParsesWorkloadArg(t *testing.T) {
	opts, fs := NewFlagSet("test", &bytes.Buffer{})
	if err := fs.Parse([]string{"--workload", "cross-device-code-mod", "--out", "/tmp/x.csv", "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if opts.WorkloadID != "cross-device-code-mod" {
		t.Errorf("workload not captured: %q", opts.WorkloadID)
	}
	if !opts.DryRun {
		t.Errorf("dry-run not captured")
	}
	if opts.OutCSV != "/tmp/x.csv" {
		t.Errorf("out not captured: %q", opts.OutCSV)
	}
}
