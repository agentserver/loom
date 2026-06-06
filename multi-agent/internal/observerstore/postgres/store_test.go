package postgres

import (
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/observer"
	"github.com/yourorg/multi-agent/internal/observerstore"
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

func TestPostgresStoreRegistrationAndEventProjection(t *testing.T) {
	st, err := Open(Config{DSN: testDSN(t)})
	require.NoError(t, err)
	defer st.Close()

	require.NoError(t, st.ReplaceAPIKeys([]observerstore.APIKeySpec{
		{ID: "ak-test", Key: "secret"},
	}))
	keyID, ok, err := st.LookupAPIKey("secret")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "ak-test", keyID)

	require.NoError(t, st.UpsertWorkspaceLazy("ws1", "Workspace", keyID))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{
		WorkspaceID: "ws1", ID: "driver", Role: observer.RoleDriver, DisplayName: "driver",
	}, "token-driver", keyID))

	agent, ok, err := st.ValidateToken("token-driver")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "ws1", agent.WorkspaceID)

	err = st.Ingest(observer.Event{
		EventID: "ev1", TS: "2026-06-07T00:00:00Z",
		WorkspaceID: "ws1", AgentID: "driver", AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: "task1", Summary: "do it", Status: "assigned",
	})
	require.NoError(t, err)

	progress, found, err := st.GetTaskProgress("ws1", "task1")
	require.NoError(t, err)
	require.True(t, found)
	require.False(t, progress.IsFinal)
}
