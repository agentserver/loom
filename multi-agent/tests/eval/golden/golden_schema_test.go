// Package golden validates the on-disk shape of the E4 task family golden
// fixtures. It is intentionally hermetic: no network, no compose, no MCP — it
// only walks the directory tree and asserts the documented contract for §F2.
//
// What "shape" means here:
//
//   - Every directory under tests/eval/golden/<family-id>/ must contain
//     exactly the layout documented in WT-0 prompt: first-task/, reuse-1/,
//     reuse-2/, reuse-3/, acceptance/.
//   - Each task directory holds a valid spec.yaml conforming to the §F1
//     workload spec field convention (we re-implement the minimal subset
//     here so this worktree has no source dependency on the §F1 worktree).
//   - Each task directory carries input/ and expected/ subtrees (oracle
//     ground-truth lives in expected/).
//   - acceptance/cases.jsonl is one JSON object per line, ≥5 cases, with the
//     {name, tool, input, expected|expected_error} protocol consumed later
//     by `skills/mcp-acceptance --cases`.
//   - The five family IDs documented in §F2 are present.
package golden

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// expectedFamilies is the closed set of family IDs §F2 commits to.
var expectedFamilies = []string{
	"api-wrapper-for-local-service",
	"csv-profiler",
	"image-metadata-extractor",
	"log-parser",
	"refund-policy-checker",
}

// requiredTaskDirs is the per-family directory contract.
var requiredTaskDirs = []string{"first-task", "reuse-1", "reuse-2", "reuse-3"}

// minCases is the §F2 acceptance floor — at least 5 cases per family
// covering happy path / edge / error / boundary / negative.
const minCases = 5

// taskSpec is the minimal §F1-aligned spec.yaml shape we enforce. We do NOT
// import internal/contract here because the §F1 worktree owns that struct
// and §F2 must stay decoupled (per prompt: "你这边只用结构，不依赖 §F1
// worktree 的代码").
type taskSpec struct {
	Family string `yaml:"family"`
	ID     string `yaml:"id"`
	Kind   string `yaml:"kind"` // "first" | "reuse"
	Intent struct {
		Goal            string   `yaml:"goal"`
		SuccessCriteria []string `yaml:"success_criteria"`
	} `yaml:"intent"`
	Inputs []struct {
		Path string `yaml:"path"`
	} `yaml:"inputs"`
	Expected struct {
		Artifacts []struct {
			Path string `yaml:"path"`
		} `yaml:"artifacts"`
	} `yaml:"expected"`
	CapabilityRequirements struct {
		Skills []string `yaml:"skills"`
		Tools  []string `yaml:"tools"`
	} `yaml:"capability_requirements"`
}

// acceptanceCase is the jsonl line contract from §F2 / B3.
//
// Exactly one of {Expected, ExpectedError} must be set per line.
type acceptanceCase struct {
	Name          string          `json:"name"`
	Tool          string          `json:"tool"`
	Input         json.RawMessage `json:"input"`
	Expected      json.RawMessage `json:"expected,omitempty"`
	ExpectedError string          `json:"expected_error,omitempty"`
}

func goldenRoot(t *testing.T) string {
	t.Helper()
	// The test runs from the package directory (tests/eval/golden), so the
	// fixtures live alongside this file. Resolving relative keeps the test
	// movable.
	abs, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	return abs
}

func TestExpectedFamiliesArePresent(t *testing.T) {
	root := goldenRoot(t)
	for _, fam := range expectedFamilies {
		info, err := os.Stat(filepath.Join(root, fam))
		if err != nil {
			t.Errorf("family %q missing: %v", fam, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("family %q is not a directory", fam)
		}
	}
}

func TestNoUnexpectedFamilies(t *testing.T) {
	root := goldenRoot(t)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("readdir %s: %v", root, err)
	}
	want := map[string]struct{}{}
	for _, f := range expectedFamilies {
		want[f] = struct{}{}
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Allow ad-hoc helper subdirs prefixed with "_" if we ever need
		// them; require everything else to be in the closed set.
		if strings.HasPrefix(e.Name(), "_") {
			continue
		}
		if _, ok := want[e.Name()]; !ok {
			t.Errorf("unexpected family dir %q under %s", e.Name(), root)
		}
	}
}

func TestEachFamilyHasRequiredLayout(t *testing.T) {
	root := goldenRoot(t)
	for _, fam := range expectedFamilies {
		fam := fam
		t.Run(fam, func(t *testing.T) {
			famDir := filepath.Join(root, fam)
			// README + acceptance dir required.
			if _, err := os.Stat(filepath.Join(famDir, "README.md")); err != nil {
				t.Errorf("missing README.md: %v", err)
			}
			if _, err := os.Stat(filepath.Join(famDir, "acceptance", "cases.jsonl")); err != nil {
				t.Errorf("missing acceptance/cases.jsonl: %v", err)
			}
			for _, sub := range requiredTaskDirs {
				taskDir := filepath.Join(famDir, sub)
				if _, err := os.Stat(taskDir); err != nil {
					t.Errorf("missing task dir %s: %v", sub, err)
					continue
				}
				validateTaskDir(t, fam, sub, taskDir)
			}
		})
	}
}

func validateTaskDir(t *testing.T, family, name, dir string) {
	t.Helper()

	specPath := filepath.Join(dir, "spec.yaml")
	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Errorf("%s: missing spec.yaml: %v", name, err)
		return
	}
	var spec taskSpec
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Errorf("%s: spec.yaml parse: %v", name, err)
		return
	}
	if spec.Family != family {
		t.Errorf("%s/spec.yaml: family=%q want %q", name, spec.Family, family)
	}
	if spec.ID == "" {
		t.Errorf("%s/spec.yaml: id is empty", name)
	}
	wantKind := "reuse"
	if name == "first-task" {
		wantKind = "first"
	}
	if spec.Kind != wantKind {
		t.Errorf("%s/spec.yaml: kind=%q want %q", name, spec.Kind, wantKind)
	}
	if strings.TrimSpace(spec.Intent.Goal) == "" {
		t.Errorf("%s/spec.yaml: intent.goal empty", name)
	}
	if len(spec.Intent.SuccessCriteria) == 0 {
		t.Errorf("%s/spec.yaml: intent.success_criteria empty", name)
	}
	if len(spec.Inputs) == 0 {
		t.Errorf("%s/spec.yaml: inputs empty", name)
	}
	if len(spec.Expected.Artifacts) == 0 {
		t.Errorf("%s/spec.yaml: expected.artifacts empty", name)
	}
	if len(spec.CapabilityRequirements.Tools) == 0 {
		t.Errorf("%s/spec.yaml: capability_requirements.tools empty (need at least one tool name so Stage C lookup has something to hit)", name)
	}

	// Referenced input/expected files must exist on disk, anchored to the
	// task dir — the oracle has to be able to find them.
	for _, in := range spec.Inputs {
		if _, err := os.Stat(filepath.Join(dir, in.Path)); err != nil {
			t.Errorf("%s/spec.yaml input %q not found: %v", name, in.Path, err)
		}
	}
	for _, art := range spec.Expected.Artifacts {
		if _, err := os.Stat(filepath.Join(dir, art.Path)); err != nil {
			t.Errorf("%s/spec.yaml expected %q not found: %v", name, art.Path, err)
		}
	}
}

func TestAcceptanceCasesAreValid(t *testing.T) {
	root := goldenRoot(t)
	for _, fam := range expectedFamilies {
		fam := fam
		t.Run(fam, func(t *testing.T) {
			path := filepath.Join(root, fam, "acceptance", "cases.jsonl")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
			var cases []acceptanceCase
			names := map[string]int{}
			for i, line := range lines {
				if strings.TrimSpace(line) == "" {
					t.Errorf("line %d: blank line not allowed in jsonl", i+1)
					continue
				}
				var c acceptanceCase
				dec := json.NewDecoder(strings.NewReader(line))
				dec.DisallowUnknownFields()
				if err := dec.Decode(&c); err != nil {
					t.Errorf("line %d: parse: %v", i+1, err)
					continue
				}
				if c.Name == "" {
					t.Errorf("line %d: name empty", i+1)
				}
				if c.Tool == "" {
					t.Errorf("line %d: tool empty", i+1)
				}
				if len(c.Input) == 0 {
					t.Errorf("line %d: input missing", i+1)
				}
				hasExpected := len(c.Expected) > 0
				hasError := c.ExpectedError != ""
				if hasExpected == hasError {
					t.Errorf("line %d: exactly one of {expected, expected_error} required (have expected=%v error=%v)", i+1, hasExpected, hasError)
				}
				names[c.Name]++
				cases = append(cases, c)
			}
			if len(cases) < minCases {
				t.Errorf("%s: have %d cases, need ≥%d", fam, len(cases), minCases)
			}
			for n, count := range names {
				if count > 1 {
					t.Errorf("duplicate case name %q (%d occurrences)", n, count)
				}
			}
			// Sanity: case names should be deterministic for sorted output,
			// and the tool field should be uniform per family (one MCP per
			// family — Stage B固化 produces one tool per family).
			tools := map[string]struct{}{}
			for _, c := range cases {
				tools[c.Tool] = struct{}{}
			}
			if len(tools) != 1 {
				keys := make([]string, 0, len(tools))
				for k := range tools {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				t.Errorf("%s acceptance: expected exactly one tool per family, got %v", fam, keys)
			}
		})
	}
}
