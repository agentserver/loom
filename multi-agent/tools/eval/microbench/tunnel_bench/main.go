// tunnel_bench measures TunnelOverhead (RTT of a small HTTP GET) and
// ArtifactTransferThroughput (bytes/sec of variable-size HTTP transfer).
// Both feed WT-2-metric-extract's Figure 4 (Overhead breakdown).
//
// Design:
//   - Warm-up ≥ 100 iters ENFORCED (see common.RunBench).
//   - 100 MiB payload STAYS IN MEMORY (spec §6 (d)): served from
//     bytes.NewReader wrapping a pre-allocated []byte. Any disk write
//     under TMPDIR is a bug and is caught by main_test.go.
//   - `--dry-run` prints the config plan and exits 0 without opening any
//     socket or file. CI uses this as the smoke target.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	"github.com/yourorg/multi-agent/tools/eval/microbench/common"
)

type flags struct {
	warmup, samples int
	seed            int64
	dryRun          bool
	printPlan       bool
	sizesCSV        string
	target          string // when non-empty, hit an external URL instead of httptest
	outCSV          string
}

// sizeSpec is a parsed --sizes value (bytes, human-readable label).
type sizeSpec struct {
	Bytes int64
	Label string
}

func parseSizes(csv string) ([]sizeSpec, error) {
	// Accepts "32,1KiB,1MiB,100MiB". "32" is a raw byte count used for
	// the ping (latency-only) path. The others use IEC binary suffixes.
	if csv == "" {
		return []sizeSpec{{32, "32B"}}, nil
	}
	var out []sizeSpec
	// Small hand-rolled parser to keep the binary stdlib-only.
	start := 0
	for i := 0; i <= len(csv); i++ {
		if i < len(csv) && csv[i] != ',' {
			continue
		}
		tok := csv[start:i]
		start = i + 1
		if tok == "" {
			continue
		}
		s, err := parseSize(tok)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no sizes parsed from %q", csv)
	}
	return out, nil
}

func parseSize(tok string) (sizeSpec, error) {
	suffixes := []struct {
		s string
		f int64
	}{
		{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30},
		{"B", 1},
	}
	for _, sfx := range suffixes {
		if len(tok) >= len(sfx.s) && tok[len(tok)-len(sfx.s):] == sfx.s {
			numStr := tok[:len(tok)-len(sfx.s)]
			var n int64
			for _, r := range numStr {
				if r < '0' || r > '9' {
					return sizeSpec{}, fmt.Errorf("bad numeric prefix in %q", tok)
				}
				n = n*10 + int64(r-'0')
			}
			if n == 0 {
				return sizeSpec{}, fmt.Errorf("zero size %q", tok)
			}
			return sizeSpec{Bytes: n * sfx.f, Label: tok}, nil
		}
	}
	// No suffix → raw byte count.
	var n int64
	for _, r := range tok {
		if r < '0' || r > '9' {
			return sizeSpec{}, fmt.Errorf("bad numeric %q", tok)
		}
		n = n*10 + int64(r-'0')
	}
	if n == 0 {
		return sizeSpec{}, fmt.Errorf("zero size %q", tok)
	}
	return sizeSpec{Bytes: n, Label: tok}, nil
}

func parseFlags(args []string) (flags, error) {
	fs := flag.NewFlagSet("tunnel_bench", flag.ContinueOnError)
	var f flags
	fs.IntVar(&f.warmup, "warmup", 100, "warm-up iterations (>=100)")
	fs.IntVar(&f.samples, "samples", 500, "recorded samples (>=500)")
	fs.Int64Var(&f.seed, "seed", 1, "deterministic RNG seed")
	fs.BoolVar(&f.dryRun, "dry-run", false, "print plan and exit; no network or disk activity")
	fs.BoolVar(&f.printPlan, "print-plan", false, "print resolved plan to stdout in addition to running")
	fs.StringVar(&f.sizesCSV, "sizes", "32,1KiB,1MiB,100MiB", "comma-separated payload sizes")
	fs.StringVar(&f.target, "target", "", "external URL (e.g. https://observer.example.com); empty uses httptest")
	fs.StringVar(&f.outCSV, "out", "-", "CSV output path or '-' for stdout")
	if err := fs.Parse(args); err != nil {
		return f, err
	}
	if f.warmup < 100 {
		return f, fmt.Errorf("warmup must be ≥ 100")
	}
	if f.samples < 500 {
		return f, fmt.Errorf("samples must be ≥ 500")
	}
	return f, nil
}

func planText(f flags, sizes []sizeSpec) string {
	var b bytes.Buffer
	fmt.Fprintf(&b, "tunnel_bench plan (seed=%d)\n", f.seed)
	fmt.Fprintf(&b, "  warmup=%d samples=%d\n", f.warmup, f.samples)
	fmt.Fprintf(&b, "  target=%s\n", targetOrHttptest(f.target))
	fmt.Fprintf(&b, "  sizes:\n")
	for _, s := range sizes {
		fmt.Fprintf(&b, "    %s (%d bytes)\n", s.Label, s.Bytes)
	}
	return b.String()
}

func targetOrHttptest(t string) string {
	if t == "" {
		return "<httptest.NewServer 127.0.0.1>"
	}
	return t
}

// buildPayload returns a shared read-only []byte of exactly n bytes. It
// is pre-allocated once per size and reused across iterations to satisfy
// spec §6 (a) same-subject rule and §6 (d) no-disk-write rule.
func buildPayload(n int64, seed int64) []byte {
	buf := make([]byte, n)
	// #nosec G404 -- deterministic seeded rng for payload permutation.
	rng := rand.New(rand.NewSource(seed))
	// Fill with pseudo-random bytes so compression / dedup doesn't lie.
	for i := range buf {
		buf[i] = byte(rng.Intn(256))
	}
	return buf
}

func run(args []string, stdout, stderr io.Writer) int {
	f, err := parseFlags(args)
	if err != nil {
		fmt.Fprintln(stderr, "tunnel_bench:", err)
		return 2
	}
	sizes, err := parseSizes(f.sizesCSV)
	if err != nil {
		fmt.Fprintln(stderr, "tunnel_bench:", err)
		return 2
	}
	plan := planText(f, sizes)
	if f.dryRun {
		fmt.Fprint(stdout, plan)
		return 0
	}
	if f.printPlan {
		fmt.Fprint(stdout, plan)
	}
	// Real run: spin an httptest server that serves buildPayload(size).
	// The payload is captured in a per-request closure so we can vary
	// per iteration if we ever need to (currently constant for §6 (a)).
	payloads := make(map[int64][]byte, len(sizes))
	for _, s := range sizes {
		payloads[s.Bytes] = buildPayload(s.Bytes, f.seed)
	}
	// Determine target: httptest server (default) or external.
	var baseURL string
	var httpc *http.Client = http.DefaultClient
	if f.target == "" {
		mux := http.NewServeMux()
		mux.HandleFunc("/serve", func(w http.ResponseWriter, r *http.Request) {
			szStr := r.URL.Query().Get("bytes")
			var sz int64
			for _, c := range szStr {
				if c < '0' || c > '9' {
					http.Error(w, "bad bytes", http.StatusBadRequest)
					return
				}
				sz = sz*10 + int64(c-'0')
			}
			p, ok := payloads[sz]
			if !ok {
				http.Error(w, "unknown size", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(p)))
			// bytes.NewReader is memory-only: no file staging.
			_, _ = io.Copy(w, bytes.NewReader(p))
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()
		baseURL = srv.URL
	} else {
		baseURL = f.target
	}

	// Emit CSV to stdout or file.
	out := stdout
	if f.outCSV != "-" {
		fh, err := os.Create(f.outCSV) // acceptable: --out is caller-selected, not TMPDIR
		if err != nil {
			fmt.Fprintln(stderr, "tunnel_bench:", err)
			return 1
		}
		defer fh.Close()
		out = fh
	}
	header := []string{"size_bytes", "size_label",
		"latency_p50_ns", "latency_p95_ns",
		"throughput_p50_bps", "throughput_p95_bps",
		"warmup_iters", "samples", "seed"}
	if err := common.WriteRow(out, header); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	for _, s := range sizes {
		s := s
		samples, err := common.RunBench(
			common.BenchConfig{Warmup: f.warmup, Samples: f.samples, Seed: f.seed},
			func(iter int, rng *rand.Rand) (common.Sample, error) {
				url := fmt.Sprintf("%s/serve?bytes=%d", baseURL, s.Bytes)
				start := time.Now()
				resp, err := httpc.Get(url)
				if err != nil {
					return common.Sample{}, err
				}
				n, err := io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if err != nil {
					return common.Sample{}, err
				}
				return common.Sample{
					DurationNs: time.Since(start).Nanoseconds(),
					Bytes:      n,
				}, nil
			},
		)
		if err != nil {
			fmt.Fprintln(stderr, "tunnel_bench:", err)
			return 1
		}
		row := []string{
			fmt.Sprint(s.Bytes),
			s.Label,
			fmt.Sprint(common.P50(samples)),
			fmt.Sprint(common.P95(samples)),
			fmt.Sprint(common.BytesP50(samples)),
			fmt.Sprint(common.BytesP95(samples)),
			fmt.Sprint(f.warmup),
			fmt.Sprint(f.samples),
			fmt.Sprint(f.seed),
		}
		if err := common.WriteRow(out, row); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	return 0
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
