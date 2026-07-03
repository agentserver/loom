package common

import (
	"strings"
	"testing"
)

// Test #31 — table-driven positive + negative cases for ValidConversationID.
func TestValidConversationID_TableDriven(t *testing.T) {
	positive := []string{
		"abcdefgh",               // 8-char min
		strings.Repeat("a", 128), // 128-char max
		"conv_abc-123",
		"CONV-ABC_123-xyz",
		"a1b2c3d4",
	}
	for _, s := range positive {
		if !ValidConversationID(s) {
			t.Errorf("positive %q rejected", s)
		}
	}
	negative := []string{
		"",                       // empty
		"short",                  // 5 chars
		"abcdef7",                // 7 chars
		strings.Repeat("a", 129), // 129 chars
		"conv abc",               // space
		"conv;abc0",              // semicolon
		"conv.abc0",              // dot
		"conv/abc0",              // slash
		"conv,abc0",              // comma
		"'; DROP TABLE probe_events; --",
	}
	for _, s := range negative {
		if ValidConversationID(s) {
			t.Errorf("negative %q accepted", s)
		}
	}
}
