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
//
// Diagnostic writes go through emitter.Warn (bounded warn channel,
// drained off the hot path) — a slow stderr cannot block the runner
// per spec §7(a). The trailing `_ io.Writer` parameter is retained
// for call-site symmetry and future use; passing io.Discard is
// idiomatic.
func EmitSetupMetrics(ctx context.Context, e *Emitter, wsRoot string, _ io.Writer) {
	path := filepath.Join(wsRoot, setupFile)
	m, reason := readSetupFile(e, path)
	if reason != "" {
		labels := map[string]string{"unavailable_reason": reason}
		_ = e.Emit(ctx, MetricManualSetupStepCount, nil, labels)
		_ = e.Emit(ctx, MetricConfigTouchCount, nil, labels)
		return
	}
	manual, mok := intFrom(m["manual_setup_step_count"])
	cfg, cok := intFrom(m["config_touch_count"])
	if !mok || !cok {
		e.Warn("probes: setup.json missing/non-int fields")
		labels := map[string]string{"unavailable_reason": "malformed_setup_file"}
		_ = e.Emit(ctx, MetricManualSetupStepCount, nil, labels)
		_ = e.Emit(ctx, MetricConfigTouchCount, nil, labels)
		return
	}
	_ = e.Emit(ctx, MetricManualSetupStepCount, manual, map[string]string{"source": "setup_counter_file"})
	_ = e.Emit(ctx, MetricConfigTouchCount, cfg, map[string]string{"source": "setup_counter_file"})
}

func readSetupFile(e *Emitter, path string) (map[string]any, string) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "d6c_setup_harness_pending"
		}
		e.Warn(fmt.Sprintf("probes: setup.json open: %v", err))
		return nil, "malformed_setup_file"
	}
	defer f.Close()
	buf := make([]byte, setupMaxBytes+1)
	read, err := io.ReadFull(f, buf)
	switch err {
	case nil:
		e.Warn(fmt.Sprintf("probes: setup.json exceeds %d bytes", setupMaxBytes))
		return nil, "malformed_setup_file"
	case io.ErrUnexpectedEOF, io.EOF:
		buf = buf[:read]
	default:
		e.Warn(fmt.Sprintf("probes: setup.json read: %v", err))
		return nil, "malformed_setup_file"
	}
	var m map[string]any
	if err := json.Unmarshal(buf, &m); err != nil {
		e.Warn(fmt.Sprintf("probes: setup.json unmarshal: %v", err))
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
