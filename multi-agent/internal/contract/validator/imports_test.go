package validator_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// §7(a): validator MUST be pure. Enforce the import allow-list
// mechanically so a future refactor cannot silently pull in os/net/io
// and turn the dry-run tool into a side-effect vector.
var allowedImports = map[string]struct{}{
	`"context"`:                                             {},
	`"errors"`:                                              {},
	`"fmt"`:                                                 {},
	`"log"`:                                                 {},
	`"path"`:                                                {},
	`"sort"`:                                                {},
	`"strings"`:                                             {},
	`"golang.org/x/mod/semver"`:                             {},
	`"github.com/yourorg/multi-agent/internal/ablation"`:   {},
	`"github.com/yourorg/multi-agent/internal/capability"`: {},
	`"github.com/yourorg/multi-agent/internal/contract"`:   {},
}

// §7(b) regression bait: any use of strings.Split with a literal "."
// under this package is a spec-forbidden hand-rolled semver split.
// Coarse text-grep (an AST walk would be more precise but false-positives
// are cheap to work around here).
func TestNoHandRolledSemverSplit(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		// Skip _test.go files so this guard itself doesn't trigger.
		if strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), `strings.Split(`) &&
			strings.Contains(string(body), `"."`) {
			t.Errorf("%s: strings.Split + literal \".\" — hand-rolled semver split forbidden (§7(b))", e.Name())
		}
	}
}

func TestImportPurity(t *testing.T) {
	fset := token.NewFileSet()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		for _, imp := range f.Imports {
			if _, ok := allowedImports[imp.Path.Value]; !ok {
				t.Errorf("%s: forbidden import %s — validator must stay pure (see spec §7(a))", e.Name(), imp.Path.Value)
			}
		}
	}
}
