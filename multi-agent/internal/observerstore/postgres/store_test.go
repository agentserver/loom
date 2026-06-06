package postgres

import (
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
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

	schema := "observer_test_" + strconv.Itoa(os.Getpid()) + "_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	adminDB, err := sql.Open("pgx", dsn)
	require.NoError(t, err)

	created := false
	t.Cleanup(func() {
		if created {
			_, err := adminDB.Exec(`DROP SCHEMA IF EXISTS ` + quoteIdentifier(schema) + ` CASCADE`)
			require.NoError(t, err)
		}
		require.NoError(t, adminDB.Close())
	})

	_, err = adminDB.Exec(`CREATE SCHEMA ` + quoteIdentifier(schema))
	require.NoError(t, err)
	created = true

	isolatedDSN, err := dsnWithSearchPath(dsn, schema)
	require.NoError(t, err)
	return isolatedDSN
}

func dsnWithSearchPath(dsn, schema string) (string, error) {
	if schema == "" {
		return "", errors.New("postgres test schema must not be empty")
	}

	lower := strings.ToLower(dsn)
	if strings.HasPrefix(lower, "postgres://") || strings.HasPrefix(lower, "postgresql://") {
		parsed, err := url.Parse(dsn)
		if err != nil {
			return "", err
		}
		q := parsed.Query()
		q.Set("search_path", schema)
		parsed.RawQuery = q.Encode()
		return parsed.String(), nil
	}

	trimmed := strings.TrimSpace(dsn)
	if trimmed == "" || !strings.Contains(trimmed, "=") {
		return "", errors.New("postgres test DSN must be a URL or keyword/value connection string")
	}
	return trimmed + " search_path=" + quoteKeywordValue(schema), nil
}

func quoteIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

func quoteKeywordValue(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `'`, `\'`)
	return "'" + value + "'"
}

func TestDSNWithSearchPathURL(t *testing.T) {
	got, err := dsnWithSearchPath("postgres://user:pass@localhost/testdb?sslmode=disable&search_path=public", "observer_test_123")
	require.NoError(t, err)

	parsed, err := url.Parse(got)
	require.NoError(t, err)
	require.Equal(t, "postgres", parsed.Scheme)
	require.Equal(t, "disable", parsed.Query().Get("sslmode"))
	require.Equal(t, "observer_test_123", parsed.Query().Get("search_path"))
}

func TestDSNWithSearchPathKeyword(t *testing.T) {
	got, err := dsnWithSearchPath("host=localhost dbname=testdb sslmode=disable", "observer_test_123")
	require.NoError(t, err)

	require.Equal(t, "host=localhost dbname=testdb sslmode=disable search_path='observer_test_123'", got)
}

func TestMigrateCreatesCoreTables(t *testing.T) {
	st, err := Open(Config{DSN: testDSN(t)})
	require.NoError(t, err)
	defer st.Close()

	var exists bool
	err = st.db.QueryRow(`SELECT to_regclass('events') IS NOT NULL`).Scan(&exists)
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

	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	apiKeyID := "ak-test-" + suffix
	apiKey := "secret-" + suffix
	workspaceID := "ws-test-" + suffix
	agentID := "driver-" + suffix
	token := "token-driver-" + suffix
	taskID := "task-" + suffix

	require.NoError(t, st.ReplaceAPIKeys([]observerstore.APIKeySpec{
		{ID: apiKeyID, Key: apiKey},
	}))
	keyID, ok, err := st.LookupAPIKey(apiKey)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, apiKeyID, keyID)

	require.NoError(t, st.UpsertWorkspaceLazy(workspaceID, "Workspace", keyID))
	require.NoError(t, st.UpsertAgent(observerstore.Agent{
		WorkspaceID: workspaceID, ID: agentID, Role: observer.RoleDriver, DisplayName: "driver",
	}, token, keyID))

	agent, ok, err := st.ValidateToken(token)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, workspaceID, agent.WorkspaceID)

	err = st.Ingest(observer.Event{
		EventID: "ev-" + suffix, TS: "2026-06-07T00:00:00Z",
		WorkspaceID: workspaceID, AgentID: agentID, AgentRole: observer.RoleDriver,
		Type: observer.EventDriverTaskSubmitted, TaskID: taskID, Summary: "do it", Status: "assigned",
	})
	require.NoError(t, err)

	progress, found, err := st.GetTaskProgress(workspaceID, taskID)
	require.NoError(t, err)
	require.True(t, found)
	require.False(t, progress.IsFinal)
}

func TestPostgresStoreTelemetryAPIKeys(t *testing.T) {
	st, err := Open(Config{DSN: testDSN(t)})
	require.NoError(t, err)
	defer st.Close()

	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	require.NoError(t, st.ReplaceTelemetryAPIKeys([]observerstore.TelemetryAPIKeySpec{
		{ID: "ops-global-" + suffix, Key: "global-secret-" + suffix, WorkspaceID: "*", Enabled: true},
		{ID: "ops-ws2-" + suffix, Key: "ws2-secret-" + suffix, WorkspaceID: "ws2-" + suffix, Enabled: true},
		{ID: "ops-disabled-" + suffix, Key: "disabled-secret-" + suffix, WorkspaceID: "*", Enabled: false},
	}))

	keyID, ok, err := st.LookupTelemetryAPIKey("global-secret-"+suffix, "ws1-"+suffix)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "ops-global-"+suffix, keyID)

	keyID, ok, err = st.LookupTelemetryAPIKey("ws2-secret-"+suffix, "ws2-"+suffix)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "ops-ws2-"+suffix, keyID)

	_, ok, err = st.LookupTelemetryAPIKey("ws2-secret-"+suffix, "ws1-"+suffix)
	require.NoError(t, err)
	require.False(t, ok)

	_, ok, err = st.LookupTelemetryAPIKey("disabled-secret-"+suffix, "ws1-"+suffix)
	require.NoError(t, err)
	require.False(t, ok)

	require.NoError(t, st.ReplaceTelemetryAPIKeys(nil))
	_, ok, err = st.LookupTelemetryAPIKey("global-secret-"+suffix, "ws1-"+suffix)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestPostgresStoreObjectMetadataMethods(t *testing.T) {
	st, err := Open(Config{DSN: testDSN(t)})
	require.NoError(t, err)
	defer st.Close()

	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	workspaceID := "ws-test-" + suffix
	ownerID := "driver-" + suffix
	writerID := "slave-" + suffix
	taskID := "task-" + suffix

	art, err := st.CreateArtifact(observerstore.ArtifactCreate{
		WorkspaceID: workspaceID, OwnerAgentID: ownerID, Path: "/tmp/input.txt",
		Kind: "file", State: observerstore.ArtifactStateRegistered,
	})
	require.NoError(t, err)
	_, err = st.RequestArtifact(workspaceID, writerID, art.ID)
	require.NoError(t, err)

	artifactObjectKey := "workspaces/" + workspaceID + "/artifacts/" + art.ID
	err = st.MarkArtifactAvailable(workspaceID, ownerID, art.ID, "text/plain", "sha-art", artifactObjectKey, 123)
	require.NoError(t, err)

	var artifactState, artifactMIME, artifactSHA, gotArtifactKey string
	var artifactBytes int64
	err = st.db.QueryRow(`SELECT state, mime, bytes, sha256, object_key FROM artifacts WHERE workspace_id=$1 AND id=$2`, workspaceID, art.ID).
		Scan(&artifactState, &artifactMIME, &artifactBytes, &artifactSHA, &gotArtifactKey)
	require.NoError(t, err)
	require.Equal(t, observerstore.ArtifactStateAvailable, artifactState)
	require.Equal(t, "text/plain", artifactMIME)
	require.Equal(t, int64(123), artifactBytes)
	require.Equal(t, "sha-art", artifactSHA)
	require.Equal(t, artifactObjectKey, gotArtifactKey)

	var requestState string
	err = st.db.QueryRow(`SELECT state FROM artifact_requests WHERE workspace_id=$1 AND artifact_id=$2`, workspaceID, art.ID).
		Scan(&requestState)
	require.NoError(t, err)
	require.Equal(t, observerstore.ArtifactStateAvailable, requestState)

	wr, err := st.CreateWrite(observerstore.WriteCreate{
		WorkspaceID: workspaceID, OwnerAgentID: ownerID, TaskID: taskID,
		Path: "/tmp/out.txt", Overwrite: true,
	})
	require.NoError(t, err)

	writeObjectKey := "workspaces/" + workspaceID + "/writes/" + wr.ID
	err = st.MarkWriteCompleted(workspaceID, writerID, wr.ID, "text/plain", "sha-write", writeObjectKey, 456)
	require.NoError(t, err)

	writes, err := st.ListCompletedWrites(workspaceID, ownerID, taskID)
	require.NoError(t, err)
	require.Len(t, writes, 1)
	require.Equal(t, wr.ID, writes[0].ID)
	require.Equal(t, writerID, writes[0].WriterAgentID)
	require.Equal(t, int64(456), writes[0].Bytes)
	require.Equal(t, "sha-write", writes[0].SHA256)
	require.Equal(t, writeObjectKey, writes[0].ObjectKey)
}
