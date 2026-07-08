"""Repo-wide anti-drift: no un-swapped 'single_machine_claude_code'
outside the single_machine/ dir (untouched reference) and this repo's
docs/ tree (spec / plan / handoff can and do reference the old name)."""
from __future__ import annotations

import subprocess
from pathlib import Path

from conftest import MODULE_ROOT

REPO_ROOT = MODULE_ROOT.parent  # <worktree>/


def test_no_claude_baseline_string_in_source() -> None:
    proc = subprocess.run(
        ["grep", "-rln",
         "--exclude-dir=__pycache__",
         "--exclude=*.pyc",
         "single_machine_claude_code",
         str(REPO_ROOT / "multi-agent" / "tests"),
         str(REPO_ROOT / "multi-agent" / "tools")],
        capture_output=True, text=True,
    )
    lines = [l for l in proc.stdout.splitlines() if l.strip()]
    # Whitelist: the single_machine/ directory (untouched Claude baseline
    # kept for reference) and the README (which explicitly documents both
    # baselines) may reference the old label.
    allowed_prefix = str(REPO_ROOT / "multi-agent" / "tests" / "eval" / "baselines" / "single_machine") + "/"
    allowed_files = {
        str(REPO_ROOT / "multi-agent" / "tests" / "eval" / "baselines" / "README.md"),
        # This test's own source references the string in an assertion
        # message — self-whitelist.
        str(Path(__file__).resolve()),
    }
    bad = [l for l in lines if not l.startswith(allowed_prefix) and l not in allowed_files]
    assert not bad, (
        "single_machine_claude_code still present in source outside the "
        "reference single_machine/ dir + baselines/README.md; enum swap incomplete:\n"
        + "\n".join(f"  {l}" for l in bad)
    )
