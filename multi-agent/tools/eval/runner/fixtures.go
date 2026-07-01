package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ErrFixtureSymlinkEscapes is returned by CopyFixturesToTempdir when a
// symlink in the source tree resolves to a target outside the source. The
// runner refuses such trees — Security §7(b) requires the copied fixture be
// self-contained.
var ErrFixtureSymlinkEscapes = errors.New("eval-runner: fixture symlink escapes source root")

// SetupWorkspace prepares the per-run tempdir:
//   - creates a tempdir with mode 0700 (Security §7(g))
//   - copies the workload's fixtures/ tree into it (Security §7(b))
//
// The returned path is the runner's "workspace" — the directory passed to
// oracle.sh as $1 and substituted for ${workspace} in spec.outputs paths.
// The caller is responsible for calling Cleanup on this struct after.
type Workspace struct {
	Root string // root tempdir (also the oracle's $1 argument)
	keep bool
}

// SetupWorkspace creates an evalrun-NNNN tempdir, chmod 0700, and copies
// the workload's `fixtures/` subtree into it. If `mock_workspace` exists
// inside fixtures, its contents are also flattened into Root so the oracle
// — which is invoked with workspace = Root — sees the same layout as
// `bash oracle.sh ./fixtures/mock_workspace` from the workload's own dir.
//
// Returning a *Workspace rather than a path lets the caller invoke
// `defer ws.Cleanup()` for guaranteed teardown even on error paths.
func SetupWorkspace(fixturesDir string, keep bool) (*Workspace, error) {
	root, err := os.MkdirTemp("", "evalrun-")
	if err != nil {
		return nil, fmt.Errorf("eval-runner: mktemp: %w", err)
	}
	// Some platforms honour umask on MkdirTemp; reassert 0700.
	if err := os.Chmod(root, 0o700); err != nil {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("eval-runner: chmod tempdir: %w", err)
	}

	ws := &Workspace{Root: root, keep: keep}

	if fixturesDir == "" {
		// Workload with no fixtures (theoretical) — Workspace is the
		// empty Root.
		return ws, nil
	}

	if err := copyTree(fixturesDir, root); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}

	// If `mock_workspace/` is present in fixtures, project its contents up
	// into Root so the oracle (invoked with workspace=Root) sees the
	// maintainer self-check artefacts unmodified. This is the skeleton's
	// "agent output" placeholder per spec §1.
	mock := filepath.Join(root, "mock_workspace")
	if info, err := os.Stat(mock); err == nil && info.IsDir() {
		if err := copyTree(mock, root); err != nil {
			_ = os.RemoveAll(root)
			return nil, err
		}
	}

	return ws, nil
}

// Cleanup removes the tempdir unless --keep-tempdir was set. Logs to
// stderr when the path is retained so operators can find it.
func (w *Workspace) Cleanup() {
	if w == nil || w.Root == "" {
		return
	}
	if w.keep {
		fmt.Fprintf(os.Stderr, "eval-runner: --keep-tempdir set; workspace at %s\n", w.Root)
		return
	}
	_ = os.RemoveAll(w.Root)
}

// SubstituteWorkspace rewrites a workload artifact path template by
// replacing the `${workspace}` token with the actual workspace root.
//
// Paths that don't contain the token are returned verbatim — they are
// workload-relative read inputs, not write targets.
func SubstituteWorkspace(template, workspace string) string {
	return strings.ReplaceAll(template, "${workspace}", workspace)
}

// copyTree recursively copies srcDir contents into dstDir. Regular files
// preserve mode bits; directories are created 0700; symlinks are
// materialised as their target file IFF the target stays inside srcDir.
// Symlinks pointing outside srcDir cause ErrFixtureSymlinkEscapes.
func copyTree(srcDir, dstDir string) error {
	srcAbs, err := filepath.Abs(srcDir)
	if err != nil {
		return fmt.Errorf("abs(src): %w", err)
	}
	return filepath.WalkDir(srcAbs, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(srcAbs, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		dst := filepath.Join(dstDir, rel)

		switch {
		case d.IsDir():
			if err := os.MkdirAll(dst, 0o700); err != nil {
				return err
			}
			return nil
		case d.Type()&os.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			// Resolve relative to symlink's directory.
			resolved := target
			if !filepath.IsAbs(resolved) {
				resolved = filepath.Join(filepath.Dir(p), resolved)
			}
			resolved, err = filepath.Abs(filepath.Clean(resolved))
			if err != nil {
				return err
			}
			if !strings.HasPrefix(resolved+string(filepath.Separator), srcAbs+string(filepath.Separator)) && resolved != srcAbs {
				return fmt.Errorf("%w: %s → %s", ErrFixtureSymlinkEscapes, p, resolved)
			}
			// Refuse symlinks-to-directories: copyFile would silently
			// produce a 0-byte file at dst instead of a recursive copy
			// (PR #53 review P2). The runner refuses rather than
			// recursing so the failure mode is loud, not a corrupt
			// fixture tree.
			info, err := os.Stat(resolved)
			if err != nil {
				return err
			}
			if info.IsDir() {
				return fmt.Errorf("%w: %s → %s (directory; refusing to materialise)", ErrFixtureSymlinkEscapes, p, resolved)
			}
			// Materialise — copy the resolved file, not the symlink.
			return copyFile(resolved, dst)
		default:
			return copyFile(p, dst)
		}
	})
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if info.IsDir() {
		return os.MkdirAll(dst, 0o700)
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
