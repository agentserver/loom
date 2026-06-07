package postgres

import (
	"database/sql"
	_ "embed"
)

//go:embed schema.sql
var schemaSQL string

func migrate(db *sql.DB) error {
	if _, err := db.Exec(schemaSQL); err != nil {
		return err
	}
	if _, err := db.Exec(`ALTER TABLE workspaces ADD COLUMN IF NOT EXISTS external_user_id text NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if _, err := db.Exec(`ALTER TABLE agents ADD COLUMN IF NOT EXISTS external_sandbox_id text NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if _, err := db.Exec(`ALTER TABLE agents ADD COLUMN IF NOT EXISTS external_user_id text NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	return nil
}
