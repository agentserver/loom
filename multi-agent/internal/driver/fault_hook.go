// Package-internal fault-injection hook shim.
//
// This file is intentionally minimal and has NO build tag — it is
// compiled into every production binary. The default Hook is nil, so
// every InjectIfActive call collapses to a single atomic pointer load
// plus a nil check (target: <100ns/op; see fault_hook_test.go::
// BenchmarkFaultHook_NoopFastPath).
//
// Eval-only builds (tagged `evaltool`) import
// `tools/eval/faultinject` and call SetHook(...) from a runner main,
// at which point InjectIfActive starts dispatching to the registered
// implementation. Production binaries never import faultinject, so the
// linker drops it and the hook stays at noop forever.

package driver

import (
	"context"
	"sync/atomic"
)

// HookPoint identifies a call site that probes the fault injector.
// Constants live alongside the call sites they label so a reader who
// greps for the constant lands on the failure point.
type HookPoint string

const (
	HookPointDriverPickup         HookPoint = "driver.pickup"
	HookPointDriverCapabilityRead HookPoint = "driver.capability_read"
	HookPointDriverCredResolve    HookPoint = "driver.cred_resolve"
	HookPointDriverModelRoute     HookPoint = "driver.model_route"
	HookPointDriverMainLoop       HookPoint = "driver.main_loop"
	// HookPointSlaveHeartbeat is registered here because the slave heartbeat
	// arrives at the driver and the slave-disconnect fault closes the
	// driver-side conn. The slave process itself does not import this hook.
	HookPointSlaveHeartbeat HookPoint = "slave.heartbeat"
)

// Hook is invoked from every call site instrumented with InjectIfActive.
// A nil return advances the caller's normal path; a non-nil error is
// returned from the call site as-if the failure occurred organically.
//
// The meta map carries call-site-specific context (path being opened,
// capability hash being read, etc.). It MAY be nil; bridges treat nil as
// an empty map.
type Hook func(ctx context.Context, runID string, hp HookPoint, meta map[string]string) error

// hook holds the currently-installed implementation. atomic.Pointer
// keeps the fast path lock-free; SetHook swaps in/out.
var hook atomic.Pointer[Hook]

// SetHook installs h and returns the previously-installed hook (or nil).
// Passing nil restores the noop fast path. SetHook is safe for concurrent
// use; tests rely on it for setup/teardown.
func SetHook(h Hook) Hook {
	if h == nil {
		old := hook.Swap(nil)
		if old == nil {
			return nil
		}
		return *old
	}
	old := hook.Swap(&h)
	if old == nil {
		return nil
	}
	return *old
}

// InjectIfActive is the call-site probe. With no hook installed it is a
// single atomic.Pointer load plus a nil check — well under the spec
// §6.1 budget of 100ns/op. Production binaries never load a hook, so
// they pay only this cost.
//
// With a hook installed, InjectIfActive returns whatever the hook
// returns; an error from the hook is propagated to the caller as the
// failure for that site.
func InjectIfActive(ctx context.Context, runID string, hp HookPoint, meta map[string]string) error {
	h := hook.Load()
	if h == nil {
		return nil
	}
	return (*h)(ctx, runID, hp, meta)
}
