// Package labels validates ground-truth annotation files for §F1/§F2 against
// their JSON Schemas. The annotations feed v3 evaluation metrics
// (RoutingAccuracy / CapabilityRecall / CapabilityPrecision); breaking the
// schema must break the build, not silently degrade the metrics.
package labels

import (
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

			cgt, ok := m["context_ground_truth"]
			if !ok {
				t.Fatalf("context_ground_truth missing")
			}
			if err := cgtSchema.Validate(cgt); err != nil {
				t.Fatalf("context_ground_truth schema violation: %v", err)
			}
		})
	}
}
