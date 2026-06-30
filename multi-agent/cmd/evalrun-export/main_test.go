package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// binPath is built once in TestMain and shared across the CLI exec
// tests. Avoids `go build` per test.
var binPath string

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "evalrun-export-bin-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(tmp)
	binPath = filepath.Join(tmp, "evalrun-export")
	cmd := exec.Command("go", "build", "-o", binPath, ".")
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout
	if err := cmd.Run(); err != nil {
		panic("go build evalrun-export: " + err.Error())
	}
	os.Exit(m.Run())
}

// schemaSQLPath walks up to find observerstore/schema.sql.
func schemaSQLPath(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := cwd
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			p := filepath.Join(dir, "internal", "observerstore", "schema.sql")
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
		dir = filepath.Dir(dir)
	}
	t.Fatalf("could not locate observerstore/schema.sql from cwd %q", cwd)
	return ""
}

// freshDB returns a tempdir SQLite DB with the runs schema applied.
func freshDB(t *testing.T) (string, *sql.DB) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "obs.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ddl, err := os.ReadFile(schemaSQLPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(ddl)); err != nil {
		t.Fatal(err)
	}
	return path, db
}

// insertSampleRow inserts one row using the same column ordering used
// by the production writer; tests use this so they don't have to
// import the internal package (cycle-free).
func insertSampleRow(t *testing.T, db *sql.DB, runID, experiment string, mutate func(map[string]any)) {
	t.Helper()
	row := map[string]any{
		"run_id":                    runID,
		"workload_id":               "wl-x",
		"claim_id":                  "C1",
		"experiment_id":             experiment,
		"baseline_or_ablation":      "full",
		"loom_commit":               "0000000000000000000000000000000000000001",
		"agentserver_commit":        "0000000000000000000000000000000000000002",
		"modelserver_commit":        "0000000000000000000000000000000000000003",
		"app_commit":                "0000000000000000000000000000000000000004",
		"machine_topology":          "linux/amd64",
		"context_ground_truth":      "(driver,x)",
		"capability_snapshot_hash":  "",
		"task_contract_hash":        "",
		"dynamic_mcp_registry_hash": "",
		"selected_context":          "(driver,x)",
		"ground_truth_context":      "(driver,x)",
		"start_time":                time.Now().UTC().Format(time.RFC3339Nano),
		"end_time":                  time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano),
		"success_oracle_result":     "pass",
		"failure_category":          "",
		"human_intervention_count":  0,
		"artifact_hashes":           "[]",
		"observer_trace_path":       "",
		"model_trace_id":            "",
	}
	if mutate != nil {
		mutate(row)
	}
	// Pre-built INSERT — same column ordering as production code.
	cols := []string{
		"run_id", "workload_id", "claim_id", "experiment_id", "baseline_or_ablation",
		"loom_commit", "agentserver_commit", "modelserver_commit", "app_commit",
		"machine_topology", "context_ground_truth", "capability_snapshot_hash",
		"task_contract_hash", "dynamic_mcp_registry_hash", "selected_context",
		"ground_truth_context", "start_time", "end_time", "success_oracle_result",
		"failure_category", "human_intervention_count", "artifact_hashes",
		"observer_trace_path", "model_trace_id",
	}
	args := make([]any, len(cols))
	for i, c := range cols {
		args[i] = row[c]
	}
	q := "INSERT INTO runs (" + strings.Join(cols, ", ") + ") VALUES (" +
		strings.TrimRight(strings.Repeat("?,", len(cols)), ",") + ")"
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

// runCmd execs the built binary with args + env, returns
// (stdout, stderr, exitCode).
func runCmd(t *testing.T, env []string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	// Inherit OS env minimally; only OBSERVER_DB is meaningful here.
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	err := cmd.Run()
	exit := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exit = ee.ExitCode()
		} else {
			t.Fatalf("exec %s: %v", binPath, err)
		}
	}
	return so.String(), se.String(), exit
}

// Test 17: TestExportCSV_EmptyDB_HeaderOnly.
func TestExportCSV_EmptyDB_HeaderOnly(t *testing.T) {
	path, _ := freshDB(t)
	stdout, stderr, exit := runCmd(t, nil, "--format=csv", "--db", path)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr)
	}
	// Single header line (csv writer appends \n).
	r := csv.NewReader(strings.NewReader(stdout))
	rec, err := r.Read()
	if err != nil {
		t.Fatalf("parse csv header: %v (stdout=%q)", err, stdout)
	}
	if len(rec) != 24 {
		t.Fatalf("header has %d fields, want 24: %v", len(rec), rec)
	}
	for i, want := range columnNames {
		if rec[i] != want {
			t.Errorf("col %d: got %q want %q", i, rec[i], want)
		}
	}
	// No second record.
	if _, err := r.Read(); err != io.EOF {
		t.Fatalf("expected EOF after header, got err=%v", err)
	}
}

// Test 18: TestExportCSV_SmokeCommandEnv.
func TestExportCSV_SmokeCommandEnv(t *testing.T) {
	path, _ := freshDB(t)
	// First: with --db.
	stdout1, _, exit1 := runCmd(t, nil, "--format=csv", "--db", path)
	if exit1 != 0 {
		t.Fatalf("with --db: exit=%d", exit1)
	}
	// Second: with OBSERVER_DB env.
	stdout2, _, exit2 := runCmd(t, []string{"OBSERVER_DB=" + path}, "--format=csv")
	if exit2 != 0 {
		t.Fatalf("with OBSERVER_DB: exit=%d", exit2)
	}
	if stdout1 != stdout2 {
		t.Fatalf("--db and OBSERVER_DB outputs differ:\n--db: %q\nenv:  %q", stdout1, stdout2)
	}
	// Third: no flag, no env → exit 2.
	_, stderr3, exit3 := runCmd(t, nil, "--format=csv")
	if exit3 != 2 {
		t.Fatalf("no db source: exit=%d (want 2)", exit3)
	}
	if !strings.Contains(stderr3, "OBSERVER_DB") {
		t.Fatalf("stderr must mention OBSERVER_DB: %q", stderr3)
	}
}

// Test 18a: --db beats OBSERVER_DB env.
func TestExportCSV_FlagBeatsEnv(t *testing.T) {
	pathA, dbA := freshDB(t)
	pathB, _ := freshDB(t)
	insertSampleRow(t, dbA, "run-A-12345678", "E1", nil) // A has 1 row
	// Env points to A (which has a row), --db points to B (which is empty).
	stdout, _, exit := runCmd(t, []string{"OBSERVER_DB=" + pathA}, "--format=csv", "--db", pathB)
	if exit != 0 {
		t.Fatalf("exit=%d", exit)
	}
	// Header only — used B, not A.
	r := csv.NewReader(strings.NewReader(stdout))
	if _, err := r.Read(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Read(); err != io.EOF {
		t.Fatalf("expected EOF after header (--db should win), got err=%v", err)
	}
}

// Test 18b: DB open failure → exit 1.
func TestExportCSV_DBOpenFailure_Exit1(t *testing.T) {
	_, stderr, exit := runCmd(t, nil, "--format=csv", "--db", "/nonexistent/dir/nope.db")
	if exit != 1 {
		t.Fatalf("exit=%d (want 1) stderr=%q", exit, stderr)
	}
	if !strings.Contains(stderr, "open db") {
		t.Fatalf("stderr must wrap open error: %q", stderr)
	}
}

// Test 18c: --out write failure → exit 1.
func TestExportCSV_OutFileWriteFailure_Exit1(t *testing.T) {
	path, _ := freshDB(t)
	_, stderr, exit := runCmd(t, nil, "--format=csv", "--db", path, "--out", "/nonexistent/dir/out.csv")
	if exit != 1 {
		t.Fatalf("exit=%d (want 1)", exit)
	}
	if !strings.Contains(stderr, "open --out") {
		t.Fatalf("stderr must mention --out failure: %q", stderr)
	}
}

// Test 18d: missing --format → exit 2.
func TestExportCSV_MissingFormat_Exit2(t *testing.T) {
	path, _ := freshDB(t)
	_, stderr, exit := runCmd(t, nil, "--db", path)
	if exit != 2 {
		t.Fatalf("exit=%d (want 2)", exit)
	}
	if !strings.Contains(stderr, "--format") {
		t.Fatalf("stderr must mention --format: %q", stderr)
	}
}

// Test 18e: unknown flag → exit 2.
func TestExportCSV_UnknownFlag_Exit2(t *testing.T) {
	path, _ := freshDB(t)
	_, stderr, exit := runCmd(t, nil, "--format=csv", "--db", path, "--bogus")
	if exit != 2 {
		t.Fatalf("exit=%d (want 2)", exit)
	}
	if !strings.Contains(stderr, "bogus") {
		t.Fatalf("stderr must mention bogus flag: %q", stderr)
	}
}

// Test 19: formula injection escape applied.
func TestExportCSV_FormulaInjectionEscaped(t *testing.T) {
	path, db := freshDB(t)
	insertSampleRow(t, db, "run-formula-12345", "E1", func(r map[string]any) {
		r["workload_id"] = "=cmd|'/C calc'!A0"
		r["machine_topology"] = "+evil"
		r["selected_context"] = "-x"
		r["observer_trace_path"] = "@bad"
		r["ground_truth_context"] = "\tlead"
		r["model_trace_id"] = "\rret"
		r["failure_category"] = "\nlf"
		r["success_oracle_result"] = "fail" // failure_category nonzero requires fail
	})
	stdout, _, exit := runCmd(t, nil, "--format=csv", "--db", path)
	if exit != 0 {
		t.Fatalf("exit=%d", exit)
	}
	rdr := csv.NewReader(strings.NewReader(stdout))
	rdr.FieldsPerRecord = -1 // tolerate the LF-prefixed field
	header, err := rdr.Read()
	if err != nil {
		t.Fatal(err)
	}
	row, err := rdr.Read()
	if err != nil {
		t.Fatalf("read data row: %v", err)
	}
	idx := func(name string) int {
		for i, c := range header {
			if c == name {
				return i
			}
		}
		t.Fatalf("col %s not in header %v", name, header)
		return -1
	}
	checks := map[string]string{
		"workload_id":          "'=cmd|'/C calc'!A0",
		"machine_topology":     "'+evil",
		"selected_context":     "'-x",
		"observer_trace_path":  "'@bad",
		"ground_truth_context": "'\tlead",
		"model_trace_id":       "'\rret",
		"failure_category":     "'\nlf",
	}
	for col, want := range checks {
		got := row[idx(col)]
		if got != want {
			t.Errorf("col %s: got %q want %q", col, got, want)
		}
	}
	// Underlying DB is untouched (escape is export-only).
	var raw string
	if err := db.QueryRow("SELECT workload_id FROM runs WHERE run_id = ?", "run-formula-12345").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != "=cmd|'/C calc'!A0" {
		t.Fatalf("DB cell mutated by escape: got %q", raw)
	}
}

// Test 20: empty string not prefixed.
func TestExportCSV_EmptyStringNotPrefixed(t *testing.T) {
	path, db := freshDB(t)
	insertSampleRow(t, db, "run-empty-12345678", "E1", nil) // failure_category = ""
	stdout, _, exit := runCmd(t, nil, "--format=csv", "--db", path)
	if exit != 0 {
		t.Fatalf("exit=%d", exit)
	}
	rdr := csv.NewReader(strings.NewReader(stdout))
	header, _ := rdr.Read()
	row, err := rdr.Read()
	if err != nil {
		t.Fatal(err)
	}
	for i, name := range header {
		if name == "failure_category" {
			if row[i] != "" {
				t.Fatalf("empty failure_category must export as empty (no prefix), got %q", row[i])
			}
			return
		}
	}
	t.Fatal("failure_category not in header")
}

// Test 21: empty DB JSONL → no rows.
func TestExportJSONL_EmptyDB_NoRows(t *testing.T) {
	path, _ := freshDB(t)
	stdout, _, exit := runCmd(t, nil, "--format=jsonl", "--db", path)
	if exit != 0 {
		t.Fatalf("exit=%d", exit)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("empty DB JSONL must emit no lines, got %q", stdout)
	}
}

// Test 22: JSONL roundtrip values and types.
func TestExportJSONL_RoundtripValues(t *testing.T) {
	path, db := freshDB(t)
	insertSampleRow(t, db, "run-jsonl-A-12345", "E1", func(r map[string]any) {
		r["human_intervention_count"] = 2
		r["artifact_hashes"] = `["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"]`
	})
	insertSampleRow(t, db, "run-jsonl-B-12345", "E2", nil)
	stdout, _, exit := runCmd(t, nil, "--format=jsonl", "--db", path)
	if exit != 0 {
		t.Fatalf("exit=%d", exit)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %v", len(lines), lines)
	}
	// Parse first line; assert key order matches columnNames and types.
	var raw map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &raw); err != nil {
		t.Fatal(err)
	}
	// human_intervention_count must be a number.
	if v, ok := raw["human_intervention_count"]; !ok {
		t.Fatal("missing human_intervention_count")
	} else if _, ok := v.(float64); !ok {
		t.Fatalf("human_intervention_count must be number, got %T", v)
	}
	// artifact_hashes must be a string (the raw JSON-array storage form).
	if v, ok := raw["artifact_hashes"]; !ok {
		t.Fatal("missing artifact_hashes")
	} else if _, ok := v.(string); !ok {
		t.Fatalf("artifact_hashes must be string, got %T", v)
	}
	// Key order: parse the raw JSON via a sequential decoder.
	dec := json.NewDecoder(strings.NewReader(lines[0]))
	// Expect '{' token.
	tok, _ := dec.Token()
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		t.Fatalf("first token must be `{`, got %v", tok)
	}
	for _, want := range columnNames {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("read key: %v", err)
		}
		k, ok := tok.(string)
		if !ok {
			t.Fatalf("expected string key, got %T %v", tok, tok)
		}
		if k != want {
			t.Fatalf("key order: got %q, want %q", k, want)
		}
		// Consume the value token (object/array would need recursion;
		// every value here is a scalar string or number).
		if _, err := dec.Token(); err != nil {
			t.Fatalf("read value for %q: %v", k, err)
		}
	}
}

// Test 23: --filter-experiment.
func TestExportCSV_FilterExperiment(t *testing.T) {
	path, db := freshDB(t)
	insertSampleRow(t, db, "run-A1-12345678", "E1", nil)
	insertSampleRow(t, db, "run-A2-12345678", "E1", nil)
	insertSampleRow(t, db, "run-B1-12345678", "E2", nil)
	stdout, _, exit := runCmd(t, nil, "--format=csv", "--db", path, "--filter-experiment", "E1")
	if exit != 0 {
		t.Fatalf("exit=%d", exit)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 1 header + 2 rows, got %d: %v", len(lines), lines)
	}
	// Unknown experiment → header only.
	stdout2, stderr2, exit2 := runCmd(t, nil, "--format=csv", "--db", path, "--filter-experiment", "Eunknown")
	if exit2 != 0 {
		t.Fatalf("unknown filter: exit=%d stderr=%q", exit2, stderr2)
	}
	lines2 := strings.Split(strings.TrimRight(stdout2, "\n"), "\n")
	if len(lines2) != 1 {
		t.Fatalf("unknown filter must yield header only, got %d lines: %v", len(lines2), lines2)
	}
}

// Test 24: filter-experiment SQL-injection attempt.
func TestExportCSV_FilterExperiment_SQLInjection(t *testing.T) {
	path, db := freshDB(t)
	insertSampleRow(t, db, "run-X1-12345678", "E1", nil)
	insertSampleRow(t, db, "run-X2-12345678", "E2", nil)
	stdout, _, exit := runCmd(t, nil, "--format=csv", "--db", path, "--filter-experiment", "E1' OR 1=1 --")
	if exit != 0 {
		t.Fatalf("exit=%d", exit)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("injection attempt must match nothing (header only), got %d lines", len(lines))
	}
	// Integrity check on the DB.
	var ok string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&ok); err != nil {
		t.Fatal(err)
	}
	if ok != "ok" {
		t.Fatalf("PRAGMA integrity_check = %q, want ok", ok)
	}
}

// Test 25: --format=xml → exit 2.
func TestExportCSV_BadFormat_ExitCode2(t *testing.T) {
	path, _ := freshDB(t)
	_, stderr, exit := runCmd(t, nil, "--format=xml", "--db", path)
	if exit != 2 {
		t.Fatalf("exit=%d (want 2)", exit)
	}
	if !strings.Contains(stderr, "csv") || !strings.Contains(stderr, "jsonl") {
		t.Fatalf("stderr must mention legal values: %q", stderr)
	}
}

// Test 26: --out writes to file.
func TestExportCSV_OutFile(t *testing.T) {
	path, _ := freshDB(t)
	outPath := filepath.Join(t.TempDir(), "out.csv")
	stdout, _, exit := runCmd(t, nil, "--format=csv", "--db", path, "--out", outPath)
	if exit != 0 {
		t.Fatalf("exit=%d", exit)
	}
	if stdout != "" {
		t.Fatalf("--out should suppress stdout, got %q", stdout)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "run_id") {
		t.Fatalf("out file missing header: %q", string(data))
	}
	st, _ := os.Stat(outPath)
	mode := st.Mode().Perm()
	if mode != 0644 {
		t.Fatalf("filemode %v want 0644", mode)
	}
}

// silence unused-import warnings if a test is removed mid-edit.
var _ = context.Background
