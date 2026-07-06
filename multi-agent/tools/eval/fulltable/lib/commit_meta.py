"""Pre-flight clean-tree assertion + post-run loom_commit verification.

Spec §7 (f). The runner already collects commit_meta and stamps
runs.loom_commit; this module cross-checks:

* Before dispatch: `git status --porcelain --untracked-files=all` MUST
  be empty. Any staged, unstaged, or untracked-and-not-gitignored path
  fails with `ErrDirtyWorktree`. Return the harness-side `rev-parse HEAD`.
* After dispatch: `verify_runs_loom_commit(runs_csv, expected)` asserts
  every row's `loom_commit` cell equals `expected`; raises
  `ErrLoomCommitMismatch` on any drift.

CLI shim `python3 -m lib.commit_meta` used by `run.sh`:

  --preflight <repo>              → exit 0 + print HEAD, or exit 2
  --verify-runs <csv> --expected <sha> → exit 0, or exit 2 on drift
"""
from __future__ import annotations

import argparse
import csv
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Callable, Iterable, List, Optional


class ErrDirtyWorktree(RuntimeError):
    """Pre-flight tree is not clean (spec §7 (f) step 2)."""

    def __init__(self, offending: List[str]):
        self.offending = offending
        super().__init__(
            "worktree dirty; first 20 offending paths: "
            + ", ".join(offending[:20])
        )


class ErrLoomCommitMismatch(RuntimeError):
    """A runs.csv row's loom_commit does not match harness expectation."""


def _default_git(args: List[str], cwd: Path) -> str:
    """Minimal git-CLI wrapper; overridable via `_git` kwarg for tests."""
    out = subprocess.run(
        ["git", *args], cwd=str(cwd), check=True,
        capture_output=True, text=True,
    )
    return out.stdout


GitFn = Callable[[List[str], Path], str]


def assert_clean_and_get_head(
    repo_dir: Path,
    *,
    _git: Optional[GitFn] = None,
) -> str:
    """Run the spec §7 (f) preflight and return the loom_commit sha.

    Raises `ErrDirtyWorktree` when `git status --porcelain
    --untracked-files=all` produces any non-empty line.
    """
    git = _git or _default_git
    head = git(["rev-parse", "HEAD"], repo_dir).strip()
    status = git(["status", "--porcelain", "--untracked-files=all"], repo_dir)
    offending = [line for line in status.splitlines() if line.strip()]
    if offending:
        raise ErrDirtyWorktree(offending)
    return head


def verify_runs_loom_commit(runs_csv: Path, expected_commit: str) -> None:
    """Cross-check every row's `loom_commit` cell against `expected_commit`.

    A `loom_commit` value in runs.csv may carry a status suffix (e.g.
    ``"abc1234 (clean)"``) — the comparison strips whitespace-delimited
    trailing tokens and matches the SHA prefix at position 0.
    """
    runs_csv = Path(runs_csv)
    if not runs_csv.exists():
        raise FileNotFoundError(runs_csv)
    with runs_csv.open() as f:
        reader = csv.DictReader(f)
        if "loom_commit" not in (reader.fieldnames or []):
            raise ErrLoomCommitMismatch(
                f"runs.csv missing loom_commit column: {reader.fieldnames}"
            )
        for i, row in enumerate(reader, start=1):
            got = (row.get("loom_commit") or "").strip()
            sha = got.split()[0] if got else ""
            # Runner stamps the full 40-char SHA; expected_commit is
            # `git rev-parse HEAD` output (also 40 chars). We accept an
            # exact match or a prefix match (in case a short SHA slipped
            # through).
            if not sha:
                raise ErrLoomCommitMismatch(
                    f"row {i} has empty loom_commit"
                )
            if sha != expected_commit and not (
                expected_commit.startswith(sha) or sha.startswith(expected_commit)
            ):
                raise ErrLoomCommitMismatch(
                    f"row {i} loom_commit {sha!r} != expected {expected_commit!r}"
                )


# ---------------------------------------------------------------------------
# CLI shim consumed by run.sh
# ---------------------------------------------------------------------------


def main(argv: Optional[List[str]] = None) -> int:
    p = argparse.ArgumentParser(prog="lib.commit_meta")
    p.add_argument("--preflight", metavar="REPO",
                   help="check clean tree in REPO; prints HEAD on success")
    p.add_argument("--verify-runs", metavar="CSV",
                   help="post-run: check every runs.csv row's loom_commit")
    p.add_argument("--expected", metavar="SHA",
                   help="expected loom_commit for --verify-runs")
    args = p.parse_args(argv)

    if args.preflight:
        try:
            head = assert_clean_and_get_head(Path(args.preflight))
        except ErrDirtyWorktree as e:
            print(f"ErrDirtyWorktree: {e}", file=sys.stderr)
            return 2
        print(head)
        return 0

    if args.verify_runs:
        if not args.expected:
            print("--expected is required with --verify-runs", file=sys.stderr)
            return 2
        try:
            verify_runs_loom_commit(Path(args.verify_runs), args.expected)
        except (FileNotFoundError, ErrLoomCommitMismatch) as e:
            print(f"ErrLoomCommitMismatch: {e}", file=sys.stderr)
            return 2
        return 0

    p.print_help(sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main())
