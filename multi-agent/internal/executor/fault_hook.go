// Package-internal fault-injection hook shim for the executor.
//
// Mirror of internal/driver/fault_hook.go. No build tag — present in
// every production binary, default Hook is nil, fast path is a single
// atomic load + nil check (target: <100ns/op). See the driver file for
// the full design rationale.

package executor

import (
	"context"
	"sync/atomic"
)

// HookPoint identifies an executor call site that probes the fault
// injector.
type HookPoint string

const (
	HookPointExecutorFileOpen    HookPoint = "executor.file_open"
	HookPointExecutorCredResolve HookPoint = "executor.cred_resolve"
)

// Hook is the executor-side fault-injection callback. See the driver
// package's Hook for documentation.
type Hook func(ctx context.Context, runID string, hp HookPoint, meta map[string]string) error

var hook atomic.Pointer[Hook]

// SetHook installs h and returns the previously-installed hook (or nil).
// Passing nil restores the noop fast path.
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

// InjectIfActive is the executor-side call-site probe. See the driver
// package for documentation; the implementation is identical.
func InjectIfActive(ctx context.Context, runID string, hp HookPoint, meta map[string]string) error {
	h := hook.Load()
	if h == nil {
		return nil
	}
	return (*h)(ctx, runID, hp, meta)
}
