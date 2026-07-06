"""Spec §7 (d) — per-run observer SQLite files are distinct AND the
harness refuses a plan with duplicate observer_db paths."""
from __future__ import annotations

import subprocess
import sys
from pathlib import Path

import pytest

from conftest import FULLTABLE_DIR, MODULE_ROOT
from lib.plan import (
    ErrObserverDBCollision,
    RunPlan,
    assert_no_observer_db_collision,
)

SMOKE_DBS = MODULE_ROOT / "tests" / "eval" / "results" / "smoke" / "dbs"


def test_three_distinct_db_files():
    dbs = sorted(SMOKE_DBS.glob("*.db"))
    assert len(dbs) == 3, f"expected 3 db files, got {[d.name for d in dbs]}"
    names = {d.name for d in dbs}
    assert len(names) == 3
    # UUIDv4 shape: 36 chars ± ".db" suffix.
    for d in dbs:
        stem = d.stem
        assert len(stem) == 36, f"expected UUID stem, got {stem}"


def test_db_paths_under_smoke_root():
    """Sanity: DB files never escape the smoke/ directory."""
    for d in SMOKE_DBS.glob("*.db"):
        assert "tests/eval/results/smoke/dbs" in str(d)


def _stub_plan(resume_key: str, observer_db: str = "shared.db") -> RunPlan:
    return RunPlan(
        kind="matrix",
        resume_key=resume_key,
        argv=["eval-runner", "run"],
        run_id="uuid",
        workload_id="cross-device-code-mod",
        configuration="full_loom",
        out_csv="out.csv",
        observer_db=observer_db,
    )


def test_collision_guard_accepts_distinct_paths():
    plans = [_stub_plan("k1", "a.db"), _stub_plan("k2", "b.db")]
    assert_no_observer_db_collision(plans)


def test_collision_guard_ignores_empty_observer_db():
    """Baseline rows leave observer_db empty; they don't collide."""
    plans = [_stub_plan("k1", ""), _stub_plan("k2", "")]
    assert_no_observer_db_collision(plans)


def test_collision_guard_rejects_duplicate_paths():
    plans = [_stub_plan("k1", "same.db"), _stub_plan("k2", "same.db")]
    with pytest.raises(ErrObserverDBCollision) as excinfo:
        assert_no_observer_db_collision(plans)
    msg = str(excinfo.value)
    assert "same.db" in msg
    assert "k1" in msg
    assert "k2" in msg


def test_plan_cli_exits_2_on_collision(monkeypatch, tmp_path):
    """End-to-end via the plan CLI: monkeypatch uuid.uuid4 to a constant
    so two rows land on the same observer_db path; CLI must exit 2 with
    ErrObserverDBCollision on stderr."""
    proc = subprocess.run(
        [sys.executable, "-c",
         "import sys, uuid; "
         "uuid.uuid4 = lambda: uuid.UUID('00000000-0000-0000-0000-000000000000'); "
         "sys.path.insert(0, r'" + str(FULLTABLE_DIR) + "'); "
         "from lib.plan import main; "
         "sys.exit(main([\"--matrix\", r'" + str(FULLTABLE_DIR / "matrix.yaml") + "', "
         "\"--smoke-root\", \"tests/eval/results/smoke\", "
         "\"--timeout\", \"60s\", \"sample\", \"--n\", \"3\"]))"],
        capture_output=True, text=True,
    )
    assert proc.returncode == 2, (proc.stdout, proc.stderr)
    assert "ErrObserverDBCollision" in proc.stderr
