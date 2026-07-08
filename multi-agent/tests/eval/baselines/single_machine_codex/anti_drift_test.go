package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

// TestCodexPromptsMatchClaudePrompts verifies codexPrompts is a
// verbatim copy of ../single_machine/workloads.go claudePrompts.
//
// The two baselines MUST share prompts so any measured behavior
// difference is attributable to the CLI, not the prompt (spec §5).
// Implementation constraint (spec Global Constraints): AST-only
// extraction, NOT go:embed of a golden JSON — a stale golden could
// pass silently while claude drifts.
func TestCodexPromptsMatchClaudePrompts(t *testing.T) {
	_, self, _, _ := runtime.Caller(0)
	claudeFile := filepath.Join(filepath.Dir(self), "..", "single_machine", "workloads.go")

	claudeExtracted := extractPromptMap(t, claudeFile, "claudePrompts")
	if len(claudeExtracted) == 0 {
		t.Fatalf("claudePrompts extraction returned empty map — parser or claude source drift")
	}
	if len(codexPrompts) != len(claudeExtracted) {
		t.Fatalf("codexPrompts has %d keys, claudePrompts has %d — key drift; add/remove entries to match", len(codexPrompts), len(claudeExtracted))
	}
	for k, cv := range claudeExtracted {
		xv, ok := codexPrompts[k]
		if !ok {
			t.Errorf("codexPrompts missing key %q present in claudePrompts", k)
			continue
		}
		if xv.Prompt != cv.Prompt {
			t.Errorf("prompt drift on %q:\n  claude: %q\n  codex:  %q", k, cv.Prompt, xv.Prompt)
		}
		if !stringSliceEqual(xv.ExpectedOutputs, cv.ExpectedOutputs) {
			t.Errorf("expected-outputs drift on %q:\n  claude: %v\n  codex:  %v", k, cv.ExpectedOutputs, xv.ExpectedOutputs)
		}
	}
	// Reverse direction — codex may not contain keys absent from claude.
	for k := range codexPrompts {
		if _, ok := claudeExtracted[k]; !ok {
			t.Errorf("codexPrompts has %q not present in claudePrompts", k)
		}
	}
}

// extractPromptMap AST-parses `file` and finds a top-level `var
// <name> = map[string]promptSpec{ ... }` declaration, returning its
// key→promptSpec pairs. Fails the test on any deviation from that
// exact shape.
func extractPromptMap(t *testing.T, file, name string) map[string]promptSpec {
	t.Helper()
	fs := token.NewFileSet()
	af, err := parser.ParseFile(fs, file, nil, parser.AllErrors)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	out := map[string]promptSpec{}
	for _, decl := range af.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, n := range vs.Names {
				if n.Name != name {
					continue
				}
				if i >= len(vs.Values) {
					t.Fatalf("%s declared without value", name)
				}
				cl, ok := vs.Values[i].(*ast.CompositeLit)
				if !ok {
					t.Fatalf("%s must be a composite literal, got %T", name, vs.Values[i])
				}
				for _, elt := range cl.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						t.Fatalf("%s element not KeyValueExpr: %T", name, elt)
					}
					key := astLitToString(t, kv.Key)
					out[key] = extractPromptSpec(t, kv.Value)
				}
				return out
			}
		}
	}
	t.Fatalf("var %s not found in %s", name, file)
	return nil
}

func extractPromptSpec(t *testing.T, e ast.Expr) promptSpec {
	t.Helper()
	cl, ok := e.(*ast.CompositeLit)
	if !ok {
		t.Fatalf("promptSpec value not CompositeLit: %T", e)
	}
	var ps promptSpec
	for _, elt := range cl.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			t.Fatalf("promptSpec field not KeyValueExpr: %T", elt)
		}
		// Plan-review P2: type-check the field key to avoid a panic
		// on non-Ident (e.g. selector expr) drift.
		fieldIdent, ok := kv.Key.(*ast.Ident)
		if !ok {
			t.Fatalf("promptSpec field key not *ast.Ident: %T", kv.Key)
		}
		switch fieldIdent.Name {
		case "Prompt":
			ps.Prompt = astLitToString(t, kv.Value)
		case "ExpectedOutputs":
			ps.ExpectedOutputs = astLitToStringSlice(t, kv.Value)
		default:
			t.Fatalf("unknown promptSpec field %q", fieldIdent.Name)
		}
	}
	return ps
}

func astLitToString(t *testing.T, e ast.Expr) string {
	t.Helper()
	bl, ok := e.(*ast.BasicLit)
	if !ok {
		t.Fatalf("expected string literal, got %T", e)
	}
	s, err := strconv.Unquote(bl.Value)
	if err != nil {
		t.Fatalf("unquote %q: %v", bl.Value, err)
	}
	return s
}

func astLitToStringSlice(t *testing.T, e ast.Expr) []string {
	t.Helper()
	cl, ok := e.(*ast.CompositeLit)
	if !ok {
		t.Fatalf("expected slice literal, got %T", e)
	}
	out := make([]string, 0, len(cl.Elts))
	for _, elt := range cl.Elts {
		out = append(out, astLitToString(t, elt))
	}
	return out
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
