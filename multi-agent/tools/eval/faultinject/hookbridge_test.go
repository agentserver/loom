//go:build evaltool

package faultinject

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yourorg/multi-agent/internal/driver"
	"github.com/yourorg/multi-agent/internal/executor"
)

// installFreshBridge installs a bridge backed by a fresh Store and an
// in-memory audit buffer. Returns store + audit buffer + closer.
func installFreshBridge(t *testing.T) (*Store, *bytes.Buffer, func()) {
	t.Helper()
	store := NewStore()
	var auditBuf bytes.Buffer
	audit := NewAuditWriter(&auditBuf, nil)
	closer := Install(store, audit)
	t.Cleanup(closer)
	return store, &auditBuf, closer
}

// F19 (slot 1): missing_file → *os.PathError(fs.ErrNotExist).
func TestHookBridge_MissingFile(t *testing.T) {
	store, _, _ := installFreshBridge(t)
	const runID = "run-miss0001"
	if _, err := store.Add(runID, FaultMissingFile, "ignored.txt", nil); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got := executor.InjectIfActive(context.Background(), runID, executor.HookPointExecutorFileOpen, map[string]string{"path": "x.txt"})
	var pe *os.PathError
	if !errors.As(got, &pe) {
		t.Fatalf("err = %T (%v), want *os.PathError", got, got)
	}
	if !errors.Is(pe.Err, fs.ErrNotExist) {
		t.Errorf("PathError.Err = %v, want fs.ErrNotExist", pe.Err)
	}
	if pe.Path != "x.txt" {
		t.Errorf("PathError.Path = %q, want %q (meta wins over target)", pe.Path, "x.txt")
	}
}

// F19 (slot 2): stale_capability → *CapabilityFault{Kind:Stale}.
func TestHookBridge_StaleCapability(t *testing.T) {
	store, _, _ := installFreshBridge(t)
	const runID = "run-stale001"
	if _, err := store.Add(runID, FaultStaleCapability, "", nil); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got := driver.InjectIfActive(context.Background(), runID, driver.HookPointDriverCapabilityRead, nil)
	var cf *CapabilityFault
	if !errors.As(got, &cf) {
		t.Fatalf("err = %T (%v), want *CapabilityFault", got, got)
	}
	if cf.Kind != FaultStaleCapability {
		t.Errorf("CapabilityFault.Kind = %q, want stale_capability", cf.Kind)
	}
	field, val := SubstituteCapability(cf)
	if field != "hash" || val != StaleCapabilityValue {
		t.Errorf("SubstituteCapability = (%q,%q), want (hash, %q)", field, val, StaleCapabilityValue)
	}
}

// F19 (slot 3): wrong_os_version → *CapabilityFault{Kind:WrongOSVersion}.
func TestHookBridge_WrongOSVersion(t *testing.T) {
	store, _, _ := installFreshBridge(t)
	const runID = "run-wrongos1"
	if _, err := store.Add(runID, FaultWrongOSVersion, "", nil); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got := driver.InjectIfActive(context.Background(), runID, driver.HookPointDriverCapabilityRead, nil)
	var cf *CapabilityFault
	if !errors.As(got, &cf) {
		t.Fatalf("err = %T, want *CapabilityFault", got)
	}
	if cf.Kind != FaultWrongOSVersion {
		t.Errorf("Kind = %q, want wrong_os_version", cf.Kind)
	}
	field, val := SubstituteCapability(cf)
	if field != "os" || val != WrongOSVersionValue {
		t.Errorf("SubstituteCapability = (%q,%q), want (os, %q)", field, val, WrongOSVersionValue)
	}
}

// F19 (slot 4) + F21: forbidden_cred returns sentinel literal — byte equality.
func TestHookBridge_ForbiddenCred_SentinelOnly(t *testing.T) {
	store, _, _ := installFreshBridge(t)
	const runID = "run-cred0001"
	if _, err := store.Add(runID, FaultForbiddenCred, "", nil); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got := executor.InjectIfActive(context.Background(), runID, executor.HookPointExecutorCredResolve, nil)
	var fc *ForbiddenCredFault
	if !errors.As(got, &fc) {
		t.Fatalf("err = %T (%v), want *ForbiddenCredFault", got, got)
	}
	if fc.Cred != "FAKE_CRED_FOR_INJECTION_DO_NOT_USE" {
		t.Fatalf("Cred = %q, want literal sentinel", fc.Cred)
	}
	// Also test the driver-side cred resolve hook.
	gotDr := driver.InjectIfActive(context.Background(), runID, driver.HookPointDriverCredResolve, nil)
	var fc2 *ForbiddenCredFault
	if !errors.As(gotDr, &fc2) {
		t.Fatalf("driver cred err = %T (%v), want *ForbiddenCredFault", gotDr, gotDr)
	}
	if fc2.Cred != SentinelFakeCred {
		t.Errorf("driver Cred = %q, want SentinelFakeCred", fc2.Cred)
	}
}

// F19 (slot 5): slave_disconnect returns ErrFaultSlaveDisconnect.
func TestHookBridge_SlaveDisconnect(t *testing.T) {
	store, _, _ := installFreshBridge(t)
	const runID = "run-disc0001"
	if _, err := store.Add(runID, FaultSlaveDisconnect, "", nil); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got := driver.InjectIfActive(context.Background(), runID, driver.HookPointSlaveHeartbeat, nil)
	if !errors.Is(got, ErrFaultSlaveDisconnect) {
		t.Fatalf("err = %v, want ErrFaultSlaveDisconnect wrap", got)
	}
}

// F19 (slot 6): driver_restart panics with ErrFaultDriverRestart wrap.
func TestHookBridge_DriverRestart_Panics(t *testing.T) {
	store, _, _ := installFreshBridge(t)
	const runID = "run-rest0001"
	if _, err := store.Add(runID, FaultDriverRestart, "", nil); err != nil {
		t.Fatalf("Add: %v", err)
	}
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected panic, got none")
		}
		err, ok := r.(error)
		if !ok {
			t.Fatalf("panic value type = %T, want error", r)
		}
		if !errors.Is(err, ErrFaultDriverRestart) {
			t.Fatalf("panic err = %v, want ErrFaultDriverRestart wrap", err)
		}
	}()
	_ = driver.InjectIfActive(context.Background(), runID, driver.HookPointDriverMainLoop, nil)
}

// F19 (slot 7): model_route_failure → ErrFaultModelRoute503.
func TestHookBridge_ModelRouteFailure(t *testing.T) {
	store, _, _ := installFreshBridge(t)
	const runID = "run-route001"
	if _, err := store.Add(runID, FaultModelRouteFailure, "", nil); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got := driver.InjectIfActive(context.Background(), runID, driver.HookPointDriverModelRoute, nil)
	if !errors.Is(got, ErrFaultModelRoute503) {
		t.Fatalf("err = %v, want ErrFaultModelRoute503", got)
	}
}

// F19 (slot 8): duplicate_pickup → ErrFaultDuplicatePickup; caller MUST
// take dedup branch.
// F20: caller must NOT replay the agent command — assert via a counter
// fixture that exec_count == 1 even though the pickup hook fires.
func TestHookBridge_DuplicatePickup_NoCommandReplay(t *testing.T) {
	store, _, _ := installFreshBridge(t)
	const runID = "run-dup00001"
	if _, err := store.Add(runID, FaultDuplicatePickup, "", nil); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Fixture: a "dispatch" function that increments execCount only
	// when it actually runs the command. On a duplicate-pickup error it
	// MUST take the dedup branch and skip execution.
	var execCount atomic.Int32
	dispatch := func(idempotencyKey string) error {
		if err := driver.InjectIfActive(context.Background(), runID, driver.HookPointDriverPickup, map[string]string{"idem": idempotencyKey}); err != nil {
			if errors.Is(err, ErrFaultDuplicatePickup) {
				// Dedup branch — see spec §7 (d). Reuse idempotency key
				// to look up existing dispatch state; never replay the
				// raw command.
				return nil
			}
			return err
		}
		execCount.Add(1)
		return nil
	}
	// First call before any inject would normally increment; but inject
	// is registered ahead of time so the very first pickup fires the
	// fault and dedup branch is taken.
	if err := dispatch("idemkey-1"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if got := execCount.Load(); got != 0 {
		t.Fatalf("execCount after duplicate-pickup fault = %d, want 0 (dedup branch must skip replay)", got)
	}

	// Now clear the fault and dispatch again — exec must run exactly once.
	if _, err := store.Clear(runID); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if err := dispatch("idemkey-1"); err != nil {
		t.Fatalf("dispatch after clear: %v", err)
	}
	if got := execCount.Load(); got != 1 {
		t.Fatalf("execCount after clear = %d, want 1", got)
	}
}

// F16: every fault fire emits exactly one audit line containing
// run_id/kind/hook/action="injected".
func TestHookBridge_AuditLogEveryFire(t *testing.T) {
	store, audit, _ := installFreshBridge(t)
	const runID = "run-audit002"
	for _, kind := range []FaultKind{FaultMissingFile, FaultMissingFile, FaultMissingFile} {
		if _, err := store.Add(runID, kind, "", nil); err != nil {
			t.Fatalf("Add %s: %v", kind, err)
		}
	}
	// Fire 3 times by calling the executor hook 3 times — even though
	// Lookup returns the same first directive, each call audits.
	for i := 0; i < 3; i++ {
		_ = executor.InjectIfActive(context.Background(), runID, executor.HookPointExecutorFileOpen, map[string]string{"path": "f"})
	}
	lines := bytes.Split(bytes.TrimRight(audit.Bytes(), "\n"), []byte{'\n'})
	if len(lines) != 3 {
		t.Fatalf("audit lines = %d, want 3; got:\n%s", len(lines), audit.String())
	}
	for i, line := range lines {
		if !strings.Contains(string(line), `"run_id":"`+runID+`"`) {
			t.Errorf("line %d missing run_id: %s", i, line)
		}
		if !strings.Contains(string(line), `"action":"injected"`) {
			t.Errorf("line %d missing action=injected: %s", i, line)
		}
		if !strings.Contains(string(line), `"hook":"executor.file_open"`) {
			t.Errorf("line %d missing hook: %s", i, line)
		}
	}
}

// F19 (matrix sanity): the 8 kinds round-trip through the bridge with
// one assertion per kind. Sub-tests above cover the per-kind detail;
// this one is the at-a-glance proof of coverage.
func TestHookBridge_AllEightKinds_MatrixSmoke(t *testing.T) {
	cases := []struct {
		kind FaultKind
		fire func(runID string) (caught error, panicked any)
	}{
		{FaultMissingFile, func(r string) (error, any) {
			return executor.InjectIfActive(context.Background(), r, executor.HookPointExecutorFileOpen, nil), nil
		}},
		{FaultStaleCapability, func(r string) (error, any) {
			return driver.InjectIfActive(context.Background(), r, driver.HookPointDriverCapabilityRead, nil), nil
		}},
		{FaultWrongOSVersion, func(r string) (error, any) {
			return driver.InjectIfActive(context.Background(), r, driver.HookPointDriverCapabilityRead, nil), nil
		}},
		{FaultForbiddenCred, func(r string) (error, any) {
			return executor.InjectIfActive(context.Background(), r, executor.HookPointExecutorCredResolve, nil), nil
		}},
		{FaultSlaveDisconnect, func(r string) (error, any) {
			return driver.InjectIfActive(context.Background(), r, driver.HookPointSlaveHeartbeat, nil), nil
		}},
		{FaultDriverRestart, func(r string) (err error, panicked any) {
			defer func() { panicked = recover() }()
			err = driver.InjectIfActive(context.Background(), r, driver.HookPointDriverMainLoop, nil)
			return
		}},
		{FaultModelRouteFailure, func(r string) (error, any) {
			return driver.InjectIfActive(context.Background(), r, driver.HookPointDriverModelRoute, nil), nil
		}},
		{FaultDuplicatePickup, func(r string) (error, any) {
			return driver.InjectIfActive(context.Background(), r, driver.HookPointDriverPickup, nil), nil
		}},
	}
	if len(cases) != len(AllFaultKinds) {
		t.Fatalf("matrix has %d cases, want %d (AllFaultKinds)", len(cases), len(AllFaultKinds))
	}
	for _, c := range cases {
		t.Run(string(c.kind), func(t *testing.T) {
			store, _, closer := installFreshBridge(t)
			runID := "run-mx-" + strings.ReplaceAll(string(c.kind), "_", "")
			if len(runID) < 8 { // ValidateRunID requires ≥8
				runID = runID + "00000000"
			}
			if len(runID) > 128 {
				runID = runID[:128]
			}
			if _, err := store.Add(runID, c.kind, "", nil); err != nil {
				t.Fatalf("Add: %v", err)
			}
			caught, panicked := c.fire(runID)
			if c.kind == FaultDriverRestart {
				if panicked == nil {
					t.Errorf("kind %s: expected panic", c.kind)
				}
			} else {
				if caught == nil {
					t.Errorf("kind %s: expected error, got nil", c.kind)
				}
			}
			closer()
		})
	}
}

// Belt: nested Install/closer stack works LIFO so concurrent tests do
// not corrupt each other's bridge state. Active hook is always the
// top of the install stack.
func TestHookBridge_NestedInstallStacksLIFO(t *testing.T) {
	store1 := NewStore()
	audit1 := NewAuditWriter(nil, nil)
	c1 := Install(store1, audit1)

	store2 := NewStore()
	audit2 := NewAuditWriter(nil, nil)
	c2 := Install(store2, audit2)

	// Active store is store2: an inject on store2 fires; same kind on
	// store1 (which is hidden) does not.
	const runID = "run-nest0001"
	if _, err := store2.Add(runID, FaultMissingFile, "", nil); err != nil {
		t.Fatalf("Add store2: %v", err)
	}
	if err := executor.InjectIfActive(context.Background(), runID, executor.HookPointExecutorFileOpen, nil); err == nil {
		t.Fatalf("want fault to fire on store2, got nil")
	}

	// Pop store2; store1 becomes active. Same runID/kind not in store1.
	c2()
	if err := executor.InjectIfActive(context.Background(), runID, executor.HookPointExecutorFileOpen, nil); err != nil {
		t.Fatalf("after c2() bridge2 is gone but store1 has no faults; want nil, got %v", err)
	}

	// Pop store1; we should be back to whatever was active before the
	// outer Install (typically nil in test isolation).
	c1()
	if err := executor.InjectIfActive(context.Background(), runID, executor.HookPointExecutorFileOpen, nil); err != nil {
		t.Fatalf("after c1() pop: want nil, got %v", err)
	}
}

// New (round-2 reviewer P1): non-LIFO close must not silently uninstall
// a still-active inner bridge. The stack-based detach guarantees that
// removing a middle bridge leaves the top bridge active.
func TestHookBridge_NonLIFOClose_PreservesActiveBridge(t *testing.T) {
	// Install A, then Install B. B is active (top of stack).
	storeA := NewStore()
	cA := Install(storeA, NewAuditWriter(nil, nil))

	storeB := NewStore()
	cB := Install(storeB, NewAuditWriter(nil, nil))

	const runID = "run-nonlifo01"
	if _, err := storeB.Add(runID, FaultMissingFile, "", nil); err != nil {
		t.Fatalf("Add storeB: %v", err)
	}

	// Close A FIRST (out of order). B should still be active.
	cA()

	// Fault on storeB must still fire.
	if err := executor.InjectIfActive(context.Background(), runID, executor.HookPointExecutorFileOpen, nil); err == nil {
		t.Fatalf("after non-LIFO close of A: storeB's fault did not fire (B was silently uninstalled)")
	}

	// Close B; we're back to the pre-install state (no hook).
	cB()
	if err := executor.InjectIfActive(context.Background(), runID, executor.HookPointExecutorFileOpen, nil); err != nil {
		t.Fatalf("after both closers: want nil, got %v", err)
	}
}

// New (round-3 reviewer P2): nil audit must not crash the hook path —
// Install replaces nil with a default writer.
func TestHookBridge_NilAuditIsSubstituted(t *testing.T) {
	store := NewStore()
	closer := Install(store, nil) // intentionally nil
	defer closer()
	const runID = "run-nilaud01"
	if _, err := store.Add(runID, FaultModelRouteFailure, "", nil); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Fire the hook; the bridge's EmitInjected must not panic.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil audit caused panic: %v", r)
		}
	}()
	got := driver.InjectIfActive(context.Background(), runID, driver.HookPointDriverModelRoute, nil)
	if !errors.Is(got, ErrFaultModelRoute503) {
		t.Errorf("err = %v, want ErrFaultModelRoute503", got)
	}
}

// New (round-2 reviewer P2): double-call closer is idempotent.
func TestHookBridge_DoubleCloseIsNoOp(t *testing.T) {
	store := NewStore()
	closer := Install(store, NewAuditWriter(nil, nil))
	closer()
	closer() // must not panic, must not corrupt the next Install
	// A fresh install must still work cleanly.
	c2 := Install(store, NewAuditWriter(nil, nil))
	const runID = "run-double001"
	if _, err := store.Add(runID, FaultMissingFile, "", nil); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := executor.InjectIfActive(context.Background(), runID, executor.HookPointExecutorFileOpen, nil); err == nil {
		t.Fatalf("fault did not fire after double-close + reinstall")
	}
	c2()
}
