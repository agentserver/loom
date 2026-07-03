"""Spec §2.4 metrics on fixture 3 (spec §4.3)."""

from __future__ import annotations

from pathlib import Path

import pytest

from tests._extract_helpers import extract_dict


def test_fixture_3_routing_accuracy(fixture_3_db: Path) -> None:
    d = extract_dict(fixture_3_db, "semantic")
    # rows 1, 3, 4 match; row 2 wrong; row 5 excluded (empty ground truth)
    assert d["metrics"]["RoutingAccuracy"] == pytest.approx(3 / 4)


def test_fixture_3_capability_recall_null(fixture_3_db: Path) -> None:
    d = extract_dict(fixture_3_db, "semantic")
    assert d["metrics"]["CapabilityRecall"] is None
    assert d["notes"]["CapabilityRecall"] == "upstream data missing"


def test_fixture_3_capability_precision_null(fixture_3_db: Path) -> None:
    d = extract_dict(fixture_3_db, "semantic")
    assert d["metrics"]["CapabilityPrecision"] is None
    assert d["notes"]["CapabilityPrecision"] == "upstream data missing"
