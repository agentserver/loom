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


# Spec §7 (h): perf tests are skipped by default; explicit opt-in via
# either `CI=true` in the environment OR `-m perf` on the pytest
# command line runs them. We inspect both signals here — the marker
# alone is not enough because pytest does not "activate" marker-gated
# tests, it only filters when `-m` is passed.
_PERF_ENABLED = (
    os.environ.get("CI") == "true"
    or any(arg == "perf" or arg.endswith("perf") for arg in os.sys.argv)
)


@pytest.mark.perf
@pytest.mark.skipif(not _PERF_ENABLED, reason="perf mark; enable with CI=true or `-m perf`")
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
        "INSERT INTO runs ("
        " run_id, workload_id, claim_id, experiment_id, baseline_or_ablation,"
        " loom_commit, agentserver_commit, modelserver_commit, app_commit,"
        " machine_topology, context_ground_truth, capability_snapshot_hash,"
        " task_contract_hash, dynamic_mcp_registry_hash, selected_context,"
        " ground_truth_context, start_time, end_time, success_oracle_result,"
        " failure_category, human_intervention_count, artifact_hashes,"
        " observer_trace_path, model_trace_id"
        ") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
        rows,
    )
    conn.commit()
    conn.close()

    t0 = time.time()
    d = extract_dict(db, "full")
    elapsed = time.time() - t0
    assert d["row_count"] == 10_000
    assert elapsed < 2.0, f"10k-row extract took {elapsed:.2f}s (> 2s)"
