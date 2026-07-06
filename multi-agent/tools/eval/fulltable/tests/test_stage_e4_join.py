"""Spec §4.4 — stage_e4.join produces 240 rows keyed on 4-tuple."""
from __future__ import annotations

from pathlib import Path

import yaml

from conftest import FULLTABLE_DIR
from lib import stage_e4


def _manifest():
    return yaml.safe_load((FULLTABLE_DIR / "e4_stages.yaml").read_text())


def test_column_list_matches_spec():
    cols = stage_e4.COLUMNS
    assert cols[0] == "stage"
    assert cols[1] == "family"
    assert cols[2] == "task_id"
    assert cols[3] == "configuration"
    # Spot-check that every metric column ∈ ALL_METRICS.
    from eval_metrics.metrics import ALL_METRICS
    known = {m.name for m in ALL_METRICS}
    for m in cols[4:]:
        assert m in known


def test_build_smoke_rows_produces_240():
    rows = stage_e4.build_smoke_rows(_manifest())
    assert len(rows) == 240


def test_4tuple_unique():
    rows = stage_e4.build_smoke_rows(_manifest())
    keys = {(r["stage"], r["family"], r["task_id"], r["configuration"]) for r in rows}
    assert len(keys) == 240


def test_stage_a_full_loom_task_success_not_blank():
    rows = stage_e4.build_smoke_rows(_manifest())
    a_full = [r for r in rows if r["stage"] == "A" and r["configuration"] == "full_loom"]
    assert len(a_full) == 20  # 5 families × 4 tasks
    for r in a_full:
        assert r["TaskSuccessRate"] != ""


def test_join_runs_blank_when_missing():
    """No matching run in the input → blank cells throughout."""
    rows = stage_e4.join_runs(runs=[], e4_manifest=_manifest())
    assert len(rows) == 240
    for r in rows:
        for m in stage_e4.METRIC_COLS:
            assert r[m] == ""


def test_join_runs_picks_up_matching():
    manifest = _manifest()[:1]  # take one entry
    entry = manifest[0]
    run = {
        "family": entry["family"],
        "stage": entry["stage"],
        "task_id": entry["task_id"],
        "configuration": entry["configuration"],
        "metrics": {"TaskSuccessRate": 1.0},
    }
    rows = stage_e4.join_runs([run], manifest)
    assert rows[0]["TaskSuccessRate"] == 1.0
