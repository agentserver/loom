"""Spec §3.3 JSON shape."""

from __future__ import annotations

import io
import json
from pathlib import Path

from eval_metrics import json_out
from eval_metrics.metrics import Context, MetricResult, metrics_for_set
from tests._extract_helpers import extract_dict


def test_json_object_keys(fixture_1_db: Path) -> None:
    d = extract_dict(fixture_1_db, "full")
    assert list(d.keys()) == ["metric_set", "row_count", "metrics", "notes"]


def test_json_notes_always_present(fixture_1_db: Path) -> None:
    """`notes` key MUST be present, even when empty (spec §3.3)."""
    # An all-populated cohort would produce empty notes; the extractor
    # still emits the key. We don't have such a cohort, but we can
    # exercise the serializer directly with an empty results map.
    obj = json_out.build_json_object(
        metric_set_value="full",
        row_count=0,
        metrics=[],
        results={},
    )
    assert "notes" in obj
    assert obj["notes"] == {}
