package validator

import (
	"log"

	"github.com/yourorg/multi-agent/internal/ablation"
)

// disableDryRun is the ablation flag's *bool target. Registered in
// init() below against ablation.NoDryRun. Readers MUST use
// IsDryRunDisabled — the ablation contract makes no concurrency
// guarantee about the raw variable (spec §7(d) + registry.go).
var disableDryRun bool

// IsDryRunDisabled reports whether the NoDryRun ablation flag is on.
// Called by driver.dryRunContractTool.Call to decide whether to
// short-circuit BEFORE invoking validator.New().Check(...). When true,
// the tool logs "[ablation] NoDryRun: skipped conversation=<id>",
// emits ZERO metric events, and returns Blocks: [] (spec §7(d)).
func IsDryRunDisabled() bool { return disableDryRun }

// SetDryRunDisabled is the test-only mutator. Production code MUST
// use ablation.Default.SetByName(...) — the CLI binder does this
// once, before the driver starts.
func SetDryRunDisabled(v bool) { disableDryRun = v }

func init() {
	if err := ablation.Default.Register(ablation.NoDryRun, &disableDryRun); err != nil {
		// Init-time panic would DoS the whole process before main
		// runs; the ablation contract says Register never panics. All
		// error modes here are programmer bugs — log loudly.
		log.Printf("validator: ablation registration failed: %v", err)
	}
}
