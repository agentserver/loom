package evalconfig

import (
	"path/filepath"
	"testing"
)

// TestEvalDefaultConfig_NoMasterPath pins the v3 freeze invariant: the
// eval-runner default config that ships with the repo must route through the
// driver and refuse to dispatch through cmd/master-agent. If someone flips
// either field, this test fails before the change can land.
func TestEvalDefaultConfig_NoMasterPath(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "..", "dev", "configs", "eval-default.yaml")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load(%s) failed: %v", path, err)
	}
	if got, want := cfg.Routing.Mode, "driver-first"; got != want {
		t.Errorf("routing.mode = %q, want %q (master path is frozen for v3)", got, want)
	}
	if cfg.Routing.AllowMaster {
		t.Error("routing.allow_master = true; master path is frozen and must stay false")
	}
}
