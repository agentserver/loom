// evalrun-export reads the observer SQLite DB and exports the runs
// table as CSV or JSONL. See docs/specs/wt1-run-schema.spec.md §4.
package main

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"

	"github.com/yourorg/multi-agent/internal/evalrun"
	_ "modernc.org/sqlite"
)

// columnNames is the 24-column header in the order defined by
// docs/specs/wt1-run-schema.spec.md §3. The CLI is intentionally
// dependency-free with respect to internal/evalrun beyond this list —
// it does NOT call into the Writer; it just reads the runs table.
var columnNames = []string{
	"run_id",
	"workload_id",
	"claim_id",
	"experiment_id",
	"baseline_or_ablation",
	"loom_commit",
	"agentserver_commit",
	"modelserver_commit",
	"app_commit",
	"machine_topology",
	"context_ground_truth",
	"capability_snapshot_hash",
	"task_contract_hash",
	"dynamic_mcp_registry_hash",
	"selected_context",
	"ground_truth_context",
	"start_time",
	"end_time",
	"success_oracle_result",
	"failure_category",
	"human_intervention_count",
	"artifact_hashes",
	"observer_trace_path",
	"model_trace_id",
}

// usageError is exit code 2 — bad arguments. runtimeError is exit 1.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

type runtimeError struct{ err error }

func (e *runtimeError) Error() string { return e.err.Error() }
func (e *runtimeError) Unwrap() error { return e.err }

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		var ue *usageError
		if errors.As(err, &ue) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

// run is the testable core. stdout/stderr injection lets the
// integration tests capture output without exec'ing the binary every
// time (though main_test.go does also build + run the binary for
// genuine end-to-end coverage).
func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("evalrun-export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		format           = fs.String("format", "", "Output format: csv or jsonl (required)")
		outPath          = fs.String("out", "-", "Output path; '-' or empty means stdout")
		dbFlag           = fs.String("db", "", "SQLite path (defaults to env OBSERVER_DB)")
		filterExperiment = fs.String("filter-experiment", "", "Restrict export to rows where experiment_id = <value>")
	)
	if err := fs.Parse(args); err != nil {
		// flag.ContinueOnError prints its own message; wrap as usage.
		return &usageError{msg: "evalrun-export: " + err.Error()}
	}
	if *format != "csv" && *format != "jsonl" {
		return &usageError{msg: "evalrun-export: --format must be csv or jsonl (got " + strconv.Quote(*format) + ")"}
	}
	dbPath := *dbFlag
	if dbPath == "" {
		dbPath = os.Getenv("OBSERVER_DB")
	}
	if dbPath == "" {
		return &usageError{msg: "evalrun-export: --db or OBSERVER_DB env must be set"}
	}
	db, err := openReadOnly(dbPath)
	if err != nil {
		return &runtimeError{err: fmt.Errorf("open db %q: %w", dbPath, err)}
	}
	defer db.Close()
	// Run the same schema-drift guard the writer does — without it
	// a drifted DB would crash export mid-stream with a low-level
	// driver error (e.g. "converting NULL to int is unsupported" if
	// human_intervention_count's NOT NULL constraint was dropped),
	// producing a half-truncated output file. The check is cheap and
	// gives the operator a clear "refresh the schema, then retry"
	// message at start instead.
	if err := evalrun.CheckSchemaDrift(db); err != nil {
		return &runtimeError{err: fmt.Errorf("schema check on %q: %w", dbPath, err)}
	}
	// Output target. We explicitly close --out files AFTER serialising
	// and report Close errors — silently swallowing them would let a
	// late write/buffer-flush failure (e.g. disk full) return success.
	var (
		out      io.Writer = stdout
		outFile  *os.File
		closeOut func() error = func() error { return nil }
	)
	if *outPath != "" && *outPath != "-" {
		f, err := os.OpenFile(*outPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
		if err != nil {
			return &runtimeError{err: fmt.Errorf("open --out %q: %w", *outPath, err)}
		}
		outFile = f
		out = f
		closeOut = f.Close
	}
	switch *format {
	case "csv":
		if err := exportCSV(context.Background(), db, out, *filterExperiment); err != nil {
			_ = closeOut()
			return &runtimeError{err: err}
		}
	case "jsonl":
		if err := exportJSONL(context.Background(), db, out, *filterExperiment); err != nil {
			_ = closeOut()
			return &runtimeError{err: err}
		}
	}
	if outFile != nil {
		if err := closeOut(); err != nil {
			return &runtimeError{err: fmt.Errorf("close --out %q: %w", *outPath, err)}
		}
	}
	return nil
}

// openReadOnly opens the SQLite DB in read-only mode. Defence in depth:
// the export tool has no business mutating observer state.
func openReadOnly(path string) (*sql.DB, error) {
	q := url.Values{}
	q.Set("mode", "ro")
	q.Set("_pragma", "busy_timeout=5000")
	sep := "?"
	for _, r := range path {
		if r == '?' {
			sep = "&"
			break
		}
	}
	dsn := "file:" + path + sep + q.Encode()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// Ping to catch "file does not exist" before downstream code.
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// selectRunsSQL is the only SELECT against runs. The optional
// experiment filter is parameterised — never string-concatenated.
const (
	selectRunsAllSQL    = `SELECT ` + columnSelectList + ` FROM runs ORDER BY run_id`
	selectRunsFilterSQL = `SELECT ` + columnSelectList + ` FROM runs WHERE experiment_id = ? ORDER BY run_id`
)

// columnSelectList is the 24-column projection in the canonical order.
// Constant so the SELECT statements above are pure literals; refactors
// that touch column order must update this AND the Insert path AND
// schema.sql in lock-step.
const columnSelectList = `run_id, workload_id, claim_id, experiment_id, baseline_or_ablation,
loom_commit, agentserver_commit, modelserver_commit, app_commit,
machine_topology, context_ground_truth, capability_snapshot_hash,
task_contract_hash, dynamic_mcp_registry_hash, selected_context,
ground_truth_context, start_time, end_time, success_oracle_result,
failure_category, human_intervention_count, artifact_hashes,
observer_trace_path, model_trace_id`

// queryRuns runs the appropriate SELECT and returns *sql.Rows.
func queryRuns(ctx context.Context, db *sql.DB, filterExperiment string) (*sql.Rows, error) {
	if filterExperiment == "" {
		return db.QueryContext(ctx, selectRunsAllSQL)
	}
	return db.QueryContext(ctx, selectRunsFilterSQL, filterExperiment)
}

// humanCountIdx is the canonical position of human_intervention_count
// in the 24-column ordering. The CSV path treats this column like the
// others (string round-trip via strconv); the JSONL path emits it as
// a JSON number.
const humanCountIdx = 20

// runRow holds one scanned row as 24 string cells. human_intervention_count
// is the only non-string column; it's converted to its decimal string
// form in scanOneRow so CSV/JSONL writers can decide whether to quote
// or emit as a number.
type runRow struct {
	values [24]string
}

func scanOneRow(rows *sql.Rows) (*runRow, error) {
	var (
		r runRow
		// int64 (not int) so a row written with
		// human_intervention_count > 2^31 on a 32-bit build doesn't
		// silently truncate to a negative number. SQLite INTEGER is
		// up to 8 bytes signed; the column NOT NULL DEFAULT 0 means
		// Scan never sees NULL — drift guard catches the NULL-able
		// drift variant separately.
		humanCount int64
		// 24 dest pointers in the canonical order.
		dest [24]any
	)
	for i := 0; i < 24; i++ {
		if i == humanCountIdx {
			dest[i] = &humanCount
		} else {
			dest[i] = &r.values[i]
		}
	}
	if err := rows.Scan(dest[:]...); err != nil {
		return nil, err
	}
	r.values[humanCountIdx] = strconv.FormatInt(humanCount, 10)
	return &r, nil
}

// exportCSV writes one header row + N data rows. Each cell is run
// through csvEscape to guard against formula injection (CWE-1236).
func exportCSV(ctx context.Context, db *sql.DB, w io.Writer, filterExperiment string) error {
	rows, err := queryRuns(ctx, db, filterExperiment)
	if err != nil {
		return fmt.Errorf("query runs: %w", err)
	}
	defer rows.Close()
	csvW := csv.NewWriter(w)
	if err := csvW.Write(columnNames); err != nil {
		return fmt.Errorf("write header: %w", err)
	}
	for rows.Next() {
		r, err := scanOneRow(rows)
		if err != nil {
			return fmt.Errorf("scan row: %w", err)
		}
		escaped := make([]string, 24)
		for i, v := range r.values {
			escaped[i] = csvEscape(v)
		}
		if err := csvW.Write(escaped); err != nil {
			return fmt.Errorf("write row: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate rows: %w", err)
	}
	csvW.Flush()
	if err := csvW.Error(); err != nil {
		return fmt.Errorf("flush csv: %w", err)
	}
	return nil
}

// csvEscape prefixes a cell with `'` if its first byte is one of the
// seven formula / control characters Excel/LibreOffice Calc may treat
// as a formula trigger or a cell-content shifter (=, +, -, @, \t, \r,
// \n). See spec §7 (e).
func csvEscape(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r', '\n':
		return "'" + s
	}
	return s
}

// exportJSONL writes one JSON object per line. Key order matches
// columnNames; human_intervention_count is emitted as a number, all
// other fields as strings.
func exportJSONL(ctx context.Context, db *sql.DB, w io.Writer, filterExperiment string) error {
	rows, err := queryRuns(ctx, db, filterExperiment)
	if err != nil {
		return fmt.Errorf("query runs: %w", err)
	}
	defer rows.Close()
	enc := json.NewEncoder(w)
	for rows.Next() {
		r, err := scanOneRow(rows)
		if err != nil {
			return fmt.Errorf("scan row: %w", err)
		}
		// Build ordered key/value pairs by hand so the JSON object
		// preserves columnNames order. Using a map would give us
		// alphabetical ordering, which would scramble the schema.
		obj := orderedJSONLine{values: r.values}
		if err := enc.Encode(obj); err != nil {
			return fmt.Errorf("encode row: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate rows: %w", err)
	}
	return nil
}

// orderedJSONLine custom-marshals one row preserving columnNames order
// and emitting human_intervention_count as a JSON number.
type orderedJSONLine struct {
	values [24]string
}

func (o orderedJSONLine) MarshalJSON() ([]byte, error) {
	buf := make([]byte, 0, 1024)
	buf = append(buf, '{')
	for i, name := range columnNames {
		if i > 0 {
			buf = append(buf, ',')
		}
		// key
		keyJSON, err := json.Marshal(name)
		if err != nil {
			return nil, err
		}
		buf = append(buf, keyJSON...)
		buf = append(buf, ':')
		// value
		if i == humanCountIdx {
			// human_intervention_count: numeric. The string was built
			// via strconv.Itoa in scanOneRow, so direct append is safe.
			buf = append(buf, []byte(o.values[i])...)
		} else {
			vJSON, err := json.Marshal(o.values[i])
			if err != nil {
				return nil, err
			}
			buf = append(buf, vJSON...)
		}
	}
	buf = append(buf, '}')
	return buf, nil
}
