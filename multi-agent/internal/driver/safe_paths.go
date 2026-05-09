package driver

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

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
