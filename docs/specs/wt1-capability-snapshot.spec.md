# WT-1-capability-snapshot — Spec

> Source: `/root/paper_writing/docs/final/todo_list.md` Phase 1 table row
> **WT-1-capability-snapshot** (depends on WT-0-windows-slave merged + WT-1-ablation-registry merged).
> Branch: `paper/v3/p1-capability-snapshot`.
> Base: `origin/paper/v3-integration` after `17f2c3c` (WT-1-ablation-registry).
> Downstream consumers: 13 号 §3.4 `CapabilityRecall` / `CapabilityPrecision`
> evaluator; D1 run schema `capability_snapshot_hash` column.
>
> **Stage**: this is the **Stage 1 design spec** of the three-stage workflow
> (Spec → Plan → Code). The Go files declared in §1 do NOT yet exist on disk
> — Stage 3 creates them. A reviewer evaluating this document is reviewing
> **design intent**: do the proposed fields, signatures, DDL, and security
> mitigations form a coherent and complete contract? Whether the
> implementation files are present yet is out of scope for Stage 1 review —
> that is what Stage 3's `git diff` review answers.

---

## 1. Task boundary & file scope

This worktree adds capability-snapshot collection and observer persistence,
plus an ablation flag that lets evaluators turn snapshot upload off without
blinding the slave's own self-defense logic.

Files this worktree owns (new or appended-to):

| Path | Action |
|---|---|
| `multi-agent/internal/capability/snapshot.go` | **NEW**. `Snapshot` struct, `ToolVersion`, `CredentialAlias`, `NetworkReach`, `ComputeHash`, `Hash()`, `DisableUpload`, `init()` that registers `ablation.NoCapabilityDiscovery`. |
| `multi-agent/internal/capability/snapshot_test.go` | **NEW**. Full §6 test matrix. |
| `multi-agent/internal/observerstore/schema.sql` | **APPEND ONLY**. Add `capability_snapshots` table + index at end of file. Do not touch any existing `CREATE TABLE` / `CREATE INDEX` statement. Postgres schema parity (see §1.2) is intentionally out of scope. |
| `multi-agent/internal/observerstore/capability_snapshots_writer.go` | **NEW**. `WriteSnapshot(ctx, db, agentID, wsID, snap)` parameterized insert; respects `DisableUpload`. |
| `multi-agent/internal/observerstore/capability_snapshots_writer_test.go` | **NEW**. Parameterized-statement + idempotency + ablation-skip + secret-strip tests against an in-memory `modernc.org/sqlite` DB. |

Files this worktree **must not** modify:

- `multi-agent/internal/capability/types.go` — `MCPToolDescriptor`,
  `FlatNames`, `WithServer`, `ExtractFromAgentCard`, `FindTool`. Untouched.
  (`Snapshot` embeds / references `MCPToolDescriptor`; it does not redefine
  it.)
- `multi-agent/internal/contract/types.go` — the WT-0-windows-slave `Platform` /
  `CommandInterfaces` fields on `contract.Capabilities` (verified present at
  lines 95–96 on this base). The new `Snapshot.Platform` /
  `Snapshot.CommandInterfaces` fields copy from
  `commandiface.Platform` / `[]commandiface.CommandInterface` — they reuse
  the existing types, not redefine them.
- `multi-agent/internal/commandiface/` — read-only dependency.
- `multi-agent/internal/ablation/` — read-only dependency. (We call
  `ablation.Default.Register(ablation.NoCapabilityDiscovery, &DisableUpload)`
  from `package init()`; that is the entire integration surface.)
- Any other table in `schema.sql`. Append-only.

`multi-agent/go.mod` is not modified: every new dependency
(`crypto/sha256`, `encoding/hex`, `encoding/json`, `regexp`, `sort`, `log`,
`context`, `database/sql`) is either standard library or already a direct
dependency of `observerstore` (`modernc.org/sqlite`).

### 1.1 Why `capability/snapshot.go` instead of extending `capability/types.go`

`capability/types.go` is a stable read-only descriptor file
(MCPToolDescriptor + helpers). The new snapshot machinery is large enough
(struct + 5 helper types + ComputeHash + init-time ablation wiring +
secret-strip logic) that putting it in its own file keeps `types.go`
focused. This is consistent with the sibling `schema_validate.go` /
`schema_validate_test.go` split already in the package.

### 1.2 SQLite only; Postgres parity deferred

Two scopes to separate explicitly:

- **Storage scope (narrow):** this worktree writes to the SQLite observer
  schema only. `multi-agent/internal/observerstore/postgres/schema.sql`
  also exists, but the §F4 eval runner (D3) plus all current Phase 1 /
  Phase 2 evaluator worktrees consume the SQLite observer (the on-host
  eval-runner default; see `tools/eval/runner/`). Adding the same DDL to
  the Postgres schema is a one-line follow-up that any future
  production-observer worktree can make when Postgres becomes the primary
  backend.
- **Semantic-completeness scope (broad):** the `Snapshot` field set + the
  evaluator translation rules in §3.2 are normative and complete for the
  five §3.4 capability kinds, regardless of which backend stores the
  bytes. Storage backend is a swap; the field schema and translation
  rules are the contract.

`WriteSnapshot` is written against `database/sql` with `?` placeholders
only — when the Postgres parity row is added, the same function works
against `pgx.Stdlib` if it ever needs to.

### 1.3 Concrete imported Go-type anchors

The new `Snapshot` struct (see §3) embeds or references types already
declared in this worktree's read-only dependencies. Exact import anchors:

| Symbol used by Snapshot | Declared at | Underlying type |
|---|---|---|
| `commandiface.Platform` | `multi-agent/internal/commandiface/interfaces.go:3` | `struct{ OS, Arch string }` |
| `commandiface.CommandInterface` | `multi-agent/internal/commandiface/interfaces.go:8` | `struct{ Skill, Kind, Command string; Default bool }` |
| `capability.MCPToolDescriptor` | `multi-agent/internal/capability/types.go:5` | `struct{ Server, Name, Description string; InputSchema json.RawMessage; ResultDescription string }` |
| `ablation.FlagName` | `multi-agent/internal/ablation/registry.go:14` | typed `string` |
| `ablation.NoCapabilityDiscovery` | `multi-agent/internal/ablation/registry.go:17` | `FlagName("NoCapabilityDiscovery")` |
| `ablation.Default.Register` | `multi-agent/internal/ablation/registry.go:74` | `func(FlagName, *bool) error` |

When the implementation files land, these anchors are how a reviewer
double-checks that no shadow definition was introduced.

### 1.5 Intentional divergence from todo_list "migrations/" wording

The todo_list row sketches the DDL file as
`multi-agent/internal/observerstore/migrations/*_capability_snapshots.sql`.
This spec deliberately puts the DDL in `schema.sql` instead because the
existing observerstore SQLite path has **no migration runner** — it
embeds `schema.sql` via `//go:embed` and exec's the whole file inside
`OpenSQLite` (see `multi-agent/internal/observerstore/store.go:60`). Every
existing table is declared with `CREATE TABLE IF NOT EXISTS`; introducing
a `migrations/` directory now would require a runner, a migration table,
ordering rules, and a new tested code path — all out of scope for this
worktree. The append-only `schema.sql` change is the smallest viable
delivery that matches the existing observerstore idiom. When/if the
project adopts a migration runner (a separate worktree's job), the
`capability_snapshots` DDL is one trivial `git mv` of those two
statements into a new migration file.

### 1.4 Why a new `capability_snapshots_writer.go` in observerstore

The existing observer write path is dominated by event / task / artifact
domain types that share helpers in `store.go`. Snapshot writes are a small,
self-contained insert and need only `database/sql` plus the `capability`
package — keeping them in their own file avoids dragging `capability` as a
new transitive import for unrelated observerstore code reviewers.

---

## 2. Background — what the snapshot is for

### 2.1 Downstream metric contract

`13_workload_spec.md` §3.4 defines 5 `kind` values inside
`context_ground_truth.required_capabilities`:

```text
tool | platform | file | network | credential
```

The §F4 `CapabilityRecall` metric is defined as the count of
`required_capabilities ∩ snapshot` divided by `len(required_capabilities)`.
`CapabilityPrecision` further penalizes the snapshot for matching any item in
`forbidden_capabilities`.

For that to be computable, the snapshot this worktree emits MUST carry
fields that 1:1 cover every one of the 5 kinds. The table in §3 below
encodes that 1:1 mapping; any kind without a snapshot-side counterpart
breaks the metric and is a P0 spec defect.

### 2.2 D1 run schema integration

WT-1-run-schema (separate worktree, also Phase 1) declares a column
`capability_snapshot_hash TEXT` on the `runs` table. The eval runner reads
that column off the slave's last snapshot via `Snapshot.Hash()` (defined
here). The hash function signature is therefore an **inter-worktree API**:
once we ship, the run-schema worktree expects `Hash() string` on
`capability.Snapshot` with the behaviour in §4.

### 2.3 Self-defense vs ablation experiment

The slave uses its own snapshot to decide whether a tool exists ("do I have
`pytest`? otherwise refuse the task with a clean error"). That self-check
must continue working even under `NoCapabilityDiscovery`, because killing
self-defense at the same time as ablating upload would conflate two
independent variables in the experiment.

Therefore `NoCapabilityDiscovery` is narrowly scoped to "do not write a
snapshot row to the observer database". Local collection still runs,
`Snapshot` values are still produced, and `ComputeHash` is still called —
only `WriteSnapshot` short-circuits. See Security (d) below.

---

## 3. Snapshot field set (1:1 with the 5 ground-truth kinds)

```text
type Snapshot struct {
    OS                string                          // ← kind=platform.os
    Arch              string                          // ← kind=platform.arch
    Platform          commandiface.Platform           // reuse — same {OS, Arch}, kept for callers that already hold the typed struct
    CommandInterfaces []commandiface.CommandInterface // ← kind=tool (shell drivers: bash / powershell / wsl …)
    Tools             []ToolVersion                   // ← kind=tool.name + tool.min_version
    MCPTools          []capability.MCPToolDescriptor  // ← kind=tool (MCP-flavoured tools; descriptor.Name = tool.name)
    Files             []FileResource                  // ← kind=file
    Network           NetworkReach                    // ← kind=network.reach
    Credentials       []CredentialAlias               // ← kind=credential.alias
}
```

Per-kind coverage:

| §3.4 `kind` | Field(s) | Notes |
|---|---|---|
| `tool` | `Tools[]`, `CommandInterfaces[]`, `MCPTools[]` | Three sources because tools surface from three different discovery paths (PATH binary scan, shell-driver detection, MCP `tools/list` RPC). All three feed the same `tool` kind. `ToolVersion.Name` ↔ `name`; `ToolVersion.Version` is matched against `min_version` by the evaluator (lex-compare on dotted-numeric prefix; details belong to the evaluator, not this snapshot — we just emit the raw version string). |
| `platform` | `OS`, `Arch` | Snapshot of `runtime.GOOS` / `runtime.GOARCH` via `commandiface.Platform`. Required for the 13号§3.4 platform enum values `{linux, windows, darwin}` × `{amd64, arm64}`. |
| `file` | `Files[]` of `FileResource{KindDetail, PathPattern}` | `KindDetail ∈ {"repo","dataset","fixture","config"}`, `PathPattern` is a literal path or glob the snapshot collector observed. Validation of enum membership lives in the snapshot factory (`AppendFile`) — see §5. |
| `network` | `Network NetworkReach` | Enum `{"internet","intranet","loopback-only","none"}`. Single-valued: a host has one effective outbound reach at snapshot time. |
| `credential` | `Credentials[]` `CredentialAlias` | Alias-only; raw tokens are rejected at construction time. See Security (a). |

### 3.1 Supporting types

```go
// ToolVersion is one entry in Snapshot.Tools. Version is free-form
// (semver / commit-hash / "unknown") so the snapshot collector can record
// whatever the upstream `--version` flag returned without coercion.
type ToolVersion struct {
    Name    string
    Version string
}

// CredentialAlias is an opaque placeholder identifying a credential the
// agent can broker. Constructed only via NewCredentialAlias, which rejects
// any value that looks like a raw token (see Security (a)).
type CredentialAlias string

// NetworkReach is the effective outbound network reach the host has at
// snapshot time. Single-valued: every host has exactly one of these.
type NetworkReach string

const (
    NetworkInternet     NetworkReach = "internet"
    NetworkIntranet     NetworkReach = "intranet"
    NetworkLoopbackOnly NetworkReach = "loopback-only"
    NetworkNone         NetworkReach = "none"
)

// FileResource is one entry in Snapshot.Files.
// KindDetail must be one of the 13号§3.4 enum: repo|dataset|fixture|config.
type FileResource struct {
    KindDetail  string
    PathPattern string
}

// NewCredentialAlias is the ONLY exported constructor for CredentialAlias.
// Returns ErrLooksLikeRawCredential when s matches a raw-token shape (see
// Security (a)); returns ErrAliasInvalidShape when s does not match the
// §3.4 regex `^[a-z][a-z0-9_]{2,63}$`. On success the returned value can
// be assigned to Snapshot.Credentials. There is no equivalent for
// CredentialAlias literals; tests use this constructor.
func NewCredentialAlias(s string) (CredentialAlias, error)

// NewSnapshot is the construction-time validator. It enforces:
//   - OS != "" and Arch != "" and Network is one of the four NetworkReach
//     constants;
//   - OS == Platform.OS and Arch == Platform.Arch (no flat/typed drift);
//   - every Tools[i].Name != "";
//   - every Files[i].KindDetail in {"repo","dataset","fixture","config"}.
// Returns ErrSnapshotInvalid wrapping the specific field that failed.
func NewSnapshot(spec Snapshot) (Snapshot, error)
```

Sentinel errors exposed by `capability`:

```go
var (
    // ErrLooksLikeRawCredential is returned by NewCredentialAlias when the
    // input matches any of the raw-token regexes in §7(a).
    ErrLooksLikeRawCredential = errors.New("capability: value looks like a raw credential token")

    // ErrAliasInvalidShape is returned by NewCredentialAlias when the
    // input does not match the §3.4 alias regex.
    ErrAliasInvalidShape = errors.New("capability: credential alias does not match ^[a-z][a-z0-9_]{2,63}$")

    // ErrSnapshotInvalid is returned by NewSnapshot when a construction
    // invariant fails. Tests use errors.Is to assert this; details are
    // appended via fmt.Errorf("%w: <field>", ErrSnapshotInvalid).
    ErrSnapshotInvalid = errors.New("capability: snapshot invalid")
)
```

### 3.2 Evaluator translation contract (Snapshot → §3.4 capability union)

This worktree owns the snapshot **producer**; the §F4 evaluator is the
**consumer** in a separate worktree. To pin the contract so the evaluator
has zero ambiguity, every `Snapshot` field translates into the §3.4
discriminated-union form by the following normative rules. The evaluator
worktree is required to follow these rules; this worktree exposes the
field set that makes the rules total.

```text
For each tv in Snapshot.Tools:
    emit { "kind": "tool", "name": tv.Name, "min_version": tv.Version }   # min_version omitted when tv.Version == ""

For each ci in Snapshot.CommandInterfaces:
    emit { "kind": "tool", "name": ci.Skill }                              # bash / powershell counts as a tool kind by §3.4

For each mt in Snapshot.MCPTools:
    emit { "kind": "tool", "name": mt.Name }                               # MCP tool name in the same `tool` namespace

emit { "kind": "platform", "os": Snapshot.OS, "arch": Snapshot.Arch }     # exactly one platform record

For each f in Snapshot.Files:
    emit { "kind": "file", "kind_detail": f.KindDetail, "path_pattern": f.PathPattern }

emit { "kind": "network", "reach": string(Snapshot.Network) }              # exactly one network record

For each alias in Snapshot.Credentials:
    emit { "kind": "credential", "alias": string(alias) }                  # 1:1 with §3.4 credential union branch
```

Result: every one of the five §3.4 `kind` branches has a producing field
on `Snapshot`. `CapabilityRecall` and `CapabilityPrecision` set algebra
is then plain set intersection / set difference on the emitted records;
**no evaluator-side inference is needed** to recover the credential or
any other kind from the snapshot.

Matching semantics the evaluator MUST follow (pinned here so producer +
consumer agree):

- `tool.min_version` is matched by lex-compare on the dotted-numeric
  prefix of `tv.Version` (i.e. `"1.22.0" >= "1.18"` is true). Free-form
  suffixes are ignored after the first non-`[0-9.]` byte.
- `tool.name` matching is case-sensitive exact.
- `platform.os` / `platform.arch` matching is exact string equality
  against the §3.4 enum.
- `file.kind_detail` matching is exact against the §3.4 enum
  (`repo|dataset|fixture|config`); `file.path_pattern` matching follows
  `filepath.Match` glob semantics.
- `network.reach` is exact string equality against the §3.4 enum
  (`internet|intranet|loopback-only|none`).
- `credential.alias` is case-sensitive exact equality against the §3.4
  alias regex.

Unsupported / empty fields:

- A `Snapshot` whose `Network == ""` is rejected by the snapshot factory
  (`NewSnapshot`) — every snapshot has exactly one network reach.
- A `Snapshot` whose `OS == ""` or `Arch == ""` is rejected by the
  snapshot factory.
- An empty `Tools` / `CommandInterfaces` / `MCPTools` / `Files` /
  `Credentials` slice is allowed (a host with no MCP tools is legal); the
  emitter simply yields zero records for that source.
- A `ToolVersion.Name == ""` is rejected by the snapshot factory; the
  emitter never sees a nameless tool.

These are **hard-fail at construction time**, not silent drops, so the
evaluator never has to choose between two interpretations.

### 3.3 Why `Platform` AND `OS`/`Arch` both appear on `Snapshot`

`commandiface.Platform` is the typed struct already returned by
`commandiface.Detect`; callers holding a `Capabilities` value want to assign
it directly. The flat `OS` / `Arch` fields are convenience accessors for
metric extraction code that does not want to pull in `commandiface` for one
struct field read. **Invariant:** `s.OS == s.Platform.OS` and
`s.Arch == s.Platform.Arch`. The snapshot factory enforces this; tests in
§6 verify it.

---

## 4. `ComputeHash` & `Snapshot.Hash`

```go
// ComputeHash returns the SHA-256 hex digest of a deterministic JSON
// encoding of s. The encoding is canonicalised before hashing:
//
//   - Every []string / []ToolVersion / []FileResource /
//     []CredentialAlias / []MCPToolDescriptor / []CommandInterface inside
//     s is sorted by its natural key (Tool: by Name then Version;
//     CommandInterface: by Skill then Kind then Command; MCPTool: by
//     Server then Name; File: by KindDetail then PathPattern;
//     CredentialAlias: lexicographic).
//   - JSON marshalling uses encoding/json with the default field order
//     (which is the Go struct declaration order — fixed at compile time).
//   - Map iteration is never used.
//
// The hash input MUST include OS and every tool's Version. A snapshot in
// which go was downgraded from 1.22 → 1.18 produces a different hash
// even when all other fields are equal — see Security (b).
//
// Stability contract: ComputeHash(s) == ComputeHash(s) bytewise across
// consecutive calls in the same process; ComputeHash(s1) == ComputeHash(s2)
// whenever s1 and s2 are field-wise equal modulo slice ordering of the
// fields listed above.
func ComputeHash(s Snapshot) string

// Hash is the method form. Equivalent to ComputeHash(s). Provided so
// downstream consumers that hold a *Snapshot can call s.Hash() without
// importing the function name (e.g. WT-1-run-schema's D1 writer).
func (s Snapshot) Hash() string
```

### 4.1 Canonical encoding mechanics

We use a private helper `canonical(s Snapshot) Snapshot` that returns a
copy of `s` with every relevant slice sorted in place. Then we call
`json.Marshal(canonical(s))`, take SHA-256 of the resulting bytes, and hex
the digest. We do not use `json.Encoder` with `SetEscapeHTML(false)` —
default encoding is sufficient and matches what callers see if they
serialise the snapshot for debugging.

`encoding/json` field order for a Go struct is defined by struct
declaration order; that is stable across builds. We do not need a custom
`MarshalJSON` for the top-level struct.

For `CommandInterface`, `Default bool` is included in the sort key
implicitly (it is part of the marshalled JSON). Two interfaces with the
same `Skill`/`Kind`/`Command` but different `Default` would change the hash
— this is correct: which interface is default is a meaningful capability
fact.

### 4.2 Why JSON-then-SHA-256 and not a custom canonical encoder

A custom canonical encoder is more space-efficient but has a much larger
attack surface for "hash unchanged after a real downgrade" bugs. We take
the bytes the existing JSON encoder produces, after sorting the slices we
own. This means any future field added to `Snapshot` is automatically
hashed without anyone having to remember to update the canonicaliser —
omission of that update is the most common rollback-attack failure mode.

---

## 5. `WriteSnapshot` & DDL

### 5.1 DDL (appended to `multi-agent/internal/observerstore/schema.sql`)

Two tables, split so dedup by hash is independent from per-agent
attribution (see §5.1.1 for the design rationale):

```sql
-- Content-addressed capability snapshot dedup store. One row per
-- unique canonical-JSON snapshot; multiple agents that observe the
-- same host state share this row.
CREATE TABLE IF NOT EXISTS capability_snapshots (
  hash          TEXT PRIMARY KEY,
  snapshot_json TEXT NOT NULL,
  first_seen_at TEXT NOT NULL
);

-- Insert-always attribution log: every observation of a snapshot by
-- an (agent, workspace) pair lands here, so the eval runner can
-- reconstruct "which agents saw which capability set at which time"
-- even when 100 agents share the same hash.
CREATE TABLE IF NOT EXISTS capability_snapshot_usages (
  workspace_id TEXT NOT NULL,
  agent_id     TEXT NOT NULL,
  hash         TEXT NOT NULL,
  used_at      TEXT NOT NULL,
  PRIMARY KEY (workspace_id, agent_id, hash, used_at)
);
CREATE INDEX IF NOT EXISTS idx_capability_snapshot_usages_agent
  ON capability_snapshot_usages(workspace_id, agent_id, used_at);
CREATE INDEX IF NOT EXISTS idx_capability_snapshot_usages_hash
  ON capability_snapshot_usages(hash);
```

- `hash` is the `Snapshot.Hash()` SHA-256 hex digest.
- `snapshot_json` stores the **same canonical JSON** that was hashed —
  byte-equivalent under SHA-256 — so post-hoc auditors can recompute the
  hash and the §F4 `CapabilityRecall` evaluator reads snapshot fields
  directly.
- `first_seen_at` is set on the initial insert of a given hash; subsequent
  `ON CONFLICT(hash) DO NOTHING` inserts leave it untouched (it is a
  provenance timestamp of first observation, NOT of the latest use).
- `capability_snapshot_usages.used_at` records when THIS agent observed
  the snapshot; the PRIMARY KEY includes `used_at` so an agent
  re-observing the same snapshot at a different instant is a new row.
- All timestamps are RFC3339Nano (UTC).
- DDL uses `CREATE TABLE IF NOT EXISTS` / `CREATE INDEX IF NOT EXISTS`
  so every `OpenSQLite` call (the existing `db.Exec(schemaSQL)` path) is
  idempotent for existing installations.

#### 5.1.1 Why two tables

The Phase-1 fresh-reviewer audit (round 5) caught that a single
`capability_snapshots(hash PK, agent_id, ...)` shape with
`ON CONFLICT(hash) DO NOTHING` **silently drops every attribution row
after the first**: agent-A observes hash H and lands a row; agent-B
observes hash H seconds later and their INSERT no-ops; a query
`WHERE agent_id='agent-B'` returns zero rows. That violates §F4
evaluator contract (which needs per-agent capability usage).

Splitting into dedup (`capability_snapshots`) + attribution
(`capability_snapshot_usages`) fixes it without duplicating the JSON
blob: the JSON is stored exactly once per unique host state; usages
grow linearly with observations. The two-table shape also lets the
evaluator do `SELECT snapshot_json FROM capability_snapshots s JOIN
capability_snapshot_usages u ON s.hash = u.hash WHERE u.agent_id = ?`
in one query.

### 5.2 Writer signature

```go
package observerstore

func WriteSnapshot(
    ctx context.Context,
    db *sql.DB,
    agentID string,
    workspaceID string,
    snap capability.Snapshot,
) error
```

Behaviour (see §7.2 for the security-motivated ordering):

1. Canonicalise `snap` (`capability.CanonicalJSON(snap)`); return the
   wrapped error if it fails (only reachable for hand-built snapshots
   that bypassed `NewSnapshot`).
2. Compute `hash` = SHA-256 hex of the canonical bytes.
3. Reject the canonical bytes if they contain an embedded raw-token
   pattern (`ErrSnapshotContainsSecret`). Runs BEFORE the ablation
   short-circuit so leaked descriptors surface regardless of upload
   state.
4. If `capability.IsUploadDisabled()` returns true: log
   `[ablation] NoCapabilityDiscovery: skipped snapshot hash=<hex>` and
   return `nil`. Do **not** touch the DB.
5. Otherwise, open a `db.BeginTx(ctx, nil)` transaction and:
   ```sql
   INSERT INTO capability_snapshots
     (hash, snapshot_json, first_seen_at)
   VALUES (?, ?, ?) ON CONFLICT(hash) DO NOTHING;

   INSERT INTO capability_snapshot_usages
     (workspace_id, agent_id, hash, used_at)
   VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING;
   ```
   Both use `?` placeholders. Commit atomically. Return any error
   verbatim (no wrapping) so callers can `errors.Is(err, sql.ErrConnDone)`.

The `first_seen_at` and `used_at` values are both `nowUTC().Format(time.RFC3339Nano)`
captured once per call — a single snapshot upload thus has identical
timestamps in the two tables when it is the first observation. Tests
override `var nowUTC` for determinism; overrides MUST be serial (no
`t.Parallel()`) and restore via `t.Cleanup`, since the global has no
internal lock. `TestWriteSnapshot_PersistedCreatedAt` exercises the
override path so the test contract is wired even if no other test
currently needs it.

Reading `IsUploadDisabled()` (not the `DisableUpload` package variable
directly) is REQUIRED — the accessor's `atomic.Bool` load is what makes
concurrent reads race-free (see §7(d)).

### 5.3 Sentinel errors

```go
// ErrSnapshotContainsSecret is returned by WriteSnapshot when the
// canonical JSON of the snapshot — typically the description field of an
// MCP tool descriptor — contains a substring matching a known raw-token
// shape. See Security (e). Callers should treat this as a programmer
// error (the descriptor source needs to be fixed) and surface it
// loudly, not retry.
var ErrSnapshotContainsSecret = errors.New("observerstore: snapshot contains embedded secret")
```

`CredentialAlias` violations are caught at construction time and surface
as `capability.ErrLooksLikeRawCredential` from `NewCredentialAlias`.
`WriteSnapshot` does not separately rescan the `Credentials` slice — by
construction it cannot contain a raw token.

---

## 6. Test matrix

All listed tests live in either
`multi-agent/internal/capability/snapshot_test.go` or
`multi-agent/internal/observerstore/capability_snapshots_writer_test.go`.
The "Security" column maps each test to the Security spec item it covers.

| Test | Verifies | Security |
|---|---|---|
| `TestCredentialAlias_ValidShape` | `abc_def`, `s3_prod` accepted; the `[a-z][a-z0-9_]{2,63}` regex matches. | — |
| `TestCredentialAlias_RejectsRawSk` | `NewCredentialAlias("sk-abc123def")` returns `ErrLooksLikeRawCredential`. | (a) |
| `TestCredentialAlias_RejectsRawEyJ` | `eyJhbGciOi…` JWT-shape rejected. | (a) |
| `TestCredentialAlias_RejectsRawAKIA` | AWS access-key shape `AKIAABCDEFGHIJKLMNOP` rejected. | (a) |
| `TestCredentialAlias_RejectsRawGhp` | `ghp_…` GitHub PAT rejected. | (a) |
| `TestCredentialAlias_RejectsRawXox` | All five Slack prefixes `xoxb-`, `xoxa-`, `xoxp-`, `xoxr-`, `xoxs-` rejected. | (a) |
| `TestCredentialAlias_RejectsUppercase` | `MY_KEY` rejected (not matching `^[a-z]`). | — |
| `TestComputeHash_Stable` | Same Snapshot value hashed twice is byte-equal. | — |
| `TestComputeHash_DiffersOnOSDowngrade` | Two snapshots identical except `OS = "linux"` vs `OS = "darwin"` differ; flipping `Platform.OS` alone (with mismatched flat field) is rejected by the factory (`NewSnapshot`). | (b) |
| `TestComputeHash_DiffersOnToolVersionDowngrade` | `Tools = [{go, 1.22}]` vs `[{go, 1.18}]` differ. | (b) |
| `TestComputeHash_DiffersOnMCPDescriptorChange` | Adding an MCPTool changes the hash. | (b) |
| `TestComputeHash_IndependentOfSliceOrder` | Permuting `Tools`, `MCPTools`, `Credentials`, `Files`, `CommandInterfaces` yields the same hash. | determinism |
| `TestSnapshot_Hash_EqualsComputeHash` | `s.Hash() == ComputeHash(s)`. | API contract |
| `TestNewSnapshot_RejectsInconsistentPlatformAndOS` | Mismatched `OS` vs `Platform.OS` is rejected. | invariant |
| `TestNewSnapshot_RejectsUnknownNetworkReach` | `Network = "wan"` rejected. | enum |
| `TestNewSnapshot_RejectsUnknownFileKindDetail` | `KindDetail = "screenshot"` rejected. | enum |
| `TestWriteSnapshot_Parameterized_SQLMetaInAgentID` | `agent_id = "a'); DROP TABLE capability_snapshots; --"` inserts successfully, table still exists, row count is 1, retrieved `agent_id` is the literal injection string. | (c) |
| `TestWriteSnapshot_IdempotentOnDuplicateHash` | Insert with the same hash twice does not error and only one row is present. | DDL idempotency |
| `TestWriteSnapshot_DifferentAgentsSameHashOneRow` | Two agents with byte-equal snapshots collapse to one `hash` row (PRIMARY KEY) but the runner can still query both agents' usages via the index (we verify the index exists in `sqlite_master`). | dedup contract |
| `TestWriteSnapshot_RejectsEmbeddedSecretInMCPDescription` | `MCPTools[0].Description = "use this with sk-abc123def…"` causes WriteSnapshot to return `ErrSnapshotContainsSecret` and no row is inserted. | (e) |
| `TestWriteSnapshot_RejectsEmbeddedSecretInToolName` | A `ToolVersion{Name: "ghp_ABCDEFGHIJKLMNOPQRSTUV"}` likewise triggers `ErrSnapshotContainsSecret`. | (e) |
| `TestNoCapabilityDiscovery_SkipsUploadButReturnsNil` | With `DisableUpload = true`, `WriteSnapshot` does not insert and returns `nil`. | (d) |
| `TestNoCapabilityDiscovery_LogsSkipLine` | Same conditions: a log line containing `"NoCapabilityDiscovery: skipped"` and the hash is emitted via the standard `log` package (test captures `log.Default().SetOutput`). | (d) |
| `TestNoCapabilityDiscovery_DefaultIsFalse` | Package-level `DisableUpload` starts as `false`. | (d) |
| `TestRegisteredOnAblationDefault` | After importing `capability`, `ablation.Default.List()` contains `ablation.NoCapabilityDiscovery`. | integration |
| `TestThreeSlavesProduceDistinctHashes` | Synthetic snapshots for `linux-laptop` (small toolset), `linux-server` (large toolset, no powershell), `windows-desktop` (powershell, different network reach) all produce distinct hashes; each individually is stable. | acceptance |

### 6.1 Test commands

```bash
go test ./internal/capability/... ./internal/observerstore/... -count=1 -shuffle=on -race
go vet ./...
gofmt -l internal/capability internal/observerstore
```

`-count=1` defeats the test cache, `-shuffle=on` forces order
independence, `-race` catches an accidental data race on
`capability.DisableUpload` if a test ever forgets to restore it.

The `_test.go` file uses `t.Cleanup` to restore `DisableUpload = false`
after every test that flips it — tests are otherwise non-parallel for the
ones that mutate the package global (we document this in the test file
header). All hash / regex / construction tests run with `t.Parallel()`.

### 6.2 Three-slave acceptance scenarios

The `TestThreeSlavesProduceDistinctHashes` test builds these synthetic
snapshots inline (no real OS / network probing in unit tests — that
belongs to the slave-side integration test in a later worktree):

| Scenario | OS | Arch | Tools | CommandInterfaces | Network |
|---|---|---|---|---|---|
| `linux-laptop` | linux | amd64 | `go@1.22.0`, `git@2.40.0`, `node@20.10.0` | `bash` default | internet |
| `linux-server` | linux | amd64 | `go@1.22.0`, `git@2.40.0`, `python@3.12.0`, `docker@24.0.0`, `kubectl@1.29.0` | `bash` default | intranet |
| `windows-desktop` | windows | amd64 | `go@1.22.0`, `git@2.40.0` | `powershell` default + `bash` via WSL | internet |

The test asserts three distinct `Hash()` values and re-computes each one
twice to confirm intra-scenario stability.

---

## 7. Security (P0)

Every item below has at least one named test in §6. A `VERDICT: CLEAN`
review must verify that mapping.

### (a) `CredentialAlias` factory rejects raw-token shapes

`NewCredentialAlias(s string) (CredentialAlias, error)` is the only way to
construct a `CredentialAlias`. The factory:

1. Rejects strings not matching `^[a-z][a-z0-9_]{2,63}$` (mirrors the
   13号§3.4 regex). Returns `ErrAliasInvalidShape`.
2. **Additionally**, before the shape check, rejects strings whose case-
   insensitive content matches any of these raw-token prefixes (the
   Phase-1 leaked-token catalogue; not exhaustive — see §7.1 below):

   | Pattern | Source |
   |---|---|
   | `(?i)sk-[A-Z0-9_-]{10,}` | OpenAI / Anthropic-shaped API keys (catches `sk-ant-api03-…` too) |
   | `(?i)eyJ[A-Z0-9_-]{10,}\.[A-Z0-9_-]{10,}(\.[A-Z0-9_-]{10,})?` | JWT / OIDC tokens (2+ base64 segments; single-segment `eyJfoo.` would false-positive on innocuous `identifier.name` phrasings, so we require the payload segment too) |
   | `(?i)AKIA[0-9A-Z]{16,}` | AWS access key ID |
   | `(?i)ghp_[A-Z0-9]{20,}` | GitHub classic PAT |
   | `(?i)github_pat_[A-Z0-9_]{20,}` | GitHub fine-grained PAT |
   | `(?i)AIza[A-Z0-9_-]{30,}` | Google API key |
   | `(?i)xox[bapres]-[A-Z0-9-]+` | Slack bot/app/user/refresh/eshare/legacy tokens (including refresh-token class `xoxe-`) |

   Returns `ErrLooksLikeRawCredential`. Note: the alias-shape regex itself
   already rejects uppercase letters, so order matters only because the
   raw-token error is the more informative sentinel — developers seeing
   `ErrLooksLikeRawCredential` immediately understand the bug, while
   `ErrAliasInvalidShape` would obscure it. We test both shape-only and
   raw-token-shaped inputs explicitly.

   #### §7.1 Catalogue coverage

   This is a **Phase-1 catalogue**, not exhaustive. Known gaps a future
   worktree should close: AWS secret access keys (40-char base64 — too
   ambiguous to regex without high false-positive rate; needs an entropy
   check), Stripe `sk_live_…` (overlapping with `sk-`), Twilio
   `AC[0-9a-f]{32}` (high false-positive rate). The catalogue is the
   single source of truth for both `NewCredentialAlias` (construction
   defence) and `JSONContainsRawToken` (pre-write defence) — adding a
   shape hardens both surfaces at once.

3. The 64-character upper bound on the alias-shape regex stops at
   `[a-z0-9_]{2,63}` (1 + 63 = 64) and matches the schema in
   `13_workload_spec.md` §3.4.

**Why this matters.** A developer writing
`cap.CredentialAlias("sk-real-openai-key")` would otherwise land a real
key in `snapshot_json` and ship it to the observer SQLite DB (which by
default is unencrypted on disk and can be uploaded as an artifact for
post-mortem analysis). The factory makes that mistake a build/test-time
error rather than a wire-format leak.

### (b) `ComputeHash` covers OS + every tool version

`ComputeHash` MUST include `OS`, `Arch`, every `ToolVersion{Name,
Version}`, every `CommandInterface.Command`, every `MCPToolDescriptor`,
and every `FileResource`. A rollback attack ("downgrade go to bypass
security checks added in 1.22") MUST change the hash so the monitoring
side can detect the regression.

The implementation achieves this by marshalling the entire `Snapshot`
struct to JSON — every field is included by definition. The test matrix
explicitly exercises OS downgrade and tool-version downgrade so a future
refactor that adds an unmarshal-side cherrypicker (e.g. trimming the
`Tools` slice before hashing) breaks RED.

### (c) `WriteSnapshot` is fully parameterized

The SQL string is a single compile-time constant containing five `?`
placeholders. All five values — `hash`, `agentID`, `workspaceID`,
`created_at`, `snapshot_json` — are passed as `args` to `db.ExecContext`.
There is no `fmt.Sprintf`, no string concatenation of user-supplied data
into the SQL, and no dynamic identifier substitution. DDL string literals
in `schema.sql` are append-only and also static.

The test `TestWriteSnapshot_Parameterized_SQLMetaInAgentID` injects a
classic SQL-injection payload as `agentID`; the table must survive and
the row must be retrievable verbatim.

### (d) `NoCapabilityDiscovery` ablates only upload, never collection

`DisableUpload` gates exactly one code path: the `db.ExecContext` inside
`WriteSnapshot`. It does not gate:

- `ComputeHash` (the slave still computes hashes for its own logs)
- `Snapshot` construction (the slave still inspects its own
  capabilities to refuse impossible tasks early)
- `Snapshot.MarshalJSON` (caller may still serialise for stdout debug)
- The `(e)` secret-scan (see §7.2 below — runs BEFORE the ablation
  short-circuit so an embedded raw token surfaces as
  `ErrSnapshotContainsSecret` regardless of upload state)

When `DisableUpload == true` AND the secret-scan passes, `WriteSnapshot`
writes exactly one log line through `log.Default()` of the form
`[ablation] NoCapabilityDiscovery: skipped snapshot hash=<hex>` and
returns `nil`. The log line is required so post-hoc ablation auditors can
distinguish "snapshot intentionally skipped" from "writer crashed silently".

The default value at process start is `false`.
Phase 2 `WT-2-flag-integration` will flip it via
`ablation.Default.SetByName("NoCapabilityDiscovery", true)` from the CLI;
no other call site flips it.

**Concurrency**: `DisableUpload` is a `*bool` target seen by the ablation
registry (the registry API is `Register(FlagName, *bool)` — a hard
constraint), but the package exposes the *authoritative* value via
`IsUploadDisabled()` which loads from a mirroring `atomic.Bool`. Three
mutation paths:

- `SetDisableUpload(v bool)` — writes both the atomic and the raw bool
  atomically-in-observation-order (`atomic.Store` first, then the bool
  assignment). Tests and direct consumers use this.
- Ablation registry `SetByName(name, v)` — writes ONLY the raw
  `DisableUpload` bool (this is the ablation package's fixed contract).
  The CLI binder (Phase-2 WT-2-flag-integration) MUST call
  `SyncDisableUpload()` immediately after any `SetByName` batch and
  BEFORE starting any goroutine that will call `WriteSnapshot`.
- `SyncDisableUpload()` — copies the current raw `DisableUpload` value
  into the atomic mirror. Idempotent; safe to call multiple times as
  long as no `WriteSnapshot` is in flight concurrently.

`WriteSnapshot` MUST read via `IsUploadDisabled()`; the round-5
fresh-reviewer audit confirmed `go test -race` fires under concurrent
access when the accessor is skipped and a bare `bool` read is used.
The `DisableUpload` variable is retained so the ablation registry
contract still works; the atomic is the source-of-truth for readers.
`TestRegisteredOnAblationDefault` guarantees the registry binding is
still to `&DisableUpload`; a new test asserts a race-free
read/write pattern via the accessor.

#### §7.2 Ordering of secret-scan vs ablation skip

`WriteSnapshot`'s prologue runs in this order:

1. `CanonicalJSON(snap)` — returns error if hand-built malformed input.
2. SHA-256 → `hash` (derived from the same bytes that will be stored).
3. `JSONContainsRawToken(body)` — return `ErrSnapshotContainsSecret`
   if any raw-token regex matches the canonical bytes.
4. `DisableUpload` short-circuit — log skip line, return nil.
5. `db.ExecContext` insert.

Putting (3) before (4) is intentional: a snapshot carrying a leaked
token is a programmer error in the MCP descriptor source, and the
ablation flag exists to disable upload for **experimental** reasons,
not to suppress real correctness diagnostics. A leaky descriptor will
not start leaking just because the run is ablated — and conversely a
clean descriptor will not start producing false negatives because the
ablation toggled on. The two concerns are orthogonal.

### (e) Pre-write secret-strip on canonical JSON

Even with (a) blocking direct `CredentialAlias` misuse, the snapshot's
free-form `MCPToolDescriptor.Description` / `ResultDescription` /
`InputSchema` blob, or a `ToolVersion.Name`/`Version`, could carry an
embedded raw token (an MCP server author pasted a sample request into
their tool description). Before insert, `WriteSnapshot` re-runs the
five raw-token regexes from (a) over the **entire canonical JSON byte
slice**. On any match, the function returns `ErrSnapshotContainsSecret`
and writes nothing to the DB.

The regexes are exposed as a package-private constant
`capability.rawTokenPatterns` so both `NewCredentialAlias` and
`WriteSnapshot` share the same source of truth — adding a new shape
hardens both surfaces at once. Tests cover both descriptor-source and
tool-name-source leak paths.

---

## 8. Acceptance checklist

- [ ] Four new Go files exist (`capability/snapshot.go`,
      `capability/snapshot_test.go`,
      `observerstore/capability_snapshots_writer.go`,
      `observerstore/capability_snapshots_writer_test.go`); `schema.sql`
      gains exactly one `CREATE TABLE IF NOT EXISTS capability_snapshots`
      + one `CREATE INDEX IF NOT EXISTS idx_capability_snapshots_agent`
      at the end. No other `CREATE` statement is touched.
- [ ] `capability.NewCredentialAlias` rejects all five raw-token shapes
      and accepts the §3.4 alias regex.
- [ ] `ComputeHash` is byte-stable, slice-order-independent, and
      differs on OS / tool-version downgrade.
- [ ] `WriteSnapshot` uses parameterized SQL only, is idempotent on
      duplicate hash, strips embedded secrets, and honours the ablation
      flag with a log line.
- [ ] `package init()` of `capability` registers
      `ablation.NoCapabilityDiscovery` against `&DisableUpload`.
- [ ] `go test ./internal/capability/... ./internal/observerstore/...
      -race -shuffle=on -count=1` passes.
- [ ] Three synthetic slave scenarios produce three distinct hashes,
      each stable across two calls.
- [ ] Final commit message ends with
      `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- [ ] Branch is **not pushed** at the end of the worktree task.
