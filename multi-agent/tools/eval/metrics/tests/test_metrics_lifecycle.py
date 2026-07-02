"""Spec §2.1 metrics on fixture 1 (spec §4.1)."""

from __future__ import annotations

import sqlite3
from pathlib import Path

import pytest

from tests._extract_helpers import extract_dict, load_expected

APPROX = pytest.approx


def test_fixture_1_task_success_rate(fixture_1_db: Path) -> None:
    d = extract_dict(fixture_1_db, "lifecycle")
    assert d["metrics"]["TaskSuccessRate"] == APPROX(0.6)


def test_fixture_1_lifecycle_closure_rate(fixture_1_db: Path) -> None:
    d = extract_dict(fixture_1_db, "lifecycle")
    assert d["metrics"]["LifecycleClosureRate"] == APPROX(0.6)


def test_fixture_1_time_to_completion(fixture_1_db: Path) -> None:
    d = extract_dict(fixture_1_db, "lifecycle")
    ttc = d["metrics"]["TimeToCompletion"]
    assert ttc["mean_seconds"] == APPROX(29.2)
    assert ttc["p50_seconds"] == APPROX(19.0)
    assert ttc["p95_seconds"] == APPROX(76.5)
    assert ttc["count"] == 10


def test_fixture_1_human_context_selection_count(fixture_1_db: Path) -> None:
    d = extract_dict(fixture_1_db, "lifecycle")
    assert d["metrics"]["HumanContextSelectionCount"] == 8


def test_fixture_1_wrong_context_failure_rate(fixture_1_db: Path) -> None:
    d = extract_dict(fixture_1_db, "lifecycle")
    # rows 3 (wrong-context), 5 (missing-file), 9 (wrong-version)
    assert d["metrics"]["WrongContextFailureRate"] == APPROX(0.3)


def test_fixture_1_artifact_correctness_rate(fixture_1_db: Path) -> None:
    d = extract_dict(fixture_1_db, "lifecycle")
    # 6 rows w/ non-empty artifacts; all passed.
    assert d["metrics"]["ArtifactCorrectnessRate"] == APPROX(1.0)


def test_fixture_1_manual_setup_step_count_null(fixture_1_db: Path) -> None:
    d = extract_dict(fixture_1_db, "lifecycle")
    assert d["metrics"]["ManualSetupStepCount"] is None
    assert d["notes"]["ManualSetupStepCount"] == "upstream data missing"


def test_fixture_1_config_touch_count_null(fixture_1_db: Path) -> None:
    d = extract_dict(fixture_1_db, "lifecycle")
    assert d["metrics"]["ConfigTouchCount"] is None
    assert d["notes"]["ConfigTouchCount"] == "upstream data missing"


def test_fixture_1_state_continuity_rate_null(fixture_1_db: Path) -> None:
    d = extract_dict(fixture_1_db, "lifecycle")
    assert d["metrics"]["StateContinuityRate"] is None
    assert d["notes"]["StateContinuityRate"] == "upstream data missing"


def test_fixture_1_hash_canonicalization(fixture_1_db: Path) -> None:
    """artifact_hashes / task_contract_hash MUST be 64-hex sha256 (spec §4.1)."""
    conn = sqlite3.connect(str(fixture_1_db))
    rows = conn.execute(
        "SELECT artifact_hashes, task_contract_hash FROM runs"
    ).fetchall()
    conn.close()
    import json as _json
    import re
    hex64 = re.compile(r"^[a-f0-9]{64}$")
    for arts_json, contract in rows:
        for h in _json.loads(arts_json):
            assert hex64.match(h), f"artifact hash not 64-hex: {h}"
        if contract:
            assert hex64.match(contract), f"contract hash not 64-hex: {contract}"


def test_fixture_1_expected_json_matches(fixture_1_db: Path, fixture_dir: Path) -> None:
    """The hand-computed expected_1.json must line up with real extractor output."""
    d = extract_dict(fixture_1_db, "lifecycle")
    exp = load_expected(fixture_dir, 1)
    for k, v in exp.items():
        got = d["metrics"][k]
        if isinstance(v, dict):
            for sk, sv in v.items():
                if sv is None:
                    assert got[sk] is None
                else:
                    assert got[sk] == APPROX(sv), f"{k}.{sk}"
        elif v is None:
            assert got is None, k
        else:
            assert got == APPROX(v), k
