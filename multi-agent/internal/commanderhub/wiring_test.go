package commanderhub

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/internal/commanderhub/authstore"
	"github.com/yourorg/multi-agent/internal/identity"
)

// TestMountAll_RegistersAllSurfaces: MountAll wires daemon-link + commander API
// + /commander page. API routes require auth (401); the page is public.
func TestMountAll_RegistersAllSurfaces(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-alice": {UserID: "alice", WorkspaceID: "W1"},
	}}
	mux := http.NewServeMux()
	MountAll(mux, resolver, "https://agent.example/", authstore.NewInMemoryStore())
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// page is public
	resp, err := http.Get(srv.URL + "/commander")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// commander API requires auth
	resp, err = http.Get(srv.URL + "/api/commander/daemons")
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp.Body.Close()

	// bearer works (no cookie needed for scripts/curl)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/commander/daemons", nil)
	req.Header.Set("Authorization", "Bearer tok-alice")
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
}
