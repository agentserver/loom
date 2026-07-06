"""Spec §4.4 — e4_stages.yaml full-Cartesian coverage.

240 rows = 5 families × 3 stages × 4 tasks × 4 configurations. The set
of (family, stage, task_id, configuration) tuples equals the exact
product; a per-family `acceptance` entry rejects; a row with a
non-E4 configuration (e.g. `manual_ssh`) rejects.
"""
from __future__ import annotations

import copy
import json
from itertools import product
from pathlib import Path

import jsonschema
import pytest
import yaml

from conftest import FULLTABLE_DIR, MODULE_ROOT

E4_PATH = FULLTABLE_DIR / "e4_stages.yaml"
SCHEMA_PATH = FULLTABLE_DIR / "e4_stages.schema.json"
GOLDEN_DIR = MODULE_ROOT / "tests" / "eval" / "golden"

FAMILIES = [
    "api-wrapper-for-local-service",
    "csv-profiler",
    "image-metadata-extractor",
    "log-parser",
    "refund-policy-checker",
]
STAGES = ["A", "B", "C"]
TASKS = ["first-task", "reuse-1", "reuse-2", "reuse-3"]
CONFIGURATIONS = ["full_loom", "NoUserPromotionPath", "NoAcceptanceGate", "NoRegistryLookup"]


def _load():
    return yaml.safe_load(E4_PATH.read_text())


def _schema():
    return json.loads(SCHEMA_PATH.read_text())


def test_240_rows():
    data = _load()
    assert len(data) == 240


def test_full_cartesian_coverage():
    data = _load()
    tuples = {(r["family"], r["stage"], r["task_id"], r["configuration"]) for r in data}
    expected = set(product(FAMILIES, STAGES, TASKS, CONFIGURATIONS))
    assert tuples == expected
    # And no duplicates.
    assert len(data) == len(tuples)


def test_families_match_golden_dir():
    """spec §4.4 — family set comes from golden/ dirs minus non-family entries."""
    non_family = {"_shared", "acceptance", "README.md"}
    dirs = sorted(
        e.name for e in GOLDEN_DIR.iterdir()
        if e.is_dir() and e.name not in non_family
    )
    assert set(FAMILIES) == set(dirs)


def test_schema_valid():
    schema = _schema()
    jsonschema.validate(_load(), schema)


def test_acceptance_entry_rejected():
    schema = _schema()
    data = _load()
    bad = copy.deepcopy(data)
    bad[0]["family"] = "acceptance"
    with pytest.raises(jsonschema.ValidationError):
        jsonschema.validate(bad, schema)


def test_bad_configuration_rejected():
    schema = _schema()
    data = _load()
    bad = copy.deepcopy(data)
    bad[0]["configuration"] = "manual_ssh"
    with pytest.raises(jsonschema.ValidationError):
        jsonschema.validate(bad, schema)
