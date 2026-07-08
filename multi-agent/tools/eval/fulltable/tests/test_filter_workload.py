"""Task 10 §Global Constraints: --workload filter selects exactly the
rows for one workload. Filter-first-then-sample ordering per spec
§4.3 (round-1 P0#2 resolution)."""
from __future__ import annotations

import subprocess
import sys
from pathlib import Path

import pytest

from conftest import FULLTABLE_DIR, MODULE_ROOT

sys.path.insert(0, str(FULLTABLE_DIR))
from lib import plan as planner


ALL_WORKLOADS = (
    "cross-device-code-mod",
    "remote-data-processing",
    "windows-only-artifact",
    "missing-parser-converter",
    "credential-bound-model",
)


def test_filter_workload_yields_12_rows_each() -> None:
    rows = planner.parse_matrix(FULLTABLE_DIR / "matrix.yaml")
    for w in ALL_WORKLOADS:
        got = planner.filter_workload(rows, w)
        assert len(got) == 12, f"workload {w}: expected 12 rows, got {len(got)}"
        for r in got:
            assert r["workload_id"] == w


def test_filter_workload_rejects_unknown_id() -> None:
    rows = planner.parse_matrix(FULLTABLE_DIR / "matrix.yaml")
    with pytest.raises(planner.UnknownWorkloadError):
        planner.filter_workload(rows, "no-such-workload")


def test_filter_workload_via_cli_dry_run() -> None:
    proc = subprocess.run(
        ["python3", "-m", "lib.plan",
         "--matrix", str(FULLTABLE_DIR / "matrix.yaml"),
         "--smoke-root", "tests/eval/results/smoke",
         "--filter-workload", "cross-device-code-mod",
         "dry-run"],
        cwd=str(MODULE_ROOT), env={"PYTHONPATH": str(FULLTABLE_DIR), "PATH": "/usr/bin:/bin"},
        capture_output=True, text=True, check=True,
    )
    lines = [l for l in proc.stdout.splitlines() if l.strip()]
    assert len(lines) == 12, f"filter dry-run expected 12 lines, got {len(lines)}"
    for l in lines:
        assert "cross-device-code-mod" in l


def test_filter_workload_before_sample_non_prefix() -> None:
    """Plan-review P0#2 regression — a workload that does NOT appear in
    the first `sample_n` rows must STILL yield rows after filter+sample.
    Without filter-first-then-sample, this returns 0 rows."""
    proc = subprocess.run(
        ["python3", "-m", "lib.plan",
         "--matrix", str(FULLTABLE_DIR / "matrix.yaml"),
         "--smoke-root", "tests/eval/results/smoke",
         "--filter-workload", "credential-bound-model",
         "sample", "--n", "2"],
        cwd=str(MODULE_ROOT), env={"PYTHONPATH": str(FULLTABLE_DIR), "PATH": "/usr/bin:/bin"},
        capture_output=True, text=True,
    )
    assert proc.returncode == 0, f"stderr={proc.stderr}"
    lines = [l for l in proc.stdout.splitlines() if l.strip()]
    assert len(lines) == 2, (
        f"filter-first-then-sample violated: expected 2 rows, got {len(lines)}. "
        f"If 0: enumerate_matrix_argvs samples before filter — see P0#2."
    )
    for l in lines:
        assert "credential-bound-model" in l


def test_filter_workload_cli_rejects_unknown() -> None:
    proc = subprocess.run(
        ["python3", "-m", "lib.plan",
         "--matrix", str(FULLTABLE_DIR / "matrix.yaml"),
         "--smoke-root", "tests/eval/results/smoke",
         "--filter-workload", "no-such-workload",
         "dry-run"],
        cwd=str(MODULE_ROOT), env={"PYTHONPATH": str(FULLTABLE_DIR), "PATH": "/usr/bin:/bin"},
        capture_output=True, text=True,
    )
    assert proc.returncode != 0
    assert "no-such-workload" in proc.stderr
