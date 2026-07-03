package harness

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ErrFixtureSymlinkEscapes is returned by SetupWorkspace when a symlink
// inside the fixtures tree resolves to a path outside the fixture root.
// Spec §7(e) requires that the copied fixture be self-contained so
// downstream reads (e.g. cloud upload) cannot escape via a `..`-shaped
// symlink target.
var ErrFixtureSymlinkEscapes = errors.New("harness: fixture symlink escapes source root")

// Workspace holds the per-run tempdir. Root is the value passed to the
// oracle as its $1 argument; SetupWorkspace also flattens
// fixtures/mock_workspace into Root so the layout matches what the
// maintainer sees when they invoke `./oracle.sh ./fixtures/mock_workspace`
// from a workload's own directory.
type Workspace struct {
	Root string
	keep bool
}

// SetupWorkspace creates `evalbaseline-*` tempdir with mode 0700, then:
//   - copies `fixturesDir` recursively into Root (Security §7(e))
//   - if `mock_workspace/` exists inside fixtures, flattens its contents
//     into Root as the "agent-produced output" placeholder used by the
//     dry-run branch of every baseline.
//
// `fixturesDir` = "" is allowed (theoretical workload with no fixtures)
// and yields an empty Root.
func SetupWorkspace(fixturesDir string, keep bool) (*Workspace, error) {
	root, err := os.MkdirTemp("", "evalbaseline-")
	if err != nil {
		return nil, fmt.Errorf("harness: mktemp: %w", err)
	}
	// Some unixes honour umask on MkdirTemp and produce 0755 despite
	// the API; reassert 0700 explicitly to close the multi-tenant leak
	// path Security §7(g) calls out.
	if err := os.Chmod(root, 0o700); err != nil {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("harness: chmod tempdir: %w", err)
	}

	ws := &Workspace{Root: root, keep: keep}
	if fixturesDir == "" {
		return ws, nil
	}
	absFixtures, err := filepath.Abs(fixturesDir)
	if err != nil {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("harness: abs fixtures dir: %w", err)
	}
	if err := copyTree(absFixtures, root); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	// Project mock_workspace up into Root for the dry-run branch. Real
	// baseline impls overwrite these files with their own agent output;
	// dry-run keeps them so the oracle can grade the maintainer's
	// mock artefacts verbatim.
	mock := filepath.Join(root, "mock_workspace")
	if info, err := os.Stat(mock); err == nil && info.IsDir() {
		if err := copyTree(mock, root); err != nil {
			_ = os.RemoveAll(root)
			return nil, err
		}
	}
	return ws, nil
}

// Cleanup deletes the tempdir unless the Workspace was created with
// keep=true. Callers should always defer this.
func (w *Workspace) Cleanup() {
	if w == nil || w.Root == "" {
		return
	}
	if w.keep {
		fmt.Fprintf(os.Stderr, "harness: --keep-tempdir set; workspace retained at %s\n", w.Root)
		return
	}
	_ = os.RemoveAll(w.Root)
}

// copyTree recursively copies srcRoot into dstRoot. Symlinks that
// resolve outside srcRoot are refused; in-tree symlinks are materialised
// as the underlying file (spec §7(e)) so a later baseline write can't
// follow a symlink out of the tempdir.
func copyTree(srcRoot, dstRoot string) error {
	absSrc, err := filepath.Abs(srcRoot)
	if err != nil {
		return fmt.Errorf("harness: abs src: %w", err)
	}
	return filepath.WalkDir(absSrc, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(absSrc, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		dst := filepath.Join(dstRoot, rel)

		// Type check via Lstat so symlinks are handled explicitly rather
		// than followed silently by filepath.WalkDir semantics.
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			// Resolve relative targets against the symlink's own dir.
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(path), target)
			}
			resolved, err := filepath.Abs(target)
			if err != nil {
				return err
			}
			if !strings.HasPrefix(resolved+string(os.PathSeparator), absSrc+string(os.PathSeparator)) && resolved != absSrc {
				return fmt.Errorf("%w: %s → %s", ErrFixtureSymlinkEscapes, path, resolved)
			}
			// Materialise the pointed-at file as a plain copy.
			return copyFileByPath(resolved, dst)
		case info.IsDir():
			return os.MkdirAll(dst, 0o700)
		default:
			return copyFileByMode(path, dst, info.Mode())
		}
	})
}

func copyFileByPath(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return os.MkdirAll(dst, 0o700)
	}
	return copyFileByMode(src, dst, info.Mode())
}

func copyFileByMode(src, dst string, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	// Force 0600 base perms (matches tempdir 0700 policy) then restore
	// executable bit if source had it (oracles are 0755 shell scripts).
	perm := fs.FileMode(0o600)
	if mode&0o111 != 0 {
		perm = 0o700
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
