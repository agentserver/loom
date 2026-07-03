package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSmoke_WorkloadRun_EightMetricsPresent replays the maintainer
// smoke path from spec §8: run cross-device-code-mod through Run and
// assert all nine probe columns appear + oracle-derived metrics are
// non-empty + D6c-dependent metrics carry the unavailable_reason
// label.
func TestSmoke_WorkloadRun_EightMetricsPresent(t *testing.T) {
	if testing.Short() {
		t.Skip("smoke test skipped in -short mode")
	}
	workloadDir := findWorkloadsDir(t)
	tmp := t.TempDir()
	csvPath := filepath.Join(tmp, "run.csv")
	port := freeLoopbackPort(t)

	opts := Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: workloadDir,
		StubListen:  port,
		OutCSV:      csvPath,
		Timeout:     60 * time.Second,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	result := Run(ctx, opts)
	if result.ExitCode != 0 {
		t.Fatalf("Run exit=%d err=%v", result.ExitCode, result.Err)
	}

	f, err := os.Open(csvPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r := csv.NewReader(f)
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("row count = %d, want 2", len(rows))
	}
	header, data := rows[0], rows[1]
	col := func(name string) string {
		for i, h := range header {
			if h == name {
				return data[i]
			}
		}
		t.Fatalf("column %s missing", name)
		return ""
	}
	// Nine probe columns present.
	for _, name := range []string{
		"probe_task_success_rate", "probe_lifecycle_closure_rate",
		"probe_time_to_completion_ns", "probe_human_context_selection_count",
		"probe_wrong_context_failure_rate", "probe_artifact_correctness_rate",
		"probe_manual_setup_step_count", "probe_config_touch_count",
		"probe_notes_json",
	} {
		_ = col(name) // fails via col() if missing
	}
	// Oracle-derived metrics populated.
	if col("probe_task_success_rate") != "true" {
		t.Fatalf("task success = %q", col("probe_task_success_rate"))
	}
	if col("probe_lifecycle_closure_rate") != "true" {
		t.Fatalf("lifecycle = %q", col("probe_lifecycle_closure_rate"))
	}
	if col("probe_artifact_correctness_rate") != "true" {
		t.Fatalf("artifact = %q", col("probe_artifact_correctness_rate"))
	}
	// D6c-dependent metrics empty + notes carry unavailable_reason.
	if col("probe_manual_setup_step_count") != "" || col("probe_config_touch_count") != "" {
		t.Fatalf("manual/config not empty: %q %q",
			col("probe_manual_setup_step_count"), col("probe_config_touch_count"))
	}
	var notes map[string]map[string]string
	if err := json.Unmarshal([]byte(col("probe_notes_json")), &notes); err != nil {
		t.Fatalf("notes JSON: %v", err)
	}
	if notes["manual_setup_step_count"]["unavailable_reason"] != "d6c_setup_harness_pending" {
		t.Fatalf("manual notes: %v", notes["manual_setup_step_count"])
	}
	if notes["config_touch_count"]["unavailable_reason"] != "d6c_setup_harness_pending" {
		t.Fatalf("config notes: %v", notes["config_touch_count"])
	}
}

// TestSmoke_ProbeFailure_DoesNotBlockRunner injects a fault that
// exercises the probe read-side: a shadow workload with a fixtures
// tree containing a MALFORMED `.probes/setup.json` (JSON syntax
// error). runner.go's SetupWorkspace copies the fixtures into the
// tempdir at line 134 via copyTree — the file copies fine — then
// flattens mock_workspace/ into the workspace root at fixtures.go:68,
// so `${workspace}/.probes/setup.json` exists with malformed JSON
// when Edit 2's EmitSetupMetrics fires. readSetupFile takes the
// malformed_setup_file branch, emits nil values + label + one stderr
// warn, and the runner MUST continue (not preflight-fail).
//
// Injecting via AgentStage would run too late — Edit 2 fires BEFORE
// AgentStage — so the fault has to be pre-seeded in the workload
// fixtures so the copy hits it.
func TestSmoke_ProbeFailure_DoesNotBlockRunner(t *testing.T) {
	if testing.Short() {
		t.Skip("smoke test skipped in -short mode")
	}
	src := findWorkloadsDir(t)
	// Build a shadow workload directory: a fresh tempdir with a copy
	// of the cross-device-code-mod workload plus a MALFORMED-JSON
	// .probes file inside mock_workspace/ (so it lands in the
	// workspace root after SetupWorkspace's mock_workspace flatten).
	shadow := t.TempDir()
	wl := filepath.Join(shadow, "cross-device-code-mod")
	if err := copyDirTree(filepath.Join(src, "cross-device-code-mod"), wl); err != nil {
		t.Fatal(err)
	}
	probesDir := filepath.Join(wl, "fixtures", "mock_workspace", ".probes")
	if err := os.MkdirAll(probesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	poisoned := filepath.Join(probesDir, "setup.json")
	// Malformed JSON: readSetupFile returns malformed_setup_file
	// reason; probe emits nil values + one stderr warn.
	if err := os.WriteFile(poisoned, []byte(`{"manual_setup_step_count": not_json_here`), 0o644); err != nil {
		t.Fatal(err)
	}

	tmp := t.TempDir()
	csvPath := filepath.Join(tmp, "run.csv")
	port := freeLoopbackPort(t)

	// Stderr collector so we can assert the probe warn line landed
	// there. A tempfile is easier to seek+re-read than a pipe, and
	// the runner's Opts.Stderr wants an *os.File.
	stderrFile, err := os.CreateTemp(tmp, "stderr-")
	if err != nil {
		t.Fatal(err)
	}
	defer stderrFile.Close()
	opts := Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: shadow,
		StubListen:  port,
		OutCSV:      csvPath,
		Timeout:     60 * time.Second,
		Stderr:      stderrFile,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	result := Run(ctx, opts)
	// Runner MUST complete — fault is a non-blocking probe warn, not
	// a preflight error. Exit code 0 (pass) or 1 (oracle fail) both
	// acceptable — this test asserts non-blockage, not oracle result.
	if result.ExitCode == 2 {
		b, _ := os.ReadFile(stderrFile.Name())
		t.Fatalf("preflight failure not expected: err=%v; stderr=%s", result.Err, b)
	}
	// CSV must have been written.
	if _, err := os.Stat(csvPath); err != nil {
		t.Fatalf("CSV missing: %v", err)
	}
	// Data row's manual_setup_step_count column must be empty AND
	// probe_notes_json must record the JSON parse failure as
	// `malformed_setup_file` — readSetupFile's json.Unmarshal branch
	// triggers on the intentionally malformed fixture we planted.
	f, err := os.Open(csvPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r := csv.NewReader(f)
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	header, data := rows[0], rows[1]
	idx := func(name string) int {
		for i, h := range header {
			if h == name {
				return i
			}
		}
		return -1
	}
	if data[idx("probe_manual_setup_step_count")] != "" {
		t.Fatalf("expected empty on read failure, got %q", data[idx("probe_manual_setup_step_count")])
	}
	var notes map[string]map[string]string
	if err := json.Unmarshal([]byte(data[idx("probe_notes_json")]), &notes); err != nil {
		t.Fatal(err)
	}
	reason := notes["manual_setup_step_count"]["unavailable_reason"]
	if reason != "malformed_setup_file" {
		t.Fatalf("expected unavailable_reason=malformed_setup_file, got %q; stderr=%s", reason, readAll(t, stderrFile.Name()))
	}
}

// copyDirTree is a minimal recursive copy used only by the
// probe-failure smoke test to shadow-clone a workload. It preserves
// modes and file bytes. Deliberately simple; the workload trees are
// small (KB scale).
func copyDirTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode()&0o777|0o700)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		return os.WriteFile(target, b, info.Mode()&0o777)
	})
}

func readAll(t *testing.T, path string) string {
	t.Helper()
	b, _ := os.ReadFile(path)
	return string(b)
}

// findWorkloadsDir walks up from the test's cwd to locate
// tests/eval/workloads so the test can run from either the package
// dir or the module root without hard-coding a path.
func findWorkloadsDir(t *testing.T) string {
	t.Helper()
	d, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		candidate := filepath.Join(d, "tests/eval/workloads")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(d)
		if parent == d {
			t.Fatalf("cannot find tests/eval/workloads under %s", d)
		}
		d = parent
	}
}

// freeLoopbackPort returns a 127.0.0.1:<port> string bound to an
// ephemeral port so parallel smoke tests do not collide.
func freeLoopbackPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}
