package common

import (
	"errors"
	"math/rand"
	"testing"
)

// Test #23 — Warmup < 100 rejected without invoking step.
func TestRunBench_RejectsShortWarmup(t *testing.T) {
	invoked := 0
	_, err := RunBench(BenchConfig{Warmup: 99, Samples: 500}, func(int, *rand.Rand) (Sample, error) {
		invoked++
		return Sample{}, nil
	})
	if !errors.Is(err, ErrWarmupTooShort) {
		t.Fatalf("want ErrWarmupTooShort, got %v", err)
	}
	if invoked != 0 {
		t.Errorf("step should not run when warmup floor fails, got %d invocations", invoked)
	}
}

// Test #24 — warm-up samples excluded from the return slice.
//
// Design: the step returns DurationNs = iter*1e6 so warm-up iters produce
// 0..99 ms and the first (and only) sampled iter produces exactly 100 ms.
// If any warm-up sample leaks into the sampler slice, samples[0] would
// be < 100_000_000.
func TestRunBench_WarmupSamplesExcluded(t *testing.T) {
	samples, err := RunBench(
		BenchConfig{Warmup: 100, Samples: 500},
		func(iter int, _ *rand.Rand) (Sample, error) {
			return Sample{DurationNs: int64(iter) * 1_000_000}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 500 {
		t.Fatalf("want 500 samples, got %d", len(samples))
	}
	if samples[0].DurationNs != 100_000_000 {
		t.Errorf("first sampled iter should be iter=100 (100_000_000 ns); got %d",
			samples[0].DurationNs)
	}
	if samples[499].DurationNs != 599_000_000 {
		t.Errorf("last sampled iter should be iter=599 (599_000_000 ns); got %d",
			samples[499].DurationNs)
	}
}

// Test #25 — same seed → byte-identical output.
func TestRunBench_DeterministicUnderSeed(t *testing.T) {
	step := func(iter int, rng *rand.Rand) (Sample, error) {
		// Draw a rand int so the rng state matters to the result.
		return Sample{DurationNs: int64(iter) + rng.Int63n(1000)}, nil
	}
	cfg := BenchConfig{Warmup: 100, Samples: 500, Seed: 42}
	a, err := RunBench(cfg, step)
	if err != nil {
		t.Fatal(err)
	}
	b, err := RunBench(cfg, step)
	if err != nil {
		t.Fatal(err)
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("determinism broken at iter %d: %+v vs %+v", i, a[i], b[i])
		}
	}
}

// Test #26 — P50/P95 nearest-rank golden.
func TestP50P95_TableDriven(t *testing.T) {
	// Empty slice: 0 (and logs a warning). Assert no panic.
	if got := P50(nil); got != 0 {
		t.Errorf("P50(nil) = %d, want 0", got)
	}
	if got := P95(nil); got != 0 {
		t.Errorf("P95(nil) = %d, want 0", got)
	}
	// 100 samples 1..100 (ns): nearest-rank p50 = 50 (rank 50), p95 = 95.
	xs := make([]Sample, 100)
	for i := range xs {
		xs[i] = Sample{DurationNs: int64(i + 1)}
	}
	if got := P50(xs); got != 50 {
		t.Errorf("P50 want 50 got %d", got)
	}
	if got := P95(xs); got != 95 {
		t.Errorf("P95 want 95 got %d", got)
	}
	// Single-sample: p50 == p95 == that sample.
	one := []Sample{{DurationNs: 7}}
	if got := P50(one); got != 7 {
		t.Errorf("P50([7]) = %d", got)
	}
	if got := P95(one); got != 7 {
		t.Errorf("P95([7]) = %d", got)
	}
}

// Test #27 — post-warmup error surfaces with iter number; partial slice
// discarded.
func TestRunBench_StepErrorPropagates(t *testing.T) {
	fail := errors.New("boom")
	samples, err := RunBench(
		BenchConfig{Warmup: 100, Samples: 500},
		func(iter int, _ *rand.Rand) (Sample, error) {
			if iter == 150 { // 50 post-warmup
				return Sample{}, fail
			}
			return Sample{DurationNs: 1}, nil
		},
	)
	if err == nil {
		t.Fatalf("expected err at iter 150")
	}
	if !errors.Is(err, fail) {
		t.Errorf("wrapped error should unwrap to sentinel; got %v", err)
	}
	if samples != nil {
		t.Errorf("partial slice should be nil, got len %d", len(samples))
	}
	var ie *iterErr
	if !errors.As(err, &ie) {
		t.Fatalf("err should be *iterErr")
	}
	if ie.Iter() != 150 {
		t.Errorf("Iter() want 150 got %d", ie.Iter())
	}
}

// Test #28 — Samples < 500 rejected.
func TestRunBench_MinSamplesEnforced(t *testing.T) {
	_, err := RunBench(
		BenchConfig{Warmup: 100, Samples: 499},
		func(int, *rand.Rand) (Sample, error) { return Sample{}, nil },
	)
	if !errors.Is(err, ErrSamplesTooShort) {
		t.Errorf("want ErrSamplesTooShort, got %v", err)
	}
	// 500 accepted.
	_, err = RunBench(
		BenchConfig{Warmup: 100, Samples: 500},
		func(int, *rand.Rand) (Sample, error) { return Sample{DurationNs: 1}, nil },
	)
	if err != nil {
		t.Errorf("Samples=500 rejected: %v", err)
	}
}
