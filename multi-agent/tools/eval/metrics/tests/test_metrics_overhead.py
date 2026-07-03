"""Spec §2.5 overhead metrics on fixture 3 (spec §4.3)."""

from __future__ import annotations

from pathlib import Path

import pytest

from eval_metrics.metrics import ALL_METRICS
from tests._extract_helpers import extract_dict


def test_fixture_3_routing_latency_p50p95(fixture_3_db: Path) -> None:
    d = extract_dict(fixture_3_db, "overhead")
    rl = d["metrics"]["RoutingLatencyP50P95"]
    # decisions [500k, 1M, 2M, 4M]; median = midpoint = 1.5M;
    # p95 at fractional index 2.85 → 2M + 0.85·2M = 3.7M
    assert rl["p50_ns"] == pytest.approx(1_500_000.0)
    assert rl["p95_ns"] == pytest.approx(3_700_000.0)
    assert rl["count"] == 4


def test_overhead_structured_shapes() -> None:
    """#35 has 5 subkeys, #36 has 6, everything else with subkeys has 3."""
    by_num = {m.number: m for m in ALL_METRICS}
    assert len(by_num[35].subkeys) == 5, "ObserverOverhead must be 5-key"
    assert len(by_num[36].subkeys) == 6, "ModelProxyOverhead must be 6-key"
    for n in (31, 32, 33, 34, 37):
        assert len(by_num[n].subkeys) == 3, f"metric #{n} must be 3-key"


def test_time_to_first_task_null(fixture_3_db: Path) -> None:
    d = extract_dict(fixture_3_db, "overhead")
    assert d["metrics"]["TimeToFirstTask"] is None
    assert d["notes"]["TimeToFirstTask"] == "upstream data missing"


def test_setup_failure_rate_null(fixture_3_db: Path) -> None:
    d = extract_dict(fixture_3_db, "overhead")
    assert d["metrics"]["SetupFailureRate"] is None
    assert d["notes"]["SetupFailureRate"] == "upstream data missing"


def test_overhead_structured_nulls_all_subkeys(fixture_3_db: Path) -> None:
    """Every null overhead structured metric emits every subkey as null."""
    d = extract_dict(fixture_3_db, "overhead")
    by_num = {m.number: m for m in ALL_METRICS}
    for n in (31, 32, 33, 34, 35, 36):
        m = by_num[n]
        val = d["metrics"][m.name]
        assert isinstance(val, dict), m.name
        for k in m.subkeys:
            assert val[k] is None, f"{m.name}.{k}"
        assert d["notes"].get(m.name) == "upstream data missing", m.name
