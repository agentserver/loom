// observer_bench measures ObserverOverhead — the wall-time cost of
// writing one probe_events span row. It runs two rounds under the same
// seed with the NoObserver ablation flag flipped between rounds; the
// difference is the overhead attributable to the observer plane.
//
// Design:
//   - Uses observerstore.OpenSQLite + NewProbeEventWriter directly against
//     a temp SQLite file (t.TempDir() in tests, --sqlite-path in real
//     runs).
//   - Two-round loop: round A with NoObserver=true (writer swapped to
//     noop; StartSpan returns nil handle → End is a no-op); round B
//     with NoObserver=false (real writer). Both rounds share the same
//     random-payload permutation so subject invariance (spec §6 (a))
//     holds.
//   - `--dry-run` prints the round plan and exits 0 without opening the
//     SQLite file.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"

	"github.com/yourorg/multi-agent/internal/observerstore"
	"github.com/yourorg/multi-agent/tools/eval/microbench/common"
)

// noObserverCurrent is the microbench-side accessor for the NoObserver
// flag value. It is a package-level indirection so tests can inject a
// stubbed value without needing a live ablation.Registry instance. The
// production wiring PR (out of this WT's file domain per spec §3.2)
// will point this at the eval-runner's Registry accessor.
var noObserverCurrent = func() bool { return false }

type flags struct {
	warmup, samples int
	seed            int64
	dryRun          bool
	sqlitePath      string
	convID          string
	outCSV          string
}

func parseFlags(args []string) (flags, error) {
	fs := flag.NewFlagSet("observer_bench", flag.ContinueOnError)
	var f flags
	fs.IntVar(&f.warmup, "warmup", 100, "warm-up iterations (>=100)")
	fs.IntVar(&f.samples, "samples", 500, "recorded samples (>=500)")
	fs.Int64Var(&f.seed, "seed", 1, "deterministic RNG seed")
	fs.BoolVar(&f.dryRun, "dry-run", false, "print plan and exit; no SQLite open")
	fs.StringVar(&f.sqlitePath, "sqlite-path", "", "sqlite file to write probe_events into (default: temp)")
	fs.StringVar(&f.convID, "conv-id", "conv-obsbench", "conversation_id for emitted spans")
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
	if !common.ValidConversationID(f.convID) {
		return f, fmt.Errorf("conv-id %q fails ValidConversationID (§6 (c))", f.convID)
	}
	return f, nil
}

func planText(f flags) string {
	var b bytes.Buffer
	fmt.Fprintf(&b, "observer_bench plan (seed=%d, warmup=%d, samples=%d)\n",
		f.seed, f.warmup, f.samples)
	fmt.Fprintf(&b, "  round A: NoObserver=true  (noop writer, span dropped)\n")
	fmt.Fprintf(&b, "  round B: NoObserver=false (real writer, span persisted)\n")
	fmt.Fprintf(&b, "  sqlite-path: %s\n", sqlitePathOrTemp(f.sqlitePath))
	return b.String()
}

func sqlitePathOrTemp(p string) string {
	if p == "" {
		return "<per-round temp file>"
	}
	return p
}

func run(args []string, stdout, stderr io.Writer) int {
	f, err := parseFlags(args)
	if err != nil {
		fmt.Fprintln(stderr, "observer_bench:", err)
		return 2
	}
	if f.dryRun {
		fmt.Fprint(stdout, planText(f))
		return 0
	}
	tmpDir := f.sqlitePath
	if tmpDir == "" {
		// Real runs without --sqlite-path use a caller-selected temp dir
		// via TMPDIR. Tests always pass an explicit path so this branch
		// is exercised only in operator hand-runs.
		td, err := os.MkdirTemp("", "observer_bench-")
		if err != nil {
			fmt.Fprintln(stderr, "observer_bench:", err)
			return 1
		}
		defer os.RemoveAll(td)
		tmpDir = filepath.Join(td, "obs.db")
	}
	st, err := observerstore.OpenSQLite(tmpDir)
	if err != nil {
		fmt.Fprintln(stderr, "observer_bench:", err)
		return 1
	}
	defer st.Close()
	realWriter := observerstore.NewProbeEventWriter(st.DB())

	// Round runner: returns []common.Sample under a given writer.
	runRound := func(w observerstore.ProbeEventWriter) ([]common.Sample, error) {
		return common.RunBench(
			common.BenchConfig{Warmup: f.warmup, Samples: f.samples, Seed: f.seed},
			func(iter int, rng *rand.Rand) (common.Sample, error) {
				h, err := w.StartSpan(observerstore.KindObserverWrite, f.convID)
				if err != nil {
					return common.Sample{}, err
				}
				start := common.Now()
				endErr := h.End(context.Background())
				dur := common.Now().Sub(start).Nanoseconds()
				if endErr != nil {
					return common.Sample{}, endErr
				}
				return common.Sample{DurationNs: dur}, nil
			},
		)
	}

	// Round A: NoObserver=true — swap in observerstore's built-in noop
	// (installed by SetProbeWriter(nil)). Its StartSpan validates then
	// returns a handle whose writer is the noop, so End is a no-op.
	prev := noObserverCurrent
	noObserverCurrent = func() bool { return true }
	writerA := chooseWriter(realWriter)
	sampA, err := runRound(writerA)
	if err != nil {
		fmt.Fprintln(stderr, "observer_bench round A:", err)
		noObserverCurrent = prev
		return 1
	}

	// Round B: NoObserver=false — use the real SQLite-backed writer.
	noObserverCurrent = func() bool { return false }
	writerB := chooseWriter(realWriter)
	sampB, err := runRound(writerB)
	noObserverCurrent = prev
	if err != nil {
		fmt.Fprintln(stderr, "observer_bench round B:", err)
		return 1
	}

	// Emit CSV.
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
		"no_observer", "p50_ns", "p95_ns", "warmup_iters", "samples", "seed",
	}); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	rows := []struct {
		label string
		samp  []common.Sample
	}{
		{"true", sampA},
		{"false", sampB},
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

// chooseWriter picks the noop or real writer based on the current
// NoObserver flag value. observerstore's built-in noop is what
// CurrentProbeWriter returns after SetProbeWriter(nil); we grab a
// reference to it once so both rounds see the same noop instance
// (identity-wise) as production would.
func chooseWriter(real observerstore.ProbeEventWriter) observerstore.ProbeEventWriter {
	if noObserverCurrent() {
		observerstore.SetProbeWriter(nil) // revert to built-in noop
		return observerstore.CurrentProbeWriter()
	}
	return real
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
