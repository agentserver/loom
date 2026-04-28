package store

import (
	"database/sql"
	_ "embed"
	"fmt"
	"sync"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

type Store struct {
	db   *sql.DB
	mu   sync.Mutex
	subs map[string][]chan Event
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{db: db, subs: make(map[string][]chan Event)}, nil
}

func (s *Store) Close() error      { return s.db.Close() }
func (s *Store) DB() *sql.DB       { return s.db } // test-only accessor
