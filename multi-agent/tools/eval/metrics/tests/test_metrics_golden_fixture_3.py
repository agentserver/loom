"""Spec §4.3 fixture 3 arithmetic — 8 populated + 33 null = 41 total."""

from __future__ import annotations

from pathlib import Path

from eval_metrics.metrics import ALL_METRICS
from tests._extract_helpers import extract_dict


def _is_null(value) -> bool:
    """Return True iff `value` is None or a dict of all-Nones (structured null)."""
    if value is None:
        return True
    if isinstance(value, dict):
        return all(v is None for v in value.values())
    return False


def test_fixture_3_full_arithmetic(fixture_3_db: Path) -> None:
    d = extract_dict(fixture_3_db, "full")
    assert len(d["metrics"]) == 41

    populated = [m.name for m in ALL_METRICS if not _is_null(d["metrics"][m.name])]
    nulls = [m.name for m in ALL_METRICS if _is_null(d["metrics"][m.name])]
    assert len(populated) == 8, f"expected 8 populated, got {len(populated)}: {populated}"
    assert len(nulls) == 33, f"expected 33 null, got {len(nulls)}"

    # Every null must have exactly one note from the closed set.
    for name in nulls:
        note = d["notes"].get(name)
        assert note in ("upstream data missing", "denominator zero"), (name, note)
