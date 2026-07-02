"""Read-only SQLite open helper + companion-table helpers.

`open_observer_db` wraps the fd from `paths.validate_observer_db` and
hands it to sqlite3 via `/proc/self/fd/<fd>` on Linux, closing the
post-verify symlink-swap TOCTOU that a plain-pathname open leaves
open. On non-Linux we close the fd and re-open by pathname (spec §7
(b) accepted trade-off).

Immediately after connect we issue `PRAGMA query_only=ON;` as a belt
against a future change that inverts the URI accidentally.

`companion_table_status` distinguishes three states for downstream
metrics: MISSING (table not in schema — operational error; treat
metric as `upstream data missing`), EMPTY (table exists, 0 rows —
treat metric as `denominator zero`), and PRESENT (has rows).
"""

from __future__ import annotations

import os
import sqlite3
import sys
from enum import Enum
from pathlib import Path


class TableStatus(Enum):
    """Three-way status for a companion table (see module docstring)."""

    MISSING = "missing"   # table not in sqlite_master
    EMPTY = "empty"       # table exists, SELECT count(*) is 0
    PRESENT = "present"   # table exists, at least one row


def open_observer_db(resolved_path: Path, fd: int) -> sqlite3.Connection:
    """Open a read-only sqlite3 connection to the pre-verified file.

    On Linux: connects via `/proc/self/fd/<fd>` — sqlite3 dups the fd
    so the caller does not need to close it separately (we still close
    the original because sqlite3 dup'd; keeping it open leaks). The
    inode identity is fixed at the time `paths.validate_observer_db`
    opened it, so any subsequent path swap cannot re-target.

    On non-Linux: closes the fd and opens by pathname; accepts the
    residual TOCTOU risk per spec §7 (b).
    """
    if sys.platform.startswith("linux"):
        proc_fd = f"file:/proc/self/fd/{fd}?mode=ro&immutable=1"
        conn = sqlite3.connect(proc_fd, uri=True)
        # sqlite3 duplicates the underlying fd via the URI open; the
        # original fd is redundant now.
        os.close(fd)
    else:  # pragma: no cover — Linux is the only supported eval target
        os.close(fd)
        uri = f"file:{resolved_path}?mode=ro&immutable=1"
        conn = sqlite3.connect(uri, uri=True)

    # Belt: PRAGMA query_only forces read-only at the query planner
    # too, even if a future change reintroduces a writable open by
    # accident. UPDATE / INSERT / DELETE from now on raise
    # OperationalError with "attempt to write a readonly database".
    conn.execute("PRAGMA query_only = ON")
    # Verify the PRAGMA took — a compile-time typo or a future SQLite
    # build that renamed the PRAGMA would silently leave the DB
    # writable. Codex round-6 code-review P0 tripwire.
    (v,) = conn.execute("PRAGMA query_only").fetchone()
    if int(v) != 1:
        conn.close()
        raise RuntimeError(
            f"PRAGMA query_only did not take (got {v!r}); refusing to return a writable connection"
        )

    # Return dictionary-like rows so downstream metric code can look
    # up columns by name (schema-drift resilience).
    conn.row_factory = sqlite3.Row
    return conn


def companion_table_status(conn: sqlite3.Connection, table_name: str) -> TableStatus:
    """Return the three-way status of `table_name` (spec §4.4 branch).

    Uses `sqlite_master` to detect presence rather than catching a
    "no such table" OperationalError — the latter is fragile against
    localised sqlite builds that phrase the error differently.
    """
    row = conn.execute(
        "SELECT name FROM sqlite_master WHERE type='table' AND name=?",
        (table_name,),
    ).fetchone()
    if row is None:
        return TableStatus.MISSING

    # Table exists; check row count. `count(*)` is cheap on a small
    # observer DB and correct regardless of column layout.
    (n,) = conn.execute(f"SELECT count(*) FROM {table_name}").fetchone()
    return TableStatus.EMPTY if n == 0 else TableStatus.PRESENT


def runs_column_present(conn: sqlite3.Connection, column_name: str) -> bool:
    """Return True iff `runs` has `column_name` per PRAGMA table_info.

    Used by spec §2.1 rows #7 / #8 and §2.3 rows #40 / #41 to decide
    whether to compute a value from a not-yet-landed column or emit
    `null` + upstream-missing.
    """
    rows = conn.execute("PRAGMA table_info(runs)").fetchall()
    return any(r["name"] == column_name for r in rows)
