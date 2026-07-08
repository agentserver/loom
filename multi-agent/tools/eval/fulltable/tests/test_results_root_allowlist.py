"""Global Constraints — --results-root rejects unsafe roots:
$HOME, $HOME/.codex, $HOME/.codex/subdirs, /, /tmp, /root,
git-repo top-level, symlink-prefix escapes."""
from __future__ import annotations

import os
import subprocess
from pathlib import Path

import pytest
from conftest import FULLTABLE_DIR, MODULE_ROOT


BAD_ROOTS = [
    "/",
    "/tmp",
    "/root",
    os.path.expanduser("~"),
    os.path.expanduser("~/.codex"),
    os.path.expanduser("~/.codex/subdir"),                # plan-review r3 P0
    os.path.expanduser("~/.codex/nested/deep/subdir"),    # plan-review r3 P0
]


@pytest.mark.parametrize("bad", BAD_ROOTS)
def test_results_root_rejects_unsafe(bad: str) -> None:
    proc = subprocess.run(
        ["python3", "-m", "lib.plan",
         "--matrix", str(FULLTABLE_DIR / "matrix.yaml"),
         "--smoke-root", "tests/eval/results/smoke",
         "--results-root", bad,
         "dry-run"],
        cwd=str(MODULE_ROOT),
        env={"PYTHONPATH": str(FULLTABLE_DIR), "PATH": "/usr/bin:/bin"},
        capture_output=True, text=True,
    )
    assert proc.returncode != 0, f"root {bad!r} accepted; expected rejection"
    assert "results-root" in proc.stderr.lower() or "unsafe" in proc.stderr.lower() or "refusing" in proc.stderr.lower(), \
        f"stderr should name the flag / reason; got: {proc.stderr}"


def _allowlisted_test_base(subdir: str) -> Path:
    """Symlink-escape tests MUST live under an allowlisted, non-/tmp
    base — otherwise the raw guard rejects vacuously.
    """
    base = MODULE_ROOT / "tests" / "eval" / "results" / "experiments" / "_symlink_test" / subdir
    base.mkdir(parents=True, exist_ok=True)
    return base


def test_results_root_rejects_symlink_prefix_to_tmp() -> None:
    """Plan-review r8 P1: symlink prefix + missing child MUST be
    resolved before allowlist check. Base MUST be under an
    allowlisted root (NOT /tmp) so the raw guard doesn't reject
    the path vacuously.
    """
    import shutil, uuid
    base = _allowlisted_test_base(f"tmp-{uuid.uuid4().hex[:8]}")
    try:
        link = base / "link"
        try:
            link.symlink_to("/tmp")
        except FileExistsError:
            pass
        candidate = str(link / "missing" / "deep")
        assert not candidate.startswith("/tmp/"), (
            f"vacuous test setup: raw candidate {candidate!r} starts with /tmp/"
        )
        proc = subprocess.run(
            ["python3", "-m", "lib.plan",
             "--matrix", str(FULLTABLE_DIR / "matrix.yaml"),
             "--smoke-root", "tests/eval/results/smoke",
             "--results-root", candidate,
             "dry-run"],
            cwd=str(MODULE_ROOT),
            env={"PYTHONPATH": str(FULLTABLE_DIR), "PATH": "/usr/bin:/bin"},
            capture_output=True, text=True,
        )
        assert proc.returncode != 0, (
            f"symlink-prefix path {candidate!r} escaped /tmp allowlist; "
            f"stderr={proc.stderr!r}"
        )
        assert "unsafe" in proc.stderr.lower() or "refusing" in proc.stderr.lower(), (
            f"expected refusal message; got: {proc.stderr}"
        )
    finally:
        shutil.rmtree(base, ignore_errors=True)


def test_results_root_rejects_symlink_prefix_to_codex() -> None:
    """Plan-review r8 P1: symlink-prefix pointing at FAKE $HOME/.codex.

    Base under allowlisted root. Fake HOME lives INSIDE base so the
    raw candidate doesn't trip a /tmp or real-codex guard.
    """
    import shutil, uuid
    base = _allowlisted_test_base(f"codex-{uuid.uuid4().hex[:8]}")
    try:
        fake_home = base / "fake_home"
        fake_codex = fake_home / ".codex"
        fake_codex.mkdir(parents=True, exist_ok=True)
        link = base / "link"
        try:
            link.symlink_to(str(fake_codex))
        except FileExistsError:
            pass
        candidate = str(link / "missing")
        assert not candidate.startswith("/tmp/"), (
            f"vacuous setup: raw candidate {candidate!r} starts with /tmp/"
        )
        real_codex = os.path.expanduser("~/.codex")
        assert not candidate.startswith(real_codex + "/"), (
            f"vacuous setup: raw candidate {candidate!r} starts with real ~/.codex"
        )
        proc = subprocess.run(
            ["python3", "-m", "lib.plan",
             "--matrix", str(FULLTABLE_DIR / "matrix.yaml"),
             "--smoke-root", "tests/eval/results/smoke",
             "--results-root", candidate,
             "dry-run"],
            cwd=str(MODULE_ROOT),
            env={"PYTHONPATH": str(FULLTABLE_DIR), "PATH": "/usr/bin:/bin",
                 "HOME": str(fake_home)},
            capture_output=True, text=True,
        )
        assert proc.returncode != 0, (
            f"symlink-prefix path {candidate!r} escaped $HOME/.codex allowlist; "
            f"stderr={proc.stderr!r}"
        )
    finally:
        shutil.rmtree(base, ignore_errors=True)


def test_results_root_planner_writes_alt() -> None:
    """Plan-review r2 P0 regression — --results-root through plan.py CLI
    must yield planner output paths under the alt root, NOT under
    tests/eval/results/smoke."""
    import shutil
    alt = MODULE_ROOT / "tests" / "eval" / "results" / "experiments" / "_plan_test_alt" / "run1"
    alt.mkdir(parents=True, exist_ok=True)
    try:
        proc = subprocess.run(
            ["python3", "-m", "lib.plan",
             "--matrix", str(FULLTABLE_DIR / "matrix.yaml"),
             "--smoke-root", "tests/eval/results/smoke",
             "--results-root", str(alt),
             "--filter-workload", "cross-device-code-mod",
             "dry-run"],
            cwd=str(MODULE_ROOT),
            env={"PYTHONPATH": str(FULLTABLE_DIR), "PATH": "/usr/bin:/bin"},
            capture_output=True, text=True,
        )
        assert proc.returncode == 0, f"planner failed: stderr={proc.stderr}"
        for l in proc.stdout.splitlines():
            assert "tests/eval/results/smoke" not in l, (
                f"planner leaked smoke path when --results-root was set:\n  {l}"
            )
        assert any("_plan_test_alt" in l for l in proc.stdout.splitlines()), (
            f"planner did not use --results-root; output:\n{proc.stdout}"
        )
    finally:
        shutil.rmtree(MODULE_ROOT / "tests" / "eval" / "results" / "experiments" / "_plan_test_alt", ignore_errors=True)
