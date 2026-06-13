package driver

import (
	"errors"
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

// AssertReadableSource enforces that a driver-local source_path resolves
// (after symlinks) inside workDir or one of allowedRoots. LLM-supplied
// source_paths can otherwise turn write_slave_file into a "read any driver
// file → push to any slave" channel.
// Fixes §1.4 #17 of docs/review-2026-06-13.md.
func AssertReadableSource(p, workDir string, allowedRoots []string) error {
	if p == "" {
		return errors.New("source_path must not be empty")
	}
	real, err := resolveExistingPrefix(p)
	if err != nil {
		return fmt.Errorf("resolve source_path %s: %w", p, err)
	}
	for _, root := range append([]string{workDir}, allowedRoots...) {
		if root == "" {
			continue
		}
		rootAbs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		// Resolve symlinks on the root too so /var/run vs /run etc. align.
		if resolved, err := filepath.EvalSymlinks(rootAbs); err == nil {
			rootAbs = resolved
		}
		rel, err := filepath.Rel(rootAbs, real)
		if err != nil {
			continue
		}
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return nil
		}
	}
	return fmt.Errorf("source_path %s outside driver workdir and allowed roots", p)
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
