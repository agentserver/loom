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
