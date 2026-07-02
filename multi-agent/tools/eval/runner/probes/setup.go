package probes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const setupFile = ".probes/setup.json"
const setupMaxBytes = 4 * 1024

// EmitSetupMetrics emits ManualSetupStepCount + ConfigTouchCount per
// spec §3.7. Nil-value + explanatory label when the D6c harness has
// not landed (§7(e)).
func EmitSetupMetrics(ctx context.Context, e *Emitter, wsRoot string, stderr io.Writer) {
	if stderr == nil {
		stderr = io.Discard
	}
	path := filepath.Join(wsRoot, setupFile)
	m, reason := readSetupFile(path, stderr)
	if reason != "" {
		labels := map[string]string{"unavailable_reason": reason}
		_ = e.Emit(ctx, MetricManualSetupStepCount, nil, labels)
		_ = e.Emit(ctx, MetricConfigTouchCount, nil, labels)
		return
	}
	manual, mok := intFrom(m["manual_setup_step_count"])
	cfg, cok := intFrom(m["config_touch_count"])
	if !mok || !cok {
		fmt.Fprintf(stderr, "probes: setup.json missing/non-int fields\n")
		labels := map[string]string{"unavailable_reason": "malformed_setup_file"}
		_ = e.Emit(ctx, MetricManualSetupStepCount, nil, labels)
		_ = e.Emit(ctx, MetricConfigTouchCount, nil, labels)
		return
	}
	_ = e.Emit(ctx, MetricManualSetupStepCount, manual, map[string]string{"source": "setup_counter_file"})
	_ = e.Emit(ctx, MetricConfigTouchCount, cfg, map[string]string{"source": "setup_counter_file"})
}

func readSetupFile(path string, stderr io.Writer) (map[string]any, string) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "d6c_setup_harness_pending"
		}
		fmt.Fprintf(stderr, "probes: setup.json open: %v\n", err)
		return nil, "malformed_setup_file"
	}
	defer f.Close()
	buf := make([]byte, setupMaxBytes+1)
	read, err := io.ReadFull(f, buf)
	switch err {
	case nil:
		fmt.Fprintf(stderr, "probes: setup.json exceeds %d bytes\n", setupMaxBytes)
		return nil, "malformed_setup_file"
	case io.ErrUnexpectedEOF, io.EOF:
		buf = buf[:read]
	default:
		fmt.Fprintf(stderr, "probes: setup.json read: %v\n", err)
		return nil, "malformed_setup_file"
	}
	var m map[string]any
	if err := json.Unmarshal(buf, &m); err != nil {
		fmt.Fprintf(stderr, "probes: setup.json unmarshal: %v\n", err)
		return nil, "malformed_setup_file"
	}
	return m, ""
}

func intFrom(v any) (int, bool) {
	switch x := v.(type) {
	case float64:
		if x < 0 || x != float64(int(x)) {
			return 0, false
		}
		return int(x), true
	case int:
		if x < 0 {
			return 0, false
		}
		return x, true
	default:
		return 0, false
	}
}
