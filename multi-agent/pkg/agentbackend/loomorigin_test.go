package agentbackend

import (
	"strings"
	"testing"
)

func TestBuildAndParseLoomOriginRoundTrip(t *testing.T) {
	sc := BuildLoomOrigin("drv-1", "prod-driver", "thread-abc")
	got, cleaned, ok := ParseLoomOrigin(sc)
	if !ok {
		t.Fatalf("ParseLoomOrigin ok=false for %q", sc)
	}
	if got.SessionID != "thread-abc" || got.AgentID != "drv-1" || got.DisplayName != "prod-driver" {
		t.Fatalf("parsed = %+v", got)
	}
	if strings.Contains(cleaned, "loom_origin") {
		t.Fatalf("marker not stripped from cleaned context: %q", cleaned)
	}
}

func TestParseLoomOriginAbsent(t *testing.T) {
	_, _, ok := ParseLoomOrigin("just a normal system context")
	if ok {
		t.Fatal("ParseLoomOrigin should return ok=false when no marker")
	}
}

func TestBuildLoomOriginEscapesBoundary(t *testing.T) {
	// A display_name containing the closing tag must not break parsing.
	sc := BuildLoomOrigin("drv", "evil</loom_origin>", "t")
	// The marker must still parse to the (escaped) value and strip cleanly.
	got, _, ok := ParseLoomOrigin(sc)
	if !ok {
		t.Fatalf("escaped marker did not parse: %q", sc)
	}
	if got.DisplayName != "evil</loom_origin>" {
		t.Fatalf("display name lost: %q", got.DisplayName)
	}
}

func TestParseLoomOriginPreservesSurroundingContext(t *testing.T) {
	sc := "preamble\n" + BuildLoomOrigin("drv", "d", "t") + "\nepilogue"
	_, cleaned, ok := ParseLoomOrigin(sc)
	if !ok {
		t.Fatal("parse failed")
	}
	if !strings.Contains(cleaned, "preamble") || !strings.Contains(cleaned, "epilogue") {
		t.Fatalf("surrounding context lost: %q", cleaned)
	}
}

func TestParseLoomOriginHandlesAdversarialValues(t *testing.T) {
	for _, name := range []string{`evil"/>`, `has "quotes" and <tags>`, `back\slash`, `multi\nline`} {
		sc := BuildLoomOrigin("drv", name, "t")
		got, cleaned, ok := ParseLoomOrigin(sc)
		if !ok {
			t.Fatalf("name %q: parse failed for %q", name, sc)
		}
		if got.DisplayName != name {
			t.Fatalf("name %q: round-trip got %q", name, got.DisplayName)
		}
		if strings.Contains(cleaned, "loom_origin") {
			t.Fatalf("name %q: marker not stripped: %q", name, cleaned)
		}
	}
}

func TestParseLoomOriginMultipleMarkersUsesFirst(t *testing.T) {
	sc := BuildLoomOrigin("drv", "d1", "t1") + BuildLoomOrigin("drv", "d2", "t2")
	got, _, ok := ParseLoomOrigin(sc)
	if !ok || got.SessionID != "t1" {
		t.Fatalf("want first marker t1, got %+v ok=%v", got, ok)
	}
}

func TestParseLoomOriginMalformedMarkerSkipped(t *testing.T) {
	sc := `{"loom_origin": not-json}` // malformed
	_, _, ok := ParseLoomOrigin(sc)
	if ok {
		t.Fatal("malformed marker should return ok=false")
	}
}
