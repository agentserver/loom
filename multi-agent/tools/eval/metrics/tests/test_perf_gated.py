"""Spec §7 (h) CI-conditional perf assertion.

Skipped by default; runs when `-m perf` is passed OR `CI=true` in env.
"""

from __future__ import annotations

import os
import sqlite3
import time
from pathlib import Path

import pytest

from tests.fixtures._common import apply_schema
from tests._extract_helpers import extract_dict


_PERF_ENABLED = os.environ.get("CI") == "true"


@pytest.mark.perf
@pytest.mark.skipif(not _PERF_ENABLED, reason="perf mark; enable with CI=true")
def test_extract_10k_rows_completes_under_2s(tmp_path: Path) -> None:
    db = tmp_path / "perf.db"
    conn = sqlite3.connect(str(db))
    apply_schema(conn)
    # 10k synthetic rows — matches spec §6 non-goals upper bound.
    rows = []
    for i in range(10_000):
        rows.append((
            f"run-{i}", f"wl-{i}", f"cl-{i}",
            "E1", "FullLoom",
            "c", "c", "c", "c",
            "t", "",
            f"cs-{i}", "", "",
            "s", "",
            "2026-07-02T09:00:00Z", "2026-07-02T09:00:10Z",
            "pass", "",
            0, "[]", "/t", "",
        ))
    conn.executemany(
        "INSERT INTO runs VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
        rows,
    )
    conn.commit()
    conn.close()

    t0 = time.time()
    d = extract_dict(db, "full")
    elapsed = time.time() - t0
    assert d["row_count"] == 10_000
    assert elapsed < 2.0, f"10k-row extract took {elapsed:.2f}s (> 2s)"
