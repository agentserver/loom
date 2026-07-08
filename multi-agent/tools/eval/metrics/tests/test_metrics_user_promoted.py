"""Spec §2.3 metrics on fixture 3 (spec §4.3). All 13 assert null today."""

from __future__ import annotations

import sqlite3
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


def test_token_usage_sums_landed_codex_columns(empty_db: Path) -> None:
    conn = sqlite3.connect(str(empty_db))
    conn.executemany(
        "INSERT INTO runs ("
        " run_id, workload_id, claim_id, experiment_id, baseline_or_ablation,"
        " loom_commit, agentserver_commit, modelserver_commit, app_commit,"
        " machine_topology, context_ground_truth, selected_context,"
        " ground_truth_context, start_time, end_time, success_oracle_result,"
        " model_input_tokens, model_output_tokens"
        ") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
        [
            (
                "run-token-01", "wl-token", "C41", "E-token", "full_loom",
                "loom", "agentserver", "modelserver", "app",
                "linux/amd64", "", "driver", "driver",
                "2026-07-08T00:00:00Z", "2026-07-08T00:00:10Z", "pass",
                100, 40,
            ),
            (
                "run-token-02", "wl-token", "C41", "E-token", "full_loom",
                "loom", "agentserver", "modelserver", "app",
                "linux/amd64", "", "driver", "driver",
                "2026-07-08T00:01:00Z", "2026-07-08T00:01:10Z", "pass",
                7, 5,
            ),
        ],
    )
    conn.commit()
    conn.close()

    d = extract_dict(empty_db, "user-promoted")

    assert d["metrics"]["TokenUsage"] == {
        "input_tokens": 107,
        "output_tokens": 45,
        "count": 2,
    }
    assert "TokenUsage" not in d["notes"]
