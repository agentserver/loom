package agentbackend

import "testing"

func TestSessionParentAgentFields(t *testing.T) {
	s := Session{
		ParentID:          "parent-session",
		ParentAgentID:     "drv-abc",
		ParentDisplayName: "prod-driver",
	}
	if s.ParentAgentID != "drv-abc" {
		t.Fatalf("ParentAgentID = %q, want drv-abc", s.ParentAgentID)
	}
	if s.ParentDisplayName != "prod-driver" {
		t.Fatalf("ParentDisplayName = %q, want prod-driver", s.ParentDisplayName)
	}
}

func TestConfigCodexHomeField(t *testing.T) {
	cfg := Config{CodexHome: "/codex-home"}
	if cfg.CodexHome != "/codex-home" {
		t.Fatalf("CodexHome = %q, want /codex-home", cfg.CodexHome)
	}
}
