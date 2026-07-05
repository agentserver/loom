# WT-2-flag-integration — Spec

> Phase 2 §E5 (12 号 §E5). Worktree `paper/v3/p2-flag-integration`.
> Baseline: `origin/paper/v3-integration` (HEAD `1c41b29` at spec time —
> the "last-merge" Phase 2 checkpoint that already carries every prior
> Phase 1/2 `ablation.Default.Register` init site for the 8 canonical
> flags; see §7(h)).
> Scope: `multi-agent/tools/eval/runner/{flags.go,flags_test.go,main.go,README.md}` (flags.go / flags_test.go / README.md NEW; main.go **wire-only**).

## 1. Purpose

The 8 canonical ablation flags (`NoCapabilityDiscovery`, `NoTypedContracts`,
`NoDryRun`, `NoContractFormalization`, `NoUserPromotionPath`,
`NoAcceptanceGate`, `NoRegistryLookup`, `NoObserver` — 12 号 §E5) are
registered from their owning packages via
`ablation.Default.Register(name, &pkg.DisableXxx)` (Phase 1 WT-1-ablation-registry
+ per-worktree `internal/*/*ablation*.go` files). This worktree closes the
last-mile CLI binding: `eval-runner run ... --ablation NoXxx[,NoYyy,...]`
flips those `*bool` targets via `ablation.Default.SetByName` **before** any
subprocess spawn, and stamps a stable `baseline_or_ablation` string into
the per-run D1 row (`runs.baseline_or_ablation`, 08 号 line 260) so D2
metric extraction can `GROUP BY baseline_or_ablation` for the ablation
tables under §E1–§E4.

The binder is intentionally thin — it owns argument parsing and the
`baseline_or_ablation` string, nothing else. It does **not** define or
maintain the canonical flag list (the registry does), does **not** read
or write any package-private ablation state (`SetByName` is the only
mutation surface), and does **not** implement flag semantics (each
owning package owns its own `if pkg.DisableXxx { ... }` guard).

**Anti-forgery invariant.** The `baseline_or_ablation` string in the CSV
row is DERIVED from the applied flag set — never accepted as a
free-form opts field. The applied flag set lives on
`Opts.AblationFlags []ablation.FlagName`; `Run` calls
`ApplyAblationFlags(opts.AblationFlags)` at pre-flight and then computes
the label via `ComputeBaselineOrAblation(opts.AblationFlags, ...)`. A
direct API caller cannot pass `Opts{BaselineOrAblation: "NoObserver"}`
to run full-Loom and misattribute the row: `Opts` has NO
`BaselineOrAblation` field (`Run` computes it internally). See §7(a).

An auxiliary top-level CLI verb `eval-runner --list-ablations` prints
the registered flag names + one-line descriptions (two tab-separated
columns per line — the Phase 1 `Registry.List()` API exposes names
only; a current-value column would require reaching into owner-package
private state, which §7(e) forbids) without running a workload — the
operator smoke that catches "a Phase 2 worktree forgot to Register"
before it silently no-ops in an experiment.

## 2. Deliverables

**Package**: every file in `tools/eval/runner/` is `package main`
(main.go:6, runner.go:1, writer.go:1, subprocess.go:1, …). The new
`flags.go` / `flags_test.go` MUST be `package main` too — the
function names referenced in this spec (`ApplyAblationFlags`,
`ScrubAmbientAblationEnv`, `ListAblationsText`,
`ComputeBaselineOrAblation`, `ValidateBaselineOrAblation`,
`AblationList`, and the constants) are package-scoped and called
unqualified from main.go / runner.go. Nothing in this spec creates
a new sub-package.

### 2.1 `multi-agent/tools/eval/runner/flags.go` (NEW, `package main`)

Owns:

- `AblationList` — an implementation of `flag.Value` for
  `--ablation`. Accepts one comma-joined value **and** multiple `--ablation`
  repetitions; stores unique canonical `ablation.FlagName` values in
  insertion order (dedup on first-seen). See §3.
- `ApplyAblationFlags(flags []ablation.FlagName) error` — thin wrapper
  that forwards to `applyAblationFlagsTo(ablation.Default, flags)`.
  Callers in main.go and `Run` use this exported form.
- `applyAblationFlagsTo(reg *ablation.Registry, flags []ablation.FlagName) error`
  (unexported) — runs in **three atomic phases**:

    **Phase 1: Validate.** Iterate `flags` and, for each `fn`,
    require that the flag both exists in the registry's canonical
    set and has a target registered. Neither check writes anything;
    both are pure lookups. Any failure (`ErrUnknownFlag` or
    `ErrNotRegistered`) returns immediately with no side effects.
    The check uses `reg.SetByName` implicitly via a peek-only
    helper: iterate `reg.List()` and match against `flags` for the
    `ErrNotRegistered` case; `ablation.KnownFlags()` (canonical set
    copy) for the `ErrUnknownFlag` case. **No `*target = v` write
    has happened yet at the end of Phase 1.**

    **Phase 2: Reset.** Iterate `reg.List()` (every REGISTERED
    canonical flag) and call `reg.SetByName(string(fn), false)` +
    the owner's `Sync…` hook + `os.Unsetenv` for any
    Python-companion env var. This scrubs stale state from a prior
    `Run` call — see §7(a.4). If Phase 2 encounters an error
    (shouldn't happen because we're iterating `reg.List()` output,
    but defence-in-depth), it returns and the process is in a
    known-bad state — the caller (`Run`) treats this as a hard
    failure and exits 2 rather than proceeding with partial state.

    **Phase 3: Apply.** Iterate `flags` (dedupe as we go for defence
    against direct callers) and call `reg.SetByName(string(fn), true)`
    + owner `Sync…` + `os.Setenv` for the Python-companion. Because
    Phase 1 already validated every entry, Phase 3 cannot fail with
    `ErrUnknownFlag` / `ErrNotRegistered` — if it does, the failure
    is a registry mutation between Phase 1 and Phase 3 (impossible
    in the pre-run-only mutation contract), and we return the error
    verbatim as a bug signal.

  Order matters: Phase 2 (reset) BEFORE Phase 3 (apply) means a
  caller who transitions from `Run(...NoObserver)` to `Run(...nil)`
  (subsequent call, empty flag list) gets ALL canonical flags reset
  to false — no stale `NoObserver=true` leak across `Run` calls.

  The sync-hook calls are fire-once per invocation (idempotent —
  see the sync functions' contracts) and never error.

  The `reg` parameter exists so that
  `TestApplyAblationFlags_UnregisteredFlagReturnsErrNotRegistered`
  can construct a fresh `ablation.NewRegistry()` that intentionally
  never registered any target and drive the negative path — this is
  the only Go-idiomatic way to exercise `ErrNotRegistered` when the
  production `Default` has every canonical flag wired via blank
  imports (§7(h)).
- `ComputeBaselineOrAblation(flags []ablation.FlagName, baseline string) (string, error)`
  — returns the stable `baseline_or_ablation` string per §4. `baseline`
  is the default when no ablation is applied (canonical `"full_loom"`;
  overridable via `--baseline-name`, mirroring the baselines harness's
  `--baseline-name`, and validated by the same
  `^[a-z][a-z0-9_-]{2,63}$` regex reused from
  `multi-agent/tests/eval/baselines/harness/row.go` — see §7(c)).
- `ValidateBaselineOrAblation(s string) error` — the belt-and-braces
  final check. Reused by BOTH `flags.go` (before returning from
  `ComputeBaselineOrAblation`) AND by `Run` (before assembling
  `RunRow`, closing the "direct API caller bypasses main.go"
  loophole). Returns wrapped `ErrBaselineOrAblationInvalid` /
  `ErrBaselineOrAblationTooLong` sentinels; `Run` maps those to exit
  2 via `preflight(...)`.
- `ListAblationsText() string` — the multi-line text `--list-ablations`
  prints. Each line: `<FlagName>\t<one-line description>` — TWO
  tab-separated fields, no bool column (Phase 1 `Registry.List()`
  exposes only names — see §7(e); a current-value column would
  require reaching into owner-package private state). Descriptions
  live in a `flags.go`-local `map[ablation.FlagName]string` named
  `flagDescriptions` — one line each, sourced from the flag-owner's
  spec header (see §6 table). No PATH / config / env content; see
  §7(d).

Constants:

- `const DefaultBaselineName = "full_loom"` — the string the runner
  stamps into `baseline_or_ablation` when no `--ablation` value is passed
  and `--baseline-name` is not supplied. Lowercase snake-case so the
  string satisfies `^[a-z][a-z0-9_-]{2,63}$` (the baselines harness
  regex reused for `baseline_or_ablation` — see §7(c)) — union-typed
  with the CamelCase `+`-join used for ablation combinations.
- `const MaxBaselineOrAblationLen = 200` — hard cap on the composed
  string. Derivation: 8-flag sorted join =
  `NoAcceptanceGate + NoCapabilityDiscovery + NoContractFormalization +
  NoDryRun + NoObserver + NoRegistryLookup + NoTypedContracts +
  NoUserPromotionPath` = 129 chars of flag name + 7 `+` separators =
  **136 chars**. Cap of 200 leaves ~64 chars slack for two more
  ~30-char flags before a re-derivation is needed. See §7(c).

Owner-package blank imports (line-count kept small, one per package):

```go
import (
    _ "github.com/yourorg/multi-agent/internal/ablation"          // NoAcceptanceGate (Python-companion; skill_flags.go init)
    _ "github.com/yourorg/multi-agent/internal/capability"        // NoCapabilityDiscovery (snapshot.go init)
    _ "github.com/yourorg/multi-agent/internal/contract"          // NoTypedContracts, NoContractFormalization (ablation.go init)
    _ "github.com/yourorg/multi-agent/internal/contract/validator" // NoDryRun (ablation.go init)
    _ "github.com/yourorg/multi-agent/internal/driver"            // NoRegistryLookup, NoUserPromotionPath (registry_lookup_ablation.go + promote_candidate_ablation.go init)
    _ "github.com/yourorg/multi-agent/internal/evalrun"           // NoObserver (schema.go init)
)
```

Rationale: `SetByName` returns `ErrNotRegistered` if the owning
package was not linked (registry.go:129). Without these blank imports,
the runner binary compiles clean but `--ablation NoObserver` exits 2
at runtime with a confusing "flag not registered" — the operator has
no signal that the fix is a Go-side link, not a CLI typo. §7(h) CI
`TestApplyAblationFlags_AllKnownFlagsBind` is the earliest signal.

Live imports (not blank) for the packages providing sync hooks:

```go
import (
    "github.com/yourorg/multi-agent/internal/capability"           // capability.SyncDisableUpload
    "github.com/yourorg/multi-agent/internal/contract/validator"   // validator.SyncDisableDryRun
)
```

(The driver and evalrun packages don't need explicit sync hooks —
their reads happen through `IsNoXxx()` accessors that either don't
mirror to atomic or the read side is single-goroutine at CLI-parse
time. See §6 table.)

Nothing else in this file: no wire tables, no ablation semantics, no
per-workload logic. The subprocess-env bridge for `NoAcceptanceGate`
uses `os.Setenv` (§5.4) — no custom injection into `WhitelistEnv`
because `WhitelistEnv` already propagates any `LOOM_*` key from the
parent process env unconditionally (subprocess.go §281).

### 2.2 `multi-agent/tools/eval/runner/flags_test.go` (NEW)

Table-driven blackbox tests covering §3 accept/reject, §4 join semantics,
§5 side-effects, and every §7 (a)–(h) security mitigation. See plan.md
§3 for the exact matrix.

### 2.3 `multi-agent/tools/eval/runner/main.go` (WIRE-ONLY EDITS)

Three additions, no restructuring:

1. Top-level dispatch: `os.Args[1] == "--list-ablations"` (mutually
   exclusive with `run`) prints `ListAblationsText()` to stdout
   and exits 0. Order in `main()` is `--list-ablations` handled first
   → `run` second → usage/exit-2 fallback.
2. Inside `runMain`: register `--ablation` (via `fs.Var(&ablationList, ...)`)
   and `--baseline-name` (StringVar defaulting to `DefaultBaselineName`).
3. After `fs.Parse` succeeds and before `Run(ctx, Opts{...})`:
   - stash the parsed set onto `Opts.AblationFlags []ablation.FlagName`
     and `Opts.BaselineName string` (defaulting to
     `DefaultBaselineName` when the operator did not pass
     `--baseline-name`). Both are inputs — never the derived label.
   - unconditionally call
     `ScrubAmbientAblationEnv()` (see §7(a) + §5.4 for the
     mechanism) so that a parent-process
     `LOOM_ABLATION_NOACCEPTANCEGATE=1` cannot silently activate the
     Python bridge under a `--ablation`-less invocation.

`Run` (in `runner.go`) owns the mutation + derivation, not `main.go`:

1. `Run` calls `ScrubAmbientAblationEnv()` FIRST inside its body
   (before ANY existing preflight), matching main.go's scrub in
   §2.3. This mirror-call is what closes the "direct `Run` caller
   inherits a parent-process `LOOM_ABLATION_NOACCEPTANCEGATE=1` and
   silently bypasses the acceptance gate" loophole — main.go's scrub
   catches CLI entrypoints, `Run`'s scrub catches every other
   entrypoint. Scrub is idempotent (`os.Unsetenv` on an already-unset
   var is a no-op), so both call sites can safely fire.
2. `Run` runs all existing preflight validators verbatim, in the
   established order: `validateStubListen` → `validateObserverDB` →
   `validateCodexConfig`. **No reordering.** `ApplyAblationFlags` is
   NOT called yet — because it mutates process-global bools, calling
   it before validators pass would leave a rejected invocation in an
   ablated process state (Codex P0 round 3).
3. After ALL preflight validators have returned nil AND after
   `LoadWorkloadSpec`, `SetupWorkspace`, `startStub`, and
   `waitStubReady` have succeeded (i.e. every path that can return
   `preflight(...)` for something OTHER than an ablation error),
   `Run` calls `ApplyAblationFlags(opts.AblationFlags)`. On error
   (which at this point can only be `ErrNotRegistered` —
   `ErrUnknownFlag` was already caught at parse time by
   `AblationList.Set`), returns via `preflight` (exit 2).

   Placing the mutation this late means the only preflight path
   remaining after apply is `resolveOraclePath` (workload spec has
   already been loaded and validated once, so this is a small
   filesystem check) and `ErrOracleOutputTooLarge` (a runtime
   condition, not a preflight). A **`defer` in `Run` guarantees
   cleanup**: right after the successful `ApplyAblationFlags` call,
   `Run` schedules `defer applyAblationFlagsTo(ablation.Default, nil)`
   — a nil flag list triggers Phase 2 (reset) alone, resetting every
   canonical flag to false + clearing every Python-bridge env var.
   This fires on both the happy path (harmless — the process is
   about to exit) AND the exit-2 paths after apply (`resolveOraclePath`
   / `ErrOracleOutputTooLarge` — restores clean state so a test
   caller can immediately re-invoke `Run`). See §7(a.4).
4. `Run` computes
   `label, err := ComputeBaselineOrAblation(opts.AblationFlags, opts.BaselineName)`
   and, on error, exits 2 via `preflight`. `Opts` has NO
   `BaselineOrAblation` field — the label is derived from applied
   flags every time (see §7(a) forgery reject).
5. `Run` calls `ValidateBaselineOrAblation(label)` as
   belt-and-braces before stamping the row (regex + length; the
   ablation branch of `ComputeBaselineOrAblation` cannot produce
   invalid output today, but a future refactor that composes labels
   differently would be caught here).

Existing direct `Run(ctx, Opts{})` callers (runner_test.go,
runner_probes_smoke_test.go) get:

- `opts.AblationFlags == nil` → `ApplyAblationFlags(nil)` runs Phase
  2 (reset every registered flag to false + clear every
  Python-bridge env var) and skips Phase 3. This is NOT a no-op —
  §7(a.4) requires the reset, and existing callers benefit from it
  because it scrubs any stale process state a prior test left
  behind (see the deferred cleanup above). No `Opts` migration
  needed; the new behaviour is a strict improvement — a previously
  ablated process cannot leak into a subsequent test run.
- `opts.BaselineName == ""` → `Run` substitutes `DefaultBaselineName`
  before calling `ComputeBaselineOrAblation`, so callers that don't
  care about the field still get `baseline_or_ablation = "full_loom"`
  and no `Opts` migration.

No other main.go / runner.go structural changes. The pre-flight
order is:

```
[NEW] ScrubAmbientAblationEnv    (Run-side mirror of main.go scrub)
      validateStubListen         (unchanged)
      validateObserverDB         (unchanged)
      validateCodexConfig        (unchanged)
      LoadWorkloadSpec           (unchanged)
      SetupWorkspace             (unchanged)
      startStub                  (unchanged)
      waitStubReady              (unchanged)
[NEW] ApplyAblationFlags         (mutation; AFTER every existing preflight)
[NEW] defer applyAblationFlagsTo(Default, nil)  (cleanup on any subsequent return)
[NEW] ComputeBaselineOrAblation + ValidateBaselineOrAblation
      resolveOraclePath          (unchanged)
      RunSubprocess              (unchanged)
      … (rest of Run unchanged)
```

Nothing existing is reordered, no existing sentinel error is
renamed. The new steps' failure paths reuse the existing
`preflight` helper so stderr formatting stays uniform. The defer
ensures every exit path from `Run` (happy + exit-2 after apply)
scrubs the global registry to its zero state.

### 2.4 `RunRow` extension (`writer.go`) — APPEND-ONLY

Add `BaselineOrAblation string` to `RunRow`, append
`"baseline_or_ablation"` to `CSVColumns()`, append the same value to
`rowAsCSVRecord`. `Run` sets the field from the derived label (§2.3),
never from an `Opts` field.

This is a **schema migration** by the CSV column contract established
in WT-1-eval-runner-skeleton (`CSVColumns()` is "stable header order —
append-only"). It aligns the runner CSV with 08 号 line 260 and matches
the `baselines/harness/writer.go` `baseline_or_ablation` column already
in place for the E1/E2/E3 baselines (see §2.5).

### 2.5 `multi-agent/tools/eval/runner/README.md` (NEW)

Sections:

1. **What the runner is** — one paragraph pointer to
   `docs/specs/wt1-eval-runner-skeleton.spec.md` §1 for the full picture.
2. **`--ablation` and `--list-ablations`** — usage snippet, semantics.
3. **The 8 ablation flags — mapping to metrics** — §6 table verbatim,
   with a "predicted impact on §A/§B/§C/§D metrics" column so a reviewer
   can, at a glance, correlate `--ablation NoObserver` with §D
   Observer-overhead drop and §A run-schema completeness effects (see
   §7(f)). Each row also names the owning package `Register` call site,
   so a reviewer can jump from the CLI flag to the semantic gate in one
   grep.
4. **`baseline_or_ablation` string in the CSV** — regex, join rules,
   downstream consumer view (§7(g)).
5. **Adding a new flag** — pointer to `internal/ablation/registry.go`
   (canonical list) + `flags.go`'s `flagDescriptions` map (must be
   updated in the same PR that adds a `FlagName` constant, otherwise
   the CI check in plan.md §3 fails).

## 3. `--ablation` argument syntax

Accepted forms (all produce the same set):

```
--ablation NoObserver
--ablation NoObserver,NoAcceptanceGate
--ablation NoObserver --ablation NoAcceptanceGate
--ablation NoObserver,NoObserver              # dedup → {NoObserver}
--ablation NoObserver --ablation NoObserver   # dedup → {NoObserver}
--ablation ""                                 # rejected: empty entry (§7(a))
--ablation "  "                               # rejected: whitespace-only
--ablation "NoObserver, NoAcceptanceGate"     # accepted: leading/trailing
                                              #   whitespace on each
                                              #   entry is TrimSpace'd
--ablation NoTpedContracts                    # rejected: unknown → exit 2
--ablation Full                               # rejected: unknown → exit 2
```

`AblationList.Set(v)` splits on `,`, TrimSpace each entry, rejects
`""`, and on unknown FlagName returns
`fmt.Errorf("eval-runner: --ablation %q: %w", entry, ablation.ErrUnknownFlag)`.
Deduplication happens at insertion time (map-of-seen), preserving first
insertion order for stable error messages.

Rationale: comma-join matches the operator convention in the ablation
sweep scripts (Phase 2 WT-2-baselines already uses comma-joined baseline
lists in `run.sh`). Repetition support matches Go's `flag.Value`
convention and lets shell scripts build the list incrementally.

## 4. `baseline_or_ablation` string

`ComputeBaselineOrAblation(flags, baseline)`:

1. If `len(flags) == 0`: return `baseline` (which is
   `DefaultBaselineName == "full_loom"` unless the operator passed
   `--baseline-name`).
2. If `len(flags) > 0`:
   - **Dedup** first (map-of-seen, preserving nothing but membership).
     `AblationList.Set` already dedups, but a direct
     `Run(ctx, Opts{AblationFlags: []{NoObserver, NoObserver}})`
     call bypasses `AblationList`; deduping here makes
     `ComputeBaselineOrAblation` invariant across CLI and direct
     entrypoints. This is what §7(b) actually requires — the CLI
     dedup is a UX nicety, this dedup is the correctness gate.
   - Convert each `ablation.FlagName` to its string form.
   - Sort ascending by string (stable, deterministic — see §7(g)); this
     matches `ablation.Default.List()` sort convention.
   - Join with `+`. Example: `--ablation NoObserver,NoAcceptanceGate`
     → `"NoAcceptanceGate+NoObserver"`. Alphabetic order is stable
     across insertion order, so `--ablation
     NoObserver,NoAcceptanceGate` and `--ablation
     NoAcceptanceGate,NoObserver` both produce
     `"NoAcceptanceGate+NoObserver"`, keeping the D2 group key stable.
   - The `baseline` argument is **ignored** in this branch (an ablation
     run is not a baseline run; it always attributes to the
     concatenated flag names). This is deliberate: if an operator sets
     `--baseline-name my-run --ablation NoObserver`, the `my-run`
     string is dropped and `NoObserver` wins. `flags.go` logs a single
     stderr diagnostic in this case so the operator notices the
     unused override.
3. In both branches, validate the returned string via
   `ValidateBaselineOrAblation`:
   - `len(s) <= MaxBaselineOrAblationLen` (200). This ceiling covers
     the true 8-flag sorted join (129 chars of flag name + 7 `+`
     separators = 136 chars) with ~64 chars slack for two more
     ~30-char flags. Adding a 9th flag requires re-deriving this cap
     alongside `registry.go` canonicalFlags — plan.md §3
     `TestBaselineOrAblationCapCoversAll8Flags` computes the actual
     join length from `KnownFlags()` and asserts it stays under the
     cap; a canonicalFlags growth without a cap bump fails that
     test.
   - Match `^[a-z][a-z0-9_-]{2,63}$` (baseline regex) **OR**
     `^([A-Z][A-Za-z0-9]{2,31})(\+[A-Z][A-Za-z0-9]{2,31})*$`
     (ablation regex — CamelCase + `+`). Either match is valid; the
     union covers all runner-emitted strings (baseline-style and
     ablation-style) as well as harness-emitted rows from
     `tests/eval/baselines/harness/writer.go` (baseline regex only).
     Any string not matching either regex → error, exit 2.

## 5. Behaviour end-to-end

### 5.1 Happy path: no ablation

```
$ eval-runner run --workload cross-device-code-mod --stub-listen 127.0.0.1:18080 \
                  --out /tmp/run.csv
```

- `AblationList.Values()` empty → `ApplyAblationFlags` no-op → registry
  bools stay `false` → all 8 owning packages behave as normal.
- `baseline_or_ablation` = `"full_loom"`.

### 5.2 Happy path: two flags

```
$ eval-runner run ... --ablation NoObserver,NoAcceptanceGate --out /tmp/run.csv
```

- `SetByName("NoObserver", true)` flips `evalrun.DisableTelemetry` → the
  SQLWriter's Insert becomes a no-op-with-log (evalrun/schema.go §5).
- `SetByName("NoAcceptanceGate", true)` flips
  `ablation.noAcceptanceGate` → `IsNoAcceptanceGate()` returns true;
  see §5.4 for the Python bridge.
- `baseline_or_ablation` = `"NoAcceptanceGate+NoObserver"`.

### 5.3 Error paths (all exit 2, all `preflight`-style stderr)

| Trigger | stderr | exit |
|---|---|---|
| `--ablation ""` | `eval-runner: --ablation entry is empty` | 2 |
| `--ablation NoTpedContracts` | `eval-runner: --ablation "NoTpedContracts": ablation: unknown flag` | 2 |
| A `SetByName` returns `ErrNotRegistered` (owning package not linked, e.g. Phase 2 worktree forgot to import it) | `eval-runner: --ablation "NoUserPromotionPath": ablation: flag not registered` | 2 |
| Composed `baseline_or_ablation` exceeds 200 chars (defence-in-depth; 8-flag ceiling is 136 so this fires only if the registry grows without §4 update, or a direct API caller injects an oversized value) | `eval-runner: baseline_or_ablation length %d exceeds cap %d` | 2 |
| `baseline_or_ablation` matches neither the baseline nor the ablation regex (§4 union) | `eval-runner: baseline_or_ablation %q matches neither ^[a-z][a-z0-9_-]{2,63}$ nor ^([A-Z][A-Za-z0-9]{2,31})(\+…)*$` | 2 |
| `--baseline-name` fails `^[a-z][a-z0-9_-]{2,63}$` | `eval-runner: --baseline-name %q: must match ^[a-z][a-z0-9_-]{2,63}$` | 2 |
| Zero ablations AND custom `--baseline-name` that collides with a canonical flag string (`--baseline-name NoObserver`) | `eval-runner: --baseline-name %q collides with an ablation flag name` | 2 |

The `SetByName` order guarantees §7(a.1): unknown FlagName reject
BEFORE the run starts; no ablation experiment ever runs against a
typo'd flag. §7(a.2) (ambient env scrub) and §7(a.3) (no `Opts`
forgery) are silent — they succeed by removing the failure
surface, not by adding an error row here.

### 5.4 Python bridge for `NoAcceptanceGate`

The `NoAcceptanceGate` flag has a Python-side companion (see
`docs/specs/wt1-acceptance-golden.spec.md` §4.a). The Python runner
reads the env var `LOOM_ABLATION_NOACCEPTANCEGATE=1`.

The bridge is a single `os.Setenv` call inside `ApplyAblationFlags`,
gated on the flag being active. The env-var name is defined once, in
flags.go as a constant

```go
const EnvNoAcceptanceGate = "LOOM_ABLATION_NOACCEPTANCEGATE"
```

and the export happens per-flag via a small package-local table.
The key is a `FlagName` **string cast** rather than a direct
`ablation.NoAcceptanceGate` constant reference — §7(e) forbids
importing the individual constants; the string cast keeps the
canonical name for lookup while leaving the source of truth (the
registry `canonicalFlags`) untouched. A drift check
(`TestPythonBridgeKeysAreCanonical`) confirms every key is present
in `ablation.KnownFlags()`.

```go
// ablationEnvExports maps each Python-companion ablation flag to the
// env var the Python runner reads. Not all flags have a Python
// companion — the map is intentionally sparse.
var ablationEnvExports = map[ablation.FlagName]string{
    ablation.FlagName("NoAcceptanceGate"): EnvNoAcceptanceGate,
}
```

`applyAblationFlagsTo` iterates the applied flags; for each `fn`
present in `ablationEnvExports`, it calls
`os.Setenv(ablationEnvExports[fn], "1")` (before returning).

`ScrubAmbientAblationEnv()` (called from main.go at CLI entry — see
§2.3 + §7(a)) iterates every value in `ablationEnvExports` and calls
`os.Unsetenv(v)`. This runs BEFORE `fs.Parse`, so a parent-process
`LOOM_ABLATION_NOACCEPTANCEGATE=1` never leaks into a
`--ablation`-less invocation. If `--ablation NoAcceptanceGate` is
subsequently applied, `applyAblationFlagsTo` re-exports the env var
with the correct value.

**Why `os.Setenv` at the CLI seam rather than injecting via
`WhitelistEnv`**: `WhitelistEnv` is called inside `Run` from
`runner.go:214`, AFTER the pre-flight validators and workspace setup
but BEFORE oracle exec. Any subprocess spawned by `Run` — the oracle,
`commit_meta.collect`, `git log`, and the future acceptance-gate child
that ships as part of a workload — inherits the parent process env
via the existing `LOOM_*` prefix rule in `WhitelistEnv`
(subprocess.go:281). Setting it once at CLI parse time with
`os.Setenv` therefore reaches every subprocess through the same path
that already handles `LOOM_EVAL_COMMIT_META_CMD` and
`LOOM_EVAL_GIT_EMAIL_CMD` — no need to plumb a new argument through
`Run` and `Opts`, and no risk of missing a downstream call site.

Future flags with Python companions extend `ablationEnvExports` with
one more line; no other file changes. Test coverage: plan.md §3
`TestApplyAblationFlags_PythonBridgeExportsEnv` calls
`ApplyAblationFlags([]FlagName{NoAcceptanceGate})` and asserts
`os.Getenv("LOOM_ABLATION_NOACCEPTANCEGATE") == "1"`; a paired
`TestApplyAblationFlags_NoBridgeWhenFlagUnset` asserts the env var
stays empty when the flag is not applied.

### 5.5 `--list-ablations`

Each line = FlagName + `\t` + description. Rendered with the tab
literal made visible (`⇥` in this doc):

```
$ eval-runner --list-ablations
NoAcceptanceGate⇥[B3] Bypass MCP acceptance golden runner.
NoCapabilityDiscovery⇥[A1] Skip capability snapshot upload from slave to driver.
NoContractFormalization⇥[A2] All step delegates use natural language, no JSON contract.
NoDryRun⇥[A3] Skip contract validator dry-run in driver plan phase.
NoObserver⇥[D1] Suppress observer and evalrun D1 SQLite writes; log-only.
NoRegistryLookup⇥[B4] Skip dynamic MCP registry and userspace FTS5 lookup hint.
NoTypedContracts⇥[A2] Do not enforce JSON schema on step contracts; structure preserved.
NoUserPromotionPath⇥[B1] Suppress user-facing promote-candidate surface.
```

Rules (assertable in tests):

- Exactly `len(ablation.Default.List())` data lines. Any deviation
  either fails the test (Phase 2 worktree unlinked → line count drops)
  or fails the count assert (unlinked package → `ErrNotRegistered` at
  bind time, caught earlier).
- Lines sorted alphabetically (sort order tied to
  `ablation.Default.List()` — spec §7(g)).
- Exactly TWO tab-separated fields per line: field 1 = flag name,
  field 2 = one-line description ≤ 120 chars. NO current-value
  column: `Registry.List() []FlagName` returns names only, and §7(e)
  forbids reaching into owner-package private state to compute the
  bool. Operators who want to inspect a flag's current value use the
  owner-side accessor (`capability.IsUploadDisabled()`,
  `validator.IsDryRunDisabled()`, etc.) in a debugger or a Go test,
  not the CLI.
- The `[Xn]` prefix in field 2 (e.g. `[A1]`, `[B3]`, `[D1]`) names the
  12 号 §A/§B/§D section the flag belongs to. Deriving the section
  from a hard-coded string in `flagDescriptions` (not from a live
  lookup) is deliberate: this file is the wire from the code to the
  paper, and drift is caught by a plan.md §3 test that greps
  12 号 §E5 for the flag name to confirm the section letter still
  matches.
- Descriptions contain no path (`/`, `\`), no config-file name
  (`config.toml`, `.yaml`), no env var name (`LOOM_*`,
  `AGENTSERVER_*`), no URL — enforced by plan.md §3
  `TestListAblations_NoSensitiveContent` regex sweep.

Exit code 0. No file I/O. No subprocess spawn. No env inspection.

## 6. The 8-flag table (used by README + `--list-ablations` prefix and by the plan.md §3 CI grep test)

| # | FlagName | Owning package (Register call site) | Sync hook after SetByName | §12 section | Predicted metric impact |
|---|---|---|---|---|---|
| 1 | `NoCapabilityDiscovery` | `internal/capability` (`snapshot.go:688`) | `capability.SyncDisableUpload()` | §A1 | ↑ `HumanContextSelectionCount`, ↓ `RoutingAccuracy` (context set no longer includes freshly-discovered capabilities) |
| 2 | `NoTypedContracts` | `internal/contract` (`ablation.go:43`) | (none — reads via `contract.IsTypedContractsDisabled()` from a raw bool at pre-run time; owner package documents no atomic mirror) | §A2 | ↑ `ContractCompleteness` deficit → ↑ `Rework`, ↑ failure categories `contract_field_missing` / `type_error` |
| 3 | `NoContractFormalization` | `internal/contract` (`ablation.go:52`) | (none — same as row 2) | §A2 | Baseline for §A2 natural-language delegation; drops `ContractCompleteness` (the §A2 canonical metric) to N/A since there is no JSON contract to score; expect ↑ `Rework`, ↓ `TaskSuccessRate` vs `NoTypedContracts` |
| 4 | `NoDryRun` | `internal/contract/validator` (`ablation.go:54`) | `validator.SyncDisableDryRun()` | §A3 | Zeroes `PreExecutionFaultCatchRate`, `MissingArtifactDetectionRate`, `PolicyViolationPreventionRate` (see 12 号 §A3); small ↓ `DriverPlanningOverhead` |
| 5 | `NoUserPromotionPath` | `internal/driver` (`promote_candidate_ablation.go:27`) | (none — reads via `driver.IsNoUserPromotionPath()` from raw bool; owner ships no atomic mirror) | §B1 | Zeroes `PromoteCandidateSurfaceRate`; expect ↓ `LifecycleClosureRate` when a workload relies on promotion |
| 6 | `NoAcceptanceGate` | `internal/ablation` (`skill_flags.go:110` — Python-companion; env `LOOM_ABLATION_NOACCEPTANCEGATE=1` per §5.4) | (none — Python bridge is via env var, no in-process consumer) | §B3 | Bypasses acceptance golden → ↑ post-hoc contract violations in D4 category `acceptance_bypassed` |
| 7 | `NoRegistryLookup` | `internal/driver` (`registry_lookup_ablation.go:26`) | (none — reads via `driver.IsNoRegistryLookup()` from raw bool) | §B4 | Zeroes `RegistryLookupHitRate`; ↑ `ManualSetupStepCount` when the missing hint would have short-circuited a lookup |
| 8 | `NoObserver` | `internal/evalrun` (`schema.go:151`) | (none — read at first `SQLWriter.Insert` call from raw bool; single-goroutine at CLI time) | §D1 | Suppresses per-run D1 `runs`-row writes via `evalrun.DisableTelemetry` (log-only) → `runs` table stays empty for this run. Sibling observer writers (`route_reasons`, `probe_events`, `audit_events`, `promotion_audit`) are NOT gated by this flag today — 12 号 §D1 lists only the D1 `runs` row as the ablation surface; other tables remain populated so the run is still traceable |

Sync-hook column semantics: only two owner packages ship atomic
mirrors (`capability`, `validator`) — those two require an explicit
`Sync…` call after `SetByName` because their mirror is not updated by
the registry's `*target = v` write. The other five owner packages
read directly from the raw `*bool` (single-goroutine at pre-run time,
per each owner's ablation contract), so no sync call is needed. If a
future refactor adds an atomic mirror to a package that currently has
none (say `driver.noRegistryLookupAtomic`), the plan.md §3 test
`TestSyncHooksTableIsUpToDate` grep-audits `internal/**/*ablation*.go`
for the `atomic.` idiom and fails if a new `Sync…` function exists in
a package not listed in this column.

The exact numeric-line references pin the plan.md §3 CI grep test's
"which file to grep" pointers; edits to a Register call site that move
the line trigger a plan-side test failure that reminds the flag owner
to bump this table.

The `NoAcceptanceGate` row is the sole entry in `ablationEnvExports`
today. Adding a Python-companion flag means adding a row to that map
AND to this table (see §7(f)).

## 7. Security & correctness gates (a)–(h)

### (a) No silent unknown flag, no ambient-env auto-activation, no label forgery

Three failure modes converge on the same "silent misattribution"
outcome; each has its own mitigation.

**(a.1) Unknown flag typo → exit 2.** `--ablation NoAccetpanceGate`
(typo) is silently accepted, the 8-hour ablation experiment runs
full-Loom, and D2 rows attribute to `NoAcceptanceGate` — reader draws
the wrong conclusion.
Mitigation: `AblationList.Set` returns
`ablation.ErrUnknownFlag`-wrapped errors on `Parse`;
`fs.ContinueOnError` + existing `if err := fs.Parse(args); err !=
nil { return 2 }` in main.go propagates to exit 2. `SetByName` in
`applyAblationFlagsTo` is defence-in-depth: even if a future refactor
bypasses `AblationList.Set`, `SetByName` enforces the same
`canonicalSet` check via the Phase 1 ablation-registry contract
(registry.go §SetByName). Both layers converge on exit 2 → the
ablation experiment never starts against a typo.

**(a.2) Ambient env auto-activation → CLI scrub before parse.** A
parent-process `LOOM_ABLATION_NOACCEPTANCEGATE=1` is enough on its
own for the Python acceptance-gate child to bypass — the value would
propagate transparently via `WhitelistEnv`'s `LOOM_*` prefix rule —
while `--ablation` was not passed and `baseline_or_ablation` stayed
`full_loom`. This produces an unlabeled ablation and misattributed
metrics.
Mitigation: `main()` calls `ScrubAmbientAblationEnv()` FIRST,
before any other work. Every env var listed as a value in
`ablationEnvExports` gets `os.Unsetenv`'d unconditionally. Only a
subsequent `--ablation NoAcceptanceGate` re-exports it, at which
point the label is `NoAcceptanceGate` and the D2 attribution is
correct. Test: plan.md §3 `TestScrubAmbientAblationEnv_ClearsParent`
sets the env var via `t.Setenv` before calling `main` with a
`--ablation`-less run and asserts the env var is empty after
`ScrubAmbientAblationEnv`.

**(a.3) Label forgery via `Opts` → derive, never accept.** A direct
`Run(ctx, Opts{BaselineOrAblation: "NoObserver"})` caller could
stamp the label `NoObserver` into a run row while `NoObserver`
was never actually flipped. This bypasses `SetByName` entirely.
Mitigation: `Opts` has no `BaselineOrAblation` field. The runner
takes `AblationFlags []ablation.FlagName` + `BaselineName string`
as inputs; the label is DERIVED inside `Run` via
`ComputeBaselineOrAblation(opts.AblationFlags, opts.BaselineName)`
after `ApplyAblationFlags(opts.AblationFlags)` has actually flipped
the flags. A caller cannot stamp a label without also flipping the
matching flags. Test: plan.md §3 `TestRunLabelDerivedFromApplied`
constructs an `Opts` with `AblationFlags: nil` and asserts the row's
`baseline_or_ablation` is exactly `"full_loom"` regardless of any
other Opts state.

**(a.4) Stale process state across `Run` calls → reset every canonical
flag at `ApplyAblationFlags` entry.** `ablation.Default` is
process-global; a test that calls
`Run(ctx, Opts{AblationFlags: [NoObserver]})` and then
`Run(ctx, Opts{})` (no ablation) would see `NoObserver` still true
in the second call because `SetByName` never sets it back to false
implicitly. This produces a row labelled `full_loom` while telemetry
is still suppressed → misattribution.
Mitigation: `applyAblationFlagsTo` Phase 2 resets EVERY
registered canonical flag to `false` (via `SetByName(..., false)` +
`Sync…` + `os.Unsetenv`) before Phase 3 applies the requested set.
`Run(ctx, Opts{AblationFlags: nil})` therefore leaves all bools
false and all Python-bridge env vars unset. Test:
`TestApplyAblationFlags_ResetsStaleState` calls
`ApplyAblationFlags([NoObserver])`, then
`ApplyAblationFlags(nil)`, then asserts
`ablation.Default.List()` still reports the flag registered AND
that each owner's `IsXxx()` accessor returns `false` AND that
`os.Getenv("LOOM_ABLATION_NOACCEPTANCEGATE") == ""`.

**Note:** the Phase 1 `ablation.Default.SetByName` API is the ONLY
mutation surface. This spec forbids reading `ablation.canonicalSet`,
importing `ablation` internal fields, or introspecting the *bool
targets — see §7(e).

### (b) Sorted-stable dedup of the `+`-joined string

Two invocations that pass the same 8 flags in different orders (say a
CI script builds the list from `sort -u` and a human types them in
workload order) must produce the SAME `baseline_or_ablation` string so
that D2 `GROUP BY` doesn't split the same experiment into two buckets.

**Mitigation:** `ComputeBaselineOrAblation` sorts the deduped flag
set ascending by string BEFORE joining. Tests
(`TestComputeBaselineOrAblation_OrderStable`) invoke it with 12
random permutations of a 4-flag set and assert all 12 return values
are byte-identical. Dedup runs BEFORE sort, so `NoObserver,NoObserver`
also produces `NoObserver` (not `NoObserver+NoObserver`) — plan.md §3
dedup test.

The alternative — insertion-order join — is REJECTED explicitly: it
would introduce a split-bucket hazard that no D2 sanity check catches
without a comparison stage.

### (c) `baseline_or_ablation` length ceiling + regex — enforced at both the CLI-seam derivation AND the `Run` derivation

Two enforcement sites; both call the same
`ValidateBaselineOrAblation(s string) error`:

1. `ComputeBaselineOrAblation(...)` calls it before returning.
   Since `Opts` has no `BaselineOrAblation` field (§7(a.3)),
   `ComputeBaselineOrAblation` is the ONLY source of the label; every
   caller — main.go or a direct `Run` caller — goes through it via
   `Run`'s internal invocation.
2. `Run(ctx, opts)` re-calls it against the derived label after
   `ComputeBaselineOrAblation` returns. This second call catches a
   future refactor that returns an ill-formed string from a new
   code path in `ComputeBaselineOrAblation` without adding validation
   inside that path — a belt-and-braces reject at the same seam
   where the row is stamped.

The validator itself:

- `MaxBaselineOrAblationLen = 200` (see §2.1 derivation). Rejects
  overlong strings.
- Ablation regex `^([A-Z][A-Za-z0-9]{2,31})(\+[A-Z][A-Za-z0-9]{2,31})*$`
  is a whitelist over composed strings — anything else fails the
  regex + returns exit 2. The regex composes ONLY from strings the
  registry has already vetted through `ErrUnknownFlag` (the CLI
  path); it exists as a belt-and-braces final check for the direct
  API path (loophole above).
- Baseline regex `^[a-z][a-z0-9_-]{2,63}$` matches
  `tests/eval/baselines/harness/row.go:ErrBaselineNameInvalid`
  verbatim → runner-generated rows (baseline branch, e.g.
  `full_loom`) and baseline-generated rows (harness) use the same
  character class for the same column. The union with the ablation
  regex covers all valid `baseline_or_ablation` values (§C5 D2
  guarantee).
- Empty string → `Run` substitutes `DefaultBaselineName` (`full_loom`)
  first — the "operator supplied `--baseline-name ""`" case is
  reduced to the default at the input boundary. Only if
  `ComputeBaselineOrAblation` still returns "" (a bug) does the
  validator's empty-string reject fire, catching the regression.

Failure mode averted: an unbounded string would let a malformed
composition (repeated flags at 32 chars each) escape into the DB and
cause an unindexed variable-length key to bloat D2 group-by scans;
the empty-string reject prevents a zero-value default from ever
persisting.

### (d) `--list-ablations` prints metadata only

Flag descriptions in `flagDescriptions` are compile-time string
constants sourced from the flag's own spec. They MUST NOT contain:

- path separators (`/`, `\`)
- config file names (`config.toml`, `dynamic_mcp.yaml`, `.codex/…`)
- env var names (`LOOM_*`, `AGENTSERVER_*`, `OPENAI_*`)
- URLs (`http://`, `https://`, `127.0.0.1:*`)
- capability-server-like paths (`skills/*`, `internal/*`, `tools/*`)
- credential-shaped strings (`token`, `secret`, `key`, `bearer`)

No exceptions. The `LOOM_ABLATION_NOACCEPTANCEGATE=1` env var name
lives in flags.go's `EnvNoAcceptanceGate` constant and in the §6
table, NOT in the `flagDescriptions` map — a reviewer wanting the
Python bridge details reads the §6 table or the README, not the CLI
output.

The two fields per line are enforced by a schema regex in plan.md §3
test `TestListAblations_LineFormat`:
`^(No[A-Z][A-Za-z0-9]{2,31})\t\[[A-D][0-9]\] [A-Za-z][A-Za-z0-9 ,.;()-]{4,119}\.$`

The character class in field 2 is intentionally restrictive: letters,
digits, spaces, and a small punctuation set. It excludes `/` `\`
`_` `@` `:` `=` `$` `<` `>` and every character used by paths, env
vars, URLs, and credentials. `TestListAblations_NoSensitiveContent`
double-checks with an explicit substring blacklist over the
categories above so a reviewer editing `flagDescriptions` gets an
actionable error message ("description 'foo LOOM_X=1 bar' contains
env-var-shape 'LOOM_X'") instead of a bare regex failure.

The `\[[A-D][0-9]\]` prefix is the §A/§B/§D section label.

### (e) `flags.go` binds through the narrow `ablation` API only

The `ablation` package's public export surface has the following
symbols the binder legitimately needs:

- `Default.SetByName(name string, v bool) error` — the mutation seam.
- `Default.List() []FlagName` — the read seam for `--list-ablations`
  (returns names only; NO current-value read is possible through the
  Phase 1 API).
- `KnownFlags() []FlagName` — read-only canonical list for tests /
  for the `--list-ablations` line-count invariant (§7(h)).
- `FlagName` (type) — required for typed API surfaces
  (`AblationList.Values() []ablation.FlagName`).
- `ErrUnknownFlag`, `ErrNotRegistered` — for `errors.Is` in wrapping.

`flags.go` MUST NOT reference:

- `ablation.canonicalFlags` / `ablation.canonicalSet` (unexported —
  wouldn't compile from another package anyway; guarded for future
  refactors that might export them).
- `ablation.NoObserver` / `ablation.NoAcceptanceGate` / any of the 8
  named constants directly. The Python-bridge map (§5.4) uses
  `ablation.FlagName("NoAcceptanceGate")` (string cast, not
  constant) so the source-of-truth for the flag name stays in
  `registry.go` — a rename there fails a plan.md §3 grep test that
  cross-checks every string-cast key against `KnownFlags()`. Every
  other use iterates `KnownFlags()`; this makes the binder
  shape-invariant to future flag additions and is the mechanism by
  which the `--list-ablations` line count stays in lockstep with the
  registry.
- Any owner-package private state (e.g.
  `capability.disableUploadAtomic`, `evalrun.initRegistrationErr`).
  The only owner-package touchpoint from `flags.go` is the exported
  `Sync…` calls listed in §6.

Rationale: the registry may swap its internal representation
(canonical set, target map layout, sort key) in a future refactor;
narrow-surface binding future-proofs the binder.

Test enforcement: plan.md §3 `TestFlagsGo_NarrowAblationSurface`
greps flags.go for any `ablation\.` reference not in the whitelist
above. A `ablation.NoObserver` (direct constant use) or an
`ablation.canonicalSet` reach-in fails the test. A
`ablation.FlagName("NoXxx")` string-cast passes the grep but is
double-checked by `TestPythonBridgeKeysAreCanonical` — the string
inside the cast MUST be a member of `KnownFlags()`, else the test
identifies which map row drifted.

### (f) README table maps each flag to predicted metric impact

Without a mapping table, `--ablation NoObserver` produces mysterious
"§D metrics went to zero" behaviour that a reviewer can't confidently
attribute. The §6 table adds a "Predicted metric impact" column and
the plan.md §3 `TestReadmeMetricMapping` asserts:

- Exactly 8 rows in the README table (matches `len(KnownFlags())`).
- Each row's FlagName column matches a canonical `ablation.FlagName`.
- Each row's "Predicted metric impact" cell is non-empty and non-`TBD`.
- The §12 section letter (`[A1]` / `[A3]` / `[B1]` / `[B3]` / `[B4]`
  / `[D1]` etc.) matches the flag's owning row in 12 号 §A/§B/§C/§D —
  cross-checked by grep against
  `/root/paper_writing/docs/intermediate/12_loom_development_tasks_for_v3.md`
  for the flag name.
- Each row's "Predicted metric impact" cell mentions at least one
  metric string listed in 12 号's `metrics` column for that same
  §A/§B/§C/§D row (e.g. `NoDryRun` [A3] must mention one of
  `PreExecutionFaultCatchRate` / `MissingArtifactDetectionRate` /
  `PolicyViolationPreventionRate`). A test that only asserts
  "non-empty and non-TBD" (round 3 P1-3) would pass an
  invented metric name; the grep against the paper's canonical
  metric list catches it.

New Phase 3 flag additions must edit both `flagDescriptions` and the
README table in the SAME PR; the CI grep fails otherwise.

### (g) Consumer-view sanity: D2 group-by demo

The spec includes a worked example that a D2 metric-extract engineer
can lift verbatim to build the ablation-comparison table without
guessing the group key:

```sql
-- SQL, run against observerstore/runs.db
SELECT baseline_or_ablation,
       COUNT(*)                                    AS runs,
       AVG(CASE WHEN success_oracle_result = 'pass' THEN 1.0 ELSE 0.0 END) AS task_success_rate,
       AVG(CASE WHEN failure_category = 'contract_field_missing' THEN 1 ELSE 0 END) AS contract_field_missing_rate
FROM runs
WHERE experiment_id = 'E1'
GROUP BY baseline_or_ablation
ORDER BY baseline_or_ablation;
```

```python
# Python, on the D2 metric-extract CSV
import pandas as pd
df = pd.read_csv("run.csv")
grouped = df.groupby("baseline_or_ablation").agg(
    runs=("run_id", "count"),
    task_success_rate=("probe_task_success_rate", lambda s: s.astype(bool).mean()),
    duration_ms_p50=("duration_ms", "median"),
    duration_ms_p95=("duration_ms", lambda s: s.quantile(0.95)),
).reset_index().sort_values("baseline_or_ablation")
print(grouped)
```

The sort-stable `+`-join (§4 + §7(b)) is what makes both queries safe
against invocation-order variation. This example lives in README.md §4
and is doubled by a plan.md §3 shape test that lints the SQL/Python
snippets exist and reference `baseline_or_ablation`.

### (h) CI 8-flag count + link-time bind assertion

Two failure modes to catch:

1. **Constant added, Register call not written.** A future
   Phase 2/3 worktree extends `registry.go:canonicalFlags` but
   forgets its `internal/xxx/xxx_ablation.go` init(). Detected by
   `TestListAblations_CountMatchesRegistry`:

   ```go
   want := len(ablation.KnownFlags())  // 8 today
   got  := strings.Count(ListAblationsText(), "\n")
   if got != want { t.Fatalf(...) }
   ```

   The test also asserts `len(ablation.Default.List()) == want` —
   catching the "constant added, Register call not written" case at
   test time even in a workflow that never runs `--list-ablations`.

2. **Register call written but owner package not linked into the
   runner binary.** The runner binary is the ONE artifact that must
   see every ablation flag; if `flags.go`'s blank imports drift out
   of sync with `KnownFlags()`, the flag registers in unit tests
   (which import the owner package directly) but silently 404s
   under the runner. Detected by
   `TestApplyAblationFlags_AllKnownFlagsBind`:

   ```go
   for _, fn := range ablation.KnownFlags() {
       t.Run(string(fn), func(t *testing.T) {
           err := ApplyAblationFlags([]ablation.FlagName{fn})
           if err != nil {
               t.Fatalf("flag %s: %v (owner package likely not linked; "+
                   "add a blank import to flags.go)", fn, err)
           }
       })
   }
   ```

   Runs INSIDE `tools/eval/runner/flags_test.go` so the test binary
   inherits the same import graph as the runner binary. This is the
   earliest signal for the "8 flags reach modules" invariant from
   todo_list.md line 107.

Third check (drift signal): plan.md §3 `TestFlagsGoBlankImports`
greps flags.go for `_ "github.com/yourorg/multi-agent/internal/…"`
lines and asserts each owning package listed in the §6 table has
one. Detects the "row added to §6, blank import not added" delta —
which would otherwise fail the runtime bind test above but with a
less informative error.

## 8. Non-goals

- The binder does not touch subprocess env exports beyond the
  `LOOM_ABLATION_NOACCEPTANCEGATE=1` k/v documented in §5.4. If a future
  Python-companion flag needs a different mechanism (e.g. a config
  file), that's a follow-up worktree — the current bridge is deliberately
  one env var per flag.
- The binder does not run any ablation experiment matrix — a wrapping
  bash script is expected to iterate over `--ablation` combinations and
  aggregate CSV rows.
- The binder does not know about baselines (E1/E2/E3): the baselines
  live under `tests/eval/baselines/` and emit their own
  `baseline_or_ablation` strings (see WT-2-baselines spec §5). This
  worktree's `baseline_or_ablation` covers Full-Loom-plus-ablation runs
  and the `full_loom` default only.
- No new ablation flag is introduced by this worktree.

## 9. Baseline HEAD

`origin/paper/v3-integration` at `1c41b29` (WT-2-driver-promotion-chain
merge). Prereq grep at spec time (§0 of the WT prompt): 7 direct
`ablation.Default.Register` call sites in `internal/` +
`internal/ablation/skill_flags.go:110`'s `mustRegister` wrapper for
`NoAcceptanceGate` = 8 canonical flags registered on `Default`.
Confirmed at spec time by `grep -rn "ablation.Default.Register"
multi-agent/internal/ multi-agent/tools/` filtered against `_test.go`.
