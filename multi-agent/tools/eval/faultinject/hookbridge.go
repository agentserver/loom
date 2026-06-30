//go:build evaltool

package faultinject

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"sync"

	"github.com/yourorg/multi-agent/internal/driver"
	"github.com/yourorg/multi-agent/internal/executor"
)

// Synthetic errors returned by the hook bridge when a fault fires. They
// are exported so integration tests / driver+executor consumers can
// errors.Is on them. The wrapped underlying error mirrors what the
// failing call-site would normally produce so caller code can keep its
// existing classification.
var (
	// ErrFaultDriverRestart panics out of HookPointDriverMainLoop; the
	// runner is responsible for catching the panic (test-only).
	ErrFaultDriverRestart = errors.New("faultinject: driver_restart triggered")

	// ErrFaultSlaveDisconnect is returned from HookPointSlaveHeartbeat.
	// In the integration script the fixture closes its own TCP conn on
	// top of this; the error keeps the failure observable from a unit
	// test that does not own a socket.
	ErrFaultSlaveDisconnect = errors.New("faultinject: slave_disconnect triggered")

	// ErrFaultModelRoute503 is returned from HookPointDriverModelRoute
	// and represents a synthetic HTTP 503 from the model gateway.
	ErrFaultModelRoute503 = fmt.Errorf("faultinject: model_route_failure 503 (%d)", http.StatusServiceUnavailable)

	// ErrFaultDuplicatePickup is returned from HookPointDriverPickup
	// when the duplicate-pickup fault fires. Callers MUST take the
	// dedup branch (re-use the idempotency key) and MUST NOT replay
	// the agent command. See spec §7 (d).
	ErrFaultDuplicatePickup = errors.New("faultinject: duplicate_pickup — take dedup branch, do NOT replay command")
)

// StaleCapabilityValue and WrongOSVersionValue are inspectable via the
// bridge for callers that need to know what value to substitute when a
// capability-related fault fires. Tests assert byte-equality.
const (
	StaleCapabilityValue = "STALE_HASH_FROM_FAULTINJECTION"
	WrongOSVersionValue  = "darwin"
)

// bridge is the live binding from a Store into the driver/executor
// hook setters. Install / Uninstall are mutex-serialised so tests cannot
// double-install accidentally.
type bridge struct {
	mu     sync.Mutex
	store  *Store
	audit  *AuditWriter
	prevDr driver.Hook
	prevEx executor.Hook
	on     bool
}

// Install wires store + audit into driver.SetHook + executor.SetHook.
// Returns a closer that restores the previously-installed hooks (or
// the noop fast path if there were none). Calls stack LIFO: nesting
// `c2 := Install(...); ...; c2()` inside another `c1 := Install(...);
// ...; c1()` works correctly because each bridge captures the hook
// that was active at the moment it ran.
func Install(store *Store, audit *AuditWriter) func() {
	b := &bridge{store: store, audit: audit}
	b.attach()
	return b.detach
}

func (b *bridge) attach() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.prevDr = driver.SetHook(b.driverHook)
	b.prevEx = executor.SetHook(b.executorHook)
	b.on = true
}

func (b *bridge) detach() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.on {
		return
	}
	driver.SetHook(b.prevDr)
	executor.SetHook(b.prevEx)
	b.on = false
}

// driverHook dispatches on HookPoint → FaultKind. The mapping mirrors
// spec §3.1.
func (b *bridge) driverHook(_ context.Context, runID string, hp driver.HookPoint, _ map[string]string) error {
	// Capability-read listens for two kinds; check both.
	if hp == driver.HookPointDriverCapabilityRead {
		for _, kind := range []FaultKind{FaultStaleCapability, FaultWrongOSVersion} {
			if d, ok := b.store.Lookup(runID, kind); ok {
				b.audit.EmitInjected(runID, kind, string(hp), d.Seq)
				return &CapabilityFault{Kind: kind, RunID: runID}
			}
		}
		return nil
	}
	kind := driverHookKind(hp)
	if kind == "" {
		return nil
	}
	d, ok := b.store.Lookup(runID, kind)
	if !ok {
		return nil
	}
	b.audit.EmitInjected(runID, kind, string(hp), d.Seq)
	switch kind {
	case FaultDriverRestart:
		panic(fmt.Errorf("%w: run_id=%s", ErrFaultDriverRestart, runID))
	case FaultSlaveDisconnect:
		return fmt.Errorf("%w: run_id=%s", ErrFaultSlaveDisconnect, runID)
	case FaultModelRouteFailure:
		return ErrFaultModelRoute503
	case FaultDuplicatePickup:
		return ErrFaultDuplicatePickup
	case FaultForbiddenCred:
		return &ForbiddenCredFault{RunID: runID, Cred: SentinelFakeCred}
	}
	return nil
}

func (b *bridge) executorHook(_ context.Context, runID string, hp executor.HookPoint, meta map[string]string) error {
	kind := executorHookKind(hp)
	if kind == "" {
		return nil
	}
	d, ok := b.store.Lookup(runID, kind)
	if !ok {
		return nil
	}
	b.audit.EmitInjected(runID, kind, string(hp), d.Seq)
	switch kind {
	case FaultMissingFile:
		target := d.Target
		if t := meta["path"]; t != "" {
			target = t
		}
		return &os.PathError{Op: "open", Path: target, Err: fs.ErrNotExist}
	case FaultForbiddenCred:
		return &ForbiddenCredFault{RunID: runID, Cred: SentinelFakeCred}
	}
	return nil
}

// driverHookKind returns the FaultKind that the given driver HookPoint
// listens for, or "" if none.
func driverHookKind(hp driver.HookPoint) FaultKind {
	switch hp {
	case driver.HookPointDriverPickup:
		return FaultDuplicatePickup
	case driver.HookPointDriverCapabilityRead:
		// Capability read is handled directly in driverHook because two
		// FaultKinds (stale_capability, wrong_os_version) listen on it.
		// Returning "" here ensures the generic dispatcher does not
		// double-fire.
		return ""
	case driver.HookPointDriverCredResolve:
		return FaultForbiddenCred
	case driver.HookPointDriverModelRoute:
		return FaultModelRouteFailure
	case driver.HookPointDriverMainLoop:
		return FaultDriverRestart
	case driver.HookPointSlaveHeartbeat:
		return FaultSlaveDisconnect
	}
	return ""
}

func executorHookKind(hp executor.HookPoint) FaultKind {
	switch hp {
	case executor.HookPointExecutorFileOpen:
		return FaultMissingFile
	case executor.HookPointExecutorCredResolve:
		return FaultForbiddenCred
	}
	return ""
}

// CapabilityFault carries the kind (stale_capability or wrong_os_version)
// so a caller can decide what substitution to perform on its local
// capability snapshot copy.
type CapabilityFault struct {
	Kind  FaultKind
	RunID string
}

func (e *CapabilityFault) Error() string {
	return fmt.Sprintf("faultinject: capability fault kind=%s run_id=%s", e.Kind, e.RunID)
}

// ForbiddenCredFault carries the sentinel cred string so the call-site
// can substitute it as the resolved credential and let downstream code
// observe a forbidden_cred failure.
type ForbiddenCredFault struct {
	RunID string
	Cred  string
}

func (e *ForbiddenCredFault) Error() string {
	return fmt.Sprintf("faultinject: forbidden_cred run_id=%s cred=%s", e.RunID, e.Cred)
}

// SubstituteCapability returns the bytes a caller should swap into its
// local capability snapshot when a CapabilityFault fires. The actual
// production capability struct is not imported here (would create a
// cycle / require evaltool tag on consumers); callers JSON-unmarshal
// these bytes into their own struct.
func SubstituteCapability(f *CapabilityFault) (string, string) {
	switch f.Kind {
	case FaultStaleCapability:
		return "hash", StaleCapabilityValue
	case FaultWrongOSVersion:
		return "os", WrongOSVersionValue
	}
	return "", ""
}

// HookPointDriverCapabilityRead handles two FaultKinds
// (FaultStaleCapability and FaultWrongOSVersion). When both are active
// for the same run_id, the bridge fires the first kind it encounters in
// the Lookup order above (stale first, then wrong-os); only one fires
// per call so the caller always sees a determinate failure.
