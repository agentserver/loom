"""Paper Table 2 / Figure 1–4 sample writers (spec §5).

All writers take a `--sample-mode` boolean; when true, the output file
basenames end in `_sample.{csv,json}`. The follow-up run worktree drops
`--sample-mode` to produce non-suffixed paper artefacts.

Every metric-name column ∈ eval_metrics.metrics.ALL_METRICS — enforced
by the writers themselves via `_assert_metric` so a drift shows up as
an assertion, not a silent bad column.
"""
from __future__ import annotations

import csv
import json
import os
from pathlib import Path
from typing import Dict, Iterable, List, Optional, Sequence

from lib import stage_e4

# -- imports resolved lazily so the module still loads outside the eval
# tree (docs generation, hypothetical fork of the harness).
_ALL_METRIC_NAMES: Optional[set[str]] = None


def _all_metric_names() -> set[str]:
    global _ALL_METRIC_NAMES
    if _ALL_METRIC_NAMES is None:
        from eval_metrics.metrics import ALL_METRICS
        _ALL_METRIC_NAMES = {m.name for m in ALL_METRICS}
    return _ALL_METRIC_NAMES


def _assert_metric(name: str) -> None:
    assert name in _all_metric_names(), (
        f"metric {name!r} not in ALL_METRICS — column drift"
    )


# ---------------------------------------------------------------------------
# Table 2 (§5.1): Full Loom vs baselines macrobenchmark.
# ---------------------------------------------------------------------------

TABLE2_COLUMNS: tuple[str, ...] = (
    "workload_id", "configuration",
    "TaskSuccessRate", "TimeToCompletion",
    "HumanContextSelectionCount", "WrongContextFailureRate",
    "LifecycleClosureRate", "run_count",
)


def write_table2_sample(
    out_dir: Path,
    aggregates,  # Dict[(workload_id, conf), Dict[metric, value]]
    *, sample_mode: bool = True,
) -> Path:
    for m in TABLE2_COLUMNS[2:-1]:
        _assert_metric(m)
    path = out_dir / ("table2_sample.csv" if sample_mode else "table2.csv")
    with path.open("w", newline="") as f:
        w = csv.writer(f)
        w.writerow(TABLE2_COLUMNS)
        for (workload, conf), metrics in sorted(aggregates.items()):
            row = [workload, conf]
            for col in TABLE2_COLUMNS[2:-1]:
                v = metrics.get(col)
                row.append("" if v is None else v)
            row.append(metrics.get("run_count", 0))
            w.writerow(row)
    return path


# ---------------------------------------------------------------------------
# Table 2 ablation (§3.4): 8-row long-form per ablation flag.
# ---------------------------------------------------------------------------


def write_table2_ablation_sample(
    out_dir: Path,
    mapping: List[dict],
    per_flag,  # Dict[flag, Dict[metric, value]]
    *, sample_mode: bool = True,
) -> Path:
    from lib.aggregate import first_appearance_union

    metric_cols = first_appearance_union(mapping)
    for m in metric_cols:
        _assert_metric(m)
    header = ["ablation_flag", "difficulty_group"] + metric_cols
    path = out_dir / (
        "table2_ablation_sample.csv" if sample_mode
        else "table2_ablation.csv"
    )
    with path.open("w", newline="") as f:
        w = csv.writer(f)
        w.writerow(header)
        for entry in mapping:
            flag = entry["flag"]
            row = [flag, entry["difficulty"]]
            per = per_flag.get(flag, {})
            for m in metric_cols:
                v = per.get(m)
                # A cell that this flag does NOT target stays blank.
                if m not in entry["target_metrics"]:
                    row.append("")
                elif v is None:
                    row.append("")
                else:
                    row.append(v)
            w.writerow(row)
    return path


# ---------------------------------------------------------------------------
# Figure 1: semantic routing.
# ---------------------------------------------------------------------------

ABLATION_ORDER = (
    "NoCapabilityDiscovery", "NoTypedContracts", "NoDryRun",
    "NoContractFormalization", "NoUserPromotionPath",
    "NoAcceptanceGate", "NoRegistryLookup", "NoObserver",
)
FIGURE1_METRICS = ("RoutingAccuracy", "WrongContextFailureRate")


def write_figure1_sample(
    out_dir: Path,
    per_conf,  # Dict[conf, Dict[metric, value]]
    *, sample_mode: bool = True,
) -> Path:
    for m in FIGURE1_METRICS:
        _assert_metric(m)
    configurations = ["full_loom"] + list(ABLATION_ORDER)
    payload = {
        "configurations": configurations,
        "series": {
            m: {c: per_conf.get(c, {}).get(m) for c in configurations}
            for m in FIGURE1_METRICS
        },
    }
    path = out_dir / (
        "figure1_data_sample.json" if sample_mode else "figure1_data.json"
    )
    path.write_text(json.dumps(payload, indent=2))
    return path


# ---------------------------------------------------------------------------
# Figure 2: contract fault injection.
# ---------------------------------------------------------------------------

FIGURE2_METRICS = ("PreExecutionFaultCatchRate", "ContractViolationRate")


def parse_all_fault_kinds(module_root: Path) -> list[str]:
    """Parse `AllFaultKinds` from tools/eval/faultinject/kinds.go at read
    time — same rule the test uses so drift is caught immediately.
    """
    return _parse_kinds_from_source(
        module_root / "tools" / "eval" / "faultinject" / "kinds.go"
    )


def _parse_kinds_from_source(path: Path) -> list[str]:
    text = path.read_text()
    # Grab the string literals following each `FaultKind = "..."`.
    import re
    kinds = re.findall(r"FaultKind\s*=\s*\"([a-z_]+)\"", text)
    if len(kinds) != 8:
        raise RuntimeError(f"expected 8 fault kinds, got {kinds!r}")
    return kinds


def write_figure2_sample(
    out_dir: Path,
    fault_kinds: list[str],
    per_kind,  # Dict[kind, Dict[metric, value]]
    *, sample_mode: bool = True,
) -> Path:
    for m in FIGURE2_METRICS:
        _assert_metric(m)
    payload = {
        "fault_types": fault_kinds,
        "series": {
            m: {k: per_kind.get(k, {}).get(m) for k in fault_kinds}
            for m in FIGURE2_METRICS
        },
    }
    path = out_dir / (
        "figure2_data_sample.json" if sample_mode else "figure2_data.json"
    )
    path.write_text(json.dumps(payload, indent=2))
    return path


# ---------------------------------------------------------------------------
# Figure 3: E4 three-stage overlay.
# ---------------------------------------------------------------------------

FAMILIES_SORTED = (
    "api-wrapper-for-local-service",
    "csv-profiler",
    "image-metadata-extractor",
    "log-parser",
    "refund-policy-checker",
)

FIGURE3_STAGES = {
    "stage_A": {"metric": "TimeToCompletion",
                "subkey": "p50_seconds", "unit": "seconds"},
    "stage_B": {"metric": "TimeFromUserDecisionToRegisteredMCP",
                "subkey": "p50_seconds", "unit": "seconds"},
    "stage_C": {"metric": "ReuseSpeedup",
                "subkey": "ratio",
                "unit": "ratio (Stage A p50_seconds / Stage C p50_seconds)"},
}


def write_figure3_sample(
    out_dir: Path,
    e4_rows: list[dict],
    *, sample_mode: bool = True,
) -> Path:
    for st in FIGURE3_STAGES.values():
        _assert_metric(st["metric"])
    _assert_metric("AdHocScriptTaskShare")
    # Configuration filter: figure 3 is a full_loom comparison.
    filtered = [r for r in e4_rows if r["configuration"] == "full_loom"]

    payload = {
        "families": list(FAMILIES_SORTED),
    }
    for st_key, meta in FIGURE3_STAGES.items():
        stage_letter = st_key.split("_")[1]
        stage_rows = [r for r in filtered if r["stage"] == stage_letter]
        assert len(stage_rows) == 20, (
            f"figure 3 {st_key}: expected 20 rows after filter, got {len(stage_rows)}"
        )
        values = _mean_by_family(stage_rows, meta["metric"])
        payload[st_key] = {
            "metric": meta["metric"],
            "subkey": meta["subkey"],
            "unit": meta["unit"],
            "values": values,
        }
    # AdHocScriptTaskShare overlay — from Stage-A rows too.
    stage_a = [r for r in filtered if r["stage"] == "A"]
    payload["AdHocScriptTaskShare"] = {
        "subkey": "share_0_to_1",
        "values": _mean_by_family(stage_a, "AdHocScriptTaskShare"),
    }
    path = out_dir / (
        "figure3_data_sample.json" if sample_mode else "figure3_data.json"
    )
    path.write_text(json.dumps(payload, indent=2))
    return path


def _mean_by_family(rows: list[dict], metric: str) -> Dict[str, Optional[float]]:
    """Family → mean(metric) across the given rows; blank → None."""
    out: Dict[str, list] = {}
    for r in rows:
        v = r.get(metric)
        if v == "" or v is None:
            continue
        try:
            out.setdefault(r["family"], []).append(float(v))
        except (TypeError, ValueError):
            continue
    result: Dict[str, Optional[float]] = {}
    for fam in FAMILIES_SORTED:
        values = out.get(fam, [])
        result[fam] = (sum(values) / len(values)) if values else None
    return result


# ---------------------------------------------------------------------------
# Figure 4: overhead breakdown.
# ---------------------------------------------------------------------------

FIGURE4_COMPONENTS = (
    "DriverPlanningOverhead",
    "TaskDispatchLatency",
    "TunnelOverhead",
    "ObserverOverhead",
    "ModelProxyOverhead",
)
FIGURE4_EXTRAS = ("ArtifactTransferThroughput", "RoutingLatencyP50P95")
SCALE_CONTEXTS = [1, 2, 4, 8, 16]
SCALE_TOOLS = [10, 50, 100]
SCALE_ARTIFACT_SIZES = [1024, 1_048_576, 104_857_600]


def write_figure4_sample(
    out_dir: Path,
    per_component,  # Dict[name, Dict["p50_ms"|"p95_ms", float]]
    extras,  # Dict[name, Dict[str, float|list]]
    *, sample_mode: bool = True,
) -> Path:
    for m in FIGURE4_COMPONENTS + FIGURE4_EXTRAS:
        _assert_metric(m)
    payload = {
        "components": list(FIGURE4_COMPONENTS),
        "series": {
            "p50_ms": {c: per_component.get(c, {}).get("p50_ms") for c in FIGURE4_COMPONENTS},
            "p95_ms": {c: per_component.get(c, {}).get("p95_ms") for c in FIGURE4_COMPONENTS},
        },
        "extras": {
            "ArtifactTransferThroughput": {
                "MiBps_p50": extras.get("ArtifactTransferThroughput", {}).get("MiBps_p50"),
                "MiBps_p95": extras.get("ArtifactTransferThroughput", {}).get("MiBps_p95"),
                "artifact_size_bytes": list(SCALE_ARTIFACT_SIZES),
            },
            "RoutingLatencyP50P95": {
                "p50_ms": extras.get("RoutingLatencyP50P95", {}).get("p50_ms"),
                "p95_ms": extras.get("RoutingLatencyP50P95", {}).get("p95_ms"),
                "scale_points": {
                    "contexts": list(SCALE_CONTEXTS),
                    "tools_per_context": list(SCALE_TOOLS),
                    "artifact_size_bytes": list(SCALE_ARTIFACT_SIZES),
                },
            },
        },
    }
    path = out_dir / (
        "figure4_data_sample.json" if sample_mode else "figure4_data.json"
    )
    path.write_text(json.dumps(payload, indent=2))
    return path


# ---------------------------------------------------------------------------
# e4_stages_sample.csv — long-form.
# ---------------------------------------------------------------------------


def write_e4_stages_sample(
    out_dir: Path,
    rows: list[dict],
    *, sample_mode: bool = True,
) -> Path:
    for m in stage_e4.METRIC_COLS:
        _assert_metric(m)
    path = out_dir / (
        "e4_stages_sample.csv" if sample_mode else "e4_stages.csv"
    )
    with path.open("w", newline="") as f:
        w = csv.writer(f)
        w.writerow(stage_e4.COLUMNS)
        for r in rows:
            w.writerow([r.get(c, "") for c in stage_e4.COLUMNS])
    return path
