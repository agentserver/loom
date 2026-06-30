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
			`TRUNCATE commander_logins, commander_sessions, commander_daemons, commander_turns, commander_forward_nonces, commander_telemetry_buckets`)
		require.NoError(t, err)
		return NewPostgresStore(db)
	})
}

// TestPostgresStore_TablesExist verifies that the new shared-registry tables
// are created with proper constraints.
func TestPostgresStore_TablesExist(t *testing.T) {
	dsn := os.Getenv("OBSERVER_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("set OBSERVER_POSTGRES_TEST_DSN to run")
	}
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, MigratePostgres(db))

	ctx := context.Background()

	// Verify commander_daemons table exists with primary key
	var exists bool
	err = db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name='commander_daemons')`).Scan(&exists)
	require.NoError(t, err)
	require.True(t, exists, "commander_daemons table should exist")

	// Verify commander_daemons constraints
	var constraintCount int
	err = db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM information_schema.constraint_column_usage
		 WHERE table_name='commander_daemons' AND constraint_name LIKE 'commander_daemons_%'`).Scan(&constraintCount)
	require.NoError(t, err)
	require.Greater(t, constraintCount, 0, "commander_daemons should have check constraints")

	// Verify commander_turns table exists
	err = db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name='commander_turns')`).Scan(&exists)
	require.NoError(t, err)
	require.True(t, exists, "commander_turns table should exist")

	// Verify commander_turns has state enum constraint
	err = db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM information_schema.table_constraints
		 WHERE table_name='commander_turns' AND constraint_name='commander_turns_state_enum')`).Scan(&exists)
	require.NoError(t, err)
	require.True(t, exists, "commander_turns should have state_enum constraint")

	// Verify commander_forward_nonces table exists
	err = db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name='commander_forward_nonces')`).Scan(&exists)
	require.NoError(t, err)
	require.True(t, exists, "commander_forward_nonces table should exist")

	// Verify commander_telemetry_buckets table exists
	err = db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name='commander_telemetry_buckets')`).Scan(&exists)
	require.NoError(t, err)
	require.True(t, exists, "commander_telemetry_buckets table should exist")
}
