package common

// Test #55 — structural drift guard: every Test.*(Latency|Throughput).*
// under tools/eval/microbench/ and tools/eval/scale_sweep/ has a
// testing.Short() OR os.Getenv("CI") clause in its body (spec §6 (h)).
//
// Runs one package-level walk over both trees; a matching function
// missing the guard fails the test with a specific file + name.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestPerfTestsUseCIGuard(t *testing.T) {
	perfNameRE := regexp.MustCompile(`^Test.*(Latency|Throughput).*$`)
	guardRE := regexp.MustCompile(`testing\.Short\(\)|os\.Getenv\("CI"\)`)
	// The test runs from tools/eval/microbench/common; walk the
	// grandparent tools/eval directory to cover both microbench/ and
	// scale_sweep/ subtrees.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// wd = .../tools/eval/microbench/common
	root := filepath.Dir(filepath.Dir(wd)) // .../tools/eval
	fset := token.NewFileSet()
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(p, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		file, perr := parser.ParseFile(fset, p, src, 0)
		if perr != nil {
			return perr
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if !perfNameRE.MatchString(fn.Name.Name) {
				continue
			}
			start := fset.Position(fn.Body.Pos()).Offset
			end := fset.Position(fn.Body.End()).Offset
			if start < 0 || end > len(src) || start >= end {
				continue
			}
			body := string(src[start:end])
			if !guardRE.MatchString(body) {
				t.Errorf("%s: %s lacks testing.Short() / os.Getenv(\"CI\") guard",
					p, fn.Name.Name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
