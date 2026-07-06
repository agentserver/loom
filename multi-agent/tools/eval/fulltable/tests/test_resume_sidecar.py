"""Spec §4.5 — resume sidecar rsplit rule."""
from __future__ import annotations

import pytest

from lib.plan import (
    parse_resume_key,
    resume_key_for_e4_row,
    resume_key_for_matrix_row,
)


def test_matrix_key_shape():
    row = {"workload_id": "cross-device-code-mod",
           "baseline_or_ablation": "full_loom"}
    k = resume_key_for_matrix_row(row)
    assert k == "matrix__cross-device-code-mod__full_loom"


def test_e4_key_shape():
    row = {"family": "csv-profiler", "stage": "A",
           "task_id": "first-task", "configuration": "full_loom"}
    k = resume_key_for_e4_row(row)
    assert k == "e4__csv-profiler__A__first-task__full_loom"


@pytest.mark.parametrize("row_dict, expected_key", [
    ({"workload_id": "credential-bound-model",
      "baseline_or_ablation": "NoAcceptanceGate"},
     "matrix__credential-bound-model__NoAcceptanceGate"),
    ({"family": "log-parser", "stage": "C",
      "task_id": "reuse-2", "configuration": "NoRegistryLookup"},
     "e4__log-parser__C__reuse-2__NoRegistryLookup"),
])
def test_rsplit_recovery(row_dict, expected_key):
    if "workload_id" in row_dict:
        k = resume_key_for_matrix_row(row_dict)
    else:
        k = resume_key_for_e4_row(row_dict)
    assert k == expected_key
    fake_uuid = "01234567-89ab-cdef-0123-456789abcdef"
    basename = f"{k}__{fake_uuid}.done"
    recovered = parse_resume_key(basename)
    assert recovered == expected_key


def test_rsplit_survives_double_underscore_in_workload():
    """Workload ids and baseline names use hyphens/underscores freely;
    rsplit(1) always splits only the LAST separator, so the resume_key
    body's own `__` separators are preserved.
    """
    key = "matrix__cross-device-code-mod__full_loom"
    fake_uuid = "abcd-efgh"
    assert parse_resume_key(f"{key}__{fake_uuid}.done") == key


def test_e4_key_survives_all_four_underscores():
    key = "e4__api-wrapper-for-local-service__B__reuse-3__NoUserPromotionPath"
    assert parse_resume_key(f"{key}__anything.done") == key
