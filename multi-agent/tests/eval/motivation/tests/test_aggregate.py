"""Aggregate-level tests (spec §5)."""

from __future__ import annotations

import json
import math
import statistics
import subprocess
import sys
from pathlib import Path

import pytest

AGG = "tests/eval/motivation/aggregate.py"


def _write_raw(dirpath: Path, key: str, values):
    dirpath.mkdir(parents=True, exist_ok=True)
    for i, v in enumerate(values):
        obj = {
            "canonical_key": key,
            "raw_value": v,
            "unit": "int",
        }
        (dirpath / f"{key}.rep{i}.json").write_text(
            json.dumps(obj), encoding="utf-8"
        )


def _run(args, cwd: Path):
    return subprocess.run(
        [sys.executable, *args], cwd=str(cwd), capture_output=True, text=True,
    )


def test_aggregate_reject_lt3_raw(ma_root: Path, tmp_path: Path):
    _write_raw(tmp_path, "contexts_count", [3, 4])
    r = _run([AGG, "--canonical-key", "contexts_count",
              "--raw-dir", str(tmp_path)], cwd=ma_root)
    assert r.returncode == 2
    assert "ErrInsufficientSamples" in r.stderr


def test_aggregate_percentile_method_pinned(ma_root: Path, tmp_path: Path):
    _write_raw(tmp_path, "contexts_count", [10, 20, 30])
    out = tmp_path / "out.json"
    r = _run([AGG, "--canonical-key", "contexts_count",
              "--raw-dir", str(tmp_path), "--out", str(out)], cwd=ma_root)
    assert r.returncode == 0, r.stderr
    rec = json.loads(out.read_text())
    q = statistics.quantiles([10, 20, 30], n=4, method="exclusive")
    assert rec["median"] == 20
    assert rec["iqr_low"] == q[0]
    assert rec["iqr_high"] == q[2]
    assert rec["n_samples"] == 3


def test_aggregate_percentile_method_pinned_5_values(ma_root: Path, tmp_path: Path):
    _write_raw(tmp_path, "reuse_time_savings", [10.0, 20.0, 30.0, 40.0, 50.0])
    out = tmp_path / "out.json"
    r = _run([AGG, "--canonical-key", "reuse_time_savings",
              "--raw-dir", str(tmp_path), "--out", str(out)], cwd=ma_root)
    assert r.returncode == 0, r.stderr
    rec = json.loads(out.read_text())
    q = statistics.quantiles([10.0, 20.0, 30.0, 40.0, 50.0], n=4, method="exclusive")
    assert rec["median"] == 30.0
    assert rec["iqr_low"] == q[0]
    assert rec["iqr_high"] == q[2]


def test_aggregate_drops_null_and_rejects_when_lt3(ma_root: Path, tmp_path: Path):
    """spec §5 test_reuse_denominator_zero_null aggregation branch."""
    _write_raw(tmp_path, "reuse_time_savings", [None, 50.0, None])
    r = _run([AGG, "--canonical-key", "reuse_time_savings",
              "--raw-dir", str(tmp_path)], cwd=ma_root)
    assert r.returncode == 2
    assert "ErrInsufficientSamples" in r.stderr


def test_aggregate_drops_null_and_accepts_when_ge3(ma_root: Path, tmp_path: Path):
    _write_raw(tmp_path, "reuse_time_savings", [None, 10.0, 20.0, 30.0])
    out = tmp_path / "out.json"
    r = _run([AGG, "--canonical-key", "reuse_time_savings",
              "--raw-dir", str(tmp_path), "--out", str(out)], cwd=ma_root)
    assert r.returncode == 0, r.stderr
    rec = json.loads(out.read_text())
    assert rec["n_samples"] == 3
    assert "note" in rec
    for k in ("median", "iqr_low", "iqr_high"):
        assert isinstance(rec[k], (int, float))
        assert not math.isnan(rec[k])
        assert not math.isinf(rec[k])
