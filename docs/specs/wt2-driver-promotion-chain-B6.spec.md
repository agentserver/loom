# WT-2-driver-promotion-chain — Sub-B6: promotion-audit

**Chain position**: 1 of 4 (B6 → B2 → B4 → B1). Runs first because
downstream sub-tasks reuse the audit table shape and hash-write path this
sub-task introduces.

**Sources**:

- `/root/paper_writing/docs/final/todo_list.md` — Phase 2 table row
  **WT-2-driver-promotion-chain**, 合流注意事项 §1.
- `/root/paper_writing/docs/intermediate/12_loom_development_tasks_for_v3.md`
  §B B6 ("固化决策审计"), §D1 (D1 `dynamic_mcp_registry_hash` field).
- `/root/paper_writing/docs/intermediate/08_evaluation_plan_v3.md`
  line 269 — the placeholder column WT-1-run-schema reserved for this
  worktree.
- Reference: `internal/observerstore/capability_snapshots_writer.go`
  (writer shape) + `internal/evalrun/writer.go`
  (`dynamic_mcp_registry_hash` column, currently defaulted to `''`).

---

## 1. Goal

Every `register_slave_mcp` / `unregister_slave_mcp` / `mcp-userspace
install` invocation MUST carry **four required audit parameters**, MUST
be recorded in a new observer table `promotion_audit`, AND — as a
side-effect of registration — MUST compute and expose the current
`dynamic_mcp_registry_hash` for consumption by the D1 `runs` writer.

The four parameters make it possible to answer, from disk, WHO (user
slug), FROM WHICH driver thread, FOR WHAT REASON, and FROM WHICH
CANDIDATE TASK a given MCP came to live in the workspace. Without them
the paper's `PromotionAdoptionRate` /
`UserInitiatedSynthesisSuccessRate` /
`PromotionCandidateSurfacingRate` metrics cannot be joined on the
originating candidate signal that B1 will emit later.

## 2. Surface changes

### 2.1 `register_slave_mcp` tool

Located at
`multi-agent/internal/driver/register_mcp_tool.go`. The tool's
`InputSchema` gains four required fields:

| JSON key                    | Go type | Format                                             | Required |
|-----------------------------|---------|----------------------------------------------------|----------|
| `promoted_by_user_id`       | string  | `^[A-Za-z0-9_-]{8,128}$`                           | yes      |
| `driver_thread_id`          | string  | `^[A-Za-z0-9_-]{8,128}$`                           | yes      |
| `promotion_reason`          | string  | enum, see §2.4                                     | yes      |
| `candidate_source_task_id`  | string  | `^[A-Za-z0-9_-]{8,128}$`                           | yes      |

`InputSchema` moves the four keys into `required`; `additionalProperties`
stays `false`. The four regex patterns are the SAME shape used by
`internal/evalrun/schema.go` for `RunID` — see WT-1-run-schema §2.2
Security item (f). Reusing the shape means one grep-friendly regex
family across the eval plane, and the length bounds carve out enough
room for e.g. `codex-thread-<ulid>` while rejecting path traversals,
whitespace, and SQL meta-characters.

Rejection contract: if any of the four is missing OR the string does not
match the regex, the tool returns an `MCPToolError` with
`Category: observerstore.FailContractViolation` and message
`"<field-name>: does not match ^[A-Za-z0-9_-]{8,128}$"` (or `"is
required"` when missing). NO delegate task is opened, NO observer row is
written. This mirrors the existing `source_path is required` /
`invalid spec` guard on the same tool.

### 2.2 `unregister_slave_mcp` tool

Same four required fields, same regex, same rejection contract. Located
at `multi-agent/internal/driver/unregister_mcp_tool.go`.

The `promotion_reason` on unregister carries WHY the MCP is being
removed, drawn from the SAME enum in §2.4 (e.g. `explicit_user_request`
when the user asked, `driver_agent_inferred` when the driver decided a
promoted MCP is no longer being reused). B1's candidate-expiry pathway
is a separate concept and lives in `promote_candidates.decision =
'expired'`, NOT in `promotion_audit.promotion_reason` — the two tables
answer different questions.

### 2.3 `mcp-userspace install` CLI

Located at
`multi-agent/cmd/mcp-userspace/cmd_install.go`. Four new required flags:

- `--promoted-by-user-id <slug>`
- `--driver-thread-id <id>`
- `--promotion-reason <enum>`
- `--candidate-source-task-id <task_id>`

Same regex validation, run BEFORE the tarball is unpacked (so a
malformed audit input aborts install without side-effects). Missing flag
→ non-zero exit + `usage: ...` on stderr; malformed value → non-zero
exit + `error: <field-name>: does not match <regex>`.

When the install-record-server-side branch runs (existing
`--workspace <id>` branch), the audit row is written after the
`client.Install(...)` call succeeds. When the branch is skipped (no
`--workspace`), the audit row is still written locally against a
`workspace_id = ''` sentinel so the operator can reconstruct install
history for stub / offline runs.

### 2.4 `promotion_reason` enum

Exported as `internal/promotionaudit.Reason` (new package — see §3.1
for placement rationale). Exactly four accepted string values:

```go
const (
    ReasonExplicitUserRequest   Reason = "explicit_user_request"
    ReasonDriverAgentInferred   Reason = "driver_agent_inferred"
    ReasonBatchImport           Reason = "batch_import"
    ReasonCISeed                Reason = "ci_seed"
)
```

Rejection: `promotion_reason` MUST equal one of the four literal
strings. Anything else — including case-variants, trailing whitespace,
UTF-8 lookalikes, empty string — returns a typed error
`ErrInvalidPromotionReason`, wrapped through the driver-tool
`MCPToolError` path with `Category: FailContractViolation`.

Enum rationale: keeping it typed at the CALLER boundary (not just a
DB-side CHECK) means a driver that constructs an invalid reason string
is stopped BEFORE the delegate task, matching the existing behaviour of
`buildspec.Validate` on the same code path. The DB CHECK is a
backstop; see §4.

### 2.5 `dynamic_mcp_registry_hash` (D1 wiring)

Every successful register / unregister invocation MUST recompute the
current registry snapshot hash. The hash algorithm is deterministic and
platform-portable:

```
hash_input := for name in sorted(current_mcp_names):
                  name || '\x1F' || descriptor_sha256(name) || '\x1E'
hash       := hex(sha256(hash_input))
```

- `descriptor_sha256(name)` = sha256 of the canonical JSON of that MCP's
  `spec` (the same `buildspec.Spec` that `register_mcp_tool.go` already
  normalises via `buildspec.Normalize`). Canonical means marshalled with
  sorted keys.
- `\x1F` (US) and `\x1E` (RS) are ASCII field/record separators;
  any real MCP name matches `[a-z][a-z0-9_]{0,31}` per
  `buildspec.Validate`, so US/RS never collide with legitimate content.
- Empty registry → sha256 of the empty byte string, hex-encoded — NOT
  the empty string. A downstream `SELECT ... WHERE
  dynamic_mcp_registry_hash = ''` filter would otherwise conflate "no
  registry" with "column not written yet" (the WT-1-run-schema DEFAULT).

The hash lives in a new helper
`internal/driver/registryhash.Compute(names []string, descriptorHash
func(name string) string) (hex string)`. The eventual reader is the
eval runner (owned by WT-2-metric-extract / WT-2-e1e6-probes), which
reads via `driver.LastRegistryHash()` and passes the value verbatim
into `evalrun.Schema.DynamicMCPRegistryHash` — see §3.3 for the
publication API and §9 for the scope boundary against `internal/evalrun`.

The register/unregister tools call the helper AFTER the slave task
completes successfully; the resulting hash is written into the
`promotion_audit.registry_hash_after` column AND published on a package
variable `driver.LastRegistryHash string` guarded by `sync.RWMutex`
(the eval runner reads this at run-completion time and passes it to
`evalrun.Insert`).

## 3. Code shape

### 3.1 New package `internal/promotionaudit`

Files:

```
multi-agent/internal/promotionaudit/
    reason.go          // Reason enum + Parse + ErrInvalidPromotionReason
    reason_test.go
    fields.go          // AuditFields struct + Validate + regexps
    fields_test.go
    writer.go          // Writer interface + SQLiteWriter
    writer_test.go
```

Rationale for a new package: the four fields are consumed by BOTH the
driver tool (`internal/driver`) AND the CLI
(`cmd/mcp-userspace`). Placing them in a third package avoids importing
`internal/driver` from `cmd/mcp-userspace` (which today does not
depend on driver) and keeps the regex constants + enum in one place.

`AuditFields` shape:

```go
type Action string

const (
    ActionRegister   Action = "register"
    ActionUnregister Action = "unregister"
    ActionInstall    Action = "install"
)

type AuditFields struct {
    WorkspaceID           string    // may be "" for offline install; see §2.3
    MCPName               string    // required; matches ^[a-z][a-z0-9_]{0,31}$
    Action                Action    // one of the three constants
    PromotedByUserID      string    // ^[A-Za-z0-9_-]{8,128}$
    DriverThreadID        string    // ^[A-Za-z0-9_-]{8,128}$
    PromotionReason       Reason
    CandidateSourceTaskID string    // ^[A-Za-z0-9_-]{8,128}$
    RegistryHashAfter     string    // 64 hex chars (§2.5); "" when action == install-local-only
    Stage                 string    // "" for B6; reserved for B2 pipeline; max 64 chars, /^[a-z_]{1,64}$/
    StageResult           string    // "" for B6; reserved for B2; enum {"", "ok", "fail"}
    TS                    time.Time // caller-supplied; writer serialises RFC3339Nano UTC
}
```

`Validate(f AuditFields) error` runs the regex checks + enum check +
Action-vs-required-field invariant BEFORE any write. Error type is one
of the typed sentinels exported from the package (see §7 (b)).

**Important**: `Stage` / `StageResult` are declared IN this spec even
though B6 does not populate them, because B2 (next sub-task) will add
one row per pipeline stage. Reserving the columns now avoids a
DDL-migration commit in B2 that would collide with the register tool's
row shape. B6 writes `stage=''`, `stage_result=''`.

### 3.2 DDL append to `internal/observerstore/schema.sql`

Appended at the end of the file, after
`idx_capability_snapshot_usages_hash`. Uses `IF NOT EXISTS` so a re-run
of `schema.sql` on an existing DB is idempotent (matches the pattern
already established by WT-1-run-schema).

```sql
-- WT-2-driver-promotion-chain B6: per-register/unregister/install audit row.
-- Populated by internal/promotionaudit.SQLiteWriter via the register /
-- unregister driver tools and the mcp-userspace install CLI. Reserved
-- columns `stage` / `stage_result` are filled by sub-B2 (acceptance
-- pipeline) which reuses this table for per-stage rows.
CREATE TABLE IF NOT EXISTS promotion_audit (
    row_id                    TEXT PRIMARY KEY,
    ts                        TEXT NOT NULL,
    workspace_id              TEXT NOT NULL DEFAULT '',
    mcp_name                  TEXT NOT NULL,
    action                    TEXT NOT NULL CHECK(action IN ('register','unregister','install')),
    promoted_by_user_id       TEXT NOT NULL,
    driver_thread_id          TEXT NOT NULL,
    promotion_reason          TEXT NOT NULL CHECK(promotion_reason IN (
        'explicit_user_request','driver_agent_inferred','batch_import','ci_seed'
    )),
    candidate_source_task_id  TEXT NOT NULL,
    registry_hash_after       TEXT NOT NULL DEFAULT '',
    stage                     TEXT NOT NULL DEFAULT '',
    stage_result              TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_promotion_audit_mcp
    ON promotion_audit(mcp_name, ts);
CREATE INDEX IF NOT EXISTS idx_promotion_audit_user
    ON promotion_audit(promoted_by_user_id, ts);
CREATE INDEX IF NOT EXISTS idx_promotion_audit_thread
    ON promotion_audit(driver_thread_id, ts);
```

`row_id` is generated by `observerstore.PrefixedID("promaud")` — the
existing pattern — so downstream consumers can filter by prefix.

`workspace_id DEFAULT ''` matches §2.3 offline-install semantics; the
column is NOT nullable so `SELECT ... WHERE workspace_id = ''`
disambiguates offline from "workspace column added later" (which is not
a case that occurs, but the invariant is safer).

The two CHECK constraints belt-and-suspenders the enum invariants
enforced by `promotionaudit.Validate`. A caller who somehow bypasses the
Go validator and writes raw SQL still hits the DB CHECK.

### 3.3 Registry hash publication

`internal/driver/registryhash.go`:

```go
package driver

import (
    "crypto/sha256"
    "encoding/hex"
    "sort"
    "sync"
)

var (
    registryHashMu   sync.RWMutex
    lastRegistryHash string
)

// LastRegistryHash returns the last-written registry snapshot hash.
// Returns the sha256 of the empty byte string when no register / unregister
// has run this process. Never returns "".
func LastRegistryHash() string {
    registryHashMu.RLock()
    defer registryHashMu.RUnlock()
    if lastRegistryHash == "" {
        return hex.EncodeToString(sha256.New().Sum(nil))
    }
    return lastRegistryHash
}

// SetLastRegistryHash publishes h. Called from register_mcp_tool /
// unregister_mcp_tool after promotionaudit.Write succeeds.
func SetLastRegistryHash(h string) { ... }

// ComputeRegistryHash returns hex(sha256(sorted(name || US || descHash(name) || RS))).
func ComputeRegistryHash(names []string, descHash func(string) string) string { ... }
```

The eval runner reads via `driver.LastRegistryHash()` at run-completion
and passes it verbatim into `evalrun.Schema.DynamicMCPRegistryHash`.

Optional future work: attach a per-workspace map so multi-tenant eval
can filter — deferred beyond B6 because the current eval-runner runs one
workspace per process.

### 3.4 Wiring changes

- `register_mcp_tool.go` — after `waitDelegatedTask` returns success:
  1. Extract the freshly-registered MCP name + spec-hash from the two
     inputs we already have on hand: `args.Spec` (the driver-side
     `buildspec.Spec` — the same object the slave persists into
     `dynamic_mcp.yaml.spec_hash` via
     `internal/executor/dynamicmcp.go:DynamicEntry.SpecHash`).
  2. Merge into an in-process snapshot of the driver's view of the
     slave's registry — kept per-slave in
     `driver.slaveRegistryView map[string]map[string]string` (slave
     agent_id → mcp_name → spec_hash) with a `sync.RWMutex`. On a
     `register` the tool sets `view[slave][name] = specHash(args.Spec)`;
     on an `unregister` it deletes the entry. First call for a slave
     starts empty; the driver does NOT pre-populate by reading the
     slave's `dynamic_mcp.yaml` remotely (that file lives on the slave
     workdir, not the driver, and there is NO existing driver-side
     descriptor API — see §7 (j)).
  3. `hash := driver.ComputeRegistryHash(names, descHash)` where
     `names, descHash` are derived from the per-slave view sorted by
     `(slave_agent_id, mcp_name)` so hash is stable across concurrent
     multi-slave callers.
  4. `promotionaudit.SQLiteWriter.Write(...)` with `RegistryHashAfter =
     hash`, `Stage = ""`, `StageResult = ""`.
  5. `driver.SetLastRegistryHash(hash)`.
  6. If step 4 fails, LOG the failure via
     `t.logHelperErr("promotion_audit", "write", err)` — DO NOT roll
     back the successful register. Matches the existing "journal append
     failure degrades to log" pattern in `register_mcp_tool.go:70-88`.
     Rationale in §7 (a).
- `unregister_mcp_tool.go` — symmetric.
- `cmd_install.go` — as §2.3.

## 4. Consumer view: which metrics can be computed?

Assume you are the eventual `tools/eval/metrics/` scorer for the four
promotion-chain metrics. Given `promotion_audit` + `promote_candidates`
(B1) + `runs` (D1), can you compute each?

| Metric                                | Source table(s)                                    | Computable? |
|---------------------------------------|----------------------------------------------------|-------------|
| `PromotionAdoptionRate`               | `promote_candidates.decision` (B1) ÷ `promote_candidates` where `surfaced_at ≥ T` | ✅ (needs B1) |
| `UserInitiatedSynthesisSuccessRate`   | `promotion_audit.action='register' AND stage_result='ok'` (B2) ÷ `promotion_audit.stage='scaffold' AND stage_result='ok'` (B2) | ✅ (needs B2 stage rows) |
| `RegistryLookupHitRate`               | new counter published by B4                        | ✅ (needs B4) |
| `PromotionCandidateSurfacingRate`     | `promote_candidates.candidate_id` (B1) JOIN ground-truth-should-promote list | ✅ (needs B1) |
| `TimeFromUserDecisionToRegisteredMCP` | `promote_candidates.decision_at` (B1) → `promotion_audit.ts` (B6) for matching `candidate_source_task_id` | ✅ (needs B1 + B6) |

The join key across ALL five is `candidate_source_task_id`. Every B6
audit row MUST carry a `candidate_source_task_id` OR the ci_seed
sentinel (see §7(g) for the sentinel value). Without this join key the
downstream metrics cannot attribute a `register` back to a candidate
surfaced by B1.

## 5. Ablation flag composition truth table

Three flags interact with B6-owned code paths:

| `NoUserPromotionPath` | `NoAcceptanceGate` | `NoRegistryLookup` | Effect on B6 register call                                                                                     |
|-----------------------|--------------------|--------------------|----------------------------------------------------------------------------------------------------------------|
| off                   | off                | off                | Baseline: acceptance runs (B2 owns the gate), B6 audit row written, hash recomputed.                            |
| off                   | ON                 | off                | B2 gate bypassed. B6 register-tool row still written per §3.1 (stage='' / stage_result=''). Any B2-pipeline row that surrounds this call is B2's responsibility; B6 code emits ONLY the register-tool row and never fabricates stage / stage_result values. |
| off                   | off                | ON                 | B4 lookup skipped by driver; B6 register still runs; audit row unaffected.                                     |
| ON                    | any                | any                | Driver-initiated register-tool calls are refused by B1's ablation guard (see B1 spec). USER-initiated calls (via `mcp-userspace install` or explicit driver tool call with `promotion_reason='explicit_user_request'`) STILL go through the acceptance gate (B2). B6 audit still writes. Rationale in §7 (c). |

**Non-negotiable**: `NoUserPromotionPath` MUST NOT silently skip audit
writes. Ablation is a research knob; skipped audits corrupt the paper's
tables.

## 6. Test plan (mapped one-to-one to Security items in §7)

Each Security item MUST have a negative test that fails on removal of
the guard.

| Security item | Test                                                        |
|---------------|-------------------------------------------------------------|
| §7 (a)        | `TestRegister_MalformedUserID_Rejects` (missing/mismatch)   |
| §7 (a)        | `TestRegister_MalformedThreadID_Rejects`                    |
| §7 (a)        | `TestRegister_MalformedCandidateTaskID_Rejects`             |
| §7 (b)        | `TestAudit_EnumTypeRejectsBadReason` (case-variant + empty) |
| §7 (b)        | `TestAudit_EnumTypeRejectsUTF8Lookalike` (Cyrillic 'a')     |
| §7 (c)        | `TestSchema_ParameterizedSQL_NoInjection` (`'; DROP…` name) |
| §7 (d)        | `TestRegistryHash_EmptyIsSHA256OfEmpty_NotEmptyString`      |
| §7 (d)        | `TestRegistryHash_OrderIndependent`                         |
| §7 (d)        | `TestRegistryHash_ChangesOnDescriptorEdit`                  |
| §7 (e)        | `TestAudit_UserIDNotEmailShape` (rejects `x@y.z`)           |
| §7 (e)        | `TestDriverConstructor_NeverProducesAPIKeyShapedThreadID` (asserts the driver-side thread-id constructor, NOT the writer, never emits an `sk-` prefixed value; §7 (e) admits the regex alone cannot reject it) |
| §7 (j)        | `TestDriverRegistryView_UndercountsUntilFirstRegister` (documents the accepted per-slave undercount trade-off — asserts that on a fresh driver process, `LastRegistryHash()` reflects only in-process register/unregister history and does NOT preload from any remote slave file; encodes the trade-off so a future PR that adds remote-preload without updating §7 (j) trips the test) |
| §7 (f)        | `TestReason_FreeStringPathSecretscrubbed` (see §7(f))       |
| §7 (g)        | `TestConsumerView_JoinKeyPresent_ForEveryRow`               |
| §7 (h)        | `TestAudit_PerfBench_ConditionalOnShortOrCI`                |

## 7. Security

### (a) All four audit params required + regex-bounded

`^[A-Za-z0-9_-]{8,128}$` reused from WT-1-run-schema `RunID`. The
bounds serve three purposes:

- `{8,}` — a 1-char user_id like `x` is almost certainly a mistake and
  would leak into `idx_promotion_audit_user`, corrupting histograms.
- `{,128}` — bounds the PRIMARY-KEY-adjacent index size and caps
  DoS-via-huge-key.
- `[A-Za-z0-9_-]` — rejects path traversal (`/`, `..`), whitespace, SQL
  meta-characters (`'`, `;`, `--`), and the % / _ LIKE wildcards.

Enforced at BOTH the driver-tool layer (fast fail, no delegate task
opened) AND the writer layer (fast fail, no DB call). Double-checking is
cheap; the failure of either check is the invariant, not their sum.

### (b) `promotion_reason` enum, not free string

The typed `Reason` (§2.4) is the invariant at the Go level; the DB
CHECK is the invariant at the SQL level. Both layers reject unknowns.

A caller passing a free-string `reason` at the tool boundary is
rejected via `ErrInvalidPromotionReason`.

### (c) Parameterized SQL

Every `Writer.Write` uses `?` placeholders — no `fmt.Sprintf`, no
concat. The DDL uses `IF NOT EXISTS`. The `row_id` column is generated
by `PrefixedID(...)` (existing helper — cryptographically random) so a
caller cannot smuggle a row_id.

### (d) `dynamic_mcp_registry_hash` invariants

- Empty registry hashes to sha256 of the empty byte string, NOT the
  empty string. `LastRegistryHash()` never returns `""`.
- Sorted names in the hash input make it order-independent.
- Descriptor hash covers the descriptor content, so a spec edit
  (add/remove a tool, change a description) changes the hash even if
  the MCP name is unchanged. Two registrations with identical `spec`
  yield the same descriptor hash — content-addressed.

### (e) No user email / API key in audit fields

- `promoted_by_user_id` is a slug matching the tight regex — a raw
  email (contains `@`, `.`) is rejected by the character class.
  If the driver has an email available, it MUST hash it to a slug
  BEFORE passing.
- `driver_thread_id` is a slug matching the same regex. Note that
  the regex character class `[A-Za-z0-9_-]` does NOT reject an
  API-key-shaped string like `sk-abc123def456…`; secret material CAN
  syntactically match. This is a REGEX INSUFFICIENCY that the driver
  MUST paper over at the caller: driver tools construct
  `driver_thread_id` from the codex thread's public id (which is
  never a raw API key). The test
  `TestDriverConstructor_NeverProducesAPIKeyShapedThreadID` therefore
  does NOT assert that the writer rejects `sk-` (it can't, cheaply); it
  asserts that the driver-side constructor never PRODUCES a `sk-`
  prefixed thread id. If a caller manages to hand-craft an
  `sk-` prefixed id past the driver constructor, the value lands in
  `driver_thread_id` verbatim — that is a bug in the caller, not the
  writer, and grepping `promotion_audit` for `driver_thread_id LIKE
  'sk-%'` MUST return zero rows in every real audit.

Rejection order at the writer: length before content, so an
oversized value fails on the cheaper check.

### (f) `secretscrub` on `promotion_reason` free-string path

Although §2.4 pins `promotion_reason` to an enum, older callers may
attempt to pass free-form reason text (e.g. "explicit_user_request:
user typed 'fix bug in service X'"). The Parse function returns
`ErrInvalidPromotionReason` for such input. BUT the error message
returned to the driver tool must NOT echo the raw input, because the
raw input may contain a secret. Instead the error message is fixed:
`"promotion_reason must be one of: explicit_user_request,
driver_agent_inferred, batch_import, ci_seed"`. No user input is
interpolated.

### (g) Consumer-view completeness

`candidate_source_task_id` is REQUIRED on every audit row. When the
row is genuinely not attributable to a candidate — a `batch_import` /
`ci_seed` — the writer accepts the two sentinel values
`ci-seed-<workspace_id>` and `batch-import-<workspace_id>` (still
matching the regex, still stable per workspace). Downstream metric
scripts join on `candidate_source_task_id` and can filter out the
sentinels.

### (h) Perf assertions conditional on CI

Any perf test (e.g. "compute registry hash for 1000 MCPs in ≤ 5 ms")
guards the wall-clock threshold with
`if testing.Short() { t.Skip("short mode") }` at the top, so
`go test -short ./...` stays fast. The compute-path invocation itself
runs unconditionally (so `-race` still catches races on the hot path);
only the numeric `< 5 ms` assertion is under the Skip. CI runs `go test
./...` without `-short`, exercising the threshold.

### (j) Driver-side registry view — no remote read

The driver keeps its own view of what it has caused to be registered
per slave (§3.4 step 2). It does NOT reach into a slave's
`dynamic_mcp.yaml` remotely. Rationale:

- Slave-local file reads over the SDK would add a round-trip in the
  hash path (called on every register / unregister), and there is no
  existing driver-side API for it. See
  `internal/executor/dynamicmcp.go` — those helpers run on the SLAVE
  process only.
- If a slave already had MCPs registered by a prior driver process,
  the current process's view MAY undercount for this specific slave
  until a register / unregister runs. This is documented and
  acceptable for the paper's eval semantics: every eval run starts
  from a clean-state slave (per §C4 stub / §J.1) so no prior state
  exists to miss. In prod (§J.2 multi-device), the operator seeds
  audit history by driving one dry `list-slave-mcps → register with
  reason=batch_import` cycle as part of the deploy runbook — this is
  covered by the `batch_import` enum value in §2.4.
- The trade-off is between per-hash-call cost and stale-view accuracy;
  we choose in-process view. Future work: promote to observer-store
  driven view keyed on `promotion_audit`, but that would create a
  cyclic dependency (writer reads its own table before writing) —
  deferred.

### (i) `NoObserver` ablation compatibility

The `promotionaudit.SQLiteWriter` respects the same `NoObserver`
ablation flag that `internal/evalrun` respects: when
`observer.DisableTelemetry == true` the writer skips the DB write AND
emits `[ablation] NoObserver: dropped promotion_audit row_id=<id>
mcp_name=<name>` via `log.Printf`. Ablation MUST NOT be a silent gap in
the audit story.

## 8. Files touched

Created:

- `multi-agent/internal/promotionaudit/reason.go`
- `multi-agent/internal/promotionaudit/reason_test.go`
- `multi-agent/internal/promotionaudit/fields.go`
- `multi-agent/internal/promotionaudit/fields_test.go`
- `multi-agent/internal/promotionaudit/writer.go`
- `multi-agent/internal/promotionaudit/writer_test.go`
- `multi-agent/internal/driver/registryhash.go`
- `multi-agent/internal/driver/registryhash_test.go`
- `docs/specs/wt2-driver-promotion-chain-B6.spec.md` (this file)
- `docs/specs/wt2-driver-promotion-chain-B6.plan.md`

Modified:

- `multi-agent/internal/driver/register_mcp_tool.go` (4 new fields,
  audit + hash wiring)
- `multi-agent/internal/driver/register_mcp_tool_test.go`
- `multi-agent/internal/driver/unregister_mcp_tool.go`
- `multi-agent/internal/driver/unregister_mcp_tool_test.go`
- `multi-agent/cmd/mcp-userspace/cmd_install.go`
- `multi-agent/cmd/mcp-userspace/cmd_install_test.go` (may be new)
- `multi-agent/internal/observerstore/schema.sql`

## 9. What this spec explicitly does NOT do

- Does NOT populate `stage` / `stage_result` from the register tool.
  B2 (next sub-task) owns pipeline stages.
- Does NOT emit the `promote-candidate` event. B1 owns that.
- Does NOT register a new ablation flag. B6 respects the EXISTING
  `NoObserver`; B4 will add `NoRegistryLookup` and B1 will add /
  reuse `NoUserPromotionPath`.
- Does NOT change `internal/observerstore/postgres/schema.sql`. See
  WT-1-run-schema §3 for the parity policy.

**D1 hash wiring scope — clarified**: this sub-task DOES write the
`dynamic_mcp_registry_hash` for D1 to consume — that's the whole point of
§3.3 `driver.LastRegistryHash()` (todo_list Phase 2 line 112 explicitly
lists it as a B6 deliverable). What B6 does NOT include is a change
INSIDE `internal/evalrun` — the `evalrun.Schema.DynamicMCPRegistryHash`
field is already present (with a `''` default, per WT-1-run-schema).
The one-line change to have the eval runner CALL
`driver.LastRegistryHash()` when populating a new `evalrun.Schema` lives
in `tools/eval/runner/` and is owned by WT-2-metric-extract /
WT-2-e1e6-probes; this sub-task provides the read-side helper and
publishes the value on the driver-side global. A doc comment on
`evalrun.Schema.DynamicMCPRegistryHash` may be added in B6 pointing at
`driver.LastRegistryHash()` but no logic change to `internal/evalrun`.
