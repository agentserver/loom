// Package baselines_test holds the 15-run dry-run smoke that exercises
// every baseline against every workload end-to-end. Guarded by the
// `matrix` build tag so the default `go test ./...` (invoked in per-
// baseline unit tests) does not spend a minute compiling three
// binaries — CI opts in with `-tags matrix`.
//
//go:build matrix

package baselines_test

import (
	"encoding/csv"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

// baselines is the fixed list of baseline directories under
// tests/eval/baselines/ + the expected baseline_or_ablation string
// each binary emits by default.
var baselines = []struct {
	dir  string
	name string
}{
	{"manual_ssh", "manual_ssh"},
	{"single_machine_codex", "single_machine_codex"},
	{"cloud_sandbox", "cloud_sandbox_e2b"},
}

// workloads is the fixed 5-workload set from Phase 0.
var workloads = []string{
	"cross-device-code-mod",
	"remote-data-processing",
	"windows-only-artifact",
	"missing-parser-converter",
	"credential-bound-model",
}

// csvReadAll is a tiny wrapper around csv.NewReader so parallel
// subtests share one helper rather than each importing encoding/csv.
func csvReadAll(f *os.File) ([][]string, error) {
	return csv.NewReader(f).ReadAll()
}

// TestMatrix15Runs — plan §15-run smoke matrix. Compiles all three
// baseline binaries, then for each (baseline × workload) pair runs
// `--dry-run` and asserts:
//   - exit ∈ {0, 1} (2 or 3 = FAIL)
//   - CSV file exists with exactly 2 lines
//   - baseline_or_ablation column matches the binary's default
//   - dry_run column is `true`
//   - metrics_baseline_wall_time_ms parses as an int
//
// Guarded by build tag `matrix` — see file header.
func TestMatrix15Runs(t *testing.T) {
	binDir := t.TempDir()
	_, self, _, _ := runtime.Caller(0)
	moduleRoot := filepath.Clean(filepath.Join(filepath.Dir(self), "..", "..", ".."))
	workloadDir := filepath.Join(moduleRoot, "tests", "eval", "workloads")

	// Build every baseline once.
	for _, b := range baselines {
		bin := filepath.Join(binDir, b.dir)
		cmd := exec.Command("go", "build", "-o", bin, "./tests/eval/baselines/"+b.dir)
		cmd.Dir = moduleRoot
		cmd.Env = os.Environ()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", b.dir, err, string(out))
		}
	}

	// Then fan out subtests. Each subtest gets its own CSV path so
	// parallel runs don't collide on the refuse-if-exists guard.
	outDir := t.TempDir()
	for _, wl := range workloads {
		for _, b := range baselines {
			wl, b := wl, b
			t.Run(wl+"×"+b.dir, func(t *testing.T) {
				t.Parallel()
				csv := filepath.Join(outDir, wl+"-"+b.dir+".csv")
				bin := filepath.Join(binDir, b.dir)
				cmd := exec.Command(bin, "run",
					"--workload", wl,
					"--workload-dir", workloadDir,
					"--out", csv,
					"--dry-run")
				cmd.Env = os.Environ()
				out, err := cmd.CombinedOutput()
				exitCode := 0
				if err != nil {
					if ee, ok := err.(*exec.ExitError); ok {
						exitCode = ee.ExitCode()
					} else {
						t.Fatalf("run %s×%s: %v\n%s", wl, b.dir, err, string(out))
					}
				}
				if exitCode != 0 && exitCode != 1 {
					t.Fatalf("exit code %d (want 0 or 1)\n%s", exitCode, string(out))
				}
				f, err := os.Open(csv)
				if err != nil {
					t.Fatalf("open csv: %v", err)
				}
				defer f.Close()
				recs, err := csvReadAll(f)
				if err != nil {
					t.Fatalf("csv read: %v", err)
				}
				if len(recs) != 2 {
					t.Errorf("want 2 records (header + row), got %d", len(recs))
					return
				}
				dataRow := recs[1]
				if len(dataRow) < 15 {
					t.Fatalf("row has %d cols, want ≥15", len(dataRow))
				}
				if dataRow[2] != b.name {
					t.Errorf("baseline_or_ablation: got %q, want %q", dataRow[2], b.name)
				}
				if dataRow[10] != "true" {
					t.Errorf("dry_run column: got %q, want true", dataRow[10])
				}
				if _, err := strconv.ParseInt(dataRow[11], 10, 64); err != nil {
					t.Errorf("wall_time_ms not int: %q (%v)", dataRow[11], err)
				}
			})
		}
	}
}
