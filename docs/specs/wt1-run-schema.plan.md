# WT-1-run-schema — Plan

> Companion to `docs/specs/wt1-run-schema.spec.md` (CLEAN as of codex
> round 3). Spec is the source of truth; this file enumerates the test
> matrix, layering, and verification commands.

---

## 1. Order of work (TDD)

1. Append the §3 DDL to `multi-agent/internal/observerstore/schema.sql`.
   This is the only modification outside `internal/evalrun/` and
   `cmd/evalrun-export/`; landing it first lets every Go test apply the
   real shipped schema rather than a fixture.
2. Add `multi-agent/internal/evalrun/schema.go`: the `Schema` struct
   (spec §2.1), the sentinel errors (§2.2), the package-level
   `DisableTelemetry` bool, the `init()` ablation registration (§2.4),
   and the validation helpers (regex compilation, 8 KiB cap).
3. Write `internal/evalrun/schema_test.go` covering all
   validation-only test cases below — these run without a DB.
4. Add `multi-agent/internal/evalrun/writer.go`: `Writer` interface, the
   `SQLWriter` constructor with §5 drift guard, the column-list
   constant, the `Insert` and `Close` implementations.
5. Write `internal/evalrun/writer_test.go` covering all DB-touching
   cases below (each test opens a fresh `:memory:` SQLite DB,
   applies `observerstore/schema.sql`, then runs).
6. Add `multi-agent/cmd/evalrun-export/main.go`: flag parsing
   (§4.1), DB open in read-only mode (§4.4), CSV / JSONL serialisation
   (§4.2), formula escape (§7 (e)), exit codes (§4.3).
7. Write `multi-agent/cmd/evalrun-export/main_test.go` covering CLI
   integration cases below.

Each numbered test in §2 is one `go test` function. The plan keeps
tests one-per-concern so a regression points at one assertion.

---

## 2. Test matrix

Test names are normative. Reviewers grep for them.

| # | Test name | Package | Validates | Spec security item |
|---|---|---|---|---|
| 1 | `TestSchema_AllFieldsRoundtrip` | `evalrun` | All 24 fields filled with the §2.1 sample values, `Insert` → `SELECT * FROM runs WHERE run_id = ?`, every column comes back byte-equal to what was inserted (times compared via `t.Equal`, slice compared via `reflect.DeepEqual`). | base contract |
| 2 | `TestInsert_Parameterized_SQLInjection` | `evalrun` | `Schema.WorkloadID = "x'); DROP TABLE runs;--"` and a second pass with `Schema.MachineTopology = "'; DELETE FROM runs;--"`. `Insert` returns nil, `SELECT COUNT(*) FROM runs` returns the expected count, and `PRAGMA table_info(runs)` still returns 24 rows. | (a) |
| 3 | `TestInsert_RejectsBadArtifactHash` | `evalrun` | Three sub-cases via `t.Run`: hash with non-hex byte (`"ZZ..."`), hash of length 63, hash that is the empty string. All three → `errors.Is(err, ErrInvalidArtifactHash)` AND error message contains `artifact_hashes[<i>]`. | (b) |
| 4 | `TestInsert_RejectsBadRunIDFormat` | `evalrun` | Sub-cases: `RunID` containing `/`, containing `..`, length 7, length 129, containing whitespace, containing `'`. All → `errors.Is(err, ErrInvalidRunID)`. | (f) |
| 5 | `TestInsert_RejectsOversizedField` | `evalrun` | Sub-cases: `WorkloadID = strings.Repeat("a", 8*1024+1)`; same with `MachineTopology`; same with `SelectedContext`. All → `errors.Is(err, ErrOversizedField)` AND the error message names the field. The boundary value `8*1024` is accepted. | (f) |
| 6 | `TestInsert_RejectsBadOracleResult` | `evalrun` | `SuccessOracleResult = "PASS"` (wrong case), `= "ok"`, `= ""`. All → `errors.Is(err, ErrInvalidOracleResult)`. The three legal values pass. | base contract |
| 7 | `TestInsert_RejectsZeroTime` | `evalrun` | `StartTime = time.Time{}` or `EndTime = time.Time{}` → `errors.Is(err, ErrInvalidTime)`. | base contract |
| 8 | `TestNoObserver_DroppedRunLogged` | `evalrun` | Set `DisableTelemetry = true`, capture `log` output via `log.SetOutput(buf)`, call `Insert`. Assertions: `Insert` returns nil, `buf.String()` contains exactly `[ablation] NoObserver: dropped run_id=<id>`, `SELECT COUNT(*) FROM runs` returns 0. Resets `DisableTelemetry = false` in `t.Cleanup`. | (c) |
| 9 | `TestNoObserver_StillRejectsInvalid` | `evalrun` | With `DisableTelemetry = true`, an invalid row (bad RunID) still returns `ErrInvalidRunID` — ablation MUST NOT bypass validation. | (c) extension |
| 10 | `TestNewSQLWriter_DetectsSchemaDriftMissingColumn` | `evalrun` | Pre-create `runs` table with only 23 of 24 columns (drop the last column). `NewSQLWriter` returns `errors.Is(err, ErrSchemaDrift)` and the error message names the missing column. | (d) |
| 11 | `TestNewSQLWriter_DetectsSchemaDriftExtraColumn` | `evalrun` | Pre-create `runs` table with 25 columns (add `extra_col TEXT`). `NewSQLWriter` returns `errors.Is(err, ErrSchemaDrift)`. | (d) |
| 12 | `TestNewSQLWriter_DetectsSchemaDriftWrongType` | `evalrun` | Pre-create `runs` with `human_intervention_count` as `TEXT` instead of `INTEGER`. `NewSQLWriter` returns `errors.Is(err, ErrSchemaDrift)`. | (d) extension |
| 12a | `TestNewSQLWriter_DetectsSchemaDriftWrongNotNull` | `evalrun` | Pre-create `runs` where `workload_id` is nullable (`TEXT` with no `NOT NULL`). `NewSQLWriter` returns `errors.Is(err, ErrSchemaDrift)`. | (d) descriptor: notnull |
| 12b | `TestNewSQLWriter_DetectsSchemaDriftWrongDefault` | `evalrun` | Pre-create `runs` where `failure_category` has no `DEFAULT ''` (the descriptor `dflt_value` becomes NULL). `NewSQLWriter` returns `errors.Is(err, ErrSchemaDrift)`. | (d) descriptor: dflt |
| 12c | `TestNewSQLWriter_DetectsSchemaDriftWrongPK` | `evalrun` | Pre-create `runs` with no PRIMARY KEY (every column with `pk=0`). `NewSQLWriter` returns `errors.Is(err, ErrSchemaDrift)`. | (d) descriptor: pk |
| 12d | `TestNewSQLWriter_DetectsSchemaDriftWrongName` | `evalrun` | Pre-create `runs` with all 24 columns but rename `model_trace_id` to `model_trace`. `NewSQLWriter` returns `errors.Is(err, ErrSchemaDrift)` and the error message names the mismatched position. | (d) descriptor: name |
| 12e | `TestNewSQLWriter_DetectsSchemaDriftWrongOrder` | `evalrun` | Pre-create `runs` with all 24 column names present but `start_time` and `end_time` declaration order swapped. `NewSQLWriter` returns `errors.Is(err, ErrSchemaDrift)` (the position-`i` comparison catches the swap). | (d) descriptor: order/cid |
| 13 | `TestNewSQLWriter_DetectsSchemaDriftMissingTable` | `evalrun` | Fresh DB, no `runs` table. `NewSQLWriter` returns `errors.Is(err, ErrSchemaDrift)` and error message contains `runs table missing`. | (d) |
| 14 | `TestNewSQLWriter_CleanSchemaPasses` | `evalrun` | DB with `observerstore/schema.sql` applied. `NewSQLWriter` returns a non-nil Writer and nil error. | base contract |
| 15 | `TestRegisteredOnAblation` | `evalrun` | `ablation.Default.List()` contains `ablation.NoObserver` (the package's `init` ran before the test). | (c) integration |
| 16 | `TestArtifactHashes_RoundtripJSON` | `evalrun` | Nil slice → stored as `[]`, retrieved as `[]string(nil)` or empty slice. Empty slice → same. Slice of 3 hashes → exact roundtrip. Confirms §2.3 nil→`[]` mapping. | base contract |
| 17 | `TestExportCSV_EmptyDB_HeaderOnly` | `cmd/evalrun-export` (main_test) | Apply `observerstore/schema.sql` to a temp DB, no rows. Run `evalrun-export --format=csv --db <tmp>` via `cmd/exec.Command` against the built binary in `t.TempDir()`. Assertions: exit 0, stdout is exactly one line that, when parsed as CSV, has 24 fields equal to the §3 column list in order. | acceptance §6 |
| 18 | `TestExportCSV_SmokeCommandEnv` | `cmd/evalrun-export` | Set `OBSERVER_DB=<tmp>` env, run `evalrun-export --format=csv` (no `--db`). Assertions: exit 0, output byte-identical to test 17's. Reverse case: no env, no `--db` → exit 2, stderr names the missing source. | acceptance §6 |
| 18a | `TestExportCSV_FlagBeatsEnv` | `cmd/evalrun-export` | Create two DBs `A.db` (1 row) and `B.db` (0 rows). Set `OBSERVER_DB=A.db`, pass `--db B.db`. Assertions: exit 0, exactly the 24-col header (i.e. used `B.db`). Confirms `--db` precedence over env per spec §4.1. | acceptance §6 |
| 18b | `TestExportCSV_DBOpenFailure_Exit1` | `cmd/evalrun-export` | `--db /nonexistent/path.db --format=csv`. Assertions: exit 1, stderr contains the wrapped open error. | §4.3 |
| 18c | `TestExportCSV_OutFileWriteFailure_Exit1` | `cmd/evalrun-export` | `--out /nonexistent-dir/out.csv` with a valid `--db`. Assertions: exit 1, stderr names the write error. | §4.3 |
| 18d | `TestExportCSV_MissingFormat_Exit2` | `cmd/evalrun-export` | No `--format`. Assertions: exit 2, stderr mentions `--format` is required. | §4.3 |
| 18e | `TestExportCSV_UnknownFlag_Exit2` | `cmd/evalrun-export` | Pass `--bogus` along with valid flags. Assertions: exit 2 (Go's `flag` package returns 2 for unknown flags), stderr mentions the unknown flag. | §4.3 |
| 19 | `TestExportCSV_FormulaInjectionEscaped` | `cmd/evalrun-export` | Insert one row whose `WorkloadID = "=cmd|'/C calc'!A0"`, `MachineTopology = "+evil"`, `SelectedContext = "-x"`, `ObserverTracePath = "@bad"`, `GroundTruthContext = "\tlead"`, `ModelTraceID = "\rret"`, `FailureCategory = "\nlf"`. Run `--format=csv`. Assertions: each affected cell starts with `'`; the underlying DB SELECT returns the unprefixed values (escape is export-only). | (e) |
| 20 | `TestExportCSV_EmptyStringNotPrefixed` | `cmd/evalrun-export` | A row with `FailureCategory = ""` (pass case) exports `""` not `"'"`. | (e) edge |
| 21 | `TestExportJSONL_EmptyDB_NoRows` | `cmd/evalrun-export` | Empty DB, `--format=jsonl`. Assertions: exit 0, stdout is empty (0 lines). | acceptance §6 |
| 22 | `TestExportJSONL_RoundtripValues` | `cmd/evalrun-export` | Insert 2 rows, run `--format=jsonl`. Each line parses as JSON, key order matches §4.2, `human_intervention_count` is a JSON number, `artifact_hashes` is a string (the JSON-array string from the DB). | acceptance §6 |
| 23 | `TestExportCSV_FilterExperiment` | `cmd/evalrun-export` | Insert 3 rows with `experiment_id ∈ {E1, E1, E2}`. Run with `--filter-experiment E1`. Stdout has header + 2 rows. `--filter-experiment Eunknown` returns header + 0 rows. | CLI |
| 24 | `TestExportCSV_FilterExperiment_SQLInjection` | `cmd/evalrun-export` | `--filter-experiment "E1' OR 1=1 --"`. Assertions: exit 0, stdout has header + 0 rows (the literal string matches nothing), `PRAGMA integrity_check` still returns `ok`. | (a) via CLI |
| 25 | `TestExportCSV_BadFormat_ExitCode2` | `cmd/evalrun-export` | `--format=xml` → exit 2, stderr mentions the legal values. | CLI |
| 26 | `TestExportCSV_OutFile` | `cmd/evalrun-export` | `--out /tmp/foo.csv` writes to file, no stdout. Filemode 0644. | CLI |

Coverage cross-check against spec security items:

- (a) parameterised SQL — tests 2, 24.
- (b) artifact_hashes sha256 — test 3.
- (c) NoObserver drop logged — tests 8, 9; (c) integration — test 15.
- (d) schema drift — tests 10, 11, 12, 12a, 12b, 12c, 12d, 12e, 13.
- (e) CSV formula escape — tests 19, 20.
- (f) field format & length caps — tests 4, 5.

Coverage cross-check against 08-line roundtrip:

- Test 1 (`TestSchema_AllFieldsRoundtrip`) writes one row that touches
  all 24 fields and reads them all back. Test 17
  (`TestExportCSV_EmptyDB_HeaderOnly`) re-asserts the column order on
  the empty-DB path. Together they pin the spec §2.1 ↔ DDL §3 ↔
  expected-column list §5 to a single ordered set of 24 names.

---

## 3. Implementation notes (TDD scaffolding)

- Compile the sha256 hex regex once at package init:
  `var sha256HexRe = regexp.MustCompile("^[a-f0-9]{64}$")`. Tests 3
  and 16 share it.
- The run-ID regex similarly:
  `var runIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)`.
- The `insertSQL` constant lists all 24 columns in §3 order; the same
  ordered name list is reused for the `expectedColumns` descriptor
  array in `NewSQLWriter` to avoid two sources of truth.
- The schema-drift expected descriptor list, the `insertSQL` column
  list, the `Schema` struct field order, and the CSV header in
  `cmd/evalrun-export` MUST all derive from one ordered constant
  (call it `runColumns []runColumn{name, sqlType, notnull, dflt, pk}`).
  Any future column add must update one location.
- The CSV writer is `csv.NewWriter(out)` with default comma; flush
  after the last row and check `w.Error()`.
- The formula-escape helper:
  `func csvEscape(s string) string { if s == "" { return s }; switch s[0] { case '=', '+', '-', '@', '\t', '\r', '\n': return "'" + s }; return s }`.
  Tested by test 19 + 20.
- `log` usage: `log.Printf` with no prefix so `log.SetOutput` capture in
  test 8 sees just the `[ablation] NoObserver: dropped run_id=<id>`
  body plus standard date/time prefix. The assertion uses
  `strings.Contains`, not equality, so the date/time prefix does not
  destabilise the test.
- Test isolation: every test uses `t.TempDir()` for the DB path; the
  SQLite DSN is `file:<path>?_pragma=busy_timeout=5000`. Tests do NOT
  share a DB.
- The CLI integration tests use the convention from
  `internal/observerstore/store_test.go` of building the binary into
  `t.TempDir()` via `go build -o` invoked through `os/exec.Command`
  in `TestMain`. This avoids relying on a pre-built `/tmp/evalrun-export`.

---

## 4. Tooling commands

Per spec §6:

```bash
cd multi-agent
go test ./internal/evalrun/... ./cmd/evalrun-export/... -count=1 -shuffle=on -race
go vet ./...
gofmt -l internal/evalrun cmd/evalrun-export internal/observerstore
go build -o /tmp/evalrun-export ./cmd/evalrun-export
# Smoke (spec §6 acceptance #5):
sqlite3 /tmp/empty.db < internal/observerstore/schema.sql
OBSERVER_DB=/tmp/empty.db /tmp/evalrun-export --format=csv | head -2
/tmp/evalrun-export --format=csv --db /tmp/empty.db | head -2  # byte-identical
```

All five MUST exit 0 / print no output / produce the 24-column header
exactly. The two header invocations MUST be byte-identical.

---

## 5. Out-of-scope reminders (mirror of spec §8)

- No CLI binding `--ablation NoObserver` on any consumer binary
  (Phase 2 WT-2-flag-integration).
- No Postgres mirror DDL.
- No population of the three placeholder hash columns by real consumers.
- No metric extraction reading `runs`.

If a reviewer asks about any of these, point to spec §8.
