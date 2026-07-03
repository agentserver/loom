// Package common carries the shared microbench harness used by all
// tools/eval/microbench binaries (tunnel_bench, observer_bench,
// proxy_bench) and by tools/eval/scale_sweep.
//
// The design points that keep the paper's overhead numbers honest:
//
//   - Warm-up ≥ 100 iterations is ENFORCED (spec §6 (a)); the first
//     sample recorded is always post-warm-up.
//   - Samples ≥ 500 is ENFORCED so p95 has a meaningful denominator.
//   - Percentile computation uses the nearest-rank method (stable,
//     unambiguous, matches metric-extract's Python implementation).
//   - Two runs with the same cfg.Seed are byte-identical (deterministic
//     rand.Rand seeded from cfg.Seed; step closures must not use any
//     other randomness source).
package common

import (
	"errors"
	"log"
	"math/rand"
	"sort"
)

// ErrWarmupTooShort is returned by RunBench when cfg.Warmup < 100. The
// step closure is not invoked. See spec §6 (a).
var ErrWarmupTooShort = errors.New("microbench: warmup must be ≥ 100 iterations (cold-start latency guard)")

// ErrSamplesTooShort is returned by RunBench when cfg.Samples < 500. The
// step closure is not invoked. See spec §5.1 floor.
var ErrSamplesTooShort = errors.New("microbench: samples must be ≥ 500 for meaningful p95")

// BenchConfig is the shared configuration for all microbench binaries.
type BenchConfig struct {
	// Warmup iterations discarded before the sampler starts recording.
	// Floor 100. RunBench returns ErrWarmupTooShort on lower values.
	Warmup int

	// Samples is the number of post-warm-up iterations that ARE recorded.
	// Floor 500. RunBench returns ErrSamplesTooShort on lower values.
	Samples int

	// Seed threads deterministic randomness into every step closure via
	// the *rand.Rand passed as the third argument to StepFunc. Two runs
	// with the same Seed yield byte-identical Sample slices.
	Seed int64

	// DryRun is a per-binary opt-out from real work; RunBench does not
	// consume it (each binary honours DryRun in its main).
	DryRun bool

	// OutCSV / ProbeDB are per-binary sink hints; RunBench does not
	// consume them.
	OutCSV  string
	ProbeDB string
}

// Sample carries one recorded measurement. Bytes is 0 for latency-only
// benches (tunnel ping, observer write, proxy round trip); it is > 0 for
// throughput samples (transfer bench sizing point).
type Sample struct {
	DurationNs int64
	Bytes      int64
}

// StepFunc is the closure a bench binary passes to RunBench. It receives
// the current iteration index (0..Warmup+Samples-1) and a *rand.Rand
// seeded deterministically from cfg.Seed. It must not use any other
// randomness source or the "same subject / same seed" invariant breaks
// (spec §6 (a)).
type StepFunc func(iter int, rng *rand.Rand) (Sample, error)

// RunBench runs cfg.Warmup + cfg.Samples iterations of step and returns
// the last cfg.Samples measurements. Warm-up samples are NEVER included
// in the return slice; the sampler index only starts incrementing after
// warm-up completes.
//
// On any step error post-warm-up, RunBench returns nil samples and an
// error annotated with the iteration index; partial slices are never
// returned so a caller cannot accidentally compute a p50 over an
// incomplete distribution.
func RunBench(cfg BenchConfig, step StepFunc) ([]Sample, error) {
	if cfg.Warmup < 100 {
		return nil, ErrWarmupTooShort
	}
	if cfg.Samples < 500 {
		return nil, ErrSamplesTooShort
	}
	// #nosec G404 -- deterministic seeded rng for reproducibility, not crypto.
	rng := rand.New(rand.NewSource(cfg.Seed))
	// Warm-up loop: throw away results but propagate errors so an
	// obviously-broken step surfaces immediately.
	for i := 0; i < cfg.Warmup; i++ {
		if _, err := step(i, rng); err != nil {
			return nil, wrapIter(i, err)
		}
	}
	samples := make([]Sample, 0, cfg.Samples)
	for i := 0; i < cfg.Samples; i++ {
		s, err := step(cfg.Warmup+i, rng)
		if err != nil {
			return nil, wrapIter(cfg.Warmup+i, err)
		}
		samples = append(samples, s)
	}
	return samples, nil
}

type iterErr struct {
	iter int
	err  error
}

func (e *iterErr) Error() string { return e.err.Error() }
func (e *iterErr) Unwrap() error { return e.err }

// Iter returns the 0-based iteration index at which the wrapped error
// occurred. Useful in tests + operator diagnostics.
func (e *iterErr) Iter() int { return e.iter }

func wrapIter(iter int, err error) error {
	return &iterErr{iter: iter, err: err}
}

// P50 returns the 50th-percentile DurationNs using the nearest-rank
// method (rank = ceil(50/100 * N)). Returns 0 and logs a warning on
// empty input; never panics.
func P50(samples []Sample) int64 { return percentile(samples, 50) }

// P95 returns the 95th-percentile DurationNs; same contract as P50.
func P95(samples []Sample) int64 { return percentile(samples, 95) }

func percentile(samples []Sample, p int) int64 {
	if len(samples) == 0 {
		log.Printf("microbench: percentile called on empty sample slice; returning 0")
		return 0
	}
	sorted := make([]int64, len(samples))
	for i, s := range samples {
		sorted[i] = s.DurationNs
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	// Nearest-rank: rank = ceil(p/100 * N), 1-indexed → index = rank-1.
	rank := (p*len(sorted) + 99) / 100 // ceil-division
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// BytesP50 / BytesP95 mirror P50 / P95 for the Bytes field — used by the
// transfer bench for throughput distributions expressed as bytes/second.
// A sample with DurationNs == 0 yields Inf bytes/sec; those samples are
// dropped from the distribution (safer than reporting +Inf p95). A slice
// of all-zero-duration samples is treated as "no measurable throughput";
// returns 0.
func BytesP50(samples []Sample) int64 { return bytesPercentile(samples, 50) }
func BytesP95(samples []Sample) int64 { return bytesPercentile(samples, 95) }

func bytesPercentile(samples []Sample, p int) int64 {
	bps := make([]int64, 0, len(samples))
	for _, s := range samples {
		if s.DurationNs <= 0 || s.Bytes <= 0 {
			continue
		}
		bps = append(bps, s.Bytes*int64(1e9)/s.DurationNs)
	}
	if len(bps) == 0 {
		log.Printf("microbench: no measurable throughput samples")
		return 0
	}
	sort.Slice(bps, func(i, j int) bool { return bps[i] < bps[j] })
	rank := (p*len(bps) + 99) / 100
	if rank < 1 {
		rank = 1
	}
	if rank > len(bps) {
		rank = len(bps)
	}
	return bps[rank-1]
}
