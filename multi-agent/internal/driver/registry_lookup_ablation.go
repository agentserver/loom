package driver

import (
	"log"
	"sync"

	"github.com/yourorg/multi-agent/internal/ablation"
)

// NoRegistryLookup ablation flag target — the FlagName is declared in
// internal/ablation (registered in Phase 1 WT-1-ablation-registry);
// this package owns the *bool target. Spec §6.

var (
	noRegistryLookup         bool
	noRegistryLookupInitErr  error
	noRegistryLookupWarnOnce sync.Once
)

// IsNoRegistryLookup reports whether the NoRegistryLookup ablation is
// active. Kept as an accessor (not a raw var read) so a future
// evolution can add synchronization without breaking callers.
func IsNoRegistryLookup() bool { return noRegistryLookup }

func init() {
	if err := ablation.Default.Register(ablation.NoRegistryLookup, &noRegistryLookup); err != nil {
		// Same never-panic-in-init pattern as evalrun.DisableTelemetry.
		// The error surfaces AGAIN on first Lookup call so an operator
		// running `driver-agent ... 2>/dev/null` still learns the flag
		// wiring is inert.
		noRegistryLookupInitErr = err
		log.Printf("driver: ablation.Default.Register(NoRegistryLookup) failed: %v — --ablation NoRegistryLookup will not gate Lookup", err)
	}
}

// surfaceInitErrorOnce is invoked by Lookup on first entry. If the
// registration failed at init time, emit one ERROR log line so it's
// visible even when stderr was suppressed at startup.
func surfaceInitErrorOnce() {
	if noRegistryLookupInitErr == nil {
		return
	}
	noRegistryLookupWarnOnce.Do(func() {
		log.Printf("[error] NoRegistryLookup ablation wiring inert (init err: %v) — subsequent Lookup calls will run unguarded", noRegistryLookupInitErr)
	})
}

// resetNoRegistryLookupForTest is test-only.
func resetNoRegistryLookupForTest() {
	noRegistryLookup = false
	noRegistryLookupInitErr = nil
	noRegistryLookupWarnOnce = sync.Once{}
}
