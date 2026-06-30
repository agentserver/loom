# WT-1-capability-snapshot — Plan

> Stage 2 of the three-stage workflow. Companion to
> `docs/specs/wt1-capability-snapshot.spec.md`.
> Branch: `paper/v3/p1-capability-snapshot`.
> Base: `origin/paper/v3-integration` after `17f2c3c`.

This document is the file-by-file work breakdown and the test matrix
Stage 3 will RED-GREEN-REFACTOR through. It does not introduce any new
design decision; everything is grounded in the spec section noted in
parentheses for each step.

---

## 1. Pre-flight checks (already passed in the worktree)

| # | Check | Command | Expected |
|---|---|---|---|
| 1 | Ablation package present | `ls multi-agent/internal/ablation/registry.go` | file exists |
| 2 | `NoCapabilityDiscovery` declared | `grep -n NoCapabilityDiscovery multi-agent/internal/ablation/registry.go` | line ~17 |
| 3 | `Platform` / `CommandInterfaces` on contract | `grep -nE 'Platform\|CommandInterfaces' multi-agent/internal/contract/types.go` | lines 95–96 |
| 4 | `commandiface.Platform` / `CommandInterface` declared | `grep -n 'type Platform\|type CommandInterface' multi-agent/internal/commandiface/interfaces.go` | lines 3 + 8 |
| 5 | `observerstore.OpenSQLite` exec's `schema.sql` | `grep -n 'db.Exec(schemaSQL)' multi-agent/internal/observerstore/store.go` | line ~60 |
| 6 | No prior `capability_snapshots` definition | `grep -rn capability_snapshots multi-agent/internal/observerstore/` | nothing |

If any row fails, stop — the dependency assumption in spec §1 is wrong.

---

## 2. Implementation order (TDD; one file at a time, RED → GREEN per step)

### Step A — `internal/capability/snapshot.go` types only

Spec refs: §3.1 (struct shapes), §1.3 (anchors), §3.3 (Platform/OS invariant).

Concrete additions (no behavior yet):

1. Add file header `package capability`. Imports are added incrementally,
   per step, so `go build` stays green between steps. Step A adds ONLY
   the `commandiface` import (`github.com/yourorg/multi-agent/internal/commandiface`)
   because Step A's types embed `commandiface.Platform` /
   `commandiface.CommandInterface`. Steps C/E/F add `regexp` /
   `crypto/sha256` + `encoding/hex` + `encoding/json` + `sort` / `log` +
   ablation import respectively as their code lands. Adding an import
   ahead of the code that uses it fails `go build` on unused imports.
2. Declare `type ToolVersion struct { Name, Version string }`.
3. Declare `type CredentialAlias string` (defined type, NOT alias) +
   sentinel errors `ErrLooksLikeRawCredential`, `ErrAliasInvalidShape`,
   `ErrSnapshotInvalid` per spec §3.1.
4. Declare `type NetworkReach string` + 4 constants per spec §3.1.
5. Declare `type FileResource struct { KindDetail, PathPattern string }`.
6. Declare `type Snapshot struct {...}` exactly per spec §3 with
   declaration order matching the canonical JSON hash contract
   (struct fields drive `encoding/json` order — order matters for the
   hash).

Tests RED for: nothing yet (types compile-only).
GREEN by: `go build ./internal/capability/...` returns 0.

### Step B — `internal/capability/snapshot_test.go` skeleton + first RED

Add test file with package-level comment explaining the
`DisableUpload`-mutating tests cannot use `t.Parallel`. Then write
`TestCredentialAlias_ValidShape` first (lex-clean: positive case
exercises the eventual factory).

Stage 3 RED-GREEN cycle for Step B–N: after every step the project
must `go test -count=1 ./internal/capability/...` PASS for already-written
tests and the just-added test must initially fail in the expected way.

### Step C — `NewCredentialAlias` + raw-token catalogue

Spec refs: §3.1 (signature), §7(a) (the 5 regexes).

1. Declare package-private `var rawTokenPatterns = []*regexp.Regexp{...}`
   compiled once at package load (NOT in `init()` — pure literal
   compilation in a `var` block; `regexp.MustCompile` is the idiomatic
   way).
2. Declare package-private `aliasShapeRe = regexp.MustCompile(\`^[a-z][a-z0-9_]{2,63}$\`)`.
3. Implement:
   ```go
   func NewCredentialAlias(s string) (CredentialAlias, error) {
       for _, re := range rawTokenPatterns {
           if re.MatchString(s) {
               return "", ErrLooksLikeRawCredential
           }
       }
       if !aliasShapeRe.MatchString(s) {
           return "", ErrAliasInvalidShape
       }
       return CredentialAlias(s), nil
   }
   ```
4. Order matters: raw-token check FIRST so a developer who pastes
   `"sk-real-key"` gets the helpful `ErrLooksLikeRawCredential` rather
   than the less-informative shape error. Spec §7(a) calls this out.

Tests added in `snapshot_test.go`:

- `TestCredentialAlias_ValidShape` — positive (`"abc_def"`, `"s3_prod"`).
- `TestCredentialAlias_RejectsRawSk` — `"sk-abc123def4567"` → `ErrLooksLikeRawCredential`.
- `TestCredentialAlias_RejectsRawEyJ` — `"eyJhbGciOiJIUzI1NiJ9.ZXlKMGVYQWlPaUpLVjFRaUxDSmhiR2NpT2lKSVV6STFOaUo5.sig"` → `ErrLooksLikeRawCredential`.
- `TestCredentialAlias_RejectsRawAKIA` — `"AKIAABCDEFGHIJKLMNOP"` → `ErrLooksLikeRawCredential`.
- `TestCredentialAlias_RejectsRawGhp` — `"ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ012345"` → `ErrLooksLikeRawCredential`.
- `TestCredentialAlias_RejectsRawXox` — table-driven across 5 prefixes
  `xoxb-`/`xoxa-`/`xoxp-`/`xoxr-`/`xoxs-` → all `ErrLooksLikeRawCredential`.
- `TestCredentialAlias_RejectsUppercase` — `"MY_KEY"` → `ErrAliasInvalidShape`.

### Step D — `NewSnapshot` factory + invariants

Spec refs: §3.1 (signature), §3.3 (OS/Platform.OS drift), §3.2 (empty/unsupported rules).

```go
func NewSnapshot(spec Snapshot) (Snapshot, error) {
    if spec.OS == "" { return Snapshot{}, fmt.Errorf("%w: OS empty", ErrSnapshotInvalid) }
    if spec.Arch == "" { return Snapshot{}, fmt.Errorf("%w: Arch empty", ErrSnapshotInvalid) }
    if spec.OS != spec.Platform.OS { return Snapshot{}, fmt.Errorf("%w: OS/Platform.OS drift", ErrSnapshotInvalid) }
    if spec.Arch != spec.Platform.Arch { return Snapshot{}, fmt.Errorf("%w: Arch/Platform.Arch drift", ErrSnapshotInvalid) }
    switch spec.Network {
    case NetworkInternet, NetworkIntranet, NetworkLoopbackOnly, NetworkNone:
    default:
        return Snapshot{}, fmt.Errorf("%w: unknown NetworkReach %q", ErrSnapshotInvalid, spec.Network)
    }
    for i, tv := range spec.Tools {
        if tv.Name == "" { return Snapshot{}, fmt.Errorf("%w: Tools[%d].Name empty", ErrSnapshotInvalid, i) }
    }
    for i, f := range spec.Files {
        switch f.KindDetail {
        case "repo", "dataset", "fixture", "config":
        default:
            return Snapshot{}, fmt.Errorf("%w: Files[%d].KindDetail %q", ErrSnapshotInvalid, i, f.KindDetail)
        }
    }
    return spec, nil
}
```

Tests added:

- `TestNewSnapshot_RejectsInconsistentPlatformAndOS` — OS=linux,
  Platform.OS=darwin → `errors.Is(err, ErrSnapshotInvalid)`.
- `TestNewSnapshot_RejectsUnknownNetworkReach` — `Network="wan"` rejected.
- `TestNewSnapshot_RejectsUnknownFileKindDetail` — `KindDetail="screenshot"` rejected.
- (Implicit) Happy path: `TestNewSnapshot_AcceptsValid` constructs a
  realistic 3-tool linux snapshot and checks `errors.Is(nil, ...)` is
  false / err is nil.

### Step E — Canonical sort helper + `ComputeHash` + `Hash`

Spec refs: §4 + §4.1 (canonical encoding).

```go
func canonical(s Snapshot) Snapshot {
    // Copy slices to avoid mutating caller; sort each in place.
    if s.Tools != nil {
        cp := append([]ToolVersion(nil), s.Tools...)
        sort.Slice(cp, func(i, j int) bool {
            if cp[i].Name != cp[j].Name { return cp[i].Name < cp[j].Name }
            return cp[i].Version < cp[j].Version
        })
        s.Tools = cp
    }
    if s.CommandInterfaces != nil {
        cp := append([]commandiface.CommandInterface(nil), s.CommandInterfaces...)
        sort.Slice(cp, func(i, j int) bool {
            if cp[i].Skill != cp[j].Skill { return cp[i].Skill < cp[j].Skill }
            if cp[i].Kind != cp[j].Kind { return cp[i].Kind < cp[j].Kind }
            return cp[i].Command < cp[j].Command
        })
        s.CommandInterfaces = cp
    }
    if s.MCPTools != nil {
        cp := append([]MCPToolDescriptor(nil), s.MCPTools...)
        sort.Slice(cp, func(i, j int) bool {
            if cp[i].Server != cp[j].Server { return cp[i].Server < cp[j].Server }
            return cp[i].Name < cp[j].Name
        })
        s.MCPTools = cp
    }
    if s.Files != nil {
        cp := append([]FileResource(nil), s.Files...)
        sort.Slice(cp, func(i, j int) bool {
            if cp[i].KindDetail != cp[j].KindDetail { return cp[i].KindDetail < cp[j].KindDetail }
            return cp[i].PathPattern < cp[j].PathPattern
        })
        s.Files = cp
    }
    if s.Credentials != nil {
        cp := append([]CredentialAlias(nil), s.Credentials...)
        sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
        s.Credentials = cp
    }
    return s
}

func canonicalJSON(s Snapshot) []byte {
    b, _ := json.Marshal(canonical(s)) // Marshal of a value-only struct can't error.
    return b
}

func ComputeHash(s Snapshot) string {
    sum := sha256.Sum256(canonicalJSON(s))
    return hex.EncodeToString(sum[:])
}

func (s Snapshot) Hash() string { return ComputeHash(s) }
```

Tests added:

- `TestComputeHash_Stable` — call twice with same value; require byte equality.
- `TestComputeHash_DiffersOnOSDowngrade` — build two snapshots via
  `NewSnapshot` (so the Platform/OS pair is kept consistent); flip both
  OS+Platform.OS together; hashes differ.
- `TestComputeHash_DiffersOnToolVersionDowngrade` — `{go, 1.22.0}` vs
  `{go, 1.18.0}`.
- `TestComputeHash_DiffersOnMCPDescriptorChange` — adding an `MCPTool`
  changes hash.
- `TestComputeHash_IndependentOfSliceOrder` — permute `Tools`,
  `MCPTools`, `Credentials`, `Files`, `CommandInterfaces` independently;
  hash unchanged.
- `TestSnapshot_Hash_EqualsComputeHash` — `s.Hash() == ComputeHash(s)`.

### Step F — `DisableUpload` + ablation registration

Spec refs: §7(d), §1.3 (`ablation.Default.Register` anchor).

```go
// DisableUpload is the ablation toggle for NoCapabilityDiscovery.
// When true, observerstore.WriteSnapshot must short-circuit (skip the
// DB write) while still allowing local snapshot collection to proceed.
// Defaults to false. Mutated only by the Phase 2 CLI binder via
// ablation.Default.SetByName before run start.
var DisableUpload bool

func init() {
    if err := ablation.Default.Register(ablation.NoCapabilityDiscovery, &DisableUpload); err != nil {
        // init-time panic would DoS the process before main runs; the
        // ablation package contract says Register never panics, and the
        // only failure modes here are "duplicate registration" or
        // "unknown flag" — both indicate a build-time bug. Log loudly so
        // dev / test see it; do not panic.
        log.Printf("capability: ablation registration failed: %v", err)
    }
}
```

Tests added:

- `TestNoCapabilityDiscovery_DefaultIsFalse` — `DisableUpload == false`
  at package load. (Run before any test that flips it; deterministic via
  unique test name.)
- `TestRegisteredOnAblationDefault` — `ablation.Default.List()` contains
  `ablation.NoCapabilityDiscovery`. (List from the ablation package's
  spec is sorted, allocate-fresh.)

### Step G — `internal/observerstore/schema.sql` append

Spec refs: §5.1.

Append at end of file (after the last existing `CREATE INDEX`):

```sql

-- WT-1-capability-snapshot: A1 capability snapshot persistence
-- (spec: docs/specs/wt1-capability-snapshot.spec.md §5.1).
CREATE TABLE IF NOT EXISTS capability_snapshots (
  hash          TEXT PRIMARY KEY,
  agent_id      TEXT NOT NULL,
  workspace_id  TEXT NOT NULL,
  created_at    TEXT NOT NULL,
  snapshot_json TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_capability_snapshots_agent
  ON capability_snapshots(workspace_id, agent_id, created_at);
```

No other line of the existing file is touched. Verified with
`git diff --stat multi-agent/internal/observerstore/schema.sql`.

### Step H — `internal/observerstore/capability_snapshots_writer.go`

Spec refs: §5.2, §5.3, §7(c)–(e).

```go
package observerstore

import (
    "context"
    "database/sql"
    "errors"
    "fmt"
    "log"
    "time"

    "github.com/yourorg/multi-agent/internal/capability"
)

// ErrSnapshotContainsSecret is returned by WriteSnapshot when the
// canonical JSON of the snapshot contains a substring matching a known
// raw-token regex. See spec §7(e).
var ErrSnapshotContainsSecret = errors.New("observerstore: snapshot contains embedded secret")

// nowUTC is overridable in tests for deterministic created_at.
var nowUTC = func() time.Time { return time.Now().UTC() }

const insertCapabilitySnapshotSQL = `
INSERT INTO capability_snapshots
  (hash, agent_id, workspace_id, created_at, snapshot_json)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(hash) DO NOTHING;
`

// WriteSnapshot persists snap to the capability_snapshots table.
//
// Behaviour matrix:
//   - capability.DisableUpload == true  → log skip line + return nil.
//   - canonical JSON contains a raw-token regex hit
//                                       → return ErrSnapshotContainsSecret.
//   - otherwise                         → ExecContext insert.
func WriteSnapshot(
    ctx context.Context,
    db *sql.DB,
    agentID string,
    workspaceID string,
    snap capability.Snapshot,
) error {
    hash := snap.Hash()
    if capability.DisableUpload {
        log.Printf("[ablation] NoCapabilityDiscovery: skipped snapshot hash=%s", hash)
        return nil
    }
    body, err := capability.CanonicalJSON(snap) // exported helper
    if err != nil {
        return fmt.Errorf("observerstore: canonical json: %w", err)
    }
    if capability.JSONContainsRawToken(body) { // exported helper
        return ErrSnapshotContainsSecret
    }
    _, err = db.ExecContext(ctx, insertCapabilitySnapshotSQL,
        hash, agentID, workspaceID, nowUTC().Format(time.RFC3339Nano), string(body))
    return err
}
```

Notes:

- `CanonicalJSON` and `JSONContainsRawToken` are exported helpers we add
  to `capability/snapshot.go` so the observerstore writer never duplicates
  the regex catalogue or canonicalisation logic — single source of truth
  per spec §7(e).
- `CanonicalJSON(s)` signature: `func CanonicalJSON(s Snapshot) ([]byte, error)`.
  Implementation calls `json.Marshal(canonical(s))`. Returns `nil, err`
  only if a future field type makes JSON marshalling fallible; today it
  is infallible but we keep the error return for forward-compat.
- `JSONContainsRawToken(b []byte) bool` walks the same `rawTokenPatterns`
  slice used by `NewCredentialAlias`.

### Step I — `internal/observerstore/capability_snapshots_writer_test.go`

Test list (in addition to the capability-side tests from Steps B–F):

| Test | Verifies | Setup |
|---|---|---|
| `TestWriteSnapshot_HappyPath` | Insert succeeds, row count == 1, retrieved fields match. | OpenSQLite in-memory `file::memory:?cache=shared`; build snapshot via `NewSnapshot`. |
| `TestWriteSnapshot_Parameterized_SQLMetaInAgentID` | `agentID = "a'); DROP TABLE capability_snapshots; --"` inserts; table still exists; retrieved `agent_id` is the literal string. | Same DB; assert `SELECT count(*) FROM sqlite_master WHERE name='capability_snapshots'` == 1 after. |
| `TestWriteSnapshot_IdempotentOnDuplicateHash` | Two inserts of same snap → no error, exactly 1 row. | — |
| `TestWriteSnapshot_DifferentAgentsSameHashOneRow` | Snap byte-equal for two agents → 1 row by `hash`; both agents recoverable via index (we query `pragma index_info('idx_capability_snapshots_agent')` to assert index exists; row dedup is the natural result of `ON CONFLICT(hash) DO NOTHING`). | — |
| `TestWriteSnapshot_RejectsEmbeddedSecretInMCPDescription` | `MCPTools[0].Description = "use sk-abc123def456 to auth"` → `errors.Is(err, ErrSnapshotContainsSecret)`; row count == 0. | — |
| `TestWriteSnapshot_RejectsEmbeddedSecretInToolName` | `Tools[0].Name = "ghp_ABCDEFGHIJKLMNOPQRSTUV"` → same. | — |
| `TestNoCapabilityDiscovery_SkipsUploadButReturnsNil` | Flip `capability.DisableUpload = true` (restored via `t.Cleanup`); call `WriteSnapshot`; row count remains 0; err == nil. | — |
| `TestNoCapabilityDiscovery_LogsSkipLine` | Same setup; `log.Default().SetOutput(buf)` (restored); after WriteSnapshot, `buf.String()` contains `"NoCapabilityDiscovery: skipped"` and the hash. | — |
| `TestThreeSlavesProduceDistinctHashes` | Per spec §6.2 three synthetic scenarios → 3 distinct hashes; each stable on repeat. (This test lives in the capability package, since it only exercises hash math; included in this writer file's neighbour `capability/snapshot_test.go`.) | — |

Testing infrastructure:

- Use `observerstore.OpenSQLite("file::memory:?cache=shared")` per test
  (each `t.Run` gets its own in-memory DB by virtue of unique cache
  parameter; we mint a per-test name with `t.Name()`).
- All ablation-flipping tests are explicitly serial:
  `// NOTE: tests in this file that touch capability.DisableUpload must
  not call t.Parallel(); they share package state.`
- `t.Cleanup(func() { capability.DisableUpload = false })` restores after
  every flip; the `TestNoCapabilityDiscovery_DefaultIsFalse` test runs
  first lexicographically as a belt-and-braces guard against an
  unrestored prior test in the same binary.

---

## 3. Final verification

```bash
# Run inside the Go module root (multi-agent/) for go test/vet/fmt,
# but git commands run from the worktree root where the multi-agent/
# prefix is part of the repo-relative path.

cd /root/multi-agent/.worktrees/p1-capability-snapshot/multi-agent
go test ./internal/capability/... ./internal/observerstore/... -count=1 -shuffle=on -race
go vet ./...
gofmt -l internal/capability internal/observerstore

cd /root/multi-agent/.worktrees/p1-capability-snapshot
git diff --stat multi-agent/internal/observerstore/schema.sql   # should be ~+12/-0 lines (the appended block)
git diff multi-agent/internal/contract/types.go                 # MUST be empty
git diff multi-agent/internal/commandiface/                     # MUST be empty
git diff multi-agent/internal/ablation/                         # MUST be empty
```

`-shuffle=on` proves no test depends on the running order of another
that flips `DisableUpload`. `-race` proves no goroutine accidentally
reads/writes that flag concurrently (it shouldn't — but Go's flag mock
patterns sometimes drift; the race detector is cheap insurance).

---

## 4. Test matrix → Spec security mapping (cross-reference)

For Stage 1 Codex audit-trail:

| Test | Spec security item |
|---|---|
| `TestCredentialAlias_Valid*`                          | (a) shape positive |
| `TestCredentialAlias_RejectsRaw{Sk,EyJ,AKIA,Ghp,Xox}` | (a) 5 raw-token shapes |
| `TestCredentialAlias_RejectsUppercase`                | (a) shape negative |
| `TestComputeHash_DiffersOnOSDowngrade`                | (b) OS rollback detection |
| `TestComputeHash_DiffersOnToolVersionDowngrade`       | (b) tool rollback detection |
| `TestComputeHash_DiffersOnMCPDescriptorChange`        | (b) MCP-set detection |
| `TestComputeHash_IndependentOfSliceOrder`             | determinism guard for (b) |
| `TestWriteSnapshot_Parameterized_SQLMetaInAgentID`    | (c) SQL injection rejected |
| `TestWriteSnapshot_IdempotentOnDuplicateHash`         | DDL idempotency guard for (c) |
| `TestWriteSnapshot_DifferentAgentsSameHashOneRow`     | DDL dedup contract for (c) |
| `TestNoCapabilityDiscovery_SkipsUploadButReturnsNil`  | (d) flag scope = upload only |
| `TestNoCapabilityDiscovery_LogsSkipLine`              | (d) audit trail |
| `TestNoCapabilityDiscovery_DefaultIsFalse`            | (d) safe default |
| `TestRegisteredOnAblationDefault`                     | (d) registry integration |
| `TestWriteSnapshot_RejectsEmbeddedSecretInMCPDescription` | (e) MCP descriptor leak path |
| `TestWriteSnapshot_RejectsEmbeddedSecretInToolName`   | (e) tool-name leak path |
| `TestNewSnapshot_Rejects*`                            | invariant guard for §3.2 |
| `TestSnapshot_Hash_EqualsComputeHash`                 | API contract (Hash ↔ ComputeHash) |
| `TestThreeSlavesProduceDistinctHashes`                | spec §6.2 acceptance |

Every Security item (a)–(e) is covered. Every §6 row in the spec is
present in this plan with the same test name.

---

## 5. Out-of-scope (intentional)

- Postgres parity (spec §1.2).
- Migration runner / `migrations/` directory (spec §1.5).
- Production slave wiring of `WriteSnapshot` into the inspect /
  capability-card publish path (separate Phase 1 worktree). This
  worktree ships only the library + DDL + ablation registration.
- CLI binding `--ablation NoCapabilityDiscovery` (Phase 2
  WT-2-flag-integration).
- Field-level evaluator implementation of `CapabilityRecall` /
  `CapabilityPrecision` (Phase 2 evaluator worktree).

---

## 6. Commit & branch hygiene

- Single squashed commit at the end of Stage 3 (or two: one for
  spec/plan, one for code — Stage 1 spec already committed at
  `de0ca72`).
- Commit message footer:
  `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`
- **Do not push.** Worktree exit hands back the branch state for the
  parent prompter to review and merge.
