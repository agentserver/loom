package executor

import (
	"reflect"
	"testing"
)

func TestImportsValidate_AllStdlib(t *testing.T) {
	src := "import json\nimport os\nfrom urllib.parse import quote\n"
	bad, err := ValidateImports(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 0 {
		t.Fatalf("unexpected disallowed: %v", bad)
	}
}

func TestImportsValidate_AllowedPackage(t *testing.T) {
	src := "import requests\nfrom PIL import Image\n"
	bad, err := ValidateImports(src, []string{"requests", "pillow"})
	if err != nil {
		t.Fatal(err)
	}
	// Note: top-level package name is what we check; "from PIL import" means
	// top-level "PIL", which the allow-list resolves via lowercase to "pillow".
	if len(bad) != 0 {
		t.Fatalf("unexpected disallowed: %v", bad)
	}
}

func TestImportsValidate_Disallowed(t *testing.T) {
	src := "import json\nimport requests_html\nimport notapackage\n"
	bad, err := ValidateImports(src, []string{"requests"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"notapackage", "requests_html"}
	if !reflect.DeepEqual(bad, want) {
		t.Fatalf("bad = %v, want %v", bad, want)
	}
}

func TestImportsValidate_FromImportSubmodule(t *testing.T) {
	src := "from os.path import join\n"
	bad, _ := ValidateImports(src, nil)
	if len(bad) != 0 {
		t.Fatalf("unexpected disallowed for stdlib submodule: %v", bad)
	}
}
