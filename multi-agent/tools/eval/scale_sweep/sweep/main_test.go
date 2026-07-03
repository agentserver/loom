package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yourorg/multi-agent/internal/observerstore"
	scale_sweep "github.com/yourorg/multi-agent/tools/eval/scale_sweep"
)

const emptyEnv = ""

func noEnv(string) string { return "" }
func ciEnv(k string) string {
	if k == "CI" {
		return "1"
	}
	return ""
}

// Test #47 — --dry-run does not exec anything.
// The sweep in this WT does not exec external binaries (live-bench
// dispatch is a follow-up). Under --dry-run it MUST not exec anything;
// under a live-bench invocation with no --observer-db it should refuse
// (exit 2). The invariant we assert here is: --dry-run prints a plan
// and exits 0.
func TestSweep_DryRun_NoExec(t *testing.T) {
	var out bytes.Buffer
	code := run([]string{
		"--dry-run",
		"--contexts", "1,2",
		"--tools-per-context", "10",
		"--artifact-size", "1KiB",
	}, &out, &out, noEnv)
	if code != 0 {
		t.Fatalf("exit=%d out=%q", code, out.String())
	}
	if !strings.Contains(out.String(), "sweep plan: 2 point(s)") {
		t.Errorf("dry-run stdout should list 2 points, got %q", out.String())
	}
}

// Test #48 — CSV header (first line of --out) matches spec §4.6.
func TestSweep_CSVHeader_MatchesSpec(t *testing.T) {
	fixture := buildFixture(t)
	outPath := filepath.Join(t.TempDir(), "out.csv")
	var errBuf bytes.Buffer
	code := run([]string{
		"--from-fixture", fixture,
		"--contexts", "1",
		"--tools-per-context", "10",
		"--artifact-size", "1KiB",
		"--out", outPath,
	}, &errBuf, &errBuf, noEnv)
	if code != 0 {
		t.Fatalf("exit=%d err=%q", code, errBuf.String())
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	rd := csv.NewReader(bytes.NewReader(got))
	header, err := rd.Read()
	if err != nil {
		t.Fatalf("csv read: %v", err)
	}
	want := csvHeader
	if len(header) != len(want) {
		t.Fatalf("header len want %d got %d: %v", len(want), len(header), header)
	}
	for i, c := range want {
		if header[i] != c {
			t.Errorf("header[%d] want %q got %q", i, c, header[i])
		}
	}
}

// Test #49 — from-fixture: reads probe_events + route_reasons and emits
// a fully-shaped CSV. Not a byte-diff against a checked-in golden — the
// value is deriving numbers from the fixture and asserting them.
func TestSweep_FromFixture(t *testing.T) {
	fixture := buildFixture(t)
	outPath := filepath.Join(t.TempDir(), "out.csv")
	code := run([]string{
		"--from-fixture", fixture,
		"--contexts", "1",
		"--tools-per-context", "10",
		"--artifact-size", "1KiB",
		"--out", outPath,
	}, os.Stdout, os.Stderr, noEnv)
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	rows := readCSV(t, outPath)
	if len(rows) != 2 {
		t.Fatalf("want header + 1 data row, got %d", len(rows))
	}
	data := rowByName(rows)
	if data["driver_planning_overhead_p50_ns"] == "" ||
		data["driver_planning_overhead_p50_ns"] == "0" {
		t.Errorf("driver_planning p50 empty/zero: %q", data["driver_planning_overhead_p50_ns"])
	}
	if data["routing_latency_p50_ns"] == "" ||
		data["routing_latency_p50_ns"] == "0" {
		t.Errorf("routing_latency p50 empty/zero: %q", data["routing_latency_p50_ns"])
	}
}

// Test #50 — CI + full matrix without --force-full refuses.
func TestSweep_CIFullMatrixRefused(t *testing.T) {
	var out bytes.Buffer
	code := run([]string{}, &out, &out, ciEnv) // no axes overrides → full matrix
	if code == 0 {
		t.Fatalf("expected non-zero exit under CI + full matrix, got 0; out=%q", out.String())
	}
	if !strings.Contains(out.String(), "refusing to run full matrix in CI") {
		t.Errorf("stderr should explain refusal, got %q", out.String())
	}
}

// Test #51 — sentinel-prefix that would produce an invalid conv_id is
// rejected at startup.
func TestSweep_SentinelConvID_ValidatesAtStartup(t *testing.T) {
	// A prefix with 121 chars, appended with "-c1-t10-b1024" (13 chars)
	// yields a 134-char sentinel — over the 128-char regex ceiling.
	badPrefix := strings.Repeat("a", 121)
	var out bytes.Buffer
	code := run([]string{
		"--sentinel-prefix", badPrefix,
		"--contexts", "1",
		"--tools-per-context", "10",
		"--artifact-size", "1KiB",
	}, &out, &out, noEnv)
	if code == 0 {
		t.Fatalf("expected non-zero exit; out=%q", out.String())
	}
	if !strings.Contains(out.String(), "sentinel conversation_id fails ValidConversationID") {
		t.Errorf("stderr should explain the rejection, got %q", out.String())
	}
}

// Test #52 — RoutingLatencyP50P95 comes from route_reasons. Populate
// route_reasons with durations 1..100 ms; assert the sweep's
// routing_latency_p50_ns == 50_000_000 and _p95_ns == 95_000_000
// (nearest-rank).
func TestSweep_RoutingP50P95_FromRouteReasons(t *testing.T) {
	fixture := buildFixtureWithRoutes(t, 100)
	outPath := filepath.Join(t.TempDir(), "out.csv")
	code := run([]string{
		"--from-fixture", fixture,
		"--contexts", "1",
		"--tools-per-context", "10",
		"--artifact-size", "1KiB",
		"--out", outPath,
	}, os.Stdout, os.Stderr, noEnv)
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	data := rowByName(readCSV(t, outPath))
	if data["routing_latency_p50_ns"] != "50000000" {
		t.Errorf("routing_latency_p50_ns want 50000000 got %q", data["routing_latency_p50_ns"])
	}
	if data["routing_latency_p95_ns"] != "95000000" {
		t.Errorf("routing_latency_p95_ns want 95000000 got %q", data["routing_latency_p95_ns"])
	}
}

// buildFixture returns a sqlite file with a handful of probe_events and
// route_reasons rows tagged with the default sentinel conv_id.
func buildFixture(t *testing.T) string {
	return buildFixtureWithRoutes(t, 3)
}

// buildFixtureWithRoutes creates a fixture with N route_reasons rows
// where decision_duration_ns = 1_000_000 * (i+1). It also seeds three
// probe_events rows for the default point c=1, t=10, size=1024.
func buildFixtureWithRoutes(t *testing.T, nRoutes int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.db")
	st, err := observerstore.OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	db := st.DB()
	insertProbe := func(kind observerstore.Kind, dur int64, sentinel string) {
		start := time.Now()
		end := start.Add(time.Duration(dur))
		_, err := db.Exec(
			`INSERT INTO probe_events(event_id, probe_kind, conversation_id,
			 span_start_at, span_end_at, duration_ns, wallclock_delta_ms)
			 VALUES(?,?,?,?,?,?,0)`,
			randomID(t),
			string(kind),
			sentinel,
			start.UTC().Format(time.RFC3339Nano),
			end.UTC().Format(time.RFC3339Nano),
			dur,
		)
		if err != nil {
			t.Fatalf("insertProbe: %v", err)
		}
	}
	sentinel := sentinelFor("sweep", scale_sweep.ScalePoint{Contexts: 1, ToolsPerContext: 10, ArtifactSizeBytes: 1024})
	insertProbe(observerstore.KindDriverPlanning, 100_000, sentinel)
	insertProbe(observerstore.KindTaskDispatch, 50_000, sentinel)
	insertProbe(observerstore.KindObserverWrite, 20_000, sentinel)
	// route_reasons: N rows with durations i*1_000_000 ns (1..N ms).
	for i := 1; i <= nRoutes; i++ {
		dur := int64(i * 1_000_000)
		insertRoute(t, db, "conv-routetest01", dur)
	}
	return path
}

func insertRoute(t *testing.T, db *sql.DB, conv string, dur int64) {
	t.Helper()
	start := time.Now()
	end := start.Add(time.Duration(dur))
	_, err := db.Exec(
		`INSERT INTO route_reasons(decision_id, conversation_id, selected_agent_id,
		 reason_code, reason_text, candidates_json,
		 decision_started_at, decision_ended_at, decision_duration_ns)
		 VALUES(?,?,?,?,?,?,?,?,?)`,
		randomID(t), conv, "", "capability_match", "", "[]",
		start.UTC().Format(time.RFC3339Nano),
		end.UTC().Format(time.RFC3339Nano),
		dur,
	)
	if err != nil {
		t.Fatalf("insertRoute: %v", err)
	}
}

// randomID gives us a 32-char unique-ish id per row. Uses time+atomic
// counter to avoid crypto/rand (tests must be fast + deterministic-ish).
func randomID(t *testing.T) string {
	t.Helper()
	return strconv.FormatInt(time.Now().UnixNano(), 16) +
		"-" + strconv.Itoa(nextRowID())
}

var rowCounter int

func nextRowID() int { rowCounter++; return rowCounter }

func readCSV(t *testing.T, path string) [][]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rd := csv.NewReader(f)
	rows, err := rd.ReadAll()
	if err != nil {
		t.Fatalf("csv read: %v", err)
	}
	return rows
}

func rowByName(rows [][]string) map[string]string {
	m := map[string]string{}
	if len(rows) < 2 {
		return m
	}
	for i, name := range rows[0] {
		m[name] = rows[1][i]
	}
	return m
}

// Silence unused imports if compiled in a mode that trims tests.
var _ = context.Background

// Test #57 — live-bench path fills all 7 metric p50/p95 pairs by
// dispatching to the three microbench binaries (mocked via
// benchCommand) and reading probe_events + route_reasons from the
// observer DB.
//
// The mock benchCommand ships a fake helper binary via `os/exec` of
// the current test binary in a special mode (see `TestHelperProcess`
// pattern). This keeps the test hermetic — no real bench binary
// required.
func TestSweep_LiveBench_AllColumnsFilled(t *testing.T) {
	fixture := buildFixture(t) // seeds probe_events + route_reasons
	outPath := filepath.Join(t.TempDir(), "out.csv")
	// Swap benchCommand for one that shells back into this test with
	// GO_HELPER_KIND set; the helper case (below) prints canned CSVs
	// matching each microbench's schema.
	orig := benchCommand
	t.Cleanup(func() { benchCommand = orig })
	benchCommand = func(name string, args ...string) *exec.Cmd {
		kind := filepath.Base(name)
		cs := []string{"-test.run=TestHelperMicrobench", "--"}
		cs = append(cs, kind)
		cs = append(cs, args...)
		cmd := exec.Command(os.Args[0], cs...)
		cmd.Env = append(os.Environ(), "GO_MICROBENCH_HELPER=1")
		return cmd
	}
	code := run([]string{
		"--observer-db", fixture,
		"--microbench-bin-dir", "/does/not/matter",
		"--contexts", "1",
		"--tools-per-context", "10",
		"--artifact-size", "1KiB",
		"--out", outPath,
	}, os.Stdout, os.Stderr, noEnv)
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	data := rowByName(readCSV(t, outPath))
	must := []string{
		"driver_planning_overhead_p50_ns",
		"driver_planning_overhead_p95_ns",
		"task_dispatch_latency_p50_ns",
		"task_dispatch_latency_p95_ns",
		"tunnel_overhead_p50_ns",
		"tunnel_overhead_p95_ns",
		"artifact_transfer_throughput_p50_bps",
		"artifact_transfer_throughput_p95_bps",
		"observer_overhead_p50_ns",
		"observer_overhead_p95_ns",
		"model_proxy_overhead_bench_p50_ns",
		"model_proxy_overhead_bench_p95_ns",
		"routing_latency_p50_ns",
		"routing_latency_p95_ns",
	}
	for _, c := range must {
		if v := data[c]; v == "" || v == "0" {
			t.Errorf("column %s not populated in live-bench mode: %q", c, v)
		}
	}
}

// TestHelperMicrobench is the exec-driven fake for the three microbench
// binaries. It is only entered when GO_MICROBENCH_HELPER=1 is set and
// prints the exact CSV shape each bench emits, so the sweep's parser
// can round-trip the values into its own output.
func TestHelperMicrobench(t *testing.T) {
	if os.Getenv("GO_MICROBENCH_HELPER") != "1" {
		return
	}
	// Args after "--": bench-name, then the flags forwarded from
	// benchCommand.
	args := os.Args[1:]
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	if len(args) == 0 {
		t.Fatal("no bench kind arg")
	}
	kind := args[0]
	switch kind {
	case "tunnel_bench":
		// header: size_bytes,size_label,latency_p50_ns,latency_p95_ns,
		//         throughput_p50_bps,throughput_p95_bps,warmup,samples,seed
		fmt.Fprintln(os.Stdout, "size_bytes,size_label,latency_p50_ns,latency_p95_ns,throughput_p50_bps,throughput_p95_bps,warmup_iters,samples,seed")
		fmt.Fprintln(os.Stdout, "1024,1KiB,111,222,333,444,100,500,1")
	case "observer_bench":
		fmt.Fprintln(os.Stdout, "no_observer,p50_ns,p95_ns,warmup_iters,samples,seed")
		fmt.Fprintln(os.Stdout, "true,10,20,100,500,1")
		fmt.Fprintln(os.Stdout, "false,555,666,100,500,1")
	case "proxy_bench":
		fmt.Fprintln(os.Stdout, "path,p50_ns,p95_ns,warmup_iters,samples,seed")
		fmt.Fprintln(os.Stdout, "proxy,777,888,100,500,1")
		fmt.Fprintln(os.Stdout, "direct,700,800,100,500,1")
	default:
		fmt.Fprintln(os.Stderr, "unknown bench:", kind)
		os.Exit(1)
	}
	os.Exit(0)
}
