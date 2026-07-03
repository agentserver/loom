package validator

import (
	"log"
	"sync/atomic"

	"github.com/yourorg/multi-agent/internal/ablation"
)

// disableDryRun is the ablation flag's *bool target. The registry
// contract requires a *bool, so this variable stays. Readers MUST use
// IsDryRunDisabled(); it loads from a mirroring atomic.Bool so
// concurrent MCP dispatches that read the flag while a test toggles
// it stay data-race-safe. Mirrors capability.SetDisableUpload pattern.
var disableDryRun bool

// disableDryRunAtomic mirrors disableDryRun for race-free reads.
var disableDryRunAtomic atomic.Bool

// IsDryRunDisabled reports whether the NoDryRun ablation flag is on.
// Called by driver.dryRunContractTool.Call to decide whether to
// short-circuit BEFORE invoking validator.New().Check(...). When true,
// the tool logs "[ablation] NoDryRun: skipped conversation=<id>",
// emits ZERO metric events, and returns Blocks: [] (spec §7(d)).
//
// ONLY correct way to read the flag from a concurrent context.
// Reading the raw disableDryRun variable races with any writer.
func IsDryRunDisabled() bool { return disableDryRunAtomic.Load() }

// SetDryRunDisabled sets both the atomic mirror and the raw bool.
// The registry writes only through the *bool; direct consumers
// (tests, production code that needs to flip the flag without going
// through the ablation registry) MUST use this rather than assigning
// the raw variable.
func SetDryRunDisabled(v bool) {
	disableDryRunAtomic.Store(v)
	disableDryRun = v
}

// SyncDisableDryRun copies the current raw disableDryRun value into
// the atomic mirror. The Phase-2 CLI binder MUST call this after any
// ablation.Default.SetByName("NoDryRun", ...) batch and BEFORE
// spawning any goroutine that reads the flag — the ablation registry
// writes only through the *bool, and readers see the atomic.
func SyncDisableDryRun() { disableDryRunAtomic.Store(disableDryRun) }

func init() {
	if err := ablation.Default.Register(ablation.NoDryRun, &disableDryRun); err != nil {
		// Init-time panic would DoS the whole process before main
		// runs; the ablation contract says Register never panics. All
		// error modes here are programmer bugs — log loudly.
		log.Printf("validator: ablation registration failed: %v", err)
	}
}
