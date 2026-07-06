"""Spec §7 (k) — --sample N > 3 hard cap fires before --dry-run."""
from __future__ import annotations

import os
import subprocess
from pathlib import Path

from conftest import FULLTABLE_DIR, MODULE_ROOT


def _run(args, env=None):
    full_env = os.environ.copy()
    if env:
        full_env.update(env)
    return subprocess.run(
        ["bash", str(FULLTABLE_DIR / "run.sh"), *args],
        cwd=str(MODULE_ROOT), capture_output=True, text=True, env=full_env,
    )


def test_sample_4_no_env_exits_2():
    proc = _run(["--sample", "4"])
    assert proc.returncode == 2
    assert "ErrFullRunNotAllowed" in proc.stderr


def test_sample_60_dry_run_no_env_still_exits_2():
    """Gate fires BEFORE --dry-run short-circuits (spec §4.3)."""
    proc = _run(["--sample", "60", "--dry-run"])
    assert proc.returncode == 2
    assert "ErrFullRunNotAllowed" in proc.stderr


def test_allow_full_run_env_lets_dry_run_pass():
    """ALLOW_FULL_RUN=1 with --sample 4 --dry-run: exit 0, ≤ 60 CLI lines."""
    proc = _run(["--sample", "4", "--dry-run"], env={"ALLOW_FULL_RUN": "1"})
    assert proc.returncode == 0
    # --dry-run always prints the full 60 (spec §4.1); --sample is only
    # respected during real dispatch. The invariant we care about is
    # that the gate did NOT fire.
    assert "ErrFullRunNotAllowed" not in proc.stderr
    assert len(proc.stdout.splitlines()) == 60


def test_sample_3_no_env_passes_gate():
    """N ≤ 3 must not trigger ErrFullRunNotAllowed. Uses SHIM to skip
    real dispatch — the CI-safe test seam per spec §7 (h)."""
    proc = _run(["--sample", "3"], env={"LOOM_FULLTABLE_DISPATCH_SHIM": "1"})
    assert "ErrFullRunNotAllowed" not in proc.stderr
    # SHIM prints one line; we don't assert exit 0 here because the
    # preflight may fail on a dirty worktree — that's a separate test.
