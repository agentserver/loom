package validator

import (
	"testing"

	"github.com/yourorg/multi-agent/internal/ablation"
)

// The init() in ablation.go must have registered NoDryRun against
// disableDryRun; List() from any test that imports validator must
// include NoDryRun.
func TestNoDryRun_RegisteredAtInit(t *testing.T) {
	found := false
	for _, name := range ablation.Default.List() {
		if name == ablation.NoDryRun {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("NoDryRun not registered in ablation.Default; init() failed")
	}
}

// SetByName("NoDryRun", true) must flip IsDryRunDisabled() to true; and
// SetByName back to false must flip it back. Serial-only test — no
// t.Parallel — the ablation contract requires pre-run-only mutation.
func TestNoDryRun_ToggleReflectsInAccessor(t *testing.T) {
	prev := IsDryRunDisabled()
	t.Cleanup(func() { SetDryRunDisabled(prev) })

	if err := ablation.Default.SetByName(string(ablation.NoDryRun), true); err != nil {
		t.Fatalf("SetByName(true): %v", err)
	}
	if !IsDryRunDisabled() {
		t.Errorf("IsDryRunDisabled() = false after SetByName(true)")
	}
	if err := ablation.Default.SetByName(string(ablation.NoDryRun), false); err != nil {
		t.Fatalf("SetByName(false): %v", err)
	}
	if IsDryRunDisabled() {
		t.Errorf("IsDryRunDisabled() = true after SetByName(false)")
	}
}

// SetDryRunDisabled is the test-only shortcut. Assert it matches
// SetByName's effect so tests that use the shortcut don't drift from
// production semantics.
func TestSetDryRunDisabled_MatchesRegistryToggle(t *testing.T) {
	prev := IsDryRunDisabled()
	t.Cleanup(func() { SetDryRunDisabled(prev) })

	SetDryRunDisabled(true)
	if !IsDryRunDisabled() {
		t.Error("SetDryRunDisabled(true) failed to set")
	}
	SetDryRunDisabled(false)
	if IsDryRunDisabled() {
		t.Error("SetDryRunDisabled(false) failed to clear")
	}
}
