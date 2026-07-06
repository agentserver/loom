"""End-to-end smoke: dry_run_smoke.sh + assertion of zero paper diff."""

from __future__ import annotations

import subprocess
from pathlib import Path

import pytest


CANONICAL_KEYS = (
    "contexts_count",
    "wrong_context_failure_manual_baseline",
    "manual_steps_ssh",
    "reuse_time_savings",
)


def test_dry_run_smoke_end_to_end(
    ma_root: Path, paper_worktree: Path, smoke_results_dir: Path,
):
    """spec §5 test_target_files_zero_changes.

    Runs dry_run_smoke.sh, asserts:
      - all 4 aggregate JSON produced under results/dry_run_smoke/;
      - /tmp/replace_intro.diff non-empty + contains both target basenames;
      - `git diff` on the two paper files is empty (harness-only invariant).
    """
    smoke_sh = ma_root / "tests" / "eval" / "motivation" / "dry_run_smoke.sh"
    r = subprocess.run(
        ["bash", str(smoke_sh)],
        cwd=str(ma_root),
        capture_output=True, text=True,
        env={
            **__import__("os").environ,
            "PAPER_WORKTREE": str(paper_worktree),
        },
    )
    assert r.returncode == 0, f"smoke failed:\nstdout={r.stdout}\nstderr={r.stderr}"

    for key in CANONICAL_KEYS:
        assert (smoke_results_dir / f"{key}.json").exists()

    for tgt in ("introduction_v3.md", "motivation_v3.md"):
        diff = subprocess.run(
            ["git", "-C", str(paper_worktree), "diff", "--", f"paper_outputs/{tgt}"],
            capture_output=True, text=True,
        )
        assert diff.returncode == 0
        assert diff.stdout == "", f"{tgt} was mutated by smoke chain:\n{diff.stdout}"

    diff_path = Path("/tmp/replace_intro.diff")
    assert diff_path.exists() and diff_path.stat().st_size > 0
    text = diff_path.read_text(encoding="utf-8")
    assert "introduction_v3.md" in text
    assert "motivation_v3.md" in text
