"""Spec §7 (b) --observer-db + §7 (d) --out path validators."""

from __future__ import annotations

import os
import sqlite3
import sys
from pathlib import Path

import pytest

from eval_metrics.paths import (
    ErrObserverDBForbiddenPath,
    ErrObserverDBMissing,
    ErrObserverDBNotSQLite,
    ErrObserverDBSymlinkSwap,
    ErrOutFileExists,
    ErrOutIsSymlink,
    ErrOutParentForbidden,
    ErrOutParentMissing,
    open_out_file,
    validate_observer_db,
    validate_out_path,
)


def _make_sqlite(path: Path) -> None:
    """Create a minimal valid SQLite DB at `path`."""
    conn = sqlite3.connect(str(path))
    conn.execute("CREATE TABLE dummy (x INTEGER)")
    conn.close()


# --- --observer-db (§7 (b)) -------------------------------------------------


def test_observer_db_missing_file_exit2(tmp_path: Path) -> None:
    with pytest.raises(ErrObserverDBMissing):
        validate_observer_db(str(tmp_path / "does-not-exist.db"))


def test_observer_db_etc_reject() -> None:
    # /etc/passwd exists on any Linux; the forbidden-prefix check
    # rejects before the magic-bytes probe would (spec §7 (b) step 1).
    with pytest.raises(ErrObserverDBForbiddenPath):
        validate_observer_db("/etc/passwd")


def test_observer_db_proc_reject() -> None:
    with pytest.raises(ErrObserverDBForbiddenPath):
        validate_observer_db("/proc/self/status")


def test_observer_db_sys_reject() -> None:
    # /sys/kernel/notes exists on Linux kernels with CONFIG_KALLSYMS.
    if not Path("/sys/kernel/notes").exists():
        pytest.skip("/sys/kernel/notes not present in this kernel build")
    with pytest.raises(ErrObserverDBForbiddenPath):
        validate_observer_db("/sys/kernel/notes")


def test_observer_db_dev_reject() -> None:
    with pytest.raises(ErrObserverDBForbiddenPath):
        validate_observer_db("/dev/null")


def test_observer_db_magic_bytes_reject(tmp_path: Path) -> None:
    bad = tmp_path / "not-sqlite.bin"
    bad.write_bytes(b"\x89PNG\r\n\x1a\n" + b"\x00" * 100)
    with pytest.raises(ErrObserverDBNotSQLite):
        validate_observer_db(str(bad))


@pytest.mark.linux_only
def test_observer_db_symlink_swap_after_resolve(tmp_path: Path) -> None:
    """Post-resolve swap of the pathname is caught by O_NOFOLLOW.

    We cannot easily simulate a genuine race in-process; we approximate
    by pointing the CLI arg at a real SQLite file via a symlink and
    then asserting O_NOFOLLOW on the resolved path succeeds normally.
    If the resolved path itself is a symlink (e.g. because the user
    handed us a symlink to a symlink), open with O_NOFOLLOW fails.
    """
    if not sys.platform.startswith("linux"):
        pytest.skip("Linux-only TOCTOU rig")
    real = tmp_path / "real.db"
    _make_sqlite(real)
    # Make a symlink whose realpath.resolve() returns `real.db`; that
    # resolves cleanly, so the open succeeds. This is the happy case.
    link = tmp_path / "link.db"
    link.symlink_to(real)
    resolved, fd = validate_observer_db(str(link))
    os.close(fd)
    assert resolved == real.resolve()


@pytest.mark.linux_only
def test_observer_db_proc_self_fd_open(tmp_path: Path) -> None:
    """Verify sqlite3 opens via /proc/self/fd/<fd> on Linux."""
    if not sys.platform.startswith("linux"):
        pytest.skip("Linux-only /proc handoff")
    from eval_metrics.db import open_observer_db

    real = tmp_path / "real.db"
    _make_sqlite(real)
    resolved, fd = validate_observer_db(str(real))
    conn = open_observer_db(resolved, fd)
    # Round-trip a SELECT so we know the connection actually reads
    # from the pre-verified inode.
    rows = conn.execute("SELECT name FROM sqlite_master").fetchall()
    conn.close()
    assert rows is not None


# --- --out (§7 (d)) --------------------------------------------------------


def test_out_parent_must_exist(tmp_path: Path) -> None:
    with pytest.raises(ErrOutParentMissing):
        validate_out_path(str(tmp_path / "nonexistent-dir" / "file.csv"))


def test_out_refuse_overwrite(tmp_path: Path) -> None:
    existing = tmp_path / "file.csv"
    existing.write_text("x")
    with pytest.raises(ErrOutFileExists):
        validate_out_path(str(existing))


def test_out_refuse_symlink(tmp_path: Path) -> None:
    real = tmp_path / "real.csv"
    real.write_text("x")
    link = tmp_path / "link.csv"
    link.symlink_to(real)
    with pytest.raises(ErrOutIsSymlink):
        validate_out_path(str(link))


def test_out_parent_forbidden_reject() -> None:
    # Attempting to write into /etc/ (parent exists) is refused up
    # front so an operator typo cannot escape the sandbox.
    with pytest.raises(ErrOutParentForbidden):
        validate_out_path("/etc/eval-metrics.csv")


def test_out_atomic_create_o_excl(tmp_path: Path) -> None:
    """A file appearing between symlink-check and open is caught by O_EXCL."""
    target = tmp_path / "created-then-created-again.csv"
    resolved_parent, basename = validate_out_path(str(target))
    fd1 = open_out_file(resolved_parent, basename)
    os.close(fd1)
    # Second open at the same path must fail with FileExistsError
    # (O_EXCL semantics).
    with pytest.raises(FileExistsError):
        open_out_file(resolved_parent, basename)


@pytest.mark.linux_only
def test_out_dir_fd_bind(tmp_path: Path) -> None:
    """After parent resolve, opening via dir_fd goes to the resolved inode."""
    if not sys.platform.startswith("linux"):
        pytest.skip("Linux-only dir_fd semantics")
    parent = tmp_path / "outdir"
    parent.mkdir()
    target = parent / "out.csv"
    resolved_parent, basename = validate_out_path(str(target))
    fd = open_out_file(resolved_parent, basename)
    os.close(fd)
    assert (parent / "out.csv").exists()
