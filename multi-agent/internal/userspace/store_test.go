package userspace

import (
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
	_, _ = db.Exec(`INSERT INTO workspaces(id) VALUES('ws-a')`)
	_, _ = db.Exec(`INSERT INTO userspace_blobs(sha256,size_bytes,blob_path,created_at) VALUES('h1',10,'p1',?)`, nowUTC())
	require.NoError(t, s.UpsertPackage(PackageRow{Slug: "foo", Kind: "mcp"}))
	require.NoError(t, s.InsertVersion(VersionRow{Slug: "foo", Version: "1.0.0",
		CreatedInWorkspace: "ws-a", CreatedByAgentID: "x", ManifestJSON: []byte(`{}`),
		CardMD: "c", TarballSHA256: "h1", BlobSHA256: "h1"}))
	require.NoError(t, s.YankVersion("foo", "1.0.0"))
	results, err := s.SearchPackages("", "ws-a", "all", 10)
	require.NoError(t, err)
	require.Len(t, results, 1) // package row still there
	require.Equal(t, "", results[0].LatestVersion, "yanked version no longer the latest ready")
}
