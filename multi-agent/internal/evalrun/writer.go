package evalrun

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Writer is the per-run sink for D1 evaluation rows. Implementations
// must be safe for single-goroutine use; concurrent use is not part of
// the contract (the eval-runner is single-threaded). Ablation flags
// (e.g. DisableTelemetry on the default SQLWriter) are implementation
// concerns — the interface contract is "validate then write or drop".
type Writer interface {
	// Insert validates s and either writes one row or — at the
	// implementation's discretion under ablation — drops the row.
	// Validation MUST run in both modes so ablations cannot mask
	// schema violations.
	Insert(ctx context.Context, s Schema) error
	// Close releases writer-side resources. The default SQLWriter is a
	// no-op — the *sql.DB is owned by the caller and MUST be closed
	// separately. Callers that program to this interface MUST NOT rely
	// on Close to clean up underlying connections.
	Close() error
}

// SQLWriter writes Schema rows to the runs table of an *sql.DB. The
// schema-drift guard fires at construction; see spec §5.
type SQLWriter struct {
	db *sql.DB
}

// columnDesc describes one SQLite column for the schema-drift check.
// Field set matches PRAGMA table_info exactly: cid, name, type,
// notnull, dflt_value, pk. See spec §5.
type columnDesc struct {
	cid     int
	name    string
	sqlType string // upper-cased
	notNull bool
	dflt    sql.NullString
	pk      int // 0 or N for the Nth PK column
}

// expectedColumns is the source of truth: 24 ordered descriptors that
// match the §3 DDL and the canonical column order. Any future column
// change must update this list AND the schema.sql DDL AND the Insert
// statement column list below in lock-step.
var expectedColumns = []columnDesc{
	{cid: 0, name: "run_id", sqlType: "TEXT", notNull: false, dflt: sql.NullString{}, pk: 1},
	{cid: 1, name: "workload_id", sqlType: "TEXT", notNull: true, dflt: sql.NullString{}, pk: 0},
	{cid: 2, name: "claim_id", sqlType: "TEXT", notNull: true, dflt: sql.NullString{}, pk: 0},
	{cid: 3, name: "experiment_id", sqlType: "TEXT", notNull: true, dflt: sql.NullString{}, pk: 0},
	{cid: 4, name: "baseline_or_ablation", sqlType: "TEXT", notNull: true, dflt: sql.NullString{}, pk: 0},
	{cid: 5, name: "loom_commit", sqlType: "TEXT", notNull: true, dflt: sql.NullString{}, pk: 0},
	{cid: 6, name: "agentserver_commit", sqlType: "TEXT", notNull: true, dflt: sql.NullString{}, pk: 0},
	{cid: 7, name: "modelserver_commit", sqlType: "TEXT", notNull: true, dflt: sql.NullString{}, pk: 0},
	{cid: 8, name: "app_commit", sqlType: "TEXT", notNull: true, dflt: sql.NullString{}, pk: 0},
	{cid: 9, name: "machine_topology", sqlType: "TEXT", notNull: true, dflt: sql.NullString{}, pk: 0},
	{cid: 10, name: "context_ground_truth", sqlType: "TEXT", notNull: true, dflt: sql.NullString{}, pk: 0},
	{cid: 11, name: "capability_snapshot_hash", sqlType: "TEXT", notNull: true, dflt: sql.NullString{String: "''", Valid: true}, pk: 0},
	{cid: 12, name: "task_contract_hash", sqlType: "TEXT", notNull: true, dflt: sql.NullString{String: "''", Valid: true}, pk: 0},
	{cid: 13, name: "dynamic_mcp_registry_hash", sqlType: "TEXT", notNull: true, dflt: sql.NullString{String: "''", Valid: true}, pk: 0},
	{cid: 14, name: "selected_context", sqlType: "TEXT", notNull: true, dflt: sql.NullString{}, pk: 0},
	{cid: 15, name: "ground_truth_context", sqlType: "TEXT", notNull: true, dflt: sql.NullString{}, pk: 0},
	{cid: 16, name: "start_time", sqlType: "TEXT", notNull: true, dflt: sql.NullString{}, pk: 0},
	{cid: 17, name: "end_time", sqlType: "TEXT", notNull: true, dflt: sql.NullString{}, pk: 0},
	{cid: 18, name: "success_oracle_result", sqlType: "TEXT", notNull: true, dflt: sql.NullString{}, pk: 0},
	{cid: 19, name: "failure_category", sqlType: "TEXT", notNull: true, dflt: sql.NullString{String: "''", Valid: true}, pk: 0},
	{cid: 20, name: "human_intervention_count", sqlType: "INTEGER", notNull: true, dflt: sql.NullString{String: "0", Valid: true}, pk: 0},
	{cid: 21, name: "artifact_hashes", sqlType: "TEXT", notNull: true, dflt: sql.NullString{String: "'[]'", Valid: true}, pk: 0},
	{cid: 22, name: "observer_trace_path", sqlType: "TEXT", notNull: true, dflt: sql.NullString{String: "''", Valid: true}, pk: 0},
	{cid: 23, name: "model_trace_id", sqlType: "TEXT", notNull: true, dflt: sql.NullString{String: "''", Valid: true}, pk: 0},
}

// insertSQL is a const so Go's vet sql-style checks see it as a literal
// and so a code reviewer can grep for the ONLY SQL string that mutates
// runs. The column list and the `?` placeholders mirror
// expectedColumns 1:1 in order. CHANGING this constant requires
// updating expectedColumns AND schema.sql in lock-step.
const insertSQL = `INSERT INTO runs (
	run_id, workload_id, claim_id, experiment_id, baseline_or_ablation,
	loom_commit, agentserver_commit, modelserver_commit, app_commit,
	machine_topology, context_ground_truth, capability_snapshot_hash,
	task_contract_hash, dynamic_mcp_registry_hash, selected_context,
	ground_truth_context, start_time, end_time, success_oracle_result,
	failure_category, human_intervention_count, artifact_hashes,
	observer_trace_path, model_trace_id
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// ColumnNames returns the canonical ordered list of 24 column names
// (sourced from the same expectedColumns slice the drift guard uses).
// Exported so out-of-package callers (notably cmd/evalrun-export) can
// pin their CSV header / SELECT projection to the same SOT, ruling
// out the "test passes because both sides updated identically" trap.
// Returns a fresh copy each call.
func ColumnNames() []string {
	out := make([]string, len(expectedColumns))
	for i, c := range expectedColumns {
		out[i] = c.name
	}
	return out
}

// NewSQLWriter wraps *sql.DB into a Writer that targets the runs table.
// Returns ErrSchemaDrift (wrapped) if the runs table is absent or its
// column descriptors do not match the expected 24-column layout.
func NewSQLWriter(db *sql.DB) (Writer, error) {
	if err := CheckSchemaDrift(db); err != nil {
		return nil, err
	}
	return &SQLWriter{db: db}, nil
}

// Insert (the SQLWriter implementation) validates s, then either
// writes one row OR — when the package-level DisableTelemetry is
// true — logs a `[ablation] NoObserver: dropped run_id=<id>` audit
// line and returns nil. Validation runs in BOTH modes so the
// NoObserver ablation cannot mask schema violations (spec §7(c)).
// DisableTelemetry is package state, not Writer state: alternative
// Writer implementations (mocks, in-memory) are not required to
// observe it.
func (w *SQLWriter) Insert(ctx context.Context, s Schema) error {
	// Repeat the init-time registration warning on first Insert so an
	// operator running with stderr suppressed at startup still sees
	// that NoObserver is effectively a no-op for THIS package.
	if initRegistrationErr != nil {
		initWarnOnce.Do(func() {
			log.Printf("evalrun: WARNING — NoObserver ablation is wired to a different *bool than evalrun.DisableTelemetry (init err: %v); --ablation NoObserver will NOT silence this writer", initRegistrationErr)
		})
	}
	if err := s.validate(); err != nil {
		return err
	}
	if DisableTelemetry {
		// Audit pointer — the only line a forensic operator can grep
		// for to learn which rows were dropped because of the ablation.
		log.Printf("[ablation] NoObserver: dropped run_id=%s", s.RunID)
		return nil
	}
	hashesJSON, err := encodeArtifactHashes(s.ArtifactHashes)
	if err != nil {
		// encodeArtifactHashes should never fail for validated input,
		// but if it does we surface the JSON error rather than coerce.
		return fmt.Errorf("evalrun: encode artifact_hashes: %w", err)
	}
	if _, err := w.db.ExecContext(ctx, insertSQL,
		s.RunID,
		s.WorkloadID,
		s.ClaimID,
		s.ExperimentID,
		s.BaselineOrAblation,
		s.LoomCommit,
		s.AgentserverCommit,
		s.ModelserverCommit,
		s.AppCommit,
		s.MachineTopology,
		s.ContextGroundTruth,
		s.CapabilitySnapshotHash,
		s.TaskContractHash,
		s.DynamicMCPRegistryHash,
		s.SelectedContext,
		s.GroundTruthContext,
		formatRFC3339NanoUTC(s.StartTime),
		formatRFC3339NanoUTC(s.EndTime),
		s.SuccessOracleResult,
		s.FailureCategory,
		s.HumanInterventionCount,
		hashesJSON,
		s.ObserverTracePath,
		s.ModelTraceID,
	); err != nil {
		return fmt.Errorf("evalrun: insert run_id=%s: %w", s.RunID, err)
	}
	return nil
}

// Close is a no-op; the caller owns the *sql.DB lifetime.
func (w *SQLWriter) Close() error { return nil }

// formatRFC3339NanoUTC normalises t to UTC and serialises with a
// FIXED 9-digit nanosecond field. Two reasons we don't use
// time.RFC3339Nano:
//  1. RFC3339Nano truncates trailing-zero nanoseconds, producing
//     mixed forms like "...12:00:00Z" and "...12:00:00.5Z" that
//     SORT INCONSISTENTLY as lexicographic strings (the temporally
//     LATER ".5Z" string actually sorts BEFORE the integer-second
//     "Z" form because '.' < 'Z' in ASCII). Downstream SQL
//     `ORDER BY start_time` and `BETWEEN` would silently mis-bucket
//     rows whose sole difference is fractional precision.
//  2. A fixed-width string makes equality + range scans behave
//     identically to chronological order, eliminating an entire
//     class of off-by-one bugs in metric extraction.
//
// UTC normalisation is mandatory: storing local-zoned times would
// make range comparisons subtly wrong across DST runs even with
// fixed precision.
func formatRFC3339NanoUTC(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
}

// encodeArtifactHashes serialises s as a JSON array of strings. A
// nil/empty slice serialises as the literal "[]" — never "null" —
// because downstream JSON_EXTRACT semantics on NULL are
// driver-dependent. See spec §2.3.
func encodeArtifactHashes(s []string) (string, error) {
	if len(s) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// decodeArtifactHashes parses the storage form back into a Go slice.
// "[]" → empty slice. The function is exported within the package for
// tests; it has no SQL surface.
func decodeArtifactHashes(raw string) ([]string, error) {
	if raw == "" || raw == "[]" {
		return []string{}, nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("evalrun: decode artifact_hashes: %w", err)
	}
	return out, nil
}

// validate enforces the §2.2 limits before any SQL exec. All errors
// wrap a sentinel so callers can errors.Is, and prefix the field name
// (or [<index>] for artifact slice entries) so the operator knows WHICH
// field failed without grepping the schema.
func (s Schema) validate() error {
	if !runIDRe.MatchString(s.RunID) {
		return fmt.Errorf("evalrun: run_id=%q does not match %s: %w", s.RunID, runIDRe.String(), ErrInvalidRunID)
	}
	// 8 KiB cap on every string field except run_id (run_id is bounded
	// by its own 128-byte regex limit and gets a sharper sentinel).
	type fieldCheck struct {
		name string
		val  string
	}
	for _, fc := range []fieldCheck{
		{"workload_id", s.WorkloadID},
		{"claim_id", s.ClaimID},
		{"experiment_id", s.ExperimentID},
		{"baseline_or_ablation", s.BaselineOrAblation},
		{"loom_commit", s.LoomCommit},
		{"agentserver_commit", s.AgentserverCommit},
		{"modelserver_commit", s.ModelserverCommit},
		{"app_commit", s.AppCommit},
		{"machine_topology", s.MachineTopology},
		{"context_ground_truth", s.ContextGroundTruth},
		{"capability_snapshot_hash", s.CapabilitySnapshotHash},
		{"task_contract_hash", s.TaskContractHash},
		{"dynamic_mcp_registry_hash", s.DynamicMCPRegistryHash},
		{"selected_context", s.SelectedContext},
		{"ground_truth_context", s.GroundTruthContext},
		{"success_oracle_result", s.SuccessOracleResult},
		{"failure_category", s.FailureCategory},
		{"observer_trace_path", s.ObserverTracePath},
		{"model_trace_id", s.ModelTraceID},
	} {
		if len(fc.val) > maxFieldBytes {
			return fmt.Errorf("evalrun: %s length %d > %d: %w", fc.name, len(fc.val), maxFieldBytes, ErrOversizedField)
		}
		// UTF-8 validity ensures CSV / JSONL exports stay symmetric
		// (encoding/csv preserves raw bytes; encoding/json silently
		// substitutes U+FFFD). Rejecting at write time means the two
		// exports always agree byte-for-byte on string content.
		if !utf8.ValidString(fc.val) {
			return fmt.Errorf("evalrun: %s contains invalid UTF-8: %w", fc.name, ErrInvalidUTF8)
		}
	}
	if _, ok := validOracleResults[s.SuccessOracleResult]; !ok {
		return fmt.Errorf("evalrun: success_oracle_result=%q: %w", s.SuccessOracleResult, ErrInvalidOracleResult)
	}
	// failure_category: enforces two coupled invariants.
	//   1. result/category compatibility: empty iff result == "pass".
	//      A pass-row must NOT carry a stale category; a non-pass row
	//      MUST carry one (use "unknown" / FailUnknown if the failure
	//      site is genuinely unclassifiable).
	//   2. taxonomy membership: when non-empty, the value must be one
	//      of the 11 stable observerstore.AllCategories() entries or
	//      the FailUnknown sentinel "unknown". Free-form strings like
	//      "FailNetwork" silently land in "other" buckets downstream
	//      and skew per-category aggregates in D4/D5/D8.
	if (s.SuccessOracleResult == "pass") != (s.FailureCategory == "") {
		return fmt.Errorf("evalrun: result=%q + failure_category=%q invariant violated (must be empty iff pass; use \"unknown\" for unclassifiable fail/timeout): %w",
			s.SuccessOracleResult, s.FailureCategory, ErrInvalidFailureCategory)
	}
	if !isAcceptedFailureCategory(s.FailureCategory) {
		return fmt.Errorf("evalrun: failure_category=%q is not in observerstore.AllCategories() (and not the \"unknown\" sentinel): %w",
			s.FailureCategory, ErrInvalidFailureCategory)
	}
	if s.StartTime.IsZero() {
		return fmt.Errorf("evalrun: start_time is zero: %w", ErrInvalidTime)
	}
	if s.EndTime.IsZero() {
		return fmt.Errorf("evalrun: end_time is zero: %w", ErrInvalidTime)
	}
	if len(s.ArtifactHashes) > maxArtifactHashes {
		return fmt.Errorf("evalrun: artifact_hashes length %d > %d: %w", len(s.ArtifactHashes), maxArtifactHashes, ErrTooManyArtifactHashes)
	}
	for i, h := range s.ArtifactHashes {
		if !sha256HexRe.MatchString(h) {
			return fmt.Errorf("evalrun: artifact_hashes[%d]=%q not sha256 hex: %w", i, h, ErrInvalidArtifactHash)
		}
	}
	return nil
}

// expectedCheckSubstring is the CHECK-constraint fragment that MUST
// appear in the verbatim CREATE TABLE SQL stored in sqlite_master.
// PRAGMA table_info does not surface CHECK clauses, so without this
// belt-and-braces lookup a drift that drops the success_oracle_result
// CHECK would pass the descriptor comparison silently — and any future
// caller that bypasses the Go-side oracle validation would write
// garbage that the DB would have rejected pre-drift. If a second
// CHECK constraint is ever added to the runs table, lift this to a
// []string at that time (not before — YAGNI).
const expectedCheckSubstring = "CHECK(success_oracle_result IN ('pass','fail','timeout'))"

// CheckSchemaDrift runs the §5 algorithm against db's runs table.
// Useful as both the writer-construction guard and the read-only
// CLI's startup guard: without an up-front check, a drifted DB
// crashes export mid-stream with a low-level driver error rather
// than refusing to start.
//
// Returns:
//   - nil if PRAGMA table_info(runs) returns a column list whose
//     ordered descriptor sequence exactly matches expectedColumns AND
//     the verbatim CREATE TABLE text in sqlite_master contains the
//     expectedCheckSubstring fragment.
//   - ErrSchemaDrift (wrapped) with a message naming the missing
//     table, mismatched position, missing CHECK clause, or column count.
func CheckSchemaDrift(db *sql.DB) error {
	rows, err := db.Query("PRAGMA table_info(runs)")
	if err != nil {
		return fmt.Errorf("evalrun: PRAGMA table_info(runs): %w", err)
	}
	defer rows.Close()
	var got []columnDesc
	for rows.Next() {
		var c columnDesc
		var notnullInt int
		if err := rows.Scan(&c.cid, &c.name, &c.sqlType, &notnullInt, &c.dflt, &c.pk); err != nil {
			return fmt.Errorf("evalrun: scan PRAGMA row: %w", err)
		}
		c.notNull = notnullInt != 0
		c.sqlType = strings.ToUpper(strings.TrimSpace(c.sqlType))
		got = append(got, c)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("evalrun: iterate PRAGMA rows: %w", err)
	}
	if len(got) == 0 {
		return fmt.Errorf("%w: runs table missing — apply observerstore/schema.sql first", ErrSchemaDrift)
	}
	// Defensive re-sort by cid (PRAGMA returns sorted, but the contract
	// is "ordered by cid" not "PRAGMA's return order").
	sort.Slice(got, func(i, j int) bool { return got[i].cid < got[j].cid })
	if len(got) != len(expectedColumns) {
		// Name the first column that is missing (expected list longer)
		// or unexpected (PRAGMA list longer) so the operator sees
		// WHICH column drifted, not just a count.
		var detail string
		if len(got) < len(expectedColumns) {
			missing := expectedColumns[len(got)].name
			// Cross-check: maybe ALL of `got` matches a non-prefix
			// subset of expected — surface the SET diff regardless.
			gotNames := map[string]struct{}{}
			for _, g := range got {
				gotNames[g.name] = struct{}{}
			}
			var allMissing []string
			for _, e := range expectedColumns {
				if _, ok := gotNames[e.name]; !ok {
					allMissing = append(allMissing, e.name)
				}
			}
			sort.Strings(allMissing)
			detail = fmt.Sprintf("missing column(s) %v (first by cid: %s)", allMissing, missing)
		} else {
			expectedNames := map[string]struct{}{}
			for _, e := range expectedColumns {
				expectedNames[e.name] = struct{}{}
			}
			var extras []string
			for _, g := range got {
				if _, ok := expectedNames[g.name]; !ok {
					extras = append(extras, g.name)
				}
			}
			sort.Strings(extras)
			detail = fmt.Sprintf("unexpected column(s) %v", extras)
		}
		return fmt.Errorf("%w: expected %d columns, got %d; %s", ErrSchemaDrift, len(expectedColumns), len(got), detail)
	}
	for i, want := range expectedColumns {
		g := got[i]
		// Compare cid, name, normalised type, notnull, dflt, pk.
		// `dflt` comparison is on (Valid, String) — Valid=false means
		// no default declared at all.
		if g.cid != want.cid {
			return fmt.Errorf("%w: column %d: cid mismatch (got %d, want %d)", ErrSchemaDrift, i, g.cid, want.cid)
		}
		if g.name != want.name {
			return fmt.Errorf("%w: column %d: name mismatch (got %q, want %q)", ErrSchemaDrift, i, g.name, want.name)
		}
		if g.sqlType != want.sqlType {
			return fmt.Errorf("%w: column %s: type mismatch (got %q, want %q)", ErrSchemaDrift, g.name, g.sqlType, want.sqlType)
		}
		if g.notNull != want.notNull {
			return fmt.Errorf("%w: column %s: notnull mismatch (got %v, want %v)", ErrSchemaDrift, g.name, g.notNull, want.notNull)
		}
		if g.dflt.Valid != want.dflt.Valid || g.dflt.String != want.dflt.String {
			return fmt.Errorf("%w: column %s: default mismatch (got %+v, want %+v)", ErrSchemaDrift, g.name, g.dflt, want.dflt)
		}
		if g.pk != want.pk {
			return fmt.Errorf("%w: column %s: pk mismatch (got %d, want %d)", ErrSchemaDrift, g.name, g.pk, want.pk)
		}
	}
	// CHECK-aware pass: sqlite_master.sql preserves the verbatim
	// CREATE TABLE statement text (verified against modernc.org/sqlite
	// v1.50.0). We string-search for each pinned CHECK fragment; this
	// is less brittle than a full canonical-form compare and catches
	// the drift mode the descriptor walk cannot see.
	var createSQL sql.NullString
	if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'runs'").Scan(&createSQL); err != nil {
		return fmt.Errorf("evalrun: read sqlite_master for runs: %w", err)
	}
	if !createSQL.Valid {
		return fmt.Errorf("%w: runs table CREATE SQL missing from sqlite_master", ErrSchemaDrift)
	}
	// Strip ALL whitespace from both haystack and needles before
	// substring match. `strings.Fields` only collapses runs to a
	// single space, so semantically-identical reformatting like
	// "CHECK ( success_oracle_result IN ('pass', 'fail', 'timeout') )"
	// (extra spaces inside parens, after commas) would false-positive
	// against the unspaced needle. Stripping all whitespace makes the
	// match invariant to any reformatting that doesn't change tokens.
	gotStripped := stripAllWhitespace(createSQL.String)
	wantStripped := stripAllWhitespace(expectedCheckSubstring)
	if !strings.Contains(gotStripped, wantStripped) {
		return fmt.Errorf("%w: missing CHECK clause %q in runs CREATE SQL", ErrSchemaDrift, expectedCheckSubstring)
	}
	return nil
}

// stripAllWhitespace drops every Unicode whitespace rune (uses
// unicode.IsSpace — catches ASCII space/tab/newline AND non-breaking
// space, ideographic space, line/paragraph separators). Used by the
// CHECK-clause drift cross-check so semantically-identical DDL
// reformatting (extra spaces around parens / commas, tabs vs spaces,
// newlines, copy-pasted NBSP from a wiki) does not trip a
// false-positive ErrSchemaDrift.
func stripAllWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if !unicode.IsSpace(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
