"""End-to-end integration tests (§4.1..§4.3 + §7 (g))."""

from __future__ import annotations

import csv
import io
import json
import subprocess
import sys
from pathlib import Path


def _run(args: list[str]) -> subprocess.CompletedProcess:
    return subprocess.run(
        [sys.executable, "-m", "eval_metrics", *args],
        capture_output=True, text=True,
    )


def test_end_to_end_fixture_1_csv(fixture_1_db: Path) -> None:
    r = _run(["extract", "--observer-db", str(fixture_1_db),
              "--format", "csv", "--metric-set", "lifecycle"])
    assert r.returncode == 0, r.stderr
    rows = list(csv.reader(io.StringIO(r.stdout)))
    assert len(rows) == 2
    header, data = rows[0], rows[1]
    idx = {h: i for i, h in enumerate(header)}
    # Populated cells
    assert float(data[idx["TaskSuccessRate"]]) == 0.6
    assert float(data[idx["LifecycleClosureRate"]]) == 0.6
    assert float(data[idx["TimeToCompletion.mean_seconds"]]) == 29.2
    assert int(data[idx["HumanContextSelectionCount"]]) == 8
    # Null cells: ManualSetupStepCount is empty; note present.
    assert data[idx["ManualSetupStepCount"]] == ""
    assert "ManualSetupStepCount: upstream data missing" in data[idx["_notes"]]


def test_end_to_end_fixture_2_json(fixture_2_db: Path) -> None:
    r = _run(["extract", "--observer-db", str(fixture_2_db),
              "--format", "json", "--metric-set", "contracted"])
    assert r.returncode == 0
    d = json.loads(r.stdout)
    contracted = [
        "ContractCompleteness", "PreExecutionFaultCatchRate",
        "ContractViolationRate", "MissingArtifactDetectionRate",
        "PolicyViolationPreventionRate", "RecoverySuccessRate",
        "DuplicateSideEffectRate",
    ]
    for name in contracted:
        assert d["metrics"][name] is None
        assert d["notes"][name] == "upstream data missing"


def test_end_to_end_fixture_3_csv(fixture_3_db: Path) -> None:
    r = _run(["extract", "--observer-db", str(fixture_3_db), "--format", "csv"])
    assert r.returncode == 0
    rows = list(csv.reader(io.StringIO(r.stdout)))
    assert len(rows) == 2
    header, data = rows[0], rows[1]
    idx = {h: i for i, h in enumerate(header)}
    # 8 populated cells present, 33 null cells noted.
    assert float(data[idx["RoutingAccuracy"]]) == 0.75
    assert int(data[idx["RoutingLatencyP50P95.count"]]) == 4
    assert float(data[idx["RoutingLatencyP50P95.p50_ns"]]) == 1_500_000.0


def test_end_to_end_runs_filter_narrows_cohort(fixture_1_db: Path) -> None:
    """--runs-filter matches all 10 fixture-1 rows (all FullLoom)."""
    r = _run([
        "extract", "--observer-db", str(fixture_1_db),
        "--format", "json",
        "--runs-filter", "baseline_or_ablation = 'FullLoom'",
    ])
    assert r.returncode == 0, r.stderr
    d = json.loads(r.stdout)
    assert d["row_count"] == 10


def test_end_to_end_runs_filter_narrows_to_zero(fixture_1_db: Path) -> None:
    """A filter matching zero rows still emits a valid 1-record output."""
    r = _run([
        "extract", "--observer-db", str(fixture_1_db),
        "--format", "json",
        "--runs-filter", "experiment_id = 'DoesNotExist'",
    ])
    assert r.returncode == 0
    d = json.loads(r.stdout)
    assert d["row_count"] == 0
    # Ratios collapse to null + denominator-zero note.
    assert d["metrics"]["TaskSuccessRate"] is None
    assert d["notes"]["TaskSuccessRate"] == "denominator zero"


def test_end_to_end_stderr_warnings(fixture_3_db: Path) -> None:
    r = _run(["extract", "--observer-db", str(fixture_3_db), "--format", "json"])
    assert r.returncode == 0
    # At least one upstream-missing warning; owner hint present.
    assert "warn: metric" in r.stderr
    assert "upstream data missing" in r.stderr
    assert "owner:" in r.stderr
