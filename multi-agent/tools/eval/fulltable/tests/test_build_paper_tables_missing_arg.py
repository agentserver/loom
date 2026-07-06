"""Spec §5 — argparse rejects a missing required flag with a naming stderr."""
from __future__ import annotations

import subprocess
import sys
from pathlib import Path

import pytest

from conftest import FULLTABLE_DIR

REQUIRED = [
    "--metrics",
    "--runs",
    "--ablation-mapping",
    "--e4-stages",
    "--out-dir",
]


@pytest.mark.parametrize("omit", REQUIRED)
def test_missing_required_arg_exits_and_names_flag(tmp_path, omit):
    args = [
        sys.executable, str(FULLTABLE_DIR / "build_paper_tables.py"),
        "--metrics", "x",
        "--runs", "x",
        "--ablation-mapping", "x",
        "--e4-stages", "x",
        "--out-dir", "x",
        "--sample-mode",
    ]
    # Strip the omitted flag + its value.
    filtered = []
    skip_next = False
    for a in args:
        if skip_next:
            skip_next = False
            continue
        if a == omit:
            skip_next = True
            continue
        filtered.append(a)
    proc = subprocess.run(filtered, capture_output=True, text=True)
    assert proc.returncode != 0
    assert omit in proc.stderr, (
        f"missing-flag error should name {omit!r}; got: {proc.stderr!r}"
    )
