package main

import (
	"crypto/sha256"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestSetupWorkspace_Perm0700 — Security §7(g). The tempdir must be
// readable only by its owner so co-tenants on a shared eval host can't read
// stub tokens / model prompts that land inside.
func TestSetupWorkspace_Perm0700(t *testing.T) {
	t.Parallel()
	ws, err := SetupWorkspace("", false)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer ws.Cleanup()
	info, err := os.Stat(ws.Root)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("perm = %o, want 0700", info.Mode().Perm())
	}
}

// TestSetupWorkspace_CleanupRemoves — the default (keep=false) path; after
// Cleanup the directory must not exist on disk.
func TestSetupWorkspace_CleanupRemoves(t *testing.T) {
	t.Parallel()
	ws, err := SetupWorkspace("", false)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	path := ws.Root
	ws.Cleanup()
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("path %s still exists after cleanup (err=%v)", path, err)
	}
}

// TestSetupWorkspace_KeepRetains — the --keep-tempdir path; after Cleanup
// the directory persists for operator inspection. (The stderr log line is
// covered by integration tests; here we just verify the file system state.)
func TestSetupWorkspace_KeepRetains(t *testing.T) {
	t.Parallel()
	ws, err := SetupWorkspace("", true)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	path := ws.Root
	t.Cleanup(func() { _ = os.RemoveAll(path) }) // manual cleanup for the test
	ws.Cleanup()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("path %s missing after keep-tempdir cleanup: %v", path, err)
	}
}

// TestFixturesCopiedToTempdir_NotInPlace — Security §7(b). The source
// fixtures directory must be byte-identical before and after a setup;
// the copy must land in the tempdir.
func TestFixturesCopiedToTempdir_NotInPlace(t *testing.T) {
	t.Parallel()
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "mock_workspace", "patch.diff"), "diff content")
	writeFile(t, filepath.Join(src, "repo", "README.md"), "readme")

	before := hashTree(t, src)
	ws, err := SetupWorkspace(src, false)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	t.Cleanup(ws.Cleanup)

	after := hashTree(t, src)
	if before != after {
		t.Errorf("source tree mutated by setup: before=%s after=%s", before, after)
	}

	// fixtures dir is copied verbatim AND mock_workspace contents are
	// flattened up into ws.Root for oracle-friendly cwd.
	if !fileExists(t, filepath.Join(ws.Root, "mock_workspace", "patch.diff")) {
		t.Errorf("fixtures sub-tree not copied")
	}
	if !fileExists(t, filepath.Join(ws.Root, "patch.diff")) {
		t.Errorf("mock_workspace contents not projected into ws.Root")
	}
	if !fileExists(t, filepath.Join(ws.Root, "repo", "README.md")) {
		t.Errorf("repo/ not copied")
	}
}

// TestCopyTree_SymlinkEscape_Rejected — a fixture tree containing a
// symlink pointing at /etc/passwd must be refused with
// ErrFixtureSymlinkEscapes.
func TestCopyTree_SymlinkEscape_Rejected(t *testing.T) {
	t.Parallel()
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "ok.txt"), "fine")
	if err := os.Symlink("/etc/passwd", filepath.Join(src, "evil-link")); err != nil {
		t.Skipf("cannot create symlink in test env: %v", err)
	}
	_, err := SetupWorkspace(src, false)
	if !errors.Is(err, ErrFixtureSymlinkEscapes) {
		t.Fatalf("err = %v, want ErrFixtureSymlinkEscapes", err)
	}
}

// TestSubstituteWorkspace_TokenReplaced — the ${workspace} token in
// spec.outputs.write_targets is replaced with the actual tempdir; paths
// without the token pass through unchanged.
func TestSubstituteWorkspace_TokenReplaced(t *testing.T) {
	t.Parallel()
	got := SubstituteWorkspace("${workspace}/patch.diff", "/tmp/evalrun-x")
	if got != "/tmp/evalrun-x/patch.diff" {
		t.Errorf("substitute(`${workspace}/patch.diff`, /tmp/evalrun-x) = %q", got)
	}
	if got := SubstituteWorkspace("fixtures/repo", "/tmp/x"); got != "fixtures/repo" {
		t.Errorf("plain path mutated: %q", got)
	}
}

// --- helpers ---

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func fileExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}

func hashTree(t *testing.T, root string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, _ := filepath.Rel(root, p)
		h.Write([]byte(rel))
		h.Write([]byte{0})
		if d.IsDir() {
			h.Write([]byte("DIR\n"))
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		h.Write(b)
		return nil
	})
	if err != nil {
		t.Fatalf("hash walk: %v", err)
	}
	return string(h.Sum(nil))
}
