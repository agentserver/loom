"""Fixture 2 — Contracted (spec §4.2).

6 runs (`experiment_id='E3'`) + 6 task_contracts rows with the JSON
bodies from spec §4.2's 7-field bitmap table.

The 7 §2.2 metrics all assert `null` on this fixture today — the
fixture's job is to exercise the fixture-builder path for
task_contracts (so a future §A3/§A4/§A6 landing has a clean starting
point) and the null-metric contract for §2.2.
"""

from __future__ import annotations

import json
import sqlite3
from pathlib import Path

from tests.fixtures._common import apply_schema, iso_utc, sha256_of


# (run_num, success, failure_category, baseline, contract_short_or_empty)
_RUNS = [
    (1, "pass", "",                   "FullLoom",         "h1"),
    (2, "fail", "contract-violation", "FullLoom",         "h2"),
    (3, "fail", "missing-file",       "FullLoom",         "h3"),
    (4, "pass", "",                   "NoDryRun",         "h4"),
    (5, "fail", "policy-violation",   "FullLoom",         "h5"),
    (6, "pass", "",                   "NoTypedContracts", ""),   # no contract
]


# Per spec §4.2 bitmap table. Each entry says which of the 7 lifecycle
# fields is present in the contract body JSON. ✓ = present, ✗ = missing.
_CONTRACT_FIELD_PRESENCE: dict[str, dict[str, bool]] = {
    "h1": {"intent.goal": True,  "intent.success_criteria": True,  "read_artifacts": True,  "write_targets": True,  "capability_requirements": True,  "execution_policy": True,  "recovery_hint": True},
    "h2": {"intent.goal": True,  "intent.success_criteria": True,  "read_artifacts": True,  "write_targets": True,  "capability_requirements": True,  "execution_policy": True,  "recovery_hint": False},
    "h3": {"intent.goal": True,  "intent.success_criteria": True,  "read_artifacts": True,  "write_targets": False, "capability_requirements": True,  "execution_policy": True,  "recovery_hint": True},
    "h4": {"intent.goal": True,  "intent.success_criteria": True,  "read_artifacts": True,  "write_targets": True,  "capability_requirements": False, "execution_policy": True,  "recovery_hint": True},
    "h5": {"intent.goal": True,  "intent.success_criteria": False, "read_artifacts": True,  "write_targets": True,  "capability_requirements": True,  "execution_policy": True,  "recovery_hint": True},
    "h6": {"intent.goal": True,  "intent.success_criteria": True,  "read_artifacts": True,  "write_targets": True,  "capability_requirements": True,  "execution_policy": False, "recovery_hint": True},
}


def _contract_body(hshort: str) -> str:
    """Encode the 7-field bitmap into a JSON blob matching 12号 §A2's shape.

    Only PRESENT fields land as top-level keys — the extractor's future
    completeness check counts present-vs-absent, and a field is 'absent'
    when the JSON key is missing. Values are placeholders since none of
    the current §2.2 metrics parse them.
    """
    presence = _CONTRACT_FIELD_PRESENCE[hshort]
    body: dict = {}
    if presence["intent.goal"] or presence["intent.success_criteria"]:
        body["intent"] = {}
        if presence["intent.goal"]:
            body["intent"]["goal"] = f"goal-of-{hshort}"
        if presence["intent.success_criteria"]:
            body["intent"]["success_criteria"] = ["ok"]
    if presence["read_artifacts"]:
        body["read_artifacts"] = []
    if presence["write_targets"]:
        body["write_targets"] = []
    if presence["capability_requirements"]:
        body["capability_requirements"] = []
    if presence["execution_policy"]:
        body["execution_policy"] = {}
    if presence["recovery_hint"]:
        body["recovery_hint"] = ""
    return json.dumps(body, sort_keys=True)


def build(db_path: Path) -> None:
    if db_path.exists():
        db_path.unlink()
    conn = sqlite3.connect(str(db_path))
    apply_schema(conn)

    for i, (num, success, fcat, baseline, contract) in enumerate(_RUNS):
        start = iso_utc(i * 60.0)
        end = iso_utc(i * 60.0 + 15.0)
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
                f"run-e3-{num:02d}", f"wl-e3-{num:02d}", f"cl-e3-{num:02d}",
                "E3", baseline,
                "commit-loom", "commit-agentserver", "commit-modelserver", "commit-app",
                "topology-baseline", "",
                f"cs-e3-{num}",
                contract_hash,
                "",
                "slave-A", "",
                start, end,
                success,
                fcat,
                0,
                "[]",
                f"/t/e3-{num}",
                "",
            ),
        )

    # 6 task_contracts rows (h1..h6). The `workspace_id` / `task_id`
    # composite PK is populated with synthetic values; §2.2 #10's
    # cohort join key doesn't exist, so nothing in the extractor
    # reads these back — they're here so a future §A3/§D1 patch can
    # test its join without regenerating the fixture.
    for i, (hshort, _presence) in enumerate(_CONTRACT_FIELD_PRESENCE.items()):
        body_json = _contract_body(hshort)
        ts = iso_utc(i * 60.0)
        conn.execute(
            "INSERT INTO task_contracts ("
            "  workspace_id, task_id, conversation_id, owner_agent_id,"
            "  body, created_at, updated_at"
            ") VALUES (?, ?, ?, ?, ?, ?, ?)",
            (
                "ws-e3", f"task-{hshort}", f"conv-{hshort}", "agent-x",
                body_json, ts, ts,
            ),
        )
    conn.commit()
    conn.close()


def expected_values() -> dict:
    """All 7 §2.2 metrics assert null (spec §4.2 rationale)."""
    return {
        "ContractCompleteness": None,
        "PreExecutionFaultCatchRate": None,
        "ContractViolationRate": None,
        "MissingArtifactDetectionRate": None,
        "PolicyViolationPreventionRate": None,
        "RecoverySuccessRate": None,
        "DuplicateSideEffectRate": None,
    }


if __name__ == "__main__":  # pragma: no cover — regeneration entry
    here = Path(__file__).resolve().parent
    db = here / "fixture_2.db"
    build(db)
    (here / "expected_2.json").write_text(
        json.dumps(expected_values(), indent=2, sort_keys=True) + "\n"
    )
