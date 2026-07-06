"""Spec §7 (c) — cloud_sandbox_e2b rows forced dry-run."""
from __future__ import annotations

from pathlib import Path

import pytest

from conftest import FULLTABLE_DIR
from lib.plan import (
    ErrCloudDryRunMissing,
    enumerate_matrix_argvs,
    plan_command_for_matrix_row,
)


def _smoke_root(tmp_path):
    p = tmp_path / "tests" / "eval" / "results" / "smoke"
    p.mkdir(parents=True)
    return p


def test_cloud_rows_get_dry_run_flag(tmp_path):
    matrix = FULLTABLE_DIR / "matrix.yaml"
    plans = enumerate_matrix_argvs(matrix, smoke_root=_smoke_root(tmp_path))
    cloud = [p for p in plans if p.configuration == "cloud_sandbox_e2b"]
    assert len(cloud) == 5
    for plan in cloud:
        assert "--dry-run" in plan.argv


def test_missing_dry_run_rejected(tmp_path):
    row = {"workload_id": "cross-device-code-mod",
           "baseline_or_ablation": "cloud_sandbox_e2b"}  # no dry_run
    with pytest.raises(ErrCloudDryRunMissing):
        plan_command_for_matrix_row(
            row, port=18100, smoke_root=_smoke_root(tmp_path))


def test_dry_run_false_rejected(tmp_path):
    row = {"workload_id": "cross-device-code-mod",
           "baseline_or_ablation": "cloud_sandbox_e2b", "dry_run": False}
    with pytest.raises(ErrCloudDryRunMissing):
        plan_command_for_matrix_row(
            row, port=18100, smoke_root=_smoke_root(tmp_path))
