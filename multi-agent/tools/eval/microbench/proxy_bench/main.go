// proxy_bench measures ModelProxyOverhead — the p50/p95 latency delta
// between the same prompt hitting a proxy endpoint versus a direct
// provider endpoint.
//
// This is the NAKED microbench per spec §1: no workload, same fixed
// prompt in both round trips. WT-2-credential-workload owns the
// WORKLOAD-integrated `ModelProxyOverhead` number (same metric name in
// English but a distinct CSV column, spec §4.6).
//
// The bench defaults both proxy_url and direct_url to `httptest`
// servers on 127.0.0.1 so CI does not need real API keys. Operators can
// override with `--proxy-url` / `--direct-url` for on-target runs.
//
// A `--fake-delay-ms` knob lets tests assert the DIFFERENCE between the
// two rounds correlates with the injected extra delay on the proxy
// side (correctness of the measurement pipeline, not perf assertion).
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net"
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
	proxyURL        string
	directURL       string
	fakeDelayMs     int
	outCSV          string
}

func parseFlags(args []string) (flags, error) {
	fs := flag.NewFlagSet("proxy_bench", flag.ContinueOnError)
	var f flags
	fs.IntVar(&f.warmup, "warmup", 100, "warm-up iterations (>=100)")
	fs.IntVar(&f.samples, "samples", 500, "recorded samples (>=500)")
	fs.Int64Var(&f.seed, "seed", 1, "deterministic RNG seed")
	fs.BoolVar(&f.dryRun, "dry-run", false, "print plan and exit; no server or socket")
	fs.StringVar(&f.proxyURL, "proxy-url", "", "proxy endpoint URL; empty spins a local httptest server")
	fs.StringVar(&f.directURL, "direct-url", "", "direct endpoint URL; empty spins a local httptest server")
	fs.IntVar(&f.fakeDelayMs, "fake-delay-ms", 0, "extra delay injected into the LOCAL proxy httptest handler only")
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

// fixedPrompt is the same-subject prompt used on both round trips
// (spec §6 (a)). It is deliberately a plain string with no random
// component so a request-body capture in the fake servers can assert
// byte equality across rounds.
const fixedPrompt = `{"model":"stub","prompt":"proxy_bench: fixed prompt for latency measurement"}`

func planText(f flags) string {
	var b bytes.Buffer
	fmt.Fprintf(&b, "proxy_bench plan (seed=%d, warmup=%d, samples=%d)\n",
		f.seed, f.warmup, f.samples)
	fmt.Fprintf(&b, "  proxy_url=%s\n", proxyPlanURL(f.proxyURL))
	fmt.Fprintf(&b, "  direct_url=%s\n", proxyPlanURL(f.directURL))
	fmt.Fprintf(&b, "  fake_delay_ms=%d\n", f.fakeDelayMs)
	return b.String()
}

func proxyPlanURL(u string) string {
	if u == "" {
		return "http://127.0.0.1:<httptest>"
	}
	return u
}

// echoServer returns a handler that reads the request body and echoes
// its first N bytes. If injectedDelay > 0 it sleeps that long before
// writing (used to inject proxy overhead on the local httptest path).
func echoServer(injectedDelay time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if injectedDelay > 0 {
			time.Sleep(injectedDelay)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		_, _ = w.Write(body)
	})
}

func run(args []string, stdout, stderr io.Writer) int {
	f, err := parseFlags(args)
	if err != nil {
		fmt.Fprintln(stderr, "proxy_bench:", err)
		return 2
	}
	if f.dryRun {
		fmt.Fprint(stdout, planText(f))
		return 0
	}
	proxyURL, closeProxy := chooseURL(f.proxyURL, time.Duration(f.fakeDelayMs)*time.Millisecond)
	defer closeProxy()
	directURL, closeDirect := chooseURL(f.directURL, 0)
	defer closeDirect()

	client := &http.Client{Timeout: 5 * time.Second}
	runRound := func(url string) ([]common.Sample, error) {
		return common.RunBench(
			common.BenchConfig{Warmup: f.warmup, Samples: f.samples, Seed: f.seed},
			func(iter int, rng *rand.Rand) (common.Sample, error) {
				start := time.Now()
				resp, err := client.Post(url, "application/json", bytes.NewReader([]byte(fixedPrompt)))
				if err != nil {
					return common.Sample{}, err
				}
				_, cerr := io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if cerr != nil {
					return common.Sample{}, cerr
				}
				return common.Sample{DurationNs: time.Since(start).Nanoseconds()}, nil
			},
		)
	}
	sampProxy, err := runRound(proxyURL)
	if err != nil {
		fmt.Fprintln(stderr, "proxy_bench proxy round:", err)
		return 1
	}
	sampDirect, err := runRound(directURL)
	if err != nil {
		fmt.Fprintln(stderr, "proxy_bench direct round:", err)
		return 1
	}
	out := stdout
	if f.outCSV != "-" {
		fh, err := os.Create(f.outCSV)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		defer fh.Close()
		out = fh
	}
	if err := common.WriteRow(out, []string{
		"path", "p50_ns", "p95_ns", "warmup_iters", "samples", "seed",
	}); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	rows := []struct {
		label string
		samp  []common.Sample
	}{
		{"proxy", sampProxy},
		{"direct", sampDirect},
	}
	for _, r := range rows {
		if err := common.WriteRow(out, []string{
			r.label,
			fmt.Sprint(common.P50(r.samp)),
			fmt.Sprint(common.P95(r.samp)),
			fmt.Sprint(f.warmup), fmt.Sprint(f.samples), fmt.Sprint(f.seed),
		}); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	return 0
}

// chooseURL returns (url, close). When u is empty a local httptest
// server is started with the given injected delay. When u is non-empty
// no server is started and close is a no-op — the operator is
// responsible for the external endpoint.
func chooseURL(u string, injected time.Duration) (string, func()) {
	if u != "" {
		return u, func() {}
	}
	srv := httptest.NewServer(echoServer(injected))
	return srv.URL, srv.Close
}

// AssertLoopbackAddr is a helper used by tests to confirm that all
// sockets opened by the bench are on 127.0.0.1 / ::1 — no accidental
// outbound traffic. Uses net.SplitHostPort so an IPv6 [::1]:port survives.
func AssertLoopbackAddr(addr net.Addr) bool {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return false
	}
	return host == "127.0.0.1" || host == "::1" || host == "localhost"
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
