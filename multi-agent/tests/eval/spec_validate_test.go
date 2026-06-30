package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// oracleTestTimeout is the per-invocation execution ceiling for an oracle
// run under tests.  R8-M3: a hung oracle (infinite loop, read-on-tty, lock
// contention) would otherwise block until Go's package-level test timeout
// (default 10m).  Real workload specs declare much longer timeouts (up to
// 86400s) but unit tests against mock workspaces all complete in <100ms,
// so a tight ceiling is safe and surfaces hangs quickly.  We deliberately
// IGNORE spec.TimeoutSeconds here and use a flat ceiling so the test
// harness is decoupled from production trial budgets.
const oracleTestTimeout = 60 * time.Second

// runOracleCmd is the single chokepoint for invoking an oracle binary
// under test with a deadline.  All oracle-execing tests should go through
// runOracle (or this helper) so the timeout invariant is uniform.  Returns
// the *exec.Cmd ready to Run/CombinedOutput plus a cancel func the caller
// MUST defer.
func runOracleCmd(t *testing.T, oraclePath, workspace string) (*exec.Cmd, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), oracleTestTimeout)
	cmd := exec.CommandContext(ctx, oraclePath, workspace)
	return cmd, cancel
}

// R5-S2: TestWorkloadRoot_RejectsStrayFile creates a probe file under
// workloads/ and removes it via t.Cleanup; if a prior run was killed
// mid-test the probe would survive and turn every subsequent
// TestWorkloadDirectoryMatchesExpectedSet invocation red.  Sweep any
// leftover .round*_stray_probe* files at package-test startup so the
// failure mode is recoverable without manual filesystem surgery.
// (A full refactor of the scan logic to accept an injected root is left
// out of scope; this surgical cleanup costs three lines and addresses
// the recoverability concern flagged in review.)
// strayProbeLiterals is the canonical sweep list for stray probe files
// that prior test runs may have left behind.  R6-S2 narrowed the previous
// `.round*_stray_probe*` glob to a fixed literal list to stop deleting
// developer scratch like `.round999_stray_probe_keep_me`.  R7-S2 extracted
// the sweep into sweepStrayProbes() so TestStrayProbeSweepIsNarrow can
// actually drive it (init() runs once at package load, before any test
// body — a test that creates a probe file after init has no way to
// observe init's behaviour).
var strayProbeLiterals = []string{
	"workloads/.round2_stray_probe",
}

// sweepStrayProbes removes the canonical set of test-managed probe files.
// It is intentionally narrow: extend strayProbeLiterals rather than
// widening to a glob if a future test introduces a new probe.
func sweepStrayProbes() {
	for _, p := range strayProbeLiterals {
		_ = os.Remove(p)
	}
}

func init() {
	sweepStrayProbes()
}

// canonicalPlatforms is the closed set of allowed required_contexts.platform
// values.  Adding a new one requires editing this slice AND updating every
// callsite that branches on platform (oracles, harness routing).
var canonicalPlatforms = []string{"linux", "darwin", "windows", "any"}

// validatePlatform enforces R3-S5(b): every required_contexts.platform must
// be one of canonicalPlatforms.  Empty / wrong case / unknown OS rejected.
func validatePlatform(p string) error {
	for _, ok := range canonicalPlatforms {
		if p == ok {
			return nil
		}
	}
	return fmt.Errorf("platform %q not in %v (R3-S5)", p, canonicalPlatforms)
}

// validateTimeoutSeconds enforces R3-S5(a): timeout must be in (0, 86400].
// Zero / negative is meaningless; over a day signals a fat-finger that
// would let a runaway trial chew through the eval budget.
func validateTimeoutSeconds(s int) error {
	if s <= 0 {
		return fmt.Errorf("timeout_seconds %d must be > 0 (R3-S5)", s)
	}
	if s > 86400 {
		return fmt.Errorf("timeout_seconds %d must be <= 86400 (R3-S5)", s)
	}
	return nil
}

// validateAllowedContexts enforces R3-S5(c): allowed_contexts must be
// exactly ["*"] OR a non-empty subset of the declared required_contexts.role
// values.  Empty, [["*","driver"], or unknown roles are all rejected.
func validateAllowedContexts(allowed []string, declaredRoles []string) error {
	if len(allowed) == 0 {
		return fmt.Errorf("allowed_contexts must be non-empty (use [\"*\"] for unrestricted) (R3-S5)")
	}
	hasStar := false
	for _, a := range allowed {
		if a == "*" {
			hasStar = true
		}
	}
	if hasStar && len(allowed) != 1 {
		return fmt.Errorf("allowed_contexts containing \"*\" must be exactly [\"*\"]; got %v (R3-S5)", allowed)
	}
	if hasStar {
		return nil
	}
	// R5-N1: reject duplicates like ["driver","driver"].  A duplicate is
	// strictly redundant; if a spec author wrote one, it likely indicates
	// a copy-paste error or unfinished edit that deserves a hard error
	// rather than silent acceptance.
	seen := map[string]struct{}{}
	for _, a := range allowed {
		if _, dup := seen[a]; dup {
			return fmt.Errorf("allowed_contexts has duplicate entry %q (R5-N1)", a)
		}
		seen[a] = struct{}{}
	}
	known := map[string]struct{}{}
	for _, r := range declaredRoles {
		known[r] = struct{}{}
	}
	for _, a := range allowed {
		if _, ok := known[a]; !ok {
			return fmt.Errorf("allowed_contexts entry %q is not a declared required_contexts.role (declared: %v) (R3-S5)", a, declaredRoles)
		}
	}
	return nil
}

// expectedWorkloads is the canonical list of 5 E1 macrobenchmark workloads.
// Adding or removing one here must match a corresponding directory under
// tests/eval/workloads/.  R8-S5: a prior comment cited a docs file with a
// specific line range that does not exist in this worktree; dropped to keep
// the comment honest.  Dynamic discovery (R6-N4) is intentionally out of
// scope — this list is the contract.
var expectedWorkloads = []string{
	"cross-device-code-mod",
	"remote-data-processing",
	"windows-only-artifact",
	"missing-parser-converter",
	"credential-bound-model",
}

type contextSpec struct {
	Role     string   `yaml:"role"`
	Platform string   `yaml:"platform"`
	Tools    []string `yaml:"tools,omitempty"`
}

type artifactSpec struct {
	Kind string `yaml:"kind"`
	Path string `yaml:"path"`
}

type inputsSpec struct {
	ReadArtifacts []artifactSpec `yaml:"read_artifacts"`
}

type outputsSpec struct {
	WriteTargets []artifactSpec `yaml:"write_targets"`
}

type workloadSpec struct {
	ID               string        `yaml:"id"`
	Description      string        `yaml:"description"`
	RequiredContexts []contextSpec `yaml:"required_contexts"`
	AllowedContexts  []string      `yaml:"allowed_contexts"`
	Inputs           inputsSpec    `yaml:"inputs"`
	Outputs          outputsSpec   `yaml:"outputs"`
	SuccessOracle    string        `yaml:"success_oracle"`
	RecoveryHint     string        `yaml:"recovery_hint"`
	TimeoutSeconds   int           `yaml:"timeout_seconds"`
}

// loadSpec parses spec.yaml with strict unknown-field rejection so typos
// (e.g. `succes_oracle:`, `read_artifact:`) fail loudly instead of being
// silently dropped to the field's zero value.
func loadSpec(t *testing.T, dir string) workloadSpec {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "spec.yaml"))
	require.NoError(t, err, "read spec.yaml in %s", dir)
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var spec workloadSpec
	require.NoError(t, dec.Decode(&spec), "parse spec.yaml in %s", dir)
	return spec
}

// TestWorkloadDirectoryMatchesExpectedSet enforces the canonical list in
// both directions: every expected workload has a directory, and every
// entry under workloads/ — directory OR file — is in the expected list.
// The "every entry" form catches stray files (orphaned README, .DS_Store,
// rename-leftover dropped as a file, debug dump) that the
// directories-only form silently skipped.  Without the reverse check, a
// half-finished or renamed workload would be invisible to CI.
func TestWorkloadDirectoryMatchesExpectedSet(t *testing.T) {
	entries, err := os.ReadDir("workloads")
	require.NoError(t, err)
	gotDirs := map[string]struct{}{}
	var extra []string
	for _, e := range entries {
		// Every entry at the workloads root must be one of the
		// canonical workload directories.  Files (stray READMEs,
		// .DS_Store, leftover debug dumps) and non-canonical
		// directories both count as `extra`.
		want := map[string]struct{}{}
		for _, id := range expectedWorkloads {
			want[id] = struct{}{}
		}
		if _, ok := want[e.Name()]; !ok {
			extra = append(extra, e.Name())
			continue
		}
		require.True(t, e.IsDir(), "%s under workloads/ must be a directory (was a file)", e.Name())
		gotDirs[e.Name()] = struct{}{}
	}
	var missing []string
	for _, id := range expectedWorkloads {
		if _, ok := gotDirs[id]; !ok {
			missing = append(missing, id)
		}
	}
	sort.Strings(extra)
	sort.Strings(missing)
	require.Empty(t, extra, "unexpected entries under workloads/ (rename leftover? typo? stray file?): %v", extra)
	require.Empty(t, missing, "expected workload directories are missing: %v", missing)
}

func TestWorkloadSpecsExistAndValidate(t *testing.T) {
	root := "workloads"
	for _, id := range expectedWorkloads {
		id := id
		t.Run(id, func(t *testing.T) {
			dir := filepath.Join(root, id)
			info, err := os.Stat(dir)
			require.NoError(t, err, "workload directory %s must exist", dir)
			require.True(t, info.IsDir(), "%s must be a directory", dir)

			spec := loadSpec(t, dir)

			require.Equal(t, id, spec.ID, "spec.id must match directory name")
			require.NotEmpty(t, spec.Description, "description required")
			require.NotEmpty(t, spec.RequiredContexts, "required_contexts must be non-empty (ground-truth capabilities)")
			roles := make([]string, 0, len(spec.RequiredContexts))
			for i, ctx := range spec.RequiredContexts {
				require.NotEmpty(t, ctx.Role, "required_contexts[%d].role required", i)
				require.NotEmpty(t, ctx.Platform, "required_contexts[%d].platform required", i)
				require.NoError(t, validatePlatform(ctx.Platform),
					"required_contexts[%d].platform invalid", i)
				roles = append(roles, ctx.Role)
			}
			require.NotEmpty(t, spec.AllowedContexts, "allowed_contexts must be set (use [\"*\"] for unrestricted)")
			require.NoError(t, validateAllowedContexts(spec.AllowedContexts, roles),
				"allowed_contexts invalid")

			// inputs.read_artifacts is part of the contract; assert
			// symmetric to outputs.write_targets below.
			require.NotEmpty(t, spec.Inputs.ReadArtifacts, "inputs.read_artifacts must list at least one artifact")
			for i, in := range spec.Inputs.ReadArtifacts {
				require.NotEmpty(t, in.Kind, "inputs.read_artifacts[%d].kind required", i)
				require.NotEmpty(t, in.Path, "inputs.read_artifacts[%d].path required", i)
			}

			require.NotEmpty(t, spec.Outputs.WriteTargets, "outputs.write_targets must list at least one artifact")
			for i, out := range spec.Outputs.WriteTargets {
				require.NotEmpty(t, out.Kind, "outputs.write_targets[%d].kind required", i)
				require.NotEmpty(t, out.Path, "outputs.write_targets[%d].path required", i)
			}
			require.NotEmpty(t, spec.SuccessOracle, "success_oracle required")
			require.NoError(t, validateTimeoutSeconds(spec.TimeoutSeconds),
				"timeout_seconds out of bounds")
			require.NotEmpty(t, spec.RecoveryHint, "recovery_hint required (used by recovery evaluation)")

			oraclePath := filepath.Join(dir, spec.SuccessOracle)
			oinfo, err := os.Stat(oraclePath)
			require.NoError(t, err, "oracle %s must exist", oraclePath)
			require.False(t, oinfo.IsDir(), "oracle %s must be a file", oraclePath)
			require.NotZero(t, oinfo.Mode()&0o111, "oracle %s must be executable", oraclePath)
		})
	}
}

// scrubbedEnv returns a minimal env for invoking an oracle so a developer's
// shell exports (e.g. EXPECTED_MODEL_ALIAS, OPENAI_API_KEY, HTTP_PROXY)
// cannot leak in and change the in-tree self-check result.
//
// R3-S4: PATH is hard-coded to the standard system directories rather than
// inherited from the parent process.  Inheriting parent PATH would let a
// developer who prepended a shim directory (e.g. for a custom sha256sum or
// stat used in unrelated work) silently change the oracle's behaviour.  The
// oracle only needs core utilities (bash, grep, awk, sed, sha256sum, stat,
// wc, cmp, find, git) — all of which live in standard system directories.
func scrubbedEnv(t *testing.T) []string {
	t.Helper()
	return []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"LC_ALL=C",
		"LANG=C",
		"HOME=" + t.TempDir(),
	}
}

// copyDir recursively copies src into dst, preserving file modes.  Used to
// give each oracle a throwaway workspace so a mutating oracle cannot
// corrupt the in-tree fixture (which would silently make the next run
// pass for the wrong reason).
func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	require.NoError(t, filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	}))
}

// oracleResult is the JSON verdict every oracle.sh must emit on stdout as
// its first non-empty line.
type oracleResult struct {
	Passed  bool                   `json:"passed"`
	Details map[string]interface{} `json:"details"`
	Metrics map[string]interface{} `json:"metrics"`
}

// runOracle invokes the workload's oracle on a freshly copied mock
// workspace with a scrubbed env, and returns the parsed JSON output, the
// raw combined output (stdout+stderr for diagnostics / substring assertions),
// a parsed-OK flag, and the exec error.  The caller decides whether
// non-zero exit is expected.
//
// R3-S3: parsed indicates whether the first non-empty stdout line was
// well-formed JSON.  Negative tests must require parsed==true so that a
// future regression where the oracle crashes before printf-ing the
// verdict (e.g. an unbound variable trap, a stat failure that pollutes
// the first line) does not silently pass for the wrong reason.
//
// R8-S1: stdout and stderr are captured SEPARATELY now; the JSON-verdict
// parse runs only against stdout, so a stderr-first warning (e.g. a shim
// emitting a deprecation notice before the oracle's printf) cannot make
// `parsed` false-negative.  The returned `raw` keeps the historical
// combined-output shape so existing callers' `require.Contains(t, raw, ...)`
// substring assertions still work for both stdout and stderr content.
//
// R8-M3: every oracle invocation runs under a context with deadline
// (oracleTestTimeout, currently 60s).  A hung oracle is killed and the
// test fails with "context deadline exceeded" rather than blocking until
// Go's package-level test timeout.
func runOracle(t *testing.T, workloadID string, mutate func(workspace string), extraEnv ...string) (oracleResult, string, bool, error) {
	t.Helper()
	dir := filepath.Join("workloads", workloadID)
	spec := loadSpec(t, dir)
	oraclePath, err := filepath.Abs(filepath.Join(dir, spec.SuccessOracle))
	require.NoError(t, err)
	srcMock, err := filepath.Abs(filepath.Join(dir, "fixtures", "mock_workspace"))
	require.NoError(t, err)
	require.DirExists(t, srcMock, "fixtures/mock_workspace must exist for self-check")

	workspace := t.TempDir()
	copyDir(t, srcMock, workspace)
	if mutate != nil {
		mutate(workspace)
	}

	cmd, cancel := runOracleCmd(t, oraclePath, workspace)
	defer cancel()
	cmd.Env = append(scrubbedEnv(t), extraEnv...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	stdout := outBuf.String()
	// `raw` is combined-shape for diagnostics + back-compat with the many
	// callers that already do `require.Contains(t, raw, ...)` against the
	// previous CombinedOutput payload.  The PARSE below operates only on
	// stdout, so a stderr-first noise line cannot break the JSON-first-line
	// contract.
	raw := stdout + errBuf.String()

	var result oracleResult
	parsed := false
	for _, l := range strings.Split(stdout, "\n") {
		if s := strings.TrimSpace(l); s != "" {
			if json.Unmarshal([]byte(s), &result) == nil {
				parsed = true
			}
			break
		}
	}
	return result, raw, parsed, runErr
}

// TestWorkloadOraclesPassOnMockFixture runs each oracle against a freshly
// copied mock workspace and asserts exit 0 with valid JSON containing
// "passed": true.  The workspace is copied (not used in place) so a
// mis-behaving oracle that writes to its workspace can never corrupt the
// in-tree fixture.
func TestWorkloadOraclesPassOnMockFixture(t *testing.T) {
	for _, id := range expectedWorkloads {
		id := id
		t.Run(id, func(t *testing.T) {
			result, raw, parsed, err := runOracle(t, id, nil)
			require.NoError(t, err, "oracle exit non-zero: %s", raw)
			require.True(t, parsed, "oracle stdout must be valid JSON; raw=%s", raw)

			// R3-N3: every oracle invocation must emit at least one
			// detail key so a future regression that empties the
			// details array (and would otherwise leave reviewers
			// blind) is caught.
			require.NotEmpty(t, result.Details,
				"oracle must always emit at least one detail key (R3-N3); raw=%s", raw)

			// First non-empty line of stdout must be JSON with passed:true.
			var line string
			for _, l := range strings.Split(raw, "\n") {
				if s := strings.TrimSpace(l); s != "" {
					line = s
					break
				}
			}
			require.NotEmpty(t, line, "oracle produced no output")
			require.NoError(t, json.Unmarshal([]byte(line), &result), "oracle stdout must be JSON: %q", line)
			require.True(t, result.Passed, "mock fixture must satisfy oracle (got %v)", result)
		})
	}
}

// TestWorkloadJSONOutputsAreValid asserts that every workload's
// mock_workspace JSON file is structurally valid JSON.  This moves the
// JSON-validation responsibility off the bash oracles (which used
// `python3 -c json.load`, mis-classifying missing-python3 as malformed
// JSON) and into the Go test layer.
func TestWorkloadJSONOutputsAreValid(t *testing.T) {
	jsonOutputs := map[string][]string{
		"credential-bound-model":   {"route.json"},
		"missing-parser-converter": {"synthesized.mcp.json"},
		"windows-only-artifact":    {"artifact.meta.json"},
		"remote-data-processing":   {"result.json"},
	}
	for id, files := range jsonOutputs {
		id, files := id, files
		t.Run(id, func(t *testing.T) {
			for _, f := range files {
				p := filepath.Join("workloads", id, "fixtures", "mock_workspace", f)
				data, err := os.ReadFile(p)
				require.NoError(t, err, "read %s", p)
				var v interface{}
				require.NoError(t, json.Unmarshal(data, &v), "%s must be valid JSON", p)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Behavior tests for review-feedback fixes.  Each one captures a real bug
// fixed in the follow-up commit; they should fail against the original
// oracles and pass after the fix.
// ---------------------------------------------------------------------------

// F1: cross-device-code-mod must NOT report passed:true on a test.log that
// contains "passed" inside prose like "5 passed, 3 failed".
func TestCrossDeviceCodeMod_RejectsPassedSubstringInFailingLog(t *testing.T) {
	result, raw, parsed, err := runOracle(t, "cross-device-code-mod", func(ws string) {
		require.NoError(t, os.WriteFile(filepath.Join(ws, "test.log"),
			[]byte("FAIL: TestFoo\n5 passed, 3 failed\n"), 0o644))
	})
	require.Error(t, err, "oracle must fail on a log with a failure marker; raw=%s", raw)
	require.True(t, parsed, "oracle must still emit valid JSON on failure; raw=%s", raw)
	require.False(t, result.Passed, "must not pass on prose 'passed' inside a failing log")
}

// F1 positive control: a real `go test` PASS log must still satisfy.
func TestCrossDeviceCodeMod_AcceptsRealGoTestPassMarker(t *testing.T) {
	result, raw, parsed, err := runOracle(t, "cross-device-code-mod", func(ws string) {
		require.NoError(t, os.WriteFile(filepath.Join(ws, "test.log"),
			[]byte("--- PASS: TestThing (0.00s)\nPASS\nok  \texample.com/x\t0.001s\n"), 0o644))
	})
	require.NoError(t, err, "oracle must pass on a real go test log; raw=%s", raw)
	require.True(t, parsed, "oracle must emit valid JSON; raw=%s", raw)
	require.True(t, result.Passed)
}

// F6: cross-device-code-mod must reject a patch that contains only a header
// (no @@ hunk, no content lines).
func TestCrossDeviceCodeMod_RejectsHeaderOnlyPatch(t *testing.T) {
	result, raw, parsed, err := runOracle(t, "cross-device-code-mod", func(ws string) {
		require.NoError(t, os.WriteFile(filepath.Join(ws, "patch.diff"),
			[]byte("+++ b/x\n"), 0o644))
	})
	require.Error(t, err, "oracle must fail on header-only patch; raw=%s", raw)
	require.True(t, parsed, "oracle must emit valid JSON on failure; raw=%s", raw)
	require.False(t, result.Passed)
}

// F3: EXPECTED_MODEL_ALIAS='.*' must be rejected (regex injection that
// would otherwise turn the alias check into a wildcard).
func TestCredentialBoundModel_RejectsRegexInjectionAlias(t *testing.T) {
	_, raw, parsed, err := runOracle(t, "credential-bound-model", func(ws string) {
		// Even with a wrong alias in route.json, the env var must be
		// rejected before the alias check runs.
		data, rerr := os.ReadFile(filepath.Join(ws, "route.json"))
		require.NoError(t, rerr)
		swapped := bytes.Replace(data, []byte("acme-bound-model-v1"), []byte("totally-wrong"), 1)
		require.NoError(t, os.WriteFile(filepath.Join(ws, "route.json"), swapped, 0o644))
	}, "EXPECTED_MODEL_ALIAS=.*")
	require.Error(t, err, "oracle must reject wildcard alias; raw=%s", raw)
	require.True(t, parsed, "oracle must emit valid JSON on failure; raw=%s", raw)
	require.Contains(t, raw, "disallowed characters",
		"oracle must explicitly reject the env var, not silently bypass the alias check")
}

// F3: EXPECTED_MODEL_ALIAS containing a literal quote must be rejected
// (would otherwise produce invalid JSON via hand-rolled string interpolation).
func TestCredentialBoundModel_RejectsQuoteInAlias(t *testing.T) {
	_, raw, parsed, err := runOracle(t, "credential-bound-model", nil, `EXPECTED_MODEL_ALIAS=foo"bar`)
	require.Error(t, err)
	require.True(t, parsed, "oracle must emit valid JSON on failure; raw=%s", raw)
	require.Contains(t, raw, "disallowed characters")
	// Whatever line the oracle emits as JSON must still be parseable.
	for _, l := range strings.Split(raw, "\n") {
		s := strings.TrimSpace(l)
		if s == "" {
			continue
		}
		var v interface{}
		require.NoError(t, json.Unmarshal([]byte(s), &v),
			"oracle stdout must remain valid JSON even on bad env: %q", s)
		break
	}
}

// F5: spec.yaml declares run.log as a write_target, so the oracle must fail
// when run.log is missing.
func TestCredentialBoundModel_RequiresRunLog(t *testing.T) {
	result, raw, parsed, err := runOracle(t, "credential-bound-model", func(ws string) {
		require.NoError(t, os.Remove(filepath.Join(ws, "run.log")))
	})
	require.Error(t, err, "oracle must fail when declared run.log is missing; raw=%s", raw)
	require.True(t, parsed, "oracle must emit valid JSON on failure; raw=%s", raw)
	require.False(t, result.Passed)
	require.Contains(t, raw, "run_log",
		"failure detail must mention run_log so the cause is debuggable")
}

// F8: confirm the single-case design is intact — the canonical mock must
// pass and details must report 1/1 (not 0/N or N/M from a phantom loop).
func TestMissingParserConverter_SingleCaseContractIsExplicit(t *testing.T) {
	result, raw, parsed, err := runOracle(t, "missing-parser-converter", nil)
	require.NoError(t, err, "canonical single-case must pass; raw=%s", raw)
	require.True(t, parsed, "oracle must emit valid JSON; raw=%s", raw)
	require.True(t, result.Passed)
	if g, ok := result.Details["golden"].(string); ok {
		require.Equal(t, "1/1", g, "golden details must reflect single-case design")
	}
}

// F9: yaml.v3 strict mode must reject typo'd spec keys.
func TestSpecLoader_RejectsUnknownFields(t *testing.T) {
	tmp := t.TempDir()
	// Spec with a typo'd key (`succes_oracle` instead of `success_oracle`).
	bad := []byte(`id: foo
description: x
required_contexts:
  - role: driver
    platform: any
allowed_contexts: ["*"]
inputs:
  read_artifacts: [{kind: x, path: y}]
outputs:
  write_targets: [{kind: x, path: y}]
succes_oracle: ./oracle.sh
recovery_hint: x
timeout_seconds: 1
`)
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "spec.yaml"), bad, 0o644))

	raw, err := os.ReadFile(filepath.Join(tmp, "spec.yaml"))
	require.NoError(t, err)
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var s workloadSpec
	err = dec.Decode(&s)
	require.Error(t, err, "strict yaml decoder must reject unknown fields")
	require.Contains(t, err.Error(), "succes_oracle",
		"error must name the offending field so authors can find the typo")
}

// F7: scrubbedEnv smoke — an exported EXPECTED_MODEL_ALIAS in the developer
// shell must NOT be visible to the oracle.  We set it in this test's env
// and assert the oracle still uses the default and passes.
func TestOracleEnv_DoesNotLeakParentExports(t *testing.T) {
	t.Setenv("EXPECTED_MODEL_ALIAS", "would-bypass-if-leaked")
	result, raw, parsed, err := runOracle(t, "credential-bound-model", nil)
	require.NoError(t, err, "oracle must still use default alias; raw=%s", raw)
	require.True(t, parsed, "oracle must emit valid JSON; raw=%s", raw)
	require.True(t, result.Passed, "if parent env leaked, alias check would fail vs the mock route.json")
}

// ---------------------------------------------------------------------------
// Round-2 adversarial findings.  Each test captures a bug found during the
// second-pass review; they should fail against the round-1 state and pass
// after this commit's fix.
// ---------------------------------------------------------------------------

// R1: cross-device-code-mod's round-1 failure-marker regex includes
// `^[[:space:]]*[0-9]+ failed`, which matches `0 failed, 5 passed` —
// the standard pytest / jest / go-test summary line on a fully passing
// suite.  Such a log must NOT be classified as a failure.
func TestCrossDeviceCodeMod_AcceptsZeroFailedSummary(t *testing.T) {
	t.Run("pytest-style", func(t *testing.T) {
		result, raw, parsed, err := runOracle(t, "cross-device-code-mod", func(ws string) {
			require.NoError(t, os.WriteFile(filepath.Join(ws, "test.log"),
				[]byte("PASS\n===== 0 failed, 4 passed in 0.12s =====\n"), 0o644))
		})
		require.NoError(t, err, "oracle must accept '0 failed' as success; raw=%s", raw)
		require.True(t, parsed)
		require.True(t, result.Passed)
	})
	t.Run("bare-line", func(t *testing.T) {
		result, raw, parsed, err := runOracle(t, "cross-device-code-mod", func(ws string) {
			require.NoError(t, os.WriteFile(filepath.Join(ws, "test.log"),
				[]byte("PASS\nok  \texample.com/x\t0.001s\n0 failed, 5 passed\n"), 0o644))
		})
		require.NoError(t, err, "oracle must accept '0 failed' as success; raw=%s", raw)
		require.True(t, parsed)
		require.True(t, result.Passed)
	})
}

// R1 negative control: a real failure (≥1) must still be rejected so the
// fix doesn't become a wildcard pass.
func TestCrossDeviceCodeMod_RejectsNonzeroFailedSummary(t *testing.T) {
	result, raw, parsed, err := runOracle(t, "cross-device-code-mod", func(ws string) {
		require.NoError(t, os.WriteFile(filepath.Join(ws, "test.log"),
			[]byte("PASS\n===== 1 failed, 4 passed in 0.12s =====\n"), 0o644))
	})
	require.Error(t, err, "oracle must reject '1 failed' summary; raw=%s", raw)
	require.True(t, parsed, "oracle must emit valid JSON on failure; raw=%s", raw)
	require.False(t, result.Passed)
}

// R2: TestWorkloadDirectoryMatchesExpectedSet originally walked only
// directories, so a stray file at workloads/foo.txt (orphaned README,
// debug dump, rename leftover that ended up as a file) was invisible.
// This test exercises the tightened logic end-to-end: drop a sentinel
// file at workloads/, scan with the same rule, assert the entry is
// flagged as `extra`.  Run sequentially so we don't race the canonical
// set check (which would otherwise see the stray file too).
func TestWorkloadRoot_RejectsStrayFile(t *testing.T) {
	stray := filepath.Join("workloads", ".round2_stray_probe")
	require.NoError(t, os.WriteFile(stray, []byte("stray"), 0o644))
	t.Cleanup(func() { _ = os.Remove(stray) })

	entries, err := os.ReadDir("workloads")
	require.NoError(t, err)
	want := map[string]struct{}{}
	for _, id := range expectedWorkloads {
		want[id] = struct{}{}
	}
	var extras []string
	for _, e := range entries {
		if _, ok := want[e.Name()]; !ok {
			extras = append(extras, e.Name())
		}
	}
	require.Contains(t, extras, ".round2_stray_probe",
		"stray file at workloads/ root must be flagged; the directories-only check missed files entirely")
}

// R3: spec.inputs.read_artifacts paths are validated for shape but
// previously not for filesystem existence.  A typo'd path
// (`fixtures/datset/input.csv`) would pass spec validation and only
// surface at trial time when the workload itself fails to open the
// file.  Catch it at spec-load time.
func TestWorkloadSpecsReadArtifactsExistOnDisk(t *testing.T) {
	for _, id := range expectedWorkloads {
		id := id
		t.Run(id, func(t *testing.T) {
			dir := filepath.Join("workloads", id)
			spec := loadSpec(t, dir)
			for i, in := range spec.Inputs.ReadArtifacts {
				// Paths in spec.yaml are workload-dir-relative for
				// the in-tree fixtures; ${workspace}/... paths under
				// outputs are populated at trial time and don't apply.
				p := filepath.Join(dir, in.Path)
				_, err := os.Stat(p)
				require.NoError(t, err,
					"inputs.read_artifacts[%d].path %q must resolve to a real file or directory under %s",
					i, in.Path, dir)
			}
		})
	}
}

// R4: negative-coverage gap — windows-only-artifact must fail when the
// produced artifact does not match the declared sha256.  Round-1 added
// negative tests for cross-device / credential / parser, but not this
// one.  A tamper here is the exact attack the oracle exists to catch.
func TestWindowsOnlyArtifact_RejectsSha256Mismatch(t *testing.T) {
	result, raw, parsed, err := runOracle(t, "windows-only-artifact", func(ws string) {
		require.NoError(t, os.WriteFile(filepath.Join(ws, "artifact.bin"),
			[]byte("tampered-bytes\n"), 0o644))
	})
	require.Error(t, err, "oracle must reject sha256 mismatch; raw=%s", raw)
	require.True(t, parsed, "oracle must emit valid JSON on failure; raw=%s", raw)
	require.False(t, result.Passed)
	require.Contains(t, raw, "sha256",
		"failure detail must mention sha256 so the cause is debuggable")
}

// R4 (continued): negative coverage for remote-data-processing —
// tampering with result.json must trip the checksum and/or golden
// checks even though the in-workspace result.sha256 was matched.
func TestRemoteDataProcessing_RejectsGoldenMismatch(t *testing.T) {
	result, raw, parsed, err := runOracle(t, "remote-data-processing", func(ws string) {
		// Rewrite result.json with new content AND keep the original
		// in-workspace checksum stale.  Both the in-workspace checksum
		// tier AND the golden-pinned tier must catch the tamper; either
		// failure path is acceptable, but R3-N2 says we additionally
		// assert the golden-tier message so a future refactor that
		// silently disables it is caught.
		newContent := []byte(`{"count": 999, "sum": 999, "mean": 999.0}`)
		require.NoError(t, os.WriteFile(filepath.Join(ws, "result.json"), newContent, 0o644))
	})
	require.Error(t, err, "oracle must reject golden mismatch; raw=%s", raw)
	require.True(t, parsed, "oracle must emit valid JSON on failure; raw=%s", raw)
	require.False(t, result.Passed)
	// R3-N2: explicitly assert the golden-tier detail is the one tripped.
	require.Contains(t, raw, `"golden":"mismatch"`,
		"golden tier must remain wired in; a refactor that disables it would silently regress")
}

// R4 (continued): negative coverage for cross-device-code-mod when the
// patch is missing entirely (round 1 only covered header-only).
func TestCrossDeviceCodeMod_RejectsMissingPatch(t *testing.T) {
	result, raw, parsed, err := runOracle(t, "cross-device-code-mod", func(ws string) {
		require.NoError(t, os.Remove(filepath.Join(ws, "patch.diff")))
	})
	require.Error(t, err, "oracle must reject missing patch; raw=%s", raw)
	require.True(t, parsed, "oracle must emit valid JSON on failure; raw=%s", raw)
	require.False(t, result.Passed)
	require.Contains(t, raw, "patch")
}

// R4 (continued) / R3-S6: negative coverage for credential-bound-model —
// the flagship invariant is "no raw credential anywhere under workspace".
// Confirm EACH declared token shape (sk-, JWT eyJ., AWS AKIA, GitHub
// ghp_, Slack xox[baprs]-) independently trips the leak grep.  The
// round-2 test only exercised ghp_, leaving the four other shapes
// completely uncovered against the regex in oracle.sh.
func TestCredentialBoundModel_DetectsPlantedToken(t *testing.T) {
	cases := []struct {
		name  string
		token string
	}{
		{"openai-sk", "sk-abcdefghij0123456789ZZZZ"},
		// Header must be ≥23 chars to clear the `eyJ[A-Za-z0-9_-]{20,}`
		// requirement.  Using a longer-but-realistic encoded JWS header.
		{"jwt-eyJ", "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTYifQ.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"},
		{"aws-akia", "AKIAIOSFODNN7EXAMPLE"},
		{"github-ghp", "ghp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"slack-xoxb", "xoxb-1234567890-abcdefghij"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			result, raw, parsed, err := runOracle(t, "credential-bound-model", func(ws string) {
				f, ferr := os.OpenFile(filepath.Join(ws, "run.log"), os.O_APPEND|os.O_WRONLY, 0o644)
				require.NoError(t, ferr)
				_, werr := f.WriteString("debug: bearer " + c.token + "\n")
				require.NoError(t, werr)
				require.NoError(t, f.Close())
			})
			require.Error(t, err, "oracle must trip on a planted %s token; raw=%s", c.name, raw)
			require.True(t, parsed, "oracle must emit valid JSON on failure; raw=%s", raw)
			require.False(t, result.Passed)
			require.Contains(t, raw, "credential_leak",
				"failure detail must name credential_leak so reviewers see the cause")
		})
	}
}

// ---------------------------------------------------------------------------
// Round-3 adversarial findings.  Each test captures a bug found during the
// third-pass review; they should fail against the round-2 state and pass
// after this commit's fix.
// ---------------------------------------------------------------------------

// R3-M1: cross-device-code-mod's failure-marker regex previously matched
// only `--- FAIL`, `FAIL$`, `^=+ N failed`, `^N failed`, `--- ERROR`,
// `ERROR\b`.  Four common shapes from real test runners slipped through —
// each combined with a preceding PASS line in the same log silently
// produced {"passed":true,...} on the round-2 oracle.  Capture all four
// shapes in a single sub-test loop; one failing case is enough to keep
// the regression visible.
func TestCrossDeviceCodeMod_RejectsAdditionalFailureShapes(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		// Jest summary.  `Tests:       1 failed, 4 passed, 5 total`
		{"jest-summary", "Tests:       1 failed, 4 passed, 5 total"},
		// Mocha summary.  Two-space-indented "1 failing".
		{"mocha-failing", "  1 failing"},
		// GNU make.  `make: *** [target] Error 1`.
		{"make-error", "make: *** [target] Error 1"},
		// Python traceback ending in AssertionError.
		{"python-traceback", "Traceback (most recent call last):\n  File \"x.py\", line 1, in <module>\n    assert False\nAssertionError"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			// Prepend a PASS line so the round-2 oracle would hit the
			// "pass marker found" branch unless the failure branch
			// fires first.  Without the prepended PASS, the test would
			// instead pass via "no pass marker" — that's a different
			// code path and would not expose the R3-M1 bug.
			result, raw, parsed, err := runOracle(t, "cross-device-code-mod", func(ws string) {
				body := "PASS\n" + c.body + "\n"
				require.NoError(t, os.WriteFile(filepath.Join(ws, "test.log"),
					[]byte(body), 0o644))
			})
			require.Error(t, err, "oracle must reject %s; raw=%s", c.name, raw)
			require.True(t, parsed, "oracle must emit valid JSON on failure; raw=%s", raw)
			require.False(t, result.Passed, "must not classify a %s log as passing", c.name)
		})
	}
}

// R3-S1: failure regex must reject zero-padded counts like `01 failed`
// (treat as ≥1) without losing the existing `0 failed` accept (treat as 0).
// Generalising `[1-9][0-9]*` to `0*[1-9][0-9]*` covers both ends.
func TestCrossDeviceCodeMod_HandlesZeroPaddedFailedCount(t *testing.T) {
	t.Run("rejects-01-failed", func(t *testing.T) {
		result, raw, parsed, err := runOracle(t, "cross-device-code-mod", func(ws string) {
			require.NoError(t, os.WriteFile(filepath.Join(ws, "test.log"),
				[]byte("PASS\n===== 01 failed, 4 passed =====\n"), 0o644))
		})
		require.Error(t, err, "oracle must reject '01 failed'; raw=%s", raw)
		require.True(t, parsed, "oracle must emit valid JSON on failure; raw=%s", raw)
		require.False(t, result.Passed)
	})
	t.Run("accepts-00-failed", func(t *testing.T) {
		result, raw, parsed, err := runOracle(t, "cross-device-code-mod", func(ws string) {
			require.NoError(t, os.WriteFile(filepath.Join(ws, "test.log"),
				[]byte("PASS\n===== 00 failed, 4 passed =====\n"), 0o644))
		})
		require.NoError(t, err, "oracle must accept '00 failed' as zero; raw=%s", raw)
		require.True(t, parsed)
		require.True(t, result.Passed)
	})
}

// R3-M2: windows-only-artifact's round-2 oracle hard-coded `stat -c %s`,
// which is GNU-only.  On macOS/BSD (and any shim that mimics BSD-stat
// semantics by rejecting `-c`), stat prints an error to stderr — which
// combined output picks up as line 1, breaking the JSON parse — and
// `size=""` triggers `printf: : invalid number`.  Install a BSD-style
// shim earlier in PATH and assert the post-fix behaviour: exit 0,
// well-formed JSON, passed:true, artifact_bytes > 0 (proving the wc
// fallback produced a real number, not 0).
func TestWindowsOnlyArtifact_StatFallback(t *testing.T) {
	shimDir := t.TempDir()
	shim := filepath.Join(shimDir, "stat")
	shimBody := `#!/usr/bin/env bash
# BSD-style stat shim: rejects -c flag, exits non-zero with an error on stderr.
if [[ "$1" == "-c" ]]; then
  echo "stat: illegal option -- c" >&2
  exit 1
fi
exec /usr/bin/stat "$@"
`
	require.NoError(t, os.WriteFile(shim, []byte(shimBody), 0o755))

	// Build a PATH that starts with the shim directory so the oracle
	// resolves `stat` to our shim, then falls back to the system PATH
	// (oracle still needs sha256sum / awk / grep / wc from /usr/bin).
	dir := filepath.Join("workloads", "windows-only-artifact")
	spec := loadSpec(t, dir)
	oraclePath, err := filepath.Abs(filepath.Join(dir, spec.SuccessOracle))
	require.NoError(t, err)
	srcMock, err := filepath.Abs(filepath.Join(dir, "fixtures", "mock_workspace"))
	require.NoError(t, err)

	workspace := t.TempDir()
	copyDir(t, srcMock, workspace)

	// R8-M3: deadline-bound execution; same chokepoint as runOracle.
	cmd, cancel := runOracleCmd(t, oraclePath, workspace)
	defer cancel()
	cmd.Env = []string{
		"PATH=" + shimDir + ":/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"LC_ALL=C",
		"LANG=C",
		"HOME=" + t.TempDir(),
	}
	out, runErr := cmd.CombinedOutput()
	raw := string(out)

	// (a) exit 0
	require.NoError(t, runErr, "oracle must succeed even when stat -c is rejected; raw=%s", raw)

	// (b) first non-empty line must be valid JSON (no stat error spill).
	var line string
	for _, l := range strings.Split(raw, "\n") {
		if s := strings.TrimSpace(l); s != "" {
			line = s
			break
		}
	}
	require.NotEmpty(t, line, "oracle produced no output")
	var result oracleResult
	require.NoError(t, json.Unmarshal([]byte(line), &result),
		"first non-empty line must be JSON; got %q (raw=%s)", line, raw)

	// (c) passed:true.
	require.True(t, result.Passed, "result must report passed:true; raw=%s", raw)

	// (d) artifact_bytes must be > 0 — proves the wc fallback produced a
	// real number, not 0.  An int round-trips through encoding/json as
	// float64 by default.
	bytesVal, ok := result.Metrics["artifact_bytes"].(float64)
	require.True(t, ok, "metrics.artifact_bytes must be a number; got %#v", result.Metrics["artifact_bytes"])
	require.Greater(t, bytesVal, float64(0),
		"artifact_bytes must be > 0; wc fallback failed or produced 0; raw=%s", raw)
}

// R4-S1: remote-data-processing/oracle.sh used `stat -c %s ... || echo 0`
// with no wc fallback (unlike windows-only-artifact post-R3-M2).  Under a
// BSD-style stat that rejects -c, metrics.result_bytes silently reported 0
// for a real non-empty file — both a metric corruption and a symmetry
// violation with the sibling oracle.  This test installs the same BSD-style
// stat shim used by TestWindowsOnlyArtifact_StatFallback and asserts:
// (a) exit 0, (b) first non-empty line is valid JSON, (c) passed:true,
// (d) metrics.result_bytes equals the actual on-disk size of the canonical
// mock result.json (read via os.Stat so the assertion has no magic number
// and tracks fixture changes automatically).
func TestRemoteDataProcessing_StatFallback(t *testing.T) {
	shimDir := t.TempDir()
	shim := filepath.Join(shimDir, "stat")
	shimBody := `#!/usr/bin/env bash
# BSD-style stat shim: rejects -c flag, exits non-zero with an error on stderr.
if [[ "$1" == "-c" ]]; then
  echo "stat: illegal option -- c" >&2
  exit 1
fi
exec /usr/bin/stat "$@"
`
	require.NoError(t, os.WriteFile(shim, []byte(shimBody), 0o755))

	dir := filepath.Join("workloads", "remote-data-processing")
	spec := loadSpec(t, dir)
	oraclePath, err := filepath.Abs(filepath.Join(dir, spec.SuccessOracle))
	require.NoError(t, err)
	srcMock, err := filepath.Abs(filepath.Join(dir, "fixtures", "mock_workspace"))
	require.NoError(t, err)

	// Capture the canonical result.json size from the in-tree fixture so the
	// expected byte count tracks any future change to the mock.
	canonResult := filepath.Join(srcMock, "result.json")
	canonInfo, err := os.Stat(canonResult)
	require.NoError(t, err, "canonical mock result.json must exist")
	wantBytes := canonInfo.Size()
	require.Greater(t, wantBytes, int64(0), "canonical mock result.json must be non-empty")

	workspace := t.TempDir()
	copyDir(t, srcMock, workspace)

	// R8-M3: deadline-bound execution; same chokepoint as runOracle.
	cmd, cancel := runOracleCmd(t, oraclePath, workspace)
	defer cancel()
	cmd.Env = []string{
		"PATH=" + shimDir + ":/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"LC_ALL=C",
		"LANG=C",
		"HOME=" + t.TempDir(),
	}
	out, runErr := cmd.CombinedOutput()
	raw := string(out)

	// (a) exit 0.
	require.NoError(t, runErr, "oracle must succeed even when stat -c is rejected; raw=%s", raw)

	// (b) first non-empty line must be valid JSON (no stat error spill).
	var line string
	for _, l := range strings.Split(raw, "\n") {
		if s := strings.TrimSpace(l); s != "" {
			line = s
			break
		}
	}
	require.NotEmpty(t, line, "oracle produced no output")
	var result oracleResult
	require.NoError(t, json.Unmarshal([]byte(line), &result),
		"first non-empty line must be JSON; got %q (raw=%s)", line, raw)

	// (c) passed:true.
	require.True(t, result.Passed, "result must report passed:true; raw=%s", raw)

	// (d) result_bytes must equal the canonical on-disk size; >0 is not
	// enough because the pre-R4-S1 oracle would emit 0 here under BSD-stat
	// and a plain Greater(..., 0) check would fail loudly, but a future
	// regression that emits e.g. 1 (a stray nibble of accidental output)
	// would slip through.  Encoding/json decodes ints into float64 by default.
	bytesVal, ok := result.Metrics["result_bytes"].(float64)
	require.True(t, ok, "metrics.result_bytes must be a number; got %#v", result.Metrics["result_bytes"])
	require.Equal(t, float64(wantBytes), bytesVal,
		"result_bytes must equal canonical fixture size (%d); wc fallback failed or produced wrong number; raw=%s",
		wantBytes, raw)
}

// R3-S4 (extension): TestOracleEnv_DoesNotLeakParentExports proved env
// vars don't leak; this proves PATH overrides don't either.  Place a
// shimmed sha256sum earlier in the parent process's PATH that writes a
// marker file; assert the marker is NOT created when the oracle runs.
// If scrubbedEnv passed PATH through, the oracle would invoke our shim
// and the marker would exist.
func TestOracleEnv_DoesNotLeakParentPATH(t *testing.T) {
	shimDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "shim_was_invoked")
	shim := filepath.Join(shimDir, "sha256sum")
	shimBody := "#!/usr/bin/env bash\ntouch " + marker + "\nexec /usr/bin/sha256sum \"$@\"\n"
	require.NoError(t, os.WriteFile(shim, []byte(shimBody), 0o755))

	// Prepend the shim dir to the parent's PATH.  scrubbedEnv must NOT
	// propagate this to the oracle subprocess.
	t.Setenv("PATH", shimDir+":"+os.Getenv("PATH"))

	// Run a workload whose oracle calls sha256sum — windows-only-artifact does.
	result, raw, parsed, err := runOracle(t, "windows-only-artifact", nil)
	require.NoError(t, err, "oracle must still pass on the mock fixture; raw=%s", raw)
	require.True(t, parsed)
	require.True(t, result.Passed)

	_, statErr := os.Stat(marker)
	require.True(t, os.IsNotExist(statErr),
		"PATH shim marker must NOT exist; parent PATH leaked into the oracle (statErr=%v)", statErr)
}

// R3-S5 (a): spec validator must reject timeout_seconds <= 0 or > 86400.
func TestSpecLoader_RejectsOutOfBoundsTimeout(t *testing.T) {
	base := `id: foo
description: x
required_contexts:
  - role: driver
    platform: any
allowed_contexts: ["*"]
inputs:
  read_artifacts: [{kind: x, path: y}]
outputs:
  write_targets: [{kind: x, path: y}]
success_oracle: ./oracle.sh
recovery_hint: x
timeout_seconds: %d
`
	cases := []struct {
		name    string
		seconds int
		wantOK  bool
	}{
		{"zero", 0, false},
		{"negative", -1, false},
		{"one", 1, true},
		{"max", 86400, true},
		{"over-max", 86401, false},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			raw := []byte(fmt.Sprintf(base, c.seconds))
			dec := yaml.NewDecoder(bytes.NewReader(raw))
			dec.KnownFields(true)
			var s workloadSpec
			require.NoError(t, dec.Decode(&s), "yaml must parse")
			err := validateTimeoutSeconds(s.TimeoutSeconds)
			if c.wantOK {
				require.NoError(t, err, "timeout %d must be accepted", c.seconds)
			} else {
				require.Error(t, err, "timeout %d must be rejected", c.seconds)
			}
		})
	}
}

// R3-S5 (b): required_contexts.platform must be one of the canonical set.
func TestSpecLoader_RejectsUnknownPlatform(t *testing.T) {
	good := []string{"linux", "darwin", "windows", "any"}
	for _, p := range good {
		require.NoError(t, validatePlatform(p), "platform %q must be accepted", p)
	}
	for _, p := range []string{"", "Linux", "linux ", "freebsd", "macos"} {
		require.Error(t, validatePlatform(p), "platform %q must be rejected", p)
	}
}

// R3-S5 (b, end-to-end): every shipped spec's required_contexts.platform
// must already be in the canonical set.  Catches drift if someone adds a
// new spec with a typo'd platform.
func TestSpecLoader_AllShippedSpecsHaveValidPlatforms(t *testing.T) {
	for _, id := range expectedWorkloads {
		id := id
		t.Run(id, func(t *testing.T) {
			spec := loadSpec(t, filepath.Join("workloads", id))
			for i, ctx := range spec.RequiredContexts {
				require.NoError(t, validatePlatform(ctx.Platform),
					"required_contexts[%d].platform %q in %s spec must be in {linux,darwin,windows,any}",
					i, ctx.Platform, id)
			}
		})
	}
}

// R3-S5 (c): allowed_contexts must be ["*"] OR a non-empty subset of the
// declared required_contexts.role values.  Empty list, or a role not
// declared, must be rejected.
func TestSpecLoader_ValidatesAllowedContexts(t *testing.T) {
	roles := []string{"driver", "slave", "model_gateway"}
	t.Run("wildcard-ok", func(t *testing.T) {
		require.NoError(t, validateAllowedContexts([]string{"*"}, roles))
	})
	t.Run("subset-ok", func(t *testing.T) {
		require.NoError(t, validateAllowedContexts([]string{"driver", "slave"}, roles))
	})
	t.Run("single-known-ok", func(t *testing.T) {
		require.NoError(t, validateAllowedContexts([]string{"slave"}, roles))
	})
	t.Run("empty-rejected", func(t *testing.T) {
		require.Error(t, validateAllowedContexts(nil, roles))
		require.Error(t, validateAllowedContexts([]string{}, roles))
	})
	t.Run("unknown-role-rejected", func(t *testing.T) {
		err := validateAllowedContexts([]string{"driver", "ghost"}, roles)
		require.Error(t, err)
		require.Contains(t, err.Error(), "ghost")
	})
	t.Run("wildcard-must-be-alone", func(t *testing.T) {
		require.Error(t, validateAllowedContexts([]string{"*", "driver"}, roles),
			"[\"*\", ...] is ambiguous; must be exactly [\"*\"] or a subset")
	})
}

// ---------------------------------------------------------------------------
// Round-5 adversarial findings.  Each test captures a bug found during the
// fifth-pass review; they should fail against the round-4 state and pass
// after this commit's fix.
// ---------------------------------------------------------------------------

// R5-M1: missing-parser-converter/oracle.sh counted PASS / FAIL lines but
// did not reject *other* lines.  A log like:
//
//	PASS expected.out
//	  1 failing
//	  AssertionError: pipe parse drift
//
// was silently accepted because pass_lines >= cases_total and fail_lines == 0.
// The README has always promised "One PASS/FAIL line per golden case";
// the tightened oracle now rejects any line that does not begin with PASS
// or FAIL.  These tests exercise three failure shapes (mocha summary,
// jest summary, python traceback) plus a positive control.

func TestMissingParserConverter_RejectsMochaFailureLineAmongPasses(t *testing.T) {
	result, raw, parsed, err := runOracle(t, "missing-parser-converter", func(ws string) {
		require.NoError(t, os.WriteFile(filepath.Join(ws, "acceptance.log"),
			[]byte("PASS expected.out\n  1 failing\n"), 0o644))
	})
	require.Error(t, err, "oracle must reject a mocha 'N failing' summary among passes; raw=%s", raw)
	require.True(t, parsed, "oracle must still emit valid JSON on failure; raw=%s", raw)
	require.False(t, result.Passed)
	if v, ok := result.Details["acceptance_log"].(string); ok {
		require.Contains(t, v, "non-canonical",
			"acceptance_log detail must name the contract violation; got %q", v)
	}
}

func TestMissingParserConverter_RejectsJestSummary(t *testing.T) {
	result, raw, parsed, err := runOracle(t, "missing-parser-converter", func(ws string) {
		require.NoError(t, os.WriteFile(filepath.Join(ws, "acceptance.log"),
			[]byte("PASS expected.out\nTests: 1 failed, 4 passed, 5 total\n"), 0o644))
	})
	require.Error(t, err, "oracle must reject a jest summary line; raw=%s", raw)
	require.True(t, parsed, "oracle must still emit valid JSON on failure; raw=%s", raw)
	require.False(t, result.Passed)
	if v, ok := result.Details["acceptance_log"].(string); ok {
		require.Contains(t, v, "non-canonical",
			"acceptance_log detail must name the contract violation; got %q", v)
	}
}

func TestMissingParserConverter_RejectsTraceback(t *testing.T) {
	result, raw, parsed, err := runOracle(t, "missing-parser-converter", func(ws string) {
		require.NoError(t, os.WriteFile(filepath.Join(ws, "acceptance.log"),
			[]byte("PASS expected.out\nTraceback (most recent call last):\n  File a.py, line 1\nAssertionError\n"), 0o644))
	})
	require.Error(t, err, "oracle must reject a python traceback; raw=%s", raw)
	require.True(t, parsed, "oracle must still emit valid JSON on failure; raw=%s", raw)
	require.False(t, result.Passed)
	if v, ok := result.Details["acceptance_log"].(string); ok {
		require.Contains(t, v, "non-canonical",
			"acceptance_log detail must name the contract violation; got %q", v)
	}
}

// Positive control: a single canonical PASS line must still satisfy the
// oracle after the tightening (covers the mock_workspace shape).
func TestMissingParserConverter_AcceptsCanonicalSingleCase(t *testing.T) {
	result, raw, parsed, err := runOracle(t, "missing-parser-converter", func(ws string) {
		require.NoError(t, os.WriteFile(filepath.Join(ws, "acceptance.log"),
			[]byte("PASS expected.out\n"), 0o644))
	})
	require.NoError(t, err, "oracle must accept a single canonical PASS line; raw=%s", raw)
	require.True(t, parsed, "oracle must emit valid JSON; raw=%s", raw)
	require.True(t, result.Passed)
}

// R5-S1: a future swap of `wc -c < file` to something that emits a
// non-integer (`1.5`, `1e3`, `1 2`) would have made `size=$(( $size + 0 ))`
// raise a bash arithmetic error to stderr, and CombinedOutput's first
// non-empty line would have been that error instead of the JSON verdict.
// The fix sanitizes via a digits-only regex with `size=0` fallback.  Test
// both oracles with a `wc`+`stat` shim that returns "1.5\n".
func TestWindowsOnlyArtifact_HandlesNonIntegerByteCount(t *testing.T) {
	assertNonIntegerByteCountSanitized(t, "windows-only-artifact", "artifact_bytes")
}

func TestRemoteDataProcessing_HandlesNonIntegerByteCount(t *testing.T) {
	assertNonIntegerByteCountSanitized(t, "remote-data-processing", "result_bytes")
}

// assertNonIntegerByteCountSanitized installs both a `stat` shim (rejects
// -c) and a `wc` shim (emits "1.5\n") earlier in PATH, runs the oracle,
// and verifies the oracle (a) exits 0, (b) emits valid JSON as the first
// non-empty stdout line, (c) reports passed:true, (d) reports the byte
// metric as 0 (sanitization fallback), and (e) never produces a bash
// "arithmetic syntax error" substring anywhere in combined output.
func assertNonIntegerByteCountSanitized(t *testing.T, workloadID, byteMetric string) {
	t.Helper()
	shimDir := t.TempDir()
	statShim := filepath.Join(shimDir, "stat")
	require.NoError(t, os.WriteFile(statShim, []byte(`#!/usr/bin/env bash
# BSD-style stat shim: rejects -c so the oracle's wc fallback engages.
if [[ "$1" == "-c" ]]; then
  echo "stat: illegal option -- c" >&2
  exit 1
fi
exec /usr/bin/stat "$@"
`), 0o755))
	wcShim := filepath.Join(shimDir, "wc")
	require.NoError(t, os.WriteFile(wcShim, []byte(`#!/usr/bin/env bash
# Emit a non-integer to force the sanitizer's fallback path.
echo "1.5"
`), 0o755))

	dir := filepath.Join("workloads", workloadID)
	spec := loadSpec(t, dir)
	oraclePath, err := filepath.Abs(filepath.Join(dir, spec.SuccessOracle))
	require.NoError(t, err)
	srcMock, err := filepath.Abs(filepath.Join(dir, "fixtures", "mock_workspace"))
	require.NoError(t, err)

	workspace := t.TempDir()
	copyDir(t, srcMock, workspace)

	// R8-M3: deadline-bound execution; same chokepoint as runOracle.
	cmd, cancel := runOracleCmd(t, oraclePath, workspace)
	defer cancel()
	cmd.Env = []string{
		"PATH=" + shimDir + ":/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"LC_ALL=C",
		"LANG=C",
		"HOME=" + t.TempDir(),
	}
	out, runErr := cmd.CombinedOutput()
	raw := string(out)

	// (a) exit 0
	require.NoError(t, runErr, "oracle must succeed with a misbehaving wc shim; raw=%s", raw)

	// (e) no arithmetic error must spill into combined output.
	require.NotContains(t, raw, "arithmetic syntax error",
		"bash arithmetic must not run on uncontrolled input; raw=%s", raw)
	require.NotContains(t, raw, "printf: 1.5: invalid number",
		"printf must not be handed a non-integer; raw=%s", raw)

	// (b) first non-empty stdout line is valid JSON.
	var line string
	for _, l := range strings.Split(raw, "\n") {
		if s := strings.TrimSpace(l); s != "" {
			line = s
			break
		}
	}
	require.NotEmpty(t, line, "oracle produced no output")
	var result oracleResult
	require.NoError(t, json.Unmarshal([]byte(line), &result),
		"first non-empty line must be JSON; got %q (raw=%s)", line, raw)

	// (c) passed:true.
	require.True(t, result.Passed, "result must report passed:true; raw=%s", raw)

	// (d) byte metric is 0 — sanitization fallback.
	v, ok := result.Metrics[byteMetric].(float64)
	require.True(t, ok, "metrics.%s must be a number; got %#v (raw=%s)",
		byteMetric, result.Metrics[byteMetric], raw)
	require.Equal(t, float64(0), v,
		"metrics.%s must be 0 under the sanitization fallback; raw=%s", byteMetric, raw)
}

// R5-N1: validateAllowedContexts must reject duplicates like
// ["driver", "driver"], which were previously accepted.
func TestSpecValidator_RejectsDuplicateAllowedContexts(t *testing.T) {
	roles := []string{"driver", "slave"}
	err := validateAllowedContexts([]string{"driver", "driver"}, roles)
	require.Error(t, err, "duplicate role in allowed_contexts must be rejected")
	require.Contains(t, err.Error(), "duplicate",
		"error message must name the contract violation; got %v", err)
}

// ---------------------------------------------------------------------------
// Round-6 adversarial findings.  Each test captures a bug found during the
// sixth-pass review; they should fail against the round-5 state and pass
// after this commit's fix.
// ---------------------------------------------------------------------------

// R6-M1: credential-bound-model/oracle.sh validated EXPECTED_MODEL_ALIAS
// against ^[A-Za-z0-9._:-]+$ then interpolated it into a grep -qE regex.
// A literal `.` in the alias passes the charset gate but then acts as a
// regex wildcard, so an alias like `acme.bound-model-v1` silently matched
// a route.json storing `acmeXbound-model-v1` and reported passed:true.
// The R1 RejectsRegexInjectionAlias test probed `.*` only — which the
// charset gate rejects (the `*` is outside the allowed set) — so the
// actual `.` injection vector was never exercised.  See triage R6-M1.
func TestCredentialBoundModel_LiteralDotInAliasDoesNotWildcard(t *testing.T) {
	result, raw, parsed, err := runOracle(t, "credential-bound-model", func(ws string) {
		// Mutate route.json so model_alias stores `acmeXbound-model-v1`.
		// With EXPECTED_MODEL_ALIAS=`acme.bound-model-v1`, the `.` MUST
		// match only a literal dot, not any char.  Pre-fix the oracle
		// silently accepted this as a match.
		data, rerr := os.ReadFile(filepath.Join(ws, "route.json"))
		require.NoError(t, rerr)
		swapped := bytes.Replace(data, []byte("acme-bound-model-v1"), []byte("acmeXbound-model-v1"), 1)
		require.NoError(t, os.WriteFile(filepath.Join(ws, "route.json"), swapped, 0o644))
	}, "EXPECTED_MODEL_ALIAS=acme.bound-model-v1")
	require.Error(t, err, "oracle must reject — `.` in alias must not match `X`; raw=%s", raw)
	require.True(t, parsed, "oracle must still emit valid JSON on failure; raw=%s", raw)
	require.False(t, result.Passed, "literal `.` in alias acted as regex wildcard (R6-M1); raw=%s", raw)
}

// R6-M2: cross-device-code-mod's round-3 regex widened to catch jest
// `N failed` and mocha `N failing`, but the new branches
// `[[:space:]]*0*[1-9][0-9]*[[:space:]]+failed` and
// `[[:space:]]*0*[1-9][0-9]*[[:space:]]+failing` are too greedy — they
// match natural prose like `3 failed allocations recovered, all passing
// now` or `5 failing` mid-sentence.  Tighten each alternative to require
// either a known runner-keyword prefix or a canonical summary banner
// shape.  See triage R6-M2.
func TestCrossDeviceCodeMod_RejectsProseFalsePositives(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		// Mid-sentence "N failed" prose with PASS already present.  The
		// pre-fix oracle wrongly classified this as a failure.
		{"failed-recovered-prose", "PASS\n  3 failed allocations recovered, all passing now\n"},
		// Tab-indented "1 failed retry succeeded" prose.
		{"failed-retry-prose", "PASS\n\t1 failed retry succeeded on the third try\n"},
		// "5 failing" mid-prose with surrounding words.  Mocha emits
		// bare `  N failing` as the whole indented line; this is the
		// non-canonical prose form that the prior regex over-matched.
		{"failing-prose", "PASS\nWe noted 5 failing services prior to the fix\n"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			result, raw, parsed, err := runOracle(t, "cross-device-code-mod", func(ws string) {
				require.NoError(t, os.WriteFile(filepath.Join(ws, "test.log"),
					[]byte(c.body), 0o644))
			})
			require.NoError(t, err, "oracle must accept prose %q as passing; raw=%s", c.name, raw)
			require.True(t, parsed, "oracle must emit valid JSON; raw=%s", raw)
			require.True(t, result.Passed, "prose with mid-sentence 'N failed/failing' must NOT trip the failure regex (R6-M2); raw=%s", raw)
		})
	}
}

// R7-S2: TestStrayProbeSweepIsNarrow as originally written was structurally
// vacuous — init() runs once at package load BEFORE any test body, so a
// test that creates a stray file AFTER init has no way to observe what
// init would have done.  Refactor the sweep into a callable function
// (sweepStrayProbes, defined alongside init()) so the test can actually
// invoke it and verify narrowness directly.  Procedure:
//  1. Create a non-canonical probe file whose name matches the OLD over-broad
//     glob `.round*_stray_probe*` but is NOT in the canonical literal list.
//  2. Call sweepStrayProbes() directly.
//  3. Assert the created file STILL EXISTS — proving the sweep is narrow.
//  4. Cleanup at end of test (t.Cleanup).
func TestStrayProbeSweepIsNarrow(t *testing.T) {
	overGlob := filepath.Join("workloads", ".round999_unrelated_stray_probe_zzz")
	require.NoError(t, os.WriteFile(overGlob, []byte("scratch"), 0o644))
	t.Cleanup(func() { _ = os.Remove(overGlob) })

	// Drive the sweep here in the test body (not at init time) so we
	// observe its behaviour on a file that exists right now.  If the
	// sweep ever regresses to a broad glob, this assertion fires.
	sweepStrayProbes()

	_, err := os.Stat(overGlob)
	require.NoError(t, err, "narrow sweep must NOT remove %s (the old glob would have); err=%v", overGlob, err)
}

// R6-S1: remote-data-processing/oracle.sh's golden block emitted
// `"golden":"mismatch"` even when result.json was missing entirely (the
// `actual` variable stayed empty so the equality check fell through to
// the mismatch branch).  That made TestRemoteDataProcessing_RejectsGoldenMismatch
// non-discriminating — a regression that drops result.json would still
// satisfy a substring-assertion for "golden mismatch".  Gate the golden
// block on having a real actual value.  See triage R6-S1.
func TestRemoteDataProcessing_MissingResultDoesNotFalseGoldenMismatch(t *testing.T) {
	result, raw, parsed, err := runOracle(t, "remote-data-processing", func(ws string) {
		require.NoError(t, os.Remove(filepath.Join(ws, "result.json")))
	})
	require.Error(t, err, "missing result.json must still fail the oracle; raw=%s", raw)
	require.True(t, parsed, "oracle must emit valid JSON; raw=%s", raw)
	require.False(t, result.Passed)
	// The golden axis must NOT claim mismatch when there is no actual to
	// compare against.  `result:missing` already covers the absence.
	require.NotContains(t, raw, `"golden":"mismatch"`,
		"golden tier must not double-signal when result.json is absent (R6-S1); raw=%s", raw)
	require.Contains(t, raw, `"result":"missing"`,
		"result-missing axis must still fire; raw=%s", raw)
}

// R6-M1 positive control: when route.json's model_alias is literally
// `acme.bound-model-v1`, the oracle MUST accept it.  This guards against
// an over-zealous fix that escapes the dot but then never matches.
func TestCredentialBoundModel_DotsInAliasMatchLiterally(t *testing.T) {
	result, raw, parsed, err := runOracle(t, "credential-bound-model", func(ws string) {
		data, rerr := os.ReadFile(filepath.Join(ws, "route.json"))
		require.NoError(t, rerr)
		swapped := bytes.Replace(data, []byte("acme-bound-model-v1"), []byte("acme.bound-model-v1"), 1)
		require.NoError(t, os.WriteFile(filepath.Join(ws, "route.json"), swapped, 0o644))
	}, "EXPECTED_MODEL_ALIAS=acme.bound-model-v1")
	require.NoError(t, err, "oracle must accept literal-dot match; raw=%s", raw)
	require.True(t, parsed, "oracle must emit valid JSON; raw=%s", raw)
	require.True(t, result.Passed, "literal `.` alias must match literal `.` in route.json; raw=%s", raw)
}

// R3-S5 (c, end-to-end): every shipped spec must satisfy the
// allowed_contexts validator.
func TestSpecLoader_AllShippedSpecsHaveValidAllowedContexts(t *testing.T) {
	for _, id := range expectedWorkloads {
		id := id
		t.Run(id, func(t *testing.T) {
			spec := loadSpec(t, filepath.Join("workloads", id))
			roles := make([]string, 0, len(spec.RequiredContexts))
			for _, c := range spec.RequiredContexts {
				roles = append(roles, c.Role)
			}
			require.NoError(t, validateAllowedContexts(spec.AllowedContexts, roles))
		})
	}
}

// ---------------------------------------------------------------------------
// Round-7 adversarial findings.  Each test captures a bug found during the
// seventh-pass review; they should fail against the round-6 state and pass
// after this commit's fix.
// ---------------------------------------------------------------------------

// R7-M1: credential-bound-model/oracle.sh's proxy_context_id presence check
// used the regex `"proxy_context_id"[[:space:]]*:[[:space:]]*"` which is
// satisfied by the opening quote alone — so a route.json containing
// `"proxy_context_id": ""` (empty value) was silently accepted and the
// oracle reported route_proxy_id:"present".  Spec requires an opaque,
// non-empty identifier; tighten the regex to require ≥1 non-quote char
// between the surrounding quotes.
func TestCredentialBoundModel_RejectsEmptyProxyContextId(t *testing.T) {
	result, raw, parsed, err := runOracle(t, "credential-bound-model", func(ws string) {
		// Rewrite route.json so proxy_context_id is the empty string.
		body := []byte(`{
  "model_alias": "acme-bound-model-v1",
  "proxy_context_id": "",
  "hops": ["driver", "model_proxy"]
}
`)
		require.NoError(t, os.WriteFile(filepath.Join(ws, "route.json"), body, 0o644))
	})
	require.Error(t, err, "oracle must reject empty proxy_context_id; raw=%s", raw)
	require.True(t, parsed, "oracle must emit valid JSON on failure; raw=%s", raw)
	require.False(t, result.Passed, "empty proxy_context_id silently satisfied the presence check (R7-M1); raw=%s", raw)
	if v, ok := result.Details["route_proxy_id"].(string); ok {
		require.Contains(t, v, "missing", "route_proxy_id detail must name the contract violation; got %q", v)
	} else {
		t.Fatalf("details.route_proxy_id missing; raw=%s", raw)
	}
}

// R7-M2: round-6 R6-M2 narrowed `ERROR\b` to `ERROR$` to kill prose
// false-positives, but the net loss was real failure shapes that no
// longer rejected: `ERROR: ...`, `ERROR\t...`, bare `FAILED`, bare
// `FAILURE`, and pytest's one-line summary `1 failed in 0.50s` (no
// `=====` banner).  Broaden the regex with explicit anchored arms for
// each shape AND retain R6-M2's prose-rejection arms.
func TestCrossDeviceCodeMod_RejectsCommonErrorAndFailedShapes(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		// ERROR: prefix — typical "ERROR: connection refused" runner line.
		{"error-colon", "ERROR: connection refused"},
		// ERROR<tab> prefix — go test verbose "ERROR\tTestFoo failed".
		{"error-tab", "ERROR\tTestFoo failed"},
		// Bare FAILED marker — common in CI step output.
		{"bare-failed", "FAILED"},
		// Bare FAILURE marker — junit-style.
		{"bare-failure", "FAILURE"},
		// Pytest one-line summary without the ===== banner.
		{"pytest-one-line", "1 failed in 0.50s"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			// Prepend PASS so the round-6 oracle would fall through to
			// "pass marker found" unless the failure branch fires first.
			result, raw, parsed, err := runOracle(t, "cross-device-code-mod", func(ws string) {
				body := "PASS\n" + c.body + "\n"
				require.NoError(t, os.WriteFile(filepath.Join(ws, "test.log"),
					[]byte(body), 0o644))
			})
			require.Error(t, err, "oracle must reject %s; raw=%s", c.name, raw)
			require.True(t, parsed, "oracle must emit valid JSON on failure; raw=%s", raw)
			require.False(t, result.Passed, "must not classify a %s log as passing (R7-M2); raw=%s", c.name, raw)
		})
	}
}

// R7-M2 regression guard: every prose-direction case from R6-M2 that
// the broadened regex must NOT trip.  Critically:
//   - `^ERROR[:[:space:]]` is line-anchored, so prose `no ERROR here` won't
//     trip (no start-of-line `ERROR`).
//   - `^FAILED$` and `^FAILURE$` require EOL, so `FAILED cleanup` /
//     `previous tests SUCCESSFULLY failed cleanup` won't trip.
//   - The pytest one-line summary arm requires `failed in ` so prose
//     `3 failed allocations recovered` won't trip.
//
// Together with the R6-M2 prose tests already in this file, this provides
// belt-and-braces coverage for prose vs failure-shape disambiguation.
func TestCrossDeviceCodeMod_R7M2ProseRegressionGuard(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"recovered-prose", "PASS\n  3 failed allocations recovered, all passing now\n"},
		{"retry-prose", "PASS\n\t1 failed retry succeeded on the third try\n"},
		{"observed-prose", "PASS\n 5 failing patterns observed but recovered\n"},
		{"zero-pad-recovered-prose", "PASS\n01 failed allocations cleaned up\n"},
		{"no-errors-here", "PASS\nno errors here\n"},
		{"prose-with-failed-word", "PASS\nprevious tests SUCCESSFULLY failed cleanup\n"},
		// Specifically guard: ERROR mid-sentence must not trip.
		{"error-mid-sentence", "PASS\nthere were no ERROR messages encountered\n"},
		// `FAILED` followed by other text on same line — `^FAILED$` requires EOL.
		{"failed-with-trailing", "PASS\nFAILED cleanup but recovered fine\n"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			result, raw, parsed, err := runOracle(t, "cross-device-code-mod", func(ws string) {
				require.NoError(t, os.WriteFile(filepath.Join(ws, "test.log"),
					[]byte(c.body), 0o644))
			})
			require.NoError(t, err, "oracle must accept prose %q as passing; raw=%s", c.name, raw)
			require.True(t, parsed, "oracle must emit valid JSON; raw=%s", raw)
			require.True(t, result.Passed, "prose %q must NOT trip the failure regex; raw=%s", c.name, raw)
		})
	}
}

// R7-M3: cross-device-code-mod's pass-marker arm
// `=+[[:space:]]+[0-9]+[[:space:]]+passed` accepted
// `==== 0 passed in 0.01s ====` (asymmetric with the failure side which
// uses `0*[1-9][0-9]*` and rejects zero counts).  Tighten the pass arm to
// `0*[1-9][0-9]*` so a 0-passed pytest banner falls through to the
// "no pass marker" branch and the verdict is `passed:false`.
func TestCrossDeviceCodeMod_RejectsZeroPassedPytestRun(t *testing.T) {
	result, raw, parsed, err := runOracle(t, "cross-device-code-mod", func(ws string) {
		require.NoError(t, os.WriteFile(filepath.Join(ws, "test.log"),
			[]byte("==== 0 passed in 0.01s ====\n"), 0o644))
	})
	require.Error(t, err, "oracle must reject 0-passed pytest run; raw=%s", raw)
	require.True(t, parsed, "oracle must emit valid JSON on failure; raw=%s", raw)
	require.False(t, result.Passed, "0-passed pytest banner must NOT satisfy the pass arm (R7-M3); raw=%s", raw)
	// Must fall through to "no pass marker".
	if v, ok := result.Details["test_log"].(string); ok {
		require.Equal(t, "no pass marker", v,
			"0-passed banner must fall through to no-pass-marker branch; got %q", v)
	}
}

// R7-M3 positive control: ≥1 passed pytest banner must still satisfy.
func TestCrossDeviceCodeMod_AcceptsOnePassedPytestRun(t *testing.T) {
	result, raw, parsed, err := runOracle(t, "cross-device-code-mod", func(ws string) {
		require.NoError(t, os.WriteFile(filepath.Join(ws, "test.log"),
			[]byte("==== 1 passed in 0.01s ====\n"), 0o644))
	})
	require.NoError(t, err, "oracle must accept 1-passed pytest run; raw=%s", raw)
	require.True(t, parsed)
	require.True(t, result.Passed)
}

// R7-M4: remote-data-processing/oracle.sh emitted
// `"checksum":"mismatch_with_result"` when result.json was absent — a
// misleading false cause (nothing was tampered, the file is just gone).
// R6-S1 had gated only the GOLDEN block on `[[ -n "$actual" ]]`; the
// checksum block was left to false-positive.  Fix gates the checksum
// block on `[[ -n "$actual" ]]` too, emitting `"checksum":"skipped"` when
// actual is empty.  Also covers R7-S1 (golden axis emits `skipped` for
// operator visibility instead of the silent `:` no-op).
func TestRemoteDataProcessing_MissingResultDoesNotFalseChecksumMismatch(t *testing.T) {
	result, raw, parsed, err := runOracle(t, "remote-data-processing", func(ws string) {
		require.NoError(t, os.Remove(filepath.Join(ws, "result.json")))
	})
	require.Error(t, err, "missing result.json must still fail the oracle; raw=%s", raw)
	require.True(t, parsed, "oracle must emit valid JSON; raw=%s", raw)
	require.False(t, result.Passed)

	// (a) checksum axis must NOT claim mismatch — there is nothing to
	// compare against.  Skipped is the operator-visible signal.
	if v, ok := result.Details["checksum"].(string); ok {
		require.Equal(t, "skipped", v,
			"checksum axis must report skipped when result.json is absent (R7-M4); got %q", v)
	} else {
		t.Fatalf("details.checksum missing; raw=%s", raw)
	}

	// (b) golden axis is always reported (R7-S1) — `skipped` rather than
	// silently disappearing from details.
	if v, ok := result.Details["golden"].(string); ok {
		require.Equal(t, "skipped", v,
			"golden axis must report skipped when result.json is absent (R7-S1); got %q", v)
	} else {
		t.Fatalf("details.golden missing; raw=%s", raw)
	}

	// (c) result-missing axis still fires — the canonical signal.
	if v, ok := result.Details["result"].(string); ok {
		require.Equal(t, "missing", v,
			"result axis must remain the canonical missing signal; got %q", v)
	} else {
		t.Fatalf("details.result missing; raw=%s", raw)
	}
}

// R7-N1: round-6 mocha branch required `[[:space:]]+` (≥1 leading
// whitespace) for `failing` while the bare-failed branch accepted
// `[[:space:]]*` (zero or more) — so `1 failing` (column-zero) silently
// passed while `1 failed` (column-zero) rejected.  The R7-M2 broadening
// of the regex picks this up alongside, but assert it independently as
// a regression guard.
func TestCrossDeviceCodeMod_RejectsColumnZeroOneFailing(t *testing.T) {
	result, raw, parsed, err := runOracle(t, "cross-device-code-mod", func(ws string) {
		require.NoError(t, os.WriteFile(filepath.Join(ws, "test.log"),
			[]byte("PASS\n1 failing\n"), 0o644))
	})
	require.Error(t, err, "oracle must reject column-zero '1 failing'; raw=%s", raw)
	require.True(t, parsed, "oracle must emit valid JSON on failure; raw=%s", raw)
	require.False(t, result.Passed, "column-zero `1 failing` must trip failure regex (R7-N1); raw=%s", raw)
}

// ---------------------------------------------------------------------------
// Round-8 adversarial findings.  Each test captures a bug found during the
// eighth-pass review; they should fail against the round-7 state and pass
// after this commit's fix.
// ---------------------------------------------------------------------------

// R8-M1: remote-data-processing/oracle.sh's R6-S1 / R7-S1 / R7-M4 fixes
// correctly stopped attributing "false mismatch" to a missing result.json,
// but they did so by emitting `"skipped"` on BOTH the checksum and golden
// axes when `actual` is empty — including when `sha256sum` itself is
// unavailable (macOS without coreutils, busybox without sha256sum, or any
// PATH where the binary exits non-zero).  With a non-empty result.json AND
// an unreachable sha256sum, both axes report `skipped` and `passed` stays
// true — a verdict of passed:true with ZERO integrity verification.  Add a
// dedicated `sha256_tool:unavailable` failure axis BEFORE the checksum and
// golden blocks so the missing capability is surfaced AND the verdict is
// forced false.
func TestRemoteDataProcessing_FailsWhenSha256sumUnavailable(t *testing.T) {
	shimDir := t.TempDir()
	shim := filepath.Join(shimDir, "sha256sum")
	// Shim exits 127 so awk gets nothing on stdin and `actual` stays empty.
	require.NoError(t, os.WriteFile(shim, []byte("#!/usr/bin/env bash\nexit 127\n"), 0o755))

	dir := filepath.Join("workloads", "remote-data-processing")
	spec := loadSpec(t, dir)
	oraclePath, err := filepath.Abs(filepath.Join(dir, spec.SuccessOracle))
	require.NoError(t, err)
	srcMock, err := filepath.Abs(filepath.Join(dir, "fixtures", "mock_workspace"))
	require.NoError(t, err)

	workspace := t.TempDir()
	copyDir(t, srcMock, workspace)

	cmd, cancel := runOracleCmd(t, oraclePath, workspace)
	defer cancel()
	cmd.Env = []string{
		"PATH=" + shimDir + ":/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"LC_ALL=C",
		"LANG=C",
		"HOME=" + t.TempDir(),
	}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	stdout := outBuf.String()
	raw := stdout + errBuf.String()

	require.Error(t, runErr, "oracle must fail when sha256sum is unavailable; raw=%s", raw)

	// First non-empty stdout line must still be valid JSON (oracle must
	// not have crashed; the verdict line must always print).
	var line string
	for _, l := range strings.Split(stdout, "\n") {
		if s := strings.TrimSpace(l); s != "" {
			line = s
			break
		}
	}
	require.NotEmpty(t, line, "oracle produced no stdout; raw=%s", raw)
	var result oracleResult
	require.NoError(t, json.Unmarshal([]byte(line), &result),
		"first stdout line must be JSON; got %q; raw=%s", line, raw)

	require.False(t, result.Passed,
		"oracle must NOT report passed:true when sha256sum is unavailable (R8-M1); raw=%s", raw)
	if v, ok := result.Details["sha256_tool"].(string); ok {
		require.Equal(t, "unavailable", v,
			"details.sha256_tool must name the missing capability; got %q", v)
	} else {
		t.Fatalf("details.sha256_tool missing; the new R8-M1 axis is the operator's signal; raw=%s", raw)
	}
	// Downstream axes should still report `skipped` so the operator sees
	// every tier of integrity check, not just the one that failed first.
	if v, ok := result.Details["checksum"].(string); ok {
		require.Equal(t, "skipped", v, "checksum axis should still emit skipped; got %q", v)
	}
	if v, ok := result.Details["golden"].(string); ok {
		require.Equal(t, "skipped", v, "golden axis should still emit skipped; got %q", v)
	}
}

// R8-M2: missing-parser-converter's R5-M1 acceptance-log gate used
// `pass_lines < cases_total` (strict less-than) with cases_total=1.  Three
// PASS lines satisfied `3 < 1 == false` and the oracle reported
// passed:true.  The README has always promised "one PASS/FAIL line per
// golden case" — equality is the contract.  This test verifies the gate
// now rejects extra PASS lines.
func TestMissingParserConverter_RejectsExtraPassLines(t *testing.T) {
	t.Run("three-passes-on-one-case", func(t *testing.T) {
		result, raw, parsed, err := runOracle(t, "missing-parser-converter", func(ws string) {
			require.NoError(t, os.WriteFile(filepath.Join(ws, "acceptance.log"),
				[]byte("PASS one\nPASS two\nPASS three\n"), 0o644))
		})
		require.Error(t, err, "oracle must reject 3 PASS lines on 1-case design (R8-M2); raw=%s", raw)
		require.True(t, parsed, "oracle must emit valid JSON on failure; raw=%s", raw)
		require.False(t, result.Passed, "extra PASS lines must fail; got passed:true; raw=%s", raw)
	})
	t.Run("two-passes-on-one-case", func(t *testing.T) {
		result, raw, parsed, err := runOracle(t, "missing-parser-converter", func(ws string) {
			require.NoError(t, os.WriteFile(filepath.Join(ws, "acceptance.log"),
				[]byte("PASS one\nPASS two\n"), 0o644))
		})
		require.Error(t, err, "oracle must reject 2 PASS lines on 1-case design (R8-M2); raw=%s", raw)
		require.True(t, parsed, "oracle must emit valid JSON on failure; raw=%s", raw)
		require.False(t, result.Passed)
	})
	// Positive control: exactly one canonical PASS line — the contract.
	t.Run("one-pass-canonical", func(t *testing.T) {
		result, raw, parsed, err := runOracle(t, "missing-parser-converter", func(ws string) {
			require.NoError(t, os.WriteFile(filepath.Join(ws, "acceptance.log"),
				[]byte("PASS expected.out\n"), 0o644))
		})
		require.NoError(t, err, "single canonical PASS line must satisfy; raw=%s", raw)
		require.True(t, parsed)
		require.True(t, result.Passed)
	})
	// Zero PASS lines still rejected (previously caught by `< cases_total`;
	// `!= cases_total` keeps it caught).  Empty log is rejected by the
	// `[[ ! -s "$log" ]]` arm first, but the comment above the gate says
	// `cases_total > pass_lines` was the round-5 path — re-cover here.
	t.Run("zero-passes-content-only", func(t *testing.T) {
		// Need at least one byte so the `-s` check passes; use a single
		// blank line that the non-canonical filter will catch as well.
		// We assert failure regardless of the exact axis.
		result, raw, parsed, err := runOracle(t, "missing-parser-converter", func(ws string) {
			// A purely non-PASS/non-FAIL line is already caught by the
			// non_canonical branch; verify nothing slips through.
			require.NoError(t, os.WriteFile(filepath.Join(ws, "acceptance.log"),
				[]byte("# no test markers here\n"), 0o644))
		})
		require.Error(t, err, "zero PASS lines must fail; raw=%s", raw)
		require.True(t, parsed)
		require.False(t, result.Passed)
	})
}

// R8-M3: a hung oracle (infinite loop, blocking read) would otherwise
// block until Go's package-level test timeout.  runOracleCmd wraps every
// invocation in a context with deadline `oracleTestTimeout`.  This test
// drops a `sleep 999` oracle into a temp dir and asserts the executor
// returns within a few seconds of the deadline with a kill/deadline error.
func TestOracleExecution_RespectsTimeout(t *testing.T) {
	dir := t.TempDir()
	hung := filepath.Join(dir, "hung.sh")
	require.NoError(t, os.WriteFile(hung,
		[]byte("#!/usr/bin/env bash\n# Hangs forever — exercises the deadline.\nsleep 999\n"),
		0o755))

	// Use a TIGHT deadline (2s) for this specific probe; we don't want to
	// wait the full oracleTestTimeout (60s) just to prove the wiring works.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, hung, dir)

	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)

	require.Error(t, err, "hung oracle must produce an error from cmd.Run")
	require.Less(t, elapsed, 5*time.Second,
		"executor must return shortly after the deadline; elapsed=%s", elapsed)
	// On Linux, exec.CommandContext kills the process when the context
	// deadline fires; cmd.Run returns either "signal: killed" or the
	// context error.  Accept either substring.
	msg := err.Error()
	require.True(t,
		strings.Contains(msg, "killed") || strings.Contains(msg, "deadline exceeded") || strings.Contains(msg, "signal:"),
		"error must indicate timeout or kill; got %q", msg)
}

// R8-S1: when stderr lands before stdout (e.g. a shim emitting a
// deprecation warning, or a sub-shell printing a diagnostic before the
// oracle's printf), the historical runOracle parsed CombinedOutput's
// first non-empty line — which would have been the stderr line — and
// reported parsed=false.  R8-S1 separates capture: the parse runs only
// against stdout.  This test installs a fake oracle that emits stderr
// FIRST, then a valid JSON verdict on stdout, and asserts parsed=true
// with passed:true.
func TestRunOracle_StderrBeforeStdoutDoesNotBreakParse(t *testing.T) {
	dir := t.TempDir()
	oracle := filepath.Join(dir, "oracle.sh")
	body := `#!/usr/bin/env bash
# Emit a noisy stderr line FIRST so CombinedOutput would interleave it
# before stdout.  The R8-S1 separation ensures the JSON parse only sees
# stdout, so this stderr noise is ignored for the verdict decision.
echo "diag: shim deprecation warning" >&2
printf '{"passed":true,"details":{"x":"y"},"metrics":{}}\n'
`
	require.NoError(t, os.WriteFile(oracle, []byte(body), 0o755))

	cmd, cancel := runOracleCmd(t, oracle, dir)
	defer cancel()
	cmd.Env = scrubbedEnv(t)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	require.NoError(t, cmd.Run())

	stdout := outBuf.String()
	stderr := errBuf.String()
	require.Contains(t, stderr, "diag:", "stderr must contain the noise line")

	// Parse stdout only — this is the R8-S1 invariant.
	var result oracleResult
	var line string
	for _, l := range strings.Split(stdout, "\n") {
		if s := strings.TrimSpace(l); s != "" {
			line = s
			break
		}
	}
	require.NotEmpty(t, line, "stdout must contain the JSON verdict; stdout=%q", stdout)
	require.NoError(t, json.Unmarshal([]byte(line), &result),
		"parse must succeed against stdout-only; line=%q", line)
	require.True(t, result.Passed, "verdict must be passed:true; got %v", result)
}

// R8-S2: GNU grep `$` matches before `\n` but does NOT consume `\r`, so a
// CRLF-terminated `FAILED\r\n` line slipped past `^FAILED$` and silently
// passed.  The fix allows trailing whitespace on the bare-FAILED and
// bare-FAILURE arms.  This test exercises both with CRLF line endings.
func TestCrossDeviceCodeMod_HandlesCRLFLineEndings(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"failed-crlf", "PASS\nFAILED\r\n"},
		{"failure-crlf", "PASS\nFAILURE\r\n"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			result, raw, parsed, err := runOracle(t, "cross-device-code-mod", func(ws string) {
				require.NoError(t, os.WriteFile(filepath.Join(ws, "test.log"),
					[]byte(c.body), 0o644))
			})
			require.Error(t, err, "oracle must reject %s with CRLF; raw=%s", c.name, raw)
			require.True(t, parsed, "oracle must emit valid JSON on failure; raw=%s", raw)
			require.False(t, result.Passed,
				"CRLF-terminated bare-failure marker must trip the failure regex (R8-S2); raw=%s", raw)
		})
	}
}

// R8-S3: windows-only-artifact/oracle.sh emitted `"sha256":"mismatch"` when
// `actual` was empty (artifact.bin missing/empty) but `declared` was a
// valid hex string from the metadata.  That's the same R6-S1 / R7-M4
// false-attribution anti-pattern — the `"artifact":"missing or empty"`
// axis already names the cause; double-signalling as a mismatch makes
// substring tests for "sha256 mismatch" pass for the wrong reason.  Gate
// the mismatch arm on `[[ -n "$actual" ]]` and emit `"sha256":"skipped"`
// when actual is empty.
func TestWindowsOnlyArtifact_MissingArtifactDoesNotFalseMismatch(t *testing.T) {
	result, raw, parsed, err := runOracle(t, "windows-only-artifact", func(ws string) {
		require.NoError(t, os.Remove(filepath.Join(ws, "artifact.bin")))
	})
	require.Error(t, err, "missing artifact.bin must still fail the oracle; raw=%s", raw)
	require.True(t, parsed, "oracle must emit valid JSON on failure; raw=%s", raw)
	require.False(t, result.Passed)

	// (a) sha256 axis must NOT claim mismatch — there's nothing to compare.
	if v, ok := result.Details["sha256"].(string); ok {
		require.Equal(t, "skipped", v,
			"sha256 axis must report skipped when artifact.bin is absent (R8-S3); got %q", v)
	} else {
		t.Fatalf("details.sha256 missing; raw=%s", raw)
	}
	// (b) The canonical missing axis still fires.
	if v, ok := result.Details["artifact"].(string); ok {
		require.Contains(t, v, "missing", "artifact axis must name the cause; got %q", v)
	} else {
		t.Fatalf("details.artifact missing; raw=%s", raw)
	}
	// (c) Importantly, raw must NOT contain `"sha256":"mismatch"` — the
	// substring assertion that the false-attribution would have satisfied.
	require.NotContains(t, raw, `"sha256":"mismatch"`,
		"missing artifact must not double-signal via sha256 mismatch (R8-S3); raw=%s", raw)
}

// R8-S3 regression guard: an actual tamper (artifact present but content
// doesn't match metadata) must still trip `"sha256":"mismatch"`.  The R3
// negative test covers this in the happy direction; this one explicitly
// re-asserts the symmetric case here so the R8-S3 fix doesn't accidentally
// gate away real-mismatch detection.
func TestWindowsOnlyArtifact_TamperStillTrippedAfterR8S3(t *testing.T) {
	result, raw, parsed, err := runOracle(t, "windows-only-artifact", func(ws string) {
		require.NoError(t, os.WriteFile(filepath.Join(ws, "artifact.bin"),
			[]byte("tampered-bytes-after-R8-S3\n"), 0o644))
	})
	require.Error(t, err, "oracle must still reject sha256 tamper; raw=%s", raw)
	require.True(t, parsed, "oracle must emit valid JSON on failure; raw=%s", raw)
	require.False(t, result.Passed)
	require.Contains(t, raw, `"sha256":"mismatch"`,
		"real tamper must still emit sha256:mismatch; R8-S3 must NOT widen the skip arm; raw=%s", raw)
}

// R8-S4: remote-data-processing/oracle.sh's success-branch printf emitted
// `"metrics":{"result_bytes":N}` while the failure-branch printf emitted
// `"metrics":{}` — asymmetric.  windows-only-artifact correctly kept
// `artifact_bytes` on both branches.  Downstream consumers expecting
// `result_bytes` on every result would NPE on the failure path.  This test
// asserts the key is always present (numeric, possibly 0).
func TestRemoteDataProcessing_FailureBranchKeepsResultBytes(t *testing.T) {
	t.Run("golden-mismatch", func(t *testing.T) {
		result, raw, parsed, err := runOracle(t, "remote-data-processing", func(ws string) {
			newContent := []byte(`{"count": 999, "sum": 999, "mean": 999.0}`)
			require.NoError(t, os.WriteFile(filepath.Join(ws, "result.json"), newContent, 0o644))
		})
		require.Error(t, err, "tampered result must fail; raw=%s", raw)
		require.True(t, parsed)
		require.False(t, result.Passed)
		_, ok := result.Metrics["result_bytes"]
		require.True(t, ok,
			"failure branch must keep result_bytes key (R8-S4); metrics=%v; raw=%s",
			result.Metrics, raw)
		// Must be numeric (json decodes ints to float64 by default).
		_, isNum := result.Metrics["result_bytes"].(float64)
		require.True(t, isNum,
			"result_bytes must be numeric; got %#v", result.Metrics["result_bytes"])
	})
	t.Run("missing-result", func(t *testing.T) {
		// Even when result.json is absent (size=0), the key MUST still print.
		result, raw, parsed, err := runOracle(t, "remote-data-processing", func(ws string) {
			require.NoError(t, os.Remove(filepath.Join(ws, "result.json")))
		})
		require.Error(t, err, "missing result must fail; raw=%s", raw)
		require.True(t, parsed)
		require.False(t, result.Passed)
		v, ok := result.Metrics["result_bytes"].(float64)
		require.True(t, ok,
			"missing-result failure branch must keep result_bytes key (R8-S4); metrics=%v; raw=%s",
			result.Metrics, raw)
		require.Equal(t, float64(0), v,
			"size=0 when result.json is missing; got %v", v)
	})
}

// ---------------------------------------------------------------------------
// Round-9 adversarial findings.  Each test captures a bug found during the
// ninth-pass review; they should fail against the round-8 state and pass
// after this commit's fix.
// ---------------------------------------------------------------------------

// R9-M1: R8-M1 force-failed remote-data-processing when sha256sum was
// unavailable, but the sibling windows-only-artifact oracle kept the same
// silent-pass hole.  With a non-empty artifact.bin, valid metadata, and a
// sha256sum shim that exits before printing a hash, actual="" fell into the
// R8-S3 `sha256:"skipped"` branch while passed stayed true.  The oracle must
// surface the missing tool and fail rather than reporting integrity success
// with zero hash verification.
func TestWindowsOnlyArtifact_FailsWhenSha256sumUnavailable(t *testing.T) {
	shimDir := t.TempDir()
	shim := filepath.Join(shimDir, "sha256sum")
	require.NoError(t, os.WriteFile(shim, []byte("#!/usr/bin/env bash\nexit 127\n"), 0o755))

	result, raw, parsed, err := runOracle(t, "windows-only-artifact", nil,
		"PATH="+shimDir+":/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")

	require.Error(t, err, "oracle must fail when sha256sum is unavailable; raw=%s", raw)
	require.True(t, parsed, "oracle must emit valid JSON on failure; raw=%s", raw)
	require.False(t, result.Passed,
		"oracle must not report passed:true when sha256sum produced no artifact hash; raw=%s", raw)
	if v, ok := result.Details["sha256_tool"].(string); ok {
		require.Equal(t, "unavailable", v,
			"details.sha256_tool must name the missing capability; got %q", v)
	} else {
		t.Fatalf("details.sha256_tool missing; raw=%s", raw)
	}
	if v, ok := result.Details["sha256"].(string); ok {
		require.Equal(t, "skipped", v,
			"sha256 axis should still report skipped when no actual hash exists; got %q", v)
	} else {
		t.Fatalf("details.sha256 missing; raw=%s", raw)
	}
}

// R9-S1: windows-only-artifact used `head -n1` only to select the first
// sha256 match from artifact.meta.json, but `head` is not part of the
// eval-oracle tool floor documented for this branch.  The oracle can do the
// same selection with grep+sed, so a missing or broken `head` binary must
// not turn a valid artifact into metadata_sha256:"missing".
func TestWindowsOnlyArtifact_DoesNotDependOnHeadBinary(t *testing.T) {
	shimDir := t.TempDir()
	shim := filepath.Join(shimDir, "head")
	require.NoError(t, os.WriteFile(shim, []byte("#!/usr/bin/env bash\nexit 127\n"), 0o755))

	result, raw, parsed, err := runOracle(t, "windows-only-artifact", nil,
		"PATH="+shimDir+":/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")

	require.NoError(t, err, "oracle should not require head; raw=%s", raw)
	require.True(t, parsed, "oracle must emit valid JSON; raw=%s", raw)
	require.True(t, result.Passed,
		"valid mock artifact must still pass when head is unavailable; raw=%s", raw)
}

// R9-S2: missing-parser-converter and remote-data-processing used external
// `dirname` to locate their committed golden fixtures.  `dirname` is not
// part of the eval-oracle tool floor; worse, remote-data-processing silently
// skipped its golden axis when `dirname` failed because the computed golden
// path became /fixtures/golden/result.sha256.  Fixture lookup should use
// bash path expansion so a broken `dirname` cannot remove a golden check.
func TestGoldenFixtureOracles_DoNotDependOnDirnameBinary(t *testing.T) {
	shimDir := t.TempDir()
	shim := filepath.Join(shimDir, "dirname")
	require.NoError(t, os.WriteFile(shim, []byte("#!/usr/bin/env bash\nexit 127\n"), 0o755))
	envPath := "PATH=" + shimDir + ":/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

	t.Run("missing-parser-converter", func(t *testing.T) {
		result, raw, parsed, err := runOracle(t, "missing-parser-converter", nil, envPath)
		require.NoError(t, err, "oracle should not require dirname to find expected.out; raw=%s", raw)
		require.True(t, parsed, "oracle must emit valid JSON; raw=%s", raw)
		require.True(t, result.Passed, "canonical mock must still pass without dirname; raw=%s", raw)
		require.Equal(t, "1/1", result.Details["golden"],
			"golden comparison must remain wired without dirname; raw=%s", raw)
	})

	t.Run("remote-data-processing", func(t *testing.T) {
		result, raw, parsed, err := runOracle(t, "remote-data-processing", nil, envPath)
		require.NoError(t, err, "oracle should not require dirname to find result.sha256; raw=%s", raw)
		require.True(t, parsed, "oracle must emit valid JSON; raw=%s", raw)
		require.True(t, result.Passed, "canonical mock must still pass without dirname; raw=%s", raw)
		require.Equal(t, "matches", result.Details["golden"],
			"golden checksum axis must not disappear when dirname is unavailable; raw=%s", raw)
	})
}
