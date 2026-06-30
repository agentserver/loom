# WT-1-ablation-registry — Implementation Plan

> Plan for the Stage-3 TDD work that satisfies
> `docs/specs/wt1-ablation-registry.spec.md`.
> Branch: `paper/v3/p1-ablation-registry`.
> Base: `origin/paper/v3-integration @ 1332327`.
>
> **Status update (post-review):** Stage-3 implementation has been amended
> with `ErrTargetAlreadyRegistered` plus the corresponding
> `TestRegister_SameTargetUnderTwoNames_Rejected` /
> `TestRegister_SamePairTwice_ErrAlreadyRegistered` regression tests; the
> race tests gained a `runtime.Gosched()` / ready-gate / `maxSeen`
> assertion; `t.Parallel()` was extended to every test. The sections
> below have been synced to match. The spec (sibling file) remains the
> authoritative contract source — if anything here drifts, prefer the
> spec.

This plan turns each spec deliverable into a file and each spec security
mitigation into a Go test, then sequences them strictly RED → GREEN →
REFACTOR.

---

## 1. File breakdown

All paths are relative to the Go module root,
`multi-agent/` (full path
`/root/multi-agent/.worktrees/p1-ablation-registry/multi-agent/`).

| File | Purpose | Public symbols (must be in this file) | Imports |
| --- | --- | --- | --- |
| `internal/ablation/doc.go` | Package-level godoc only. Describes the registry's role and the consumer pattern from spec §5. No code. | — | (none) |
| `internal/ablation/errors.go` | Sentinel error values. | `ErrUnknownFlag`, `ErrNilTarget`, `ErrAlreadyRegistered`, `ErrNotRegistered`, `ErrTargetAlreadyRegistered` | `errors` |
| `internal/ablation/registry.go` | Core types, constants, methods, and `Default`. | `FlagName`, the 8 `No*` constants, `KnownFlags() []FlagName`, `Registry`, `NewRegistry()`, `(*Registry).Register`, `(*Registry).SetByName`, `(*Registry).List`, `Default` | `sort`, `sync` |
| `internal/ablation/registry_test.go` | Functional + security test matrix. `package ablation` (white-box) — tests need to construct fresh `Registry` instances and pass typed `FlagName` values. | (tests) | `errors`, `runtime`, `strings`, `sync`, `sync/atomic`, `testing`, `time` |

Notes:

- `errors.go` and `registry.go` are split rather than merged so the
  sentinel set is greppable in one place. This matches the convention used
  in `multi-agent/internal/observerstore/store.go`, where the top of the
  file groups its `Err*` sentinels.
- `doc.go` is the conventional location for the `// Package ablation ...`
  comment block so it is not buried under `registry.go`'s imports.
- The test file uses `package ablation` (not `ablation_test`) because two
  of the security tests (`TestRegister_UnknownFlag_ErrUnknownFlag`,
  `TestList_Stable`) need to call private helpers / construct
  unexported-state fixtures cheaply. White-box testing is the lower-risk
  choice for a 100-line package; we don't have a separate "public
  consumer" surface to keep honest.

### 1.1 What this plan does NOT touch

Per spec §1: nothing under `multi-agent/internal/` other than the new
`internal/ablation/` directory; no edits to `go.mod` / `go.sum`; no CLI
binding; no consumer-package `init()` calls.

---

## 2. Implementation sketch (anchor for TDD; not the actual code)

```go
// errors.go
package ablation

import "errors"

var (
    ErrUnknownFlag       = errors.New("ablation: unknown flag")
    ErrNilTarget         = errors.New("ablation: nil target")
    ErrAlreadyRegistered = errors.New("ablation: flag already registered")
    ErrNotRegistered     = errors.New("ablation: flag not registered")
    // (post-review addition; precedence: name-duplicate beats target-duplicate.)
    ErrTargetAlreadyRegistered = errors.New("ablation: target already registered under another flag")
)
```

```go
// registry.go
package ablation

import (
    "sort"
    "sync"
)

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

// canonicalFlags is the unexported source of truth used by both
// KnownFlags() (which always copies it) and the Register/SetByName
// validity check (which uses the set form).
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

var canonicalSet = func() map[FlagName]struct{} {
    s := make(map[FlagName]struct{}, len(canonicalFlags))
    for _, f := range canonicalFlags { s[f] = struct{}{} }
    return s
}()

// KnownFlags returns a fresh copy in the canonical (declaration) order.
func KnownFlags() []FlagName {
    out := make([]FlagName, len(canonicalFlags))
    copy(out, canonicalFlags)
    return out
}

type Registry struct {
    mu      sync.Mutex
    targets map[FlagName]*bool
}

func NewRegistry() *Registry {
    return &Registry{targets: make(map[FlagName]*bool)}
}

func (r *Registry) Register(name FlagName, target *bool) error {
    if _, ok := canonicalSet[name]; !ok {
        return ErrUnknownFlag
    }
    if target == nil {
        return ErrNilTarget
    }
    r.mu.Lock()
    defer r.mu.Unlock()
    if r.targets == nil { r.targets = make(map[FlagName]*bool) } // lazy init for zero-value Registry
    if _, dup := r.targets[name]; dup {
        return ErrAlreadyRegistered // (precedence: name-duplicate wins over target-duplicate below)
    }
    for _, existingTarget := range r.targets {
        if existingTarget == target {
            return ErrTargetAlreadyRegistered
        }
    }
    r.targets[name] = target
    return nil
}

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

var Default = NewRegistry()
```

```go
// doc.go
// Package ablation is the typed flag registry for paper-v3 ablation
// experiments. Downstream packages register a *bool via
// Default.Register(...) in init(); the CLI binder (Phase 2
// WT-2-flag-integration) flips them via Default.SetByName(name, true)
// before workload start. See docs/specs/wt1-ablation-registry.spec.md.
package ablation
```

The sketch above is what GREEN looks like; the TDD loop in §4 writes the
RED tests first and only adds enough code to flip each RED → GREEN.

---

## 3. Test matrix

Each row maps to a test function in `registry_test.go`. "对应安全段" cross-
references the spec §7 security mitigations.

| 测试名 | 验证什么 | 对应安全段 |
| --- | --- | --- |
| `TestRegister_Success` | Single valid `Register(NoCapabilityDiscovery, &b)` returns nil; subsequent `SetByName("NoCapabilityDiscovery", true)` makes `b == true`. | — (happy path) |
| `TestRegister_NilTarget_ErrNilTarget` | `Register(NoObserver, nil)` returns `errors.Is(err, ErrNilTarget)`. | (a) defensive |
| `TestRegister_UnknownFlag_ErrUnknownFlag` | `Register(FlagName("NoBogus"), &b)` returns `ErrUnknownFlag`; ensures the runtime-side of the (d) mitigation is wired. | (d) |
| `TestRegister_Duplicate_ErrAlreadyRegistered` | Two `Register(NoDryRun, ...)` calls — second returns `ErrAlreadyRegistered`; does NOT panic; the **first** target is the one a subsequent `SetByName("NoDryRun", true)` flips (second target remains `false`). | (c) name-aliasing |
| `TestRegister_SameTargetUnderTwoNames_Rejected` | `Register(NoCapabilityDiscovery, &b); Register(NoObserver, &b)` — second returns `ErrTargetAlreadyRegistered`; the second name is NOT wired (SetByName on it returns `ErrNotRegistered`, `*b` unchanged). | (c) target-aliasing |
| `TestRegister_SamePairTwice_ErrAlreadyRegistered` | Idempotent re-Register with the same name AND same `*bool` returns `ErrAlreadyRegistered` (NOT `ErrTargetAlreadyRegistered`); pins the documented precedence rule. | (c) precedence |
| `TestSetByName_Unknown_ErrUnknownFlag` | `SetByName("NoTpedContracts", true)` returns `ErrUnknownFlag`; explicitly NOT nil. This is the headline (b) test. | (b) |
| `TestSetByName_NotRegistered_ErrNotRegistered` | Known flag, no prior Register → `ErrNotRegistered`. | (b) |
| `TestSetByName_Flips` | true → false → true cycle on a registered target. | — |
| `TestList_Stable` | After registering an out-of-order subset (e.g. `NoObserver`, `NoDryRun`, `NoAcceptanceGate`), `List()` returns `[NoAcceptanceGate, NoDryRun, NoObserver]` — i.e. ascending string order — and a second call returns a slice equal element-by-element to the first. The expected slice is hand-rolled in the test, not derived from a second `sort.Slice` of the result. | (e) |
| `TestKnownFlags_CopyIsolation` | Mutating the slice returned by `KnownFlags()` does not affect a subsequent `KnownFlags()` call; length is 8; element 0 is `NoCapabilityDiscovery`. | (e) — copy isolation per spec §4 last bullet |
| `TestConcurrent_Register_Race` | 8 goroutines, each registering a different one of the 8 known flags concurrently (`sync.WaitGroup` + `t.Parallel()`); all return nil; final `len(r.List()) == 8`. Run with `-race`. | (a) |
| `TestConcurrent_SetByName_Race` | One known flag, 100 goroutines calling `SetByName("NoObserver", g%2==0)` concurrently; the final `*target` value is allowed to be either true or false (race semantics). Test then performs two deterministic post-`wg.Wait()` writes and asserts `*target` reflects each — catches a future refactor that silently drops the deref. | (a) + write-observed contract |
| `TestConcurrent_RegisterSetList_Race` | Mixed workload, released via a `ready`-gate barrier so the List goroutine genuinely runs concurrently with Register/SetByName. List loop uses a 50ms wall-clock budget + `runtime.Gosched()` (so it interleaves under `GOMAXPROCS=1`) and tracks `maxSeen` via `sync/atomic`. Asserts (1) no `-race` failure, (2) `maxSeen > half` (proves concurrency was observed). | (a) |
| `TestSentinels_AreDistinct` | Each sentinel's `.Error()` starts with `"ablation: "`; no sentinel matches a different one via `errors.Is` (catches accidental `var ErrFoo = ErrBar` aliasing). | — |
| `TestDefault_IsRegistryAndIndependent` | `Default != nil`; the same singleton returned across two reads; `NewRegistry() != Default`. Guards against someone refactoring `Default` into a `func` or a per-call constructor. | — |
| `TestZeroValueRegistry_DoesNotPanic` | `var r ablation.Registry; r.Register(...)` must not panic on the nil-map write (lazy init), and `SetByName` / `List` on a never-Register-ed zero-value Registry return `ErrNotRegistered` / empty respectively. | (a) defensive |

Verification commands (Stage-3 must run all three with zero output / zero
non-zero exit):

```bash
cd multi-agent
gofmt -l internal/ablation/
go vet ./internal/ablation/...
go test ./internal/ablation/... -count=1 -shuffle=on -race
```

`-shuffle=on` is a low-cost guard against accidental ordering coupling
between tests (e.g. a future contributor reusing a shared package-level
variable across two tests and relying on registration order). It is not
what catches the race-detector findings — those come from the goroutine
interleavings inside each `TestConcurrent_*` test, not from the order
`go test` invokes top-level functions in.

---

## 4. TDD execution order

Strict RED → GREEN → REFACTOR; each step is its own commit candidate (we
can squash at the end if the tree is noisy, but during development the
commits make the RED/GREEN evidence reviewable).

1. **Skeleton + doc.go + errors.go** *(non-TDD scaffolding step; the
   RED/GREEN loop starts at step 2)*
   - Write `doc.go` with the package comment.
   - Write `errors.go` with the five sentinels (the four originals plus
     `ErrTargetAlreadyRegistered` from the post-review amendment).
   - Write `registry_test.go` containing only `package ablation` and an
     empty import block so the test binary compiles.
   - We deliberately do NOT add `TestSentinels_AreDistinct` here; it is
     pulled in at step 6 as a final-pass regression smoke test, where it
     belongs (it can't be a meaningful RED since "two distinct error
     values are distinct" is true the moment they are typed).

2. **FlagName + constants + KnownFlags + KnownFlags isolation**
   - First, add `registry.go` with ONLY the minimum stubs needed for the
     test file to compile: `type FlagName string`, the 8 `No*` constants,
     and `func KnownFlags() []FlagName { return nil }` (deliberately
     broken). Empty `Registry` struct + `NewRegistry() *Registry` stub
     returning `&Registry{}`. No internal map yet.
   - RED: write `TestKnownFlags_CopyIsolation` (asserts length 8, element
     0 is `NoCapabilityDiscovery`, mutating the returned slice does not
     affect a subsequent call). Run `go test` and confirm it fails on the
     length / element assertion.
   - GREEN: add `canonicalFlags`, `canonicalSet`, and make
     `KnownFlags` copy from `canonicalFlags`. Re-run; expect pass.

3. **Register happy + error paths**
   - RED: leave `Register` as a stub returning `nil`. Add
     `TestRegister_Success`, `TestRegister_NilTarget_ErrNilTarget`,
     `TestRegister_UnknownFlag_ErrUnknownFlag`,
     `TestRegister_Duplicate_ErrAlreadyRegistered`. Run; expect the four
     error-path tests to fail (success test happens to pass against the
     stub, which is fine — TDD discipline is "no implementation code
     without a failing test", and four failing tests bound the next
     implementation step).
   - GREEN: implement `Register` per the sketch in §2 (canonicalSet
     check → nil target check → mutex → duplicate check → store). Re-run;
     expect all 4 to pass.

3.5. **Register target-aliasing rejection** *(post-review addition; the §7
     (c) target-aliasing arm of the security mitigation)*
   - RED: extend `Register`'s test surface with
     `TestRegister_SameTargetUnderTwoNames_Rejected` and
     `TestRegister_SamePairTwice_ErrAlreadyRegistered`. The first will
     fail at compile time (`ErrTargetAlreadyRegistered` does not yet
     exist); the second will fail at runtime because the implementation
     from step 3 will silently accept the second Register and write
     through the duplicate target.
   - GREEN: add the `ErrTargetAlreadyRegistered` sentinel to
     `errors.go`. In `Register`, after the name-duplicate check (which
     by the spec §7 (c) precedence rule takes priority), add a linear
     scan over `r.targets` rejecting the call if any existing entry has
     the same `*bool`. The pre-existing FlagName of the conflicting
     target is NOT surfaced in the sentinel; spec §2.5's `%w`-wrapping
     rule reserves that surface for a future enrichment.
   - Verify both tests now GREEN; verify the original `TestRegister_*`
     tests still GREEN.

4. **SetByName + List**
   - RED: leave `SetByName` and `List` as stubs (`func (r *Registry)
     SetByName(string, bool) error { return nil }`, `func (r *Registry)
     List() []FlagName { return nil }`). Add tests in this order so the
     most security-critical one is RED first:
     1. `TestSetByName_Unknown_ErrUnknownFlag` — this MUST be the first
        test added and the first to fail; it is the spec's headline (b)
        security regression test. A naive implementation that only checks
        the map (and not `canonicalSet`) would return `ErrNotRegistered`
        instead of `ErrUnknownFlag`, which still looks "loud" but is the
        wrong sentinel and would let the CLI binder misclassify a typo
        as a "package not linked" diagnostic.
     2. `TestSetByName_NotRegistered_ErrNotRegistered`
     3. `TestSetByName_Flips`
     4. `TestList_Stable`
   - Run; expect all four to fail.
   - GREEN: implement `SetByName` (canonicalSet check BEFORE map lookup —
     this is what makes test 1 pass with the correct sentinel) and `List`
     (mutex, copy keys, `sort.Slice` ascending). Re-run; expect all four
     to pass.

5. **Concurrent / race tests** *(post-GREEN verification step — see below
   for why this is not a fresh RED/GREEN loop)*
   - The mutex lives in `Register`, `SetByName`, and `List` from steps 3
     and 4 because those tests' own correctness assertions require the
     map to be coherent on a single-goroutine timeline; the race tests
     here verify that the mutex is sufficient under concurrency, not
     whether it exists. So this step is explicitly labelled
     post-GREEN / property-based verification, not a strict RED → GREEN
     cycle.
   - Add `TestConcurrent_Register_Race`, `TestConcurrent_SetByName_Race`,
     `TestConcurrent_RegisterSetList_Race`. Run under `-race`.
   - Expected outcome: GREEN on the first run. If `-race` fires or any
     test panics, that's a real bug in the step-3/4 implementation —
     fix in-place; do NOT weaken the test.
   - Discipline note: this is the one step in §4 that intentionally skips
     RED. The compensating control is that the security mitigations in
     spec §7(a) are already encoded in the §3 test matrix and the §2
     sketch (mutex spans the `*target = v` write); the race tests are
     a guard, not a primary driver.

6. **Default singleton**
   - RED: add `TestDefault_IsRegistryAndIndependent` to the test file
     first (asserts `Default != nil`, `Default == Default` across two
     reads, `NewRegistry() != Default`). Run; expect compile failure
     because `Default` is not yet defined.
   - GREEN: add `var Default = NewRegistry()` to `registry.go`. Re-run;
     expect pass.

7. **Final smoke tests**
   - Add `TestSentinels_AreDistinct` as a non-RED regression smoke test
     (it cannot meaningfully fail at this point — two distinct
     `errors.New` values are distinct — and is included only to guard
     against an accidental `var ErrFoo = ErrBar` aliasing in a future
     refactor). Document this intent in a comment on the test function.
   - Add `TestZeroValueRegistry_DoesNotPanic` (post-review addition)
     covering `var r ablation.Registry; r.Register(...)`, which a
     non-lazy `Register` would panic on (nil-map write). The current
     implementation lazy-inits `r.targets` under the mutex; the test
     pins that contract so a future refactor that removes the nil-check
     fails here instead of in production.

8. **Refactor pass**
   - Run `gofmt -w`, `go vet`, look for duplication.
   - Re-run the full command set in §3.
   - Confirm no other files in the repo changed (`git status --short`
     should show only `internal/ablation/*` + the two spec/plan docs).

---

## 5. Commit plan

Three logical commits, mirroring the spec's three artefacts. Each commit
ends with the required `Co-Authored-By: Claude Opus 4.8 (1M context)
<noreply@anthropic.com>` trailer.

1. `docs(ablation): WT-1 stage-1 spec ...` — already landed.
2. `docs(ablation): WT-1 stage-2 plan ...` — this file, lands at end of
   Stage 2.
3. `feat(ablation): WT-1 stage-3 internal/ablation package ...` — all four
   Go files together (doc.go, errors.go, registry.go, registry_test.go).
   The TDD step-by-step from §4 may be squashed into this one commit
   before the final review pass, since the value to a reviewer is the
   final passing diff plus the test matrix that proves the security
   mitigations; the intermediate RED commits are useful only locally.

`git push` is explicitly OUT — the worktree owner does not push. The user
merges `paper/v3/p1-ablation-registry` into `paper/v3-integration`
themselves.

---

## 6. Out-of-band checks before declaring Stage 3 done

- `git diff --stat origin/paper/v3-integration..HEAD` shows ONLY:
  - `docs/specs/wt1-ablation-registry.spec.md` (added)
  - `docs/specs/wt1-ablation-registry.plan.md` (added)
  - `multi-agent/internal/ablation/doc.go` (added)
  - `multi-agent/internal/ablation/errors.go` (added)
  - `multi-agent/internal/ablation/registry.go` (added)
  - `multi-agent/internal/ablation/registry_test.go` (added)
- `git diff origin/paper/v3-integration..HEAD -- multi-agent/go.mod multi-agent/go.sum` is empty (no module drift).
- `gofmt -l internal/ablation/` is empty.
- `go vet ./internal/ablation/...` clean.
- `go test ./internal/ablation/... -count=1 -shuffle=on -race` passes.
- Codex Stage-3 review on the diff reports no P0/P1.
