package userspace

import (
	"database/sql"
	_ "embed"
)

//go:embed schema.sql
var schemaSQL string

// Migrate creates userspace tables if missing. Idempotent — safe to call
// on every observer-server startup. Does NOT touch observer's business tables.
func Migrate(db *sql.DB) error {
	_, err := db.Exec(schemaSQL)
	return err
}
