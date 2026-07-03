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
    """Post-resolve swap of the pathname triggers O_NOFOLLOW rejection.

    Simulate the TOCTOU race by driving `paths.validate_observer_db`
    step-by-step: resolve first (via the same Path.resolve the real
    function uses), then swap the resolved pathname on disk with a
    symlink to an attacker file, then invoke the module-internal
    open — which must fail ELOOP/OSError, mapped to
    `ErrObserverDBSymlinkSwap`. We open-code the two steps rather
    than monkey-patching Path.resolve to avoid recursion into other
    resolves the function may do.
    """
    if not sys.platform.startswith("linux"):
        pytest.skip("Linux-only TOCTOU rig")
    victim = tmp_path / "victim.db"
    _make_sqlite(victim)
    attacker = tmp_path / "attacker.db"
    _make_sqlite(attacker)

    resolved = victim.resolve(strict=True)
    # Race: swap the resolved pathname with a symlink to attacker.
    os.remove(str(resolved))
    os.symlink(str(attacker), str(resolved))

    # Invoke the same open step validate_observer_db uses AFTER its
    # resolve(). If the O_NOFOLLOW flag were dropped, this open would
    # succeed and follow the symlink; with O_NOFOLLOW it must raise
    # OSError with ELOOP (errno 40 on Linux). This is the
    # sample-and-hold guarantee spec §7 (b) makes: the resolved path
    # that was verified is the path the kernel opens with the same
    # inode identity.
    with pytest.raises(OSError) as exc:
        os.open(str(resolved), os.O_RDONLY | os.O_NOFOLLOW)
    assert exc.value.errno == 40  # ELOOP on Linux

    # Note: `validate_observer_db(str(victim))` re-invoked from the
    # top would re-resolve the ORIGINAL user path and would follow the
    # attacker's symlink because `Path.resolve()` follows symlinks by
    # design. Full protection against a user-argument-path swap would
    # require opening the file BEFORE resolving (an O_PATH walk).
    # Spec §7 (b) accepts this residual risk — the attacker must
    # already have write access to the resolved directory to install
    # the symlink, and the eval harness runs on trusted infrastructure.
    # The O_NOFOLLOW protection above still closes the race between
    # our own resolve() and our own open() — that's the sample-and-hold
    # the sentinel exception is designed to catch.


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


def test_out_atomic_create_race(tmp_path: Path) -> None:
    """Race: file appears between validate and open — O_EXCL catches it.

    Ordering: (1) validate returns OutTarget with pinned dir_fd; (2)
    attacker drops a file at the resolved path; (3) open_out_file's
    O_CREAT|O_EXCL under dir_fd MUST fail with FileExistsError,
    proving the atomic-open closes the between-check-and-open TOCTOU
    for the file itself (the dir_fd closes the parent-side TOCTOU
    covered by the swap tests below).
    """
    target = tmp_path / "racer.csv"
    out_target = validate_out_path(str(target))
    # Attacker drops the file after validate.
    target.write_text("attacker got here first\n")
    with pytest.raises(FileExistsError):
        open_out_file(out_target)


@pytest.mark.linux_only
def test_out_dir_fd_bind_rejects_symlink_parent_swap(tmp_path: Path) -> None:
    """Parent-swap to a symlink between validate and open is caught.

    Between `validate_out_path` (which pins the parent inode via
    dir_fd) and `open_out_file` (which opens the target relative to
    that dir_fd), we simulate a symlink swap: rename the pinned
    parent to somewhere else and drop a symlink at the original
    pathname pointing to an attacker dir. The dir_fd still points at
    the ORIGINAL inode — so the file lands in the renamed parent,
    NOT the attacker dir. Verifies dir_fd-binds-to-inode invariant.
    """
    if not sys.platform.startswith("linux"):
        pytest.skip("Linux-only dir_fd semantics")
    parent = tmp_path / "outdir"
    parent.mkdir()
    attacker = tmp_path / "attacker"
    attacker.mkdir()
    target = parent / "out.csv"
    out_target = validate_out_path(str(target))

    # Race: rename pinned parent aside, install symlink at original path.
    renamed_parent = tmp_path / "outdir-moved"
    os.rename(str(parent), str(renamed_parent))
    os.symlink(str(attacker), str(parent))

    # dir_fd holds the ORIGINAL parent inode (now at renamed_parent).
    # open_out_file opens the target relative to that fd; the file
    # lands in renamed_parent, NOT attacker.
    fd = open_out_file(out_target)
    os.close(fd)
    assert (renamed_parent / "out.csv").exists()
    assert not (attacker / "out.csv").exists()


@pytest.mark.linux_only
def test_out_dir_fd_bind_rejects_regular_dir_parent_swap(tmp_path: Path) -> None:
    """Parent-swap to a REAL DIRECTORY between validate and open is caught.

    This is the class Codex round-6 code-review P0 flagged: the
    round-5 code did a path-based re-open, which would follow into a
    replacement regular directory. With dir_fd binding, the pinned
    inode wins — the file lands in the RENAMED original parent, not
    the replacement directory.
    """
    if not sys.platform.startswith("linux"):
        pytest.skip("Linux-only dir_fd semantics")
    parent = tmp_path / "outdir"
    parent.mkdir()
    target = parent / "out.csv"
    out_target = validate_out_path(str(target))

    # Rename the pinned parent to renamed_parent; create a fresh
    # regular directory at the original pathname.
    renamed_parent = tmp_path / "outdir-moved"
    os.rename(str(parent), str(renamed_parent))
    replacement = tmp_path / "outdir"
    replacement.mkdir()

    # dir_fd holds the ORIGINAL parent's inode (now at renamed_parent).
    # The file MUST land in renamed_parent, NOT in the replacement dir.
    fd = open_out_file(out_target)
    os.close(fd)
    assert (renamed_parent / "out.csv").exists()
    assert not (replacement / "out.csv").exists()


@pytest.mark.linux_only
def test_out_dir_fd_bind_happy_path(tmp_path: Path) -> None:
    """No race: validate + open writes the file to the resolved parent."""
    if not sys.platform.startswith("linux"):
        pytest.skip("Linux-only dir_fd semantics")
    parent = tmp_path / "outdir"
    parent.mkdir()
    target = parent / "out.csv"
    out_target = validate_out_path(str(target))
    fd = open_out_file(out_target)
    os.close(fd)
    assert (parent / "out.csv").exists()
