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
	// last_seen_at: bumped by every (re)registration so register's duplicate
	// takeover guard can tell "fresh slot" from "live agent". §1.3 #11.
	if _, err := db.Exec(`ALTER TABLE agents ADD COLUMN IF NOT EXISTS last_seen_at text NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	return nil
}
