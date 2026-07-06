#!/usr/bin/env python3
"""Collector for canonical_key = contexts_count.

Reads an observer sqlite `route_reasons` table and reports the count of
distinct slave_id (or capability_snapshot_hash as fallback) for a given
workload_id. Emits canonical JSON.

See docs/specs/wt3-mini-case.spec.md §3.4 row 1 in the paper_writing
worktree for the schema and behaviour contract.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import sqlite3
import sys
from pathlib import Path

CANONICAL_KEY = "contexts_count"
UNIT = "int"


def _sha256_file(p: Path) -> str:
    h = hashlib.sha256()
    with p.open("rb") as f:
        for chunk in iter(lambda: f.read(65536), b""):
            h.update(chunk)
    return h.hexdigest()


def collect(sqlite_path: Path, workload_id: str) -> dict:
    if not sqlite_path.exists():
        print(f"error: sqlite file not found: {sqlite_path}", file=sys.stderr)
        sys.exit(2)
    with sqlite3.connect(sqlite_path) as conn:
        cur = conn.execute(
            "SELECT COUNT(DISTINCT COALESCE(slave_id, capability_snapshot_hash)) "
            "FROM route_reasons WHERE workload_id = ?",
            (workload_id,),
        )
        raw = cur.fetchone()[0]
    if raw is None or raw == 0:
        print(
            f"error: no rows for workload_id={workload_id!r} in {sqlite_path}",
            file=sys.stderr,
        )
        sys.exit(2)
    return {
        "canonical_key": CANONICAL_KEY,
        "workload_id": workload_id,
        "raw_value": int(raw),
        "unit": UNIT,
        "source_file": str(sqlite_path.resolve()),
        "source_sha256": _sha256_file(sqlite_path),
    }


def _cli() -> None:
    p = argparse.ArgumentParser(description="Collector for contexts_count.")
    p.add_argument("--route-reasons", required=True, type=Path)
    p.add_argument("--workload-id", required=True)
    p.add_argument("--out", type=Path, default=None,
                   help="Output JSON file (default: stdout).")
    args = p.parse_args()
    record = collect(args.route_reasons, args.workload_id)
    payload = json.dumps(record, ensure_ascii=False, sort_keys=True)
    if args.out is None:
        print(payload)
    else:
        args.out.write_text(payload + "\n", encoding="utf-8")


if __name__ == "__main__":
    _cli()
