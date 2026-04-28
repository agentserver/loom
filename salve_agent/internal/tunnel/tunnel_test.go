package tunnel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/salve_agent/internal/config"
)

func TestEnsureRegistered_PersistsCredentials(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/oauth2/device/auth":
			// SDK sends form-encoded POST; respond with device auth JSON.
			// Set interval=0; SDK enforces minimum 5s so the poll will
			// fire after ~5s regardless.
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"device_code":               "dc",
				"user_code":                 "UC",
				"verification_uri":          srv.URL + "/device",
				"verification_uri_complete": srv.URL + "/device?user_code=UC",
				"expires_in":                60,
				"interval":                  0,
			})
		case "/api/oauth2/token":
			// SDK polls with form-encoded body; return success immediately.
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"access_token": "atk",
				"token_type":   "Bearer",
				"expires_in":   3600,
			})
		case "/api/agent/register":
			// SDK checks for StatusCreated (201), not 200.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"sandbox_id":   "sb-1",
				"tunnel_token": "tt",
				"proxy_token":  "pt",
				"short_id":     "sid",
				"workspace_id": "ws",
			})
		default:
			t.Logf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	cfgPath := filepath.Join(t.TempDir(), "c.yaml")
	c := &config.Config{Server: config.Server{URL: srv.URL, Name: "n"}}
	require.NoError(t, c.Save(cfgPath))

	tn := NewWithDeps(c, cfgPath, http.DefaultServeMux, Deps{
		Open: func(string) error { return nil },
	})
	require.NoError(t, tn.EnsureRegistered(context.Background()))

	reloaded, err := config.Load(cfgPath)
	require.NoError(t, err)
	require.Equal(t, "sb-1", reloaded.Credentials.SandboxID)
	require.Equal(t, "pt", reloaded.Credentials.ProxyToken)
	require.Equal(t, "sid", reloaded.Credentials.ShortID)
}

func TestEnsureRegistered_SkipsWhenAlreadyRegistered(t *testing.T) {
	// If credentials are already present, EnsureRegistered must be a no-op
	// (no HTTP calls made, no error returned).
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(500)
	}))
	defer srv.Close()

	cfgPath := filepath.Join(t.TempDir(), "c.yaml")
	c := &config.Config{
		Server: config.Server{URL: srv.URL, Name: "n"},
		Credentials: config.Credentials{
			SandboxID:   "existing-sb",
			TunnelToken: "existing-tt",
			ProxyToken:  "existing-pt",
			ShortID:     "existing-sid",
		},
	}
	require.NoError(t, c.Save(cfgPath))

	tn := NewWithDeps(c, cfgPath, http.DefaultServeMux, Deps{
		Open: func(string) error { return nil },
	})
	require.NoError(t, tn.EnsureRegistered(context.Background()))
	require.Equal(t, 0, callCount, "should make no HTTP requests when already registered")
}
