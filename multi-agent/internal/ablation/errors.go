package ablation

import "errors"

// Sentinel errors returned by Register / SetByName.
//
// Callers MUST use errors.Is to test against these values; string contents
// are not part of the API contract.
var (
	// ErrUnknownFlag is returned when a name is not one of the canonical
	// 8 ablation FlagName values (KnownFlags). Returned by Register if the
	// FlagName argument is unknown and by SetByName if the string argument
	// is unknown. The whole point of returning this instead of silently
	// no-op'ing is so that a CLI typo (e.g. "--ablation NoTpedContracts")
	// is caught before the experiment runs.
	ErrUnknownFlag = errors.New("ablation: unknown flag")

	// ErrNilTarget is returned by Register when its *bool target is nil.
	ErrNilTarget = errors.New("ablation: nil target")

	// ErrAlreadyRegistered is returned by Register on the second (and
	// further) attempts to register the same FlagName. The first target
	// stays in place; the duplicate caller is rejected. This is deliberate:
	// silent overwrite would make ablation behaviour depend on init order,
	// which is fragile across refactors (see spec §7 (c)).
	ErrAlreadyRegistered = errors.New("ablation: flag already registered")

	// ErrNotRegistered is returned by SetByName when the name is a known
	// FlagName but no Register call has yet wired a target for it. This
	// typically means the owning package was not linked into this binary.
	ErrNotRegistered = errors.New("ablation: flag not registered")
)
