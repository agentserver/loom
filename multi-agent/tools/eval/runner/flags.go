// WT-2-flag-integration §2.1: CLI binder that flips ablation flags
// registered via internal/ablation.Default.Register and derives the
// per-run baseline_or_ablation label.
//
// Every function here is a thin translation layer over
// ablation.Default.SetByName plus a small set of owner-package Sync
// hooks (see §6 of the spec). No ablation semantics live here.

package main

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/yourorg/multi-agent/internal/ablation"
	"github.com/yourorg/multi-agent/internal/capability"
	"github.com/yourorg/multi-agent/internal/contract/validator"

	// Owner-package blank imports — without these the runner binary
	// links only the ablation registry itself and every non-capability /
	// non-validator canonical flag's Register() init() never fires,
	// producing a runtime ErrNotRegistered from SetByName.
	// TestApplyAblationFlags_AllKnownFlagsBind is the earliest signal
	// that this list drifted out of sync with KnownFlags().
	_ "github.com/yourorg/multi-agent/internal/contract"          // NoTypedContracts, NoContractFormalization
	_ "github.com/yourorg/multi-agent/internal/driver"            // NoRegistryLookup, NoUserPromotionPath
	_ "github.com/yourorg/multi-agent/internal/evalrun"           // NoObserver
	// internal/ablation and internal/capability + internal/contract/validator
	// are already live-imported above for the Sync hooks and constants;
	// their init()s fire the same way blank imports would.
)

// DefaultBaselineName is the string the runner stamps into
// runs.baseline_or_ablation when no --ablation value is passed and
// --baseline-name is not supplied. Lowercase snake_case so the value
// satisfies baselineNameRe (spec §7(c)); union-typed with the
// CamelCase + `+`-join form used for ablation combinations.
const DefaultBaselineName = "full_loom"

// MaxBaselineOrAblationLen is the hard cap on any derived
// baseline_or_ablation string. Derivation: 8-flag sorted join is 129
// chars of names + 7 `+` separators = 136 chars. Cap of 200 leaves
// ~64 chars slack for two more ~30-char flags (spec §2.1). The
// TestBaselineOrAblationCapCoversAll8Flags CI test asserts the actual
// 8-flag sorted join stays strictly below this cap.
const MaxBaselineOrAblationLen = 200

// EnvNoAcceptanceGate is the single env-var name bridged to the
// Python acceptance-gate skill's ablation flag. See spec §5.4 and
// docs/specs/wt1-acceptance-golden.spec.md §3(d).
const EnvNoAcceptanceGate = "LOOM_ABLATION_NOACCEPTANCEGATE"

// baselineNameRe matches the character class shared with
// tests/eval/baselines/harness/row.go's ErrBaselineNameInvalid so
// runner-emitted rows and baseline-emitted rows use the same regex
// for the same column (spec §7(c) union).
var baselineNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{2,63}$`)

// ablationLabelRe matches the CamelCase + `+`-join form produced by
// ComputeBaselineOrAblation for one or more ablation flags. Anchored
// on both ends so a stray `+`-prefixed or `+`-suffixed value fails
// (spec §7(c) whitelist).
var ablationLabelRe = regexp.MustCompile(`^([A-Z][A-Za-z0-9]{2,31})(\+[A-Z][A-Za-z0-9]{2,31})*$`)

// Sentinel errors surfaced from ValidateBaselineOrAblation and the
// CLI binder. Callers MUST use errors.Is; string contents are not
// part of the API contract.
var (
	// ErrBaselineOrAblationInvalid — the input matches neither the
	// baseline regex nor the ablation regex, or is empty, or
	// collides with a canonical ablation flag name.
	ErrBaselineOrAblationInvalid = errors.New("eval-runner: baseline_or_ablation invalid")

	// ErrBaselineOrAblationTooLong — the input's length exceeds
	// MaxBaselineOrAblationLen.
	ErrBaselineOrAblationTooLong = errors.New("eval-runner: baseline_or_ablation length exceeds cap")
)

// flagDescriptions is the CLI-facing one-liner per canonical flag.
// The bracketed section prefix (`[Xn]`) MUST match the flag's owning
// row in 12 号 §A/§B/§C/§D; TestReadmeMetricMapping cross-checks. No
// path, config, env-var, URL, or credential-shaped strings — spec
// §7(d); enforced by TestListAblations_NoSensitiveContent.
var flagDescriptions = map[ablation.FlagName]string{
	ablation.FlagName("NoAcceptanceGate"):        "[B3] Bypass MCP acceptance golden runner.",
	ablation.FlagName("NoCapabilityDiscovery"):   "[A1] Skip capability snapshot upload from slave to driver.",
	ablation.FlagName("NoContractFormalization"): "[A2] All step delegates use natural language, no JSON contract.",
	ablation.FlagName("NoDryRun"):                "[A3] Skip contract validator dry-run in driver plan phase.",
	ablation.FlagName("NoObserver"):              "[D1] Suppress observer and evalrun D1 SQLite writes; log-only.",
	ablation.FlagName("NoRegistryLookup"):        "[B4] Skip dynamic MCP registry and userspace FTS5 lookup hint.",
	ablation.FlagName("NoTypedContracts"):        "[A2] Do not enforce JSON schema on step contracts; structure preserved.",
	ablation.FlagName("NoUserPromotionPath"):     "[B1] Suppress user-facing promote-candidate surface.",
}

// ablationEnvExports maps each Python-companion ablation flag to the
// env var the Python runner reads. Sparse — most flags have no
// Python companion. TestPythonBridgeKeysAreCanonical asserts every
// key is a member of ablation.KnownFlags().
var ablationEnvExports = map[ablation.FlagName]string{
	ablation.FlagName("NoAcceptanceGate"): EnvNoAcceptanceGate,
}

// syncHooks is invoked from applyAblationFlagsTo after each
// registry mutation, per §6 of the spec. Each entry runs a
// package's atomic-mirror sync (or is nil for packages that read
// the raw bool directly). Called on every apply AND every reset,
// so it must be idempotent — every listed function is documented
// as such in the owning package.
var syncHooks = []func(){
	capability.SyncDisableUpload,
	validator.SyncDisableDryRun,
}

// defaultMu serialises every applyAblationFlagsTo(ablation.Default, …)
// call so two Runs in the same process (parallel `go test`, a
// wrapping bash script that shells out repeatedly) cannot race on
// the raw *bool targets or on the SetByName/Sync-hook sequence.
//
// Necessary because the pre-run-only mutation contract is a
// documented invariant, not an enforced one — `go test -race`
// happily runs sibling package tests in parallel goroutines while
// each one Run()s and defers a reset. Without this mutex, the
// deferred reset in one Run's goroutine writes DisableTelemetry
// while a Sync* in another Run's goroutine reads the mirror.
//
// Only Default is protected; the reg *ablation.Registry parameter
// exists so tests can drive a private registry (see
// TestApplyAblationFlagsTo_*_reg tests), and those tests own their
// own registry — no serialisation needed there.
var defaultMu sync.Mutex

// -----------------------------------------------------------------------------
// AblationList — flag.Value implementation for --ablation
// -----------------------------------------------------------------------------

// AblationList collects --ablation values from the command line.
// Multiple --ablation invocations and comma-joined single invocations
// both append; entries are canonicalised, deduped, and rejected if
// unknown.
type AblationList struct {
	// values is the deduped insertion-ordered list of accepted flag
	// names. AblationList maintains insertion order so error
	// messages against the (rejected) N+1'th entry name the
	// preceding entries correctly.
	values []ablation.FlagName
	// seen indexes values for O(1) dedup.
	seen map[ablation.FlagName]bool
}

// String satisfies flag.Value; used by `-h`. Returns the
// comma-joined canonical form.
func (a *AblationList) String() string {
	if a == nil || len(a.values) == 0 {
		return ""
	}
	parts := make([]string, len(a.values))
	for i, fn := range a.values {
		parts[i] = string(fn)
	}
	return strings.Join(parts, ",")
}

// Set is called by the flag package for each --ablation value. The
// value may be a single flag or a comma-joined list. TrimSpace is
// applied to each entry to tolerate `--ablation "NoObserver, NoAcceptanceGate"`.
func (a *AblationList) Set(v string) error {
	if a.seen == nil {
		a.seen = map[ablation.FlagName]bool{}
	}
	// Split on comma and iterate; TrimSpace each entry so
	// `--ablation "  NoObserver  , NoAcceptanceGate "` succeeds.
	for _, raw := range strings.Split(v, ",") {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			return fmt.Errorf("eval-runner: --ablation entry is empty")
		}
		fn := ablation.FlagName(entry)
		// Verify against the canonical set BEFORE touching state.
		// KnownFlags is the runtime source of truth (ablation
		// package internals may change) — iterate rather than
		// referencing named constants (spec §7(e)).
		known := false
		for _, k := range ablation.KnownFlags() {
			if k == fn {
				known = true
				break
			}
		}
		if !known {
			return fmt.Errorf("eval-runner: --ablation %q: %w", entry, ablation.ErrUnknownFlag)
		}
		if a.seen[fn] {
			continue
		}
		a.seen[fn] = true
		a.values = append(a.values, fn)
	}
	return nil
}

// Values returns a copy of the accepted flag list. Callers may
// mutate the returned slice without affecting a subsequent Set.
func (a *AblationList) Values() []ablation.FlagName {
	if a == nil || len(a.values) == 0 {
		return nil
	}
	out := make([]ablation.FlagName, len(a.values))
	copy(out, a.values)
	return out
}

// -----------------------------------------------------------------------------
// Registry mutation — three-phase (validate / reset / apply)
// -----------------------------------------------------------------------------

// isCanonicalFlag reports whether fn is one of the 8 canonical
// ablation flag names. Uses KnownFlags rather than a named-constant
// reference to keep flags.go decoupled from the constant surface
// (spec §7(e)).
func isCanonicalFlag(fn ablation.FlagName) bool {
	for _, k := range ablation.KnownFlags() {
		if k == fn {
			return true
		}
	}
	return false
}

// ApplyAblationFlags is the exported entry point used by Run and
// main.go. Delegates to applyAblationFlagsTo against the
// process-wide Default registry.
func ApplyAblationFlags(flags []ablation.FlagName) error {
	return applyAblationFlagsTo(ablation.Default, flags)
}

// applyAblationFlagsTo runs the three-phase mutation described in
// spec §2.1. Phase 1 validates; Phase 2 resets every registered
// canonical flag to false + clears every Python-bridge env var;
// Phase 3 applies the requested set.
//
// Called with reg == ablation.Default in production; tests inject
// a fresh Registry to exercise ErrUnknownFlag / ErrNotRegistered
// negative paths without touching global state.
//
// Concurrency: when reg is ablation.Default, defaultMu is held for
// the entire three-phase mutation + sync-hook invocation so a
// concurrent caller cannot observe a torn (target-written /
// mirror-stale) state. Tests that pass a private Registry via
// ablation.NewRegistry own their own registry and do not need this
// serialisation.
func applyAblationFlagsTo(reg *ablation.Registry, flags []ablation.FlagName) error {
	if reg == ablation.Default {
		defaultMu.Lock()
		defer defaultMu.Unlock()
	}
	// -------- Phase 1: validate (no mutation) --------
	//
	// Bail on the FIRST invalid entry before any write. reg.List()
	// enumerates every canonical name that has a target registered
	// in `reg`; membership tells us Phase 3 will not hit
	// ErrNotRegistered. isCanonicalFlag catches unknown names
	// (would hit ErrUnknownFlag).
	registered := map[ablation.FlagName]bool{}
	for _, fn := range reg.List() {
		registered[fn] = true
	}
	for _, fn := range flags {
		if !isCanonicalFlag(fn) {
			return fmt.Errorf("eval-runner: --ablation %q: %w", string(fn), ablation.ErrUnknownFlag)
		}
		if !registered[fn] {
			return fmt.Errorf("eval-runner: --ablation %q: %w", string(fn), ablation.ErrNotRegistered)
		}
	}

	// -------- Phase 2: reset every registered flag + bridge env --------
	//
	// Scrubs stale state from a prior Run call (spec §7(a.4)). We
	// only touch flags that reg.List() reports; a canonical flag
	// whose owner package wasn't linked into this binary won't be
	// in the list, so we can't reset something we can't observe —
	// but Phase 1 already ruled out an apply against such a flag,
	// so the invariant "every flag we may set true, we can also
	// set false" holds.
	for _, fn := range reg.List() {
		if err := reg.SetByName(string(fn), false); err != nil {
			// Not expected: we iterated reg.List() output. If
			// this fires, the registry mutated between the
			// List() call and the SetByName call — a bug in a
			// caller violating the pre-run-only mutation
			// contract.
			return fmt.Errorf("eval-runner: reset %q: %w", string(fn), err)
		}
	}
	for _, envName := range ablationEnvExports {
		_ = os.Unsetenv(envName)
	}
	// Sync every owner mirror so a caller reading via an atomic
	// accessor sees the reset before Phase 3 begins.
	for _, hook := range syncHooks {
		hook()
	}

	// -------- Phase 3: apply the requested set --------
	//
	// Dedup here too (defence against direct callers that pass
	// duplicates in Opts.AblationFlags — CLI callers already
	// deduped via AblationList.Set).
	applied := map[ablation.FlagName]bool{}
	for _, fn := range flags {
		if applied[fn] {
			continue
		}
		applied[fn] = true
		if err := reg.SetByName(string(fn), true); err != nil {
			// Not expected — Phase 1 validated every entry
			// and the registry mutation contract is pre-run
			// only. Surface verbatim as a bug signal.
			return fmt.Errorf("eval-runner: apply %q: %w", string(fn), err)
		}
		if envName, ok := ablationEnvExports[fn]; ok {
			if err := os.Setenv(envName, "1"); err != nil {
				return fmt.Errorf("eval-runner: export %s: %w", envName, err)
			}
		}
	}
	// Sync again after apply so the atomic mirrors reflect the new
	// true values before Run returns.
	for _, hook := range syncHooks {
		hook()
	}
	return nil
}

// ScrubAmbientAblationEnv unsets every env var listed as a value in
// ablationEnvExports. Called from main() before flag parsing and
// from Run() before any downstream env propagation, so a parent
// process's LOOM_ABLATION_* value cannot silently activate an
// ablation the operator did not request (spec §7(a.2)).
//
// Idempotent: os.Unsetenv on an already-unset var is a no-op.
func ScrubAmbientAblationEnv() {
	for _, envName := range ablationEnvExports {
		_ = os.Unsetenv(envName)
	}
}

// -----------------------------------------------------------------------------
// baseline_or_ablation label derivation
// -----------------------------------------------------------------------------

// ComputeBaselineOrAblation derives the D1 runs.baseline_or_ablation
// value from the applied flag list and the operator's chosen
// baseline name. Called from Run; NEVER accepts a caller-supplied
// label (spec §7(a.3) anti-forgery).
//
// Behaviour (spec §4):
//
//   - Empty flags + non-empty baseline: return baseline (validated).
//   - Non-empty flags: dedupe, sort ascending, join with "+".
//     The baseline argument is IGNORED in this branch (a stderr note
//     from the caller informs the operator).
//   - The returned string is validated via ValidateBaselineOrAblation
//     before return.
func ComputeBaselineOrAblation(flags []ablation.FlagName, baseline string) (string, error) {
	if len(flags) == 0 {
		// A baseline-branch label MUST match the strict
		// baseline regex (lowercase snake_case). Anything that
		// only satisfies the ablation regex — a raw canonical
		// flag name ("NoObserver") or a CamelCase `+`-join
		// ("NoAcceptanceGate+NoObserver") — is label forgery:
		// the operator would be stamping an ablation-shaped
		// row with no flags actually applied, collision with
		// real ablation rows in D2 GROUP BY (spec §7(a.3)).
		if !baselineNameRe.MatchString(baseline) {
			// Distinguish the two subcases so the operator sees
			// a specific diagnostic.
			if baseline != "" && (isCanonicalFlag(ablation.FlagName(baseline)) ||
				strings.Contains(baseline, "+")) {
				return "", fmt.Errorf("%w: --baseline-name %q collides with an ablation flag name",
					ErrBaselineOrAblationInvalid, baseline)
			}
			return "", fmt.Errorf("%w: --baseline-name %q must match ^[a-z][a-z0-9_-]{2,63}$",
				ErrBaselineOrAblationInvalid, baseline)
		}
		if err := ValidateBaselineOrAblation(baseline); err != nil {
			return "", err
		}
		return baseline, nil
	}
	seen := map[ablation.FlagName]bool{}
	names := make([]string, 0, len(flags))
	for _, fn := range flags {
		if seen[fn] {
			continue
		}
		seen[fn] = true
		names = append(names, string(fn))
	}
	sort.Strings(names)
	label := strings.Join(names, "+")
	if err := ValidateBaselineOrAblation(label); err != nil {
		return "", err
	}
	return label, nil
}

// ValidateBaselineOrAblation runs the belt-and-braces final check
// on a candidate label. Called from ComputeBaselineOrAblation
// (before returning) AND from Run (before stamping the row) so a
// future refactor of Compute that produces a bad string is still
// caught at the row-assembly seam (spec §7(c) dual enforcement).
func ValidateBaselineOrAblation(s string) error {
	if s == "" {
		return fmt.Errorf("%w: empty", ErrBaselineOrAblationInvalid)
	}
	if len(s) > MaxBaselineOrAblationLen {
		return fmt.Errorf("%w: length %d cap %d",
			ErrBaselineOrAblationTooLong, len(s), MaxBaselineOrAblationLen)
	}
	if baselineNameRe.MatchString(s) {
		// A canonical flag name in the baseline branch confuses
		// D2 GROUP BY (a full-Loom run labelled "NoObserver"
		// would collide with the actual NoObserver ablation
		// row). Reject only in the baseline branch — the
		// ablation-form regex (CamelCase + `+`-join) accepts
		// "NoObserver" as a legitimate ablation label and MUST
		// NOT trip the collision check.
		if isCanonicalFlag(ablation.FlagName(s)) {
			return fmt.Errorf("%w: %q collides with an ablation flag name",
				ErrBaselineOrAblationInvalid, s)
		}
		return nil
	}
	if ablationLabelRe.MatchString(s) {
		return nil
	}
	return fmt.Errorf("%w: %q matches neither baseline nor ablation regex",
		ErrBaselineOrAblationInvalid, s)
}

// -----------------------------------------------------------------------------
// --list-ablations renderer
// -----------------------------------------------------------------------------

// ListAblationsText renders the CLI-facing --list-ablations output.
// Exactly len(ablation.KnownFlags()) lines of the form
// `<FlagName>\t<description>`, sorted ascending. The description
// column carries no path / env / config content (spec §7(d)); the
// TestListAblations_NoSensitiveContent regex sweep is the
// authoritative gate.
func ListAblationsText() string {
	names := make([]string, 0, len(ablation.KnownFlags()))
	for _, fn := range ablation.KnownFlags() {
		names = append(names, string(fn))
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		b.WriteString(n)
		b.WriteByte('\t')
		b.WriteString(flagDescriptions[ablation.FlagName(n)])
		b.WriteByte('\n')
	}
	return b.String()
}
