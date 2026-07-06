"""Spec §3.4 + §5 + §7 (i) — Table 2 ablation long-form shape."""
from __future__ import annotations

import csv
import subprocess
import sys
from pathlib import Path

import pytest
import yaml

from conftest import FULLTABLE_DIR

FIXTURE = FULLTABLE_DIR / "tests" / "fixtures" / "fake"


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


def _first_appearance_union():
    mapping = yaml.safe_load((FULLTABLE_DIR / "ablation_mapping.yaml").read_text())
    seen = []
    for entry in mapping:
        for m in entry["target_metrics"]:
            if m not in seen:
                seen.append(m)
    return seen


def test_8_rows(tmp_path):
    out = _run(tmp_path)
    with (out / "table2_ablation_sample.csv").open() as f:
        rows = list(csv.reader(f))
    assert len(rows) == 1 + 8  # header + 8 data rows


def test_header_first_appearance_order(tmp_path):
    out = _run(tmp_path)
    with (out / "table2_ablation_sample.csv").open() as f:
        header = next(csv.reader(f))
    expected = ["ablation_flag", "difficulty_group"] + _first_appearance_union()
    assert header == expected


def test_metric_columns_in_all_metrics(tmp_path):
    from eval_metrics.metrics import ALL_METRICS
    known = {m.name for m in ALL_METRICS}
    out = _run(tmp_path)
    with (out / "table2_ablation_sample.csv").open() as f:
        header = next(csv.reader(f))
    for col in header[2:]:
        assert col in known


def test_difficulty_column_values(tmp_path):
    out = _run(tmp_path)
    with (out / "table2_ablation_sample.csv").open() as f:
        rows = list(csv.DictReader(f))
    for r in rows:
        assert r["difficulty_group"] in {"难点一", "难点二", "难点三"}
