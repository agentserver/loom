package codex

import (
	"strings"
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

func TestBackendEffectiveCodexHomeFromConfig(t *testing.T) {
	t.Setenv("CODEX_HOME", "/stale-process")
	b := New(agentbackend.Config{CodexHome: "/cfg-home"}, nil)
	if got := b.effectiveCodexHome(); got != "/cfg-home" {
		t.Fatalf("effectiveCodexHome = %q, want /cfg-home", got)
	}
}

func TestBackendEffectiveCodexHomeFromEnvSlice(t *testing.T) {
	t.Setenv("CODEX_HOME", "/stale-process")
	b := New(agentbackend.Config{}, []string{"CODEX_HOME=/env-home"})
	if got := b.effectiveCodexHome(); got != "/env-home" {
		t.Fatalf("effectiveCodexHome = %q, want /env-home", got)
	}
}

func TestNewResolvedEnvHasSingleCodexHome(t *testing.T) {
	t.Setenv("CODEX_HOME", "/stale-process")
	b := New(agentbackend.Config{CodexHome: "/new"}, []string{"Codex_Home=/old-case", "FOO=bar"})
	count := 0
	for _, kv := range b.env {
		k, v, ok := splitEnv(kv)
		if !ok {
			continue
		}
		if strings.EqualFold(k, "CODEX_HOME") {
			count++
			if k != "CODEX_HOME" || v != "/new" {
				t.Fatalf("CODEX_HOME entry = %q, want CODEX_HOME=/new", kv)
			}
		}
	}
	if count != 1 {
		t.Fatalf("b.env has %d CODEX_HOME entries, want 1: %#v", count, b.env)
	}
	if envValue(b.env, "FOO") != "bar" {
		t.Fatalf("FOO not preserved in b.env: %#v", b.env)
	}
}
