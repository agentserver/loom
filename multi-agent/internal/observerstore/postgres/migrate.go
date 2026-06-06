package postgres

import (
	"database/sql"
	_ "embed"
)

//go:embed schema.sql
var schemaSQL string

func migrate(db *sql.DB) error {
	_, err := db.Exec(schemaSQL)
	return err
}
