"""Spec §3 + §3.2 + §7 (e) CLI enforcement."""

from __future__ import annotations

import subprocess
import sys
from pathlib import Path

import pytest

from eval_metrics.metrics import metrics_for_set


def _run(args: list[str]) -> subprocess.CompletedProcess:
    return subprocess.run(
        [sys.executable, "-m", "eval_metrics", *args],
        capture_output=True, text=True,
    )


def test_metric_set_full_default(fixture_1_db: Path) -> None:
    """`full` emits all 41 metrics (spec §3.2)."""
    r = _run(["extract", "--observer-db", str(fixture_1_db), "--format", "json"])
    assert r.returncode == 0
    import json
    d = json.loads(r.stdout)
    assert d["metric_set"] == "full"
    assert len(d["metrics"]) == 41


def test_metric_set_typo_reject(fixture_1_db: Path) -> None:
    """`--metric-set liflecycle` → exit 2; allowed list printed (spec §7 (e))."""
    r = _run(["extract", "--observer-db", str(fixture_1_db),
              "--format", "csv", "--metric-set", "liflecycle"])
    assert r.returncode == 2
    assert "liflecycle" in r.stderr or "invalid choice" in r.stderr.lower()


def test_metric_set_bare_missing_reject(fixture_1_db: Path) -> None:
    """`--metric-set` without a value → argparse exit 2."""
    r = _run(["extract", "--observer-db", str(fixture_1_db),
              "--format", "csv", "--metric-set"])
    assert r.returncode == 2


def test_metric_set_case_sensitivity(fixture_1_db: Path) -> None:
    """`Lifecycle` (capitalised) → exit 2 (case-sensitive silent-typo protection)."""
    r = _run(["extract", "--observer-db", str(fixture_1_db),
              "--format", "csv", "--metric-set", "Lifecycle"])
    assert r.returncode == 2


def test_metric_set_lifecycle_cross_listed() -> None:
    """`lifecycle` emits §2.1 rows + #22 + #23 (spec §3.2 cross-list)."""
    names = [m.name for m in metrics_for_set("lifecycle")]
    for n in (
        "TaskSuccessRate", "LifecycleClosureRate", "TimeToCompletion",
        "HumanContextSelectionCount", "WrongContextFailureRate",
        "ArtifactCorrectnessRate", "ManualSetupStepCount",
        "ConfigTouchCount", "StateContinuityRate",
        # Cross-listed
        "CapabilityReuseRate", "RepeatedGenerationRate",
    ):
        assert n in names, n


def test_metric_set_semantic_cross_listed() -> None:
    """`semantic` emits §2.4 + #4 + #5 (spec §3.2 cross-list, 08:140)."""
    names = [m.name for m in metrics_for_set("semantic")]
    for n in (
        "RoutingAccuracy", "CapabilityRecall", "CapabilityPrecision",
        "HumanContextSelectionCount", "WrongContextFailureRate",
    ):
        assert n in names, n


def test_metric_set_overhead_cross_listed() -> None:
    """`overhead` emits §2.5 + #7 + #8 (spec §3.2 cross-list)."""
    names = [m.name for m in metrics_for_set("overhead")]
    for n in (
        "DriverPlanningOverhead", "TaskDispatchLatency", "TunnelOverhead",
        "ArtifactTransferThroughput", "ObserverOverhead",
        "ModelProxyOverhead", "RoutingLatencyP50P95",
        "TimeToFirstTask", "SetupFailureRate",
        # Cross-listed
        "ManualSetupStepCount", "ConfigTouchCount",
    ):
        assert n in names, n


def test_metric_set_user_promoted_cross_listed() -> None:
    """`user-promoted` emits §2.3 + #1 + #3 + #40 + #41 (spec §3.2)."""
    names = [m.name for m in metrics_for_set("user-promoted")]
    for n in (
        "PromotionCandidateSurfacingRate", "TimeFromUserDecisionToRegisteredMCP",
        "CapabilityReuseRate", "RepeatedGenerationRate",
        "HumanEditCount", "TokenUsage",
        # Cross-listed
        "TaskSuccessRate", "TimeToCompletion",
    ):
        assert n in names, n


def test_format_missing_reject(fixture_1_db: Path) -> None:
    """Omitting `--format` → argparse exit 2 (no default; silent-CSV hazard)."""
    r = _run(["extract", "--observer-db", str(fixture_1_db)])
    assert r.returncode == 2


def test_format_typo_reject(fixture_1_db: Path) -> None:
    """`--format js` → exit 2."""
    r = _run(["extract", "--observer-db", str(fixture_1_db), "--format", "js"])
    assert r.returncode == 2


def test_out_stdout_when_omitted(fixture_1_db: Path) -> None:
    """--out omitted → output goes to stdout."""
    r = _run(["extract", "--observer-db", str(fixture_1_db), "--format", "csv"])
    assert r.returncode == 0
    assert r.stdout.startswith("metric_set,")
