// skill_flags.go is the collection point for ablation flag targets owned
// by Python (or other non-Go) skills that ship under skills/<name>/. The
// Go ablation registry (registry.go) is the single source of truth for
// which flags exist (KnownFlags) and the runtime *bool wiring; a Python
// skill cannot Register against the registry directly, so the bridge is
// twofold:
//
//  1. This file's init() registers each Python-skill-owned flag against
//     a package-private *bool target, making Default.List() exhaustive
//     and Default.SetByName(name, true) functional. The Phase-2 CLI
//     binder (WT-2-flag-integration) sets the flag through SetByName as
//     usual.
//  2. An exported accessor (e.g. IsNoAcceptanceGate) reads the *bool so
//     a future binder can mirror the value out to a Python child
//     process via an environment variable (e.g.
//     LOOM_ABLATION_NOACCEPTANCEGATE=1). This package does NOT do the
//     env export itself; that is the binder's job.
//
// To add a new Python-skill ablation flag here:
//
//   - Add the FlagName constant + canonical entry to registry.go (that
//     change is owned by the registry, not by this file).
//   - Add a new `var fooGate bool` private variable below.
//   - Add `mustRegister(FooFlag, &fooGate)` to the init() block.
//   - Add an exported `IsFoo() bool` accessor returning `fooGate`.
//
// Why a separate file rather than registering from the skill itself: a
// Python script cannot Register against a Go registry, so the wiring
// has to live in Go. Co-locating all Python-skill registrations here
// keeps the touchpoint discoverable (one file to grep) and avoids
// scattering trivial init() blocks across the package.

package ablation

import "fmt"

// noAcceptanceGate is the *bool target wired to ablation.NoAcceptanceGate.
//
// Set by Default.SetByName("NoAcceptanceGate", true), typically from the
// Phase-2 CLI binder. The binder is expected to also export
// LOOM_ABLATION_NOACCEPTANCEGATE=1 into the environment of any forked
// python3 skills/mcp-acceptance/scripts/mcp_acceptance.py process — the
// Python runner reads that env var to decide whether to bypass the
// acceptance gate (see docs/specs/wt1-acceptance-golden.spec.md §3 (d)).
var noAcceptanceGate bool

// IsNoAcceptanceGate reports whether the NoAcceptanceGate ablation flag
// has been flipped on for this run. Exposed for the Phase-2 CLI binder
// (WT-2-flag-integration) to read after argument parsing and before
// forking the Python child.
//
// Concurrency: this accessor is intentionally unsynchronized, matching
// the package-wide contract documented on registry.go's Registry type
// ("the package makes no concurrency guarantee about external reads of
// the *bool itself — see the spec §4 for the intended pre-run-only
// mutation pattern"). The binder is expected to call
// Default.SetByName(...) once during CLI argument parsing, BEFORE any
// goroutine that observes the gate is spawned. Reading and writing
// concurrently is a usage bug, not a contract gap.
func IsNoAcceptanceGate() bool { return noAcceptanceGate }

// mustRegister wraps Default.Register and panics on error. This is the
// documented escalation pattern, NOT a violation of the
// "Register-never-panics" contract on the registry itself: Register
// returns errors; this wrapper chooses to turn them into init-time
// panics because no recovery path is meaningful here.
//
// Justification per error:
//   - ErrUnknownFlag: only fires if NoAcceptanceGate is removed from
//     registry.go's canonicalFlags. That removal would also dead-code
//     this file; running with a half-removed flag is worse than
//     failing fast.
//   - ErrNilTarget: we pass &noAcceptanceGate (a non-nil package var
//     literal) — unreachable in practice.
//   - ErrAlreadyRegistered: would mean another init() in this package
//     already wired NoAcceptanceGate against a different target. That
//     is exactly the "two flags secretly share an owner" footgun spec
//     §7 (c) of WT-1-ablation-registry prohibits; panicking surfaces
//     the copy-paste at startup instead of letting it silently
//     mis-route a CLI flag.
//   - ErrTargetAlreadyRegistered: would mean &noAcceptanceGate was
//     already wired under a different FlagName. Same class as the
//     above; same response.
//
// All four are programmer bugs in this package's source, not runtime
// inputs. Panicking is the right level of severity for "the binary
// must not start in this state."
func mustRegister(name FlagName, target *bool) {
	if err := Default.Register(name, target); err != nil {
		panic(fmt.Errorf("ablation: registering %s: %w", name, err))
	}
}

func init() {
	mustRegister(NoAcceptanceGate, &noAcceptanceGate)
}
