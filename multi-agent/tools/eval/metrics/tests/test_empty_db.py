"""Spec §5.1 + §3.3 empty-DB contract."""

from __future__ import annotations

import json
import subprocess
import sys
from pathlib import Path

from eval_metrics.metrics import ALL_METRICS


def _run_csv(db: Path) -> subprocess.CompletedProcess:
    return subprocess.run(
        [sys.executable, "-m", "eval_metrics", "extract",
         "--observer-db", str(db), "--format", "csv"],
        capture_output=True, text=True,
    )


def _run_json(db: Path) -> subprocess.CompletedProcess:
    return subprocess.run(
        [sys.executable, "-m", "eval_metrics", "extract",
         "--observer-db", str(db), "--format", "json"],
        capture_output=True, text=True,
    )


def test_empty_db_csv_header(empty_db: Path) -> None:
    r = _run_csv(empty_db)
    assert r.returncode == 0
    lines = r.stdout.splitlines()
    assert len(lines) == 2, f"expected header + 1 data row; got {len(lines)}"
    header = lines[0].split(",")
    assert header[0] == "metric_set"
    assert header[1] == "row_count"
    assert header[-1] == "_notes"
    data = lines[1].split(",")
    assert data[0] == "full"
    assert data[1] == "0"


def test_empty_db_count_landed_zero(empty_db: Path) -> None:
    r = _run_json(empty_db)
    assert r.returncode == 0
    d = json.loads(r.stdout)
    # HumanContextSelectionCount: landed upstream → 0 on empty cohort.
    assert d["metrics"]["HumanContextSelectionCount"] == 0


def test_empty_db_count_unlanded_null(empty_db: Path) -> None:
    r = _run_json(empty_db)
    d = json.loads(r.stdout)
    # ManualSetupStepCount: unlanded upstream → null (empty CSV cell).
    assert d["metrics"]["ManualSetupStepCount"] is None
    assert d["notes"]["ManualSetupStepCount"] == "upstream data missing"


def test_empty_db_all_null_ratios_have_notes(empty_db: Path) -> None:
    r = _run_json(empty_db)
    d = json.loads(r.stdout)
    # Every metric that came back None (or structured all-None) MUST
    # be in `notes` with one of the two closed-set reasons.
    for m in ALL_METRICS:
        v = d["metrics"][m.name]
        is_null = v is None or (isinstance(v, dict) and all(x is None for x in v.values()))
        if is_null:
            assert m.name in d["notes"], m.name
            assert d["notes"][m.name] in ("upstream data missing", "denominator zero")


def test_empty_db_json_shape(empty_db: Path) -> None:
    r = _run_json(empty_db)
    d = json.loads(r.stdout)
    assert list(d.keys()) == ["metric_set", "row_count", "metrics", "notes"]
    assert d["row_count"] == 0
    assert len(d["metrics"]) == 41
    # RoutingLatencyP50P95: route_reasons EXISTS but is empty →
    # denominator zero, NOT upstream missing (spec §4.4).
    assert d["notes"]["RoutingLatencyP50P95"] == "denominator zero"
