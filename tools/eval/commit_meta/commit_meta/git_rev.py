"""Thin wrapper around ``git rev-parse`` to grab a short SHA + branch + state.

Reading .git/HEAD by hand looks tempting but breaks in three real cases the
eval harness will hit: linked worktrees (HEAD is a relative ref to the main
gitdir), packed-refs (no loose ref file), and detached HEAD on a commit that
hasn't been written as a ref yet. Subprocess to git handles all three.
"""

from __future__ import annotations

import subprocess
from typing import Optional


def _run_git(cwd: str, *args: str) -> Optional[str]:
    """Run ``git <args>`` in *cwd*; return stripped stdout or None on failure."""
    try:
        proc = subprocess.run(
            ["git", *args],
            cwd=cwd,
            capture_output=True,
            text=True,
            check=False,
            timeout=5,
        )
    except (FileNotFoundError, subprocess.TimeoutExpired):
        return None
    if proc.returncode != 0:
        return None
    return proc.stdout.strip()


def get_commit(path: Optional[str]) -> str:
    """Return ``"<short-sha> (<branch> <clean|dirty>)"`` or ``"N/A: ..."``.

    * ``path`` is the on-disk root of a git checkout. When None, missing, or
      not a git repo, the result is a stable ``"N/A: not present at <path>"``
      string so downstream JSON stays uniform.
    * Branch may be ``HEAD`` on a detached checkout — that's fine; the string
      stays informative.
    """
    if path is None:
        return "N/A: not present at <unset>"

    # rev-parse --short is the canonical way to get the abbreviated SHA;
    # git itself decides the abbreviation length (usually 7+).
    sha = _run_git(path, "rev-parse", "--short", "HEAD")
    if sha is None:
        return f"N/A: not present at {path}"

    branch = _run_git(path, "rev-parse", "--abbrev-ref", "HEAD") or "HEAD"

    # `status --porcelain` is empty iff the working tree matches HEAD.
    status = _run_git(path, "status", "--porcelain")
    state = "clean" if status == "" else "dirty"
    # status == None means git failed mid-flight; record that, not a misleading
    # "clean" verdict.
    if status is None:
        state = "unknown"

    return f"{sha} ({branch} {state})"


def find_git_root(start: str) -> Optional[str]:
    """Walk up from *start* and return the directory git considers the root."""
    return _run_git(start, "rev-parse", "--show-toplevel")
