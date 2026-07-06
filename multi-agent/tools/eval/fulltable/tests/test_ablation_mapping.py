"""Spec §3.3 — ablation_mapping.yaml matches spec table verbatim."""
from __future__ import annotations

import json
import re
from pathlib import Path

import jsonschema
import pytest
import yaml

from conftest import FULLTABLE_DIR, REPO_ROOT

MAPPING_PATH = FULLTABLE_DIR / "ablation_mapping.yaml"
SCHEMA_PATH = FULLTABLE_DIR / "ablation_mapping.schema.json"
SPEC_PATH = REPO_ROOT / "docs" / "specs" / "wt3-stub-fulltable.spec.md"

FLAGS = {
    "NoCapabilityDiscovery",
    "NoTypedContracts",
    "NoDryRun",
    "NoContractFormalization",
    "NoUserPromotionPath",
    "NoAcceptanceGate",
    "NoRegistryLookup",
    "NoObserver",
}
DIFFICULTIES = {"难点一", "难点二", "难点三"}


def _load():
    return yaml.safe_load(MAPPING_PATH.read_text())


def test_8_rows():
    data = _load()
    assert len(data) == 8


def test_flag_enum():
    data = _load()
    seen = [e["flag"] for e in data]
    assert set(seen) == FLAGS
    assert len(set(seen)) == 8


def test_difficulty_enum():
    data = _load()
    for e in data:
        assert e["difficulty"] in DIFFICULTIES


def test_target_metrics_in_ALL_METRICS():
    from eval_metrics.metrics import ALL_METRICS

    known = {m.name for m in ALL_METRICS}
    data = _load()
    for e in data:
        for m in e["target_metrics"]:
            assert m in known, f"metric {m!r} not in ALL_METRICS"


def test_schema_valid():
    schema = json.loads(SCHEMA_PATH.read_text())
    data = _load()
    jsonschema.validate(data, schema)


# --- spec-table cross-check --------------------------------------------------


_ROW_RE = re.compile(
    r"\|\s*`([A-Za-z]+)`\s*\|\s*(难点[一二三])\s*\|\s*(.+?)\s*\|"
)
_METRIC_RE = re.compile(r"`([A-Za-z0-9_]+)`")
_PARENTH_RE = re.compile(r"\((?:proxied by\s*)?([^)]+)\)")


def _parse_spec_table():
    """Extract the §3.3 mapping table into {flag: (difficulty, [metrics])}."""
    text = SPEC_PATH.read_text()
    # locate the §3.3 table
    start = text.index("| ablation flag")
    end = text.index("This table is normative")
    table = text[start:end]
    out = {}
    for line in table.splitlines():
        m = _ROW_RE.match(line)
        if not m:
            continue
        flag, difficulty, cell = m.group(1), m.group(2), m.group(3)
        # Backtick metric names in the cell.
        metrics = _METRIC_RE.findall(cell)
        # Any parenthetical "(proxied by `X` + `Y`)" already yields those
        # via _METRIC_RE, so the "extends the extracted set by parenthetical
        # metrics" rule falls out automatically. Prose fragments like
        # "下游 side-effect ↑" are non-backticked and dropped.
        # Deduplicate while preserving order.
        seen = []
        for x in metrics:
            if x not in seen:
                seen.append(x)
        out[flag] = (difficulty, seen)
    return out


def test_yaml_matches_spec_table():
    spec_table = _parse_spec_table()
    assert set(spec_table.keys()) == FLAGS, (
        f"parsed spec table flags {sorted(spec_table)} != {sorted(FLAGS)}"
    )
    yaml_by_flag = {e["flag"]: (e["difficulty"], e["target_metrics"]) for e in _load()}
    assert set(yaml_by_flag.keys()) == FLAGS

    for flag in FLAGS:
        spec_diff, spec_metrics = spec_table[flag]
        yaml_diff, yaml_metrics = yaml_by_flag[flag]
        assert yaml_diff == spec_diff, f"{flag}: yaml diff {yaml_diff} != spec {spec_diff}"
        assert set(yaml_metrics) == set(spec_metrics), (
            f"{flag}: yaml metrics {yaml_metrics} != spec extracted {spec_metrics}"
        )
