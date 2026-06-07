package userspace

import (
	"database/sql"
	_ "embed"
	"strings"
)

//go:embed schema.sql
var schemaSQL string

// Migrate creates userspace tables if missing. Idempotent — safe to call
// on every observer-server startup. Does NOT touch observer's business tables.
func Migrate(db *sql.DB) error {
	if _, err := db.Exec(schemaSQL); err != nil {
		return err
	}
	if _, err := db.Exec(`ALTER TABLE userspace_package_versions ADD COLUMN visibility TEXT NOT NULL DEFAULT 'workspace'`); err != nil && !isDuplicateColumn(err) {
		return err
	}
	if _, err := db.Exec(`ALTER TABLE userspace_package_versions ADD COLUMN created_by_user_id TEXT NOT NULL DEFAULT ''`); err != nil && !isDuplicateColumn(err) {
		return err
	}
	return nil
}

func isDuplicateColumn(err error) bool {
	return err != nil && strings.Contains(err.Error(), "duplicate column")
}
