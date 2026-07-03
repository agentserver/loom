package common

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// Test #29 — every trigger prefix gets a leading single quote.
func TestCSVSink_EscapesInjection(t *testing.T) {
	cases := []struct{ in, want string }{
		{"=SUM(A1:A9)", "'=SUM(A1:A9)"},
		{"+1+1", "'+1+1"},
		{"-cmd|calc", "'-cmd|calc"},
		{"@import(x)", "'@import(x)"},
		{"\thidden tab", "'\thidden tab"},
		{"\rcarriage", "'\rcarriage"},
	}
	for _, c := range cases {
		got := escapeCell(c.in)
		// The escape may also wrap in "" because of embedded \n / \r / , .
		// The invariant we care about: the FIRST rune of the result is
		// the single-quote prefix (either literally, or the quoted form
		// "'X..." where the leading " is the RFC 4180 wrap).
		if got[0] != '\'' && !(got[0] == '"' && len(got) > 1 && got[1] == '\'') {
			t.Errorf("escapeCell(%q)=%q — missing injection-safe prefix", c.in, got)
		}
	}
}

// Test #30 — benign cells + RFC 4180 quoting + golden fixture.
func TestCSVSink_LeavesBenignCellsUnchanged(t *testing.T) {
	benign := []string{"a", "1", "_underscore", ".period", "#pound"}
	for _, s := range benign {
		if got := escapeCell(s); got != s {
			t.Errorf("escapeCell(%q) mutated benign cell to %q", s, got)
		}
	}
	// RFC 4180: comma / quote / newline force wrap.
	if got := escapeCell(`hello, world`); got != `"hello, world"` {
		t.Errorf("comma wrap: %q", got)
	}
	if got := escapeCell(`he said "hi"`); got != `"he said ""hi"""` {
		t.Errorf("quote escape: %q", got)
	}
	// Golden fixture: assemble a small table with mixed cells.
	var buf bytes.Buffer
	rows := [][]string{
		{"header_a", "header_b", "header_c"},
		{"1", "safe", "=A1"},
		{"2", `has "quote"`, "+1"},
		{"3", "line\nbreak", "-x"},
	}
	for _, r := range rows {
		if err := WriteRow(&buf, r); err != nil {
			t.Fatalf("WriteRow: %v", err)
		}
	}
	goldenPath := filepath.Join("testdata", "csv_golden.csv")
	if os.Getenv("REGEN_CSV_GOLDEN") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("regenerated golden; unset REGEN_CSV_GOLDEN to verify")
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if !bytes.Equal(want, buf.Bytes()) {
		t.Errorf("CSV output does not match golden.\nwant:\n%q\ngot:\n%q",
			string(want), buf.String())
	}
}
