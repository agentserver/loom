package ablation

import (
	"sort"
	"sync"
)

// FlagName is the typed identifier of a known ablation flag. Defined
// string type (not an alias) so that an identifier-form call site like
// ablation.Default.Register(ablation.NoTypedContracts, &x) fails at
// `go build` on a misspelled identifier. An untyped string literal still
// compiles; that path is caught at runtime by Register / SetByName via
// ErrUnknownFlag.
type FlagName string

const (
	NoCapabilityDiscovery   FlagName = "NoCapabilityDiscovery"
	NoTypedContracts        FlagName = "NoTypedContracts"
	NoDryRun                FlagName = "NoDryRun"
	NoContractFormalization FlagName = "NoContractFormalization"
	NoUserPromotionPath     FlagName = "NoUserPromotionPath"
	NoAcceptanceGate        FlagName = "NoAcceptanceGate"
	NoRegistryLookup        FlagName = "NoRegistryLookup"
	NoObserver              FlagName = "NoObserver"
)

// canonicalFlags is the unexported source of truth for the 8 known
// ablation flag names. KnownFlags copies from this slice; the Register /
// SetByName validity check reads canonicalSet.
var canonicalFlags = []FlagName{
	NoCapabilityDiscovery,
	NoTypedContracts,
	NoDryRun,
	NoContractFormalization,
	NoUserPromotionPath,
	NoAcceptanceGate,
	NoRegistryLookup,
	NoObserver,
}

// canonicalSet is the set form of canonicalFlags, built once at init.
var canonicalSet = func() map[FlagName]struct{} {
	s := make(map[FlagName]struct{}, len(canonicalFlags))
	for _, f := range canonicalFlags {
		s[f] = struct{}{}
	}
	return s
}()

// KnownFlags returns a fresh copy of the canonical 8 ablation FlagName
// values, in declaration order. Each call allocates a new slice so that
// a caller mutating the result cannot corrupt registry validation state.
func KnownFlags() []FlagName {
	out := make([]FlagName, len(canonicalFlags))
	copy(out, canonicalFlags)
	return out
}

// Registry is a concurrency-safe map from a known FlagName to a *bool
// target owned by a downstream package. Register / SetByName / List are
// safe to call from multiple goroutines; the package makes no concurrency
// guarantee about external reads of the *bool itself — see the spec §4
// for the intended pre-run-only mutation pattern.
type Registry struct {
	mu      sync.Mutex
	targets map[FlagName]*bool
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{targets: make(map[FlagName]*bool)}
}

// Register associates a *bool target with a known ablation FlagName.
//
// Returns:
//   - ErrUnknownFlag       if name is not one of the 8 KnownFlags() values.
//   - ErrNilTarget         if target is nil.
//   - ErrAlreadyRegistered if a target is already registered for name; the
//     previously registered *bool is NOT overwritten.
//
// Register never panics — init-time panics would DoS the whole process
// before main runs.
func (r *Registry) Register(name FlagName, target *bool) error {
	if _, ok := canonicalSet[name]; !ok {
		return ErrUnknownFlag
	}
	if target == nil {
		return ErrNilTarget
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.targets[name]; dup {
		return ErrAlreadyRegistered
	}
	r.targets[name] = target
	return nil
}

// SetByName looks up the FlagName equal to name and writes v through the
// registered *bool target.
//
// Returns:
//   - ErrUnknownFlag   if name is not one of the 8 KnownFlags() values
//     (catches CLI / config typos before they silently no-op — see spec
//     §7 (b)).
//   - ErrNotRegistered if name is a known FlagName but no target has been
//     registered for it yet (the owning package wasn't linked).
//
// Critical: the canonicalSet check happens BEFORE the targets-map lookup.
// Inverting that order would return ErrNotRegistered for typos, which is
// the wrong sentinel and lets a CLI binder misclassify a typo as "package
// not linked" diagnostic noise.
func (r *Registry) SetByName(name string, v bool) error {
	fn := FlagName(name)
	if _, ok := canonicalSet[fn]; !ok {
		return ErrUnknownFlag
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	target, ok := r.targets[fn]
	if !ok {
		return ErrNotRegistered
	}
	*target = v
	return nil
}

// Default is the process-wide Registry. Downstream packages call
//
//	ablation.Default.Register(ablation.NoXxx, &xxx.DisableXxx)
//
// from init(). The Phase-2 CLI binder (WT-2-flag-integration) calls
// Default.SetByName(name, true) for each --ablation NAME value passed
// on the command line.
var Default = NewRegistry()

// List returns the FlagName subset that currently has a target registered,
// sorted by ascending underlying string. Two consecutive calls return
// element-equal slices (stable order is part of the contract — Go map
// iteration order is randomised, so the sort is mandatory, not cosmetic).
// The returned slice is a fresh copy; callers may modify it.
func (r *Registry) List() []FlagName {
	r.mu.Lock()
	out := make([]FlagName, 0, len(r.targets))
	for name := range r.targets {
		out = append(out, name)
	}
	r.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
