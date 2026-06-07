package userspace

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Store wraps the userspace tables. It deliberately does NOT expose the
// raw *sql.DB so callers can't accidentally read observer's business tables.
type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// PackageRow mirrors userspace_packages.
type PackageRow struct {
	Slug        string
	Kind        string
	Description string
	Tags        []string
	CreatedAt   string
	UpdatedAt   string
}

// VersionRow mirrors userspace_package_versions.
type VersionRow struct {
	Slug               string
	Version            string
	CreatedInWorkspace string
	CreatedByAgentID   string
	CreatedByUserID    string
	ManifestJSON       []byte
	SpecJSON           []byte // may be nil for kind=skill
	CardMD             string
	TarballSHA256      string
	BlobSHA256         string
	Status             string
	Visibility         string
	CreatedAt          string
}

// InstallationRow mirrors userspace_workspace_installations.
type InstallationRow struct {
	WorkspaceID      string
	Slug             string
	InstalledVersion string
	InstalledAt      string
	InstalledByAgent string
}

// PackageView is the search/list output shape; description comes from the
// owning package, installed_version from the requesting workspace.
type PackageView struct {
	Slug             string   `json:"slug"`
	Kind             string   `json:"kind"`
	Description      string   `json:"description"`
	Tags             []string `json:"tags"`
	LatestVersion    string   `json:"latest_version"`
	InstalledVersion string   `json:"installed_version,omitempty"`
}

var ErrVersionExists = errors.New("userspace: version already exists")

// UpsertPackage inserts a new row or updates description/tags/updated_at.
// Kind is INSERT-only — once set for a slug, conflict updates do not change it
// (caller responsible for rejecting kind mismatch upstream).
func (s *Store) UpsertPackage(p PackageRow) error {
	if p.Slug == "" {
		return errors.New("userspace: slug required")
	}
	tagsJSON, err := json.Marshal(p.Tags)
	if err != nil {
		return err
	}
	if p.Tags == nil {
		tagsJSON = []byte("[]")
	}
	now := nowUTC()
	_, err = s.db.Exec(`
		INSERT INTO userspace_packages(slug, kind, description, tags_json, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(slug) DO UPDATE SET
		    description = excluded.description,
		    tags_json   = excluded.tags_json,
		    updated_at  = excluded.updated_at`,
		p.Slug, p.Kind, p.Description, string(tagsJSON), now, now)
	return err
}

// GetPackage returns the package row or (nil, nil) if not found.
func (s *Store) GetPackage(slug string) (*PackageRow, error) {
	var p PackageRow
	var tagsJSON string
	err := s.db.QueryRow(`
		SELECT slug, kind, description, tags_json, created_at, updated_at
		  FROM userspace_packages WHERE slug=?`, slug,
	).Scan(&p.Slug, &p.Kind, &p.Description, &tagsJSON, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(tagsJSON), &p.Tags); err != nil {
		return nil, fmt.Errorf("userspace: parse tags_json: %w", err)
	}
	return &p, nil
}

// InsertVersion inserts a new version row. Conflict on (slug, version) returns
// ErrVersionExists. Caller must have already inserted the matching blob row
// (FK constraint on blob_sha256).
func (s *Store) InsertVersion(v VersionRow) error {
	if v.Slug == "" || v.Version == "" {
		return errors.New("userspace: slug + version required")
	}
	if v.Status == "" {
		v.Status = "ready"
	}
	if v.Visibility == "" {
		v.Visibility = "workspace"
	}
	v.CreatedAt = nowUTC()
	res, err := s.db.Exec(`
		INSERT OR IGNORE INTO userspace_package_versions
		  (slug, version, created_in_workspace, created_by_agent_id,
		   manifest_json, spec_json, card_md, tarball_sha256, blob_sha256,
		   status, visibility, created_by_user_id, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		v.Slug, v.Version, v.CreatedInWorkspace, v.CreatedByAgentID,
		string(v.ManifestJSON), nullIfEmpty(v.SpecJSON), v.CardMD,
		v.TarballSHA256, v.BlobSHA256, v.Status, v.Visibility, v.CreatedByUserID, v.CreatedAt)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrVersionExists
	}
	// Mirror into FTS5 for search.
	_, err = s.db.Exec(`
		INSERT INTO userspace_pkg_fts(slug, description, card_md)
		VALUES(?, ?, ?)`, v.Slug, v.CardMD, v.CardMD)
	return err
}

func (s *Store) GetVersion(slug, version string) (*VersionRow, error) {
	var v VersionRow
	var specJSON sql.NullString
	err := s.db.QueryRow(`
		SELECT slug, version, created_in_workspace, created_by_agent_id,
		       manifest_json, spec_json, card_md, tarball_sha256, blob_sha256,
		       status, visibility, created_by_user_id, created_at
		  FROM userspace_package_versions WHERE slug=? AND version=?`,
		slug, version,
	).Scan(&v.Slug, &v.Version, &v.CreatedInWorkspace, &v.CreatedByAgentID,
		&v.ManifestJSON, &specJSON, &v.CardMD, &v.TarballSHA256, &v.BlobSHA256,
		&v.Status, &v.Visibility, &v.CreatedByUserID, &v.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if specJSON.Valid {
		v.SpecJSON = []byte(specJSON.String)
	}
	return &v, nil
}

func (s *Store) GetVisibleVersion(slug, version, workspaceID, userID string) (*VersionRow, error) {
	v, err := s.GetVersion(slug, version)
	if err != nil || v == nil {
		return v, err
	}
	if !versionVisibleTo(v, workspaceID, userID) {
		return nil, nil
	}
	return v, nil
}

// ListVersions returns all versions for a slug, newest first by created_at.
func (s *Store) ListVersions(slug string) ([]VersionRow, error) {
	return s.ListVersionsForIdentity(slug, "", "")
}

func (s *Store) ListVersionsForIdentity(slug, workspaceID, userID string) ([]VersionRow, error) {
	rows, err := s.db.Query(`
		SELECT slug, version, created_in_workspace, created_by_agent_id,
		       tarball_sha256, blob_sha256, status, visibility, created_by_user_id, created_at
		  FROM userspace_package_versions WHERE slug=?
		 ORDER BY created_at DESC`, slug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VersionRow
	for rows.Next() {
		var v VersionRow
		if err := rows.Scan(&v.Slug, &v.Version, &v.CreatedInWorkspace,
			&v.CreatedByAgentID, &v.TarballSHA256, &v.BlobSHA256,
			&v.Status, &v.Visibility, &v.CreatedByUserID, &v.CreatedAt); err != nil {
			return nil, err
		}
		if workspaceID == "" || versionVisibleTo(&v, workspaceID, userID) {
			out = append(out, v)
		}
	}
	return out, rows.Err()
}

// YankVersion soft-deletes a version (search hides it; installs unaffected).
func (s *Store) YankVersion(slug, version string) error {
	res, err := s.db.Exec(
		`UPDATE userspace_package_versions SET status='yanked'
		 WHERE slug=? AND version=? AND status='ready'`, slug, version)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// UpsertInstallation sets the workspace's currently-installed version for slug.
func (s *Store) UpsertInstallation(in InstallationRow) error {
	in.InstalledAt = nowUTC()
	_, err := s.db.Exec(`
		INSERT INTO userspace_workspace_installations
		  (workspace_id, slug, installed_version, installed_at, installed_by_agent_id)
		VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(workspace_id, slug) DO UPDATE SET
		    installed_version     = excluded.installed_version,
		    installed_at          = excluded.installed_at,
		    installed_by_agent_id = excluded.installed_by_agent_id`,
		in.WorkspaceID, in.Slug, in.InstalledVersion,
		in.InstalledAt, in.InstalledByAgent)
	return err
}

// GetInstallation returns this workspace's installed version of slug, or
// ("", false, nil) if not installed.
func (s *Store) GetInstallation(workspaceID, slug string) (string, bool, error) {
	var v string
	err := s.db.QueryRow(`
		SELECT installed_version FROM userspace_workspace_installations
		 WHERE workspace_id=? AND slug=?`, workspaceID, slug).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// ListInstallations returns all packages installed in the given workspace.
func (s *Store) ListInstallations(workspaceID string) ([]InstallationRow, error) {
	rows, err := s.db.Query(`
		SELECT workspace_id, slug, installed_version, installed_at, installed_by_agent_id
		  FROM userspace_workspace_installations
		 WHERE workspace_id=?
		 ORDER BY installed_at DESC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InstallationRow
	for rows.Next() {
		var in InstallationRow
		if err := rows.Scan(&in.WorkspaceID, &in.Slug, &in.InstalledVersion,
			&in.InstalledAt, &in.InstalledByAgent); err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

func (s *Store) DeleteInstallation(workspaceID, slug string) error {
	_, err := s.db.Exec(
		`DELETE FROM userspace_workspace_installations
		 WHERE workspace_id=? AND slug=?`, workspaceID, slug)
	return err
}

// SearchPackages runs the FTS5 query and returns up to limit results, each
// joined with the latest version + caller's installed_version (if any).
// q="" lists all packages.
func (s *Store) SearchPackages(q, workspaceID, kindFilter string, limit int) ([]PackageView, error) {
	return s.SearchPackagesForIdentity(q, workspaceID, "", kindFilter, limit)
}

func (s *Store) SearchPackagesForIdentity(q, workspaceID, userID, kindFilter string, limit int) ([]PackageView, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	args := []any{}
	where := []string{}
	from := "userspace_packages p"
	if q != "" {
		from = `userspace_pkg_fts f JOIN userspace_packages p ON p.slug = f.slug`
		where = append(where, `f.userspace_pkg_fts MATCH ?`)
		args = append(args, q)
	}
	if kindFilter != "" && kindFilter != "all" {
		where = append(where, `p.kind = ?`)
		args = append(args, kindFilter)
	}
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = "WHERE " + joinAnd(where)
	}
	query := fmt.Sprintf(`
		SELECT p.slug, p.kind, p.description, p.tags_json,
		       COALESCE((SELECT version FROM userspace_package_versions v
		                  WHERE v.slug=p.slug AND v.status='ready'
		                    AND %s
		                  ORDER BY v.created_at DESC LIMIT 1), '') AS latest_version,
		       COALESCE((SELECT installed_version FROM userspace_workspace_installations i
		                  WHERE i.workspace_id=? AND i.slug=p.slug), '') AS installed_version
		  FROM %s %s
		 ORDER BY p.updated_at DESC
		 LIMIT ?`, visibleVersionSQL("v"), from, whereSQL)
	finalArgs := append([]any{workspaceID, userID, workspaceID}, args...)
	finalArgs = append(finalArgs, limit)
	rows, err := s.db.Query(query, finalArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PackageView
	for rows.Next() {
		var pv PackageView
		var tagsJSON string
		if err := rows.Scan(&pv.Slug, &pv.Kind, &pv.Description, &tagsJSON,
			&pv.LatestVersion, &pv.InstalledVersion); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(tagsJSON), &pv.Tags)
		out = append(out, pv)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Suppress ghost slugs whose only versions are yanked (no ready latest).
	// Empty installed_version means caller's workspace isn't tracking it either.
	filtered := out[:0]
	for _, pv := range out {
		if pv.LatestVersion != "" || pv.InstalledVersion != "" {
			filtered = append(filtered, pv)
		}
	}
	out = filtered
	return out, nil
}

func visibleVersionSQL(alias string) string {
	return fmt.Sprintf(`(%s.visibility='public'
		OR (%s.visibility='workspace' AND %s.created_in_workspace=?)
		OR (%s.visibility='user' AND %s.created_by_user_id<>'' AND %s.created_by_user_id=?))`,
		alias, alias, alias, alias, alias, alias)
}

func versionVisibleTo(v *VersionRow, workspaceID, userID string) bool {
	switch v.Visibility {
	case "", "workspace":
		return v.CreatedInWorkspace == workspaceID
	case "user":
		return v.CreatedByUserID != "" && v.CreatedByUserID == userID
	case "public":
		return true
	default:
		return false
	}
}

// BlobRefcount returns the current refcount for a blob sha; 0 if no row.
func (s *Store) BlobRefcount(sha256hex string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT refcount FROM userspace_blobs WHERE sha256=?`, sha256hex).Scan(&n)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return n, err
}

func nullIfEmpty(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}

func joinAnd(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " AND "
		}
		out += p
	}
	return out
}
