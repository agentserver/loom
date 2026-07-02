"""Fixture 1 — Lifecycle (spec §4.1).

Ten runs, `experiment_id='E1'`, `baseline_or_ablation='FullLoom'`,
`capability_snapshot_hash='cs-<n>'` on every row.

Rerun with `python -m tests.fixtures.build_fixture_1` (or the direct
path) to regenerate `fixture_1.db` + `expected_1.json`.

The hand-computed expected values below MUST match spec §4.1 verbatim;
if a formula drift shows up in test comparisons, the fixture builder
is the source-of-truth arbiter (the spec §4.1 table has already been
Codex-reviewed 14 rounds).
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


# (run_num, success, duration_seconds, human_intervention, failure_category,
#  artifact_shorts, contract_short_or_empty, trace_path)
_ROWS = [
    (1,  "pass",    12.0, 0, "",              ["a1"],           "h1",  "/t/1"),
    (2,  "pass",    18.0, 1, "",              ["a2"],           "h2",  "/t/2"),
    (3,  "fail",    30.0, 2, "wrong-context", [],               "h3",  "/t/3"),
    (4,  "pass",    10.0, 0, "",              ["a4"],           "h4",  "/t/4"),
    (5,  "fail",    60.0, 3, "missing-file",  [],               "",    "/t/5"),
    (6,  "pass",    20.0, 0, "",              ["a6", "a6b"],    "h6",  "/t/6"),
    (7,  "timeout", 90.0, 1, "timeout",       [],               "h7",  "/t/7"),
    (8,  "pass",    14.0, 0, "",              ["a8"],           "h8",  "/t/8"),
    (9,  "fail",    22.0, 1, "wrong-version", [],               "h9",  "/t/9"),
    (10, "pass",    16.0, 0, "",              ["a10"],          "h10", "/t/10"),
]


def build(db_path: Path) -> None:
    if db_path.exists():
        db_path.unlink()
    conn = sqlite3.connect(str(db_path))
    apply_schema(conn)

    # Anchor start_time to a stable offset per row so rebuilds are
    # deterministic. Start times are 60 s apart; end_time = start +
    # duration. Neither timestamp is empty so `TimeToCompletion`
    # includes every row.
    for i, (num, success, dur, human, fcat, shorts, contract, trace) in enumerate(_ROWS):
        start = iso_utc(i * 60.0)
        end = iso_utc(i * 60.0 + dur)
        contract_hash = sha256_of(contract) if contract else ""
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
                f"run-{num:02d}", f"wl-{num:02d}", f"cl-{num:02d}",
                "E1", "FullLoom",
                "commit-loom", "commit-agentserver", "commit-modelserver", "commit-app",
                "topology-baseline", "",
                f"cs-{num}",  # capability_snapshot_hash non-empty on all 10 rows
                contract_hash,
                "",  # dynamic_mcp_registry_hash
                "slave-A", "",  # selected_context, ground_truth_context (unused in §2.1)
                start, end,
                success,
                fcat,
                human,
                artifact_hashes(shorts),
                trace,
                "",
            ),
        )
    conn.commit()
    conn.close()


def expected_values() -> dict:
    """Hand-computed values per spec §4.1.

    All 41 metrics accounted for: 8 populated (§2.1 rows #1..#6 plus
    the two structured TTC subkeys plus #4/#5/#6) + 3 §2.1 nulls
    (#7/#8/#9) + everything else in the catalog defers to its own
    module (all null on this fixture per that module's contract).
    Only §2.1-relevant keys land here; test files that go beyond §2.1
    use fixtures 2/3.
    """
    return {
        "TaskSuccessRate": 6 / 10,
        "LifecycleClosureRate": 6 / 10,
        "TimeToCompletion": {
            "p50_seconds": 19.0,
            "p95_seconds": 76.5,
            "mean_seconds": 29.2,
            "count": 10,
        },
        "HumanContextSelectionCount": 8,
        "WrongContextFailureRate": 3 / 10,
        "ArtifactCorrectnessRate": 1.0,
        "ManualSetupStepCount": None,     # upstream missing
        "ConfigTouchCount": None,         # upstream missing
        "StateContinuityRate": None,      # upstream missing (§A6)
        # Every other §2.2..§2.5 + #40/#41 metric asserts null on this
        # fixture per its per-module contract (no fixture-1 data drives
        # them). Tests target §2.1 explicitly; broader coverage lives
        # in fixture 2 / 3 tests.
    }


if __name__ == "__main__":  # pragma: no cover — regeneration entry
    here = Path(__file__).resolve().parent
    db = here / "fixture_1.db"
    build(db)
    (here / "expected_1.json").write_text(
        json.dumps(expected_values(), indent=2, sort_keys=True) + "\n"
    )
