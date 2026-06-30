# WT-1-ablation-registry — Spec

> Source: `/root/paper_writing/docs/final/todo_list.md` Phase 1 table row
> **WT-1-ablation-registry** (⭐ 必须先 merge).
> Branch: `paper/v3/p1-ablation-registry`.
> Base: `origin/paper/v3-integration @ 1332327`.

---

## 1. Task boundary & file scope

This worktree introduces a single brand-new Go package, `internal/ablation`,
inside the `multi-agent/` Go module
(`github.com/yourorg/multi-agent/internal/ablation`).

Hard rules:

- All new files live under `multi-agent/internal/ablation/` (created by this
  worktree).
- No other package may be modified by this worktree. In particular: no CLI
  wiring, no flag registration from any consumer package, no edits to
  `cmd/...`, `internal/driver/...`, `internal/capability/...`, etc.
- CLI binding (`--ablation Noxxx` on a binary) is explicitly out of scope; it
  is owned by Phase 2 `WT-2-flag-integration`. This worktree only delivers the
  library interface that the CLI binding (and every other ablation consumer)
  will later call.
- No additions to `multi-agent/go.mod`. The package depends only on the
  standard library (`errors`, `sort`, `sync`) plus the standard
  `testing` package for tests. (`fmt` is reserved for the future §2.5
  `%w` enrichment path; v1 returns bare sentinels and does not import
  it.)

### 1.1 Intentional divergence from the todo_list row signature

The todo_list row sketches the API as:

```text
Registry.Register(name string, target *bool)
Registry.SetByName(name string, v bool) error
Registry.List() []string
```

This spec deliberately tightens that sketch (per this worktree's prompt) to:

- `Register(name FlagName, target *bool) error` — `name` is the defined
  string type `FlagName`, not raw `string`. When the caller uses an
  exported identifier (`ablation.NoTypedContracts`), a typo at the
  registration call site is a `go build` error. Note that Go still
  permits an untyped string literal (`"NoTpedContracts"`) to be passed as
  a `FlagName` argument and that path compiles; it is caught at runtime
  by `Register` returning `ErrUnknownFlag` (see §7 (d) for the full
  story).
- `List() []FlagName` — same typed return; downstream code that just wants
  strings does `string(name)` on each element. Returning `[]FlagName`
  preserves the type-safety chain for CLI / diagnostic code that compares
  results to constants like `ablation.NoTypedContracts`.
- `Default` instead of `Registry` for the process-wide variable, because
  `type Registry struct { ... }` already uses the identifier `Registry`
  inside the package, so a `var Registry = NewRegistry()` would be a Go
  naming conflict. Downstream calls become
  `ablation.Default.Register(ablation.NoXxx, &xxx.DisableXxx)`. This is the
  exact same wiring shape the todo_list row described; only the variable
  name changes.

`SetByName` still takes `name string` (not `FlagName`) because its callers
are CLI parsers and config readers whose input is always a raw string —
and rejecting unknown strings is itself a deliberate security check (§7
(b)).

---

## 2. Public API

All identifiers below are exported from `package ablation`.

### 2.1 Typed flag name

```go
// FlagName is a defined string type identifying a known ablation flag.
// (Defined type, not a type alias.) Downstream packages MUST use one of
// the exported FlagName constants (e.g. ablation.NoCapabilityDiscovery)
// at Register call sites — that path gives a `go build` error on a
// misspelled identifier. A caller that falls back to an untyped string
// literal (`Register("NoXxx", &x)`) compiles, and is caught at runtime
// by Register returning ErrUnknownFlag; see §7 (d) for full reasoning.
type FlagName string
```

Rationale (security): typed constants force typo-resistance at compile time
when the caller uses an identifier (see §7 (d)). A consumer that writes
`ablation.Default.Register(ablation.NoTpedContracts, &x)` fails at
`go build` because the identifier does not exist. A consumer that bypasses
the convention and writes `ablation.Default.Register("NoTpedContracts", &x)`
compiles (Go converts the untyped string literal to `FlagName`); that path
is caught instead by the runtime defensive check in `Register`, which
returns `ErrUnknownFlag` — and `init()` wiring per §5 calls `panic(err)` on
that error, so the typo still fails fast at process start.

### 2.2 Known flag constants

Exactly the 8 ablation flag names listed in the todo_list row are exported, in
this order:

```go
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
```

The package also exposes the canonical list:

```go
// KnownFlags returns the 8 known FlagName values in a stable, documented
// order (matching the const block above). Callers MUST treat the result as
// read-only.
func KnownFlags() []FlagName
```

This is used by tests and by the future CLI binder (WT-2-flag-integration)
when enumerating accepted `--ablation` values.

### 2.3 Registry

```go
// Registry is a concurrency-safe map from a known FlagName to a *bool target
// owned by a downstream package. A package wires its own boolean toggle into
// the registry from init() so that a single switch (e.g. CLI flag) can flip
// the toggle by name without that package having to know about the CLI.
type Registry struct {
    // unexported fields: sync.Mutex + map[FlagName]*bool
}

// NewRegistry constructs an empty Registry.
func NewRegistry() *Registry
```

Methods:

```go
// Register associates a *bool target with a known ablation FlagName.
// It returns:
//   - ErrUnknownFlag        if name is not one of the 8 KnownFlags() values.
//   - ErrNilTarget          if target is nil.
//   - ErrAlreadyRegistered  if a target is already registered for name
//                           (any previous *bool stays in place; the call is
//                           a no-op other than the returned error).
//
// On success the *bool pointer is stored; SetByName later flips *target.
// Register MUST NOT panic; init()-time panics would DoS the whole process.
func (r *Registry) Register(name FlagName, target *bool) error

// SetByName looks up the FlagName equal to `name` (string→FlagName cast) and
// writes v through the registered *bool target.
// It returns:
//   - ErrUnknownFlag    if name is not one of the 8 KnownFlags() values.
//   - ErrNotRegistered  if name is a known FlagName but no target has been
//                       registered for it yet (typically: the owning package
//                       was not linked into this binary).
//
// SetByName MUST NOT silently no-op on unknown names; doing so would allow a
// CLI typo (e.g. `--ablation NoTpedContracts`) to look like an ablation run
// while the binary actually ran with no ablation enabled — see §7 (b).
func (r *Registry) SetByName(name string, v bool) error

// List returns the FlagName subset that currently has a target registered,
// sorted with sort.Slice so that two calls in a row return equal slices
// (see §7 (e)). The returned slice is a fresh copy; callers may modify it.
func (r *Registry) List() []FlagName
```

### 2.4 Process-wide default

```go
// Default is the process-wide Registry. Downstream packages call
//   ablation.Default.Register(ablation.NoXxx, &xxx.DisableXxx)
// from init(). The future CLI binder (WT-2-flag-integration) calls
// Default.SetByName(name, true) for each --ablation NAME passed on the
// command line.
var Default = NewRegistry()
```

### 2.5 Sentinel errors

```go
var (
    ErrUnknownFlag             = errors.New("ablation: unknown flag")
    ErrNilTarget               = errors.New("ablation: nil target")
    ErrAlreadyRegistered       = errors.New("ablation: flag already registered")
    ErrNotRegistered           = errors.New("ablation: flag not registered")
    ErrTargetAlreadyRegistered = errors.New("ablation: target already registered under another flag")
)
```

All API errors returned by `Register` / `SetByName` MUST satisfy
`errors.Is(err, ErrXxx)` for the corresponding sentinel; callers are expected
to switch on the sentinel, not on string contents. Sentinel errors are
returned bare in v1; any future enrichment (e.g. adding the offending flag
name to the message) MUST use `fmt.Errorf("...: %w", ErrXxx, ...)` so that
`errors.Is(err, ErrXxx)` continues to hold for every caller.

---

## 3. Error semantics — input → outcome → caller action

| Method     | Input                                                     | Returns                                | Caller action                                                                   |
| ---------- | --------------------------------------------------------- | -------------------------------------- | ------------------------------------------------------------------------------- |
| Register   | `name` is not in `KnownFlags()`                           | `ErrUnknownFlag`                       | Caller bug — switch to an exported `FlagName` constant (identifier form fails at `go build`). |
| Register   | `target == nil`                                           | `ErrNilTarget`                         | Bug in caller; pass `&pkg.DisableFoo`.                                          |
| Register   | `name` is already registered with some target             | `ErrAlreadyRegistered` (no overwrite)  | Two packages claim the same flag — pick one owner.                              |
| Register   | `target` is already registered under a different `name`   | `ErrTargetAlreadyRegistered` (no overwrite) | Copy-paste in the consumer pattern — fix the consumer to pass distinct `*bool`s. |
| Register   | identical `(name, target)` already registered             | `ErrAlreadyRegistered` (precedence: name match beats target match) | Idempotent re-Register from a copy-pasted line — fix the consumer to remove the duplicate. |
| Register   | valid name, valid non-nil target, not previously seen     | `nil`                                  | Target is wired. SetByName(name, …) will flip it.                               |
| SetByName  | `name` not in `KnownFlags()` (e.g. typo on CLI)           | `ErrUnknownFlag`                       | CLI binder surfaces this to the user as an error before the binary runs work.   |
| SetByName  | known `name` but no Register call has happened yet        | `ErrNotRegistered`                     | CLI binder surfaces it; the owning package wasn't linked.                       |
| SetByName  | known `name` with registered target                       | `nil` and `*target = v`                | OK.                                                                             |
| List       | any time                                                  | sorted snapshot of registered names    | Iterate for diagnostics / `--list-ablations`.                                   |

`Register` is the only path that mutates the *set* of registered flags;
`SetByName` only writes through an existing `*bool`. There is no `Unregister`
in v1 (init-time wiring is permanent for the life of the process).

---

## 4. Concurrency contract

`Registry` guards its internal `map[FlagName]*bool` with a `sync.Mutex`.

- `Register`, `SetByName`, and `List` are safe to call from multiple
  goroutines concurrently. This is the *only* concurrency guarantee the
  package makes; everything below is a non-guarantee that callers must
  respect.
- The registry mutex protects the registry's own state (map of name →
  `*bool`) and the write `*target = v` inside `SetByName`. It does **not**
  synchronise reads of `*target` performed by code outside this package:
  the downstream package's own read of its `DisableXxx` boolean races with
  any concurrent `SetByName` unless the downstream package adds its own
  synchronisation. The intended usage pattern (which keeps the package
  data-race-free with no extra work in consumers) is:
  - All `SetByName` calls — one per `--ablation NAME` value passed to the
    CLI binder, so there may be several — complete during single-threaded
    pre-run setup, before any workload goroutine is started; and
  - All reads of `DisableXxx` happen after that point, from the workload
    goroutines.
- The API explicitly **does not support** runtime mutation of an ablation
  toggle concurrent with reads by the workload. `Register` accepts only
  `*bool` and `SetByName` writes a plain `*target = v` under the
  registry's own mutex; there is no setter/atomic/callback variant. A
  downstream package that needs hot-path concurrent mutation should not
  use this registry for that toggle at all — it should keep its own
  `sync/atomic.Bool` and (for ablation wiring purposes) ALSO register a
  shadow `*bool` here that the CLI binder flips once at pre-run, copying
  into the atomic before workload start. The ablation package's contract
  ends at pre-run.
- The race detector is the authoritative gate for the guarantees the
  package *does* make:
  `go test ./internal/ablation/... -count=1 -shuffle=on -race` MUST pass.
- `KnownFlags()` MUST return a freshly allocated slice on each call (so a
  caller mutating the returned slice cannot corrupt registry validation
  state). The package SHOULD keep its own canonical name set in an
  unexported variable that `KnownFlags()` copies from.

---

## 5. Downstream-consumer contract

Phase 1 and Phase 2 worktrees that introduce an ablation toggle do exactly
this, in their own package:

```go
package capability

import "github.com/yourorg/multi-agent/internal/ablation"

// DisableCapabilityDiscovery, when true, makes inspect skip telemetry upload.
var DisableCapabilityDiscovery bool

func init() {
    if err := ablation.Default.Register(
        ablation.NoCapabilityDiscovery,
        &DisableCapabilityDiscovery,
    ); err != nil {
        // init-time wiring bug: surface immediately.
        panic(err)
    }
}
```

`WT-2-flag-integration` (Phase 2, separate worktree) is the only place
allowed to call `ablation.Default.SetByName(...)`. It parses `--ablation N`
(potentially repeated) from the CLI and, for each value, calls
`Default.SetByName(value, true)`. A non-nil error from `SetByName` propagates
to the CLI as a fatal pre-run error so the user notices a typo before any
experiment is launched (this is the whole point of §7 (b)).

This spec does **not** prescribe how Phase 2 surfaces the CLI flag (cobra,
stdlib `flag`, env var) — only the underlying API contract.

---

## 6. Acceptance checklist

Mapped 1:1 to the todo_list row "接口 + 8 个 const 名；`go test
./internal/ablation/...`":

1. Package `internal/ablation` exists.
2. `FlagName` defined string type is exported (`type FlagName string`).
3. The 8 `FlagName` constants from §2.2 exist with the exact spelling above.
4. `KnownFlags() []FlagName` returns those 8 in the documented order.
5. `Registry` struct, `NewRegistry() *Registry`, and `Default = NewRegistry()`
   exist.
6. Methods `Register(FlagName, *bool) error`, `SetByName(string, bool) error`,
   `List() []FlagName` exist and behave per §3.
7. Sentinel errors `ErrUnknownFlag`, `ErrNilTarget`, `ErrAlreadyRegistered`,
   `ErrNotRegistered`, and `ErrTargetAlreadyRegistered` (the §7 (c)
   target-aliasing sentinel) exist and are returned by the cases in §3.
8. `go test ./internal/ablation/... -count=1 -shuffle=on -race` passes.
9. `go vet ./internal/ablation/...` clean; `gofmt -l internal/ablation/`
   empty.

### 6.1 Required per-mitigation test sketches (Security §7 binding)

These are the minimum test names the Stage-2 plan must encode (the plan
expands each into a row in its test matrix). The spec is satisfied only
when each mitigation has at least one passing test:

| Mitigation | Required test (name as guide, not literal) | What it asserts |
| --- | --- | --- |
| §7 (a) Goroutine safety | `TestConcurrent_Register_Race`, `TestConcurrent_SetByName_Race`, `TestConcurrent_RegisterSetList_Race` | N goroutines hit `Register` / `SetByName` / `List` (the last one in a mixed-method test) under `t.Parallel()` + `-race`; no data race, no panic, registry stays consistent. |
| §7 (b) Unknown name not silent | `TestSetByName_Unknown_ErrUnknownFlag` | `SetByName("NoTpedContracts", true)` returns `ErrUnknownFlag` (NOT `nil`). |
| §7 (b) Known-but-unregistered not silent | `TestSetByName_NotRegistered_ErrNotRegistered` | `SetByName("NoObserver", true)` with no prior `Register` returns `ErrNotRegistered`. |
| §7 (c) Duplicate Register (name) | `TestRegister_Duplicate_ErrAlreadyRegistered`, `TestSetByName_DuplicateRegisterDoesNotOverwrite` | Second `Register` of the same name returns `ErrAlreadyRegistered`, does NOT panic, does NOT overwrite (the original target's value still tracks subsequent `SetByName`). |
| §7 (c) Duplicate Register (target) | `TestRegister_SameTargetUnderTwoNames_Rejected` | Second `Register` of the same `*bool` under a different `FlagName` returns `ErrTargetAlreadyRegistered`; the second name remains NOT registered. |
| §7 (d) Typed FlagName runtime check | `TestRegister_UnknownFlag_ErrUnknownFlag` | `Register(FlagName("NoBogus"), &x)` returns `ErrUnknownFlag`. |
| §7 (e) Stable List output | `TestList_Stable` | After a non-trivial registration sequence, `List()` returns the registered FlagNames in **ascending order of the underlying string** (the natural `<` order). Asserted by comparing the result with a hand-rolled `sort.Strings`-style expected slice — not just by comparing two consecutive `List()` calls to each other. |

---

## 7. Security section

This package looks small but sits on the *trust path* of every experimental
result we will publish: if the ablation switch lies about being on, an entire
sweep's numbers are invalid in a way that is invisible from the output. The
mitigations below are mandatory. The Stage-2 plan
(`docs/specs/wt1-ablation-registry.plan.md`, written next in this worktree
right after this spec is approved) maps each mitigation (a)–(e) to a
specific Go test in its test matrix under the column "对应安全段". That
test matrix is the enforcement mechanism; the spec is considered satisfied
only when those tests exist and pass under `-race`. The required minimum
test names are spelled out in §6.1 above.

### (a) Goroutine safety

`Register`, `SetByName`, and `List` MUST hold the same `sync.Mutex` for the
duration of any read or write of the internal `map[FlagName]*bool` *and*
for the dereference write `*target = v` inside `SetByName`.
Why: Go runs `init` functions serially within a single goroutine, so the
narrow init-time hand-off is safe even without a lock — but the registry
also gets called *outside* init: by the CLI binder from its parse goroutine,
by diagnostic / `--list-ablations` code, and (in tests) by parallel
sub-tests that fan out registrations across goroutines. Without the lock
the map can corrupt under any of those concurrent paths, and a concurrent
`SetByName` could read a half-published map entry. Test gate: race-detector
tests with `t.Parallel()` over many goroutines.

### (b) Unknown name MUST NOT silent no-op

`SetByName("NoTpedContracts", true)` (a typo of `NoTypedContracts`) MUST
return `ErrUnknownFlag`. It MUST NOT return nil "because there was nothing
to flip anyway". The reason is not pedantry: if the CLI binder accepts an
unknown flag silently, an 8-hour ablation sweep can run with zero ablations
enabled while the operator's log shows "ran with --ablation
NoTpedContracts", and there is no post-hoc way to tell which `tee`'d run was
real and which was a typo. The single worst outcome for this package is
silent acceptance of an unknown flag name.

### (c) Duplicate ownership MUST NOT panic and MUST NOT overwrite

Two registrations resolve to the same owner if they share the same name
OR the same `*bool` target. Both arms must be rejected — the package
exists to prevent silent ownership ambiguity from EITHER direction.

**Name-aliasing:** If two packages both try to
`Register(ablation.NoX, &theirBool)`:

- The second call MUST return `ErrAlreadyRegistered`.
- The previously registered `*bool` MUST remain the one that future
  `SetByName(NoX, …)` writes through.
- The package MUST NOT `panic`. Reason: init-time panics propagate as
  `runtime: panic` and kill the binary before `main` runs, which is a
  trivially-exploitable DoS on a process whose only sin is linking two
  packages that both claim a flag. Returning an error lets the caller
  decide (panic in their own init if they want strict mode).

**Target-aliasing:** If a package mistakenly does

```go
ablation.Default.Register(ablation.NoCapabilityDiscovery, &DisableCapabilityDiscovery)
ablation.Default.Register(ablation.NoObserver,            &DisableCapabilityDiscovery) // copy-paste, wrong var
```

the second call MUST return `ErrTargetAlreadyRegistered` and MUST NOT
wire the second name. Without this rule, `--ablation NoCapabilityDiscovery`
and `--ablation NoObserver` would silently flip the SAME toggle, which is
the same "two flags secretly share an owner" failure surface as the
name-aliasing case — just discovered through the consumer's copy-paste
instead of through two packages competing for the same name. The §5
consumer pattern (`panic(err)` in init on Register failure) means a
target-aliasing typo fails at process start, before any ablation sweep
runs.
- The package MUST NOT silently overwrite. Reason: silent overwrite means
  whichever package was init'd second wins. Even though Go does define a
  deterministic init order from the import graph, flag *ownership* must
  not depend on that order — a future refactor that reshuffles imports
  would then silently swap which `DisableXxx` the CLI controls, with no
  build-time or runtime signal. Hard-erroring at the second `Register`
  makes the duplicate ownership a compile-time-equivalent surface
  (init-time panic at the call site, via the `panic(err)` pattern in §5).

### (d) Typed `FlagName`, not raw strings

Exported flag names are `FlagName` constants, and the **strong** mitigation
is convention plus a defensive runtime check: `Register` rejects any
`FlagName` value not in `KnownFlags()` with `ErrUnknownFlag` (see §3). So a
caller who writes `ablation.Default.Register("NoTpedContracts", &x)` — Go
*does* permit an untyped string literal to be implicitly converted to a
defined string type at the call site, so this *does* compile — fails fast
at init with a clear sentinel error rather than running a half-wired
ablation.

What `FlagName` buys us in addition is that the *recommended* call site
`ablation.Default.Register(ablation.NoTpedContracts, &x)` (using an
identifier, not a string literal) fails at `go build` because the
identifier does not exist. We document and require the identifier form in
the consumer contract (§5) and rely on code review + the runtime check
above to catch the rare case where a contributor falls back to a string
literal. The more dangerous case — a typo'd string in a config file that
flows into `SetByName` — is caught by mitigation (b).

`SetByName` accepts `string` (because CLI / config input is always
`string`), and its mitigation is (b): unknown names are loudly rejected
rather than silently ignored.

### (e) Stable `List()` output

`List` MUST sort its output (the natural ordering on `FlagName` /
`sort.Slice` with `<` on the underlying string is sufficient). Go map
iteration order is intentionally randomised; tests and diagnostics that
diff `List()` output between runs must see byte-identical results, or they
will produce spurious flakes in CI and spurious diffs in
`--list-ablations` output, both of which erode trust in the ablation
machinery.

### Threat model summary

The two failure modes that this package exists to prevent are:

1. **Silent typo** (mitigation: (b), (d)) — operator thinks the ablation
   flag was on, but it wasn't. Output: invalid metrics, paper retraction.
2. **Non-deterministic flag ownership** (mitigation: (c)) — two
   registrations resolve to the same owner (either by sharing a name or
   by sharing a `*bool` target via a copy-paste in the consumer). Either
   way the "winning" target depends on the import graph or on which line
   of init() ran first, and would silently change under an unrelated
   refactor. Output: irreproducible experiments.

Mitigations (a) and (e) are correctness substrate that makes (b) and (c)
actually hold under realistic runtime / test concurrency (CLI binder
goroutine, parallel sub-tests).

---

## 8. Out of scope (explicit, to prevent worktree creep)

- CLI flag parsing / `cobra` / `flag` integration.
- Any consumer-package `init()` calling `Default.Register(...)` — Tagged work
  for the downstream Phase 1 / Phase 2 worktrees, not this one.
- `Unregister`, dynamic re-registration, hot reload.
- Metrics / observer hooks for "ablation X was flipped at time T". If those
  are needed they belong to WT-2-flag-integration or to the consuming
  package's observer code, not here.
- Persistence: the registry is in-memory for the life of the process.
