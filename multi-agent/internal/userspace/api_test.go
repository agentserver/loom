package userspace

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/mcpmarket/manifest"
	"github.com/yourorg/multi-agent/internal/mcpmarket/pack"
)

func newTestHandler(t *testing.T) (*Handler, *httptest.Server) {
	t.Helper()
	db := newTestDB(t)
	// Insert workspaces needed by FK constraint in userspace_workspace_installations.
	for _, ws := range []string{"ws-a", "ws-b", "ws-OTHER"} {
		_, err := db.Exec(`INSERT OR IGNORE INTO workspaces(id) VALUES(?)`, ws)
		require.NoError(t, err)
	}
	store := NewStore(db)
	blobs, err := NewBlobStore(db, t.TempDir())
	require.NoError(t, err)
	resolver := func(r *http.Request) (Identity, bool) {
		ws := r.Header.Get("X-Test-WS")
		ag := r.Header.Get("X-Test-Agent")
		return Identity{WorkspaceID: ws, AgentID: ag, UserID: r.Header.Get("X-Test-User")}, ws != "" && ag != ""
	}
	h := &Handler{Store: store, Blobs: blobs, Resolver: resolver}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/userspace/packages", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			h.push(w, r)
		} else {
			h.listPackages(w, r)
		}
	})
	mux.HandleFunc("/api/userspace/search", h.search)
	mux.HandleFunc("/api/userspace/packages/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			h.routePackagePost(w, r)
		} else {
			h.getPackage(w, r)
		}
	})
	mux.HandleFunc("/api/userspace/workspaces/", h.installVersion)
	return h, httptest.NewServer(mux)
}

func buildPushBody(t *testing.T, kind manifest.Kind, slug, version string) (*bytes.Buffer, string) {
	t.Helper()
	files := []pack.File{
		{Path: "capability_card.md", Content: []byte("# card")},
	}
	if kind == manifest.KindMCP {
		files = append(files,
			pack.File{Path: "spec.json", Content: []byte(`{"name":"x","version":1}`)},
			pack.File{Path: "tests/cases.json", Content: []byte(`{}`)},
		)
	} else {
		files = append(files, pack.File{Path: "skill/SKILL.md",
			Content: []byte("---\nname: " + slug + "\n---\nbody\n")})
	}
	prefix := "mcp-package-" + slug + "-" + version
	tarBytes, _, err := pack.WriteTarball(prefix, files)
	require.NoError(t, err)

	m := &manifest.Manifest{
		SchemaVersion: 1, Kind: kind, Slug: slug, Version: version,
		CardRef:  "capability_card.md",
		SpecRef:  "spec.json",
		CasesRef: "tests/cases.json",
		Software: manifest.Software{Packages: []string{}},
		Hardware: manifest.Hardware{NetworkEgress: []string{}},
		Tags:     []string{}, License: "MIT",
		CreatedAt: "2026-05-26T00:00:00Z",
	}
	if kind == manifest.KindSkill {
		m.SpecRef = ""
		m.CasesRef = ""
	}
	mfJSON, err := json.Marshal(m)
	require.NoError(t, err)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	require.NoError(t, mw.WriteField("manifest", string(mfJSON)))
	fw, err := mw.CreateFormFile("tarball", "pkg.tar.gz")
	require.NoError(t, err)
	_, err = fw.Write(tarBytes)
	require.NoError(t, err)
	require.NoError(t, mw.Close())
	return &body, mw.FormDataContentType()
}

func TestAPI_PushHappyPath(t *testing.T) {
	_, srv := newTestHandler(t)
	defer srv.Close()
	body, ct := buildPushBody(t, manifest.KindMCP, "foo", "1.0.0")
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
		srv.URL+"/api/userspace/packages", body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("X-Test-WS", "ws-a")
	req.Header.Set("X-Test-Agent", "agent-1")
	req.Header.Set("X-Test-User", "user-agent-1")
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	bodyBytes, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode, "body=%s", string(bodyBytes))
	var pr PushResponse
	require.NoError(t, json.Unmarshal(bodyBytes, &pr))
	require.Equal(t, "foo", pr.Slug)
	require.Equal(t, "1.0.0", pr.Version)
	require.False(t, pr.Dedup)
}

func TestAPI_PushRecordsCreatedByUserID(t *testing.T) {
	h, srv := newTestHandler(t)
	defer srv.Close()
	body, ct := buildPushBody(t, manifest.KindMCP, "foo", "1.0.0")
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
		srv.URL+"/api/userspace/packages", body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("X-Test-WS", "ws-a")
	req.Header.Set("X-Test-Agent", "agent-1")
	req.Header.Set("X-Test-User", "user-1")
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode, "body=%s", readBody(resp))

	v, err := h.Store.GetVersion("foo", "1.0.0")
	require.NoError(t, err)
	require.Equal(t, "workspace", v.Visibility)
	require.Equal(t, "user-1", v.CreatedByUserID)
}

func TestAPI_PushRejectsKindMismatch(t *testing.T) {
	_, srv := newTestHandler(t)
	defer srv.Close()
	doPush(t, srv, manifest.KindMCP, "foo", "1.0.0", "ws-a", "ag", 200)
	body, ct := buildPushBody(t, manifest.KindSkill, "foo", "2.0.0")
	resp := postMultipart(t, srv, "/api/userspace/packages", body, ct, "ws-a", "ag")
	require.Equal(t, 400, resp.StatusCode)
	require.Contains(t, readBody(resp), "kind mismatch")
}

func TestAPI_PushVersionConflict(t *testing.T) {
	_, srv := newTestHandler(t)
	defer srv.Close()
	doPush(t, srv, manifest.KindMCP, "foo", "1.0.0", "ws-a", "ag", 200)
	doPush(t, srv, manifest.KindMCP, "foo", "1.0.0", "ws-a", "ag", 409)
}

func TestAPI_InstallCrossWorkspaceForbidden(t *testing.T) {
	_, srv := newTestHandler(t)
	defer srv.Close()
	doPush(t, srv, manifest.KindMCP, "foo", "1.0.0", "ws-a", "ag", 200)
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/api/userspace/workspaces/ws-OTHER/installations/foo",
		bytes.NewReader([]byte(`{"version":"1.0.0"}`)))
	req.Header.Set("X-Test-WS", "ws-a")
	req.Header.Set("X-Test-Agent", "ag")
	resp, _ := srv.Client().Do(req)
	require.Equal(t, 403, resp.StatusCode)
}

func TestAPI_Search_HappyPath(t *testing.T) {
	_, srv := newTestHandler(t)
	defer srv.Close()
	doPush(t, srv, manifest.KindMCP, "invoice_extract", "1.0.0", "ws-a", "ag", 200)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/userspace/search?q=invoice", nil)
	req.Header.Set("X-Test-WS", "ws-a")
	req.Header.Set("X-Test-Agent", "ag")
	resp, _ := srv.Client().Do(req)
	require.Equal(t, 200, resp.StatusCode)
	var out struct {
		Results []PackageView `json:"results"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.Len(t, out.Results, 1)
	require.Equal(t, "invoice_extract", out.Results[0].Slug)
}

func doPush(t *testing.T, srv *httptest.Server, k manifest.Kind, slug, ver, ws, ag string, wantStatus int) {
	t.Helper()
	body, ct := buildPushBody(t, k, slug, ver)
	resp := postMultipart(t, srv, "/api/userspace/packages", body, ct, ws, ag)
	require.Equal(t, wantStatus, resp.StatusCode, "body=%s", readBody(resp))
}
func postMultipart(t *testing.T, srv *httptest.Server, path string, body io.Reader, ct, ws, ag string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+path, body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("X-Test-WS", ws)
	req.Header.Set("X-Test-Agent", ag)
	req.Header.Set("X-Test-User", "user-"+ag)
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	return resp
}
func readBody(r *http.Response) string {
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	return string(b)
}

func TestAPI_InstallYankedVersionRejected(t *testing.T) {
	_, srv := newTestHandler(t)
	defer srv.Close()
	doPush(t, srv, manifest.KindMCP, "foo", "1.0.0", "ws-a", "ag", 200)
	// yank via the routePackagePost path
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/api/userspace/packages/foo/yank/1.0.0", nil)
	req.Header.Set("X-Test-WS", "ws-a")
	req.Header.Set("X-Test-Agent", "ag")
	resp, _ := srv.Client().Do(req)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	// attempt to install the yanked version
	body := bytes.NewReader([]byte(`{"version":"1.0.0"}`))
	req2, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/api/userspace/workspaces/ws-a/installations/foo", body)
	req2.Header.Set("X-Test-WS", "ws-a")
	req2.Header.Set("X-Test-Agent", "ag")
	req2.Header.Set("Content-Type", "application/json")
	resp2, _ := srv.Client().Do(req2)
	require.Equal(t, http.StatusGone, resp2.StatusCode)
}

func TestAPI_YankedSourceTarballGone(t *testing.T) {
	_, srv := newTestHandler(t)
	defer srv.Close()
	doPush(t, srv, manifest.KindMCP, "foo", "1.0.0", "ws-a", "ag", 200)
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/api/userspace/packages/foo/yank/1.0.0", nil)
	req.Header.Set("X-Test-WS", "ws-a")
	req.Header.Set("X-Test-Agent", "ag")
	_, _ = srv.Client().Do(req)

	req2, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/api/userspace/packages/foo/versions/1.0.0/source.tar.gz", nil)
	req2.Header.Set("X-Test-WS", "ws-a")
	req2.Header.Set("X-Test-Agent", "ag")
	resp, _ := srv.Client().Do(req2)
	require.Equal(t, http.StatusGone, resp.StatusCode)
}

func TestAPI_GhostSlugsHiddenFromSearch(t *testing.T) {
	_, srv := newTestHandler(t)
	defer srv.Close()
	doPush(t, srv, manifest.KindMCP, "ghost", "1.0.0", "ws-a", "ag", 200)
	// yank — workspace ws-a still has it installed from auto-install on push
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/api/userspace/packages/ghost/yank/1.0.0", nil)
	req.Header.Set("X-Test-WS", "ws-a")
	req.Header.Set("X-Test-Agent", "ag")
	_, _ = srv.Client().Do(req)

	// ws-a still tracks it as installed → appears in search
	req2, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/userspace/search?q=ghost", nil)
	req2.Header.Set("X-Test-WS", "ws-a")
	req2.Header.Set("X-Test-Agent", "ag")
	resp, _ := srv.Client().Do(req2)
	var out struct {
		Results []PackageView `json:"results"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.Len(t, out.Results, 1, "ws-a should still see ghost because it has it installed")

	// ws-b never installed it → should NOT see ghost
	req3, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/userspace/search?q=ghost", nil)
	req3.Header.Set("X-Test-WS", "ws-b")
	req3.Header.Set("X-Test-Agent", "ag2")
	resp3, _ := srv.Client().Do(req3)
	var out3 struct {
		Results []PackageView `json:"results"`
	}
	require.NoError(t, json.NewDecoder(resp3.Body).Decode(&out3))
	require.Len(t, out3.Results, 0, "ws-b must not see fully-yanked ghost slug")
}
