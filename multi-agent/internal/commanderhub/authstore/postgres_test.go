package authstore

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
)

// TestPostgresStore_Conformance drives the same RunConformanceTests suite
// against the real Postgres store. Requires OBSERVER_POSTGRES_TEST_DSN
// (consistent with internal/userspace/store_postgres_test.go convention).
func TestPostgresStore_Conformance(t *testing.T) {
	dsn := os.Getenv("OBSERVER_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("set OBSERVER_POSTGRES_TEST_DSN to run")
	}
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, MigratePostgres(db))

	RunConformanceTests(t, func(t *testing.T) Store {
		_, err := db.ExecContext(context.Background(),
			`TRUNCATE commander_logins, commander_sessions`)
		require.NoError(t, err)
		return NewPostgresStore(db)
	})
}
