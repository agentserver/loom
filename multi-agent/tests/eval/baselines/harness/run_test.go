package harness

import (
	"context"
	"encoding/csv"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// stubImpl copies the mock_workspace projection the harness left in ws.Root
// as the "agent output" — mirrors the runner skeleton's dry-run pattern.
type stubImpl struct {
	name string
}

func (s stubImpl) Name() string               { return s.name }
func (stubImpl) AgentForwards() AgentForwards { return nil }
func (stubImpl) Prepare(ctx context.Context, ws *Workspace, agentEnv []string, dryRun bool) error {
	return nil
}
func (stubImpl) ExecuteAgent(ctx context.Context, ws *Workspace, agentEnv []string, dryRun bool) (ExecuteMetrics, error) {
	// Harness has already projected mock_workspace into ws.Root; nothing
	// more to do.
	return ExecuteMetrics{WallTimeMS: 1, APICalls: 0, UploadBytes: 0}, nil
}

// erroringImpl fails ExecuteAgent — verifies the exit-1 with partial-row
// path.
type erroringImpl struct{ stubImpl }

func (erroringImpl) ExecuteAgent(ctx context.Context, ws *Workspace, agentEnv []string, dryRun bool) (ExecuteMetrics, error) {
	return ExecuteMetrics{WallTimeMS: 2}, errors.New("stub: agent failed")
}

// TestRun_EndToEnd_StubImpl — plan #21. StubImpl produces mock_workspace;
// CSV has 2 lines, passed=true, metric columns populated.
func TestRun_EndToEnd_StubImpl(t *testing.T) {
	out := filepath.Join(t.TempDir(), "row.csv")
	res := Run(context.Background(), Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: workloadsDir(t),
		OutCSV:      out,
		DryRun:      true,
	}, stubImpl{name: "manual_ssh"}, io.Discard)
	if res.ExitCode != 0 {
		t.Fatalf("want exit 0, got %d; err=%v", res.ExitCode, res.Err)
	}
	assertCSVShape(t, out, true, "manual_ssh")
}

// TestRun_ExitCode1_OnOracleFail — plan #22. ExecuteAgent that fails
// still yields a 2-line CSV with exit code 1.
func TestRun_ExitCode1_OnOracleFail(t *testing.T) {
	out := filepath.Join(t.TempDir(), "row.csv")
	res := Run(context.Background(), Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: workloadsDir(t),
		OutCSV:      out,
		DryRun:      true,
	}, erroringImpl{stubImpl: stubImpl{name: "manual_ssh"}}, io.Discard)
	if res.ExitCode != 1 {
		t.Fatalf("want exit 1, got %d; err=%v", res.ExitCode, res.Err)
	}
	// CSV still on disk.
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("CSV not written: %v", err)
	}
}

// TestBaseline_MetricsFieldsPresent — plan #32. Columns 12–14 present
// and numerically parseable after a stub happy run.
func TestBaseline_MetricsFieldsPresent(t *testing.T) {
	out := filepath.Join(t.TempDir(), "row.csv")
	res := Run(context.Background(), Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: workloadsDir(t),
		OutCSV:      out,
		DryRun:      true,
	}, stubImpl{name: "manual_ssh"}, io.Discard)
	if res.ExitCode != 0 {
		t.Fatalf("stub run failed: %v", res.Err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	recs, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("want 2 records, got %d", len(recs))
	}
	// column 12 = index 11 (metrics_baseline_wall_time_ms)
	if v, err := strconv.ParseInt(recs[1][11], 10, 64); err != nil || v < 0 {
		t.Errorf("wall_time_ms not parseable / negative: %q err=%v", recs[1][11], err)
	}
	// column 13 = index 12 (metrics_baseline_api_calls)
	if _, err := strconv.Atoi(recs[1][12]); err != nil {
		t.Errorf("api_calls not parseable: %q", recs[1][12])
	}
	// column 14 = index 13 (metrics_baseline_upload_bytes)
	if _, err := strconv.ParseInt(recs[1][13], 10, 64); err != nil {
		t.Errorf("upload_bytes not parseable: %q", recs[1][13])
	}
}

// TestRun_RejectsInvalidBaselineName — regex enforcement wired at Run
// entry (defence in depth on top of the flag validator).
func TestRun_RejectsInvalidBaselineName(t *testing.T) {
	out := filepath.Join(t.TempDir(), "row.csv")
	res := Run(context.Background(), Opts{
		WorkloadID:   "cross-device-code-mod",
		WorkloadDir:  workloadsDir(t),
		OutCSV:       out,
		BaselineName: "BAD NAME",
	}, stubImpl{name: "manual_ssh"}, io.Discard)
	if res.ExitCode != 2 {
		t.Fatalf("want exit 2, got %d", res.ExitCode)
	}
	// CSV must NOT exist (pre-flight abort).
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("CSV should not exist on pre-flight fail; stat err=%v", err)
	}
}

func assertCSVShape(t *testing.T, path string, wantPassed bool, wantBaseline string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	recs, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("want 2 records, got %d", len(recs))
	}
	// Passed at column 7 = index 6.
	got := recs[1][6]
	want := "false"
	if wantPassed {
		want = "true"
	}
	if got != want {
		t.Errorf("passed column: got %q want %q", got, want)
	}
	if recs[1][2] != wantBaseline {
		t.Errorf("baseline_or_ablation: got %q want %q", recs[1][2], wantBaseline)
	}
}
