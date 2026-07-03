"""Spec §2.2 metrics on fixture 2 (spec §4.2). All 7 assert null today."""

from __future__ import annotations

from pathlib import Path

from tests._extract_helpers import extract_dict


_ALL_CONTRACTED = [
    "ContractCompleteness",
    "PreExecutionFaultCatchRate",
    "ContractViolationRate",
    "MissingArtifactDetectionRate",
    "PolicyViolationPreventionRate",
    "RecoverySuccessRate",
    "DuplicateSideEffectRate",
]


def test_fixture_2_all_contracted_null(fixture_2_db: Path) -> None:
    d = extract_dict(fixture_2_db, "contracted")
    for name in _ALL_CONTRACTED:
        assert d["metrics"][name] is None, name
        assert d["notes"].get(name) == "upstream data missing", name
