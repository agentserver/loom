package postgres

import (
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("OBSERVER_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("set OBSERVER_POSTGRES_TEST_DSN to run PostgreSQL integration tests")
	}
	return dsn
}

func TestMigrateCreatesCoreTables(t *testing.T) {
	st, err := Open(Config{DSN: testDSN(t)})
	require.NoError(t, err)
	defer st.Close()

	var exists bool
	err = st.db.QueryRow(`SELECT EXISTS (
		SELECT 1 FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name = 'events'
	)`).Scan(&exists)
	require.NoError(t, err)
	require.True(t, exists)
}

func TestSchemaPermitsAPIKeyDeleteWithDependentRows(t *testing.T) {
	st, err := Open(Config{DSN: testDSN(t)})
	require.NoError(t, err)
	defer st.Close()

	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	apiKeyID := "ak-test-" + suffix
	workspaceID := "ws-test-" + suffix
	agentID := "agent-test-" + suffix
	keyHash := "key-hash-test-" + suffix
	tokenHash := "token-hash-test-" + suffix

	tx, err := st.db.Begin()
	require.NoError(t, err)
	defer tx.Rollback()

	_, err = tx.Exec(`
		INSERT INTO api_keys (id, key_hash, note, created_at)
		VALUES ($1, $2, '', NOW())
	`, apiKeyID, keyHash)
	require.NoError(t, err)

	_, err = tx.Exec(`
		INSERT INTO workspaces (id, name, created_by_api_key_id, created_at, last_seen_at)
		VALUES ($1, '', $2, NOW(), NOW())
	`, workspaceID, apiKeyID)
	require.NoError(t, err)

	_, err = tx.Exec(`
		INSERT INTO agents (workspace_id, id, role, display_name, token_hash, created_by_api_key_id)
		VALUES ($1, $2, 'driver', 'Driver', $3, $4)
	`, workspaceID, agentID, tokenHash, apiKeyID)
	require.NoError(t, err)

	_, err = tx.Exec(`DELETE FROM api_keys WHERE id = $1`, apiKeyID)
	require.NoError(t, err)
}
