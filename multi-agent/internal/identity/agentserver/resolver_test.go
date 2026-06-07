package agentserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/identity"
)

func TestResolverParsesWhoamiResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/agent/whoami", r.URL.Path)
		require.Equal(t, "Bearer proxy-token", r.Header.Get("Authorization"))
		_ = json.NewEncoder(w).Encode(map[string]string{
			"user_id":        "user-1",
			"workspace_id":   "ws-1",
			"workspace_name": "Workspace",
			"sandbox_id":     "sandbox-1",
			"short_id":       "short-1",
			"role":           "developer",
		})
	}))
	defer srv.Close()

	resolver := New(Config{BaseURL: srv.URL, Timeout: time.Second})
	got, err := resolver.Resolve(context.Background(), "proxy-token")
	require.NoError(t, err)
	require.Equal(t, identity.Identity{
		UserID:        "user-1",
		WorkspaceID:   "ws-1",
		WorkspaceName: "Workspace",
		AgentID:       "short-1",
		SandboxID:     "sandbox-1",
		Role:          "developer",
		Source:        identity.SourceAgentserver,
	}, got)
}

func TestResolverUsesSandboxIDWhenShortIDIsMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"user_id":      "user-1",
			"workspace_id": "ws-1",
			"sandbox_id":   "sandbox-1",
			"role":         "developer",
		})
	}))
	defer srv.Close()

	resolver := New(Config{BaseURL: srv.URL, Timeout: time.Second})
	got, err := resolver.Resolve(context.Background(), "proxy-token")
	require.NoError(t, err)
	require.Equal(t, "sandbox-1", got.AgentID)
}

func TestResolverMapsHTTPStatusErrors(t *testing.T) {
	for _, tc := range []struct {
		name       string
		statusCode int
		wantErr    error
	}{
		{name: "unauthorized", statusCode: http.StatusUnauthorized, wantErr: identity.ErrInvalid},
		{name: "forbidden", statusCode: http.StatusForbidden, wantErr: identity.ErrRevoked},
		{name: "server error", statusCode: http.StatusInternalServerError, wantErr: identity.ErrUpstream},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.statusCode)
			}))
			defer srv.Close()

			resolver := New(Config{BaseURL: srv.URL, Timeout: time.Second})
			_, err := resolver.Resolve(context.Background(), "proxy-token")
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestResolverMapsTimeoutToUpstream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
	}))
	defer srv.Close()

	resolver := New(Config{BaseURL: srv.URL, Timeout: time.Millisecond})
	_, err := resolver.Resolve(context.Background(), "proxy-token")
	require.ErrorIs(t, err, identity.ErrUpstream)
}

func TestResolverMapsMalformedJSONToUpstream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer srv.Close()

	resolver := New(Config{BaseURL: srv.URL, Timeout: time.Second})
	_, err := resolver.Resolve(context.Background(), "proxy-token")
	require.ErrorIs(t, err, identity.ErrUpstream)
}

func TestResolverRequiresDocumentedFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"user_id":      "user-1",
			"workspace_id": "ws-1",
			"role":         "developer",
		})
	}))
	defer srv.Close()

	resolver := New(Config{BaseURL: srv.URL, Timeout: time.Second})
	_, err := resolver.Resolve(context.Background(), "proxy-token")
	require.ErrorIs(t, err, identity.ErrUpstream)
}

func TestResolverMapsClosedServerToUpstream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	baseURL := srv.URL
	srv.Close()

	resolver := New(Config{BaseURL: baseURL, Timeout: time.Second})
	_, err := resolver.Resolve(context.Background(), "proxy-token")
	require.ErrorIs(t, err, identity.ErrUpstream)
}
