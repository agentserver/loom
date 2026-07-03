package probes

import (
	"context"
	"io"
	"os"
	"testing"
	"time"
)

// TestPerf_EmitLatency_LocalOnly asserts Emit stays below 1 ms
// per call locally. Skipped on CI (§7(h) precedent from
// wt1-fault-injection commit e7e8897).
func TestPerf_EmitLatency_LocalOnly(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("perf assertion skipped on CI")
	}
	e := NewEmitter(0, io.Discard)
	defer e.Close()
	total := 1000
	start := time.Now()
	for i := 0; i < total; i++ {
		_ = e.Emit(context.Background(), MetricTaskSuccessRate, true, nil)
	}
	avg := time.Since(start) / time.Duration(total)
	if avg > time.Millisecond {
		t.Fatalf("avg emit latency = %s, want < 1ms", avg)
	}
}
