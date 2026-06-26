package authstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/internal/identity"
)

// TestPostgresStore_NoSQLiteDialect_NoPlaintextSidLeak drives every
// postgresStore method against a recording driver and asserts:
//
//  1. No `?` placeholders (must be `$N`)
//  2. No SQLite-only constructs (INSERT OR REPLACE, AUTOINCREMENT, PRAGMA)
//  3. No `%s` / `%v`-shaped substring (would indicate post-Sprintf SQL)
//  4. SECURITY: no captured query string OR captured arg contains the
//     plaintext sentinel sid `dialectTestPlaintextSID` — only its sha256
//     hash is permitted. This is the structural test that hashSID() is the
//     single boundary for plaintext.
//
// Runs without OBSERVER_POSTGRES_TEST_DSN.
func TestPostgresStore_NoSQLiteDialect_NoPlaintextSidLeak(t *testing.T) {
	const dialectTestPlaintextSID = "DIALECT_TEST_PLAINTEXT_SID_SENTINEL"
	plaintextHash := hashSID(dialectTestPlaintextSID)

	db, rec := newRecordingSQLDB(t)
	s := NewPostgresStore(db)
	ctx := context.Background()
	mkIdentity := func() identity.Identity {
		return identity.Identity{
			UserID: "u", WorkspaceID: "w", Role: "member",
			Source: identity.SourceAgentserver,
		}
	}

	cases := []struct {
		name string
		run  func() error
	}{
		{"ReserveLogin", func() error {
			return s.ReserveLogin(ctx, "lid", time.Now(), 10*time.Minute)
		}},
		{"FinalizeReservedLogin", func() error {
			return s.FinalizeReservedLogin(ctx, "lid", "dc",
				time.Now().Add(5*time.Minute), 5)
		}},
		{"DeleteLogin", func() error {
			return s.DeleteLogin(ctx, "lid")
		}},
		{"GetLogin", func() error {
			_, err := s.GetLogin(ctx, "lid")
			// recordingSQLDB returns ErrNotFound (no rows); fine.
			if errors.Is(err, ErrNotFound) {
				err = nil
			}
			return err
		}},
		{"SetPollThrottle", func() error {
			return s.SetPollThrottle(ctx, "lid", 30, time.Now().Add(time.Minute))
		}},
		{"MarkLoginDone", func() error {
			err := s.MarkLoginDone(ctx, "lid", SessionRecord{
				PlaintextSessionID: dialectTestPlaintextSID, // PLAINTEXT SENTINEL
				Identity:           mkIdentity(),
				ExpiresAt:          time.Now().Add(12 * time.Hour),
			})
			// recordingSQLDB UPDATE returns RowsAffected=1 → no ErrNotFound; tx commit succeeds.
			return err
		}},
		{"MarkLoginFailed", func() error {
			err := s.MarkLoginFailed(ctx, "lid", FailureAuthorizationDenied)
			return err
		}},
		{"ConsumeLogin", func() error {
			_, err := s.ConsumeLogin(ctx, "lid")
			if errors.Is(err, ErrNotFound) {
				err = nil
			}
			return err
		}},
		{"GetSession", func() error {
			_, err := s.GetSession(ctx, dialectTestPlaintextSID) // PLAINTEXT SENTINEL
			if errors.Is(err, ErrNotFound) {
				err = nil
			}
			return err
		}},
		{"DeleteSession", func() error {
			return s.DeleteSession(ctx, dialectTestPlaintextSID) // PLAINTEXT SENTINEL
		}},
		{"SweepExpired", func() error {
			_, _, err := s.SweepExpired(ctx)
			return err
		}},
	}

	disallowedSubstrings := []string{
		"INSERT OR REPLACE",
		"INSERT OR IGNORE",
		"AUTOINCREMENT",
		"PRAGMA",
	}
	fmtSpec := regexp.MustCompile(`%[vsdfqxX]`)
	questionPlaceholder := regexp.MustCompile(`(?:^|[^$])\?\b`)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec.Reset()
			require.NoError(t, tc.run(), "method invocation")

			joined := rec.JoinedQueries()
			require.NotEmpty(t, joined, "no SQL captured for %s", tc.name)

			require.NotRegexp(t, questionPlaceholder, joined,
				"%s: must use $N placeholders, not ?", tc.name)
			require.NotRegexp(t, fmtSpec, joined,
				"%s: SQL contains %%-formatting, looks like Sprintf'd query", tc.name)
			upper := strings.ToUpper(joined)
			for _, bad := range disallowedSubstrings {
				require.NotContains(t, upper, bad,
					"%s: SQLite-only construct %q present", tc.name, bad)
			}

			// SECURITY: plaintext sid must never appear in queries OR args.
			require.NotContains(t, joined, dialectTestPlaintextSID,
				"%s: plaintext sid leaked into SQL query string", tc.name)
			for i, args := range rec.Args() {
				for j, a := range args {
					str := fmt.Sprintf("%v", a.Value)
					require.NotContains(t, str, dialectTestPlaintextSID,
						"%s: plaintext sid leaked into SQL arg (call #%d arg #%d = %q)",
						tc.name, i, j, str)
				}
			}
			// Sanity: when MarkLoginDone / GetSession / DeleteSession run,
			// the hash MUST appear somewhere (proves we did hash, not skip).
			if tc.name == "MarkLoginDone" || tc.name == "GetSession" || tc.name == "DeleteSession" {
				found := false
				for _, args := range rec.Args() {
					for _, a := range args {
						if fmt.Sprintf("%v", a.Value) == plaintextHash {
							found = true
							break
						}
					}
					if found {
						break
					}
				}
				require.True(t, found, "%s: expected sha256 hash in args", tc.name)
			}
		})
	}
}

// --- recording driver --------------------------------------------------------
//
// Captures every (query, args) tuple to the recorder. Supports BeginTx
// (commands like ReserveLogin and MarkLoginDone need it) by returning a
// transactional connection that funnels its writes through the same recorder.
// Returns RowsAffected=1 for ExecContext so guards-by-RowsAffected don't
// short-circuit; returns empty rows for QueryContext / QueryRowContext.

type recordingSQLRecorder struct {
	mu      sync.Mutex
	queries []string
	args    [][]driver.NamedValue
}

func (r *recordingSQLRecorder) Add(query string, args []driver.NamedValue) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queries = append(r.queries, query)
	// Copy args so subsequent driver reuses don't mutate captured slice.
	cp := make([]driver.NamedValue, len(args))
	copy(cp, args)
	r.args = append(r.args, cp)
}

func (r *recordingSQLRecorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queries = nil
	r.args = nil
}

func (r *recordingSQLRecorder) JoinedQueries() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.queries, "\n")
}

func (r *recordingSQLRecorder) Args() [][]driver.NamedValue {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][]driver.NamedValue, len(r.args))
	for i, a := range r.args {
		cp := make([]driver.NamedValue, len(a))
		copy(cp, a)
		out[i] = cp
	}
	return out
}

var (
	recordingSQLOnce      sync.Once
	recordingSQLRecorders sync.Map
)

func newRecordingSQLDB(t *testing.T) (*sql.DB, *recordingSQLRecorder) {
	t.Helper()
	recordingSQLOnce.Do(func() {
		sql.Register("authstore_recording_sql", recordingSQLDriver{})
	})
	dsn := fmt.Sprintf("authstore-rec-%s-%d", t.Name(), time.Now().UnixNano())
	dsn = strings.ReplaceAll(dsn, "/", "_")
	rec := &recordingSQLRecorder{}
	recordingSQLRecorders.Store(dsn, rec)
	t.Cleanup(func() { recordingSQLRecorders.Delete(dsn) })

	db, err := sql.Open("authstore_recording_sql", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db, rec
}

type recordingSQLDriver struct{}

func (recordingSQLDriver) Open(name string) (driver.Conn, error) {
	v, ok := recordingSQLRecorders.Load(name)
	if !ok {
		return nil, errors.New("recording sql recorder not found")
	}
	return &recordingSQLConn{rec: v.(*recordingSQLRecorder)}, nil
}

type recordingSQLConn struct {
	rec *recordingSQLRecorder
}

func (c *recordingSQLConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("recording sql prepare not implemented")
}

func (c *recordingSQLConn) Close() error { return nil }

// Begin is the legacy interface; database/sql prefers BeginTx (below).
func (c *recordingSQLConn) Begin() (driver.Tx, error) {
	return &recordingSQLTx{}, nil
}

func (c *recordingSQLConn) BeginTx(_ context.Context, _ driver.TxOptions) (driver.Tx, error) {
	return &recordingSQLTx{}, nil
}

func (c *recordingSQLConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.rec.Add(query, args)
	return driver.RowsAffected(1), nil
}

func (c *recordingSQLConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.rec.Add(query, args)
	return emptyRecordingRows{}, nil
}

type recordingSQLTx struct{}

func (recordingSQLTx) Commit() error   { return nil }
func (recordingSQLTx) Rollback() error { return nil }

type emptyRecordingRows struct{}

func (emptyRecordingRows) Columns() []string { return []string{"unused"} }
func (emptyRecordingRows) Close() error      { return nil }
func (emptyRecordingRows) Next([]driver.Value) error {
	return io.EOF
}
