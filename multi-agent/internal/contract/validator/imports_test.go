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
