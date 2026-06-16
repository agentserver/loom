package commanderhub

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWeb_CommanderPageAndAssets(t *testing.T) {
	mux := http.NewServeMux()
	MountWeb(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/commander")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "text/html; charset=utf-8", resp.Header.Get("Content-Type"))
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.NoError(t, err)
	require.Contains(t, string(body), `id="root"`)

	entries, err := os.ReadDir("assets/dist/assets")
	require.NoError(t, err)
	require.NotEmpty(t, entries)
	var assetName string
	for _, entry := range entries {
		if !entry.IsDir() {
			assetName = entry.Name()
			break
		}
	}
	require.NotEmpty(t, assetName)

	resp, err = http.Get(srv.URL + "/commander/assets/" + assetName)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	resp, err = http.Get(srv.URL + "/commander/nope")
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp.Body.Close()
}
