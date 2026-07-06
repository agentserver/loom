"""Spec §5 (Figure 1/2/3/4) + §7 (i)."""
from __future__ import annotations

import json
import subprocess
import sys
from pathlib import Path

import pytest

from conftest import FULLTABLE_DIR, MODULE_ROOT
from lib import paper_tables

FIXTURE = FULLTABLE_DIR / "tests" / "fixtures" / "fake"

ABLATIONS = (
    "NoCapabilityDiscovery", "NoTypedContracts", "NoDryRun",
    "NoContractFormalization", "NoUserPromotionPath",
    "NoAcceptanceGate", "NoRegistryLookup", "NoObserver",
)


def _run(tmp_path: Path):
    out_dir = tmp_path / "paper"
    out_dir.mkdir()
    cmd = [
        sys.executable, str(FULLTABLE_DIR / "build_paper_tables.py"),
        "--metrics", str(FIXTURE / "metrics.csv"),
        "--runs", str(FIXTURE / "runs.csv"),
        "--ablation-mapping", str(FULLTABLE_DIR / "ablation_mapping.yaml"),
        "--e4-stages", str(FULLTABLE_DIR / "e4_stages.yaml"),
        "--out-dir", str(out_dir),
        "--sample-mode",
    ]
    subprocess.run(cmd, check=True, capture_output=True, text=True)
    return out_dir


# --- Figure 1 ---------------------------------------------------------------


def test_figure1_configurations(tmp_path):
    out = _run(tmp_path)
    data = json.loads((out / "figure1_data_sample.json").read_text())
    expected = ["full_loom"] + list(ABLATIONS)
    assert data["configurations"] == expected
    for m, series in data["series"].items():
        assert set(series.keys()) == set(expected), (
            f"figure1 series {m}: keys {sorted(series)} != {sorted(expected)}"
        )


def test_figure1_series_metric_names(tmp_path):
    from eval_metrics.metrics import ALL_METRICS
    known = {m.name for m in ALL_METRICS}
    out = _run(tmp_path)
    data = json.loads((out / "figure1_data_sample.json").read_text())
    for m in data["series"]:
        assert m in known


# --- Figure 2 ---------------------------------------------------------------


def test_figure2_fault_types_match_source(tmp_path):
    out = _run(tmp_path)
    data = json.loads((out / "figure2_data_sample.json").read_text())
    expected = paper_tables.parse_all_fault_kinds(MODULE_ROOT)
    assert data["fault_types"] == expected
    # Order matters — 08 号 §Figure 2 caption expects declaration order.
    for m, series in data["series"].items():
        assert list(series.keys()) == expected


# --- Figure 3 ---------------------------------------------------------------


def test_figure3_families_and_stage_metrics(tmp_path):
    out = _run(tmp_path)
    data = json.loads((out / "figure3_data_sample.json").read_text())
    expected_families = sorted(paper_tables.FAMILIES_SORTED)
    assert data["families"] == list(paper_tables.FAMILIES_SORTED)
    # families sorted list check
    assert sorted(data["families"]) == expected_families

    assert data["stage_A"]["metric"] == "TimeToCompletion"
    assert data["stage_A"]["subkey"] == "p50_seconds"
    assert data["stage_B"]["metric"] == "TimeFromUserDecisionToRegisteredMCP"
    assert data["stage_B"]["subkey"] == "p50_seconds"
    assert data["stage_C"]["metric"] == "ReuseSpeedup"
    assert data["stage_C"]["subkey"] == "ratio"

    for st in ("stage_A", "stage_B", "stage_C"):
        assert set(data[st]["values"].keys()) == set(paper_tables.FAMILIES_SORTED)

    assert data["AdHocScriptTaskShare"]["subkey"] == "share_0_to_1"


# --- Figure 4 ---------------------------------------------------------------


def test_figure4_components_and_extras_exact(tmp_path):
    out = _run(tmp_path)
    data = json.loads((out / "figure4_data_sample.json").read_text())
    assert data["components"] == list(paper_tables.FIGURE4_COMPONENTS)
    assert len(data["components"]) == 5
    assert set(data["extras"].keys()) == set(paper_tables.FIGURE4_EXTRAS)
    art = data["extras"]["ArtifactTransferThroughput"]
    assert art["artifact_size_bytes"] == [1024, 1048576, 104857600]
    rl = data["extras"]["RoutingLatencyP50P95"]["scale_points"]
    assert rl["contexts"] == [1, 2, 4, 8, 16]
    assert rl["tools_per_context"] == [10, 50, 100]
    assert rl["artifact_size_bytes"] == [1024, 1048576, 104857600]


# --- e4_stages_sample.csv ---------------------------------------------------


def test_e4_stages_sample_shape(tmp_path):
    import csv
    from lib import stage_e4
    out = _run(tmp_path)
    with (out / "e4_stages_sample.csv").open() as f:
        rows = list(csv.reader(f))
    assert rows[0] == list(stage_e4.COLUMNS)
    assert len(rows) == 1 + 240
    from eval_metrics.metrics import ALL_METRICS
    known = {m.name for m in ALL_METRICS}
    for col in rows[0][4:]:
        assert col in known
