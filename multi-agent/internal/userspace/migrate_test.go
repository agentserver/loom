package userspace

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	// userspace schema references workspaces(id); create a stub.
	_, err = db.Exec(`CREATE TABLE workspaces (id TEXT PRIMARY KEY)`)
	require.NoError(t, err)
	require.NoError(t, Migrate(db))
	return db
}

func TestMigrate_Idempotent(t *testing.T) {
	db := newTestDB(t)
	require.NoError(t, Migrate(db)) // second run
	require.NoError(t, Migrate(db)) // third run
}

func TestMigrate_CreatesAllTables(t *testing.T) {
	db := newTestDB(t)
	for _, table := range []string{
		"userspace_packages",
		"userspace_package_versions",
		"userspace_workspace_installations",
		"userspace_blobs",
		"userspace_pkg_fts",
	} {
		var name string
		err := db.QueryRow(`SELECT name FROM sqlite_master WHERE name=?`, table).Scan(&name)
		require.NoError(t, err, "table %s missing", table)
	}
}

func TestMigrateSQLiteAddsObjectKeyColumn(t *testing.T) {
	db := newTestDB(t)
	rows, err := db.Query(`PRAGMA table_info(userspace_blobs)`)
	require.NoError(t, err)
	defer rows.Close()

	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		require.NoError(t, rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk))
		columns[name] = true
	}
	require.NoError(t, rows.Err())
	require.True(t, columns["blob_path"])
	require.True(t, columns["object_key"])
}
