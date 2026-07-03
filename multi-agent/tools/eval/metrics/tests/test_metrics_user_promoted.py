"""Spec §2.3 metrics on fixture 3 (spec §4.3). All 13 assert null today."""

from __future__ import annotations

from pathlib import Path

from tests._extract_helpers import extract_dict


_ALL_USER_PROMOTED = [
    "PromotionCandidateSurfacingRate",
    "UserInitiatedSynthesisSuccessRate",
    "ValidationFalseAcceptRate",
    "TimeFromUserDecisionToRegisteredMCP",
    "RegistryLookupHitRate",
    "CapabilityReuseRate",
    "RepeatedGenerationRate",
    "PromotionAdoptionRate",
    "AdHocScriptTaskShare",
    "GeneratedCapabilityDefectRate",
    "ReuseSpeedup",
    "HumanEditCount",
    "TokenUsage",
]


_STRUCTURED = {
    "TimeFromUserDecisionToRegisteredMCP": (
        "p50_seconds", "p95_seconds", "mean_seconds", "count",
    ),
    "TokenUsage": ("input_tokens", "output_tokens", "count"),
}


def test_all_13_user_promoted_null(fixture_3_db: Path) -> None:
    d = extract_dict(fixture_3_db, "user-promoted")
    for name in _ALL_USER_PROMOTED:
        val = d["metrics"][name]
        if name in _STRUCTURED:
            assert isinstance(val, dict), name
            for sk in _STRUCTURED[name]:
                assert val[sk] is None, f"{name}.{sk}"
        else:
            assert val is None, name
        assert d["notes"].get(name) == "upstream data missing", name
