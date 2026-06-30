# WT-1-run-schema — Spec

> Source: `/root/paper_writing/docs/final/todo_list.md` Phase 1 table row
> **WT-1-run-schema** (line ~68).
> Branch: `paper/v3/p1-run-schema`.
> Base: `origin/paper/v3-integration` HEAD = `17f2c3c` (WT-1-ablation-registry
> already merged via PR #51).
> Authoritative field list: `/root/paper_writing/docs/intermediate/08_evaluation_plan_v3.md`
> lines 256–279 (24 fields, enumerated verbatim in §2.1 below).

---

## 1. Task boundary & file scope

This worktree introduces two brand-new Go components inside the
`multi-agent/` Go module (`github.com/yourorg/multi-agent`):

- A library package `internal/evalrun/` that owns the per-run schema struct,
  the SQL writer, the schema-drift guard, validation sentinels, and the
  `NoObserver` ablation flag registration.
- A CLI binary `cmd/evalrun-export/` that reads from the observer SQLite DB
  and exports per-run rows to CSV or JSONL.

It also **appends** the `runs` table DDL (and one index) to the existing
shared file `internal/observerstore/schema.sql`. The append goes at the end
of the file so that the parallel `WT-1-capability-snapshot` worktree, which
also appends to the same file, does not collide on line numbers.

Hard rules:

- All new code lives under `multi-agent/internal/evalrun/` and
  `multi-agent/cmd/evalrun-export/` (created by this worktree).
- The ONLY modification outside those new directories is the DDL append at
  the end of `multi-agent/internal/observerstore/schema.sql`. No other file
  in `observerstore/` is touched, no Go code is modified in any other
  package.
- **Intentional divergence from the Phase 1 row's file-scope wording.** The
  Phase 1 row in `todo_list.md` writes the DDL location as
  `internal/observerstore/migrations/*_runs.sql`. The repository as of base
  `17f2c3c` does NOT have an `observerstore/migrations/` subdirectory
  (verified: `ls multi-agent/internal/observerstore/` returns only
  `schema.sql`, `store.go`, etc.); existing tables all live in the single
  `schema.sql` file, applied at `OpenSQLite` via `db.Exec(schemaSQL)`. The
  kickoff prompt §前置条件 #3 calls this out and instructs us to append to
  the existing `schema.sql`. We follow the prompt, not the todo_list row.
- No additions to `multi-agent/go.mod` beyond the standard library and
  packages already required by `go.mod` (e.g. `database/sql`,
  `github.com/mattn/go-sqlite3` if and only if it is already a direct
  requirement — otherwise tests construct `*sql.DB` via the in-tree
  driver). v1 must not pull in new third-party deps for hashing,
  validation, or CSV output: `crypto/sha256`, `regexp`, `encoding/csv`,
  `encoding/json`, and `flag` are all stdlib.

### 1.1 Dependency on `internal/ablation` (already merged)

`origin/paper/v3-integration @ 17f2c3c` ships
`internal/ablation/registry.go` with the following surface:

- Typed `FlagName` (defined string type) with eight canonical constants;
  `ablation.NoObserver` is one of them.
- `Default` is the process-wide `*Registry`.
- `Default.Register(name FlagName, target *bool) error` returns a sentinel
  on bad input (`ErrUnknownFlag`, `ErrNilTarget`, `ErrAlreadyRegistered`,
  `ErrTargetAlreadyRegistered`) and **never panics**.

The `evalrun` package's `init()` MUST therefore register `NoObserver` via
`Default.Register`, log a single warning if registration fails, and
otherwise proceed. Panicking from `init()` would DoS the whole binary
before `main` runs — see ablation spec §7 (a) for the same argument; we
inherit that contract.

### 1.2 Intentional divergence from the worktree-prompt sketch

The kickoff prompt's `Schema` struct sketch lists 24 fields plus a
parenthetical comment that "08 schema 列了两次" for `SelectedContext` and
`GroundTruthContext`. After re-counting `08_evaluation_plan_v3.md` lines
256–279, there are **24 fields total** (not 27 — the prompt's "27" is a
typo confirmed with the user). The `Schema` struct, the DDL, the
expected-column set used by the schema-drift guard, the CSV/JSONL export
header, and every test that enumerates columns all use **exactly 24**.
The drift-guard's expected column set is the same 24 column names in the
same declaration order.

Apart from the field count, this spec follows the kickoff prompt
verbatim. Anywhere this spec contradicts the prompt, this spec is the
source of truth.

---

## 2. Public API — `package evalrun`

### 2.1 The 24-field `Schema` struct

Field order below matches the order in `08_evaluation_plan_v3.md`
lines 256–279, which is also the column order in the DDL (§3) and the
CSV/JSONL export (§4). The sample values are concrete fixtures that the
`TestSchema_AllFieldsRoundtrip` test in the plan uses verbatim.

```go
type Schema struct {
    // 1.  08:256
    RunID                  string    // e.g. "run-2026-06-30T12-00-00-E1-wl01-full"
    // 2.  08:257
    WorkloadID             string    // e.g. "wl-credential-bound-model"
    // 3.  08:258
    ClaimID                string    // e.g. "C1" (one of C1..C5)
    // 4.  08:259
    ExperimentID           string    // e.g. "E1" (one of E1..E6)
    // 5.  08:260
    BaselineOrAblation     string    // e.g. "full" | "manual_ssh" | "NoCapabilityDiscovery"
    // 6.  08:261
    LoomCommit             string    // full 40-char git SHA, e.g. "17f2c3cdec8c891c96ea155351600eb76292b269"
    // 7.  08:262
    AgentserverCommit      string    // full git SHA
    // 8.  08:263
    ModelserverCommit      string    // full git SHA
    // 9.  08:264
    AppCommit              string    // full git SHA
    // 10. 08:265
    MachineTopology        string    // free-form, e.g. "linux/amd64 1×driver 3×slave"
    // 11. 08:266
    ContextGroundTruth     string    // 13 号 §3.3 snapshot scope, e.g. "(driver,inspect_capabilities)"
    // 12. 08:267  — interface placeholder, filled by WT-1-capability-snapshot
    CapabilitySnapshotHash string    // sha256 hex of capability snapshot blob; "" until consumer wires it
    // 13. 08:268  — interface placeholder, filled by WT-1-contract-schema
    TaskContractHash       string    // sha256 hex of normalised contract JSON; "" until consumer wires it
    // 14. 08:269  — interface placeholder, filled by WT-2-driver-promotion-chain
    DynamicMCPRegistryHash string    // sha256 hex of registry snapshot; "" until consumer wires it
    // 15. 08:270
    SelectedContext        string    // (agent_role,context_id) the run actually selected
    // 16. 08:271
    GroundTruthContext     string    // (agent_role,context_id) the labels say is correct
    // 17. 08:272
    StartTime              time.Time // UTC, serialised as RFC3339Nano
    // 18. 08:273
    EndTime                time.Time // UTC, serialised as RFC3339Nano
    // 19. 08:274
    SuccessOracleResult    string    // CHECK constraint: "pass" | "fail" | "timeout"
    // 20. 08:275
    FailureCategory        string    // observerstore.AllCategories() value OR "unknown" sentinel; "" iff result == "pass"
    // 21. 08:276
    HumanInterventionCount int       // count of humanloop pauses surfaced to the operator
    // 22. 08:277
    ArtifactHashes         []string  // each MUST match ^[a-f0-9]{64}$ (sha256 hex)
    // 23. 08:278
    ObserverTracePath      string    // relative path under tests/eval/runs/<run_id>/observer/
    // 24. 08:279
    ModelTraceID           string    // model-side trace ID returned by modelserver
}
```

`08_evaluation_plan_v3.md` lists `context_ground_truth` (line 266) and
`ground_truth_context` (line 271) as two distinct fields. They are
semantically different — the snapshot-scope tuple vs the per-run
`(agent_role,context_id)` label — and the spec keeps both as separate
columns. The `GroundTruthContext` comment in the struct documents this.

### 2.2 Validation sentinels and limits

Exported sentinel errors, all returned by `(*SQLWriter).Insert` on
constructed-but-invalid input. Callers MUST test with `errors.Is`;
string contents are not part of the API contract. The actual error
returned is always `fmt.Errorf("evalrun: <field-name>[<index>]: ...:
%w", sentinel)`-wrapped — for example a bad sha256 at index 2 yields
`evalrun: artifact_hashes[2]: "DEADBEEF" not sha256 hex:
evalrun: invalid artifact_hashes entry (must be sha256 hex)`. The
wrap preserves `errors.Is(err, ErrInvalidArtifactHash)` so callers can
branch, while the human-readable prefix tells the operator WHICH
field/index failed (an unwrapped sentinel on a 200-element artifact
slice is unactionable):

```go
var (
    ErrInvalidRunID            = errors.New("evalrun: invalid run_id format")
    ErrInvalidArtifactHash     = errors.New("evalrun: invalid artifact_hashes entry (must be sha256 hex)")
    ErrOversizedField          = errors.New("evalrun: field exceeds 8 KiB limit")
    ErrInvalidOracleResult     = errors.New("evalrun: success_oracle_result must be pass|fail|timeout")
    ErrInvalidTime             = errors.New("evalrun: start_time/end_time must be non-zero")
    ErrSchemaDrift             = errors.New("evalrun: runs table schema does not match expected layout")
)
```

Format and length limits (enforced in `Insert` BEFORE the SQL exec):

- `RunID` MUST match `^[A-Za-z0-9_-]{8,128}$`. Rejects path-traversal
  payloads (`/`, `..`), whitespace, and SQL meta-characters; bounds
  PRIMARY-KEY index size; matches the regex shape in the worktree prompt
  Security item (f).
- Every entry in `ArtifactHashes` MUST match `^[a-f0-9]{64}$`. Empty
  slice is allowed (a run with no artifacts is legitimate). The empty
  string is NOT a valid entry — it is rejected as
  `ErrInvalidArtifactHash`.
- Every other string field has a hard cap of 8 KiB (`8 * 1024` bytes,
  measured by `len(s)` on the UTF-8 form). 8 KiB chosen because the
  longest legitimate value is `MachineTopology`, which in practice is
  one line; 8 KiB is two orders of magnitude headroom and still caps
  DoS-via-large-row.
- `SuccessOracleResult` MUST be exactly one of `pass`, `fail`,
  `timeout`. Anything else is `ErrInvalidOracleResult` (the DDL CHECK
  constraint backstops this in the database, but the Go-side check fires
  first and returns a typed sentinel).
- `StartTime` and `EndTime` MUST be non-zero (`!t.IsZero()`). A zero
  `time.Time` would serialise to `0001-01-01T00:00:00Z`, which is a
  valid RFC3339Nano string but semantically meaningless and downstream
  metric extractors would silently mis-bucket it. They are NOT
  required to be in any particular order — a long-running diagnostic
  workload may legitimately have `EndTime < StartTime` if clocks
  resync, and that's the operator's call to investigate, not the
  writer's to block.

### 2.3 `Writer` interface and `SQLWriter` constructor

```go
// Writer is the per-run sink for D1 evaluation rows. Implementations MUST
// be safe to call from a single goroutine; concurrent use is not part of
// the contract (the eval-runner is single-threaded).
type Writer interface {
    Insert(ctx context.Context, s Schema) error
    Close() error
}

// NewSQLWriter wraps *sql.DB into a Writer that targets the `runs` table.
//
// On first call it runs schema-drift detection (§5) against the DB's
// current `runs` table. If the DB has no `runs` table at all, it does
// NOT auto-create one (DDL lives in observerstore/schema.sql and is
// applied by the observer init path, not the writer). In that case
// drift-detection returns ErrSchemaDrift with a wrapped message naming
// "runs (table missing)".
func NewSQLWriter(db *sql.DB) (Writer, error)
```

Implementation notes:

- `Insert` runs validation first, then a single `INSERT INTO runs (...)
  VALUES (?, ?, ..., ?)` using **parameterised arguments** through
  `db.ExecContext` — no `fmt.Sprintf` or string concatenation builds
  any of the value placeholders or column literals. The column list is
  a hard-coded constant in the package (private `const insertSQL =
  "INSERT INTO runs (run_id, workload_id, ..."`), itself never
  interpolated.
- `ArtifactHashes` and the two `time.Time` fields are serialised in Go
  before the parameterised exec:
  - `ArtifactHashes` → `json.Marshal` → stored in column
    `artifact_hashes` (JSON array of hex strings). The DDL DEFAULT for
    that column is `'[]'`; `json.Marshal(nil) == []byte("null")`
    cannot be allowed because a downstream `JSON_EXTRACT` of `null`
    would behave inconsistently across SQLite versions — the writer
    therefore special-cases nil/empty as the literal byte string `[]`,
    never `null`. The Go field is `ArtifactHashes` (no `Json` suffix)
    because it carries `[]string` to consumers, not raw JSON.
  - `StartTime` / `EndTime` → `t.UTC().Format(time.RFC3339Nano)`. The
    UTC normalisation is mandatory: storing a local-time-zoned RFC3339
    would make downstream `ORDER BY start_time` correct but
    `>= '2026-...'` comparisons subtly wrong across DST runs.
- `Close` closes the underlying `*sql.DB` only if `NewSQLWriter` was
  given ownership (v1: `Close` is a no-op on the wrapper, callers own
  `*sql.DB` lifetime — this matches `observerstore.Store`'s pattern in
  the same module).

### 2.4 `NoObserver` ablation flag

```go
// DisableTelemetry, when true, causes (*SQLWriter).Insert to skip the
// DB write AND log one structured line per dropped run. It is wired into
// ablation.Default at init time via Default.Register(ablation.NoObserver,
// &DisableTelemetry).
var DisableTelemetry bool

func init() {
    if err := ablation.Default.Register(ablation.NoObserver, &DisableTelemetry); err != nil {
        log.Printf("evalrun: ablation.Default.Register(NoObserver) failed: %v", err)
    }
}
```

On `DisableTelemetry == true`, `Insert` MUST:

1. Run the same validation pipeline (so the operator gets the same
   error for a malformed row whether telemetry is on or off — silent
   acceptance of bad rows would let an ablation accidentally hide
   schema violations).
2. Skip the `db.ExecContext`.
3. Emit exactly one log line of the form
   `[ablation] NoObserver: dropped run_id=<run_id>` via the standard
   library `log` package (the prefix `[ablation]` is grep-friendly and
   keeps the audit story portable across structured/unstructured
   logging backends).
4. Return `nil`.

The dropped-row log line is the **non-negotiable security counterweight**
for the NoObserver ablation — see §7 (c).

---

## 3. DDL append to `internal/observerstore/schema.sql`

Appended verbatim to the END of the file (after the
`idx_resource_snapshots_latest` index). The leading blank line is
required so the diff against the parallel capability-snapshot worktree
stays small and the file stays grep-friendly.

```sql

-- WT-1-run-schema: per-run D1 evaluation rows (24 columns matching
-- /root/paper_writing/docs/intermediate/08_evaluation_plan_v3.md lines 256-279).
CREATE TABLE IF NOT EXISTS runs (
    run_id                    TEXT PRIMARY KEY,
    workload_id               TEXT NOT NULL,
    claim_id                  TEXT NOT NULL,
    experiment_id             TEXT NOT NULL,
    baseline_or_ablation      TEXT NOT NULL,
    loom_commit               TEXT NOT NULL,
    agentserver_commit        TEXT NOT NULL,
    modelserver_commit        TEXT NOT NULL,
    app_commit                TEXT NOT NULL,
    machine_topology          TEXT NOT NULL,
    context_ground_truth      TEXT NOT NULL,
    capability_snapshot_hash  TEXT NOT NULL DEFAULT '',
    task_contract_hash        TEXT NOT NULL DEFAULT '',
    dynamic_mcp_registry_hash TEXT NOT NULL DEFAULT '',
    selected_context          TEXT NOT NULL,
    ground_truth_context      TEXT NOT NULL,
    start_time                TEXT NOT NULL,
    end_time                  TEXT NOT NULL,
    success_oracle_result     TEXT NOT NULL CHECK(success_oracle_result IN ('pass','fail','timeout')),
    failure_category          TEXT NOT NULL DEFAULT '',
    human_intervention_count  INTEGER NOT NULL DEFAULT 0,
    artifact_hashes           TEXT NOT NULL DEFAULT '[]',
    observer_trace_path       TEXT NOT NULL DEFAULT '',
    model_trace_id            TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_runs_experiment ON runs(experiment_id, workload_id);
```

Notes:

- Column count: **24** data columns. The SQL column name is
  `artifact_hashes` (matching the 08 schema literal name from line 277);
  it stores the JSON-array serialisation of the Go `[]string` field
  `ArtifactHashes`. The drift-guard's expected set uses
  `artifact_hashes` so column-name comparison stays a pure equality
  check against the 08 spec.
- `CHECK(success_oracle_result IN ('pass','fail','timeout'))` is a
  belt-and-suspenders check against any future caller that bypasses the
  Go validator and writes raw SQL. In SQLite the CHECK is enforced;
  Postgres parity is provided by the postgres mirror file (NOT in scope
  for this worktree — postgres mirror is owned by whoever next touches
  `observerstore/postgres/schema.sql`).
- `IF NOT EXISTS` + `DEFAULT ''` / `DEFAULT '[]'` for the placeholder
  hash columns lets downstream worktrees (`WT-1-capability-snapshot`,
  `WT-1-contract-schema`, `WT-2-driver-promotion-chain`) start writing
  values into those columns without a coordinated DDL change. They MUST
  use a separate `ALTER TABLE` migration if they need to ADD columns;
  for the three placeholder columns this spec carves out, they just
  flip from `''` to the real hash.
- This spec does NOT modify
  `multi-agent/internal/observerstore/postgres/schema.sql`. Postgres
  parity is a separate concern; the eval pipeline runs against SQLite
  per §13 of `/root/paper_writing/docs/intermediate/13_workload_spec.md`.

---

## 4. `cmd/evalrun-export/main.go` — CLI

### 4.1 Flags

| Flag                  | Type     | Required | Default                          | Effect |
|---|---|---|---|---|
| `--format`            | `string` | yes      | (none)                           | `csv` or `jsonl`. Any other value → exit 2 with usage. |
| `--out`               | `string` | no       | `-` (stdout)                     | Path to write the export to. `-` or empty → stdout. |
| `--db`                | `string` | no       | env `OBSERVER_DB`, then `""`     | SQLite path. Empty after env lookup → exit 2 with usage. |
| `--filter-experiment` | `string` | no       | `""` (no filter)                 | Restrict export to rows where `experiment_id = <value>`. |

### 4.2 Output shape

- **CSV** (`encoding/csv` writer):
  - First row is the 24 SQL column names in the order defined in §3.
    Empty DB → only that header row, exit 0.
  - Subsequent rows are one per `SELECT * FROM runs [...] ORDER BY
    run_id`. `artifact_hashes` is exported as the raw JSON-array
    string (not re-parsed) so a downstream `jq`/`pandas` consumer can
    decide how to unpack it. Times are exported as the RFC3339Nano
    strings stored in the DB.
  - **Formula-injection escape (security item (e) / CWE-1236):** for
    every cell whose first byte is one of `=`, `+`, `-`, `@`, `\t`,
    `\r`, or `\n`, the writer prepends a single `'` (apostrophe)
    before handing the value to `csv.Writer.Write`. The tab / CR / LF
    cases are included because Excel and LibreOffice Calc both
    interpret a leading whitespace control character as cell-content
    that can re-position a subsequent `=` into the formula slot, and
    LF is the byte some spreadsheet importers normalise CR/CRLF into
    before the formula check fires. The empty string is NOT prefixed
    (no first byte).
- **JSONL** (`encoding/json` writer):
  - One JSON object per line. Keys are the 24 SQL column names (same
    order as CSV via a deterministic key list; v1 uses
    `json.Marshal` on a `*orderedmap`-equivalent slice-of-pairs
    wrapper so order is stable across runs).
  - `human_intervention_count` is emitted as a JSON number; everything
    else is a string (matches the DB's TEXT storage).
  - Empty DB → zero lines, exit 0. There is no JSONL "header" concept;
    the empty output is the correct empty representation. The CSV
    "header only" guarantee from acceptance §9 only applies to CSV.

### 4.3 Exit codes

- `0` — success (even on empty DB).
- `1` — runtime error reading from DB or writing to output (wrapped
  error printed to stderr).
- `2` — flag parsing error (missing required flag, unknown format,
  unknown sub-flag).

### 4.4 Database open mode

The CLI opens the SQLite file in **read-only** mode (`mode=ro` in the
DSN; SQLite read-only open implicitly disables journal updates, so no
explicit `_journal=OFF` is needed). This is defence-in-depth: the
export tool has no business mutating the observer DB, and a typo'd
`--db` pointing at an in-use writable DB would otherwise risk lock
contention with the live observer process. If the file does not exist,
SQLite read-only open fails fast with a clear error → exit 1.

---

## 5. Schema-drift guard

Triggered in `NewSQLWriter`. Goal: detect "DDL upgraded, binary not
rebuilt" or "binary upgraded, DB not migrated" before any row lands in
a stale or unexpected schema.

Algorithm:

1. `PRAGMA table_info(runs)` is executed via the supplied `*sql.DB`.
   The driver returns one row per existing column, with the columns:
   `cid` (declaration order, 0-indexed), `name`, `type`, `notnull`
   (0/1), `dflt_value` (string or NULL), `pk` (0 or N for the Nth PK
   column).
2. The guard collects these rows into an ordered slice of column
   descriptors `[]columnDesc{cid, name, type, notnull, dfltValue, pk}`
   sorted by `cid` (PRAGMA returns sorted, but the guard re-sorts
   defensively).
3. If the slice is empty (no rows returned) → return
   `fmt.Errorf("%w: runs table missing — apply observerstore/schema.sql first",
   ErrSchemaDrift)`.
4. The expected descriptor list is a hard-coded constant
   `expectedColumns = []columnDesc{...}` of length **24** in the same
   order as §3, each entry naming `cid`, `name`, `type` (one of
   `TEXT`/`INTEGER`), `notnull`, `dfltValue`, `pk` (1 for `run_id`, 0
   otherwise).
5. Comparison is **element-wise on the ordered slices**:
   - Length mismatch → `ErrSchemaDrift` with message
     `expected N columns, got M`.
   - For each position `i`: compare `(cid, name, normalisedType,
     notnull, dfltValue, pk)`. Including `cid` belt-and-suspenders the
     ordering check: if a future SQLite version ever returned
     out-of-order PRAGMA rows, the position-`i` comparison would still
     catch the drift even before the defensive re-sort kicks in.
     `normalisedType` is the upper-case SQLite type
     affinity (`TEXT`, `INTEGER`); empty type strings map to
     `BLOB` per SQLite's affinity rules and trigger drift unless
     expected. The first position that differs is reported with the
     specific mismatched fields in the wrapped error message.
6. The sentinel returned is always `ErrSchemaDrift`; the human-readable
   prefix names the offending column and exact field. Output is
   deterministic across SQLite versions because the descriptor source
   is `PRAGMA table_info`, which is stable across the SQLite 3.x line
   that ships with `modernc.org/sqlite v1.50.0` (the driver
   `internal/observerstore` already uses; confirmed in §1).

Why descriptors and not just names: a downstream worktree that ADDs a
column (e.g. WT-1-capability-snapshot promoting `capability_snapshot_hash`
to `NOT NULL` with no default) would otherwise silently pass a
name-only check but break inserts at runtime. Ordered descriptors
catch the change at writer construction. The check still does NOT
verify CHECK-constraint text (SQLite does not expose CHECK clauses
via `PRAGMA table_info`); the runtime CHECK on
`success_oracle_result` remains the last line of defence for that.

---

## 6. Acceptance criteria (mirror of prompt §验收)

1. `go test ./internal/evalrun/... ./cmd/evalrun-export/... -count=1 -shuffle=on -race`
   passes.
2. `go vet ./...` clean across the touched packages.
3. `gofmt -l internal/evalrun cmd/evalrun-export internal/observerstore`
   prints nothing.
4. `go build -o /tmp/evalrun-export ./cmd/evalrun-export` succeeds.
5. The Phase 1 row's smoke command
   `evalrun-export --format=csv` (no `--db`) MUST work against an
   empty seeded DB. The accepted source for the DB path is, in order:
   the `--db` flag if non-empty; otherwise the `OBSERVER_DB`
   environment variable. The smoke test in this spec therefore runs:

   ```bash
   OBSERVER_DB=/tmp/empty.db /tmp/evalrun-export --format=csv
   ```

   against a `/tmp/empty.db` that has had `observerstore/schema.sql`
   applied. Result: prints exactly **24 column names** as a single
   header row in the §3 order, exit code 0. The equivalent explicit
   form `/tmp/evalrun-export --format=csv --db /tmp/empty.db` MUST
   produce byte-identical output. Calling `evalrun-export --format=csv`
   with neither `--db` nor `OBSERVER_DB` set is a usage error (exit 2,
   stderr names the missing source) — this is intentional, since
   "guess the DB path" failures would silently export an empty file
   that looks valid.
6. The final commit message includes the trailer
   `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
7. **Do not push.**

---

## 7. Security mitigations (prompt §安全段)

### (a) Parameterised SQL only — NO concatenation

The `INSERT INTO runs ...` statement is a `const` string with literal `?`
placeholders. Every value is bound via `db.ExecContext(ctx, insertSQL,
args...)`. There are zero call sites of `fmt.Sprintf` or string `+` that
produce SQL inside `internal/evalrun/`. The `evalrun-export` CLI's
`--filter-experiment` value is bound the same way: the SELECT is
`SELECT * FROM runs WHERE experiment_id = ? ORDER BY run_id` (or
`SELECT * FROM runs ORDER BY run_id` when the filter is empty). A
`vet` pass with the `sqlclosecheck` analyzer is sufficient to keep this
property, but the code review checklist for this worktree (plan §test
matrix) explicitly forbids fmt-built SQL.

The threat is not theoretical: `workload_id`, `failure_category`,
`machine_topology`, and `selected_context` all carry operator-supplied
strings and may contain `'`, `--`, or `;` legitimately (a free-form
machine topology might literally say "1 driver; 3 slaves").

### (b) `artifact_hashes` validated as sha256 hex

Every entry MUST match `^[a-f0-9]{64}$` (compiled once into a package-level
`*regexp.Regexp`). Rejecting non-hex values prevents downstream auditors
who reverse-lookup an artifact by hash from being misled by a path or
free-form string accidentally landing in the field. Empty string is NOT
a valid entry (it would round-trip as `"\"\""` inside the JSON array and
violate the same audit contract); a nil/empty SLICE is allowed and
stored as `[]`.

### (c) NoObserver ablation MUST NOT silently drop

`DisableTelemetry == true` runs validation, skips the DB write, AND
emits `[ablation] NoObserver: dropped run_id=<id>` to the standard
`log` output. This line is the audit pointer that lets a post-hoc
investigator answer "which runs are absent because the ablation was on,
and which are absent because the DB was offline?" Without it, an
operator who enables `--ablation NoObserver` by accident on a 60-run
sweep would discover the loss only by noticing an empty CSV — the
prompt's named worst-case failure mode. The §7 (c) test in the plan
asserts on the log line via `log.SetOutput(&bytes.Buffer{})`.

### (d) Schema drift detected at writer construction

`NewSQLWriter` calls the §5 guard before returning a Writer. The
writer's `INSERT` is explicit-column form
(`INSERT INTO runs (run_id, workload_id, ...) VALUES (?, ?, ...)`),
so SQLite would actually REJECT a row whose column list does not match
the DB — but the rejection arrives one INSERT at a time, mid-eval,
once the runner has already invested setup work. The §5 guard fires at
writer construction, before any row is attempted, so a stale-binary /
stale-DB pairing is surfaced at process start instead of at row 1 of
a 60-run sweep. The bigger risk the guard catches is a downstream
worktree silently ADDing a column with no Go-side awareness: the
INSERT would still succeed (the new column has a DDL default), the
operator would assume the row is complete, and the new column's value
would never be populated — invisibly skewing analysis. Ordered
descriptor comparison (§5) catches that too.

### (e) CSV cell prefix escape (CWE-1236)

Any cell whose first byte is `=`, `+`, `-`, `@`, `\t`, `\r`, or `\n`
is prefixed with `'` before being written. Excel and LibreOffice Calc
interpret a leading `=` as a formula; an unescaped
`WorkloadID = "=cmd|' /C calc'!A0"` would execute arbitrary code on
the reviewer's laptop. The prefix is added during CSV serialisation
only — the underlying DB row is unchanged, so a JSONL export of the
same row carries the raw value. Reviewers consuming JSONL never feed
it to a formula engine, so the asymmetry is correct.

### (f) Field-format and length caps

- `RunID`: `^[A-Za-z0-9_-]{8,128}$`. Lower bound 8 keeps the PRIMARY
  KEY meaningfully unique; upper bound 128 bounds index size and
  rejects pathologically long IDs. Excluding `/` and `.` blocks any
  attempt to encode a relative path into the run ID (which observer
  tooling later joins onto disk paths under `tests/eval/runs/`).
- Every other string field has a max length of **8 KiB**
  (`8 * 1024` bytes). Beyond that → `ErrOversizedField`. 8 KiB is two
  orders of magnitude above the longest legitimate value; the cap is a
  DoS-via-large-row backstop, not a normal-usage constraint. Operators
  who genuinely need a 9 KiB `MachineTopology` should encode it as an
  artifact and put the sha256 in `ArtifactHashes`.

---

## 8. Out of scope for this worktree

These items are explicitly NOT delivered here; downstream worktrees own
them:

- CLI binding `--ablation NoObserver` on any consumer binary — owned by
  Phase 2 `WT-2-flag-integration`.
- Population of `CapabilitySnapshotHash` from real snapshots — owned by
  `WT-1-capability-snapshot`.
- Population of `TaskContractHash` from real contracts — owned by
  `WT-1-contract-schema`.
- Population of `DynamicMCPRegistryHash` — owned by
  `WT-2-driver-promotion-chain`.
- The end-to-end runner that produces one row per workload — owned by
  `WT-1-eval-runner-skeleton`.
- A Postgres mirror of the `runs` DDL in
  `internal/observerstore/postgres/schema.sql` — out of scope; the eval
  pipeline targets SQLite.
- Any metric computation reading the `runs` table — owned by
  `WT-2-metric-extract`.

---

## 9. Files this worktree creates or modifies

Created:

- `multi-agent/internal/evalrun/schema.go`
- `multi-agent/internal/evalrun/writer.go`
- `multi-agent/internal/evalrun/schema_test.go`
- `multi-agent/internal/evalrun/writer_test.go`
- `multi-agent/cmd/evalrun-export/main.go`
- `multi-agent/cmd/evalrun-export/main_test.go` (CLI integration tests)

Modified (append only):

- `multi-agent/internal/observerstore/schema.sql` (DDL block at end)

No other file under `multi-agent/` is touched.
