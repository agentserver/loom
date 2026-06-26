package observerweb

import (
	"testing"

	"github.com/yourorg/multi-agent/internal/identity/static"
	"github.com/yourorg/multi-agent/internal/observerstore"
)

// TestNewWithResolverOptions_PanicsWithoutAuthStore: AgentserverURL set
// without AuthStore must panic. This is the production safety net against
// silently regressing to in-memory state (which would re-introduce the
// multi-pod login bug).
func TestNewWithResolverOptions_PanicsWithoutAuthStore(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when AgentserverURL is set and AuthStore is nil")
		}
	}()
	s, err := observerstore.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	_ = NewWithResolverOptions(s, nil, static.New(s), Options{
		AgentserverURL: "https://agent.example/",
		// AuthStore intentionally absent.
	})
}
