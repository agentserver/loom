"""Path validators for --observer-db (§7 (b)) and --out (§7 (d)).

Two separate policies:

- `--observer-db` is a **read** target: it must exist. We resolve the
  path (strict=True), reject `/etc/` `/proc/` `/sys/` `/dev/` subtree
  descendants, then open with `os.open(..., O_NOFOLLOW)` and carry the
  fd forward. On Linux we hand the fd to sqlite3 via
  `/proc/self/fd/<fd>` so any post-verify rename/symlink swap of the
  pathname cannot re-target the connection. On other platforms we fall
  back to a plain-path open with a documented residual TOCTOU risk
  (spec §7 (b) rationale).

- `--out` is a **write** target: its PARENT must exist (we do NOT
  mkdir -p) and the target basename must not exist (`O_EXCL`) and
  must not be a symlink (`O_NOFOLLOW`). We open relative to a dir_fd
  pinned on the resolved parent to close the parent-side TOCTOU too.

Every rejection returns via a dedicated sentinel `PathValidationError`
subclass so the CLI in `cli.py` can pick the exit code (2 for validation,
3 for I/O) and the message the operator sees.
"""

from __future__ import annotations

import os
import stat
import sys
from pathlib import Path

# The four forbidden subtree prefixes for both --observer-db and --out
# (spec §7 (b), reused by §7 (d) with the same rationale). We compare
# against the resolved path's byte-prefix; a prefix that starts with the
# resolved value and is followed by `/` or the end of string is a
# subtree descendant.
_FORBIDDEN_PREFIXES: tuple[str, ...] = ("/etc/", "/proc/", "/sys/", "/dev/")


# SQLite file header magic bytes; verified before sqlite3 is allowed to
# touch the file (spec §7 (b) step 2). The trailing NUL is part of the
# 16-byte header per https://www.sqlite.org/fileformat.html §1.3.
SQLITE_MAGIC: bytes = b"SQLite format 3\x00"


class PathValidationError(Exception):
    """Base class for --observer-db and --out validation failures.

    The CLI maps any subclass to exit-code 2 with the exception message
    on stderr. Subclasses carry a stable name (mostly for tests) so
    behavior can be asserted without matching on prose.
    """

    #: Exit code this error should map to at the CLI boundary.
    exit_code: int = 2


class ErrObserverDBMissing(PathValidationError):
    """--observer-db points at a nonexistent file."""


class ErrObserverDBForbiddenPath(PathValidationError):
    """--observer-db resolves under /etc/ /proc/ /sys/ /dev/."""


class ErrObserverDBNotSQLite(PathValidationError):
    """--observer-db points at a file whose magic bytes are not SQLite's."""


class ErrObserverDBSymlinkSwap(PathValidationError):
    """A symlink appeared at the resolved path between resolve and open."""


class ErrOutParentMissing(PathValidationError):
    """--out's parent directory does not exist (we do NOT mkdir -p)."""


class ErrOutParentForbidden(PathValidationError):
    """--out's parent resolves under /etc/ /proc/ /sys/ /dev/."""


class ErrOutFileExists(PathValidationError):
    """--out already exists; refuse to overwrite (no --force)."""


class ErrOutIsSymlink(PathValidationError):
    """--out is a symlink (dangling or otherwise)."""


def _is_forbidden(resolved_str: str) -> bool:
    """Return True iff `resolved_str` descends from a forbidden prefix.

    A path is "under" a forbidden prefix if it either equals the prefix
    (rare — `/etc` itself) or starts with the prefix. We normalise the
    trailing-slash form so both `/etc` and `/etc/passwd` are rejected.
    """
    for prefix in _FORBIDDEN_PREFIXES:
        # Strip the trailing '/' from the prefix for equality comparison
        # with a resolved path that has no trailing slash.
        if resolved_str == prefix.rstrip("/") or resolved_str.startswith(prefix):
            return True
    return False


def validate_observer_db(user_path: str) -> tuple[Path, int]:
    """Validate `--observer-db` and return (resolved_path, opened_fd).

    Steps:
      1. resolve(strict=True) — non-existence → ErrObserverDBMissing.
         Reject if the resolved path is inside a forbidden subtree.
      2. Magic-bytes probe via `os.open(..., O_RDONLY | O_NOFOLLOW)`;
         O_NOFOLLOW closes the swap-symlink-at-final-component race.
         Read 16 bytes and compare to SQLITE_MAGIC.
      3. Return the fd for step 3 (TOCTOU handoff to sqlite3 in db.py).

    The caller MUST close the returned fd (or `open_observer_db`
    below wraps this and hands it to sqlite3).
    """
    p = Path(user_path).expanduser()
    try:
        resolved = p.resolve(strict=True)
    except FileNotFoundError as e:
        raise ErrObserverDBMissing(f"--observer-db not found: {user_path}") from e

    if _is_forbidden(str(resolved)):
        raise ErrObserverDBForbiddenPath(
            f"--observer-db resolves under a forbidden subtree (/etc /proc /sys /dev): {resolved}"
        )

    # O_NOFOLLOW on the final path component: if the resolved pathname
    # was replaced by a symlink between resolve() and here, open fails
    # with ELOOP and we translate that to ErrObserverDBSymlinkSwap.
    try:
        fd = os.open(str(resolved), os.O_RDONLY | os.O_NOFOLLOW)
    except OSError as e:
        if e.errno in (40, 62):  # ELOOP on Linux / macOS respectively
            raise ErrObserverDBSymlinkSwap(
                f"--observer-db appears as symlink after resolve (TOCTOU): {resolved}"
            ) from e
        # Any other OSError: bubble as generic PathValidationError so
        # the CLI exits 2 with a clean message.
        raise PathValidationError(
            f"--observer-db could not be opened: {resolved}: {e}"
        ) from e

    # Magic-bytes probe (spec §7 (b) step 2). We use os.read to avoid
    # buffering; 16 bytes fits well under any Linux/macOS default page.
    try:
        head = os.read(fd, 16)
    except OSError as e:
        os.close(fd)
        raise PathValidationError(f"--observer-db read failed: {resolved}: {e}") from e

    if head != SQLITE_MAGIC:
        os.close(fd)
        raise ErrObserverDBNotSQLite(
            f"--observer-db is not a SQLite database (bad magic bytes): {resolved}"
        )

    # Fd position is now at byte 16; sqlite3 does not care because it
    # will open a NEW handle via /proc/self/fd/<fd> (Linux) which
    # dups the fd — starting position is not shared. On non-Linux we
    # close this fd in db.py and open by pathname.
    return resolved, fd


def validate_out_path(user_path: str) -> tuple[Path, Path]:
    """Validate --out and return (resolved_parent, target_basename).

    Steps:
      1. Resolve the PARENT with strict=True (parent must exist; do
         NOT mkdir -p). Reject if resolved parent is in a forbidden
         subtree.
      2. If the target exists as a symlink → ErrOutIsSymlink.
      3. If the target exists as a regular file / dir → ErrOutFileExists.

    The atomic `O_CREAT | O_EXCL | O_NOFOLLOW` open happens in
    `open_out_file` below (spec §7 (d) step 4), which pins the open on
    a dir_fd of `resolved_parent` to close the parent-side TOCTOU.
    """
    p = Path(user_path).expanduser()
    parent = p.parent if str(p.parent) else Path(".")
    try:
        resolved_parent = parent.resolve(strict=True)
    except FileNotFoundError as e:
        raise ErrOutParentMissing(
            f"--out parent directory does not exist (no mkdir -p): {parent}"
        ) from e

    if _is_forbidden(str(resolved_parent)):
        raise ErrOutParentForbidden(
            f"--out parent resolves under a forbidden subtree: {resolved_parent}"
        )

    # The target basename may or may not exist yet. If it exists,
    # decide which rejection applies. lstat, NOT stat: we want to see
    # a symlink as-is, not follow it.
    target = resolved_parent / p.name
    try:
        st = os.lstat(target)
    except FileNotFoundError:
        # Ideal case: target does not exist, open_out_file's O_EXCL
        # will atomically create it.
        return resolved_parent, Path(p.name)

    if stat.S_ISLNK(st.st_mode):
        raise ErrOutIsSymlink(f"--out is a symlink; refusing to write: {target}")
    # Any other kind of existing entry (regular file, dir, socket, …)
    # is refused so we do not clobber data.
    raise ErrOutFileExists(f"--out already exists (no --force): {target}")


def open_out_file(resolved_parent: Path, basename: Path) -> int:
    """Atomically create the --out target under a dir_fd-anchored open.

    See spec §7 (d) step 4: opening the target relative to a dir_fd
    that already points at the resolved parent's inode closes the
    parent-side TOCTOU that a plain path-based `open()` leaves gaping.

    Returns an fd opened O_WRONLY; caller is responsible for
    fdopen'ing to a text stream if writing CSV/JSON, and for closing.

    On non-Linux platforms without `O_DIRECTORY`/dir_fd semantics
    (mostly Windows) we fall back to a plain path-based
    O_CREAT|O_EXCL|O_NOFOLLOW open. The eval pipeline runs on Linux so
    the fallback is documented, not tested, per spec §7 (d).
    """
    if not hasattr(os, "O_DIRECTORY") or sys.platform == "win32":  # pragma: no cover — non-Linux fallback
        target_str = str(resolved_parent / basename)
        return os.open(
            target_str,
            os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW,
            0o600,
        )

    dir_fd = os.open(str(resolved_parent), os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        return os.open(
            str(basename),
            os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW,
            0o600,
            dir_fd=dir_fd,
        )
    finally:
        os.close(dir_fd)
