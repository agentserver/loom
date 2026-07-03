package harness

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestSetupWorkspace_Mode0700 — plan #11. Multi-tenant boxes must not
// let another local user read the workspace mid-run.
func TestSetupWorkspace_Mode0700(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "x.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := SetupWorkspace(src, false)
	if err != nil {
		t.Fatalf("SetupWorkspace: %v", err)
	}
	defer ws.Cleanup()
	info, err := os.Stat(ws.Root)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("tempdir mode: got %o, want 0700", got)
	}
}

// TestSetupWorkspace_RejectsSymlinkEscape — plan #12. A symlink to
// /etc/passwd inside fixtures must halt the copy, not be materialised.
func TestSetupWorkspace_RejectsSymlinkEscape(t *testing.T) {
	src := t.TempDir()
	if err := os.Symlink("/etc/passwd", filepath.Join(src, "leak")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	_, err := SetupWorkspace(src, false)
	if !errors.Is(err, ErrFixtureSymlinkEscapes) {
		t.Fatalf("want ErrFixtureSymlinkEscapes, got %v", err)
	}
}

// TestSetupWorkspace_CleanupRemovesTempdir — plan #13.
func TestSetupWorkspace_CleanupRemovesTempdir(t *testing.T) {
	src := t.TempDir()
	ws, err := SetupWorkspace(src, false)
	if err != nil {
		t.Fatal(err)
	}
	root := ws.Root
	ws.Cleanup()
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("expected tempdir removed, stat err = %v", err)
	}
}

// TestSetupWorkspace_KeepRetainsTempdir — companion to plan #13; when
// --keep-tempdir is set the workspace must survive Cleanup.
func TestSetupWorkspace_KeepRetainsTempdir(t *testing.T) {
	src := t.TempDir()
	ws, err := SetupWorkspace(src, true)
	if err != nil {
		t.Fatal(err)
	}
	root := ws.Root
	ws.Cleanup()
	defer os.RemoveAll(root)
	if _, err := os.Stat(root); err != nil {
		t.Errorf("expected tempdir retained, stat err = %v", err)
	}
}

// TestSetupWorkspace_ProjectsMockWorkspace — mock_workspace/ contents
// are flattened into Root so the oracle sees the maintainer artefacts
// verbatim. This underpins the dry-run branch across all baselines.
func TestSetupWorkspace_ProjectsMockWorkspace(t *testing.T) {
	src := t.TempDir()
	mock := filepath.Join(src, "mock_workspace")
	if err := os.MkdirAll(mock, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mock, "patch.diff"), []byte("--- a\n+++ b\n@@ -1 +1 @@\n-x\n+y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := SetupWorkspace(src, false)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Cleanup()
	if _, err := os.Stat(filepath.Join(ws.Root, "patch.diff")); err != nil {
		t.Errorf("mock_workspace not projected into Root: %v", err)
	}
}

// TestSetupWorkspace_DoesNotMutateSourceFixtures — plan #13b. Spec
// §7(e): fixture tree stays read-only from the harness's perspective.
// SHA256 the source dir before + after a full round-trip (harness copies
// fixtures → stub impl writes new files into the workspace → cleanup)
// and assert the source SHA map is unchanged.
func TestSetupWorkspace_DoesNotMutateSourceFixtures(t *testing.T) {
	src := t.TempDir()
	// Realistic layout: nested dir + files with different exec bits.
	if err := os.WriteFile(filepath.Join(src, "input.txt"), []byte("data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "oracle.sh"), []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	before := hashTree(t, src)

	ws, err := SetupWorkspace(src, false)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an impl mutating the workspace: write a new file, delete
	// a copied file, chmod another. None of this should reach `src`.
	if err := os.WriteFile(filepath.Join(ws.Root, "impl_wrote_this.txt"), []byte("mutated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(ws.Root, "input.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(ws.Root, "sub", "oracle.sh"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws.Cleanup()

	after := hashTree(t, src)
	if len(before) != len(after) {
		t.Fatalf("source tree file count changed: before=%d after=%d", len(before), len(after))
	}
	for k, v := range before {
		if after[k] != v {
			t.Errorf("source file %q mutated: before=%s after=%s", k, v, after[k])
		}
	}
}

func hashTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		rel, _ := filepath.Rel(root, path)
		info, _ := d.Info()
		out[rel] = fmt.Sprintf("%x:%o", sum, info.Mode().Perm())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestSetupWorkspace_PreservesExecBit — oracle.sh copies must remain
// executable.
func TestSetupWorkspace_PreservesExecBit(t *testing.T) {
	src := t.TempDir()
	script := filepath.Join(src, "oracle.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ws, err := SetupWorkspace(src, false)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Cleanup()
	info, err := os.Stat(filepath.Join(ws.Root, "oracle.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("oracle.sh lost exec bit; mode=%o", info.Mode())
	}
}
