package main

import "testing"

func TestHasSkill(t *testing.T) {
	if !hasSkill([]string{"chat", "bash"}, "bash") {
		t.Fatal("expected bash skill")
	}
	if !hasSkill([]string{"chat", "claude_permissions"}, "claude_permissions") {
		t.Fatal("expected claude_permissions skill")
	}
	if hasSkill([]string{"chat"}, "bash") {
		t.Fatal("did not expect bash skill")
	}
}
