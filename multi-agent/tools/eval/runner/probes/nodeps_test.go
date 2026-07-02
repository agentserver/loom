// nodeps_test.go — Security §7(f): probes package must not import
// database/sql or net/http. Prevents the "quick add sqlite writer"
// drift that would put SQL / network IO on the runner hot path.
package probes

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProbes_NoForbiddenImports(t *testing.T) {
	forbidden := map[string]bool{
		`"database/sql"`: true,
		`"net/http"`:     true,
	}
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(".", e.Name()), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		for _, imp := range f.Imports {
			if forbidden[imp.Path.Value] {
				t.Fatalf("%s: forbidden import %s", e.Name(), imp.Path.Value)
			}
		}
	}
}
