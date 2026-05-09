package driver

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// AssertWritableTarget enforces v1 write-safety rules from spec §5:
//  1. The parent directory must exist and not be a symlink.
//  2. (Unless disableUIDCheck) the parent's owner uid must equal os.Getuid().
//  3. If the leaf exists, it must not be a symlink (we never overwrite a symlink).
//
// Returns a user-facing error suitable for surfacing through MCP.
func AssertWritableTarget(absPath string, disableUIDCheck bool) error {
	if !filepath.IsAbs(absPath) {
		return fmt.Errorf("write path must be absolute: %s", absPath)
	}
	parent := filepath.Dir(absPath)
	pinfo, err := os.Lstat(parent)
	if err != nil {
		return fmt.Errorf("parent dir not accessible: %s: %w", parent, err)
	}
	if pinfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("parent dir is a symlink: %s", parent)
	}
	if !pinfo.IsDir() {
		return fmt.Errorf("parent is not a directory: %s", parent)
	}
	if !disableUIDCheck {
		if st, ok := pinfo.Sys().(*syscall.Stat_t); ok {
			if int(st.Uid) != os.Getuid() {
				return fmt.Errorf("parent dir uid %d != driver uid %d: %s",
					st.Uid, os.Getuid(), parent)
			}
		}
	}
	if linfo, err := os.Lstat(absPath); err == nil {
		if linfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("write target is a symlink (refusing to overwrite): %s", absPath)
		}
	}
	return nil
}

// AssertNoSymlinkLeaf rejects paths whose leaf is itself a symlink.
// Used by RegisterFile when the user passes a read path.
func AssertNoSymlinkLeaf(absPath string) error {
	info, err := os.Lstat(absPath)
	if err != nil {
		return fmt.Errorf("stat %s: %w", absPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("symlinks not allowed: %s", absPath)
	}
	return nil
}

// AssertSafeRelPath rejects paths that are absolute or escape their root via "..".
// Used by /files/dir/{token}/blob to validate the ?path= query param.
func AssertSafeRelPath(rel string) error {
	if rel == "" {
		return fmt.Errorf("empty rel path")
	}
	if filepath.IsAbs(rel) {
		return fmt.Errorf("rel path must not be absolute: %s", rel)
	}
	cleaned := filepath.Clean(rel)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return fmt.Errorf("rel path escapes root: %s", rel)
	}
	return nil
}
