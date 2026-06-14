package agentbackend_test

import (
	"strings"
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
	_ "github.com/yourorg/multi-agent/pkg/agentbackend/claude"
	_ "github.com/yourorg/multi-agent/pkg/agentbackend/codex"
)

// TestNewRequiresKind pins the §issue-15 contract: agent.kind is
// required, no implicit default. Empty kind must error rather than
// silently routing to claude.
func TestNewRequiresKind(t *testing.T) {
	_, err := agentbackend.New(agentbackend.Config{Bin: "claude", WorkDir: "/tmp"}, nil)
	if err == nil {
		t.Fatal("expected error for empty kind")
	}
	if !strings.Contains(err.Error(), "kind") {
		t.Fatalf("err should mention 'kind'; got %v", err)
	}
}

// TestNewUnknownKindListsRegistered pins that the error message
// names every registered kind so the operator can see which import
// they're missing — rather than a bare "unknown kind".
func TestNewUnknownKindListsRegistered(t *testing.T) {
	_, err := agentbackend.New(agentbackend.Config{Kind: "frobnitz", WorkDir: "/tmp"}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"frobnitz", "claude", "codex"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err missing %q: %v", want, err)
		}
	}
}

// TestNewDispatchesClaude exercises the registered claude path
// using the flat Config — proves the rename + flatten path works
// end-to-end.
func TestNewDispatchesClaude(t *testing.T) {
	b, err := agentbackend.New(agentbackend.Config{
		Kind:    agentbackend.KindClaude,
		Bin:     "claude",
		WorkDir: t.TempDir(),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b.Kind() != agentbackend.KindClaude {
		t.Fatalf("kind=%v", b.Kind())
	}
}

func TestNewDispatchesCodex(t *testing.T) {
	b, err := agentbackend.New(agentbackend.Config{
		Kind:    agentbackend.KindCodex,
		Bin:     "codex",
		WorkDir: t.TempDir(),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b.Kind() != agentbackend.KindCodex {
		t.Fatalf("kind=%v", b.Kind())
	}
}

// TestRegisteredKinds is the introspection helper used by the LoadConfig
// validators (so the error message there can also list available kinds).
func TestRegisteredKinds(t *testing.T) {
	kinds := agentbackend.RegisteredKinds()
	if len(kinds) < 2 {
		t.Fatalf("expected at least claude+codex registered; got %v", kinds)
	}
	var hasClaude, hasCodex bool
	for _, k := range kinds {
		if k == string(agentbackend.KindClaude) {
			hasClaude = true
		}
		if k == string(agentbackend.KindCodex) {
			hasCodex = true
		}
	}
	if !hasClaude || !hasCodex {
		t.Fatalf("missing builtin kinds; got %v", kinds)
	}
}
