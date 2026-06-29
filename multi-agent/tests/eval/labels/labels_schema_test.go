// Package labels validates ground-truth annotation files for §F1/§F2 against
// their JSON Schemas. The annotations feed v3 evaluation metrics
// (RoutingAccuracy / CapabilityRecall / CapabilityPrecision); breaking the
// schema must break the build, not silently degrade the metrics.
package labels

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

const (
	// File-name suffix every labels file must use.
	labelsSuffix = ".labels.json"

	// Expected fan-out (must match the prompt: 5 workloads + 5 families × 4 tasks).
	expectedWorkloadLabels = 5
	expectedFamilyLabels   = 5 * 4
)

// knownContexts is the closed set of (agent_role, context_id) pairs documented
// in README.md as the namespace this worktree fixes for the §F1/§F2 spec.yaml
// join. The JSON Schema only enforces kebab-case shape, so a typo like
// `slave-windoes-desktop` or a mis-paired `(driver, slave-linux-server)` would
// pass schema validation and silently break the downstream
// `required_contexts[].role` join. Enforcing the set here catches it at build
// time. To grow the set, edit BOTH this map AND the README `context_id ↔ spec
// coupling` table in the same PR.
var knownContexts = map[string]map[string]struct{}{
	"driver":  {"driver-linux-laptop": {}},
	"slave":   {"slave-linux-server": {}, "slave-windows-desktop": {}},
	"sandbox": {"sandbox-cloud": {}},
}

func labelsDir(t *testing.T) string {
	t.Helper()
	// Test is invoked with the package directory as cwd.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return wd
}

// collectLabels walks a subdirectory (workloads/ or families/) and returns
// every *.labels.json under it.
func collectLabels(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if strings.HasSuffix(info.Name(), labelsSuffix) {
			out = append(out, path)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(out)
	return out
}

// loadCompiler compiles a JSON Schema from a path on disk.
func loadCompiler(t *testing.T, path string) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	c.Draft = jsonschema.Draft7
	if err := c.AddResource(filepath.Base(path), mustOpen(t, path)); err != nil {
		t.Fatalf("AddResource %s: %v", path, err)
	}
	sch, err := c.Compile(filepath.Base(path))
	if err != nil {
		t.Fatalf("compile %s: %v", path, err)
	}
	return sch
}

func mustOpen(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func mustReadJSON(t *testing.T, path string) any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var v any
	if err := jsonUnmarshalStrict(b, &v); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return v
}

// TestSchemasRejectBadInput is a sanity test on the schemas themselves: a
// payload that violates the constraints we care about (unknown kind, missing
// required field, unknown additional property) must fail. Without this the
// "labels validate" test could be passing because the schemas are too lax.
func TestSchemasRejectBadInput(t *testing.T) {
	base := labelsDir(t)
	schemaDir := filepath.Join(base, "schema")
	gtcSchema := loadCompiler(t, filepath.Join(schemaDir, "ground_truth_context.schema.json"))
	cgtSchema := loadCompiler(t, filepath.Join(schemaDir, "context_ground_truth.schema.json"))

	cases := []struct {
		name   string
		schema *jsonschema.Schema
		doc    string
	}{
		{
			name:   "gtc rejects unknown agent_role",
			schema: gtcSchema,
			doc:    `{"agent_role":"master","context_id":"x-y-z","rationale":"long enough"}`,
		},
		{
			name:   "gtc rejects empty context_id",
			schema: gtcSchema,
			doc:    `{"agent_role":"driver","context_id":"","rationale":"long enough"}`,
		},
		{
			name:   "gtc rejects extra property",
			schema: gtcSchema,
			doc:    `{"agent_role":"driver","context_id":"x-y-z","rationale":"long enough","extra":1}`,
		},
		{
			name:   "cgt rejects empty required_capabilities",
			schema: cgtSchema,
			doc:    `{"required_capabilities":[]}`,
		},
		{
			name:   "cgt rejects unknown capability kind",
			schema: cgtSchema,
			doc:    `{"required_capabilities":[{"kind":"gpu","name":"a100"}]}`,
		},
		{
			name:   "cgt rejects unknown additional property inside capability",
			schema: cgtSchema,
			doc:    `{"required_capabilities":[{"kind":"tool","name":"go","stray":true}]}`,
		},
		{
			name:   "cgt rejects credential alias with uppercase",
			schema: cgtSchema,
			doc:    `{"required_capabilities":[{"kind":"tool","name":"go"}],"credential_aliases":["BAD_ALIAS"]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var v any
			if err := jsonUnmarshalStrict([]byte(tc.doc), &v); err != nil {
				t.Fatalf("doc not parseable: %v", err)
			}
			if err := tc.schema.Validate(v); err == nil {
				t.Fatalf("expected schema rejection, got accept for: %s", tc.doc)
			}
		})
	}
}

func TestSchemasCompile(t *testing.T) {
	dir := filepath.Join(labelsDir(t), "schema")
	for _, name := range []string{
		"ground_truth_context.schema.json",
		"context_ground_truth.schema.json",
	} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("schema missing: %s", p)
		}
		_ = loadCompiler(t, p)
	}
}

func TestLabelsValidateAgainstSchemas(t *testing.T) {
	base := labelsDir(t)
	schemaDir := filepath.Join(base, "schema")

	gtcSchema := loadCompiler(t, filepath.Join(schemaDir, "ground_truth_context.schema.json"))
	cgtSchema := loadCompiler(t, filepath.Join(schemaDir, "context_ground_truth.schema.json"))

	workloads := collectLabels(t, filepath.Join(base, "workloads"))
	families := collectLabels(t, filepath.Join(base, "families"))

	if got := len(workloads); got != expectedWorkloadLabels {
		t.Errorf("workload labels count = %d, want %d (files: %v)", got, expectedWorkloadLabels, workloads)
	}
	if got := len(families); got != expectedFamilyLabels {
		t.Errorf("family labels count = %d, want %d (files: %v)", got, expectedFamilyLabels, families)
	}

	seen := map[string]string{}
	for _, p := range append(append([]string{}, workloads...), families...) {
		t.Run(strings.TrimPrefix(p, base+string(os.PathSeparator)), func(t *testing.T) {
			doc := mustReadJSON(t, p)
			m, ok := doc.(map[string]any)
			if !ok {
				t.Fatalf("top-level must be object")
			}
			id, _ := m["task_id"].(string)
			if id == "" {
				t.Fatalf("task_id missing or empty")
			}
			if prev, dup := seen[id]; dup {
				t.Fatalf("duplicate task_id %q (also at %s)", id, prev)
			}
			seen[id] = p

			gtc, ok := m["ground_truth_context"]
			if !ok {
				t.Fatalf("ground_truth_context missing")
			}
			if err := gtcSchema.Validate(gtc); err != nil {
				t.Fatalf("ground_truth_context schema violation: %v", err)
			}
			if err := checkKnownContext(gtc); err != nil {
				t.Fatalf("known-context check: %v", err)
			}

			cgt, ok := m["context_ground_truth"]
			if !ok {
				t.Fatalf("context_ground_truth missing")
			}
			if err := cgtSchema.Validate(cgt); err != nil {
				t.Fatalf("context_ground_truth schema violation: %v", err)
			}

			// README invariant: every `credential` capability alias must
			// appear in the top-level credential_aliases mirror, and
			// vice-versa. The schemas can't express this on their own
			// (cross-field constraint) so it's enforced here.
			checkCredentialAliasMirror(t, cgt)
		})
	}
}

// checkCredentialAliasMirror enforces the invariant documented in README.md
// (`credential_aliases is a top-level mirror of any credential capability`).
// A drift between the two would silently degrade credential analysis tooling
// that relies on the mirror to skip a capability-tree walk.
func checkCredentialAliasMirror(t *testing.T, cgt any) {
	t.Helper()
	m, ok := cgt.(map[string]any)
	if !ok {
		t.Fatalf("context_ground_truth not an object")
	}
	caps, _ := m["required_capabilities"].([]any)
	fromCaps := map[string]struct{}{}
	for _, c := range caps {
		obj, _ := c.(map[string]any)
		if obj["kind"] != "credential" {
			continue
		}
		alias, _ := obj["alias"].(string)
		if alias == "" {
			t.Fatalf("credential capability missing alias: %v", obj)
		}
		fromCaps[alias] = struct{}{}
	}
	mirror := map[string]struct{}{}
	if raw, ok := m["credential_aliases"].([]any); ok {
		for _, v := range raw {
			s, _ := v.(string)
			mirror[s] = struct{}{}
		}
	}
	for a := range fromCaps {
		if _, ok := mirror[a]; !ok {
			t.Errorf("credential alias %q present in required_capabilities but missing from credential_aliases mirror", a)
		}
	}
	for a := range mirror {
		if _, ok := fromCaps[a]; !ok {
			t.Errorf("credential alias %q listed in credential_aliases mirror but no matching credential capability", a)
		}
	}
}

// checkKnownContext rejects any (agent_role, context_id) pair not in the
// closed set documented in README.md. The schema cannot express this
// (context_id is free-form kebab-case so contributors can extend the namespace
// without touching the schema). Without this gate, a typo in context_id would
// pass the build and silently fail the §F1/§F2 spec.yaml join at metric
// extraction time, when it is much harder to diagnose.
func checkKnownContext(gtc any) error {
	m, ok := gtc.(map[string]any)
	if !ok {
		return fmt.Errorf("ground_truth_context not an object")
	}
	role, _ := m["agent_role"].(string)
	cid, _ := m["context_id"].(string)
	allowed, ok := knownContexts[role]
	if !ok {
		return fmt.Errorf("agent_role %q not in knownContexts (schema accepted it but the README context_id namespace does not enumerate it)", role)
	}
	if _, ok := allowed[cid]; !ok {
		want := make([]string, 0, len(allowed))
		for k := range allowed {
			want = append(want, k)
		}
		sort.Strings(want)
		return fmt.Errorf("context_id %q not allowed for role %q; allowed: %v (update knownContexts + README in the same PR if intentional)", cid, role, want)
	}
	return nil
}

// TestKnownContextRejectsTypoAndMismatch is the negative test for the closed
// set above: a typo'd context_id (right shape, wrong identity) and a swapped
// role/context_id pairing must both be rejected. Without this, future
// refactors could silently drop the check and the schema-only validation
// would not catch the regression.
func TestKnownContextRejectsTypoAndMismatch(t *testing.T) {
	cases := []struct {
		name string
		gtc  map[string]any
	}{
		{
			name: "typo in context_id",
			gtc: map[string]any{
				"agent_role": "slave",
				"context_id": "slave-windoes-desktop", // missing 'w', kebab-case shape still passes the regex
				"rationale":  "ignored",
			},
		},
		{
			name: "role/context_id mismatch",
			gtc: map[string]any{
				"agent_role": "driver",
				"context_id": "slave-linux-server",
				"rationale":  "ignored",
			},
		},
		{
			name: "unknown context_id with right kebab shape",
			gtc: map[string]any{
				"agent_role": "sandbox",
				"context_id": "sandbox-on-prem",
				"rationale":  "ignored",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkKnownContext(tc.gtc); err == nil {
				t.Fatalf("expected checkKnownContext to reject %v, got nil", tc.gtc)
			}
		})
	}
}

// TestJSONStrictRejectsTrailingTokens locks in the bug fix in
// json_strict.go: a concatenated payload must fail to parse, not
// silently keep the first object.
func TestJSONStrictRejectsTrailingTokens(t *testing.T) {
	var v any
	if err := jsonUnmarshalStrict([]byte(`{"a":1}{"b":2}`), &v); err == nil {
		t.Fatalf("expected trailing-token rejection, got nil")
	}
	// Sanity: single object still parses.
	if err := jsonUnmarshalStrict([]byte(`{"a":1}`), &v); err != nil {
		t.Fatalf("unexpected parse error on single object: %v", err)
	}
}
