"""Spec §3.1 + §3.2 + §7 (c) + §7 (g).

matrix.yaml is exactly 60 rows; every workload_id ∈ 5-value enum;
every baseline_or_ablation ∈ 12-value enum; typo `NoAccetpanceGate`
rejects; a `cloud_sandbox_e2b` row without `dry_run: true` rejects;
no duplicate (workload_id, baseline_or_ablation) rows.
"""
from __future__ import annotations

import copy
import json
from pathlib import Path

import jsonschema
import pytest
import yaml

from conftest import FULLTABLE_DIR

MATRIX_PATH = FULLTABLE_DIR / "matrix.yaml"
SCHEMA_PATH = FULLTABLE_DIR / "matrix.schema.json"

WORKLOADS = [
    "cross-device-code-mod",
    "remote-data-processing",
    "windows-only-artifact",
    "missing-parser-converter",
    "credential-bound-model",
]
CONFIGS = [
    "full_loom",
    "NoCapabilityDiscovery",
    "NoTypedContracts",
    "NoDryRun",
    "NoContractFormalization",
    "NoUserPromotionPath",
    "NoAcceptanceGate",
    "NoRegistryLookup",
    "NoObserver",
    "manual_ssh",
    "single_machine_codex",
    "cloud_sandbox_e2b",
]


def _load_matrix():
    return yaml.safe_load(MATRIX_PATH.read_text())


def _load_schema():
    return json.loads(SCHEMA_PATH.read_text())


def _validate(matrix, schema):
    jsonschema.validate(matrix, schema)


def test_60_rows():
    matrix = _load_matrix()
    assert isinstance(matrix, list)
    assert len(matrix) == 60


def test_workload_enum():
    matrix = _load_matrix()
    for row in matrix:
        assert row["workload_id"] in WORKLOADS


def test_baseline_or_ablation_enum():
    matrix = _load_matrix()
    for row in matrix:
        assert row["baseline_or_ablation"] in CONFIGS


def test_row_order_configuration_outer_workload_inner():
    """Spec §4.2: row order = §3.2 configuration order × §3.1 workload order."""
    matrix = _load_matrix()
    expected = [(c, w) for c in CONFIGS for w in WORKLOADS]
    actual = [(r["baseline_or_ablation"], r["workload_id"]) for r in matrix]
    assert actual == expected


def test_first_three_rows_are_full_loom():
    """--sample 3 must pick 3 full_loom rows (spec §4.2, plan §Step 1.4)."""
    matrix = _load_matrix()
    for row in matrix[:3]:
        assert row["baseline_or_ablation"] == "full_loom"


def test_typo_rejected():
    schema = _load_schema()
    matrix = _load_matrix()
    matrix[6]["baseline_or_ablation"] = "NoAccetpanceGate"
    with pytest.raises(jsonschema.ValidationError):
        _validate(matrix, schema)


def test_cloud_rows_dry_run_true():
    matrix = _load_matrix()
    cloud_rows = [r for r in matrix if r["baseline_or_ablation"] == "cloud_sandbox_e2b"]
    assert len(cloud_rows) == 5
    for r in cloud_rows:
        assert r.get("dry_run") is True


def test_cloud_row_without_dry_run_rejected():
    schema = _load_schema()
    matrix = _load_matrix()
    for row in matrix:
        if row["baseline_or_ablation"] == "cloud_sandbox_e2b":
            del row["dry_run"]
            break
    with pytest.raises(jsonschema.ValidationError):
        _validate(matrix, schema)


def test_no_duplicate_rows():
    matrix = _load_matrix()
    keys = [(r["baseline_or_ablation"], r["workload_id"]) for r in matrix]
    assert len(set(keys)) == 60


def test_schema_valid_on_correct_yaml():
    schema = _load_schema()
    matrix = _load_matrix()
    _validate(matrix, schema)
