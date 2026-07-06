"""S056 — --resume preserves completed sidecars + associated per-row
CSVs; only stale CSVs (no matching .done) get removed (spec §4.5)."""
from __future__ import annotations

import os
import subprocess
from pathlib import Path

import pytest

from conftest import FULLTABLE_DIR, MODULE_ROOT

SMOKE_ROOT = MODULE_ROOT / "tests" / "eval" / "results" / "smoke"
SMOKE_RUNS = SMOKE_ROOT / "runs"


def _init_clean_repo(tmp_path: Path) -> Path:
    repo = tmp_path / "repo"
    repo.mkdir()
    subprocess.run(["git", "init", "-q"], cwd=str(repo), check=True)
    subprocess.run(["git", "config", "user.email", "a@b"], cwd=str(repo), check=True)
    subprocess.run(["git", "config", "user.name", "a"], cwd=str(repo), check=True)
    (repo / "seed.txt").write_text("seed\n")
    subprocess.run(["git", "add", "seed.txt"], cwd=str(repo), check=True)
    subprocess.run(["git", "commit", "-qm", "seed"], cwd=str(repo), check=True)
    return repo


def _run(args, tmp_repo, env=None):
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
def _isolated_sidecars():
    """Snapshot + restore every file under smoke/runs/ around the test."""
    snapshot = {p.name: p.read_bytes() for p in SMOKE_RUNS.iterdir() if p.is_file()}
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


def test_resume_preserves_completed_sidecars_and_purges_stale(
    _isolated_sidecars, tmp_path
):
    # Two completed matrix rows (row 1 cross-device-code-mod, row 2
    # remote-data-processing) — each has both .done and its .csv.
    done_a = SMOKE_RUNS / "matrix__cross-device-code-mod__full_loom__aaaa.done"
    csv_a  = SMOKE_RUNS / "matrix__cross-device-code-mod__full_loom__aaaa.csv"
    done_b = SMOKE_RUNS / "matrix__remote-data-processing__full_loom__bbbb.done"
    csv_b  = SMOKE_RUNS / "matrix__remote-data-processing__full_loom__bbbb.csv"
    done_a.write_bytes(b"")
    csv_a.write_text("completed run A\n")
    done_b.write_bytes(b"")
    csv_b.write_text("completed run B\n")
    # One stale row (matrix row 3, windows-only-artifact) — .csv only,
    # no .done. Simulates a prior attempt that crashed mid-flight.
    stale = SMOKE_RUNS / "matrix__windows-only-artifact__full_loom__stale.csv"
    stale.write_text("stale leftover\n")

    proc = _run(["--resume", "--sample", "3"], _init_clean_repo(tmp_path))
    assert proc.returncode == 0, (proc.stdout, proc.stderr)

    # Completed .done files preserved.
    assert done_a.exists(), "matrix row A .done was destroyed"
    assert done_b.exists(), "matrix row B .done was destroyed"
    # Their associated per-row .csv files preserved.
    assert csv_a.exists(), "matrix row A .csv was destroyed"
    assert csv_b.exists(), "matrix row B .csv was destroyed"
    # Stale .csv (no matching .done) removed.
    assert not stale.exists(), "stale .csv should have been cleaned up"
    # Contents of completed CSVs unchanged.
    assert csv_a.read_text() == "completed run A\n"
    assert csv_b.read_text() == "completed run B\n"

    # 3 matrix - 2 completed = 1 matrix; + 3 e4 = 4 rows total.
    assert "SHIM: would dispatch 4 rows" in proc.stdout, proc.stdout


def test_resume_leaves_runs_csv_metrics_csv_failures_jsonl_untouched(
    _isolated_sidecars, tmp_path
):
    """spec §4.5: those aggregate files accumulate across resume runs
    (the runner appends). Non-resume smoke truncates them; --resume
    must NOT."""
    # Fabricate accumulating output.
    (SMOKE_ROOT / "runs.csv").write_text("run_id,x\nprev-a,1\nprev-b,2\n")
    (SMOKE_ROOT / "metrics.csv").write_text("run_id,m\nprev-a,0.5\n")
    (SMOKE_ROOT / "failures.jsonl").write_text('{"run_id":"prev-a"}\n')
    # Existing .db files must not be nuked either.
    (SMOKE_ROOT / "dbs").mkdir(exist_ok=True)
    keeper_db = SMOKE_ROOT / "dbs" / "prev-a.db"
    keeper_db.write_bytes(b"")

    try:
        proc = _run(["--resume", "--sample", "3"], _init_clean_repo(tmp_path))
        assert proc.returncode == 0, (proc.stdout, proc.stderr)
        # Aggregating files still contain the pre-existing content.
        assert "prev-a" in (SMOKE_ROOT / "runs.csv").read_text()
        assert "prev-a" in (SMOKE_ROOT / "metrics.csv").read_text()
        assert "prev-a" in (SMOKE_ROOT / "failures.jsonl").read_text()
        assert keeper_db.exists(), "previous run's .db was destroyed"
    finally:
        # Restore the committed smoke fixture.
        subprocess.run(
            ["git", "checkout", "--",
             "tests/eval/results/smoke/runs.csv",
             "tests/eval/results/smoke/metrics.csv",
             "tests/eval/results/smoke/failures.jsonl",
             "tests/eval/results/smoke/dbs/"],
            cwd=str(MODULE_ROOT), check=False, capture_output=True,
        )
        # Remove the fabricated dbs entry if git checkout didn't
        # (it wasn't tracked so git won't delete it).
        if keeper_db.exists():
            keeper_db.unlink()
