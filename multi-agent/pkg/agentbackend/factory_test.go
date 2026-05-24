package agentbackend_test

import (
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
	_ "github.com/yourorg/multi-agent/pkg/agentbackend/claude"
	_ "github.com/yourorg/multi-agent/pkg/agentbackend/codex"
)

func TestNewDefaultsToClaude(t *testing.T) {
	b, err := agentbackend.New(agentbackend.Config{Claude: agentbackend.ClaudeConfig{Bin: "claude"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b.Kind() != agentbackend.KindClaude {
		t.Fatalf("kind=%v want claude", b.Kind())
	}
}

func TestNewCodexBuilds(t *testing.T) {
	b, err := agentbackend.New(agentbackend.Config{
		Kind:  agentbackend.KindCodex,
		Codex: agentbackend.CodexConfig{Bin: "codex"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b.Kind() != agentbackend.KindCodex {
		t.Fatalf("kind=%v", b.Kind())
	}
}

func TestNewRejectsUnknownKind(t *testing.T) {
	_, err := agentbackend.New(agentbackend.Config{Kind: "frobnitz"}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
}
