"""S053 — --resume enumerates matrix + e4, skips completed sidecars,
deletes stale per-row CSVs (spec §4.5)."""
from __future__ import annotations

import os
import subprocess
import tempfile
from pathlib import Path

import pytest

from conftest import FULLTABLE_DIR, MODULE_ROOT

SMOKE_RUNS = MODULE_ROOT / "tests" / "eval" / "results" / "smoke" / "runs"


def _init_clean_repo(tmp_path: Path) -> Path:
    """Synth git repo for WORKTREE_ROOT — keeps the preflight happy while
    the test mutates smoke/ sidecars."""
    repo = tmp_path / "repo"
    repo.mkdir()
    subprocess.run(["git", "init", "-q"], cwd=str(repo), check=True)
    subprocess.run(["git", "config", "user.email", "a@b"], cwd=str(repo), check=True)
    subprocess.run(["git", "config", "user.name", "a"], cwd=str(repo), check=True)
    (repo / "seed.txt").write_text("seed\n")
    subprocess.run(["git", "add", "seed.txt"], cwd=str(repo), check=True)
    subprocess.run(["git", "commit", "-qm", "seed"], cwd=str(repo), check=True)
    return repo


def _run(args, tmp_repo: Path, env=None):
    full_env = os.environ.copy()
    full_env["LOOM_FULLTABLE_DISPATCH_SHIM"] = "1"
    full_env["WORKTREE_ROOT"] = str(tmp_repo)
    if env:
        full_env.update(env)
    return subprocess.run(
        ["bash", str(FULLTABLE_DIR / "run.sh"), *args],
        cwd=str(MODULE_ROOT), capture_output=True, text=True, env=full_env,
    )


@pytest.fixture
def _clean_smoke_sidecars():
    """Snapshot + restore any .done / stray .csv sidecars around each test.

    The committed smoke fixture already ships some .done markers; the
    test would otherwise mutate them.
    """
    snapshot = {p.name: p.read_bytes() for p in SMOKE_RUNS.iterdir()
                if p.is_file()}
    # Clear everything so the test starts from a known state.
    for p in list(SMOKE_RUNS.iterdir()):
        if p.is_file():
            p.unlink()
    try:
        yield
    finally:
        for p in list(SMOKE_RUNS.iterdir()):
            if p.is_file():
                p.unlink()
        for name, data in snapshot.items():
            (SMOKE_RUNS / name).write_bytes(data)


def test_resume_defaults_to_sample_3(_clean_smoke_sidecars, tmp_path):
    """No --sample given → --resume defaults to N=3 (matrix only, since
    no completed sidecars); expect 3 rows planned."""
    proc = _run(["--resume"], _init_clean_repo(tmp_path))
    assert proc.returncode == 0, (proc.stdout, proc.stderr)
    # With --include-e4 and n=3 → matrix 3 + e4 3 = 6 rows.
    assert "SHIM: would dispatch 6 rows" in proc.stdout, proc.stdout


def test_resume_skips_completed_matrix_sidecar(_clean_smoke_sidecars, tmp_path):
    """Pre-populate one completed matrix sidecar; expect 5 remaining."""
    # First matrix row (spec §4.2): full_loom × cross-device-code-mod.
    marker = SMOKE_RUNS / "matrix__cross-device-code-mod__full_loom__abc123.done"
    marker.write_bytes(b"")
    proc = _run(["--resume", "--sample", "3"], _init_clean_repo(tmp_path))
    assert proc.returncode == 0, (proc.stdout, proc.stderr)
    assert "SHIM: would dispatch 5 rows" in proc.stdout, proc.stdout
    assert "resume: skip matrix__cross-device-code-mod__full_loom" in proc.stderr


def test_resume_skips_completed_e4_sidecar(_clean_smoke_sidecars, tmp_path):
    """Pre-populate an E4 sidecar; expect the matching E4 row skipped."""
    # First e4 row: api-wrapper-for-local-service, stage A, first-task, full_loom.
    marker = SMOKE_RUNS / (
        "e4__api-wrapper-for-local-service__A__first-task__full_loom__def456.done"
    )
    marker.write_bytes(b"")
    proc = _run(["--resume", "--sample", "3"], _init_clean_repo(tmp_path))
    assert proc.returncode == 0, (proc.stdout, proc.stderr)
    # 3 matrix + 2 e4 (one skipped) = 5.
    assert "SHIM: would dispatch 5 rows" in proc.stdout, proc.stdout
    assert (
        "resume: skip e4__api-wrapper-for-local-service__A__first-task__full_loom"
        in proc.stderr
    )


def test_resume_deletes_stale_csv_from_prior_attempt(_clean_smoke_sidecars, tmp_path):
    """Pre-populate a stale .csv (no .done). --resume should remove it
    before retry so runner --out refuse-if-exists doesn't fire."""
    stale = SMOKE_RUNS / (
        "matrix__cross-device-code-mod__full_loom__stale-uuid.csv"
    )
    stale.write_text("stale contents\n")
    assert stale.exists()
    proc = _run(["--resume", "--sample", "3"], _init_clean_repo(tmp_path))
    assert proc.returncode == 0, (proc.stdout, proc.stderr)
    assert not stale.exists(), "stale .csv should have been cleaned up"


def test_resume_skips_both_scopes_simultaneously(_clean_smoke_sidecars, tmp_path):
    """Two completed sidecars — one matrix, one e4 — both skipped."""
    # Both must fall inside the first-3 truncation of each manifest —
    # matrix row 2 (remote-data-processing × full_loom) and e4 row 2
    # (api-wrapper-for-local-service × A × first-task × NoUserPromotionPath).
    (SMOKE_RUNS / "matrix__remote-data-processing__full_loom__x.done").write_bytes(b"")
    (SMOKE_RUNS / (
        "e4__api-wrapper-for-local-service__A__first-task__NoUserPromotionPath__y.done"
    )).write_bytes(b"")
    proc = _run(["--resume", "--sample", "3"], _init_clean_repo(tmp_path))
    assert proc.returncode == 0, (proc.stdout, proc.stderr)
    # 3 matrix - 1 + 3 e4 - 1 = 4.
    assert "SHIM: would dispatch 4 rows" in proc.stdout, proc.stdout
