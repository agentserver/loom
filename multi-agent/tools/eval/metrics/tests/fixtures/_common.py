"""Shared helpers for the three fixture builders (spec §4).

Two helpers only:

- `apply_schema(conn)` — read the FULL observer schema.sql (see spec
  §4.4 rationale: applying only WT-1-run-schema DDL would leave
  `route_reasons` absent and #37 would fail with `no such table`).
- `sha256_of(short)` — canonicalize a short-ID literal (like `a1` or
  `h1`) to its 64-hex sha256 per spec §4.1 hash-format note.
- `iso_utc(offset_seconds)` — produce a deterministic ISO-8601 UTC
  timestamp from a fixed 2026-07-02T09:00:00Z base + offset. Used so
  fixture DBs are byte-identical across rebuilds.
"""

from __future__ import annotations

import hashlib
import json
import sqlite3
from datetime import datetime, timedelta, timezone
from pathlib import Path

# Base timestamp for all fixture rows (spec §4 fixture-1 table uses
# end-start durations; we anchor start_time to this base and derive
# end_time = start + duration_seconds).
BASE_TS = datetime(2026, 7, 2, 9, 0, 0, tzinfo=timezone.utc)

# Locate schema.sql relative to this file so a rebuild from any cwd
# still works. Fixture builders run at edit-time (rarely) and at CI
# (each pytest session rebuilds via conftest fixtures) — both need
# the same absolute path.
_SCHEMA_PATH = (
    # __file__: .../multi-agent/tools/eval/metrics/tests/fixtures/_common.py
    # parents: [0]=fixtures, [1]=tests, [2]=metrics, [3]=eval, [4]=tools, [5]=multi-agent
    Path(__file__).resolve().parents[5]
    / "internal" / "observerstore" / "schema.sql"
)


def apply_schema(conn: sqlite3.Connection) -> None:
    """Apply the observer schema.sql to a fresh SQLite connection."""
    with open(_SCHEMA_PATH, "r", encoding="utf-8") as f:
        conn.executescript(f.read())


def sha256_of(short: str) -> str:
    """Return the sha256 hex of the short-ID literal (spec §4.1)."""
    return hashlib.sha256(short.encode()).hexdigest()


def iso_utc(offset_seconds: float) -> str:
    """Return an ISO-8601 UTC timestamp `<BASE_TS + offset>Z` (spec §4)."""
    ts = BASE_TS + timedelta(seconds=offset_seconds)
    # Match the observer writer's format: `2006-01-02T15:04:05Z`.
    # datetime.isoformat emits `2026-07-02T09:00:00+00:00`; we swap the
    # trailing offset for `Z` so downstream `_parse_ts` sees the same
    # shape it does in real DBs.
    return ts.strftime("%Y-%m-%dT%H:%M:%SZ")


def artifact_hashes(shorts: list[str]) -> str:
    """Return the JSON-serialised `artifact_hashes` cell for a run.

    Each entry is canonicalised via sha256_of per spec §4.1. Empty
    list serialises as `[]` (matches the DEFAULT '[]' in the DDL).
    """
    return json.dumps([sha256_of(s) for s in shorts])
