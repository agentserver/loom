package scale_sweep

import (
	"reflect"
	"testing"
)

// Test #44 — default axes give exactly 45 points.
func TestExpand_DefaultCount(t *testing.T) {
	got := Expand(ScaleAxes{})
	if len(got) != 45 {
		t.Errorf("want 45 points, got %d", len(got))
	}
}

// Test #45 — Expand output is stable across runs.
func TestExpand_Lex_Order(t *testing.T) {
	a := Expand(ScaleAxes{})
	b := Expand(ScaleAxes{})
	if !reflect.DeepEqual(a, b) {
		t.Errorf("Expand output non-deterministic")
	}
	// Assert first three and last elements to catch lex-order drift.
	if a[0] != (ScalePoint{1, 10, 1 << 10}) {
		t.Errorf("first point mismatch: %+v", a[0])
	}
	if a[len(a)-1] != (ScalePoint{16, 100, 100 << 20}) {
		t.Errorf("last point mismatch: %+v", a[len(a)-1])
	}
}

// Test #46 — DefaultAxes match §E5 axes VERBATIM.
// Catches drift where a well-meaning refactor swaps {1,2,4,8,16} for
// {1,2,3,4,5} or similar.
func TestExpand_DefaultAxes_Match08E5ScalePoints(t *testing.T) {
	da := DefaultAxes()
	if !reflect.DeepEqual(da.Contexts, []int{1, 2, 4, 8, 16}) {
		t.Errorf("Contexts axis drift: %v", da.Contexts)
	}
	if !reflect.DeepEqual(da.ToolsPerContext, []int{10, 50, 100}) {
		t.Errorf("ToolsPerContext axis drift: %v", da.ToolsPerContext)
	}
	if !reflect.DeepEqual(da.ArtifactSizes, []int64{1 << 10, 1 << 20, 100 << 20}) {
		t.Errorf("ArtifactSizes axis drift: %v", da.ArtifactSizes)
	}
}
