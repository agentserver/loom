//go:build windows

package driver

import (
	"fmt"
	"os"
	"path/filepath"
)

// AssertWritableTarget — Windows variant. Skips the uid check entirely
// (Windows doesn't have POSIX uids on plain Stat_t). Other guards remain.
func AssertWritableTarget(absPath string, _ bool) error {
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
	if linfo, err := os.Lstat(absPath); err == nil {
		if linfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("write target is a symlink (refusing to overwrite): %s", absPath)
		}
	}
	return nil
}
