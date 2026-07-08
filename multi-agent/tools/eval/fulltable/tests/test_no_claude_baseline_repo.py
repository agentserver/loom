"""Repo-wide anti-drift: no un-swapped 'single_machine_claude_code'
outside the single_machine/ dir (untouched reference) and this repo's
docs/ tree (spec / plan / handoff can and do reference the old name)."""
from __future__ import annotations

import subprocess
from pathlib import Path

from conftest import MODULE_ROOT

REPO_ROOT = MODULE_ROOT.parent  # <worktree>/


def test_no_claude_baseline_string_in_source() -> None:
    """Plan-review Phase E P1: search the WHOLE `multi-agent/` tree,
    not just tests/tools. Otherwise a stale reference in
    `multi-agent/internal/…` or `multi-agent/cmd/…` slips through.
    """
    proc = subprocess.run(
        ["grep", "-rln",
         "--exclude-dir=__pycache__",
         "--exclude-dir=.git",
         "--exclude=*.pyc",
         "single_machine_claude_code",
         str(REPO_ROOT / "multi-agent")],
        capture_output=True, text=True,
    )
    # grep exit code: 0 = matches found, 1 = no matches, 2 = error.
    assert proc.returncode in (0, 1), (
        f"grep failed unexpectedly (rc={proc.returncode}): "
        f"stderr={proc.stderr!r}"
    )
    lines = [l for l in proc.stdout.splitlines() if l.strip()]
    # Whitelist: the single_machine/ directory (untouched Claude
    # baseline kept for reference), the baselines README (which
    # explicitly documents both), and this test's own source.
    allowed_prefix = str(REPO_ROOT / "multi-agent" / "tests" / "eval" / "baselines" / "single_machine") + "/"
    allowed_files = {
        str(REPO_ROOT / "multi-agent" / "tests" / "eval" / "baselines" / "README.md"),
        str(Path(__file__).resolve()),
    }
    bad = [l for l in lines if not l.startswith(allowed_prefix) and l not in allowed_files]
    assert not bad, (
        "single_machine_claude_code still present in source outside the "
        "reference single_machine/ dir + baselines/README.md + this test:\n"
        + "\n".join(f"  {l}" for l in bad)
    )
