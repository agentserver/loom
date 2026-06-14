package opencode

import (
	"context"
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// TestStore_GetReturnsDefault pins the NoOp contract: a fresh store
// returns State with Backend=KindOpencode and Mode="ask" (the
// conservative default — opencode lacks an on-disk permissions
// schema so we report the "needs operator approval" state).
func TestStore_GetReturnsDefault(t *testing.T) {
	s := NewStore(t.TempDir())
	got, err := s.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Backend != agentbackend.KindOpencode {
		t.Errorf("Backend=%v want opencode", got.Backend)
	}
	if got.Mode != "ask" {
		t.Errorf("Mode=%q want ask", got.Mode)
	}
}

// TestStore_PatchAcceptsModeAndPresets pins that Patch surfaces a
// new Mode (either explicit or via Presets) without trying to
// persist (opencode lacks the on-disk schema; mode lives only in
// the in-memory backend state for this turn).
func TestStore_PatchAcceptsMode(t *testing.T) {
	s := NewStore(t.TempDir())
	got, err := s.Patch(context.Background(), agentbackend.Patch{Mode: "workspace-write"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != "workspace-write" {
		t.Errorf("Mode=%q want workspace-write", got.Mode)
	}
	// Subsequent Get returns "ask" again — NoOp does NOT persist.
	got2, _ := s.Get(context.Background())
	if got2.Mode != "ask" {
		t.Errorf("after Get: Mode=%q want ask (NoOp does not persist)", got2.Mode)
	}
}

// TestStore_PatchRejectsClaudeOnlyFields pins that Allow/Deny lists
// are claude-only and produce an explicit error rather than silent
// drop. Mirrors codex Store behaviour.
func TestStore_PatchRejectsClaudeOnlyFields(t *testing.T) {
	s := NewStore(t.TempDir())
	cases := []agentbackend.Patch{
		{AllowAdd: []string{"Read(./*)"}},
		{AllowRemove: []string{"Read(./*)"}},
		{DenyAdd: []string{"Write(/etc/**)"}},
		{DenyRemove: []string{"Write(/etc/**)"}},
	}
	for i, p := range cases {
		_, err := s.Patch(context.Background(), p)
		if err == nil {
			t.Errorf("case %d: expected error for claude-only fields", i)
		}
	}
}
