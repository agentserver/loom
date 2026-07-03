// sweep is the harness that expands the §E5 scale-point cross-product,
// runs each point through the three microbench binaries, aggregates
// their outputs into one CSV row, and appends the two metrics read
// from observer tables (probe_events + route_reasons).
//
// Two operating modes:
//
//   - Normal: shells out to tunnel_bench / observer_bench / proxy_bench
//     for each scale point, then queries the operator-provided
//     `--observer-db` for probe_events / route_reasons percentiles. This
//     mode is documented but NOT exercised by CI (too slow) — CI uses
//     --dry-run instead.
//
//   - --from-fixture: reads a pre-populated SQLite fixture and derives
//     ALL columns from it. Used in tests to assert §4.6 header exactly
//     and to prove RoutingLatencyP50P95 flows from route_reasons.
//
// CI-safety:
//   - `--dry-run` prints the plan and exits 0; zero exec / socket / DB.
//   - When CI=1 and the sweep is asked to run the full 45-point matrix
//     WITHOUT `--force-full`, it refuses (exit 2) with a specific error.
package main

import (
	"bytes"
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yourorg/multi-agent/internal/observerstore"
	"github.com/yourorg/multi-agent/tools/eval/microbench/common"
	scale_sweep "github.com/yourorg/multi-agent/tools/eval/scale_sweep"
)

// benchCommand is a package-level indirection so tests can substitute a
// fake command (e.g. one that prints a canned CSV). In production it
// builds an exec.Cmd with a 60-second timeout.
var benchCommand = func(name string, args ...string) *exec.Cmd {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	// cancel is called when the process exits or the context deadline
	// hits; store it on the Cmd via Cancel so callers don't need to
	// thread it explicitly.
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Cancel = func() error { cancel(); return cmd.Process.Kill() }
	return cmd
}

type flags struct {
	contextsCSV      string
	toolsCSV         string
	sizesCSV         string
	dryRun           bool
	fromFixture      string // sqlite path; when set, skips real bench runs
	observerDB       string // sqlite path for real runs
	microbenchBinDir string // directory containing tunnel_bench / observer_bench / proxy_bench
	out              string
	forceFull        bool
	sentinelPrefix   string
	benchWarmup      int
	benchSamples     int
	benchSeed        int64
}

func parseFlags(args []string) (flags, error) {
	fs := flag.NewFlagSet("sweep", flag.ContinueOnError)
	var f flags
	fs.StringVar(&f.contextsCSV, "contexts", "", "comma-separated context counts; empty = §E5 default {1,2,4,8,16}")
	fs.StringVar(&f.toolsCSV, "tools-per-context", "", "comma-separated tools/context; empty = §E5 default {10,50,100}")
	fs.StringVar(&f.sizesCSV, "artifact-size", "", "comma-separated artifact sizes (with IEC suffix); empty = §E5 default {1KiB,1MiB,100MiB}")
	fs.BoolVar(&f.dryRun, "dry-run", false, "print plan + would-run commands, exit 0")
	fs.StringVar(&f.fromFixture, "from-fixture", "", "sqlite fixture to read probe_events + route_reasons from (skips bench runs)")
	fs.StringVar(&f.observerDB, "observer-db", "", "sqlite observer DB for probe_events + route_reasons queries in a real run")
	fs.StringVar(&f.microbenchBinDir, "microbench-bin-dir", "", "directory containing tunnel_bench/observer_bench/proxy_bench binaries")
	fs.StringVar(&f.out, "out", "-", "CSV output path or '-' for stdout")
	fs.BoolVar(&f.forceFull, "force-full", false, "allow full 45-point matrix under CI=1")
	fs.StringVar(&f.sentinelPrefix, "sentinel-prefix", "sweep", "conversation_id sentinel prefix (§6 (c) validation applies)")
	fs.IntVar(&f.benchWarmup, "bench-warmup", 100, "warmup iterations passed to each microbench binary (>=100)")
	fs.IntVar(&f.benchSamples, "bench-samples", 500, "sample iterations passed to each microbench binary (>=500)")
	fs.Int64Var(&f.benchSeed, "bench-seed", 1, "seed passed to each microbench binary")
	if err := fs.Parse(args); err != nil {
		return f, err
	}
	return f, nil
}

func parseIntList(s string) ([]int, error) {
	if s == "" {
		return nil, nil
	}
	var out []int
	for _, tok := range strings.Split(s, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		n, err := strconv.Atoi(tok)
		if err != nil {
			return nil, fmt.Errorf("bad int %q: %v", tok, err)
		}
		out = append(out, n)
	}
	return out, nil
}

func parseSizeList(s string) ([]int64, error) {
	if s == "" {
		return nil, nil
	}
	var out []int64
	for _, tok := range strings.Split(s, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		n, err := parseIECBytes(tok)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

func parseIECBytes(tok string) (int64, error) {
	suffixes := []struct {
		s string
		f int64
	}{
		{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30},
		{"B", 1},
	}
	for _, sfx := range suffixes {
		if len(tok) >= len(sfx.s) && tok[len(tok)-len(sfx.s):] == sfx.s {
			num := tok[:len(tok)-len(sfx.s)]
			n, err := strconv.ParseInt(num, 10, 64)
			if err != nil || n == 0 {
				return 0, fmt.Errorf("bad size %q", tok)
			}
			return n * sfx.f, nil
		}
	}
	n, err := strconv.ParseInt(tok, 10, 64)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("bad size %q", tok)
	}
	return n, nil
}

// csvHeader is the AUTHORITATIVE header per spec §4.6. Any change here
// requires a matching change in WT-2-metric-extract's Figure 4 mapping.
var csvHeader = []string{
	"contexts", "tools_per_context", "artifact_size_bytes",
	"driver_planning_overhead_p50_ns", "driver_planning_overhead_p95_ns",
	"task_dispatch_latency_p50_ns", "task_dispatch_latency_p95_ns",
	"tunnel_overhead_p50_ns", "tunnel_overhead_p95_ns",
	"artifact_transfer_throughput_p50_bps", "artifact_transfer_throughput_p95_bps",
	"observer_overhead_p50_ns", "observer_overhead_p95_ns",
	"model_proxy_overhead_bench_p50_ns", "model_proxy_overhead_bench_p95_ns",
	"routing_latency_p50_ns", "routing_latency_p95_ns",
	"samples", "warmup_iters", "seed",
}

// sentinelFor returns the per-point conversation_id used to tag
// probe_events rows so the SELECT can bucket by point. Validated at
// startup (spec §6 (c) startup gate).
func sentinelFor(prefix string, p scale_sweep.ScalePoint) string {
	return fmt.Sprintf("%s-c%d-t%d-b%d", prefix, p.Contexts, p.ToolsPerContext, p.ArtifactSizeBytes)
}

// planPreview describes what the sweep WOULD do, for --dry-run.
func planPreview(pts []scale_sweep.ScalePoint, f flags) string {
	var b bytes.Buffer
	fmt.Fprintf(&b, "sweep plan: %d point(s)\n", len(pts))
	for _, p := range pts {
		fmt.Fprintf(&b, "  contexts=%d tools_per_context=%d artifact_size_bytes=%d sentinel=%s\n",
			p.Contexts, p.ToolsPerContext, p.ArtifactSizeBytes, sentinelFor(f.sentinelPrefix, p))
	}
	if f.fromFixture != "" {
		fmt.Fprintf(&b, "  mode=from-fixture path=%s\n", f.fromFixture)
	} else {
		fmt.Fprintf(&b, "  mode=live-bench (observer-db=%s)\n", f.observerDB)
	}
	return b.String()
}

func run(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	f, err := parseFlags(args)
	if err != nil {
		fmt.Fprintln(stderr, "sweep:", err)
		return 2
	}
	contexts, err := parseIntList(f.contextsCSV)
	if err != nil {
		fmt.Fprintln(stderr, "sweep:", err)
		return 2
	}
	tools, err := parseIntList(f.toolsCSV)
	if err != nil {
		fmt.Fprintln(stderr, "sweep:", err)
		return 2
	}
	sizes, err := parseSizeList(f.sizesCSV)
	if err != nil {
		fmt.Fprintln(stderr, "sweep:", err)
		return 2
	}
	axes := scale_sweep.ScaleAxes{Contexts: contexts, ToolsPerContext: tools, ArtifactSizes: sizes}
	pts := scale_sweep.Expand(axes)

	// Sentinel-id validation at startup (spec §6 (c) startup gate).
	for _, p := range pts {
		if !common.ValidConversationID(sentinelFor(f.sentinelPrefix, p)) {
			fmt.Fprintf(stderr, "sweep: sentinel conversation_id fails ValidConversationID (%s regex): point=%+v\n",
				common.ConvIDRegex.String(), p)
			return 2
		}
	}

	// CI full-matrix refuse gate. "Full matrix" here means "all default
	// axes present" — i.e. contexts len ≥ 5 AND tools len ≥ 3 AND sizes
	// len ≥ 3, which is exactly what the paper's 45-point default is.
	if getenv("CI") != "" && !f.forceFull {
		full := len(axes.Contexts) >= 5 && len(axes.ToolsPerContext) >= 3 && len(axes.ArtifactSizes) >= 3
		if full || (len(contexts) == 0 && len(tools) == 0 && len(sizes) == 0) {
			// When all three CSVs were empty, Expand fell back to
			// DefaultAxes (45 points); refuse.
			fmt.Fprintln(stderr, "sweep: refusing to run full matrix in CI without --force-full")
			return 2
		}
	}

	if f.dryRun {
		fmt.Fprint(stdout, planPreview(pts, f))
		return 0
	}

	// Open CSV output.
	out := stdout
	if f.out != "-" {
		fh, err := os.Create(f.out)
		if err != nil {
			fmt.Fprintln(stderr, "sweep:", err)
			return 1
		}
		defer fh.Close()
		out = fh
	}
	if err := common.WriteRow(out, csvHeader); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	// Fixture mode: read probe_events + route_reasons only.
	if f.fromFixture != "" {
		return runFromFixture(f, pts, out, stderr)
	}

	// Live-bench mode: shell out to the three microbench binaries per
	// scale point, then read probe_events + route_reasons from the
	// operator-provided observer DB. Requires --observer-db AND
	// --microbench-bin-dir to locate the built binaries; if either is
	// missing we cannot fill the full 7-column row and refuse rather
	// than emit an all-empty CSV.
	if f.observerDB == "" {
		fmt.Fprintln(stderr, "sweep: --observer-db required in live-bench mode (or use --from-fixture)")
		return 2
	}
	if f.microbenchBinDir == "" {
		fmt.Fprintln(stderr, "sweep: --microbench-bin-dir required in live-bench mode")
		return 2
	}
	return runLiveBench(f, pts, out, stderr)
}

func runFromFixture(f flags, pts []scale_sweep.ScalePoint, out io.Writer, stderr io.Writer) int {
	st, err := observerstore.OpenSQLite(f.fromFixture)
	if err != nil {
		fmt.Fprintln(stderr, "sweep from-fixture:", err)
		return 1
	}
	defer st.Close()
	db := st.DB()

	// route_reasons is scale-independent in this WT — it's whatever the
	// slave-agent recorded during the eval run. We compute one pair of
	// p50/p95 values once and stamp them on every row.
	routeP50, routeP95, err := percentileFromDuration(db,
		`SELECT decision_duration_ns FROM route_reasons ORDER BY decision_duration_ns ASC`)
	if err != nil {
		fmt.Fprintln(stderr, "sweep from-fixture route_reasons:", err)
		return 1
	}
	for _, p := range pts {
		sentinel := sentinelFor(f.sentinelPrefix, p)
		row, err := fixtureRow(db, p, sentinel, routeP50, routeP95)
		if err != nil {
			fmt.Fprintln(stderr, "sweep from-fixture row:", err)
			return 1
		}
		if err := common.WriteRow(out, row); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	return 0
}

func fixtureRow(db *sql.DB, p scale_sweep.ScalePoint, sentinel string, routeP50, routeP95 int64) ([]string, error) {
	kindP50P95 := func(kind string) (int64, int64, error) {
		return percentileFromDuration(db,
			`SELECT duration_ns FROM probe_events
			 WHERE probe_kind=? AND conversation_id=?
			 ORDER BY duration_ns ASC`, kind, sentinel)
	}
	dp50, dp95, err := kindP50P95(string(observerstore.KindDriverPlanning))
	if err != nil {
		return nil, err
	}
	tp50, tp95, err := kindP50P95(string(observerstore.KindTaskDispatch))
	if err != nil {
		return nil, err
	}
	op50, op95, err := kindP50P95(string(observerstore.KindObserverWrite))
	if err != nil {
		return nil, err
	}
	// Tunnel / throughput / proxy come from the three microbench CSVs;
	// in fixture mode we don't have those, so they land as empty cells
	// (metric-extract treats empty as null per the D2 spec).
	return []string{
		strconv.Itoa(p.Contexts),
		strconv.Itoa(p.ToolsPerContext),
		strconv.FormatInt(p.ArtifactSizeBytes, 10),
		fmt.Sprint(dp50), fmt.Sprint(dp95),
		fmt.Sprint(tp50), fmt.Sprint(tp95),
		"", "", // tunnel_overhead p50/p95
		"", "", // artifact_transfer_throughput p50/p95
		fmt.Sprint(op50), fmt.Sprint(op95),
		"", "", // model_proxy_overhead_bench p50/p95
		strconv.FormatInt(routeP50, 10), strconv.FormatInt(routeP95, 10),
		"", "", "", // samples / warmup_iters / seed (bench-only)
	}, nil
}

// runLiveBench dispatches to the three microbench binaries per scale
// point and then reads probe_events + route_reasons from the observer
// DB. All seven overhead metrics land in the emitted CSV row.
//
// Design:
//   - tunnel_bench: run once per point with `--sizes <point.ArtifactSize>`.
//     Parse its CSV; tunnel_overhead p50/p95 come from `latency_p50_ns`
//     and `latency_p95_ns` columns; artifact_transfer_throughput from
//     `throughput_p*_bps`.
//   - observer_bench: run once per point; take the "NoObserver=false"
//     row's p50/p95 (real writer cost).
//   - proxy_bench: run once per point; take the "proxy" row's p50/p95.
//   - probe_events (driver_planning / task_dispatch): read from
//     observer DB using the per-point sentinel.
//   - route_reasons: read once outside the per-point loop; stamp
//     identically on every row (routing latency is scale-independent).
func runLiveBench(f flags, pts []scale_sweep.ScalePoint, out io.Writer, stderr io.Writer) int {
	st, err := observerstore.OpenSQLite(f.observerDB)
	if err != nil {
		fmt.Fprintln(stderr, "sweep live-bench observer DB:", err)
		return 1
	}
	defer st.Close()
	db := st.DB()
	routeP50, routeP95, err := percentileFromDuration(db,
		`SELECT decision_duration_ns FROM route_reasons ORDER BY decision_duration_ns ASC`)
	if err != nil {
		fmt.Fprintln(stderr, "sweep live-bench route_reasons:", err)
		return 1
	}
	for _, p := range pts {
		sentinel := sentinelFor(f.sentinelPrefix, p)
		tunP50, tunP95, thrP50, thrP95, err := runTunnelBench(f, p, stderr)
		if err != nil {
			fmt.Fprintln(stderr, "sweep live-bench tunnel:", err)
			return 1
		}
		obsP50, obsP95, err := runObserverBench(f, sentinel, stderr)
		if err != nil {
			fmt.Fprintln(stderr, "sweep live-bench observer:", err)
			return 1
		}
		proxP50, proxP95, err := runProxyBench(f, stderr)
		if err != nil {
			fmt.Fprintln(stderr, "sweep live-bench proxy:", err)
			return 1
		}
		dp50, dp95, err := percentileFromDuration(db,
			`SELECT duration_ns FROM probe_events WHERE probe_kind=? AND conversation_id=? ORDER BY duration_ns ASC`,
			string(observerstore.KindDriverPlanning), sentinel)
		if err != nil {
			fmt.Fprintln(stderr, "sweep live-bench probe planning:", err)
			return 1
		}
		tp50, tp95, err := percentileFromDuration(db,
			`SELECT duration_ns FROM probe_events WHERE probe_kind=? AND conversation_id=? ORDER BY duration_ns ASC`,
			string(observerstore.KindTaskDispatch), sentinel)
		if err != nil {
			fmt.Fprintln(stderr, "sweep live-bench probe dispatch:", err)
			return 1
		}
		row := []string{
			strconv.Itoa(p.Contexts),
			strconv.Itoa(p.ToolsPerContext),
			strconv.FormatInt(p.ArtifactSizeBytes, 10),
			fmt.Sprint(dp50), fmt.Sprint(dp95),
			fmt.Sprint(tp50), fmt.Sprint(tp95),
			fmt.Sprint(tunP50), fmt.Sprint(tunP95),
			fmt.Sprint(thrP50), fmt.Sprint(thrP95),
			fmt.Sprint(obsP50), fmt.Sprint(obsP95),
			fmt.Sprint(proxP50), fmt.Sprint(proxP95),
			strconv.FormatInt(routeP50, 10), strconv.FormatInt(routeP95, 10),
			strconv.Itoa(f.benchSamples), strconv.Itoa(f.benchWarmup), strconv.FormatInt(f.benchSeed, 10),
		}
		if err := common.WriteRow(out, row); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	return 0
}

// runTunnelBench execs tunnel_bench once and parses its CSV output.
// Returns (latency_p50, latency_p95, throughput_p50_bps, throughput_p95_bps).
func runTunnelBench(f flags, p scale_sweep.ScalePoint, stderr io.Writer) (int64, int64, int64, int64, error) {
	binPath := filepath.Join(f.microbenchBinDir, "tunnel_bench")
	sizeArg := fmt.Sprintf("%d", p.ArtifactSizeBytes)
	out, err := execBench(binPath, []string{
		"--warmup", strconv.Itoa(f.benchWarmup),
		"--samples", strconv.Itoa(f.benchSamples),
		"--seed", strconv.FormatInt(f.benchSeed, 10),
		"--sizes", sizeArg,
	})
	if err != nil {
		return 0, 0, 0, 0, err
	}
	// tunnel_bench CSV header: size_bytes,size_label,latency_p50_ns,
	// latency_p95_ns,throughput_p50_bps,throughput_p95_bps,warmup,samples,seed
	cols := parseSingleDataRow(out)
	if len(cols) < 6 {
		return 0, 0, 0, 0, fmt.Errorf("tunnel_bench CSV malformed: %q", out)
	}
	p50, _ := strconv.ParseInt(cols[2], 10, 64)
	p95, _ := strconv.ParseInt(cols[3], 10, 64)
	thrP50, _ := strconv.ParseInt(cols[4], 10, 64)
	thrP95, _ := strconv.ParseInt(cols[5], 10, 64)
	return p50, p95, thrP50, thrP95, nil
}

// runObserverBench execs observer_bench and returns (real_p50, real_p95).
func runObserverBench(f flags, sentinel string, stderr io.Writer) (int64, int64, error) {
	binPath := filepath.Join(f.microbenchBinDir, "observer_bench")
	out, err := execBench(binPath, []string{
		"--warmup", strconv.Itoa(f.benchWarmup),
		"--samples", strconv.Itoa(f.benchSamples),
		"--seed", strconv.FormatInt(f.benchSeed, 10),
		"--conv-id", sentinel,
	})
	if err != nil {
		return 0, 0, err
	}
	// observer_bench CSV: no_observer,p50_ns,p95_ns,...
	// Find the row where no_observer=false.
	rows := parseAllDataRows(out)
	for _, r := range rows {
		if len(r) >= 3 && r[0] == "false" {
			p50, _ := strconv.ParseInt(r[1], 10, 64)
			p95, _ := strconv.ParseInt(r[2], 10, 64)
			return p50, p95, nil
		}
	}
	return 0, 0, fmt.Errorf("observer_bench: no NoObserver=false row: %q", out)
}

// runProxyBench execs proxy_bench and returns (proxy_p50, proxy_p95).
func runProxyBench(f flags, stderr io.Writer) (int64, int64, error) {
	binPath := filepath.Join(f.microbenchBinDir, "proxy_bench")
	out, err := execBench(binPath, []string{
		"--warmup", strconv.Itoa(f.benchWarmup),
		"--samples", strconv.Itoa(f.benchSamples),
		"--seed", strconv.FormatInt(f.benchSeed, 10),
	})
	if err != nil {
		return 0, 0, err
	}
	// proxy_bench CSV: path,p50_ns,p95_ns,...
	rows := parseAllDataRows(out)
	for _, r := range rows {
		if len(r) >= 3 && r[0] == "proxy" {
			p50, _ := strconv.ParseInt(r[1], 10, 64)
			p95, _ := strconv.ParseInt(r[2], 10, 64)
			return p50, p95, nil
		}
	}
	return 0, 0, fmt.Errorf("proxy_bench: no proxy row: %q", out)
}

// execBench is a thin wrapper around os/exec that captures stdout. Uses
// a bounded timeout so a wedged bench cannot hang the sweep forever.
func execBench(binPath string, args []string) (string, error) {
	cmd := benchCommand(binPath, args...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s %v: %w", binPath, args, err)
	}
	return stdout.String(), nil
}

// parseSingleDataRow returns the second CSV line (skipping the header).
func parseSingleDataRow(csvOut string) []string {
	rows := parseAllDataRows(csvOut)
	if len(rows) == 0 {
		return nil
	}
	return rows[0]
}

// parseAllDataRows returns every non-header CSV row (best-effort: no
// RFC 4180 quoting handling; the microbench CSVs do not use fields
// containing commas or newlines).
func parseAllDataRows(csvOut string) [][]string {
	var rows [][]string
	lines := strings.Split(strings.TrimSpace(csvOut), "\n")
	for i, ln := range lines {
		if i == 0 {
			continue // header
		}
		ln = strings.TrimRight(ln, "\r")
		if ln == "" {
			continue
		}
		rows = append(rows, strings.Split(ln, ","))
	}
	return rows
}

// percentileFromDuration streams a single-column int64 result set,
// sorts (SQL already sorted asc), and computes nearest-rank p50/p95.
// Returns (0, 0, nil) on an empty result set so callers see explicit
// zeros rather than an error.
func percentileFromDuration(db *sql.DB, q string, args ...any) (int64, int64, error) {
	rows, err := db.Query(q, args...)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	var xs []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return 0, 0, err
		}
		xs = append(xs, v)
	}
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	if len(xs) == 0 {
		return 0, 0, nil
	}
	sort.Slice(xs, func(i, j int) bool { return xs[i] < xs[j] })
	rank := func(p int) int64 {
		r := (p*len(xs) + 99) / 100
		if r < 1 {
			r = 1
		}
		if r > len(xs) {
			r = len(xs)
		}
		return xs[r-1]
	}
	return rank(50), rank(95), nil
}

func main() {
	// Discover repo root via cwd walk (best-effort); testdata paths are
	// caller-relative so no discovery is required by the binary itself.
	_ = filepath.Separator
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}
