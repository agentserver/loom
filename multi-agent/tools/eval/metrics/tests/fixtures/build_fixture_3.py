"""Fixture 3 — Semantic + Overhead + User-promoted (spec §4.3).

5 runs (`experiment_id='E2'`) + 4 `route_reasons` rows. Timestamps
chosen so each of the 4 dispatched runs' [start_time, end_time]
windows contains exactly one route_reasons.decision_started_at; run
5 has no overlap (never dispatched).

Base timestamps are all anchored at BASE_TS + 3600s (i.e. 10:00:00Z
on 2026-07-02) so the fixture matches the "2026-07-02T10:00:05Z"
literal decision times in spec §4.3's route_reasons table.
"""

from __future__ import annotations

import json
import sqlite3
from pathlib import Path

from tests.fixtures._common import (
    apply_schema,
    artifact_hashes,
    iso_utc,
    sha256_of,
)


# Offset from BASE_TS (09:00:00Z) → 3600s lands at 10:00:00Z.
_HOUR_OFFSET = 3600.0


# (run_num, success, dur_s, human, failure_category, artifact_shorts,
#  selected_context, ground_truth_context, start_offset_from_hour)
_RUNS = [
    (1, "pass", 20.0, 0, "",              ["ar1"], "slave-A", "slave-A", 0.0),
    (2, "fail", 25.0, 1, "wrong-context", [],      "slave-A", "slave-B", 55.0),
    (3, "pass", 15.0, 0, "",              ["ar3"], "slave-B", "slave-B", 120.0),
    (4, "pass", 10.0, 0, "",              ["ar4"], "slave-A", "slave-A", 180.0),
    (5, "fail", 40.0, 2, "missing-file",  [],      "slave-C", "",        240.0),
]


# (decision_num, start_offset_from_hour_seconds, duration_ns, conversation_id)
_DECISIONS = [
    (1,   5.0,   500_000,    "conv-1"),
    (2,  65.0, 1_000_000,    "conv-2"),
    (3, 125.0, 2_000_000,    "conv-3"),
    (4, 185.0, 4_000_000,    "conv-4"),
]


def build(db_path: Path) -> None:
    if db_path.exists():
        db_path.unlink()
    conn = sqlite3.connect(str(db_path))
    apply_schema(conn)

    for num, success, dur, human, fcat, shorts, sel, gt, off in _RUNS:
        start = iso_utc(_HOUR_OFFSET + off)
        end = iso_utc(_HOUR_OFFSET + off + dur)
        conn.execute(
            "INSERT INTO runs ("
            " run_id, workload_id, claim_id, experiment_id, baseline_or_ablation,"
            " loom_commit, agentserver_commit, modelserver_commit, app_commit,"
            " machine_topology, context_ground_truth, capability_snapshot_hash,"
            " task_contract_hash, dynamic_mcp_registry_hash, selected_context,"
            " ground_truth_context, start_time, end_time, success_oracle_result,"
            " failure_category, human_intervention_count, artifact_hashes,"
            " observer_trace_path, model_trace_id"
            ") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
            (
                f"run-e2-{num:02d}", f"wl-e2-{num:02d}", f"cl-e2-{num:02d}",
                "E2", "FullLoom",
                "commit-loom", "commit-agentserver", "commit-modelserver", "commit-app",
                "topology-baseline", "",
                # Per spec §4.3: every row has capability_snapshot_hash = ''
                # so LifecycleClosureRate correctly evaluates to 0/5.
                "",
                "",  # task_contract_hash empty on every row
                "",
                sel, gt,
                start, end,
                success,
                fcat,
                human,
                artifact_hashes(shorts),
                f"/t/e2-{num}",
                "",
            ),
        )

    for num, off, dur_ns, conv in _DECISIONS:
        dec_start = iso_utc(_HOUR_OFFSET + off)
        dec_end = iso_utc(_HOUR_OFFSET + off + dur_ns / 1e9)
        conn.execute(
            "INSERT INTO route_reasons ("
            "  decision_id, conversation_id, selected_agent_id,"
            "  reason_code, reason_text, candidates_json,"
            "  decision_started_at, decision_ended_at, decision_duration_ns"
            ") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
            (
                f"dec-{num}", conv, "slave-A",
                "unit-test", "fixture 3 decision", "[]",
                dec_start, dec_end, dur_ns,
            ),
        )
    conn.commit()
    conn.close()


def expected_values() -> dict:
    """Hand-computed values per spec §4.3.

    8 populated + 33 null = 41 total. Only the populated keys land
    here; test files consult `metrics.metrics_for_set('full')` +
    per-module null contracts for the other 33.
    """
    return {
        # §2.1 (5 populated + 3 null)
        "TaskSuccessRate": 3 / 5,
        "LifecycleClosureRate": 0.0,
        "TimeToCompletion": {
            "mean_seconds": 22.0,
            "p50_seconds": 20.0,
            "p95_seconds": 37.0,
            "count": 5,
        },
        "HumanContextSelectionCount": 3,
        "WrongContextFailureRate": 2 / 5,
        "ArtifactCorrectnessRate": 1.0,   # 3/3 (rows 1, 3, 4 all pass w/ artifacts)
        # §2.4 (1 populated + 2 null)
        "RoutingAccuracy": 3 / 4,
        # §2.5 (1 populated + 8 null)
        "RoutingLatencyP50P95": {
            "p50_ns": 1_500_000.0,
            "p95_ns": 3_700_000.0,
            "count": 4,
        },
    }


if __name__ == "__main__":  # pragma: no cover — regeneration entry
    here = Path(__file__).resolve().parent
    db = here / "fixture_3.db"
    build(db)
    (here / "expected_3.json").write_text(
        json.dumps(expected_values(), indent=2, sort_keys=True) + "\n"
    )
