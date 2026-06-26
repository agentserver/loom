package authstore

import (
	"database/sql"
	_ "embed"
)

//go:embed schema_postgres.sql
var schemaPostgresSQL string

// MigratePostgres creates commander_logins + commander_sessions if missing.
// Idempotent — every observer-server startup re-runs it; helm's
// migration-job also runs it via `observer-server --migrate-only`.
func MigratePostgres(db *sql.DB) error {
	_, err := db.Exec(schemaPostgresSQL)
	return err
}
