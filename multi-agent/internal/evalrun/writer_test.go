package evalrun

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// schemaSQLPath returns the absolute path to the shipped
// observerstore/schema.sql so every test applies the actual production
// DDL (avoids two sources of truth).
func schemaSQLPath(t *testing.T) string {
	t.Helper()
	// Walk up until we find go.mod, then the observerstore path is fixed.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := cwd
	for i := 0; i < 6; i++ {
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

// freshDB opens a tempdir-backed SQLite DB and applies
// observerstore/schema.sql (so the full runs table descriptor is real).
func freshDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "obs.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ddl, err := os.ReadFile(schemaSQLPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(ddl)); err != nil {
		t.Fatalf("apply schema.sql: %v", err)
	}
	return db
}

// newTestWriter is the shared constructor used by both schema_test.go
// and writer_test.go. Returns the Writer concrete type wrapping db.
func newTestWriter(t *testing.T) (*SQLWriter, *sql.DB) {
	t.Helper()
	db := freshDB(t)
	w, err := NewSQLWriter(db)
	if err != nil {
		t.Fatalf("NewSQLWriter: %v", err)
	}
	sw, ok := w.(*SQLWriter)
	if !ok {
		t.Fatalf("NewSQLWriter returned %T, want *SQLWriter", w)
	}
	return sw, db
}

// getDB exposes the wrapped *sql.DB to tests that need direct SQL.
func getDB(_ *testing.T, w *SQLWriter) *sql.DB { return w.db }

// freshEmptyDB returns a SQLite DB with NO schema applied — used by
// drift tests so the caller controls exactly what runs looks like.
func freshEmptyDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "obs.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// Test 1: TestSchema_AllFieldsRoundtrip.
func TestSchema_AllFieldsRoundtrip(t *testing.T) {
	w, db := newTestWriter(t)
	s := sampleSchema()
	// Make all three placeholder hashes non-empty to confirm they
	// round-trip through DEFAULT '' columns when given values.
	s.CapabilitySnapshotHash = "csh-" + strings.Repeat("0", 60)
	s.TaskContractHash = "tch-" + strings.Repeat("0", 60)
	s.DynamicMCPRegistryHash = "dmr-" + strings.Repeat("0", 60)
	s.HumanInterventionCount = 3
	s.FailureCategory = ""
	if err := w.Insert(context.Background(), s); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	// Read back every column in the §3 order and compare.
	cols := strings.Join(expectedColumnNames, ", ")
	row := db.QueryRow("SELECT "+cols+" FROM runs WHERE run_id = ?", s.RunID)
	var (
		runID, workloadID, claimID, experimentID, baseline, loomCommit,
		agentCommit, modelCommit, appCommit, machineTopo, ctxGT,
		capHash, contractHash, dynRegHash, selCtx, gtCtx,
		startStr, endStr, oracle, failCat, artifactJSON,
		obsTrace, modelTrace string
		humanCount int
	)
	if err := row.Scan(
		&runID, &workloadID, &claimID, &experimentID, &baseline,
		&loomCommit, &agentCommit, &modelCommit, &appCommit, &machineTopo,
		&ctxGT, &capHash, &contractHash, &dynRegHash, &selCtx, &gtCtx,
		&startStr, &endStr, &oracle, &failCat, &humanCount, &artifactJSON,
		&obsTrace, &modelTrace,
	); err != nil {
		t.Fatalf("scan: %v", err)
	}
	check := func(name, got, want string) {
		t.Helper()
		if got != want {
			t.Errorf("%s: got %q, want %q", name, got, want)
		}
	}
	check("run_id", runID, s.RunID)
	check("workload_id", workloadID, s.WorkloadID)
	check("claim_id", claimID, s.ClaimID)
	check("experiment_id", experimentID, s.ExperimentID)
	check("baseline_or_ablation", baseline, s.BaselineOrAblation)
	check("loom_commit", loomCommit, s.LoomCommit)
	check("agentserver_commit", agentCommit, s.AgentserverCommit)
	check("modelserver_commit", modelCommit, s.ModelserverCommit)
	check("app_commit", appCommit, s.AppCommit)
	check("machine_topology", machineTopo, s.MachineTopology)
	check("context_ground_truth", ctxGT, s.ContextGroundTruth)
	check("capability_snapshot_hash", capHash, s.CapabilitySnapshotHash)
	check("task_contract_hash", contractHash, s.TaskContractHash)
	check("dynamic_mcp_registry_hash", dynRegHash, s.DynamicMCPRegistryHash)
	check("selected_context", selCtx, s.SelectedContext)
	check("ground_truth_context", gtCtx, s.GroundTruthContext)
	check("success_oracle_result", oracle, s.SuccessOracleResult)
	check("failure_category", failCat, s.FailureCategory)
	check("observer_trace_path", obsTrace, s.ObserverTracePath)
	check("model_trace_id", modelTrace, s.ModelTraceID)
	if humanCount != s.HumanInterventionCount {
		t.Errorf("human_intervention_count: got %d, want %d", humanCount, s.HumanInterventionCount)
	}
	// Times: stored as RFC3339Nano UTC.
	wantStart := s.StartTime.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	wantEnd := s.EndTime.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	check("start_time", startStr, wantStart)
	check("end_time", endStr, wantEnd)
	// Artifact hashes round-trip through JSON.
	gotHashes, err := decodeArtifactHashes(artifactJSON)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotHashes, s.ArtifactHashes) {
		t.Errorf("artifact_hashes: got %v, want %v", gotHashes, s.ArtifactHashes)
	}
}

// Test 2: TestInsert_Parameterized_SQLInjection.
func TestInsert_Parameterized_SQLInjection(t *testing.T) {
	w, db := newTestWriter(t)
	cases := []struct {
		name   string
		mutate func(*Schema)
	}{
		{"workload_id", func(s *Schema) { s.WorkloadID = "x'); DROP TABLE runs;--" }},
		{"machine_topology", func(s *Schema) { s.MachineTopology = "'; DELETE FROM runs;--" }},
		{"failure_category", func(s *Schema) {
			s.SuccessOracleResult = "fail"
			s.FailureCategory = "fc'); DROP TABLE runs;--"
		}},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := sampleSchema()
			s.RunID = "run-inj-" + c.name + "-1234"
			c.mutate(&s)
			if err := w.Insert(context.Background(), s); err != nil {
				t.Fatalf("Insert: %v", err)
			}
			// Table still exists (24 columns) and row count increases.
			var colCount int
			if err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('runs')").Scan(&colCount); err != nil {
				t.Fatal(err)
			}
			if colCount != 24 {
				t.Fatalf("runs table mutated: column count %d (want 24)", colCount)
			}
			var rowCount int
			if err := db.QueryRow("SELECT COUNT(*) FROM runs").Scan(&rowCount); err != nil {
				t.Fatal(err)
			}
			if rowCount != i+1 {
				t.Fatalf("row count %d (want %d)", rowCount, i+1)
			}
		})
	}
}

// Test 10: schema drift — missing column.
func TestNewSQLWriter_DetectsSchemaDriftMissingColumn(t *testing.T) {
	db := freshEmptyDB(t)
	// Create runs with only 23 columns (drop model_trace_id).
	if _, err := db.Exec(`CREATE TABLE runs (
		run_id TEXT PRIMARY KEY,
		workload_id TEXT NOT NULL,
		claim_id TEXT NOT NULL,
		experiment_id TEXT NOT NULL,
		baseline_or_ablation TEXT NOT NULL,
		loom_commit TEXT NOT NULL,
		agentserver_commit TEXT NOT NULL,
		modelserver_commit TEXT NOT NULL,
		app_commit TEXT NOT NULL,
		machine_topology TEXT NOT NULL,
		context_ground_truth TEXT NOT NULL,
		capability_snapshot_hash TEXT NOT NULL DEFAULT '',
		task_contract_hash TEXT NOT NULL DEFAULT '',
		dynamic_mcp_registry_hash TEXT NOT NULL DEFAULT '',
		selected_context TEXT NOT NULL,
		ground_truth_context TEXT NOT NULL,
		start_time TEXT NOT NULL,
		end_time TEXT NOT NULL,
		success_oracle_result TEXT NOT NULL,
		failure_category TEXT NOT NULL DEFAULT '',
		human_intervention_count INTEGER NOT NULL DEFAULT 0,
		artifact_hashes TEXT NOT NULL DEFAULT '[]',
		observer_trace_path TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		t.Fatal(err)
	}
	_, err := NewSQLWriter(db)
	if !errors.Is(err, ErrSchemaDrift) {
		t.Fatalf("want ErrSchemaDrift, got %v", err)
	}
	if !strings.Contains(err.Error(), "model_trace_id") {
		t.Fatalf("error must name the missing column model_trace_id: %v", err)
	}
}

// Test 11: schema drift — extra column.
func TestNewSQLWriter_DetectsSchemaDriftExtraColumn(t *testing.T) {
	db := freshEmptyDB(t)
	// Apply real DDL then add a column.
	ddl, err := os.ReadFile(schemaSQLPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(ddl)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE runs ADD COLUMN extra_col TEXT`); err != nil {
		t.Fatal(err)
	}
	_, err = NewSQLWriter(db)
	if !errors.Is(err, ErrSchemaDrift) {
		t.Fatalf("want ErrSchemaDrift, got %v", err)
	}
}

// Test 12: schema drift — wrong type.
func TestNewSQLWriter_DetectsSchemaDriftWrongType(t *testing.T) {
	db := freshEmptyDB(t)
	if _, err := db.Exec(driftDDL(map[string]string{
		"human_intervention_count": "TEXT NOT NULL DEFAULT '0'",
	})); err != nil {
		t.Fatal(err)
	}
	_, err := NewSQLWriter(db)
	if !errors.Is(err, ErrSchemaDrift) {
		t.Fatalf("want ErrSchemaDrift, got %v", err)
	}
}

// Test 12a: drift — wrong notnull.
func TestNewSQLWriter_DetectsSchemaDriftWrongNotNull(t *testing.T) {
	db := freshEmptyDB(t)
	if _, err := db.Exec(driftDDL(map[string]string{
		"workload_id": "TEXT", // dropped NOT NULL
	})); err != nil {
		t.Fatal(err)
	}
	_, err := NewSQLWriter(db)
	if !errors.Is(err, ErrSchemaDrift) {
		t.Fatalf("want ErrSchemaDrift, got %v", err)
	}
}

// Test 12b: drift — wrong default.
func TestNewSQLWriter_DetectsSchemaDriftWrongDefault(t *testing.T) {
	db := freshEmptyDB(t)
	if _, err := db.Exec(driftDDL(map[string]string{
		"failure_category": "TEXT NOT NULL", // dropped DEFAULT ''
	})); err != nil {
		t.Fatal(err)
	}
	_, err := NewSQLWriter(db)
	if !errors.Is(err, ErrSchemaDrift) {
		t.Fatalf("want ErrSchemaDrift, got %v", err)
	}
}

// Test 12c: drift — wrong PK.
func TestNewSQLWriter_DetectsSchemaDriftWrongPK(t *testing.T) {
	db := freshEmptyDB(t)
	if _, err := db.Exec(driftDDL(map[string]string{
		"run_id": "TEXT NOT NULL", // dropped PRIMARY KEY
	})); err != nil {
		t.Fatal(err)
	}
	_, err := NewSQLWriter(db)
	if !errors.Is(err, ErrSchemaDrift) {
		t.Fatalf("want ErrSchemaDrift, got %v", err)
	}
}

// Test 12d: drift — wrong column name (renamed position 23).
func TestNewSQLWriter_DetectsSchemaDriftWrongName(t *testing.T) {
	db := freshEmptyDB(t)
	if _, err := db.Exec(driftDDL(map[string]string{
		"model_trace_id__RENAMED_TO": "model_trace TEXT NOT NULL DEFAULT ''",
	})); err != nil {
		t.Fatal(err)
	}
	_, err := NewSQLWriter(db)
	if !errors.Is(err, ErrSchemaDrift) {
		t.Fatalf("want ErrSchemaDrift, got %v", err)
	}
	if !strings.Contains(err.Error(), "model_trace") {
		t.Fatalf("error must mention the renamed column: %v", err)
	}
}

// Test 12e: drift — wrong order (swap start_time and end_time).
func TestNewSQLWriter_DetectsSchemaDriftWrongOrder(t *testing.T) {
	db := freshEmptyDB(t)
	// Build a DDL where end_time and start_time positions are swapped.
	if _, err := db.Exec(driftDDLSwapOrder("start_time", "end_time")); err != nil {
		t.Fatal(err)
	}
	_, err := NewSQLWriter(db)
	if !errors.Is(err, ErrSchemaDrift) {
		t.Fatalf("want ErrSchemaDrift, got %v", err)
	}
}

// Test 13: schema drift — table missing.
func TestNewSQLWriter_DetectsSchemaDriftMissingTable(t *testing.T) {
	db := freshEmptyDB(t)
	_, err := NewSQLWriter(db)
	if !errors.Is(err, ErrSchemaDrift) {
		t.Fatalf("want ErrSchemaDrift, got %v", err)
	}
	if !strings.Contains(err.Error(), "runs table missing") {
		t.Fatalf("error must say 'runs table missing': %v", err)
	}
}

// Test 14: clean schema passes.
func TestNewSQLWriter_CleanSchemaPasses(t *testing.T) {
	db := freshDB(t)
	w, err := NewSQLWriter(db)
	if err != nil {
		t.Fatalf("NewSQLWriter on clean schema: %v", err)
	}
	if w == nil {
		t.Fatal("Writer must be non-nil")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// driftDDL builds a `CREATE TABLE runs (...)` statement matching the
// real schema except for the overrides map. `overrides[col] = "..."`
// replaces the column declaration body for that column. To rename a
// column, set key = "<oldname>__RENAMED_TO" and value =
// "<newname> <decl>".
func driftDDL(overrides map[string]string) string {
	type col struct {
		name string
		decl string
	}
	cols := []col{
		{"run_id", "TEXT PRIMARY KEY"},
		{"workload_id", "TEXT NOT NULL"},
		{"claim_id", "TEXT NOT NULL"},
		{"experiment_id", "TEXT NOT NULL"},
		{"baseline_or_ablation", "TEXT NOT NULL"},
		{"loom_commit", "TEXT NOT NULL"},
		{"agentserver_commit", "TEXT NOT NULL"},
		{"modelserver_commit", "TEXT NOT NULL"},
		{"app_commit", "TEXT NOT NULL"},
		{"machine_topology", "TEXT NOT NULL"},
		{"context_ground_truth", "TEXT NOT NULL"},
		{"capability_snapshot_hash", "TEXT NOT NULL DEFAULT ''"},
		{"task_contract_hash", "TEXT NOT NULL DEFAULT ''"},
		{"dynamic_mcp_registry_hash", "TEXT NOT NULL DEFAULT ''"},
		{"selected_context", "TEXT NOT NULL"},
		{"ground_truth_context", "TEXT NOT NULL"},
		{"start_time", "TEXT NOT NULL"},
		{"end_time", "TEXT NOT NULL"},
		{"success_oracle_result", "TEXT NOT NULL"},
		{"failure_category", "TEXT NOT NULL DEFAULT ''"},
		{"human_intervention_count", "INTEGER NOT NULL DEFAULT 0"},
		{"artifact_hashes", "TEXT NOT NULL DEFAULT '[]'"},
		{"observer_trace_path", "TEXT NOT NULL DEFAULT ''"},
		{"model_trace_id", "TEXT NOT NULL DEFAULT ''"},
	}
	parts := make([]string, 0, len(cols))
	for _, c := range cols {
		// Renaming: a key "<oldname>__RENAMED_TO" replaces the WHOLE
		// "name decl" pair with the override value.
		if v, ok := overrides[c.name+"__RENAMED_TO"]; ok {
			parts = append(parts, v)
			continue
		}
		if v, ok := overrides[c.name]; ok {
			parts = append(parts, c.name+" "+v)
			continue
		}
		parts = append(parts, c.name+" "+c.decl)
	}
	return "CREATE TABLE runs (" + strings.Join(parts, ", ") + ")"
}

// driftDDLSwapOrder builds the same CREATE TABLE with columns a and b
// swapped in declaration order.
func driftDDLSwapOrder(a, b string) string {
	// Start with the clean ordered list and swap.
	cols := []string{
		"run_id TEXT PRIMARY KEY",
		"workload_id TEXT NOT NULL",
		"claim_id TEXT NOT NULL",
		"experiment_id TEXT NOT NULL",
		"baseline_or_ablation TEXT NOT NULL",
		"loom_commit TEXT NOT NULL",
		"agentserver_commit TEXT NOT NULL",
		"modelserver_commit TEXT NOT NULL",
		"app_commit TEXT NOT NULL",
		"machine_topology TEXT NOT NULL",
		"context_ground_truth TEXT NOT NULL",
		"capability_snapshot_hash TEXT NOT NULL DEFAULT ''",
		"task_contract_hash TEXT NOT NULL DEFAULT ''",
		"dynamic_mcp_registry_hash TEXT NOT NULL DEFAULT ''",
		"selected_context TEXT NOT NULL",
		"ground_truth_context TEXT NOT NULL",
		"start_time TEXT NOT NULL",
		"end_time TEXT NOT NULL",
		"success_oracle_result TEXT NOT NULL",
		"failure_category TEXT NOT NULL DEFAULT ''",
		"human_intervention_count INTEGER NOT NULL DEFAULT 0",
		"artifact_hashes TEXT NOT NULL DEFAULT '[]'",
		"observer_trace_path TEXT NOT NULL DEFAULT ''",
		"model_trace_id TEXT NOT NULL DEFAULT ''",
	}
	ai, bi := -1, -1
	for i, c := range cols {
		if strings.HasPrefix(c, a+" ") {
			ai = i
		}
		if strings.HasPrefix(c, b+" ") {
			bi = i
		}
	}
	if ai < 0 || bi < 0 {
		panic("driftDDLSwapOrder: column not found")
	}
	cols[ai], cols[bi] = cols[bi], cols[ai]
	return "CREATE TABLE runs (" + strings.Join(cols, ", ") + ")"
}
