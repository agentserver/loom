"""Spec §7 (f) and §7 (g) null contract."""

from __future__ import annotations

import subprocess
import sys
from pathlib import Path

from eval_metrics.metrics import ALL_METRICS
from tests._extract_helpers import extract_dict


def _is_null(v) -> bool:
    if v is None:
        return True
    if isinstance(v, dict):
        return all(sv is None for sv in v.values())
    return False


def test_denominator_zero_outputs_null_not_nan(empty_db: Path) -> None:
    """Empty cohort → ratio metrics are null; no NaN/Infinity/0 in output."""
    r = subprocess.run(
        [sys.executable, "-m", "eval_metrics", "extract",
         "--observer-db", str(empty_db), "--format", "csv"],
        capture_output=True, text=True,
    )
    assert r.returncode == 0, r.stderr
    # Ratio cells for e.g. TaskSuccessRate must be empty (blank
    # between commas); "NaN" / "Infinity" must not appear as tokens.
    assert "NaN" not in r.stdout
    assert "Infinity" not in r.stdout


def test_denominator_zero_emits_note(empty_db: Path) -> None:
    d = extract_dict(empty_db, "full")
    # TaskSuccessRate has a landed upstream (runs) so its null on an
    # empty cohort is denominator-zero, not upstream-missing.
    assert d["metrics"]["TaskSuccessRate"] is None
    assert d["notes"]["TaskSuccessRate"] == "denominator zero"


def test_upstream_missing_emits_note(empty_db: Path) -> None:
    d = extract_dict(empty_db, "full")
    assert d["notes"]["ManualSetupStepCount"] == "upstream data missing"


def test_note_reason_closed_set(empty_db: Path) -> None:
    d = extract_dict(empty_db, "full")
    for name, note in d["notes"].items():
        assert note in ("upstream data missing", "denominator zero"), (name, note)


def test_count_metrics_zero_vs_null(empty_db: Path) -> None:
    """Landed count = 0; unlanded count = null (spec §3.3 empty-DB)."""
    d = extract_dict(empty_db, "full")
    # Landed upstream count metric
    assert d["metrics"]["HumanContextSelectionCount"] == 0
    assert "HumanContextSelectionCount" not in d["notes"]
    # Unlanded upstream count metric — null + note
    assert d["metrics"]["ManualSetupStepCount"] is None
    assert d["notes"]["ManualSetupStepCount"] == "upstream data missing"
