package validator

import (
	"context"
	"strings"
	"testing"

	"github.com/yourorg/multi-agent/internal/capability"
	"github.com/yourorg/multi-agent/internal/contract"
)

func TestNew_ReturnsValidator(t *testing.T) {
	v := New()
	if v == nil {
		t.Fatal("New() returned nil")
	}
}

func TestValidator_EmptyContract_NoBlocks(t *testing.T) {
	v := New()
	got := v.Check(context.Background(), contract.TaskContract{}, capability.Snapshot{})
	if len(got) != 0 {
		t.Errorf("empty contract + empty snapshot must produce no blocks; got %d: %+v", len(got), got)
	}
}

func TestBlock_DetailShape(t *testing.T) {
	b := newBlock(KindMissingFile, "data_contract.read_artifacts[0].name", "config.yaml", "no snapshot file resource matches")
	if b.Kind != KindMissingFile {
		t.Errorf("Kind: got %q", b.Kind)
	}
	if b.Severity != SeverityBlock {
		t.Errorf("Severity: got %q", b.Severity)
	}
	if b.Field != "data_contract.read_artifacts[0].name" {
		t.Errorf("Field: got %q", b.Field)
	}
	want := "data_contract.read_artifacts[0].name: expected config.yaml, actual no snapshot file resource matches"
	if b.Detail != want {
		t.Errorf("Detail: got %q\nwant %q", b.Detail, want)
	}
}

// §7(c) + §7(f): Detail is truncated at maxDetailBytes on construction.
// A 20 KiB actual value must be cut down and end with a truncation marker.
func TestBlock_DetailTruncatedAt8KiB(t *testing.T) {
	huge := strings.Repeat("x", 20*1024)
	b := newBlock(KindWrongVersion, "capability_requirements.tools[0]", ">=1.22.0", huge)
	if len(b.Detail) > maxDetailBytes {
		t.Errorf("Detail exceeded cap: got %d bytes, cap %d", len(b.Detail), maxDetailBytes)
	}
	if !strings.HasSuffix(b.Detail, "<...truncated>") {
		t.Errorf("truncated Detail must carry sentinel; got trailing %q", b.Detail[len(b.Detail)-32:])
	}
}
