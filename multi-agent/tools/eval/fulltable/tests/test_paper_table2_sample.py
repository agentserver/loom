"""Spec §5.1 + §7 (i) — Table 2 sample CSV shape."""
from __future__ import annotations

import csv
import subprocess
import sys
from pathlib import Path

import pandas as pd
import pytest

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


def test_table2_header_exact(tmp_path):
    out = _run(tmp_path)
    with (out / "table2_sample.csv").open() as f:
        header = next(csv.reader(f))
    assert header == [
        "workload_id", "configuration", "TaskSuccessRate", "TimeToCompletion",
        "HumanContextSelectionCount", "WrongContextFailureRate",
        "LifecycleClosureRate", "run_count",
    ]


def test_metric_columns_in_all_metrics(tmp_path):
    from eval_metrics.metrics import ALL_METRICS
    known = {m.name for m in ALL_METRICS}
    out = _run(tmp_path)
    with (out / "table2_sample.csv").open() as f:
        header = next(csv.reader(f))
    for col in header[2:-1]:
        assert col in known


def test_round_trip_pd_read_csv(tmp_path):
    out = _run(tmp_path)
    df = pd.read_csv(out / "table2_sample.csv")
    assert list(df.columns) == [
        "workload_id", "configuration", "TaskSuccessRate", "TimeToCompletion",
        "HumanContextSelectionCount", "WrongContextFailureRate",
        "LifecycleClosureRate", "run_count",
    ]
    # Three fixture runs → three (workload_id, full_loom) cells.
    assert len(df) == 3
