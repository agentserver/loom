package userspace

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/yourorg/multi-agent/internal/mcpmarket/manifest"
	"github.com/yourorg/multi-agent/internal/mcpmarket/pack"
)

// AgentResolver returns the workspace_id and agent_id authenticated by the
// observer agent-token middleware. observerweb provides the concrete impl.
type AgentResolver func(r *http.Request) (workspaceID, agentID string, ok bool)

// Handler holds wired-up dependencies for all /api/userspace/* routes.
type Handler struct {
	Store    *Store
	Blobs    *BlobStore
	Resolver AgentResolver
}

// PushResponse is the body returned by POST /api/userspace/packages.
type PushResponse struct {
	Slug       string `json:"slug"`
	Version    string `json:"version"`
	BlobSHA256 string `json:"blob_sha256"`
	Dedup      bool   `json:"dedup"`
}

func (h *Handler) push(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	wsID, agentID, ok := h.Resolver(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, pack.MaxCompressedBytes+1<<16)
	if err := r.ParseMultipartForm(pack.MaxCompressedBytes + 1<<16); err != nil {
		http.Error(w, "bad multipart: "+err.Error(), http.StatusBadRequest)
		return
	}
	manifestRaw := r.FormValue("manifest")
	if manifestRaw == "" {
		http.Error(w, "missing 'manifest' form field", http.StatusBadRequest)
		return
	}
	if len(manifestRaw) > 64*1024 {
		http.Error(w, "manifest too large", http.StatusRequestEntityTooLarge)
		return
	}
	mfp, err := manifest.Parse([]byte(manifestRaw))
	if err != nil {
		http.Error(w, "manifest parse: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := mfp.Validate(); err != nil {
		http.Error(w, "manifest invalid: "+err.Error(), http.StatusBadRequest)
		return
	}
	tarFile, _, err := r.FormFile("tarball")
	if err != nil {
		http.Error(w, "missing 'tarball' file field", http.StatusBadRequest)
		return
	}
	defer tarFile.Close()
	tarBytes, err := io.ReadAll(io.LimitReader(tarFile, pack.MaxCompressedBytes+1))
	if err != nil || len(tarBytes) > pack.MaxCompressedBytes {
		http.Error(w, "tarball read/oversize", http.StatusRequestEntityTooLarge)
		return
	}
	actual := ComputeSHA256Hex(tarBytes)
	prefix, files, err := pack.ReadTarball(tarBytes)
	if err != nil {
		http.Error(w, "unpack: "+err.Error(), http.StatusBadRequest)
		return
	}
	expectedPrefix := fmt.Sprintf("mcp-package-%s-%s", mfp.Slug, mfp.Version)
	if prefix != expectedPrefix {
		http.Error(w, fmt.Sprintf("prefix mismatch: got %q want %q", prefix, expectedPrefix), http.StatusBadRequest)
		return
	}
	specJSON, cardMD, err := extractRefs(files, mfp)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if existing, _ := h.Store.GetPackage(mfp.Slug); existing != nil && existing.Kind != string(mfp.Kind) {
		http.Error(w, fmt.Sprintf("kind mismatch: slug %s already registered as %s", mfp.Slug, existing.Kind), http.StatusBadRequest)
		return
	}
	existingRefcount := 0
	_ = h.Store.db.QueryRow(`SELECT refcount FROM userspace_blobs WHERE sha256=?`, actual).Scan(&existingRefcount)
	dedup := existingRefcount > 0
	if _, err := h.Blobs.Put(tarBytes); err != nil {
		http.Error(w, "blob put: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.Store.UpsertPackage(PackageRow{
		Slug: mfp.Slug, Kind: string(mfp.Kind),
		Description: "",
		Tags:        mfp.Tags,
	}); err != nil {
		http.Error(w, "upsert pkg: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.Store.InsertVersion(VersionRow{
		Slug: mfp.Slug, Version: mfp.Version,
		CreatedInWorkspace: wsID, CreatedByAgentID: agentID,
		ManifestJSON:  []byte(manifestRaw),
		SpecJSON:      specJSON,
		CardMD:        cardMD,
		TarballSHA256: actual, BlobSHA256: actual,
	}); err != nil {
		if errors.Is(err, ErrVersionExists) {
			http.Error(w, "version already exists", http.StatusConflict)
			return
		}
		http.Error(w, "insert version: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.Store.UpsertInstallation(InstallationRow{
		WorkspaceID: wsID, Slug: mfp.Slug,
		InstalledVersion: mfp.Version, InstalledByAgent: agentID,
	}); err != nil {
		http.Error(w, "upsert installation: "+err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("userspace: push slug=%s version=%s ws=%s agent=%s dedup=%v",
		mfp.Slug, mfp.Version, wsID, agentID, dedup)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(PushResponse{
		Slug: mfp.Slug, Version: mfp.Version, BlobSHA256: actual, Dedup: dedup,
	})
}

func extractRefs(files []pack.File, mfp *manifest.Manifest) (specJSON []byte, cardMD string, err error) {
	byPath := map[string]pack.File{}
	for _, f := range files {
		byPath[f.Path] = f
	}
	card, ok := byPath[mfp.CardRef]
	if !ok {
		return nil, "", fmt.Errorf("card_ref %q not in tarball", mfp.CardRef)
	}
	if len(card.Content) > 16*1024 {
		return nil, "", errors.New("card_md > 16 KiB")
	}
	cardMD = string(card.Content)
	if mfp.Kind == manifest.KindMCP {
		spec, ok := byPath[mfp.SpecRef]
		if !ok {
			return nil, "", fmt.Errorf("spec_ref %q not in tarball", mfp.SpecRef)
		}
		if len(spec.Content) > 32*1024 {
			return nil, "", errors.New("spec.json > 32 KiB")
		}
		specJSON = spec.Content
	}
	return specJSON, cardMD, nil
}

func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	wsID, _, ok := h.Resolver(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	q := r.URL.Query().Get("q")
	kind := r.URL.Query().Get("kind")
	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		fmt.Sscanf(l, "%d", &limit)
	}
	results, err := h.Store.SearchPackages(q, wsID, kind, limit)
	if err != nil {
		log.Printf("userspace: search error: %v", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if results == nil {
		results = []PackageView{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"results":     results,
		"search_mode": "fts5",
	})
}

func (h *Handler) getPackage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/userspace/packages/")
	if !strings.Contains(rest, "/") {
		pkg, err := h.Store.GetPackage(rest)
		if err != nil {
			http.Error(w, "internal", http.StatusInternalServerError)
			return
		}
		if pkg == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		versions, err := h.Store.ListVersions(rest)
		if err != nil {
			http.Error(w, "internal", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"package":  pkg,
			"versions": versions,
		})
		return
	}
	h.getVersionOrSource(w, r)
}

func (h *Handler) getVersionOrSource(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/userspace/packages/")
	parts := strings.SplitN(rest, "/", 4)
	if len(parts) < 3 || parts[1] != "versions" {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	slug, ver := parts[0], parts[2]
	v, err := h.Store.GetVersion(slug, ver)
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if v == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if len(parts) == 4 && parts[3] == "source.tar.gz" {
		rc, sz, err := h.Blobs.Open(v.BlobSHA256)
		if err != nil {
			http.Error(w, "blob missing", http.StatusGone)
			return
		}
		defer rc.Close()
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", sz))
		w.Header().Set("ETag", `"`+v.TarballSHA256+`"`)
		_, _ = io.Copy(w, rc)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"slug":                 v.Slug,
		"version":              v.Version,
		"created_in_workspace": v.CreatedInWorkspace,
		"created_by_agent_id":  v.CreatedByAgentID,
		"manifest":             json.RawMessage(v.ManifestJSON),
		"card_md":              v.CardMD,
		"tarball_sha256":       v.TarballSHA256,
		"status":               v.Status,
	})
}

func (h *Handler) installVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	wsID, agentID, ok := h.Resolver(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/userspace/workspaces/")
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) != 3 || parts[1] != "installations" {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	pathWS, slug := parts[0], parts[2]
	if pathWS != wsID {
		http.Error(w, "cross-workspace write not allowed", http.StatusForbidden)
		return
	}
	if r.Method == http.MethodDelete {
		if err := h.Store.DeleteInstallation(wsID, slug); err != nil {
			http.Error(w, "internal", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var body struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Version == "" {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	v, err := h.Store.GetVersion(slug, body.Version)
	if err != nil || v == nil {
		http.Error(w, "version not found", http.StatusNotFound)
		return
	}
	if err := h.Store.UpsertInstallation(InstallationRow{
		WorkspaceID: wsID, Slug: slug,
		InstalledVersion: body.Version, InstalledByAgent: agentID,
	}); err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) yankVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, _, ok := h.Resolver(r); !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/userspace/packages/")
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) != 3 || parts[1] != "yank" {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	slug, ver := parts[0], parts[2]
	if err := h.Store.YankVersion(slug, ver); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "not found or already yanked", http.StatusNotFound)
			return
		}
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listPackages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	wsID, _, ok := h.Resolver(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	scope := r.URL.Query().Get("workspace")
	if scope == "" {
		scope = "mine"
	}
	kind := r.URL.Query().Get("kind")
	results, err := h.Store.SearchPackages("", wsID, kind, 100)
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if scope == "mine" {
		filtered := results[:0]
		for _, p := range results {
			if p.InstalledVersion != "" {
				filtered = append(filtered, p)
			}
		}
		results = filtered
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"packages": results})
}
