package commanderhub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWeb_CommanderPageAndAssets(t *testing.T) {
	mux := http.NewServeMux()
	MountWeb(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// /commander → HTML
	resp, err := http.Get(srv.URL + "/commander")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "text/html; charset=utf-8", resp.Header.Get("Content-Type"))
	body := make([]byte, 4096)
	n, _ := resp.Body.Read(body)
	resp.Body.Close()
	require.True(t, strings.Contains(string(body[:n]), "commander"), "index references app.js")

	// assets served
	for _, p := range []string{"/commander/app.js", "/commander/style.css"} {
		resp, err := http.Get(srv.URL + p)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode, p)
		resp.Body.Close()
	}

	// unknown path under /commander/ → 404 (no stray fileserver catch-all)
	resp, err = http.Get(srv.URL + "/commander/nope")
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp.Body.Close()
}
