package userspace

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
)

func TestPostgresStoreSQLDoesNotUseSQLiteOnlySyntax(t *testing.T) {
	db, rec := newRecordingSQLDB(t)
	s := NewPostgresStore(db)

	cases := []struct {
		name string
		run  func() error
	}{
		{
			name: "UpsertPackage",
			run: func() error {
				return s.UpsertPackage(PackageRow{Slug: "foo", Kind: "mcp", Description: "PDF tables", Tags: []string{"invoice"}})
			},
		},
		{
			name: "GetPackage",
			run: func() error {
				_, err := s.GetPackage("foo")
				return err
			},
		},
		{
			name: "InsertVersion",
			run: func() error {
				return s.InsertVersion(VersionRow{
					Slug: "foo", Version: "1.0.0", CreatedInWorkspace: "ws-a", CreatedByAgentID: "agent-a",
					ManifestJSON: []byte(`{}`), SpecJSON: []byte(`{"name":"tool"}`), CardMD: "card",
					TarballSHA256: "sha", BlobSHA256: "sha",
				})
			},
		},
		{
			name: "GetVersion",
			run: func() error {
				_, err := s.GetVersion("foo", "1.0.0")
				return err
			},
		},
		{
			name: "ListVersions",
			run: func() error {
				_, err := s.ListVersions("foo")
				return err
			},
		},
		{
			name: "YankVersion",
			run: func() error {
				return s.YankVersion("foo", "1.0.0")
			},
		},
		{
			name: "UpsertInstallation",
			run: func() error {
				return s.UpsertInstallation(InstallationRow{
					WorkspaceID: "ws-a", Slug: "foo", InstalledVersion: "1.0.0", InstalledByAgent: "agent-a",
				})
			},
		},
		{
			name: "GetInstallation",
			run: func() error {
				_, _, err := s.GetInstallation("ws-a", "foo")
				return err
			},
		},
		{
			name: "ListInstallations",
			run: func() error {
				_, err := s.ListInstallations("ws-a")
				return err
			},
		},
		{
			name: "DeleteInstallation",
			run: func() error {
				return s.DeleteInstallation("ws-a", "foo")
			},
		},
		{
			name: "SearchPackages",
			run: func() error {
				_, err := s.SearchPackages("invoice", "ws-a", "mcp", 10)
				return err
			},
		},
		{
			name: "BlobRefcount",
			run: func() error {
				_, err := s.BlobRefcount("sha")
				return err
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec.Reset()
			require.NoError(t, tc.run())

			sqlText := rec.JoinedQueries()
			require.NotEmpty(t, sqlText)
			require.Contains(t, sqlText, "$1")
			require.NotContains(t, sqlText, "?")
			require.NotContains(t, strings.ToUpper(sqlText), "INSERT OR IGNORE")
			require.NotContains(t, sqlText, "userspace_pkg_fts")
			require.NotContains(t, strings.ToUpper(sqlText), " MATCH ")
		})
	}
}

func TestPostgresStoreSearchUsesTsvectorQuery(t *testing.T) {
	db, rec := newRecordingSQLDB(t)
	s := NewPostgresStore(db)

	_, err := s.SearchPackages("invoice", "ws-a", "mcp", 10)
	require.NoError(t, err)

	sqlText := rec.JoinedQueries()
	require.Contains(t, sqlText, "to_tsvector('simple'")
	require.Contains(t, sqlText, "plainto_tsquery('simple'")
	require.Contains(t, sqlText, "@@")
	require.NotContains(t, sqlText, "userspace_pkg_fts")
	require.NotContains(t, strings.ToUpper(sqlText), " MATCH ")
}

func TestNewStoreForDriverSelectsPostgresDialect(t *testing.T) {
	db, rec := newRecordingSQLDB(t)
	s, err := NewStoreForDriver(db, "postgres")
	require.NoError(t, err)

	_, err = s.SearchPackages("invoice", "ws-a", "mcp", 10)
	require.NoError(t, err)
	require.Contains(t, rec.JoinedQueries(), "plainto_tsquery('simple'")
}

func TestPostgresStoreLiveRoundTrip(t *testing.T) {
	dsn := userspacePostgresTestDSN(t)
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	defer db.Close()

	require.NoError(t, createUserspaceTestWorkspaceTable(db))
	require.NoError(t, MigratePostgres(db))

	s := NewPostgresStore(db)
	require.NoError(t, s.UpsertPackage(PackageRow{
		Slug: "invoice_extract", Kind: "mcp", Description: "PDF tables", Tags: []string{"invoice"},
	}))
	pkg, err := s.GetPackage("invoice_extract")
	require.NoError(t, err)
	require.Equal(t, []string{"invoice"}, pkg.Tags)

	_, err = db.Exec(`
		INSERT INTO userspace_blobs(sha256, size_bytes, object_key, refcount, created_at)
		VALUES($1, $2, $3, 1, $4)`, "sha-live", 10, "workspaces/userspace/blobs/sha-live", nowUTC())
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO workspaces(id) VALUES($1), ($2)`, "ws-a", "ws-b")
	require.NoError(t, err)

	version := VersionRow{
		Slug: "invoice_extract", Version: "1.0.0", CreatedInWorkspace: "ws-a", CreatedByAgentID: "agent-a",
		ManifestJSON: []byte(`{"slug":"invoice_extract"}`), SpecJSON: []byte(`{"name":"tool"}`),
		CardMD: "extracts invoice tables from pdf", TarballSHA256: "sha-live", BlobSHA256: "sha-live",
	}
	require.NoError(t, s.InsertVersion(version))
	require.ErrorIs(t, s.InsertVersion(version), ErrVersionExists)

	gotVersion, err := s.GetVersion("invoice_extract", "1.0.0")
	require.NoError(t, err)
	require.Equal(t, "sha-live", gotVersion.BlobSHA256)

	versions, err := s.ListVersions("invoice_extract")
	require.NoError(t, err)
	require.Len(t, versions, 1)

	results, err := s.SearchPackages("invoice", "ws-b", "mcp", 10)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "1.0.0", results[0].LatestVersion)

	require.NoError(t, s.UpsertInstallation(InstallationRow{
		WorkspaceID: "ws-b", Slug: "invoice_extract", InstalledVersion: "1.0.0", InstalledByAgent: "agent-b",
	}))
	installed, ok, err := s.GetInstallation("ws-b", "invoice_extract")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "1.0.0", installed)

	installations, err := s.ListInstallations("ws-b")
	require.NoError(t, err)
	require.Len(t, installations, 1)

	refcount, err := s.BlobRefcount("sha-live")
	require.NoError(t, err)
	require.Equal(t, 1, refcount)

	require.NoError(t, s.YankVersion("invoice_extract", "1.0.0"))
	results, err = s.SearchPackages("", "ws-a", "all", 10)
	require.NoError(t, err)
	require.Len(t, results, 0)

	require.NoError(t, s.DeleteInstallation("ws-b", "invoice_extract"))
	_, ok, err = s.GetInstallation("ws-b", "invoice_extract")
	require.NoError(t, err)
	require.False(t, ok)
}

type recordingSQLRecorder struct {
	mu      sync.Mutex
	queries []string
}

func (r *recordingSQLRecorder) Add(query string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queries = append(r.queries, query)
}

func (r *recordingSQLRecorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queries = nil
}

func (r *recordingSQLRecorder) JoinedQueries() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.queries, "\n")
}

var (
	recordingSQLOnce      sync.Once
	recordingSQLRecorders sync.Map
)

func newRecordingSQLDB(t *testing.T) (*sql.DB, *recordingSQLRecorder) {
	t.Helper()
	recordingSQLOnce.Do(func() {
		sql.Register("userspace_recording_sql", recordingSQLDriver{})
	})
	dsn := strings.ReplaceAll(t.Name(), "/", "_") + "_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	rec := &recordingSQLRecorder{}
	recordingSQLRecorders.Store(dsn, rec)
	t.Cleanup(func() {
		recordingSQLRecorders.Delete(dsn)
	})

	db, err := sql.Open("userspace_recording_sql", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db, rec
}

type recordingSQLDriver struct{}

func (recordingSQLDriver) Open(name string) (driver.Conn, error) {
	value, ok := recordingSQLRecorders.Load(name)
	if !ok {
		return nil, errors.New("recording sql recorder not found")
	}
	return &recordingSQLConn{rec: value.(*recordingSQLRecorder)}, nil
}

type recordingSQLConn struct {
	rec *recordingSQLRecorder
}

func (c *recordingSQLConn) Prepare(query string) (driver.Stmt, error) {
	return nil, errors.New("recording sql prepare is not implemented")
}

func (c *recordingSQLConn) Close() error { return nil }

func (c *recordingSQLConn) Begin() (driver.Tx, error) {
	return nil, errors.New("recording sql transactions are not implemented")
}

func (c *recordingSQLConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.rec.Add(query)
	return driver.RowsAffected(1), nil
}

func (c *recordingSQLConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.rec.Add(query)
	return emptyRecordingRows{}, nil
}

type emptyRecordingRows struct{}

func (emptyRecordingRows) Columns() []string { return []string{"unused"} }
func (emptyRecordingRows) Close() error      { return nil }
func (emptyRecordingRows) Next(dest []driver.Value) error {
	return io.EOF
}

func userspacePostgresTestDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("OBSERVER_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("set OBSERVER_POSTGRES_TEST_DSN to run PostgreSQL userspace integration tests")
	}

	schema := "userspace_test_" + strconv.Itoa(os.Getpid()) + "_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	adminDB, err := sql.Open("pgx", dsn)
	require.NoError(t, err)

	created := false
	t.Cleanup(func() {
		if created {
			_, err := adminDB.Exec(`DROP SCHEMA IF EXISTS ` + quotePostgresIdentifier(schema) + ` CASCADE`)
			require.NoError(t, err)
		}
		require.NoError(t, adminDB.Close())
	})

	_, err = adminDB.Exec(`CREATE SCHEMA ` + quotePostgresIdentifier(schema))
	require.NoError(t, err)
	created = true

	isolatedDSN, err := userspaceDSNWithSearchPath(dsn, schema)
	require.NoError(t, err)
	return isolatedDSN
}

func userspaceDSNWithSearchPath(dsn, schema string) (string, error) {
	if schema == "" {
		return "", errors.New("postgres test schema must not be empty")
	}
	lower := strings.ToLower(dsn)
	if strings.HasPrefix(lower, "postgres://") || strings.HasPrefix(lower, "postgresql://") {
		parsed, err := url.Parse(dsn)
		if err != nil {
			return "", err
		}
		q := parsed.Query()
		q.Set("search_path", schema)
		parsed.RawQuery = q.Encode()
		return parsed.String(), nil
	}

	trimmed := strings.TrimSpace(dsn)
	if trimmed == "" || !strings.Contains(trimmed, "=") {
		return "", errors.New("postgres test DSN must be a URL or keyword/value connection string")
	}
	return trimmed + " search_path=" + quotePostgresKeywordValue(schema), nil
}

func quotePostgresIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

func quotePostgresKeywordValue(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `'`, `\'`)
	return "'" + value + "'"
}

func createUserspaceTestWorkspaceTable(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE workspaces (id text PRIMARY KEY)`)
	return err
}
