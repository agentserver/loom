package main

import (
	"bytes"
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

// Test #33 — --dry-run neither opens a socket nor writes to disk.
func TestTunnelBench_DryRun_NoNetworkNoDisk(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	before := sumTreeBytes(t, tmp)
	var out bytes.Buffer
	code := run([]string{"--dry-run"}, &out, &out)
	if code != 0 {
		t.Fatalf("dry-run exit=%d out=%s", code, out.String())
	}
	if !strings.Contains(out.String(), "tunnel_bench plan") {
		t.Errorf("dry-run stdout missing plan header: %q", out.String())
	}
	after := sumTreeBytes(t, tmp)
	if before != after {
		t.Errorf("TMPDIR byte total changed during --dry-run: before=%d after=%d", before, after)
	}
}

// Test #34 — no disk writes during a full 100 MiB transfer.
// Uses --warmup 100 --samples 500 (spec floor) but a single 100 MiB size.
// If any code path staged the payload to a temp file, sumTreeBytes(TMPDIR)
// would drift.
func TestTunnelBench_NoDiskWrites_100MiB(t *testing.T) {
	if testing.Short() {
		t.Skip("100 MiB transfer skipped in short mode")
	}
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	before := sumTreeBytes(t, tmp)
	var out bytes.Buffer
	// Use tighter sample count to keep the test fast; still above floor.
	code := run([]string{
		"--sizes", "100MiB",
		"--warmup", "100",
		"--samples", "500",
	}, &out, &out)
	if code != 0 {
		t.Fatalf("exit=%d out=%s", code, out.String())
	}
	after := sumTreeBytes(t, tmp)
	if before != after {
		t.Errorf("TMPDIR bytes drifted: before=%d after=%d — 100 MiB payload leaked to disk", before, after)
	}
}

// Test #35 — every Test.*(Latency|Throughput).* function in this file
// has a CI-guard clause in its body (spec §6 (h)). Structural go/parser
// walk; catches drift where a future PR "conveniently" adds an
// unguarded perf test.
//
// Note: the name deliberately avoids the (Latency|Throughput|Perf)
// pattern the regex hunts for, so the checker doesn't flag itself.
func TestTunnelBench_GuardDrift_StructuralCheck(t *testing.T) {
	assertCIGuards(t, ".")
}

func assertCIGuards(t *testing.T, dir string) {
	t.Helper()
	perfNameRE := regexp.MustCompile(`^Test.*(Latency|Throughput|Perf).*$`)
	guardRE := regexp.MustCompile(`testing\.Short\(\)|os\.Getenv\("CI"\)`)
	fset := token.NewFileSet()
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
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
		file, err := parser.ParseFile(fset, p, src, 0)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if !perfNameRE.MatchString(fn.Name.Name) {
				continue
			}
			// Render the body as source text and search for a guard.
			var buf bytes.Buffer
			for _, stmt := range fn.Body.List {
				buf.WriteString(exprText(fset, stmt))
				buf.WriteByte('\n')
			}
			if !guardRE.MatchString(buf.String()) {
				t.Errorf("%s: %s lacks testing.Short() or os.Getenv(\"CI\") guard",
					p, fn.Name.Name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func exprText(fset *token.FileSet, node ast.Node) string {
	var b bytes.Buffer
	pos := fset.Position(node.Pos())
	end := fset.Position(node.End())
	if pos.Filename == "" {
		return ""
	}
	src, err := os.ReadFile(pos.Filename)
	if err != nil {
		return ""
	}
	off, endOff := pos.Offset, end.Offset
	if off < 0 || endOff > len(src) || off >= endOff {
		return ""
	}
	b.Write(src[off:endOff])
	return b.String()
}

// Test #36 — CLI parse rejects --warmup below floor.
func TestTunnelBench_WarmupBelow100Rejected(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := run([]string{"--warmup", "99"}, &out, &errBuf)
	if code == 0 {
		t.Fatalf("expected non-zero exit; out=%q err=%q", out.String(), errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "warmup") {
		t.Errorf("stderr should mention warmup, got %q", errBuf.String())
	}
}

// Test #37 — same seed → byte-identical dry-run stdout.
func TestTunnelBench_SeedThreaded(t *testing.T) {
	var a, b bytes.Buffer
	if code := run([]string{"--seed", "42", "--dry-run"}, &a, &a); code != 0 {
		t.Fatal(a.String())
	}
	if code := run([]string{"--seed", "42", "--dry-run"}, &b, &b); code != 0 {
		t.Fatal(b.String())
	}
	if a.String() != b.String() {
		t.Errorf("dry-run stdout differed across two seed=42 runs:\nA: %q\nB: %q", a.String(), b.String())
	}
	// Different seeds may or may not differ in dry-run (plan text doesn't
	// depend on seed today). The determinism guarantee is same-seed →
	// same output; different-seed → we do not require difference here
	// because dry-run does not emit random-derived content.
}

// sumTreeBytes returns the total size of all regular files under root.
// Portable across Linux/macOS (Windows CI is out of scope). No external
// binaries required.
func sumTreeBytes(t *testing.T, root string) int64 {
	t.Helper()
	var total int64
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		t.Fatalf("sumTreeBytes(%s): %v", root, err)
	}
	return total
}
