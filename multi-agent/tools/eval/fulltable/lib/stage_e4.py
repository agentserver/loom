"""E4 three-stage join + long-form CSV row builder (spec §4.4).

`join(runs, e4_manifest)` returns 240 rows keyed on
`(family, stage, task_id, configuration)` with the exact column list
from spec §4.4. Cells for metrics that don't apply to a given
(stage, configuration) combination stay blank.

The smoke path fills the metric cells with fixture numbers — the goal
here is column shape, not value magnitudes (the follow-up run worktree
supplies real numbers).
"""
from __future__ import annotations

from dataclasses import dataclass
from typing import Iterable, List, Optional, Tuple

# Column order — mirrors spec §4.4 (block starting "stage, family, task_id,").
COLUMNS: Tuple[str, ...] = (
    "stage",
    "family",
    "task_id",
    "configuration",
    "TaskSuccessRate",
    "TimeToCompletion",
    "HumanEditCount",
    "TokenUsage",
    "RegistryLookupHitRate",
    "CapabilityReuseRate",
    "ReuseSpeedup",
    "RepeatedGenerationRate",
    "AdHocScriptTaskShare",
    "GeneratedCapabilityDefectRate",
    "PromotionCandidateSurfacingRate",
    "UserInitiatedSynthesisSuccessRate",
    "ValidationFalseAcceptRate",
    "PromotionAdoptionRate",
    "TimeFromUserDecisionToRegisteredMCP",
)

METRIC_COLS: Tuple[str, ...] = COLUMNS[4:]

# Which metrics belong to which stage. Cells outside a stage's set stay
# blank. This mirrors 11 号 §4 (Stage A: ad-hoc baseline; Stage B: user
# decision → registered; Stage C: reuse).
STAGE_METRICS: dict[str, tuple[str, ...]] = {
    "A": (
        "TaskSuccessRate",
        "TimeToCompletion",
        "HumanEditCount",
        "TokenUsage",
        "AdHocScriptTaskShare",
    ),
    "B": (
        "TaskSuccessRate",
        "PromotionCandidateSurfacingRate",
        "UserInitiatedSynthesisSuccessRate",
        "ValidationFalseAcceptRate",
        "PromotionAdoptionRate",
        "TimeFromUserDecisionToRegisteredMCP",
        "GeneratedCapabilityDefectRate",
    ),
    "C": (
        "TaskSuccessRate",
        "TimeToCompletion",
        "RegistryLookupHitRate",
        "CapabilityReuseRate",
        "ReuseSpeedup",
        "RepeatedGenerationRate",
        "TokenUsage",
    ),
}


@dataclass
class JoinedRow:
    stage: str
    family: str
    task_id: str
    configuration: str
    metrics: dict


def _fixture_value(stage: str, metric: str, family: str, task_id: str,
                   configuration: str) -> Optional[float]:
    """Deterministic placeholder — the smoke fixture value.

    The follow-up run worktree replaces this function with a real
    metrics_source lookup; here we only need to certify that the
    Stage-A `full_loom` `TaskSuccessRate` cell is non-blank (spec §4.4
    test rule (b)).
    """
    if metric not in STAGE_METRICS[stage]:
        return None  # blank cell
    # Deterministic small number per (stage, metric, family, task_id, config).
    seed = hash((stage, metric, family, task_id, configuration)) & 0xFFFFFFFF
    return round(0.10 + (seed % 900) / 1000.0, 4)


def build_smoke_rows(e4_manifest: Iterable[dict]) -> List[dict]:
    """Fixture-fill the 240 rows. Blank cells stay as empty strings."""
    out: list[dict] = []
    for entry in e4_manifest:
        stage = entry["stage"]
        row = {
            "stage": stage,
            "family": entry["family"],
            "task_id": entry["task_id"],
            "configuration": entry["configuration"],
        }
        for m in METRIC_COLS:
            v = _fixture_value(stage, m, entry["family"], entry["task_id"],
                               entry["configuration"])
            row[m] = "" if v is None else v
        out.append(row)
    return out


def join_runs(runs: Iterable[dict], e4_manifest: Iterable[dict]) -> List[dict]:
    """Join a `runs`-shaped iterable with the e4 manifest.

    `runs` is expected to carry `family`, `stage`, `task_id`,
    `configuration`, and a dict of metric_name → value under `metrics`.
    Rows in the manifest that have no matching run yield blank cells.
    """
    key = lambda r: (r["family"], r["stage"], r["task_id"], r["configuration"])
    lookup = {key(r): r for r in runs}
    out = []
    for entry in e4_manifest:
        k = (entry["family"], entry["stage"], entry["task_id"], entry["configuration"])
        run = lookup.get(k)
        row = {c: entry[c] if c in entry else "" for c in COLUMNS[:4]}
        for m in METRIC_COLS:
            if run is None or m not in STAGE_METRICS[entry["stage"]]:
                row[m] = ""
                continue
            v = (run.get("metrics") or {}).get(m)
            row[m] = "" if v is None else v
        out.append(row)
    return out
