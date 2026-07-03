package observerstore

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Helpers ----------------------------------------------------------

func openAuditTestDB(t *testing.T) *sql.DB {
	t.Helper()
	p := filepath.Join(t.TempDir(), "audit.db")
	st, err := OpenSQLite(p)
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })
	return st.DB()
}

// seedTaskContract inserts one task_contracts row directly (bypassing
// the store's higher-level API) so this test file does not need to
// build a full contract writer. The body is expected to be
// well-formed JSON matching contract.TaskContract's shape.
func seedTaskContract(t *testing.T, db *sql.DB, workspaceID, taskID, conversationID, body string) {
	t.Helper()
	now := NowUTC()
	_, err := db.Exec(`INSERT INTO task_contracts
        (workspace_id, task_id, conversation_id, owner_agent_id, body,
         created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?)`,
		workspaceID, taskID, conversationID, "owner-a", body, now, now)
	require.NoError(t, err)
}

// seedTaskContractAt inserts with an explicit updated_at, used by the
// tied-contracts + shadow tests.
func seedTaskContractAt(t *testing.T, db *sql.DB, workspaceID, taskID, conversationID, body, updatedAt string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO task_contracts
        (workspace_id, task_id, conversation_id, owner_agent_id, body,
         created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?)`,
		workspaceID, taskID, conversationID, "owner-a", body, updatedAt, updatedAt)
	require.NoError(t, err)
}

// insertAuditRaw INSERTs a row directly for tests that need to control
// event_id / ts. Production code MUST go through WriteAuditEvent — this
// helper exists only inside a _test.go file, so the grep-anchored
// single-writer rule (TestOnlyOneAuditEventsWriter) skips it.
func insertAuditRaw(t *testing.T, db *sql.DB, r AuditEventRow) {
	t.Helper()
	require.NoError(t, NewAuditWriter(db).WriteAuditEvent(context.Background(), r))
}

// Test 14: round-trip -----------------------------------------------

func TestWriteAuditEvent_RoundTrip(t *testing.T) {
	db := openAuditTestDB(t)
	w := NewAuditWriter(db)
	ctx := context.Background()

	rows := []AuditEventRow{
		{EventID: "ev-r-1", WorkspaceID: "w1", ConversationID: "c1", RunID: "run-1", Kind: "read", Target: "/tmp/a", TS: NowUTC()},
		{EventID: "ev-w-1", WorkspaceID: "w1", ConversationID: "c1", RunID: "run-1", Kind: "write", Target: "/tmp/b", SizeBytes: 42, Hash: strings64('a'), TS: NowUTC()},
		{EventID: "ev-t-1", WorkspaceID: "w1", ConversationID: "c1", RunID: "run-1", Kind: "tool_call", Target: "bash", TS: NowUTC()},
		{EventID: "ev-m-1", WorkspaceID: "w1", ConversationID: "c1", RunID: "run-1", Kind: "model_call", Target: "glm-5.2", TS: NowUTC()},
	}
	for _, r := range rows {
		require.NoError(t, w.WriteAuditEvent(ctx, r))
	}
	var got int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM audit_events").Scan(&got))
	require.Equal(t, 4, got)
}

// Test 15: SQL injection guard --------------------------------------

func TestWriteAuditEvent_SQLInjection_Parameterised(t *testing.T) {
	db := openAuditTestDB(t)
	malicious := `x'); DROP TABLE audit_events;--`
	require.NoError(t, NewAuditWriter(db).WriteAuditEvent(context.Background(), AuditEventRow{
		EventID:        "inj-1",
		ConversationID: malicious,
		Kind:           "read",
		Target:         "/tmp/a",
		TS:             NowUTC(),
	}))
	// Table must still exist and hold the row with the payload
	// verbatim in the ConversationID column.
	var got string
	require.NoError(t, db.QueryRow("SELECT conversation_id FROM audit_events WHERE event_id=?", "inj-1").Scan(&got))
	require.Equal(t, malicious, got)
}

// Test 16: duplicate event_id surfaces error ------------------------

func TestWriteAuditEvent_UniqueEventIDConflictSurfaces(t *testing.T) {
	db := openAuditTestDB(t)
	row := AuditEventRow{EventID: "dup-1", ConversationID: "c1", Kind: "read", Target: "/tmp/a", TS: NowUTC()}
	require.NoError(t, NewAuditWriter(db).WriteAuditEvent(context.Background(), row))
	err := NewAuditWriter(db).WriteAuditEvent(context.Background(), row)
	require.Error(t, err)
	require.Contains(t, err.Error(), "audit_events")
}

// Test 17 + 18: single writer + const SQL (static grep) -------------

func TestOnlyOneAuditEventsWriter(t *testing.T) {
	root := findRepoRootFromHere(t)
	re := regexp.MustCompile(`(?i)INSERT\s+INTO\s+audit_events|UPDATE\s+audit_events|DELETE\s+FROM\s+audit_events`)
	hits := grepInternal(t, root, re, ".go")
	// Test files (_test.go) are permitted — they wrap the real
	// writer as a helper and do not multiply the production writer
	// path. Production hits MUST be exactly one file.
	var prodHits []string
	for _, h := range hits {
		if strings.HasSuffix(h, "_test.go") {
			continue
		}
		prodHits = append(prodHits, h)
	}
	if len(prodHits) != 1 || !strings.HasSuffix(prodHits[0], "contract_violations_view.go") {
		t.Fatalf("expected exactly one production writer path for audit_events; got %v", prodHits)
	}
}

func TestContractViolationsView_NoWritePaths_Static(t *testing.T) {
	root := findRepoRootFromHere(t)
	re := regexp.MustCompile(`(?i)INSERT\s+INTO\s+contract_violations|UPDATE\s+contract_violations|DELETE\s+FROM\s+contract_violations`)
	hits := grepInternal(t, root, re, ".go")
	if len(hits) != 0 {
		t.Fatalf("contract_violations view MUST be read-only; found writer references: %v", hits)
	}
}

func TestContractViolationsView_NoTrigger_Static(t *testing.T) {
	root := findRepoRootFromHere(t)
	schemaPath := filepath.Join(root, "internal", "observerstore", "schema.sql")
	data, err := os.ReadFile(schemaPath)
	require.NoError(t, err)
	// Strip -- ... EOL comments before grepping — the audit-view DDL
	// intentionally names "INSTEAD OF" inside a comment ("...unless
	// an INSTEAD OF trigger is attached — this WT MUST NOT..."), and
	// we want the assertion to see only executable SQL.
	body := stripSQLLineComments(string(data))
	require.NotContains(t, strings.ToUpper(body), "INSTEAD OF",
		"executable SQL must not contain INSTEAD OF (trigger promotes the view to writable)")
	// Any CREATE TRIGGER whose statement mentions contract_violations
	// is banned by spec §7 (e). Grep the executable SQL body — the
	// regex must not fire.
	re := regexp.MustCompile(`(?i)CREATE\s+TRIGGER[\s\S]*?contract_violations`)
	if loc := re.FindString(body); loc != "" {
		t.Fatalf("contract_violations view must not have any trigger attached; found: %q", loc)
	}
}

// stripSQLLineComments removes `-- ...` up to EOL from every line. Not
// a full SQL comment stripper (no /* ... */ handling — the schema uses
// only line comments), just enough for the trigger-ban grep.
func stripSQLLineComments(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// Test 37: end-to-end acceptance -----------------------------------

func TestContractViolationsView_ByRunID_ReturnsExpectedRows(t *testing.T) {
	db := openAuditTestDB(t)
	seedTaskContract(t, db, "w1", "task-1", "conv-1", `{
        "version": 1,
        "conversation_id": "conv-1",
        "data_contract": {
            "read_artifacts":  [{"name": "/tmp/a"}],
            "write_targets":   [{"name": "/tmp/b"}]
        },
        "capability_requirements": {
            "tools":  ["bash"],
            "skills": ["chat"]
        }
    }`)
	// 3 matching events + 2 violating events.
	ts := time.Unix(1_700_000_000, 0).UTC()
	rows := []AuditEventRow{
		{EventID: "e1", WorkspaceID: "w1", ConversationID: "conv-1", RunID: "run-1", Kind: "read", Target: "/tmp/a", TS: fmtTS(ts.Add(1 * time.Millisecond))},
		{EventID: "e2", WorkspaceID: "w1", ConversationID: "conv-1", RunID: "run-1", Kind: "write", Target: "/tmp/b", TS: fmtTS(ts.Add(2 * time.Millisecond))},
		{EventID: "e3", WorkspaceID: "w1", ConversationID: "conv-1", RunID: "run-1", Kind: "tool_call", Target: "bash", TS: fmtTS(ts.Add(3 * time.Millisecond))},
		{EventID: "e4", WorkspaceID: "w1", ConversationID: "conv-1", RunID: "run-1", Kind: "read", Target: "/etc/passwd", TS: fmtTS(ts.Add(4 * time.Millisecond))},
		{EventID: "e5", WorkspaceID: "w1", ConversationID: "conv-1", RunID: "run-1", Kind: "tool_call", Target: "mcp:evil:exfil", TS: fmtTS(ts.Add(5 * time.Millisecond))},
	}
	for _, r := range rows {
		insertAuditRaw(t, db, r)
	}
	got, err := NewViolationsQuery(db).ByRunID(context.Background(), "run-1")
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "undeclared_read", got[0].ViolationKind)
	require.Equal(t, "/etc/passwd", got[0].Target)
	require.Equal(t, "undeclared_tool_call", got[1].ViolationKind)
	require.Equal(t, "mcp:evil:exfil", got[1].Target)
}

// Test 37a: match by name OR artifact_id ---------------------------

func TestContractViolationsView_ReadArtifactID_MatchedByEither(t *testing.T) {
	db := openAuditTestDB(t)
	seedTaskContract(t, db, "w1", "task-1", "conv-1", `{
        "version": 1,
        "conversation_id": "conv-1",
        "data_contract": {
            "read_artifacts":  [{"artifact_id": "abc123"}],
            "write_targets":   []
        },
        "capability_requirements": {"tools": [], "skills": []}
    }`)
	ts := fmtTS(time.Unix(1_700_000_000, 0).UTC())
	insertAuditRaw(t, db, AuditEventRow{EventID: "e1", WorkspaceID: "w1", ConversationID: "conv-1", RunID: "run-1", Kind: "read", Target: "abc123", TS: ts})
	got, err := NewViolationsQuery(db).ByRunID(context.Background(), "run-1")
	require.NoError(t, err)
	require.Len(t, got, 0, "target matching artifact_id must not fire violation")

	// Symmetric case: contract has name only, event uses that name.
	seedTaskContract(t, db, "w1", "task-2", "conv-2", `{
        "version": 1,
        "conversation_id": "conv-2",
        "data_contract": {
            "read_artifacts":  [{"name": "foo"}],
            "write_targets":   []
        },
        "capability_requirements": {"tools": [], "skills": []}
    }`)
	insertAuditRaw(t, db, AuditEventRow{EventID: "e2", WorkspaceID: "w1", ConversationID: "conv-2", RunID: "run-2", Kind: "read", Target: "foo", TS: ts})
	got2, err := NewViolationsQuery(db).ByRunID(context.Background(), "run-2")
	require.NoError(t, err)
	require.Len(t, got2, 0, "target matching name must not fire violation")
}

// Test 37b: tied contracts — any declares defeats ------------------

func TestContractViolationsView_TiedContracts_AnyDeclaresDefeats(t *testing.T) {
	db := openAuditTestDB(t)
	tieAt := NowUTC()
	seedTaskContractAt(t, db, "w1", "task-A", "conv-1", `{"data_contract":{"read_artifacts":[{"name":"/tmp/a"}]}}`, tieAt)
	seedTaskContractAt(t, db, "w1", "task-B", "conv-1", `{"data_contract":{"read_artifacts":[{"name":"/tmp/b"}]}}`, tieAt)

	ts := time.Unix(1_700_000_000, 0).UTC()
	insertAuditRaw(t, db, AuditEventRow{EventID: "e1", WorkspaceID: "w1", ConversationID: "conv-1", RunID: "run-1", Kind: "read", Target: "/tmp/a", TS: fmtTS(ts.Add(1 * time.Millisecond))})
	insertAuditRaw(t, db, AuditEventRow{EventID: "e2", WorkspaceID: "w1", ConversationID: "conv-1", RunID: "run-1", Kind: "read", Target: "/tmp/b", TS: fmtTS(ts.Add(2 * time.Millisecond))})
	insertAuditRaw(t, db, AuditEventRow{EventID: "e3", WorkspaceID: "w1", ConversationID: "conv-1", RunID: "run-1", Kind: "read", Target: "/tmp/c", TS: fmtTS(ts.Add(3 * time.Millisecond))})

	got, err := NewViolationsQuery(db).ByRunID(context.Background(), "run-1")
	require.NoError(t, err)
	// Only /tmp/c is undeclared by BOTH tied contracts → 1 violation
	// row. /tmp/a is declared by contract-A; /tmp/b by contract-B.
	// The outer row count MUST equal 1 — ties MUST NOT multiply.
	require.Len(t, got, 1)
	require.Equal(t, "/tmp/c", got[0].Target)
}

// Test 37c: no contract → all undeclared (fail-closed) --------------

func TestContractViolationsView_NoContract_AllUndeclared(t *testing.T) {
	db := openAuditTestDB(t)
	ts := time.Unix(1_700_000_000, 0).UTC()
	insertAuditRaw(t, db, AuditEventRow{EventID: "e1", WorkspaceID: "w1", ConversationID: "conv-orphan", RunID: "run-1", Kind: "read", Target: "/tmp/x", TS: fmtTS(ts.Add(1 * time.Millisecond))})
	insertAuditRaw(t, db, AuditEventRow{EventID: "e2", WorkspaceID: "w1", ConversationID: "conv-orphan", RunID: "run-1", Kind: "tool_call", Target: "bash", TS: fmtTS(ts.Add(2 * time.Millisecond))})
	got, err := NewViolationsQuery(db).ByRunID(context.Background(), "run-1")
	require.NoError(t, err)
	require.Len(t, got, 2)
}

// Test 37d: later contract shadows earlier -------------------------

func TestContractViolationsView_LaterContract_ShadowsEarlier(t *testing.T) {
	db := openAuditTestDB(t)
	earlier := "2026-01-01T00:00:00.000000000Z"
	later := "2026-06-01T00:00:00.000000000Z"
	seedTaskContractAt(t, db, "w1", "task-1", "conv-1", `{"data_contract":{"read_artifacts":[{"name":"/tmp/old"}]}}`, earlier)
	seedTaskContractAt(t, db, "w1", "task-2", "conv-1", `{"data_contract":{"read_artifacts":[{"name":"/tmp/new"}]}}`, later)

	ts := time.Unix(1_700_000_000, 0).UTC()
	insertAuditRaw(t, db, AuditEventRow{EventID: "e-old", WorkspaceID: "w1", ConversationID: "conv-1", RunID: "run-1", Kind: "read", Target: "/tmp/old", TS: fmtTS(ts)})
	got, err := NewViolationsQuery(db).ByRunID(context.Background(), "run-1")
	require.NoError(t, err)
	require.Len(t, got, 1, "the earlier contract must NOT rescue an event once a later contract exists")
	require.Equal(t, "/tmp/old", got[0].Target)
}

// Test 41: consumer-view SQL (spec §7 (g)) -------------------------

func TestConsumerView_ContractViolationRate_SQL(t *testing.T) {
	db := openAuditTestDB(t)
	seedTaskContract(t, db, "w1", "task-1", "conv-1", `{
        "data_contract":{"read_artifacts":[{"name":"/tmp/a"}]},
        "capability_requirements":{"tools":["bash"],"skills":[]}
    }`)
	ts := time.Unix(1_700_000_000, 0).UTC()
	insertAuditRaw(t, db, AuditEventRow{EventID: "e1", WorkspaceID: "w1", ConversationID: "conv-1", RunID: "run-42", Kind: "read", Target: "/etc/passwd", TS: fmtTS(ts.Add(1 * time.Millisecond))})
	insertAuditRaw(t, db, AuditEventRow{EventID: "e2", WorkspaceID: "w1", ConversationID: "conv-1", RunID: "run-42", Kind: "tool_call", Target: "bash", TS: fmtTS(ts.Add(2 * time.Millisecond))})
	insertAuditRaw(t, db, AuditEventRow{EventID: "e3", WorkspaceID: "w1", ConversationID: "conv-1", RunID: "run-42", Kind: "tool_call", Target: "evil-tool", TS: fmtTS(ts.Add(3 * time.Millisecond))})

	// (a) SELECT COUNT(*) numerator per run.
	var num int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM contract_violations WHERE run_id = ?", "run-42").Scan(&num))
	require.Equal(t, 2, num, "run-42 has 2 undeclared events (read /etc/passwd + tool evil-tool)")

	// (c) GROUP BY violation_kind.
	rows, err := db.Query("SELECT violation_kind, COUNT(*) FROM contract_violations WHERE run_id = ? GROUP BY violation_kind ORDER BY violation_kind", "run-42")
	require.NoError(t, err)
	defer rows.Close()
	got := map[string]int{}
	for rows.Next() {
		var k string
		var c int
		require.NoError(t, rows.Scan(&k, &c))
		got[k] = c
	}
	require.Equal(t, 1, got["undeclared_read"])
	require.Equal(t, 1, got["undeclared_tool_call"])
}

// Test 42: perf-bench CI skip guard --------------------------------

func TestPerfBench_SkippedInCI(t *testing.T) {
	// This WT ships no perf assertions today. The test spec-locks the
	// convention so a future benchmark author cannot silently add one
	// without the CI-skip guard. Grep every _test.go in
	// internal/journal / internal/observerstore for
	// `func Benchmark[A-Z]` and, for each hit, assert the file also
	// contains testing.Short()/os.Getenv("CI") plus a t.Skip / b.Skip
	// call inside a short window of lines.
	root := findRepoRootFromHere(t)
	dirs := []string{
		filepath.Join(root, "internal", "journal"),
		filepath.Join(root, "internal", "observerstore"),
	}
	benchRe := regexp.MustCompile(`func\s+Benchmark[A-Z]\w*`)
	guardRe := regexp.MustCompile(`testing\.Short\(\)\s*\|\|\s*os\.Getenv\("CI"\)\s*!=\s*""`)
	for _, d := range dirs {
		files, err := filepath.Glob(filepath.Join(d, "*_test.go"))
		require.NoError(t, err)
		for _, f := range files {
			data, err := os.ReadFile(f)
			require.NoError(t, err)
			if benchRe.Find(data) == nil {
				continue
			}
			if !guardRe.Match(data) {
				t.Fatalf("perf benchmark in %s lacks the required testing.Short()/CI guard", f)
			}
		}
	}
}

// helpers -----------------------------------------------------------

// strings64 returns a 64-char string of the given byte — a convenient
// fixture-hash generator ("a"*64 is not a valid sha256 hex but IS
// valid ^[a-f0-9]{64}$ — good enough for round-trip tests that just
// need a hex-shaped placeholder).
func strings64(b byte) string {
	buf := make([]byte, 64)
	for i := range buf {
		buf[i] = b
	}
	return string(buf)
}

// fmtTS formats a time.Time the way audit_events wants (fixed-width
// UTC RFC3339Nano); tests use it so sort order in the view matches
// wall-clock order.
func fmtTS(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
}

// findRepoRootFromHere walks upward from this file until it finds
// go.mod, so the static grep tests work regardless of where the test
// binary was invoked from.
func findRepoRootFromHere(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate go.mod from any ancestor of %s", dir)
		}
		dir = parent
	}
}

// grepInternal walks $root/internal recursively and returns every file
// (with the given extension) whose content matches re. Test files ARE
// included; the caller filters if they need to.
func grepInternal(t *testing.T, root string, re *regexp.Regexp, ext string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(filepath.Join(root, "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ext {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if re.Match(data) {
			out = append(out, path)
		}
		return nil
	})
	require.NoError(t, err)
	return out
}
