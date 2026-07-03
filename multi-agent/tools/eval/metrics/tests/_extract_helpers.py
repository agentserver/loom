"""Shared test-side helpers for CLI-driven end-to-end tests.

Kept in a private `_`-prefixed module so pytest does NOT collect it as
a test file (name doesn't start with `test_`).
"""

from __future__ import annotations

import io
import json
import sqlite3
import subprocess
import sys
from pathlib import Path

from eval_metrics import csv_out, json_out
from eval_metrics.db import open_observer_db
from eval_metrics.metrics import Context, metrics_for_set
from eval_metrics.paths import validate_observer_db


def run_cli(args: list[str]) -> subprocess.CompletedProcess:
    """Invoke the CLI in a subprocess (real exit codes, real stderr)."""
    return subprocess.run(
        [sys.executable, "-m", "eval_metrics", *args],
        capture_output=True, text=True,
    )


def extract_dict(db_path: Path, metric_set: str = "full") -> dict:
    """In-process extract → JSON dict. Faster than subprocess for large batches."""
    resolved, fd = validate_observer_db(str(db_path))
    conn = open_observer_db(resolved, fd)
    runs = conn.execute("SELECT * FROM runs").fetchall()
    metrics = metrics_for_set(metric_set)
    ctx = Context(conn=conn, runs=runs, row_count=len(runs))
    results = {m.name: m.compute(ctx) for m in metrics}
    obj = json_out.build_json_object(metric_set, len(runs), metrics, results)
    conn.close()
    return obj


def extract_csv_rows(db_path: Path, metric_set: str = "full") -> tuple[list[str], list[str]]:
    """In-process extract → (header, data) list-of-strings tuple."""
    resolved, fd = validate_observer_db(str(db_path))
    conn = open_observer_db(resolved, fd)
    runs = conn.execute("SELECT * FROM runs").fetchall()
    metrics = metrics_for_set(metric_set)
    ctx = Context(conn=conn, runs=runs, row_count=len(runs))
    results = {m.name: m.compute(ctx) for m in metrics}
    buf = io.StringIO()
    csv_out.write_csv(buf, metric_set, len(runs), metrics, results)
    conn.close()
    import csv as _csv
    reader = list(_csv.reader(buf.getvalue().splitlines()))
    return reader[0], reader[1]


def load_expected(fixture_dir: Path, n: int) -> dict:
    return json.loads((fixture_dir / f"expected_{n}.json").read_text())
