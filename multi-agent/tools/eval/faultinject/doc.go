//go:build evaltool

// Package faultinject is the eval-only fault injection harness for the
// driver/executor failure-path matrix (Phase 1 D5). It exposes:
//
//   - 8 wire-format FaultKind constants and the AllFaultKinds slice.
//   - A loopback-only HTTP control plane (POST /inject, POST /clear,
//     GET /list) listening on 127.0.0.1:18189 by default.
//   - An in-memory Store keyed by run_id with a per-(run_id, kind) rate
//     limit (MaxInjectionsPerRun = 100).
//   - A bridge (Install) that wires the store into driver.SetHook and
//     executor.SetHook so InjectIfActive calls in those packages observe
//     the registered faults.
//   - An audit writer that emits one structured JSON line to its sink
//     (default os.Stderr) on every fault registration and every hook
//     fire — silent injection is a P0 review failure.
//
// The package has a //go:build evaltool tag on every file. Production
// builds of cmd/driver-agent et al do not pass -tags=evaltool, so the
// linker drops faultinject entirely and the driver/executor fault_hook
// shims stay at their default nil-hook noop fast path (<100ns/op).
//
// See docs/specs/wt1-fault-injection.spec.md for the full security
// model (§7 a–h) and the integration acceptance criteria.
package faultinject
