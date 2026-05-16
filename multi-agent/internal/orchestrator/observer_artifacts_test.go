package orchestrator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yourorg/multi-agent/internal/config"
)

func TestObserverArtifactResolver_RetriesSQLiteBusy(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, "database is locked (5) (SQLITE_BUSY)", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte("artifact body"))
	}))
	t.Cleanup(srv.Close)

	resolver := NewObserverArtifactResolver(config.Observer{
		Enabled: true,
		URL:     srv.URL,
		Token:   "token",
	})

	body, _, err := resolver.GetArtifact(context.Background(), srv.URL+"/api/artifacts/art_1")
	if err != nil {
		t.Fatalf("GetArtifact: %v", err)
	}
	if string(body) != "artifact body" {
		t.Fatalf("body = %q", string(body))
	}
	if calls != 2 {
		t.Fatalf("calls = %d", calls)
	}
}
