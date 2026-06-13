package observerclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWriteTokenFileSetsMode0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observer.token")
	require.NoError(t, writeTokenFile(path, "tk_abc123"))

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "tk_abc123", string(got))
}

func TestWriteTokenFileTruncatesExistingContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observer.token")
	require.NoError(t, os.WriteFile(path, []byte("OLD_LONG_CONTENT_xxxxxxxxxxx"), 0o600))

	require.NoError(t, writeTokenFile(path, "new_short"))

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "new_short", string(got))
}

func TestReadTokenFileTrimsWhitespace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observer.token")
	require.NoError(t, os.WriteFile(path, []byte("  tk_xyz789\n"), 0o600))

	got, ok, err := readTokenFile(path)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "tk_xyz789", got)
}

func TestReadTokenFileMissingReturnsNotOk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nope.token")

	_, ok, err := readTokenFile(path)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestReadTokenFileEmptyReturnsNotOk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observer.token")
	require.NoError(t, os.WriteFile(path, []byte("   \n\t"), 0o600))

	_, ok, err := readTokenFile(path)
	require.NoError(t, err)
	require.False(t, ok, "whitespace-only file should be treated as missing")
}

func TestRegisterPostsAPIKeyAndAgentDetails(t *testing.T) {
	var gotAuth, gotPath, gotMethod, gotContentType string
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"workspace_id":"ws-1","agent_id":"slave-a","role":"slave","display_name":"Slave A","token":"tk_issued"}`))
	}))
	defer srv.Close()

	ctx := context.Background()
	token, ws, err := register(ctx, &http.Client{Timeout: 2 * time.Second}, srv.URL, "ak_secret", "slave-a", "slave", "Slave A", "ws-1", "", false)
	require.NoError(t, err)
	require.Equal(t, "tk_issued", token)
	require.Equal(t, "ws-1", ws)
	require.Equal(t, http.MethodPost, gotMethod)
	require.Equal(t, "/api/agents/register", gotPath)
	require.Equal(t, "Bearer ak_secret", gotAuth)
	require.Equal(t, "application/json", gotContentType)
	require.Equal(t, "slave-a", gotBody["agent_id"])
	require.Equal(t, "slave", gotBody["role"])
	require.Equal(t, "Slave A", gotBody["display_name"])
}

func TestRegisterStrips_TrailingSlashOnURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/agents/register", r.URL.Path)
		_, _ = w.Write([]byte(`{"workspace_id":"ws-1","token":"tk_ok"}`))
	}))
	defer srv.Close()

	ctx := context.Background()
	_, _, err := register(ctx, http.DefaultClient, srv.URL+"/", "ak", "agent", "slave", "", "ws-test", "", false)
	require.NoError(t, err)
}

func TestRegisterSurfacesNon2xxAsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`invalid api key`))
	}))
	defer srv.Close()

	_, _, err := register(context.Background(), http.DefaultClient, srv.URL, "ak_bad", "agent", "slave", "", "ws-test", "", false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "403")
	require.Contains(t, err.Error(), "invalid api key")
}

func TestRegisterNetworkFailureReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	_, _, err := register(context.Background(), &http.Client{Timeout: 200 * time.Millisecond}, url, "ak", "agent", "slave", "", "ws-test", "", false)
	require.Error(t, err)
}

func TestRegisterDefaultsDisplayNameWhenEmpty(t *testing.T) {
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"workspace_id":"ws","token":"tk"}`))
	}))
	defer srv.Close()

	_, _, err := register(context.Background(), http.DefaultClient, srv.URL, "ak", "agent-x", "slave", "", "ws-test", "", false)
	require.NoError(t, err)
	_, present := gotBody["display_name"]
	require.True(t, present)
	require.Equal(t, "", gotBody["display_name"])
}

func TestRegister_SendsWorkspaceFieldsInBody(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		gotBody, err = io.ReadAll(r.Body)
		require.NoError(t, err)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"token":"t1","workspace_id":"ws-x","agent_id":"a","role":"slave","display_name":"a"}`))
	}))
	defer srv.Close()

	ctx := context.Background()
	_, _, err := register(ctx, &http.Client{Timeout: 2 * time.Second},
		srv.URL, "apikey", "a", "slave", "a", "ws-x", "Test Name", false)
	require.NoError(t, err)
	require.JSONEq(t,
		`{"agent_id":"a","role":"slave","display_name":"a","workspace_id":"ws-x","workspace_name":"Test Name"}`,
		string(gotBody))
}

var _ = strings.NewReader

func newTestClient(t *testing.T, cfg Config) *Client {
	t.Helper()
	return &Client{cfg: cfg}
}

func TestLoadOrRegister_WarmStartReusesCachedToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observer.token")
	require.NoError(t, os.WriteFile(path, []byte("cached_token\n"), 0o600))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("register endpoint should not be called when token file exists; got %s", r.URL.Path)
	}))
	defer srv.Close()

	c := newTestClient(t, Config{
		URL: srv.URL, WorkspaceID: "ws-1", AgentID: "agent-1",
		AgentRole: "slave", APIKey: "ak", TokenStatePath: path,
	})
	tok, err := c.loadOrRegister(context.Background())
	require.NoError(t, err)
	require.Equal(t, "cached_token", tok)
}

func TestLoadOrRegister_ColdStartCallsRegisterAndPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observer.token")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"workspace_id":"ws-1","agent_id":"agent-1","role":"slave","token":"tk_fresh"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, Config{
		URL: srv.URL, WorkspaceID: "ws-1", AgentID: "agent-1",
		AgentRole: "slave", APIKey: "ak", TokenStatePath: path,
	})
	tok, err := c.loadOrRegister(context.Background())
	require.NoError(t, err)
	require.Equal(t, "tk_fresh", tok)

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	got, _ := os.ReadFile(path)
	require.Equal(t, "tk_fresh", string(got))
}

func TestLoadOrRegister_WorkspaceMismatchReturnsErrorAndDoesNotPersist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observer.token")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"workspace_id":"OTHER","token":"tk"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, Config{
		URL: srv.URL, WorkspaceID: "ws-1", AgentID: "agent-1",
		AgentRole: "slave", APIKey: "ak", TokenStatePath: path,
	})
	_, err := c.loadOrRegister(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "OTHER")
	require.Contains(t, err.Error(), "ws-1")
	_, statErr := os.Stat(path)
	require.True(t, os.IsNotExist(statErr), "token file must not be written when workspace mismatches")
}

func TestLoadOrRegister_RegisterErrorPropagatesAndDoesNotPersist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observer.token")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid api key", http.StatusForbidden)
	}))
	defer srv.Close()

	c := newTestClient(t, Config{
		URL: srv.URL, WorkspaceID: "ws-1", AgentID: "agent-1",
		AgentRole: "slave", APIKey: "ak_bad", TokenStatePath: path,
	})
	_, err := c.loadOrRegister(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "403")
	_, statErr := os.Stat(path)
	require.True(t, os.IsNotExist(statErr))
}
