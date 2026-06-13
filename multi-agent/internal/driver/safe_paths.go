package driver

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolveExistingPrefix returns filepath.EvalSymlinks(p) if p exists.
// For not-yet-existing leaf paths, it evaluates the longest existing
// prefix and rejoins the non-existent tail unchanged. Lets jail checks
// reason about would-be-written files.
func resolveExistingPrefix(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	cur := abs
	var tail []string
	for {
		real, err := filepath.EvalSymlinks(cur)
		if err == nil {
			if len(tail) == 0 {
				return real, nil
			}
			parts := append([]string{real}, reverseStrings(tail)...)
			return filepath.Join(parts...), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent, leaf := filepath.Split(cur)
		parent = filepath.Clean(parent)
		if parent == cur {
			return abs, nil
		}
		tail = append(tail, leaf)
		cur = parent
	}
}

func reverseStrings(s []string) []string {
	out := make([]string, len(s))
	for i, v := range s {
		out[len(s)-1-i] = v
	}
	return out
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
