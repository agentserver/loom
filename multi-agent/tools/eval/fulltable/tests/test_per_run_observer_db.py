"""Spec §7 (d) — per-run observer SQLite files are distinct."""
from __future__ import annotations

from pathlib import Path

from conftest import MODULE_ROOT

SMOKE_DBS = MODULE_ROOT / "tests" / "eval" / "results" / "smoke" / "dbs"


def test_three_distinct_db_files():
    dbs = sorted(SMOKE_DBS.glob("*.db"))
    assert len(dbs) == 3, f"expected 3 db files, got {[d.name for d in dbs]}"
    names = {d.name for d in dbs}
    assert len(names) == 3
    # UUIDv4 shape: 36 chars ± ".db" suffix.
    for d in dbs:
        stem = d.stem
        assert len(stem) == 36, f"expected UUID stem, got {stem}"


def test_db_paths_under_smoke_root():
    """Sanity: DB files never escape the smoke/ directory."""
    for d in SMOKE_DBS.glob("*.db"):
        assert "tests/eval/results/smoke/dbs" in str(d)
