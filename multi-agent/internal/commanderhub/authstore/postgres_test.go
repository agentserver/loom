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

// TestPostgresStore_ClusterTablesCreated verifies that the new shared-registry tables
// are created with proper constraints and primary key shapes.
func TestPostgresStore_ClusterTablesCreated(t *testing.T) {
	dsn := os.Getenv("OBSERVER_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("set OBSERVER_POSTGRES_TEST_DSN to run")
	}
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, MigratePostgres(db))

	// commander_daemons PK must include short_id (NOT a per-connection
	// daemon_id; that would lose ownership across reconnect).
	var pkCols string
	require.NoError(t, db.QueryRow(`
		SELECT string_agg(a.attname, ',' ORDER BY array_position(i.indkey, a.attnum))
		FROM pg_index i
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
		WHERE i.indrelid = 'commander_daemons'::regclass AND i.indisprimary
	`).Scan(&pkCols))
	require.Equal(t, "user_id,workspace_id,short_id", pkCols)

	// commander_turns CHECK constraint enforces the state enum.
	_, err = db.Exec(`
		INSERT INTO commander_turns (user_id, workspace_id, short_id, session_id, state)
		VALUES ('u', 'w', 's', 'sess', 'not_a_valid_state')
	`)
	require.Error(t, err, "expected CHECK constraint violation")

	// commander_telemetry_buckets composite PK (no NUL bytes in PG text).
	var btPK string
	require.NoError(t, db.QueryRow(`
		SELECT string_agg(a.attname, ',' ORDER BY array_position(i.indkey, a.attnum))
		FROM pg_index i
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
		WHERE i.indrelid = 'commander_telemetry_buckets'::regclass AND i.indisprimary
	`).Scan(&btPK))
	require.Equal(t, "workspace_id,agent_id,telemetry_key_id", btPK)
}
