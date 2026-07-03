package harness

import (
	"context"
	"encoding/csv"
	"encoding/json"
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

// panickyImpl panics inside ExecuteAgent — regression for the P2
// finding that spec §7(f) promised exit 3 on a "dry-run reached real
// client" panic but harness.Run had no defer recover().
type panickyImpl struct{ stubImpl }

func (panickyImpl) ExecuteAgent(ctx context.Context, ws *Workspace, agentEnv []string, dryRun bool) (ExecuteMetrics, error) {
	panic("simulated: dry-run reached real client")
}

// controlByteErrImpl returns an ExecuteAgent error whose message
// contains ANSI escapes, tab, bell, and non-ASCII runes — the shapes
// that fmt.Sprintf("%q", …) would produce Go-quoted (not JSON-quoted).
type controlByteErrImpl struct{ stubImpl }

func (controlByteErrImpl) ExecuteAgent(ctx context.Context, ws *Workspace, agentEnv []string, dryRun bool) (ExecuteMetrics, error) {
	return ExecuteMetrics{WallTimeMS: 3}, errors.New("stderr=\x1b[31mred\x1b[0m\t\a非ascii\v")
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

// TestRun_RecoversImplPanic_ExitCode3 — spec §7(f) promise that a
// dry-run panic maps to exit 3 (not a Go stack trace crash).
func TestRun_RecoversImplPanic_ExitCode3(t *testing.T) {
	out := filepath.Join(t.TempDir(), "row.csv")
	res := Run(context.Background(), Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: workloadsDir(t),
		OutCSV:      out,
		DryRun:      true,
	}, panickyImpl{stubImpl: stubImpl{name: "manual_ssh"}}, io.Discard)
	if res.ExitCode != 3 {
		t.Fatalf("want exit 3 on impl panic, got %d; err=%v", res.ExitCode, res.Err)
	}
	if res.Err == nil {
		t.Errorf("expected non-nil err carrying panic value")
	}
}

// TestRun_AgentErrorJSON_ParsesWithControlBytes — regression for the
// P1 fresh-review finding: fmt.Sprintf("%q", …) emits Go-quoted strings
// (\x1b, \a, \v), which json.Unmarshal rejects with "invalid character
// in string escape code". Since ExecuteAgent errors wrap subprocess
// stderr (very likely to contain ANSI escapes), the harness must emit
// oracle_details_json that a downstream D1 fold-in can actually parse.
func TestRun_AgentErrorJSON_ParsesWithControlBytes(t *testing.T) {
	out := filepath.Join(t.TempDir(), "row.csv")
	res := Run(context.Background(), Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: workloadsDir(t),
		OutCSV:      out,
		DryRun:      true,
	}, controlByteErrImpl{stubImpl: stubImpl{name: "manual_ssh"}}, io.Discard)
	if res.ExitCode != 1 {
		t.Fatalf("want exit 1 on agent failure, got %d", res.ExitCode)
	}
	// The row's oracle_details_json must be valid JSON — round-trip it.
	var parsed map[string]any
	if err := json.Unmarshal([]byte(res.Row.OracleDetailsJSON), &parsed); err != nil {
		t.Fatalf("oracle_details_json must be valid JSON; parse err=%v; raw=%q", err, res.Row.OracleDetailsJSON)
	}
	if _, ok := parsed["agent_error"]; !ok {
		t.Errorf("expected agent_error key; got %v", parsed)
	}
}

// TestRun_RejectsWorkloadPathTraversal — regression for the P2 fresh-
// review finding: `--workload ../../etc/passwd` would construct
// `<dir>/../../etc/passwd/spec.yaml` and stat outside the workloads
// root, exposing an existence oracle. loadWorkloadSpec must reject
// any id containing a path separator or `..` BEFORE touching disk.
//
// Assertion is `errors.Is(res.Err, ErrWorkloadIDPathTraversal)` —
// NOT just `res.ExitCode == 2`. Every case in the table also points at
// a non-existent path, so a "file not found" would ALSO give exit 2;
// the errors.Is check is what proves the traversal guard fired and
// not the downstream ReadFile.
func TestRun_RejectsWorkloadPathTraversal(t *testing.T) {
	cases := []string{
		"../../etc/passwd",
		"..",
		"foo/bar",
		`foo\bar`,
		".",
		"foo/..",
		"..foo", // contains ".." substring — belt and braces
	}
	for _, id := range cases {
		out := filepath.Join(t.TempDir(), "row.csv")
		res := Run(context.Background(), Opts{
			WorkloadID:   id,
			WorkloadDir:  t.TempDir(),
			OutCSV:       out,
			BaselineName: "manual_ssh",
		}, stubImpl{name: "manual_ssh"}, io.Discard)
		if res.ExitCode != 2 {
			t.Errorf("id=%q: want exit 2, got %d; err=%v", id, res.ExitCode, res.Err)
		}
		if !errors.Is(res.Err, ErrWorkloadIDPathTraversal) {
			t.Errorf("id=%q: want ErrWorkloadIDPathTraversal, got %v", id, res.Err)
		}
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
