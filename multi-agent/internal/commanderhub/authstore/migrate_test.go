package authstore

import (
	"database/sql"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
)

// TestMigratePostgres_Idempotent exercises the real Postgres path when
// OBSERVER_POSTGRES_TEST_DSN is set. Without it, skip — the no-DSN drift
// check below still runs.
func TestMigratePostgres_Idempotent(t *testing.T) {
	dsn := os.Getenv("OBSERVER_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("set OBSERVER_POSTGRES_TEST_DSN to run")
	}
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, MigratePostgres(db))
	require.NoError(t, MigratePostgres(db)) // re-run must not error
}

// TestFailureEnumMatchesSchema is the no-DSN drift guard: parses the embedded
// schema_postgres.sql for the `failure IN (...)` allowlist and compares it to
// the Go enum. Diverges → fail. Edit one side without the other → fail.
func TestFailureEnumMatchesSchema(t *testing.T) {
	re := regexp.MustCompile(`(?s)commander_logins_failure_enum\s+CHECK\s*\(\s*failure\s+IS\s+NULL\s+OR\s+failure\s+IN\s*\(([^)]+)\)`)
	match := re.FindStringSubmatch(schemaPostgresSQL)
	require.NotNil(t, match, "schema_postgres.sql missing commander_logins_failure_enum CHECK")

	raw := match[1]
	var schemaList []string
	for _, lit := range strings.Split(raw, ",") {
		lit = strings.TrimSpace(lit)
		lit = strings.Trim(lit, "'")
		if lit != "" {
			schemaList = append(schemaList, lit)
		}
	}
	sort.Strings(schemaList)

	var goList []string
	for _, f := range allFailureValues {
		goList = append(goList, string(f))
	}
	sort.Strings(goList)

	require.Equal(t, goList, schemaList,
		"Go Failure enum and SQL CHECK list have drifted; sync both.")
}
