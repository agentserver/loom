# WT-1-contract-schema — Spec

> Source: `/root/paper_writing/docs/final/todo_list.md` Phase 1 table row
> **WT-1-contract-schema** (line ~67).
> Source: `/root/paper_writing/docs/intermediate/12_loom_development_tasks_for_v3.md` §A2.
> Branch: `paper/v3/p1-contract-schema`.
> Base: `origin/paper/v3-integration @ 17f2c3c` (`WT-1-ablation-registry` is now merged at this base; this spec depends on `internal/ablation.Default`).

---

## 1. Task boundary & file scope

This worktree (a) extends the existing `internal/contract` package with one
new top-level field (`RecoveryHint`), (b) replaces the pre-existing
`(TaskContract).Validate()` body with a stricter 7-lifecycle + 2-trace
enforcement that emits a `ContractCompleteness` event, (c) registers two
ablation flags (`NoTypedContracts`, `NoContractFormalization`) with the
shared `ablation.Default` registry, and (d) wires schema enforcement to the
first line of the `submit_contract_task` MCP tool entry handler.

Hard rules:

- File scope (touched by this worktree):
  - `multi-agent/internal/contract/types.go` — add `RecoveryHint string` field.
  - `multi-agent/internal/contract/validate.go` — replace the existing
    `Validate()` body with strict 7+2 enforcement, add the
    `ContractCompleteness` event emission, and add ablation-flag fast paths.
  - `multi-agent/internal/contract/ablation.go` — **new file**: define
    `DisableSchemaEnforce`, `DisableContractEntirely`, and an `init()` that
    registers both with `ablation.Default`.
  - `multi-agent/internal/contract/completeness.go` — **new file**: define
    the `ContractCompletenessEvent` type, the `EventSink` interface, and the
    package-level `RegisterCompletenessSink` / `currentSink()` plumbing.
  - `multi-agent/internal/contract/contract_test.go` — extend with the test
    matrix in §6 / plan §4.
  - `multi-agent/internal/contract/fuzz_test.go` — **new file**: `FuzzValidate`.
  - `multi-agent/internal/driver/contract_tools.go` — replace the existing
    `tc.ApplyDefaults(); tc.Validate()` pair on lines 49–52 with a single
    first-line `contract.EnforceContract(tc)` call (which defers to
    `Validate` internally) and pass-through. The handler MUST NOT have any
    statement, defer, or wrapper layer between function entry and that call.
  - `multi-agent/internal/driver/contract_tools_test.go` — **new file**:
    the §6 (a) entry-point bypass-resistance test.

- **Not touched** by this worktree:
  - `multi-agent/internal/contract/envelope.go` (already calls
    `ApplyDefaults()` + `Validate()` and benefits transitively).
  - `multi-agent/internal/contract/snapshot.go` (resource snapshot logic).
  - `multi-agent/internal/ablation/*` (was merged via WT-1-ablation-registry;
    we only consume `ablation.Default.Register`).
  - `multi-agent/internal/driver/capability_tools.go`, `tools.go`, and the
    larger driver suite — out of scope; their existing `ApplyDefaults +
    Validate` pairs continue to work because §2.3 keeps `Validate()` as the
    canonical entry point.
  - Observer relay / persistence (`internal/driver/observer_relay.go`):
    `ContractCompleteness` is delivered via the in-process `EventSink`
    interface, not over HTTP. Wiring the sink into the real observer is
    Phase D2 work; this worktree only delivers the interface plus an
    in-package nil-safe default.

- No additions to `multi-agent/go.mod`. The new code depends only on the
  standard library (`encoding/json`, `errors`, `fmt`, `log`, `regexp`,
  `strings`, `sync`, `unicode/utf8`) plus `internal/ablation`.

### 1.1 Intentional divergences from the todo_list row signature

- The todo row lists "7 lifecycle 字段 (intent / inputs/read_artifacts /
  outputs/write_targets / capability_requirements / execution_policy /
  success_oracle / recovery_hint)" but does NOT specify whether
  `success_oracle` is a top-level field or nested. §A2 of the 12-number doc
  is explicit: `success_oracle` stays nested inside `intent.success_criteria`
  (which is already `[]string` and serves as the success-oracle in v1);
  `recovery_hint` is the only new top-level field. This spec follows §A2.
- The todo row mentions "validator 顺手输出 `ContractCompleteness` percentile
  event". In v1 we emit a single per-contract event (`PresentFields` bitmap
  + `ratio`), not a percentile — percentile is a downstream eval-time
  aggregation, computed in §D2 from the stream of single-contract events.
  This spec keeps the on-package emission to one event per `Validate` call.
- The todo row's "ContractCompleteness 分母 = 7 lifecycle 字段（非 08 号
  原文的 8 字段计法）" governs this spec: `ratio = len(PresentFields) / 7`,
  fixed denominator; the 2 trace fields (`version`, `conversation_id`) are
  also required for `Validate` to return nil, but they are NOT counted in
  the completeness ratio (they exist for audit trace, not lifecycle
  coverage).

---

## 2. Public API

### 2.1 `RecoveryHint` field

Added to `TaskContract` in `types.go`:

```go
type TaskContract struct {
    Version                int                    `json:"version"`
    ConversationID         string                 `json:"conversation_id"`
    Intent                 IntentSpec             `json:"intent"`
    DataContract           DataContract           `json:"data_contract"`
    ExecutionPolicy        ExecutionPolicy        `json:"execution_policy"`
    CapabilityRequirements CapabilityRequirements `json:"capability_requirements"`
    RecoveryHint           string                 `json:"recovery_hint"`
}
```

Semantics:

- A free-form short string that names the key artifact paths a §D5 recovery
  evaluator must inspect, the idempotency assumption the contract makes
  about repeated invocation (e.g. "writes are append-only; resume is safe"),
  and the artifact-semantic notes the recovery agent needs (e.g. "the
  `refund-risk-report.md` is the only authoritative output").
- **NOT** an oracle input. The success oracle is `intent.success_criteria`;
  `recovery_hint` is consulted only when the oracle returns failure, to
  decide whether/how to retry. Conflating the two would let a sloppy
  recovery hint silently weaken the success check.
- Required (non-empty after `strings.TrimSpace`) when schema enforcement is
  active. Empty is rejected by `Validate` with `ErrMissingFields` listing
  `recovery_hint`.
- Length cap, control-char ban, HTML-prefix ban — see §2.4.

### 2.2 The 7 lifecycle + 2 trace required fields

`Validate` rejects a contract that is missing any of:

| # | JSON path                       | Counted in `PresentFields` bitmap | Test for "missing"                                 |
|---|---------------------------------|-----------------------------------|----------------------------------------------------|
| 1 | `intent.goal`                   | yes (lifecycle)                   | `strings.TrimSpace(tc.Intent.Goal) == ""`          |
| 2 | `intent.success_criteria`       | yes (lifecycle) — serves as the success_oracle per §A2; the cell label "lifecycle" refers to this row's role in `PresentFields`, not to the oracle classification | `len(tc.Intent.SuccessCriteria) == 0` OR every entry trims to empty |
| 3 | `data_contract.read_artifacts`  | yes (lifecycle) — see §2.2.1 for empty-vs-absent | `tc.DataContract.ReadArtifacts == nil` (nil slice — see §2.2.1)         |
| 4 | `data_contract.write_targets`   | yes (lifecycle)                   | `len(tc.DataContract.WriteTargets) == 0`           |
| 5 | `capability_requirements`       | yes (lifecycle)                   | `len(Skills) == 0 && len(Tools) == 0 && len(Resources) == 0` |
| 6 | `execution_policy`              | yes (lifecycle)                   | `Routing == "" && CodePersistence == "" && ExposeCodeToUser == "" && WriteMode == ""` — i.e. caller passed a zero-value `ExecutionPolicy` and never invoked `ApplyDefaults` |
| 7 | `recovery_hint`                 | yes (lifecycle) — new this worktree | `strings.TrimSpace(tc.RecoveryHint) == ""`        |
| T1 | `version`                      | NO (trace; required but excluded from ratio) | `tc.Version != contract.Version`                  |
| T2 | `conversation_id`              | NO (trace)                        | `strings.TrimSpace(tc.ConversationID) == ""`       |

Rationale for #3 (`read_artifacts` newly required): the prior `Validate`
allowed both nil and missing `read_artifacts`; §A2 lists "inputs /
read_artifacts" as a lifecycle field, so v1 enforces presence. See §2.2.1
for the exact nil-vs-empty rule (the §6 review caught a contradiction in
an earlier draft; the rule below is the precise one).

### 2.2.1 `ReadArtifacts` nil-vs-empty semantics

The required check is `tc.DataContract.ReadArtifacts == nil` (nil slice),
NOT `len(...) == 0`. Concretely:

- JSON `"read_artifacts": []` → decodes to a non-nil zero-length slice
  → **passes** (counts as "declared, no inputs").
- JSON missing the key entirely OR `"read_artifacts": null` → decodes
  to a nil slice → **fails** with `ErrMissingFields` listing
  `read_artifacts`.
- Go-literal `DataContract{WriteTargets: []WriteTarget{...}}` → nil
  slice → **fails**; any in-repo test or fixture that constructs a
  contract via Go literal MUST set `ReadArtifacts: []ArtifactRef{}` (or
  populate it) explicitly. Plan §3 step 7 enumerates the migrations.
- Go-literal `DataContract{ReadArtifacts: []ArtifactRef{}, ...}` →
  non-nil zero-length slice → **passes**.

Why the nil-vs-empty distinction rather than the simpler "len > 0":

- "len > 0" forces every contract to have at least one fake read
  artifact, which is silly for genuinely-input-less tasks like
  "generate hello world".
- "nil == missing" tracks the JSON semantic exactly: a contract author
  who forgets the key gets rejected; a contract author who declares
  `[]` is communicating "I have considered the inputs; there are none".

`PresentFields` bitmap counts this row as present iff the slice is
non-nil (i.e. matches the "passes" cases above), so a contract with
`"read_artifacts": []` reports 7/7 completeness — declaring there are
no inputs IS a complete declaration.

The §A2 reading of "inputs" maps to `data_contract.read_artifacts`.

Rationale for #6 (zero-value `ExecutionPolicy` detection): the existing
`ApplyDefaults` fills in every `ExecutionPolicy.*` enum so most callers
never trip this check. The check exists for the test path that constructs
a `TaskContract{}` literal and skips `ApplyDefaults`; the response is the
same as for any other missing field (`ErrMissingFields`). `ApplyDefaults`
must still be called **before** `Validate` whenever defaults are wanted —
this spec does NOT add an implicit defaulting pass inside `Validate` (would
change the semantics for downstream callers like
`internal/driver/capability_tools.go:181-182` that explicitly defaults then
validates).

### 2.3 `Validate` signature

```go
// Validate returns nil iff all 7 lifecycle and 2 trace fields are present
// and well-formed. On success, Validate emits exactly one
// ContractCompletenessEvent to the registered EventSink (no-op if nil).
//
// On failure, Validate returns a *ValidationError whose Missing slice
// names EVERY missing field (not just the first), in the §2.2 table
// order. No event is emitted on the failure path.
//
// Validate is pure: no network I/O, no filesystem access, no goroutine
// spawning beyond the synchronous sink call. The sink call must
// complete before Validate returns; sinks that buffer asynchronously
// own their own goroutine (see §2.6).
//
// Behaviour under ablation:
//   - DisableSchemaEnforce (NoTypedContracts) = true:  Validate skips
//     §2.2 required-field checks, but STILL runs the existing
//     validatePolicy / write-target enum checks (it would have run them
//     anyway via ApplyDefaults paths), and STILL emits the completeness
//     event with whatever fields happen to be present. Caller logs one
//     line "[ablation] NoTypedContracts: skipped enforce on
//     conversation=<id>" per call.
//   - DisableContractEntirely (NoContractFormalization) = true: Validate
//     itself is unaffected (this flag controls the entry tool, not the
//     validator — see §3). Validate still runs to completion if invoked.
func (tc TaskContract) Validate() error
```

Backward compatibility note: the existing `Validate()` is a value-receiver
method that returns `error`; this spec keeps that signature unchanged. The
return value upgrades from `fmt.Errorf` strings to a typed
`*ValidationError` that still satisfies `error`. Existing callers that
`strings.Contains(err.Error(), "intent.goal is required")` continue to
work because `(*ValidationError).Error()` includes the per-field strings;
see §2.5 for the exact format.

### 2.4 `RecoveryHint` content rules

Enforced by `Validate` (and by the future hot path on entry tool — same
function, both call sites). All three checks fire in the §2.5 error format.

(a) **Length cap.** `utf8.RuneCountInString(tc.RecoveryHint) > 4096`
    → `ErrRecoveryHintTooLong`. We count runes (not bytes) so that a
    legitimate multi-byte hint isn't rejected by a fast byte-len check that
    happens to land near the boundary. The 4096-rune cap is generous for
    operator notes (~4 dense paragraphs) and an order of magnitude smaller
    than typical user prompts, which keeps the observer DB cost bounded.

(b) **Control-character ban.** Any rune in `0x00..0x1F` or `0x7F`, with the
    explicit exceptions `\t` (0x09), `\n` (0x0A), and `\r` (0x0D), is
    rejected. → `ErrRecoveryHintContainsControlChar`.
    Reason: a `\x07` (BEL) in a hint logged to operator terminals beeps
    them; a `\x1b` (ESC) is the start of an ANSI escape that can rewrite
    the terminal scrollback. We allow common whitespace because hints are
    multi-line by design.

(c) **HTML-prefix ban.** A case-insensitive substring match against the
    rejection list below → `ErrRecoveryHintLooksLikeHTML`. Match is
    case-insensitive (using `strings.ToLower` once on the trimmed input,
    then `strings.Contains` per prefix — NOT regex, because a regex on
    attacker-controlled input is a DoS surface we don't need to open).
    The rejection list (exactly 8 entries, frozen at v1; additions
    require a follow-up spec change):

    1. `<script`
    2. `<iframe`
    3. `<object`
    4. `<embed`
    5. `<svg`
    6. `<img`
    7. `javascript:`
    8. `data:text/html`

    Reason: operators sometimes render hints in HTML diagnostic pages
    (driver UI, observer trace viewer). A hint of the form `<script
    src=evil/>` rendered raw becomes XSS. This is defense-in-depth — the
    renderer SHOULD escape, but this enforcement at the contract layer
    means the bad value never makes it to the renderer in the first place.
    A legitimate hint that needs to literally discuss XSS can use
    backtick-quoting; this is an acceptable false-positive trade.

### 2.5 `ValidationError` shape and ordering

```go
// ValidationError is the *typed* return from Validate on the rejection
// path. Use errors.Is against the sentinels below.
type ValidationError struct {
    // Missing names every required field that failed §2.2 / §2.4. Order
    // matches the §2.2 table (lifecycle 1..7 then trace T1..T2 then
    // RecoveryHint content checks at the end). Stable order so that
    // (a) test assertions don't flake on map-iteration order, and
    // (b) the operator-facing diagnostic is reproducible.
    Missing []string

    // Causes is a slice of the package-level sentinels that apply. At
    // least one of ErrMissingFields, ErrRecoveryHintTooLong,
    // ErrRecoveryHintContainsControlChar, or ErrRecoveryHintLooksLikeHTML
    // will be present. Other validation paths (policy enum, write-target
    // enum) keep their existing fmt.Errorf returns — they are NOT
    // re-routed through ValidationError, to keep diff scope small.
}

func (e *ValidationError) Error() string  // "task contract: missing [intent.goal, recovery_hint]"
func (e *ValidationError) Unwrap() []error // returns e.Causes (Go 1.20+ multi-unwrap)

// Sentinels (use errors.Is):
var (
    ErrMissingFields                    = errors.New("task contract: required fields missing")
    ErrRecoveryHintTooLong              = errors.New("task contract: recovery_hint exceeds 4096 runes")
    ErrRecoveryHintContainsControlChar  = errors.New("task contract: recovery_hint contains control character")
    ErrRecoveryHintLooksLikeHTML        = errors.New("task contract: recovery_hint contains HTML/script/javascript: prefix")
)
```

Important: `(*ValidationError).Error()` MUST include the literal substring
`"is required"` for each `Missing` entry that names a required field, so
that the legacy assertion in `internal/contract/contract_test.go:120,143,162`
(`strings.Contains(err.Error(), "intent.goal is required")` etc.) keeps
passing. Format: `"task contract: <field> is required"` per missing field,
joined with `"; "`. For recovery-hint content errors, the format is
`"task contract: <field> <constraint>"` (e.g.
`"task contract: recovery_hint exceeds 4096 runes"`).

### 2.6 `ContractCompletenessEvent` and the `EventSink`

```go
// ContractCompletenessEvent is the per-contract telemetry payload emitted
// once per successful Validate(). It contains ONLY the bitmap and the
// derived ratio — never the contract body. See §7 (d).
type ContractCompletenessEvent struct {
    ConversationID    string   `json:"conversation_id"`
    PresentFields     []string `json:"present_fields"`     // subset of the 7 lifecycle field names from §2.2 (rows 1..7)
    CompletenessRatio float64  `json:"completeness_ratio"` // len(PresentFields) / 7 ; range [0.0, 1.0]
}

// EventSink receives one ContractCompletenessEvent per Validate() call.
// Implementations MUST NOT block on network or disk I/O on the calling
// goroutine; the typical implementation is a channel send into an
// observer-owned goroutine.
type EventSink interface {
    EmitContractCompleteness(ContractCompletenessEvent)
}

// RegisterCompletenessSink atomically swaps the package-level sink.
// Pass nil to revert to the silent default. Returns the previous sink so
// tests can restore.
func RegisterCompletenessSink(sink EventSink) EventSink
```

Implementation rule: the package-level sink lives behind a `sync/atomic`
`atomic.Pointer[EventSink]` (or `atomic.Value` storing the interface), so
that concurrent `Validate` calls from many goroutines do not need a mutex
and a test that swaps the sink mid-run is race-free. `currentSink()` reads
the atomic; if nil it is a no-op. In production binaries before §D2 wires
a real sink, the sink IS nil and every `Validate` call emits to no-one;
this is intentional — the event surface is in place but inert until §D2.

Emission gate (precise rule — addresses §6 review P1 ambiguity):

- `Validate()` returns nil (no required-field rejection, no policy
  rejection, no recovery_hint content rejection) → emit one event.
- `Validate()` returns non-nil for ANY reason → emit zero events.
- The emission gate is therefore tied to "Validate returns nil", NOT
  to "required-field checks pass" — so under `DisableSchemaEnforce =
  true` with a missing field, `Validate` returns nil (the only check
  that would have rejected was skipped) and the event IS emitted, with
  `PresentFields` reflecting the truth (e.g. 3/7). Under
  `DisableSchemaEnforce = true` with an invalid `MaxDAGNodes = -1`,
  `Validate` returns the policy error (the policy check is NOT
  skipped — see §3.1) and the event is suppressed. This is consistent
  with "a contract that failed validation is not a meaningful data
  point for the completeness percentile" — the percentile is over
  contracts the system actually accepted.

Field ordering inside `PresentFields`: lifecycle field names in §2.2 table
order, so a downstream serializer can compute the bitmap deterministically.

`PresentFields` bitmap-counting rule (precise, addresses round-2 review):

- `ReadArtifacts` (slice-typed): present iff the slice is non-nil —
  length zero is fine. This matches §2.2.1's nil-vs-empty rule:
  declaring `[]` IS a declaration of "I considered the inputs; there
  are none". Inputs can legitimately be empty (e.g. "generate hello
  world" has no read artifacts).
- `Intent.SuccessCriteria` and `DataContract.WriteTargets` (slice-typed)
  are deliberate carve-outs: present iff the slice contains at least
  one trimmed-non-empty entry (for SuccessCriteria) / at least one
  entry (for WriteTargets). Both are productive fields: an empty
  success_criteria is a meaningless oracle, and an empty write_targets
  declares a task that produces nothing observable — neither is an "I
  considered there are none" statement in any useful sense.
  `collectMissing` correctly rejects both under schema-enforce
  (§2.2 #2, §2.2 #4); the bitmap mirrors that rejection so the
  completeness signal does not contradict the validity signal. The
  asymmetry vs ReadArtifacts is intentional and stems from the
  productive-vs-inventory distinction.
- `CapabilityRequirements` (struct-typed): present iff at least one
  sub-field is non-nil — the §2.2.1 nil-vs-empty rule applied across
  the three sub-fields (`Skills`, `Tools`, `Resources`). A contract
  that declares `Skills: []string{}` (or any one of the three as
  non-nil empty) is communicating "I considered capability
  requirements; there are none required" — a valid declaration for an
  opaque/generic task. A fully-zero struct (all three sub-fields nil)
  is what fails — that is the "operator forgot the field entirely"
  case.
- `ExecutionPolicy` (struct-typed) is present iff any of `Routing`,
  `CodePersistence`, `ExposeCodeToUser`, `WriteMode` is non-empty.
  After `ApplyDefaults` this is always true (the policy enums get
  filled). The check exists for the test path that constructs a
  zero-value policy and skips `ApplyDefaults`.
- A string field (`Intent.Goal`, `RecoveryHint`) counts as present iff
  `strings.TrimSpace(...) != ""`.

This rule is implemented by a single helper `presentFields(tc)` returning
the slice; the helper is unit-tested independently so a §2.6 bug doesn't
have to fail two tests to be caught.

### 2.7 Entry-tool guard: `EnforceContract`

```go
// EnforceContract is the entry-tool guard wrapper. It is the EXACT
// function the MCP submit_contract_task handler MUST call on its first
// non-trivial line, before ANY other work. It:
//
//   1. If DisableContractEntirely == true, returns
//      ErrContractFormalizationDisabled (the caller is then expected to
//      log the drop and fall back; see §3).
//   2. Calls tc.ApplyDefaults() so the policy enums are populated.
//   3. If DisableSchemaEnforce == true, logs the skip line and returns
//      nil (skipping the §2.2 required-field check).
//   4. Otherwise calls tc.Validate() and returns its error verbatim.
//
// The caller passes a *TaskContract because ApplyDefaults is a pointer
// receiver and must mutate in place to fill enum defaults. Validate is
// a value-receiver method, so the implementation calls (*tc).Validate()
// (with Go's implicit deref) after ApplyDefaults runs. The contract is
// mutated by ApplyDefaults; the caller sees the defaulted values after
// EnforceContract returns.
func EnforceContract(tc *TaskContract) error

var ErrContractFormalizationDisabled = errors.New(
    "task contract: formalization disabled by NoContractFormalization ablation")
```

This single function is the entire schema-enforce surface seen by the
entry tool — making it impossible to "validate without applying defaults"
or "apply defaults without validating" by accident. It is what the §7 (a)
test verifies the entry handler's first statement is.

---

## 3. Ablation flags

Two new exported package-level vars in `internal/contract/ablation.go`:

```go
// DisableSchemaEnforce gates §2.2 required-field enforcement. Wired to
// ablation.NoTypedContracts. Mutated by the CLI binder pre-run; reads on
// the hot path are unsynchronized intentionally (the ablation pattern is
// "set once before workload start, never flipped mid-run" — see the
// WT-1-ablation-registry spec §4).
var DisableSchemaEnforce bool

// DisableContractEntirely gates whether the entry tool parses the
// contract at all. Wired to ablation.NoContractFormalization.
var DisableContractEntirely bool

func init() {
    if err := ablation.Default.Register(
        ablation.NoTypedContracts, &DisableSchemaEnforce,
    ); err != nil {
        // Unreachable in production binary linking; surface loudly so a
        // misconfigured test setup fails immediately rather than silently
        // losing the registration.
        panic(fmt.Sprintf("contract: register NoTypedContracts: %v", err))
    }
    if err := ablation.Default.Register(
        ablation.NoContractFormalization, &DisableContractEntirely,
    ); err != nil {
        panic(fmt.Sprintf("contract: register NoContractFormalization: %v", err))
    }
}
```

Both flags are wired with `ablation.Default.Register`. The corresponding
sentinels (`ablation.NoTypedContracts`, `ablation.NoContractFormalization`)
already exist in the merged WT-1-ablation-registry code; see
`internal/ablation/registry.go:18,20`.

### 3.1 Behaviour under `NoTypedContracts`

When `DisableSchemaEnforce == true`:

- `(*TaskContract).Validate()` still parses the JSON (the entry tool's
  `json.Unmarshal(raw, &args)` runs unchanged), still runs `validatePolicy`
  (the existing routing/code-persistence/write-mode enum checks), still
  runs `WriteTarget.{Type,Kind,Name}` per-entry checks, but SKIPS:
  - Required-field check for each of the 7 lifecycle fields in §2.2.
  - Required-field check for `conversation_id` (T2 trace field).
  - `RecoveryHint` content checks (§2.4) — these are part of the
    "schema enforce" contract; an ablation that turns enforce off has to
    turn these off too, otherwise the ablation is partial and the
    experimental signal is muddied.
- `Version` (T1) is ALSO required even under this ablation, because
  unversioned envelopes break the decoder downstream and that's an
  unrelated bug class, not a schema-enforce concern.
- The completeness event STILL fires (with whatever subset of fields is
  present), so the observer can see "even with enforce off, the contract
  was 3/7 complete". This is consistent with §2.6's emission gate: under
  this ablation `Validate` returns nil even on partial input, so the
  gate fires. The event payload reflects the truth (3/7), not 7/7 — the
  bitmap is built from actual field presence, not from "would-have-
  enforced".
- Logged exactly once per call, to the standard `log` package:
  `[ablation] NoTypedContracts: skipped enforce on conversation=<id>`.
  Log MUST include the conversation ID so a postmortem can correlate.

### 3.2 Behaviour under `NoContractFormalization`

When `DisableContractEntirely == true`:

- The entry tool (`submit_contract_task` handler) sees
  `EnforceContract(&tc)` return `ErrContractFormalizationDisabled`.
- The handler MUST log exactly once at `log.Printf` level
  `[ablation] NoContractFormalization: dropped contract body on conversation=<id>`,
  then fall back to the natural-language delegation path. "Dropped contract
  body" wording is mandatory — the keyword "dropped" + "conversation=" is
  what the test in §6 greps for; vague phrasing like "skipping" would let
  a future refactor silently turn the log off.
- The fallback path:
  - Uses `args.Prompt` if non-empty; otherwise constructs
    `fmt.Sprintf("(contract formalization disabled) %s", tc.Intent.Goal)`
    so we never lose the operator intent entirely.
  - Skips the §2.7 `EncodeEnvelope` step entirely — the slave receives a
    bare prompt without the `<TASK_CONTRACT version=1>` markers.
  - Still calls `selectTarget` with `args.TargetDisplayName` /
    `args.Skill` — routing without a contract is allowed when the
    ablation is explicit.
  - Returns a response with `route: "natural_language_fallback"` so the
    eval harness can distinguish this from a normal routed delegation.
- The completeness event is NOT emitted in this path: the contract was
  never parsed past `json.Unmarshal`.

### 3.3 Mutual exclusion

The two flags are independent; both may be true. In that case,
`NoContractFormalization` wins (the entry tool returns the fallback path
before `EnforceContract` ever gets to the schema-enforce branch). This is
documented in the §6 test
`TestEnforceContract_BothAblations_ContractEntirelyWins`.

---

## 4. Entry-tool integration (`submit_contract_task`)

Existing code at `multi-agent/internal/driver/contract_tools.go:37-52`:

```go
func (s *submitContractTaskTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
    var args struct { Contract contract.TaskContract `json:"contract"` ... }
    if err := json.Unmarshal(raw, &args); err != nil { ... }
    tc := args.Contract
    tc.ApplyDefaults()
    if err := tc.Validate(); err != nil {
        return nil, &MCPToolError{Message: "invalid contract: " + err.Error(), ...}
    }
    // ... discover agents, build snapshot, ... → dispatch
}
```

Replacement structure (the minimal change consistent with §7 (a)):

```go
func (s *submitContractTaskTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
    var args struct { Contract contract.TaskContract `json:"contract"` ... }
    if err := json.Unmarshal(raw, &args); err != nil {
        // JSON parse failure precedes contract enforcement — the call has no
        // contract to enforce.
        return nil, &MCPToolError{Message: "invalid args: " + err.Error(), Category: observerstore.FailContractViolation}
    }
    tc := args.Contract
    // §7 (a) — the FIRST statement after unmarshal MUST be EnforceContract.
    // No discover, no snapshot, no observer write, no log line, no thread
    // bind: nothing between json.Unmarshal and EnforceContract.
    if err := contract.EnforceContract(&tc); err != nil {
        if errors.Is(err, contract.ErrContractFormalizationDisabled) {
            // §3.2 fallback path.
            return s.callNaturalLanguageFallback(ctx, tc, args)
        }
        return nil, &MCPToolError{Message: "invalid contract: " + err.Error(), Category: observerstore.FailContractViolation}
    }
    // ... existing discover/dispatch path unchanged from here.
}
```

The `args` struct is unchanged. The pre-existing `if err := json.Unmarshal(...)`
line is kept because malformed JSON is not a "contract violation" in the
schema-enforce sense — it's a protocol error. The §7 (a) test pins that
`EnforceContract` is invoked on the line immediately after the unmarshal-
error branch closes; see the test in §6 for the exact AST shape.

`callNaturalLanguageFallback` is a small private helper in
`contract_tools.go` that performs the §3.2 fallback. Signature:

```go
// fallbackArgs is the subset of the submit_contract_task argument
// struct that the natural-language fallback needs. Defined inline in
// contract_tools.go (not exported) so the public surface stays small.
type fallbackArgs struct {
    Prompt            string
    TargetDisplayName string
    Skill             string
    TimeoutSec        int
}

func (s *submitContractTaskTool) callNaturalLanguageFallback(
    ctx context.Context, tc contract.TaskContract, args fallbackArgs,
) (json.RawMessage, error)
```

Behaviour:

- Logs `[ablation] NoContractFormalization: dropped contract body on conversation=<id>` exactly once.
- Body selection: `args.Prompt` if non-empty; else
  `fmt.Sprintf("(contract formalization disabled) %s", tc.Intent.Goal)` if
  `tc.Intent.Goal` is non-empty; else return
  `&MCPToolError{Message: "no prompt and no intent.goal to delegate", Category: observerstore.FailContractViolation}`.
  The empty-and-empty case is a tool misuse and should fail loudly, not
  silently send an empty prompt to a slave.
- Calls `s.selectTarget(ctx, cards, tc, args.TargetDisplayName, args.Skill)`
  using `cards` from a fresh `DiscoverAgents` (the fallback path is the
  one place the spec accepts a DiscoverAgents call after no envelope
  enforce, because the call is what's being explicitly tested for under
  this ablation).
- Calls `DelegateTask` with the body (no envelope, no contract JSON in
  the prompt) and `SystemContext` populated the same way the normal
  path populates it (`agentbackend.BuildLoomOrigin(...)` when
  parent-link delegation is in play; empty string otherwise).
- Response JSON shape (kept compatible with the normal-path response so
  the eval harness can read both):

  ```json
  {
    "task_id":             "<from DelegateTask response>",
    "target_id":           "<...>",
    "target_display_name": "<...>",
    "skill":               "<...>",
    "route":               "natural_language_fallback",
    "warnings":            ["..."]
  }
  ```

  Notable differences from the normal-path response: `resource_snapshot`
  is omitted (the fallback path doesn't persist one, by §3.2 design — we're
  intentionally exercising the no-contract code path); `route` is the
  fixed string `"natural_language_fallback"`.

---

## 5. JSON wire compatibility

- The new `recovery_hint` field is required (omitempty NOT used) in the Go
  struct, so absent JSON keys decode to empty string and §2.2 #7 catches
  the missing case explicitly. The struct tag is `json:"recovery_hint"`
  with no `omitempty`, so a marshalled `TaskContract{}` round-trips with
  the key present (the test in §6 pins this — without it, a downstream
  serializer that round-trips through Marshal+Unmarshal would silently
  drop the field).
- Existing scenario JSON fixtures
  (`examples/dynamic-mcp/scenarios/*/request/contract.json` and
  `request/drafted_contract.json`) currently lack `recovery_hint`. They
  MUST be updated in this worktree as part of the fixture-migration
  delta (plan §3 step 7). The acceptance test
  `examples/dynamic-mcp/scenario_test.go:TestDriverClarifyContractScenarioFixtures`
  already calls `tc.Validate()`; without fixture migration the
  pre-existing test fails. The fixture-migration step in the plan
  enumerates the touched files and the literal `recovery_hint` strings
  to insert.
- Test helpers that build `TaskContract` literals (`testTaskContract`,
  `envelopedPrompt`, `routeContractPrompt`, `fanoutContractPrompt`,
  `TestContractFromPromptRejectsAllowMasterFalse`,
  `TestValidatePlanWithPolicyRejectsTooManyNodes` parameter struct, and
  the `tools_test.go` test bodies that construct a literal directly)
  MUST be updated to include both `RecoveryHint` and (where missing) a
  non-empty `ReadArtifacts`. Plan §3 enumerates the touched test bodies.

---

## 6. Test matrix (high-level — full table in plan §4)

The plan §4 expands each row into a complete test name. The spec only
needs to assert which behaviours are covered; the negative-test column
maps 1:1 to the §7 security items.

| Behaviour                                                                | Covers spec §        | Security item |
|--------------------------------------------------------------------------|----------------------|---------------|
| Full 7+2-field contract validates and emits completeness event 7/7       | 2.2, 2.6             | —             |
| Missing `recovery_hint` returns `ErrMissingFields` with `recovery_hint` listed | 2.2 #7         | (a)           |
| Multiple missing fields → `Missing` slice contains ALL of them, in §2.2 order | 2.5            | (a)           |
| `RecoveryHint` length 4097 runes rejected; 4096 accepted                 | 2.4 (a)              | (b)           |
| `RecoveryHint` containing `\x07` rejected; containing `\t\n\r` accepted  | 2.4 (b)              | (b)           |
| `RecoveryHint` containing each of the 8 HTML/script prefixes rejected (case-insensitive) | 2.4 (c)  | (b)           |
| Entry-tool handler — `EnforceContract` is the first call after the JSON-unmarshal-error branch closes (AST pin) | 2.7, 4 | (a) |
| Entry-tool handler — partial contract (only `intent.goal`) rejected by the handler before any `DiscoverAgents` call | 4 + 7 (a) | (a) |
| `DisableSchemaEnforce = true` → contract missing 4 fields passes `EnforceContract` | 3.1            | (c)           |
| `DisableSchemaEnforce = true` → invalid JSON STILL rejected at `json.Unmarshal` (ablation is field-level, not parse-level) | 3.1 | (c) |
| `DisableSchemaEnforce = true` → log line `[ablation] NoTypedContracts: skipped enforce on conversation=<id>` produced | 3.1 | (c) |
| `DisableContractEntirely = true` → entry-tool log line `[ablation] NoContractFormalization: dropped contract body on conversation=<id>` produced | 3.2 | (c) |
| `DisableContractEntirely = true` → response `route == "natural_language_fallback"` | 3.2 | (c) |
| `DisableSchemaEnforce = true && DisableContractEntirely = true` → `NoContractFormalization` wins; handler returns natural-language fallback, never logs the NoTypedContracts skip line | 3.3 | (c) |
| `ContractCompletenessEvent` payload contains ONLY bitmap + ratio + conversation_id — NEVER the contract body or any goal/criteria/hint strings | 2.6 | (d) |
| `CompletenessRatio` denominator is fixed at 7 — four assertions on a full 7/7 fixture: (i) `event.CompletenessRatio == 1.0` (catches a numerator off-by-one); (ii) `len(event.PresentFields) == 7` (catches a bitmap that silently drops or duplicates a field); (iii) `contract.completenessDenominator == 7` (a package-local exported-for-test constant; if a future commit changes the denominator, this assertion fails before any ratio math runs, so the developer sees "you changed the denominator, now go update the spec + the 7/7 fixture"); plus a separate 4/7 fixture asserting `event.CompletenessRatio` is within `1e-9` of `4.0/7.0` (catches a divisor change that happens to round 7/N to a different float-close-to-1.0). | 2.6 (Section 1.1) | (d) |
| `FuzzValidate` for 30s does not panic and does not deadlock the sink | 2.3 + 2.6 | (e) |
| `init()` registers both flags on `ablation.Default` — `ablation.Default.List()` after a blank import contains both names | 3 | integration |
| All existing tests (`internal/contract/...`, `internal/driver/...`, `internal/orchestrator/...`, `internal/dispatch/...`, `examples/dynamic-mcp/...`) pass with migrated fixtures | 5 | regression |

---

## 7. Security (mandatory)

The most damaging failure mode is a partial contract reaching the
dispatcher: that's the surface where capability_requirements and
allowed_targets are consulted to decide which slave runs the work.

### 7 (a) Schema enforce on the FIRST line of the entry tool

Background: today's entry tool runs `tc.ApplyDefaults() → tc.Validate()`
just three lines after `json.Unmarshal`. That's already early, but it's
also re-orderable in a careless refactor — if someone moves a "trivial"
`DiscoverAgents` call up for logging or instrumentation, the validation
no longer runs before that call.

Mitigation: the entry-tool handler's body MUST have the structure

```go
var args struct { ... }
if err := json.Unmarshal(raw, &args); err != nil { return nil, ... }
tc := args.Contract
if err := contract.EnforceContract(&tc); err != nil { return nil, ... }
// ... everything else
```

Two complementary tests pin this:

`TestContractToolsEntry_SchemaEnforceBeforeDispatch` (runtime / semantic):
- Constructs a partial `TaskContract` whose only set field is
  `intent.goal` and whose `capability_requirements.skills` is a single
  attacker-chosen high-trust skill (`"admin"`).
- Wires a fake `agentsdk` whose `discoverFunc` and `delegateFunc`
  fields (already the function-pointer hook pattern used by
  `fakeSDK` in `internal/driver/tools_test.go:181-192`) BOTH call
  `t.Fatal` on entry — if either is invoked, the test fails with
  the fatal message naming the offending side effect. A counter-based
  check could be defeated by a refactor that calls DiscoverAgents
  before EnforceContract for "harmless" instrumentation; t.Fatal can't.
  This reuses the existing `discoverFunc func() ([]agentsdk.AgentCard, error)`
  hook style rather than adding a new mock surface.
- Calls the handler.
- Asserts the handler returns a `*MCPToolError` from EnforceContract.
- Asserts no observer-relay side effect either (the fake observer
  similarly `t.Fatal`s its `SaveResourceSnapshot` and `SaveTaskContract`).

`TestSubmitContractTaskHandler_FirstCallIsEnforce` (static / lexical):
- Uses `go/parser` + `go/ast` to parse `contract_tools.go`, locate the
  `Call` method, walk its `BlockStmt`, and assert the first call
  expression after the `json.Unmarshal` error-return `IfStmt` is a call
  to `contract.EnforceContract`. Pure static check — costs ~5 ms.
- The static test is intentionally narrow (lexical, not semantic): it
  catches the future refactor that moves an "innocent" line above the
  guard even if the runtime test happens to miss it (e.g. because the
  hypothetical added line doesn't touch a fatal-on-entry mock).
- Robustness across Go versions: the test uses only `ast.Walk` and
  `*ast.CallExpr`/`*ast.SelectorExpr` — public API stable since Go 1.0.
  No vendoring of parse trees; the parsing is performed at test time
  against the file on disk.

Together: the runtime test is the strict-correctness pin (no dispatch
side effect ever runs); the static test is the refactor-resistance pin
(no innocent-looking line slips above the guard).

### 7 (b) `RecoveryHint` content rules

Background: `recovery_hint` is freeform operator text. It's also
something operators paste from arbitrary sources (an existing recovery
script, a wiki page, a chat thread). Without rules, it becomes a vector
for:
- Operator-terminal damage: a single `\x1b[2J` in a recovery hint
  clears the terminal scrollback when the hint is `echo`'d to logs.
- Cross-site scripting in the diagnostic UI: a hint of `<script
  src=evil/>` rendered in the driver-UI's "task detail" pane is XSS.
- Quiet PII leakage: a hint truncated to "first 4 KiB" by a downstream
  serializer might cut a string mid-secret; capping at 4096 runes
  upstream makes the boundary explicit.

Mitigation: §2.4 (a) length cap, (b) control-char ban (with `\t\n\r`
explicitly allowed), (c) 8-prefix HTML-substring ban. Tests in §6
exercise each rule and pin a specific positive case (length 4096, hint
with `\t\n\r`, hint that legitimately discusses "javascript:" in
backticks NOT rejected if the backticks are present — see the test
fixture).

Note: rule (c) uses `strings.ToLower` + `strings.Contains` per prefix,
NOT a regex. A regex over attacker-controlled input is a redoS surface
we don't need to introduce for an 8-needle check.

### 7 (c) Ablation flags MUST log every skip / drop

Background: silent ablation is the worst kind. An operator who runs
`./driver --ablation NoTypedContracts` and then a workload that ends in
a failed task: was the failure caused by the ablation (expected), by
the operator forgetting the ablation (a bug masquerading as a feature),
or by something else entirely? Without a per-call log line, the post-
mortem is a guess.

Mitigation: §3.1 mandates a log line per `EnforceContract` call when
`DisableSchemaEnforce` is true, with the conversation ID included.
§3.2 mandates a log line per `submit_contract_task` call when
`DisableContractEntirely` is true, with the literal substring "dropped
contract body" plus the conversation ID. Test
`TestNoTypedContracts_LogsSkip` and
`TestNoContractFormalization_FallsBackButLogsDrop` capture stderr via
`log.SetOutput(&buf)` and assert the substring is present.

### 7 (d) `ContractCompletenessEvent` MUST NOT contain the contract body

Background: the completeness event flows into the observer DB. The
observer DB is also the place that already stores the contract body (in
the `task_contracts` table, behind a separate audit path). Including
the body in the completeness event would (a) double-store the body
under a second access-control surface, (b) make the completeness event
much larger than necessary, and (c) allow a "telemetry-only" subscriber
to reconstruct the body when its access policy says it should only see
the bitmap.

Mitigation: §2.6 defines the event with exactly three fields. The test
`TestContractCompleteness_OnlyBitmap_NoBody` marshals the event to JSON
and asserts the JSON output does NOT contain the goal string, any of
the success_criteria strings, the recovery_hint string, or any
write_target name — using fixture strings that are intentionally
distinctive (UUIDs) so a false positive is essentially impossible.

### 7 (e) `Validate` is pure and must not panic under fuzz input

Background: `Validate` is the trust boundary at the entry tool. A panic
in `Validate` becomes a 5xx that takes down the driver process; an
attacker who can submit a contract can then DoS the driver by mutating
some sneaky JSON value (NaN floats, deep nesting, gigantic strings).

Mitigation: `Validate` is a pure function (no I/O, no goroutines, no
shared mutable state) and is fuzzed by `FuzzValidate`. The fuzz seed
corpus includes:
- An empty `TaskContract{}`.
- A fully-valid contract.
- A contract with extreme values: `RecoveryHint` at length 0, 4096,
  4097, 100000 (caught by length cap), filled with `\x00` (caught by
  control-char check), and the HTML prefix list.
- A contract with `MaxDAGNodes = -1`, `MaxConcurrency = 0`.
- A contract whose `Intent.Goal` is a 10 MiB string (we don't cap goal
  length but `Validate` must still return in bounded time).

The fuzz target `FuzzValidate` is invoked from `make` and CI with
`go test -fuzz=FuzzValidate -fuzztime=30s` — short, but enough to catch
the headline classes (panics, nil-deref, unbounded recursion). Any
crash file written under `testdata/fuzz/FuzzValidate/` is checked into
the repo as a regression seed.

---

## 8. Acceptance

The merge gate is:

1. `go test ./internal/contract/... ./internal/driver/... -count=1
   -shuffle=on -race` passes.
2. `go test ./internal/contract/... -fuzz=FuzzValidate -fuzztime=30s`
   completes without a crash.
3. `go vet ./...` is clean.
4. `gofmt -l internal/contract internal/driver` produces no output.
5. Every §6 test row is implemented and passing.
6. Every §7 security item has at least one negative test in §6.
7. Scenario fixtures
   (`examples/dynamic-mcp/scenarios/*/request/contract.json` and
   `drafted_contract.json`) decode-and-validate cleanly with the new
   `RecoveryHint` field populated (plan §3 step 7 enumerates the
   literal updates).

---

## 9. Out of scope (explicitly)

- Wiring the `EventSink` to the real observer relay over HTTP — that's
  §D2. This worktree only delivers the interface and an in-package nil
  default.
- Wiring the `--ablation NoTypedContracts` CLI flag to a binary —
  that's Phase 2 `WT-2-flag-integration`.
- Adding `RecoveryHint` content to the `draft_task_contract` MCP tool's
  default response — that's a UX polish for after the schema lands;
  the draft tool can return contracts with empty `recovery_hint` for
  now, and operators fill it in before submission. The §6 test for the
  draft tool's existing default output stays unchanged.
- Reformulating `success_criteria` into a structured success-oracle
  object — out of scope per §A2 ("`success_oracle` 仍嵌套在
  `intent.success_criteria`，不抽顶层"). A future worktree may take this
  on; this worktree treats `success_criteria []string` as the oracle.
