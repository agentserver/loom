package userspace

import (
	"database/sql"
	_ "embed"
	"fmt"
	"strings"
)

//go:embed schema.sql
var schemaSQL string

//go:embed schema_postgres.sql
var schemaPostgresSQL string

// Migrate creates userspace tables if missing. Idempotent — safe to call
// on every observer-server startup. Does NOT touch observer's business tables.
func Migrate(db *sql.DB) error {
	return MigrateSQLite(db)
}

func MigrateSQLite(db *sql.DB) error {
	_, err := db.Exec(schemaSQL)
	if err != nil {
		return err
	}
	return ensureSQLiteObjectKeyColumn(db)
}

func MigratePostgres(db *sql.DB) error {
	_, err := db.Exec(schemaPostgresSQL)
	return err
}

func MigrateForDriver(db *sql.DB, driver string) error {
	switch driver {
	case "", "sqlite":
		return MigrateSQLite(db)
	case "postgres", "pgx":
		return MigratePostgres(db)
	default:
		return fmt.Errorf("userspace: unsupported database driver %q", driver)
	}
}

func ensureSQLiteObjectKeyColumn(db *sql.DB) error {
	_, err := db.Exec(`ALTER TABLE userspace_blobs ADD COLUMN object_key TEXT NOT NULL DEFAULT ''`)
	if err != nil && !isDuplicateColumnError(err) {
		return err
	}
	return nil
}

func isDuplicateColumnError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "duplicate column")
}
