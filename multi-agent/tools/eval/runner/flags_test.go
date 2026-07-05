package main

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/yourorg/multi-agent/internal/ablation"
	"github.com/yourorg/multi-agent/internal/capability"
	"github.com/yourorg/multi-agent/internal/contract"
	"github.com/yourorg/multi-agent/internal/contract/validator"
	"github.com/yourorg/multi-agent/internal/evalrun"
)

// resetGlobalAblationState is called in t.Cleanup by tests that
// touch the process-wide ablation.Default. Its job is to leave the
// registry in the state a fresh binary would see: every canonical
// flag false, every Python-bridge env var unset. Uses
// ApplyAblationFlags(nil) which runs Phase 2 (reset) of the
// three-phase mutation.
func resetGlobalAblationState(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		if err := ApplyAblationFlags(nil); err != nil {
			t.Fatalf("cleanup: ApplyAblationFlags(nil): %v", err)
		}
	})
}

// -----------------------------------------------------------------------------
// Constants + regex sanity
// -----------------------------------------------------------------------------

func TestConstantsAndRegex_Sanity(t *testing.T) {
	if !baselineNameRe.MatchString(DefaultBaselineName) {
		t.Fatalf("DefaultBaselineName %q must satisfy baselineNameRe %s",
			DefaultBaselineName, baselineNameRe)
	}
	if baselineNameRe.MatchString("Has Space") {
		t.Errorf("baselineNameRe wrongly accepts 'Has Space'")
	}
	if !ablationLabelRe.MatchString("NoObserver") {
		t.Errorf("ablationLabelRe should accept 'NoObserver'")
	}
	if !ablationLabelRe.MatchString("NoObserver+NoAcceptanceGate") {
		t.Errorf("ablationLabelRe should accept 'NoObserver+NoAcceptanceGate'")
	}
	if ablationLabelRe.MatchString("NoObserver+") {
		t.Errorf("ablationLabelRe should reject trailing '+'")
	}
	if EnvNoAcceptanceGate != "LOOM_ABLATION_NOACCEPTANCEGATE" {
		t.Errorf("EnvNoAcceptanceGate drift: %q", EnvNoAcceptanceGate)
	}
	if MaxBaselineOrAblationLen != 200 {
		t.Errorf("MaxBaselineOrAblationLen drift: %d", MaxBaselineOrAblationLen)
	}
}

// -----------------------------------------------------------------------------
// ValidateBaselineOrAblation
// -----------------------------------------------------------------------------

func TestValidateBaselineOrAblation_BaselineHappy(t *testing.T) {
	for _, s := range []string{"full_loom", "my_run", "manual_ssh"} {
		if err := ValidateBaselineOrAblation(s); err != nil {
			t.Errorf("Validate(%q): unexpected error %v", s, err)
		}
	}
}

func TestValidateBaselineOrAblation_AblationHappy(t *testing.T) {
	for _, s := range []string{"NoObserver", "NoObserver+NoAcceptanceGate"} {
		if err := ValidateBaselineOrAblation(s); err != nil {
			t.Errorf("Validate(%q): unexpected error %v", s, err)
		}
	}
}

func TestValidateBaselineOrAblation_Empty(t *testing.T) {
	err := ValidateBaselineOrAblation("")
	if !errors.Is(err, ErrBaselineOrAblationInvalid) {
		t.Errorf("empty: want ErrBaselineOrAblationInvalid, got %v", err)
	}
}

func TestValidateBaselineOrAblation_TooLong(t *testing.T) {
	s := strings.Repeat("a", MaxBaselineOrAblationLen+1)
	err := ValidateBaselineOrAblation(s)
	if !errors.Is(err, ErrBaselineOrAblationTooLong) {
		t.Errorf("overlong: want ErrBaselineOrAblationTooLong, got %v", err)
	}
}

func TestValidateBaselineOrAblation_MalformedAblation(t *testing.T) {
	for _, s := range []string{"NoObserver+", "NoObserver+++", "Noobserver+No", "+NoObserver"} {
		if err := ValidateBaselineOrAblation(s); !errors.Is(err, ErrBaselineOrAblationInvalid) {
			t.Errorf("Validate(%q): want invalid, got %v", s, err)
		}
	}
}

func TestValidateBaselineOrAblation_MalformedBaseline(t *testing.T) {
	// "FullLoom" is CamelCase; would match ablationLabelRe as a
	// single word, but it collides with... nothing (it's not a
	// canonical flag). So this actually PASSES via ablationLabelRe.
	// The malformed cases here are ones matching NEITHER regex.
	for _, s := range []string{"has spaces", "has/slash", "UPPER_snake", "!", "--"} {
		if err := ValidateBaselineOrAblation(s); !errors.Is(err, ErrBaselineOrAblationInvalid) {
			t.Errorf("Validate(%q): want invalid, got %v", s, err)
		}
	}
}

func TestComputeBaselineOrAblation_CanonicalFlagAsBaseline_Rejected(t *testing.T) {
	// A raw canonical flag name in the BASELINE branch (nil flags)
	// is a collision — spec §5.3 baseline-name-collides row.
	// This is enforced in ComputeBaselineOrAblation, not in
	// ValidateBaselineOrAblation (validate accepts the same
	// string via the ablation regex for the multi-flag case).
	for _, fn := range ablation.KnownFlags() {
		_, err := ComputeBaselineOrAblation(nil, string(fn))
		if err == nil {
			t.Errorf("Compute(nil, %q): want collision reject, got nil", string(fn))
		} else if !errors.Is(err, ErrBaselineOrAblationInvalid) {
			t.Errorf("Compute(nil, %q): want ErrBaselineOrAblationInvalid, got %v", string(fn), err)
		} else if !strings.Contains(err.Error(), "collides with an ablation flag name") {
			t.Errorf("Compute(nil, %q): stderr missing collision marker, got %v", string(fn), err)
		}
	}
}

// -----------------------------------------------------------------------------
// ComputeBaselineOrAblation
// -----------------------------------------------------------------------------

func TestComputeBaselineOrAblation_BaselineBranch(t *testing.T) {
	out, err := ComputeBaselineOrAblation(nil, "full_loom")
	if err != nil || out != "full_loom" {
		t.Fatalf("nil+full_loom: got (%q,%v)", out, err)
	}
}

func TestComputeBaselineOrAblation_EmptyBaselineRejected(t *testing.T) {
	// Run is responsible for substituting the default when the CLI
	// gets "". Compute itself must reject empty (defence-in-depth).
	if _, err := ComputeBaselineOrAblation(nil, ""); !errors.Is(err, ErrBaselineOrAblationInvalid) {
		t.Fatalf("nil+empty: want invalid, got %v", err)
	}
}

func TestComputeBaselineOrAblation_AblationSingle(t *testing.T) {
	out, err := ComputeBaselineOrAblation(
		[]ablation.FlagName{ablation.FlagName("NoObserver")},
		"",
	)
	if err != nil || out != "NoObserver" {
		t.Fatalf("single: got (%q,%v)", out, err)
	}
}

func TestComputeBaselineOrAblation_AblationJoined(t *testing.T) {
	out, err := ComputeBaselineOrAblation(
		[]ablation.FlagName{ablation.FlagName("NoObserver"), ablation.FlagName("NoAcceptanceGate")},
		"",
	)
	if err != nil || out != "NoAcceptanceGate+NoObserver" {
		t.Fatalf("two-sorted: got (%q,%v)", out, err)
	}
}

func TestComputeBaselineOrAblation_OrderStable(t *testing.T) {
	rng := rand.New(rand.NewSource(0xDEADBEEF))
	base := []ablation.FlagName{
		ablation.FlagName("NoObserver"),
		ablation.FlagName("NoAcceptanceGate"),
		ablation.FlagName("NoDryRun"),
		ablation.FlagName("NoRegistryLookup"),
	}
	want, err := ComputeBaselineOrAblation(base, "")
	if err != nil {
		t.Fatalf("baseline compute: %v", err)
	}
	for i := 0; i < 12; i++ {
		shuffled := make([]ablation.FlagName, len(base))
		copy(shuffled, base)
		rng.Shuffle(len(shuffled), func(x, y int) {
			shuffled[x], shuffled[y] = shuffled[y], shuffled[x]
		})
		got, err := ComputeBaselineOrAblation(shuffled, "")
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		if got != want {
			t.Errorf("iter %d: got %q, want %q", i, got, want)
		}
	}
}

func TestComputeBaselineOrAblation_DedupeDirectCaller(t *testing.T) {
	fn := ablation.FlagName("NoObserver")
	out, err := ComputeBaselineOrAblation([]ablation.FlagName{fn, fn}, "")
	if err != nil {
		t.Fatalf("dup: %v", err)
	}
	if out != "NoObserver" {
		t.Errorf("dup: got %q, want %q", out, "NoObserver")
	}
}

func TestComputeBaselineOrAblation_IgnoresBaselineWhenFlagsPresent(t *testing.T) {
	out, err := ComputeBaselineOrAblation(
		[]ablation.FlagName{ablation.FlagName("NoObserver")},
		"my_run",
	)
	if err != nil || out != "NoObserver" {
		t.Fatalf("ablation+baseline: got (%q,%v)", out, err)
	}
}

func TestComputeBaselineOrAblation_AllEightFlagsFitCap(t *testing.T) {
	out, err := ComputeBaselineOrAblation(ablation.KnownFlags(), "")
	if err != nil {
		t.Fatalf("all-8: %v", err)
	}
	if len(out) > MaxBaselineOrAblationLen {
		t.Fatalf("all-8 join length %d exceeds cap %d: %s",
			len(out), MaxBaselineOrAblationLen, out)
	}
}

// -----------------------------------------------------------------------------
// AblationList (flag.Value)
// -----------------------------------------------------------------------------

func TestAblationList_SingleEntry(t *testing.T) {
	var a AblationList
	if err := a.Set("NoObserver"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := a.Values(); len(got) != 1 || got[0] != ablation.FlagName("NoObserver") {
		t.Errorf("Values: %v", got)
	}
}

func TestAblationList_CommaJoined(t *testing.T) {
	var a AblationList
	if err := a.Set("NoObserver,NoAcceptanceGate"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got := a.Values()
	want := []ablation.FlagName{ablation.FlagName("NoObserver"), ablation.FlagName("NoAcceptanceGate")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Values: got %v, want %v", got, want)
	}
}

func TestAblationList_RepeatedSet(t *testing.T) {
	var a AblationList
	if err := a.Set("NoObserver"); err != nil {
		t.Fatalf("Set 1: %v", err)
	}
	if err := a.Set("NoObserver,NoAcceptanceGate"); err != nil {
		t.Fatalf("Set 2: %v", err)
	}
	got := a.Values()
	want := []ablation.FlagName{ablation.FlagName("NoObserver"), ablation.FlagName("NoAcceptanceGate")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("dedup+order: got %v, want %v", got, want)
	}
}

func TestAblationList_DedupWithinSingleSet(t *testing.T) {
	var a AblationList
	if err := a.Set("NoObserver,NoObserver"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := a.Values(); len(got) != 1 {
		t.Errorf("dedup: %v", got)
	}
}

func TestAblationList_UnknownFlagRejected(t *testing.T) {
	var a AblationList
	err := a.Set("NoTpedContracts")
	if !errors.Is(err, ablation.ErrUnknownFlag) {
		t.Fatalf("want ErrUnknownFlag, got %v", err)
	}
}

func TestAblationList_EmptyEntryRejected(t *testing.T) {
	var a AblationList
	if err := a.Set(""); err == nil {
		t.Errorf("Set(\"\"): want error")
	}
	if err := a.Set(",,"); err == nil {
		t.Errorf("Set(\",,\"): want error")
	}
}

func TestAblationList_WhitespaceEntry_Trimmed(t *testing.T) {
	var a AblationList
	if err := a.Set("  NoObserver  , NoAcceptanceGate "); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got := a.Values()
	want := []ablation.FlagName{ablation.FlagName("NoObserver"), ablation.FlagName("NoAcceptanceGate")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("trim: got %v, want %v", got, want)
	}
}

func TestAblationList_WhitespaceOnlyEntryRejected(t *testing.T) {
	var a AblationList
	if err := a.Set("   "); err == nil {
		t.Errorf("Set(\"   \"): want error")
	}
}

func TestAblationList_String_Roundtrip(t *testing.T) {
	var a AblationList
	_ = a.Set("NoObserver")
	_ = a.Set("NoAcceptanceGate")
	got := a.String()
	if got != "NoObserver,NoAcceptanceGate" {
		t.Errorf("String: %q", got)
	}
}

// -----------------------------------------------------------------------------
// applyAblationFlagsTo — three-phase mutation
// -----------------------------------------------------------------------------

func TestApplyAblationFlagsTo_UnknownFlagRejectsBeforeMutating(t *testing.T) {
	reg := ablation.NewRegistry()
	var target bool
	if err := reg.Register(ablation.FlagName("NoObserver"), &target); err != nil {
		t.Fatalf("Register: %v", err)
	}
	err := applyAblationFlagsTo(reg, []ablation.FlagName{
		ablation.FlagName("NoObserver"),
		ablation.FlagName("NoTpedContracts"),
	})
	if !errors.Is(err, ablation.ErrUnknownFlag) {
		t.Fatalf("want ErrUnknownFlag, got %v", err)
	}
	if target {
		t.Errorf("NoObserver target flipped despite Phase-1 reject; want false")
	}
}

func TestApplyAblationFlagsTo_UnregisteredFlagRejectsBeforeMutating(t *testing.T) {
	// Fresh registry, no Register calls → every canonical flag is
	// unregistered from this reg's perspective.
	reg := ablation.NewRegistry()
	err := applyAblationFlagsTo(reg, []ablation.FlagName{ablation.FlagName("NoObserver")})
	if !errors.Is(err, ablation.ErrNotRegistered) {
		t.Fatalf("want ErrNotRegistered, got %v", err)
	}
}

func TestApplyAblationFlagsTo_ResetPhaseClearsPriorFlags(t *testing.T) {
	reg := ablation.NewRegistry()
	var a, b bool
	if err := reg.Register(ablation.FlagName("NoObserver"), &a); err != nil {
		t.Fatalf("Register a: %v", err)
	}
	if err := reg.Register(ablation.FlagName("NoAcceptanceGate"), &b); err != nil {
		t.Fatalf("Register b: %v", err)
	}
	if err := applyAblationFlagsTo(reg, []ablation.FlagName{ablation.FlagName("NoObserver")}); err != nil {
		t.Fatalf("apply 1: %v", err)
	}
	if !a || b {
		t.Fatalf("after apply 1: a=%v b=%v; want a=true b=false", a, b)
	}
	if err := applyAblationFlagsTo(reg, []ablation.FlagName{ablation.FlagName("NoAcceptanceGate")}); err != nil {
		t.Fatalf("apply 2: %v", err)
	}
	if a || !b {
		t.Fatalf("after apply 2: a=%v b=%v; want a=false b=true", a, b)
	}
}

func TestApplyAblationFlagsTo_NilResetsAll(t *testing.T) {
	reg := ablation.NewRegistry()
	var a, b bool
	_ = reg.Register(ablation.FlagName("NoObserver"), &a)
	_ = reg.Register(ablation.FlagName("NoAcceptanceGate"), &b)
	if err := applyAblationFlagsTo(reg, []ablation.FlagName{
		ablation.FlagName("NoObserver"),
		ablation.FlagName("NoAcceptanceGate"),
	}); err != nil {
		t.Fatalf("apply both: %v", err)
	}
	if !a || !b {
		t.Fatalf("pre-reset: a=%v b=%v; want both true", a, b)
	}
	if err := applyAblationFlagsTo(reg, nil); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if a || b {
		t.Fatalf("post-reset: a=%v b=%v; want both false", a, b)
	}
}

func TestApplyAblationFlagsTo_PhaseThreeSetsBoolsAndSyncsAtomic(t *testing.T) {
	resetGlobalAblationState(t)
	if err := ApplyAblationFlags([]ablation.FlagName{ablation.FlagName("NoDryRun")}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !validator.IsDryRunDisabled() {
		t.Errorf("IsDryRunDisabled false after ApplyAblationFlags(NoDryRun); Sync hook not wired")
	}
}

// TestApplyAblationFlags_SyncsCapabilityAtomic — companion to
// TestApplyAblationFlagsTo_PhaseThreeSetsBoolsAndSyncsAtomic. The
// syncHooks slice must include capability.SyncDisableUpload; without
// it, capability.IsUploadDisabled() (which reads a mirroring
// atomic.Bool) stays stale after ApplyAblationFlags(NoCapabilityDiscovery)
// even though the raw *bool was flipped. The two owner packages that
// ship atomic mirrors (`capability`, `validator`) each need their own
// end-to-end test — a single test on validator would mask a
// regression where capability.SyncDisableUpload is dropped from
// syncHooks.
func TestApplyAblationFlags_SyncsCapabilityAtomic(t *testing.T) {
	resetGlobalAblationState(t)
	if err := ApplyAblationFlags([]ablation.FlagName{ablation.FlagName("NoCapabilityDiscovery")}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !capability.IsUploadDisabled() {
		t.Errorf("IsUploadDisabled false after ApplyAblationFlags(NoCapabilityDiscovery); capability.SyncDisableUpload not in syncHooks")
	}
	// Reset via ApplyAblationFlags(nil) MUST also mirror to the atomic
	// (Phase 2 runs the sync hooks after clearing the raw bools).
	if err := ApplyAblationFlags(nil); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if capability.IsUploadDisabled() {
		t.Errorf("IsUploadDisabled still true after ApplyAblationFlags(nil); reset-phase sync did not mirror")
	}
}

// TestApplyAblationFlags_SyncsValidatorAtomicOnReset — mirror of
// TestApplyAblationFlags_SyncsCapabilityAtomic for validator. The
// existing TestApplyAblationFlagsTo_PhaseThreeSetsBoolsAndSyncsAtomic
// asserts sync-after-apply but NOT sync-after-reset for validator.
// Together they cover both directions for both atomic-mirror owner
// packages so a regression in either sync hook is caught immediately.
func TestApplyAblationFlags_SyncsValidatorAtomicOnReset(t *testing.T) {
	resetGlobalAblationState(t)
	if err := ApplyAblationFlags([]ablation.FlagName{ablation.FlagName("NoDryRun")}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !validator.IsDryRunDisabled() {
		t.Fatalf("pre-reset: IsDryRunDisabled false; sync hook not wired")
	}
	if err := ApplyAblationFlags(nil); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if validator.IsDryRunDisabled() {
		t.Errorf("IsDryRunDisabled still true after ApplyAblationFlags(nil); reset-phase sync did not mirror")
	}
}

// -----------------------------------------------------------------------------
// ScrubAmbientAblationEnv + Python bridge
// -----------------------------------------------------------------------------

func TestScrubAmbientAblationEnv_ClearsParent(t *testing.T) {
	t.Setenv(EnvNoAcceptanceGate, "1")
	ScrubAmbientAblationEnv()
	if got := os.Getenv(EnvNoAcceptanceGate); got != "" {
		t.Fatalf("env: got %q, want empty", got)
	}
}

func TestScrubAmbientAblationEnv_Idempotent(t *testing.T) {
	ScrubAmbientAblationEnv()
	ScrubAmbientAblationEnv() // no panic, no error
	if got := os.Getenv(EnvNoAcceptanceGate); got != "" {
		t.Errorf("env: %q", got)
	}
}

func TestApplyAblationFlags_PythonBridgeExportsEnv(t *testing.T) {
	resetGlobalAblationState(t)
	if err := ApplyAblationFlags([]ablation.FlagName{ablation.FlagName("NoAcceptanceGate")}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := os.Getenv(EnvNoAcceptanceGate); got != "1" {
		t.Errorf("env: got %q, want 1", got)
	}
}

func TestApplyAblationFlags_NoBridgeWhenFlagUnset(t *testing.T) {
	resetGlobalAblationState(t)
	if err := ApplyAblationFlags([]ablation.FlagName{ablation.FlagName("NoObserver")}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := os.Getenv(EnvNoAcceptanceGate); got != "" {
		t.Errorf("env: got %q, want empty", got)
	}
}

func TestPythonBridgeKeysAreCanonical(t *testing.T) {
	known := map[ablation.FlagName]bool{}
	for _, fn := range ablation.KnownFlags() {
		known[fn] = true
	}
	for fn := range ablationEnvExports {
		if !known[fn] {
			t.Errorf("ablationEnvExports key %q is not in KnownFlags()", string(fn))
		}
	}
}

func TestApplyAblationFlags_ResetsStaleState(t *testing.T) {
	resetGlobalAblationState(t)
	if err := ApplyAblationFlags([]ablation.FlagName{ablation.FlagName("NoObserver")}); err != nil {
		t.Fatalf("apply 1: %v", err)
	}
	if !evalrun.DisableTelemetry {
		t.Fatalf("apply 1: DisableTelemetry not flipped")
	}
	if err := ApplyAblationFlags(nil); err != nil {
		t.Fatalf("apply nil: %v", err)
	}
	if evalrun.DisableTelemetry {
		t.Errorf("DisableTelemetry still true after ApplyAblationFlags(nil)")
	}
	if os.Getenv(EnvNoAcceptanceGate) != "" {
		t.Errorf("bridge env still set after ApplyAblationFlags(nil)")
	}
}

func TestApplyAblationFlags_AllKnownFlagsBind(t *testing.T) {
	resetGlobalAblationState(t)
	for _, fn := range ablation.KnownFlags() {
		fn := fn
		t.Run(string(fn), func(t *testing.T) {
			if err := ApplyAblationFlags([]ablation.FlagName{fn}); err != nil {
				t.Fatalf("flag %s: %v (owner package likely not linked; add a blank import to flags.go)", string(fn), err)
			}
			// Reset between sub-tests to keep isolation.
			if err := ApplyAblationFlags(nil); err != nil {
				t.Fatalf("reset after %s: %v", string(fn), err)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// ListAblationsText
// -----------------------------------------------------------------------------

func TestListAblations_CountMatchesRegistry(t *testing.T) {
	want := len(ablation.KnownFlags())
	got := strings.Count(ListAblationsText(), "\n")
	if got != want {
		t.Fatalf("line count = %d, want %d (KnownFlags)", got, want)
	}
	if len(ablation.Default.List()) != want {
		t.Fatalf("Default.List() len = %d, want %d; canonical flag added without Register",
			len(ablation.Default.List()), want)
	}
}

func TestListAblations_LineFormat(t *testing.T) {
	line := regexp.MustCompile(`^No[A-Z][A-Za-z0-9]{2,31}\t\[[A-D][0-9]\] [A-Za-z][A-Za-z0-9 ,.;()-]{4,119}\.$`)
	for i, l := range strings.Split(strings.TrimRight(ListAblationsText(), "\n"), "\n") {
		if !line.MatchString(l) {
			t.Errorf("line %d does not match schema: %q", i, l)
		}
	}
}

func TestListAblations_NoSensitiveContent(t *testing.T) {
	blacklist := []string{
		"/", "\\",
		"LOOM_", "AGENTSERVER_", "OPENAI_",
		"http://", "https://",
		"skills/", "internal/", "tools/",
		"token", "secret", "bearer", "key",
		"config.toml", ".yaml", ".codex",
	}
	for _, l := range strings.Split(strings.TrimRight(ListAblationsText(), "\n"), "\n") {
		lower := strings.ToLower(l)
		for _, b := range blacklist {
			if strings.Contains(lower, strings.ToLower(b)) {
				t.Errorf("line %q contains blacklisted substring %q", l, b)
			}
		}
	}
}

func TestListAblations_SortedAscending(t *testing.T) {
	lines := strings.Split(strings.TrimRight(ListAblationsText(), "\n"), "\n")
	prev := ""
	for i, l := range lines {
		name := strings.SplitN(l, "\t", 2)[0]
		if i > 0 && name <= prev {
			t.Errorf("line %d name %q not > prev %q", i, name, prev)
		}
		prev = name
	}
}

func TestListAblations_DescriptionCoversAll8Flags(t *testing.T) {
	for _, fn := range ablation.KnownFlags() {
		if strings.TrimSpace(flagDescriptions[fn]) == "" {
			t.Errorf("flagDescriptions[%q] empty; every canonical flag needs a line", string(fn))
		}
	}
}

// -----------------------------------------------------------------------------
// Drift audits (Phase G)
// -----------------------------------------------------------------------------

// TestFlagsGo_NarrowAblationSurface walks flags.go's AST and asserts
// every dotted reference into the `ablation` package or any owner
// package is on the narrow whitelist described in spec §7(e).
func TestFlagsGo_NarrowAblationSurface(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "flags.go", nil, 0)
	if err != nil {
		t.Fatalf("parse flags.go: %v", err)
	}
	ablationAllowed := map[string]bool{
		"ablation.FlagName":          true,
		"ablation.ErrUnknownFlag":    true,
		"ablation.ErrNotRegistered":  true,
		"ablation.KnownFlags":        true,
		"ablation.Default":           true,
		"ablation.Default.SetByName": true,
		"ablation.Default.List":      true,
		"ablation.Registry":          true,
	}
	ownerPkgs := map[string]bool{
		"capability": true, "contract": true, "validator": true,
		"driver": true, "evalrun": true,
	}
	// resolveSelector returns "" if the selector's base is not a
	// bare identifier; otherwise the dotted path.
	var resolveSelector func(ast.Expr) string
	resolveSelector = func(e ast.Expr) string {
		switch v := e.(type) {
		case *ast.Ident:
			return v.Name
		case *ast.SelectorExpr:
			base := resolveSelector(v.X)
			if base == "" {
				return ""
			}
			return base + "." + v.Sel.Name
		}
		return ""
	}
	seen := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		path := resolveSelector(sel)
		if path == "" || seen[path] {
			return true
		}
		seen[path] = true
		root := strings.SplitN(path, ".", 2)[0]
		if root == "ablation" {
			if !ablationAllowed[path] {
				t.Errorf("forbidden ablation ref in flags.go: %s", path)
			}
			return true
		}
		if ownerPkgs[root] {
			last := path
			if i := strings.LastIndex(path, "."); i >= 0 {
				last = path[i+1:]
			}
			if !strings.HasPrefix(last, "Sync") {
				t.Errorf("forbidden owner-pkg ref in flags.go: %s (only Sync* accessors allowed)", path)
			}
		}
		return true
	})
}

// TestFlagsGoBlankImports parses flags.go and asserts every owning
// package for a canonical flag has a live or blank import. The
// production surface uses `capability` and `validator` live (for
// Sync hooks) and the other four owner packages blank.
func TestFlagsGoBlankImports(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "flags.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	seen := map[string]bool{}
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		seen[path] = true
	}
	// Every canonical flag has an owner package; the flag→pkg
	// mapping mirrors the §6 spec table.
	required := []string{
		"github.com/yourorg/multi-agent/internal/ablation",
		"github.com/yourorg/multi-agent/internal/capability",
		"github.com/yourorg/multi-agent/internal/contract",
		"github.com/yourorg/multi-agent/internal/contract/validator",
		"github.com/yourorg/multi-agent/internal/driver",
		"github.com/yourorg/multi-agent/internal/evalrun",
	}
	for _, r := range required {
		if !seen[r] {
			t.Errorf("flags.go missing import %s", r)
		}
	}
}

// TestBaselineOrAblationCapCoversAll8Flags computes the actual
// 8-flag sorted-join length and asserts it stays strictly below the
// declared cap. A future 9th flag that pushes past the cap fails
// here BEFORE it lands in production.
func TestBaselineOrAblationCapCoversAll8Flags(t *testing.T) {
	names := make([]string, 0, len(ablation.KnownFlags()))
	for _, fn := range ablation.KnownFlags() {
		names = append(names, string(fn))
	}
	sort.Strings(names)
	joined := strings.Join(names, "+")
	if len(joined) >= MaxBaselineOrAblationLen {
		t.Fatalf("8-flag join length %d not below cap %d: %s",
			len(joined), MaxBaselineOrAblationLen, joined)
	}
}

// TestOpts_HasAblationFields is the reflect-based check that
// Opts carries the two fields the CLI plumbs.
func TestOpts_HasAblationFields(t *testing.T) {
	tp := reflect.TypeOf(Opts{})
	if _, ok := tp.FieldByName("AblationFlags"); !ok {
		t.Errorf("Opts.AblationFlags missing")
	}
	if _, ok := tp.FieldByName("BaselineName"); !ok {
		t.Errorf("Opts.BaselineName missing")
	}
	if _, ok := tp.FieldByName("BaselineOrAblation"); ok {
		t.Errorf("Opts.BaselineOrAblation should NOT exist (label is derived, spec §7(a.3))")
	}
}

// TestCSVColumns_IncludesBaselineOrAblation asserts the new column
// is at the tail of the frozen order.
func TestCSVColumns_IncludesBaselineOrAblation(t *testing.T) {
	cols := CSVColumns()
	if cols[len(cols)-1] != "baseline_or_ablation" {
		t.Fatalf("baseline_or_ablation not at CSV tail: %v", cols)
	}
}

// TestRun_CallsValidateBaselineOrAblation walks runner.go's Run
// function AST and asserts a ValidateBaselineOrAblation call exists
// between the ComputeBaselineOrAblation call and the RunRow
// composite literal (spec §7(c) dual enforcement).
func TestRun_CallsValidateBaselineOrAblation(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "runner.go", nil, 0)
	if err != nil {
		t.Fatalf("parse runner.go: %v", err)
	}
	var runFn *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "Run" && fd.Recv == nil {
			runFn = fd
			break
		}
	}
	if runFn == nil {
		t.Fatalf("Run function not found in runner.go")
	}
	var (
		computePos, validatePos, rowLitPos token.Pos
	)
	ast.Inspect(runFn, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.CallExpr:
			if id, ok := v.Fun.(*ast.Ident); ok {
				if id.Name == "ComputeBaselineOrAblation" && computePos == token.NoPos {
					computePos = v.Pos()
				}
				if id.Name == "ValidateBaselineOrAblation" && validatePos == token.NoPos {
					validatePos = v.Pos()
				}
			}
		case *ast.CompositeLit:
			if id, ok := v.Type.(*ast.Ident); ok && id.Name == "RunRow" && rowLitPos == token.NoPos {
				rowLitPos = v.Pos()
			}
		}
		return true
	})
	if computePos == token.NoPos {
		t.Fatalf("Run does not call ComputeBaselineOrAblation")
	}
	if validatePos == token.NoPos {
		t.Fatalf("Run does not call ValidateBaselineOrAblation (spec §7(c) dual enforcement)")
	}
	if rowLitPos == token.NoPos {
		t.Fatalf("Run does not assemble a RunRow composite literal")
	}
	if !(computePos < validatePos && validatePos < rowLitPos) {
		t.Fatalf("expected Compute < Validate < RunRow{}; got %d < %d < %d",
			computePos, validatePos, rowLitPos)
	}
}

// -----------------------------------------------------------------------------
// End-to-end runner and CLI tests
// -----------------------------------------------------------------------------
//
// These live in flags_test.go (not runner_test.go) so the ablation-flag
// negative paths stay close to the code they exercise. All of them
// reuse the withShims / stubBinaryPath / pickFreePort /
// findRepoModuleRoot / readCSV helpers from runner_test.go.

func runOptsForTest(t *testing.T) Opts {
	t.Helper()
	root := findRepoModuleRoot(t)
	withShims(t, commitMetaJSON(), "alice@example.com|alice@example.com")
	return Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: filepath.Join(root, "tests/eval/workloads"),
		StubListen:  pickFreePort(t),
		StubBin:     stubBinaryPath(t),
	}
}

func TestRun_LabelDerivedFromApplied_NoAblation(t *testing.T) {
	resetGlobalAblationState(t)
	opts := runOptsForTest(t)
	opts.OutCSV = filepath.Join(t.TempDir(), "run.csv")
	res := Run(context.Background(), opts)
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d err=%v", res.ExitCode, res.Err)
	}
	if res.Row.BaselineOrAblation != DefaultBaselineName {
		t.Errorf("baseline_or_ablation = %q, want %q", res.Row.BaselineOrAblation, DefaultBaselineName)
	}
	rows := readCSV(t, opts.OutCSV)
	last := len(rows[0]) - 1
	if rows[0][last] != "baseline_or_ablation" {
		t.Fatalf("CSV last column = %q", rows[0][last])
	}
	if rows[1][last] != DefaultBaselineName {
		t.Errorf("CSV data last cell = %q, want %q", rows[1][last], DefaultBaselineName)
	}
}

func TestRun_LabelDerivedFromApplied_SingleFlag(t *testing.T) {
	resetGlobalAblationState(t)
	opts := runOptsForTest(t)
	opts.OutCSV = filepath.Join(t.TempDir(), "run.csv")
	opts.AblationFlags = []ablation.FlagName{ablation.FlagName("NoObserver")}
	res := Run(context.Background(), opts)
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d err=%v", res.ExitCode, res.Err)
	}
	if res.Row.BaselineOrAblation != "NoObserver" {
		t.Errorf("label = %q, want NoObserver", res.Row.BaselineOrAblation)
	}
}

func TestRun_LabelDerivedFromApplied_MultiSortedJoined(t *testing.T) {
	resetGlobalAblationState(t)
	opts := runOptsForTest(t)
	opts.OutCSV = filepath.Join(t.TempDir(), "run.csv")
	opts.AblationFlags = []ablation.FlagName{
		ablation.FlagName("NoObserver"),
		ablation.FlagName("NoAcceptanceGate"),
	}
	res := Run(context.Background(), opts)
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d err=%v", res.ExitCode, res.Err)
	}
	if res.Row.BaselineOrAblation != "NoAcceptanceGate+NoObserver" {
		t.Errorf("label = %q", res.Row.BaselineOrAblation)
	}
}

func TestRun_ScrubsAmbientLoomAblationEnv_BeforeSubprocess(t *testing.T) {
	resetGlobalAblationState(t)
	t.Setenv(EnvNoAcceptanceGate, "1")
	var captured string
	opts := runOptsForTest(t)
	opts.OutCSV = filepath.Join(t.TempDir(), "run.csv")
	opts.AgentStage = func(_ context.Context, _ *Workspace, _ *WorkloadSpec) error {
		captured = os.Getenv(EnvNoAcceptanceGate)
		return nil
	}
	res := Run(context.Background(), opts)
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d err=%v", res.ExitCode, res.Err)
	}
	if captured != "" {
		t.Errorf("AgentStage saw env %q; scrub did not fire before AgentStage", captured)
	}
	if got := os.Getenv(EnvNoAcceptanceGate); got != "" {
		t.Errorf("env after Run = %q; want empty", got)
	}
}

func TestRun_DeferResetsFlagsOnHappyPath(t *testing.T) {
	resetGlobalAblationState(t)
	opts := runOptsForTest(t)
	opts.OutCSV = filepath.Join(t.TempDir(), "run.csv")
	opts.AblationFlags = []ablation.FlagName{ablation.FlagName("NoObserver")}
	res := Run(context.Background(), opts)
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d err=%v", res.ExitCode, res.Err)
	}
	if evalrun.DisableTelemetry {
		t.Errorf("DisableTelemetry still true after happy-path Run")
	}
}

func TestRun_ApplyAfterAllExistingPreflights_NoFlipOnEarlyReject(t *testing.T) {
	resetGlobalAblationState(t)
	opts := Opts{
		WorkloadID:    "cross-device-code-mod",
		WorkloadDir:   filepath.Join(findRepoModuleRoot(t), "tests/eval/workloads"),
		StubListen:    "8.8.8.8:80",
		OutCSV:        filepath.Join(t.TempDir(), "run.csv"),
		AblationFlags: []ablation.FlagName{ablation.FlagName("NoObserver")},
	}
	res := Run(context.Background(), opts)
	if res.ExitCode != 2 {
		t.Fatalf("exit=%d, want 2", res.ExitCode)
	}
	if evalrun.DisableTelemetry {
		t.Errorf("DisableTelemetry flipped despite rejected preflight; spec §7(a) invariant broken")
	}
}

// TestRun_DeferResetsFlagsOnExit2AfterApply — apply an ablation
// flag, then trigger the oracle-too-large exit-2 path AFTER apply
// has flipped the flag; assert the flag is reset by the deferred
// cleanup (spec §7(a.4)).
func TestRun_DeferResetsFlagsOnExit2AfterApply(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash unavailable")
	}
	resetGlobalAblationState(t)
	withShims(t, commitMetaJSON(), "x@y|x@y")
	workloadDir := mkTestWorkload(t, "spew-workload-abl",
		"#!/bin/sh\nyes x | head -c 2097152\n", 60)
	outCSV := filepath.Join(t.TempDir(), "run.csv")
	res := Run(context.Background(), Opts{
		WorkloadID:    "spew-workload-abl",
		WorkloadDir:   workloadDir,
		StubListen:    pickFreePort(t),
		StubBin:       stubBinaryPath(t),
		OutCSV:        outCSV,
		AblationFlags: []ablation.FlagName{ablation.FlagName("NoObserver")},
	})
	if res.ExitCode != 2 {
		t.Fatalf("exit = %d, want 2 (ErrOracleOutputTooLarge)", res.ExitCode)
	}
	if evalrun.DisableTelemetry {
		t.Errorf("DisableTelemetry still true after exit-2-after-apply; deferred reset broke")
	}
}

func TestRun_BaselineNameCollidesWithFlag_Exit2(t *testing.T) {
	resetGlobalAblationState(t)
	opts := runOptsForTest(t)
	opts.OutCSV = filepath.Join(t.TempDir(), "run.csv")
	opts.BaselineName = "NoObserver"
	res := Run(context.Background(), opts)
	if res.ExitCode != 2 {
		t.Fatalf("exit=%d, want 2 (collision reject)", res.ExitCode)
	}
	if res.Err == nil || !strings.Contains(res.Err.Error(), "collides with an ablation flag name") {
		t.Errorf("Err missing collision marker: %v", res.Err)
	}
}

func TestRun_BaselineNameInvalid_Exit2(t *testing.T) {
	resetGlobalAblationState(t)
	opts := runOptsForTest(t)
	opts.OutCSV = filepath.Join(t.TempDir(), "run.csv")
	opts.BaselineName = "Has Space"
	res := Run(context.Background(), opts)
	if res.ExitCode != 2 {
		t.Fatalf("exit=%d, want 2 (invalid reject)", res.ExitCode)
	}
	if res.Err == nil || !errors.Is(res.Err, ErrBaselineOrAblationInvalid) {
		t.Errorf("Err missing invalid sentinel: %v", res.Err)
	}
}

func TestRun_BaselineNamePlusJoinedForged_Exit2(t *testing.T) {
	resetGlobalAblationState(t)
	opts := runOptsForTest(t)
	opts.OutCSV = filepath.Join(t.TempDir(), "run.csv")
	opts.BaselineName = "NoObserver+NoAcceptanceGate"
	res := Run(context.Background(), opts)
	if res.ExitCode != 2 {
		t.Fatalf("exit=%d, want 2 (forgery reject)", res.ExitCode)
	}
}

func TestMain_ListAblations_Exit0(t *testing.T) {
	if testing.Short() {
		t.Skip("skips end-to-end binary build")
	}
	bin := filepath.Join(t.TempDir(), "eval-runner")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	out, err := exec.Command(bin, "--list-ablations").Output()
	if err != nil {
		t.Fatalf("--list-ablations: %v", err)
	}
	if got := strings.Count(string(out), "\n"); got != 8 {
		t.Errorf("line count = %d, want 8", got)
	}
}

func TestMain_UnknownFlagExit2(t *testing.T) {
	if testing.Short() {
		t.Skip("skips end-to-end binary build")
	}
	bin := filepath.Join(t.TempDir(), "eval-runner")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	cmd := exec.Command(bin, "run",
		"--workload", "cross-device-code-mod",
		"--workload-dir", filepath.Join(findRepoModuleRoot(t), "tests/eval/workloads"),
		"--stub-listen", "127.0.0.1:1",
		"--out", filepath.Join(t.TempDir(), "run.csv"),
		"--ablation", "NoTpedContracts")
	err := cmd.Run()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 2 {
		t.Fatalf("exit = %v, want ExitError code 2", err)
	}
}

func TestMain_BaselineNameInvalid_Exit2(t *testing.T) {
	if testing.Short() {
		t.Skip("skips end-to-end binary build")
	}
	bin := filepath.Join(t.TempDir(), "eval-runner")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	cmd := exec.Command(bin, "run",
		"--workload", "cross-device-code-mod",
		"--workload-dir", filepath.Join(findRepoModuleRoot(t), "tests/eval/workloads"),
		"--stub-listen", "127.0.0.1:1",
		"--out", filepath.Join(t.TempDir(), "run.csv"),
		"--baseline-name", "Has Space")
	err := cmd.Run()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 2 {
		t.Fatalf("exit = %v, want ExitError code 2", err)
	}
}

// TestMain_ScrubsParentEnvBeforeParse — end-to-end: the operator
// starts the binary with LOOM_ABLATION_NOACCEPTANCEGATE=1 already
// in the environment (parent process leaked it in). Without a
// scrub, WhitelistEnv would propagate the value through to the
// oracle subprocess, silently activating the Python acceptance-gate
// bypass — while the CSV row's baseline_or_ablation stays
// "full_loom" (no --ablation applied).
//
// Test builds a workload whose oracle prints its inherited
// LOOM_* env vars to a file we can read back. Invokes the binary
// with the bridge env set. Asserts the oracle saw the bridge var
// as EMPTY and the CSV label stayed "full_loom".
func TestMain_ScrubsParentEnvBeforeParse(t *testing.T) {
	if testing.Short() {
		t.Skip("skips end-to-end binary build")
	}
	if runtime.GOOS == "windows" {
		t.Skip("bash-based oracle unavailable")
	}
	root := findRepoModuleRoot(t)
	bin := filepath.Join(t.TempDir(), "eval-runner")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	envDump := filepath.Join(t.TempDir(), "oracle-env.txt")
	// Oracle writes the LOOM_ABLATION_NOACCEPTANCEGATE env var
	// value it saw to envDump, then prints a passing JSON line.
	oracleBody := "#!/bin/sh\n" +
		"printf '%s' \"${LOOM_ABLATION_NOACCEPTANCEGATE:-EMPTY}\" > " + envDump + "\n" +
		`echo '{"passed":true,"details":{},"metrics":{}}'` + "\n"
	workloadDir := mkTestWorkload(t, "env-scrub-probe", oracleBody, 30)
	outCSV := filepath.Join(t.TempDir(), "run.csv")
	commitShim := commitMetaShim(t, commitMetaJSON())
	emailShim := gitEmailShim(t, "a@b|c@d")
	cmd := exec.Command(bin, "run",
		"--workload", "env-scrub-probe",
		"--workload-dir", workloadDir,
		"--stub-listen", pickFreePort(t),
		"--out", outCSV,
	)
	cmd.Env = append(os.Environ(),
		EnvNoAcceptanceGate+"=1",
		"LOOM_EVAL_COMMIT_META_CMD="+commitShim,
		"LOOM_EVAL_GIT_EMAIL_CMD="+emailShim,
		"PATH="+os.Getenv("PATH")+":"+filepath.Join(root, "..", "tools"),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Non-zero exit is tolerated (oracle-first-line format may
		// mismatch), what matters is the oracle ran and the env
		// dump exists.
		t.Logf("cmd err (tolerated): %v\n%s", err, out)
	}
	dump, err := os.ReadFile(envDump)
	if err != nil {
		t.Fatalf("oracle env dump not written; oracle didn't run: %v", err)
	}
	if string(dump) != "EMPTY" {
		t.Errorf("oracle saw LOOM_ABLATION_NOACCEPTANCEGATE=%q; scrub failed (want EMPTY)", dump)
	}
	// The CSV row should carry the default label — no --ablation
	// was passed.
	if _, err := os.Stat(outCSV); err == nil {
		rows := readCSV(t, outCSV)
		if len(rows) >= 2 {
			last := len(rows[0]) - 1
			if rows[0][last] != "baseline_or_ablation" {
				t.Fatalf("CSV last column = %q", rows[0][last])
			}
			if rows[1][last] != DefaultBaselineName {
				t.Errorf("CSV label = %q, want %q", rows[1][last], DefaultBaselineName)
			}
		}
	}
}

func TestMain_BaselineNameCollidesWithFlag_Exit2(t *testing.T) {
	if testing.Short() {
		t.Skip("skips end-to-end binary build")
	}
	bin := filepath.Join(t.TempDir(), "eval-runner")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	cmd := exec.Command(bin, "run",
		"--workload", "cross-device-code-mod",
		"--workload-dir", filepath.Join(findRepoModuleRoot(t), "tests/eval/workloads"),
		"--stub-listen", "127.0.0.1:1",
		"--out", filepath.Join(t.TempDir(), "run.csv"),
		"--baseline-name", "NoObserver")
	stderr, _ := cmd.CombinedOutput()
	if !strings.Contains(string(stderr), "collides with an ablation flag name") {
		t.Errorf("stderr missing collision marker: %s", stderr)
	}
	if cmd.ProcessState.ExitCode() != 2 {
		t.Errorf("exit = %d, want 2", cmd.ProcessState.ExitCode())
	}
}

// Compile-time reference so the linked-but-otherwise-unused owner
// packages don't get pruned by IDE tooling that flags "unused"
// imports. Each accessor is exported; calling it is cheap.
func TestOwnerPackagesLinked(t *testing.T) {
	_ = capability.IsUploadDisabled()
	_ = contract.DisableSchemaEnforce
	_ = validator.IsDryRunDisabled()
}

// -----------------------------------------------------------------------------
// README audit (Phase F)
// -----------------------------------------------------------------------------

// TestReadmeContainsSQLAndPandasSnippets grep-audits the README.
func TestReadmeContainsSQLAndPandasSnippets(t *testing.T) {
	b, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	s := string(b)
	for _, needle := range []string{"SELECT", "GROUP BY", "baseline_or_ablation", "pd.read_csv", "--list-ablations"} {
		if !strings.Contains(s, needle) {
			t.Errorf("README missing %q", needle)
		}
	}
}

// TestReadmeMetricMapping cross-checks the README's flag/section
// table against ablation.KnownFlags and against 12 号 §A/§B/§D
// canonical metric names.
func TestReadmeMetricMapping(t *testing.T) {
	rb, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	// Find the flag rows: lines starting with `| Nox…` inside the
	// table. Extract three fields via a permissive regex.
	rowRe := regexp.MustCompile(`^\|\s+(No[A-Z][A-Za-z0-9]+)\s+\|.*\|\s+(§[A-D][0-9])\s+\|\s+(.+?)\s+\|\s*$`)
	rows := map[string][2]string{} // flagName -> [section, impactCell]
	for _, line := range strings.Split(string(rb), "\n") {
		m := rowRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		rows[m[1]] = [2]string{m[2], m[3]}
	}
	if len(rows) != len(ablation.KnownFlags()) {
		t.Fatalf("README table has %d flag rows, want %d", len(rows), len(ablation.KnownFlags()))
	}
	for _, fn := range ablation.KnownFlags() {
		row, ok := rows[string(fn)]
		if !ok {
			t.Errorf("README missing row for %q", string(fn))
			continue
		}
		section := row[0]
		impact := row[1]
		if impact == "" || strings.EqualFold(strings.TrimSpace(impact), "tbd") {
			t.Errorf("README row %q has empty/TBD impact", string(fn))
		}
		// Section letter must match flagDescriptions bracket.
		desc := flagDescriptions[fn]
		if !strings.Contains(desc, "["+strings.TrimPrefix(section, "§")+"]") {
			t.Errorf("README row %q section %s does not match flagDescriptions bracket %q",
				string(fn), section, desc)
		}
		// Cross-check against 12 号: locate the section's own
		// content (either a `| Xn |` table row or a
		// `- **Xn**` bullet). §A/§D rows in 12 号 are table
		// rows; §B rows are bullet-list items in `11 号 §6` /
		// `12 号 §B`. Both forms carry the metric names we
		// need for the mapping check.
		paperPath := "/root/paper_writing/docs/intermediate/12_loom_development_tasks_for_v3.md"
		pb, err := os.ReadFile(paperPath)
		if err != nil {
			t.Skipf("paper doc unreadable, skipping cross-check: %v", err)
			return
		}
		key := strings.TrimPrefix(section, "§")
		tableKey := "| " + key + " |"
		bulletKey := "- **" + key + "**"
		var paperRow string
		for _, l := range strings.Split(string(pb), "\n") {
			if strings.HasPrefix(l, tableKey) || strings.HasPrefix(l, bulletKey) {
				paperRow = l
				break
			}
		}
		if paperRow == "" {
			// Neither form found; section is documented
			// elsewhere (e.g. via a reference table with a
			// different header). Emit a warning but do not
			// fail — the section-letter drift check is what
			// this test is really about.
			t.Logf("12 号 %s row not directly found; skipping metric cross-check for %q",
				section, string(fn))
			continue
		}
		// Extract candidate metric names from the paper row and
		// (best-effort) require at least one to appear in the
		// impact cell. Some paper rows describe the flag
		// without naming a metric by CamelCase; those cases
		// pass the check trivially.
		metricRe := regexp.MustCompile(`[A-Z][A-Za-z0-9]+(Rate|Count|Overhead|Latency|Completeness|Time|Throughput|Accuracy)`)
		metrics := metricRe.FindAllString(paperRow, -1)
		if len(metrics) == 0 {
			continue
		}
		hit := false
		for _, m := range metrics {
			if strings.Contains(impact, m) {
				hit = true
				break
			}
		}
		if !hit {
			t.Errorf("README row %q impact %q mentions none of the §%s canonical metrics %v — invented metric names not allowed (spec §7(f))",
				string(fn), impact, section, metrics)
		}
	}
	// Belt-and-braces: fail loudly if the file layout drifts.
	if _, err := os.Stat(filepath.Clean("README.md")); err != nil {
		t.Errorf("README stat: %v", err)
	}
}
