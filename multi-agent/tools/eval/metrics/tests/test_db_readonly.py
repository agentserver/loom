"""Spec §7 (a) read-only DB open + spec §4.4 companion-table branch."""

from __future__ import annotations

import sqlite3
from pathlib import Path

import pytest

from eval_metrics.db import (
    TableStatus,
    companion_table_status,
    open_observer_db,
)
from eval_metrics.paths import validate_observer_db


def _open(db_path: Path):
    resolved, fd = validate_observer_db(str(db_path))
    return open_observer_db(resolved, fd)


def test_readonly_open_mode(fixture_1_db: Path) -> None:
    conn = _open(fixture_1_db)
    (val,) = conn.execute("PRAGMA query_only").fetchone()
    assert val == 1
    conn.close()


def test_update_raises_operational_error(fixture_1_db: Path) -> None:
    conn = _open(fixture_1_db)
    with pytest.raises(sqlite3.OperationalError, match=r"readonly"):
        conn.execute("UPDATE runs SET success_oracle_result='fail'")
    conn.close()


def test_insert_raises_operational_error(fixture_1_db: Path) -> None:
    conn = _open(fixture_1_db)
    with pytest.raises(sqlite3.OperationalError, match=r"readonly"):
        conn.execute(
            "INSERT INTO runs (run_id, workload_id, claim_id, experiment_id,"
            " baseline_or_ablation, loom_commit, agentserver_commit,"
            " modelserver_commit, app_commit, machine_topology,"
            " context_ground_truth, selected_context, ground_truth_context,"
            " start_time, end_time, success_oracle_result)"
            " VALUES ('x','x','x','x','x','x','x','x','x','x','x','x','x','x','x','pass')"
        )
    conn.close()


def test_delete_raises_operational_error(fixture_1_db: Path) -> None:
    conn = _open(fixture_1_db)
    with pytest.raises(sqlite3.OperationalError, match=r"readonly"):
        conn.execute("DELETE FROM runs")
    conn.close()


def test_missing_companion_table(tmp_path: Path) -> None:
    """route_reasons absent → MISSING (spec §4.4 asymmetry)."""
    db = tmp_path / "runs-only.db"
    conn = sqlite3.connect(str(db))
    conn.execute(
        "CREATE TABLE runs (run_id TEXT PRIMARY KEY, workload_id TEXT NOT NULL,"
        " claim_id TEXT NOT NULL, experiment_id TEXT NOT NULL,"
        " baseline_or_ablation TEXT NOT NULL, loom_commit TEXT NOT NULL,"
        " agentserver_commit TEXT NOT NULL, modelserver_commit TEXT NOT NULL,"
        " app_commit TEXT NOT NULL, machine_topology TEXT NOT NULL,"
        " context_ground_truth TEXT NOT NULL,"
        " capability_snapshot_hash TEXT NOT NULL DEFAULT '',"
        " task_contract_hash TEXT NOT NULL DEFAULT '',"
        " dynamic_mcp_registry_hash TEXT NOT NULL DEFAULT '',"
        " selected_context TEXT NOT NULL, ground_truth_context TEXT NOT NULL,"
        " start_time TEXT NOT NULL, end_time TEXT NOT NULL,"
        " success_oracle_result TEXT NOT NULL"
        " CHECK(success_oracle_result IN ('pass','fail','timeout')),"
        " failure_category TEXT NOT NULL DEFAULT '',"
        " human_intervention_count INTEGER NOT NULL DEFAULT 0,"
        " artifact_hashes TEXT NOT NULL DEFAULT '[]',"
        " observer_trace_path TEXT NOT NULL DEFAULT '',"
        " model_trace_id TEXT NOT NULL DEFAULT '')"
    )
    conn.close()
    conn = _open(db)
    assert companion_table_status(conn, "route_reasons") is TableStatus.MISSING
    conn.close()


def test_empty_companion_table(empty_db: Path) -> None:
    """Full schema applied, route_reasons empty → EMPTY (spec §4.4)."""
    conn = _open(empty_db)
    assert companion_table_status(conn, "route_reasons") is TableStatus.EMPTY
    conn.close()


def test_present_companion_table(fixture_3_db: Path) -> None:
    """Fixture 3 populates route_reasons with 4 rows → PRESENT."""
    conn = _open(fixture_3_db)
    assert companion_table_status(conn, "route_reasons") is TableStatus.PRESENT
    conn.close()
