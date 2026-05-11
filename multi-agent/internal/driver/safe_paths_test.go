package driver

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAssertWritableTarget_NormalPath(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.txt")
	if err := AssertWritableTarget(target, false); err != nil {
		t.Errorf("normal path rejected: %v", err)
	}
}

func TestAssertWritableTarget_MissingParent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "nope", "out.txt")
	if err := AssertWritableTarget(target, false); err == nil {
		t.Error("missing parent dir should be rejected")
	}
}

func TestAssertWritableTarget_SymlinkParent(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks unsupported on this fs")
	}
	target := filepath.Join(link, "out.txt")
	if err := AssertWritableTarget(target, false); err == nil {
		t.Error("symlink parent should be rejected")
	}
}

func TestAssertWritableTarget_LeafIsSymlink(t *testing.T) {
	dir := t.TempDir()
	leaf := filepath.Join(dir, "out.txt")
	target := filepath.Join(dir, "real.txt")
	os.WriteFile(target, []byte("x"), 0o644)
	if err := os.Symlink(target, leaf); err != nil {
		t.Skip("symlinks unsupported on this fs")
	}
	if err := AssertWritableTarget(leaf, false); err == nil {
		t.Error("symlink leaf should be rejected even when overwrite=false")
	}
}

func TestAssertWritableTarget_DisableUIDCheckSkipsUIDComparison(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.txt")
	if err := AssertWritableTarget(target, true); err != nil {
		t.Errorf("disable_uid_check should bypass uid check: %v", err)
	}
}

func TestAssertNoSymlinkLeaf_RejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.txt")
	os.WriteFile(target, []byte("x"), 0o644)
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unsupported on this fs")
	}
	if err := AssertNoSymlinkLeaf(link); err == nil {
		t.Error("symlink leaf should be rejected")
	}
}

func TestAssertNoSymlinkLeaf_AllowsRegularFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "real.txt")
	os.WriteFile(f, []byte("x"), 0o644)
	if err := AssertNoSymlinkLeaf(f); err != nil {
		t.Errorf("regular file rejected: %v", err)
	}
}

func TestAssertSafeRelPath_RejectsEscape(t *testing.T) {
	if err := AssertSafeRelPath("../../etc/passwd"); err == nil {
		t.Error("path escape should be rejected")
	}
	if err := AssertSafeRelPath("a/b/c"); err != nil {
		t.Errorf("clean relpath rejected: %v", err)
	}
	if err := AssertSafeRelPath("/abs/path"); err == nil {
		t.Error("absolute path should be rejected as relpath")
	}
}
