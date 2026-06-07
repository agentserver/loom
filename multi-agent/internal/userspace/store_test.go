package userspace

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpsertPackage_DescriptionAndTagsUpdate(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	require.NoError(t, s.UpsertPackage(PackageRow{Slug: "foo", Kind: "mcp", Description: "first"}))
	require.NoError(t, s.UpsertPackage(PackageRow{Slug: "foo", Kind: "mcp", Description: "second", Tags: []string{"a"}}))
	p, err := s.GetPackage("foo")
	require.NoError(t, err)
	require.Equal(t, "mcp", p.Kind)
	require.Equal(t, "second", p.Description)
	require.Equal(t, []string{"a"}, p.Tags)
}

func TestInsertVersion_ConflictReturnsErrVersionExists(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	_, err := db.Exec(`INSERT INTO workspaces(id) VALUES('ws-a')`)
	require.NoError(t, err)
	require.NoError(t, s.UpsertPackage(PackageRow{Slug: "foo", Kind: "mcp"}))
	_, err = db.Exec(`INSERT INTO userspace_blobs(sha256,size_bytes,blob_path,created_at) VALUES('h1',10,'p1',?)`, nowUTC())
	require.NoError(t, err)
	v := VersionRow{Slug: "foo", Version: "1.0.0", CreatedInWorkspace: "ws-a", CreatedByAgentID: "a1",
		ManifestJSON: []byte(`{}`), CardMD: "card", TarballSHA256: "h1", BlobSHA256: "h1"}
	require.NoError(t, s.InsertVersion(v))
	require.ErrorIs(t, s.InsertVersion(v), ErrVersionExists)
}

func TestSchemaIncludesVisibilityColumns(t *testing.T) {
	db := newTestDB(t)
	cols := userspaceTableColumns(t, db, "userspace_package_versions")
	require.Contains(t, cols, "visibility")
	require.Contains(t, cols, "created_by_user_id")
}

func TestVersionVisibilityRoundTrip(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	_, err := db.Exec(`INSERT INTO workspaces(id) VALUES('ws-a')`)
	require.NoError(t, err)
	require.NoError(t, s.UpsertPackage(PackageRow{Slug: "foo", Kind: "mcp"}))
	_, err = db.Exec(`INSERT INTO userspace_blobs(sha256,size_bytes,blob_path,created_at) VALUES('h1',10,'p1',?)`, nowUTC())
	require.NoError(t, err)

	require.NoError(t, s.InsertVersion(VersionRow{Slug: "foo", Version: "1.0.0",
		CreatedInWorkspace: "ws-a", CreatedByAgentID: "a1", CreatedByUserID: "user-1",
		Visibility: "user", ManifestJSON: []byte(`{}`), CardMD: "card",
		TarballSHA256: "h1", BlobSHA256: "h1"}))

	v, err := s.GetVersion("foo", "1.0.0")
	require.NoError(t, err)
	require.Equal(t, "user", v.Visibility)
	require.Equal(t, "user-1", v.CreatedByUserID)
}

func TestVisibilityFiltering(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	_, err := db.Exec(`INSERT INTO workspaces(id) VALUES('ws-a'),('ws-b')`)
	require.NoError(t, err)
	require.NoError(t, s.UpsertPackage(PackageRow{Slug: "workspace_pkg", Kind: "mcp"}))
	require.NoError(t, s.UpsertPackage(PackageRow{Slug: "user_pkg", Kind: "mcp"}))
	require.NoError(t, s.UpsertPackage(PackageRow{Slug: "public_pkg", Kind: "mcp"}))
	for _, h := range []string{"h1", "h2", "h3"} {
		_, err = db.Exec(`INSERT INTO userspace_blobs(sha256,size_bytes,blob_path,created_at) VALUES(?,10,?,?)`, h, h, nowUTC())
		require.NoError(t, err)
	}
	require.NoError(t, s.InsertVersion(VersionRow{Slug: "workspace_pkg", Version: "1.0.0",
		CreatedInWorkspace: "ws-a", CreatedByAgentID: "agent-a",
		ManifestJSON: []byte(`{}`), CardMD: "workspace", TarballSHA256: "h1", BlobSHA256: "h1"}))
	require.NoError(t, s.InsertVersion(VersionRow{Slug: "user_pkg", Version: "1.0.0",
		CreatedInWorkspace: "ws-a", CreatedByAgentID: "agent-a", CreatedByUserID: "user-1", Visibility: "user",
		ManifestJSON: []byte(`{}`), CardMD: "user", TarballSHA256: "h2", BlobSHA256: "h2"}))
	require.NoError(t, s.InsertVersion(VersionRow{Slug: "public_pkg", Version: "1.0.0",
		CreatedInWorkspace: "ws-a", CreatedByAgentID: "agent-a", Visibility: "public",
		ManifestJSON: []byte(`{}`), CardMD: "public", TarballSHA256: "h3", BlobSHA256: "h3"}))

	wsA, err := s.SearchPackagesForIdentity("", "ws-a", "user-2", "all", 10)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"workspace_pkg", "public_pkg"}, packageSlugs(wsA))

	wsBUser1, err := s.SearchPackagesForIdentity("", "ws-b", "user-1", "all", 10)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"user_pkg", "public_pkg"}, packageSlugs(wsBUser1))

	wsBUser2, err := s.SearchPackagesForIdentity("", "ws-b", "user-2", "all", 10)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"public_pkg"}, packageSlugs(wsBUser2))
}

func TestInstallation_RoundTrip(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	_, err := db.Exec(`INSERT INTO workspaces(id) VALUES('ws-a'),('ws-b')`)
	require.NoError(t, err)
	require.NoError(t, s.UpsertPackage(PackageRow{Slug: "foo", Kind: "mcp"}))
	_, err = db.Exec(`INSERT INTO userspace_blobs(sha256,size_bytes,blob_path,created_at) VALUES('h1',10,'p1',?)`, nowUTC())
	require.NoError(t, err)
	require.NoError(t, s.InsertVersion(VersionRow{Slug: "foo", Version: "1.0.0",
		CreatedInWorkspace: "ws-a", CreatedByAgentID: "a1",
		ManifestJSON: []byte(`{}`), CardMD: "x",
		TarballSHA256: "h1", BlobSHA256: "h1"}))
	require.NoError(t, s.UpsertInstallation(InstallationRow{
		WorkspaceID: "ws-b", Slug: "foo", InstalledVersion: "1.0.0", InstalledByAgent: "a-b"}))
	v, ok, err := s.GetInstallation("ws-b", "foo")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "1.0.0", v)
	_, ok2, _ := s.GetInstallation("ws-a", "foo")
	require.False(t, ok2)
}

func packageSlugs(pkgs []PackageView) []string {
	out := make([]string, 0, len(pkgs))
	for _, pkg := range pkgs {
		out = append(out, pkg.Slug)
	}
	return out
}

func TestSearchPackages_FTSFindsByCardMD(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	_, err := db.Exec(`INSERT INTO workspaces(id) VALUES('ws-a')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO userspace_blobs(sha256,size_bytes,blob_path,created_at) VALUES('h1',10,'p1',?)`, nowUTC())
	require.NoError(t, err)
	require.NoError(t, s.UpsertPackage(PackageRow{Slug: "invoice_extract", Kind: "mcp", Description: "PDF tables"}))
	require.NoError(t, s.InsertVersion(VersionRow{Slug: "invoice_extract", Version: "1.0.0",
		CreatedInWorkspace: "ws-a", CreatedByAgentID: "x", ManifestJSON: []byte(`{}`),
		CardMD:        "extracts invoice tables from pdf",
		TarballSHA256: "h1", BlobSHA256: "h1"}))
	results, err := s.SearchPackages("invoice", "ws-a", "mcp", 10)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "invoice_extract", results[0].Slug)
	require.Equal(t, "1.0.0", results[0].LatestVersion)
	require.Equal(t, "", results[0].InstalledVersion)
}

func TestYankVersion_HidesFromLatest(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	_, _ = db.Exec(`INSERT INTO workspaces(id) VALUES('ws-a'),('ws-b')`)
	_, _ = db.Exec(`INSERT INTO userspace_blobs(sha256,size_bytes,blob_path,created_at) VALUES('h1',10,'p1',?)`, nowUTC())
	require.NoError(t, s.UpsertPackage(PackageRow{Slug: "foo", Kind: "mcp"}))
	require.NoError(t, s.InsertVersion(VersionRow{Slug: "foo", Version: "1.0.0",
		CreatedInWorkspace: "ws-a", CreatedByAgentID: "x", ManifestJSON: []byte(`{}`),
		CardMD: "c", TarballSHA256: "h1", BlobSHA256: "h1"}))
	// ws-b installs before yank
	require.NoError(t, s.UpsertInstallation(InstallationRow{
		WorkspaceID: "ws-b", Slug: "foo", InstalledVersion: "1.0.0", InstalledByAgent: "x"}))
	require.NoError(t, s.YankVersion("foo", "1.0.0"))

	// ws-a never installed — ghost slug is suppressed
	resultsA, err := s.SearchPackages("", "ws-a", "all", 10)
	require.NoError(t, err)
	require.Len(t, resultsA, 0, "ws-a never installed: ghost slug must be hidden")

	// ws-b still has it installed — package row still appears with empty latest_version
	resultsB, err := s.SearchPackages("", "ws-b", "all", 10)
	require.NoError(t, err)
	require.Len(t, resultsB, 1, "ws-b installed it: should still appear")
	require.Equal(t, "", resultsB[0].LatestVersion, "yanked version no longer the latest ready")
	require.Equal(t, "1.0.0", resultsB[0].InstalledVersion, "installed_version preserved")
}

func userspaceTableColumns(t *testing.T, db interface {
	Query(query string, args ...any) (*sql.Rows, error)
}, table string) map[string]bool {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	require.NoError(t, err)
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue interface{}
		var pk int
		require.NoError(t, rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk))
		out[name] = true
	}
	require.NoError(t, rows.Err())
	return out
}
