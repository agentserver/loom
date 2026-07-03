"""Pytest fixture plumbing.

Provides one copy of each `fixture_N.db` per test, via a
session-scoped source-of-truth + a per-test tmp_path copy. Tests
should NOT open the source fixture directly — they should use the
`fixture_N_db` fixture which gives them a writable-in-name-only
sandbox copy (all metrics open the DB read-only, but keeping tests
strictly file-per-invocation avoids surprises when a follow-up test
mutates the DB, e.g. via the fault-injection tests we might add
later).

Also registers the `linux_only` and `perf` marks (see pyproject
markers table).
"""

from __future__ import annotations

import shutil
import sys
from pathlib import Path

import pytest

# Make sure `eval_metrics` and `tests.fixtures.*` import from THIS
# checkout even if a copy was installed system-wide. pyproject already
# scopes package discovery, but the fixture-builder modules under
# `tests/fixtures/*` are not installed, and pytest's rootdir handling
# does not put the project root on sys.path unless we ask.
_PROJECT_ROOT = Path(__file__).resolve().parents[1]
if str(_PROJECT_ROOT) not in sys.path:
    sys.path.insert(0, str(_PROJECT_ROOT))


_FIXTURE_DIR = Path(__file__).resolve().parent / "fixtures"


@pytest.fixture(scope="session")
def fixture_dir() -> Path:
    """Return the on-disk fixture directory path (session-scoped)."""
    return _FIXTURE_DIR


def _copy_fixture(name: str, tmp_path: Path) -> Path:
    """Copy `fixtures/<name>` into tmp_path and return the copy path."""
    src = _FIXTURE_DIR / name
    dst = tmp_path / name
    shutil.copy2(src, dst)
    return dst


@pytest.fixture
def fixture_1_db(tmp_path: Path) -> Path:
    return _copy_fixture("fixture_1.db", tmp_path)


@pytest.fixture
def fixture_2_db(tmp_path: Path) -> Path:
    return _copy_fixture("fixture_2.db", tmp_path)


@pytest.fixture
def fixture_3_db(tmp_path: Path) -> Path:
    return _copy_fixture("fixture_3.db", tmp_path)


@pytest.fixture
def empty_db(tmp_path: Path) -> Path:
    """Return a freshly-created SQLite DB with the FULL observer schema.

    Distinct from a fixture_N copy because #37 RoutingLatencyP50P95
    branches on `route_reasons` MISSING vs EMPTY (spec §4.4); this
    fixture applies the schema so the table is EMPTY (not MISSING).
    """
    import sqlite3

    from tests.fixtures._common import apply_schema

    dst = tmp_path / "empty.db"
    conn = sqlite3.connect(str(dst))
    apply_schema(conn)
    conn.close()
    return dst
