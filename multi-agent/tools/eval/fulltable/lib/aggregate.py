"""Fake-safe metric aggregation across runs.

`aggregate_by_configuration(rows)` returns a dict keyed by
(workload_id, configuration) → {metric: mean, ..., run_count: N}.
Missing values (blanks) are excluded from the mean. Zero-sample cells
land as `None` (never silently zeroed).

`rows` is a list of dicts merging one row from `metrics.csv` with the
matching `runs.csv` row's `workload_id` and `configuration` columns.
"""
from __future__ import annotations

import statistics
from typing import Dict, Iterable, List, Optional, Tuple

Key = Tuple[str, str]  # (workload_id, configuration)


def aggregate_by_configuration(
    rows: Iterable[dict],
    metric_names: Iterable[str],
) -> Dict[Key, Dict[str, Optional[float]]]:
    metric_names = list(metric_names)
    buckets: Dict[Key, Dict[str, list]] = {}
    for row in rows:
        key = (row["workload_id"], row["configuration"])
        b = buckets.setdefault(key, {m: [] for m in metric_names})
        for m in metric_names:
            v = row.get(m)
            if v is None or v == "" or (isinstance(v, float) and v != v):
                continue
            try:
                b[m].append(float(v))
            except (TypeError, ValueError):
                continue

    out: Dict[Key, Dict[str, Optional[float]]] = {}
    for key, per_metric in buckets.items():
        agg: Dict[str, Optional[float]] = {}
        for m in metric_names:
            values = per_metric[m]
            agg[m] = statistics.fmean(values) if values else None
        agg["run_count"] = len(next(iter(per_metric.values()), []))
        # ^ approximation for "how many rows in this bucket had at least
        # one metric present"; the smoke fixture always has all-or-none.
        # Track the max across metrics to be safe.
        agg["run_count"] = max(
            (len(v) for v in per_metric.values()),
            default=0,
        )
        out[key] = agg
    return out


def aggregate_by_flag(
    rows: Iterable[dict],
    mapping: List[dict],
) -> Dict[str, Dict[str, Optional[float]]]:
    """Per-ablation-flag mean over the flag's target_metrics list.

    Fixture-fills any missing (flag, metric) cell with None. The 8-row
    long-form table then drops the None values as blank cells.
    """
    per_flag_rows: Dict[str, List[dict]] = {}
    for row in rows:
        conf = row["configuration"]
        if conf in {e["flag"] for e in mapping}:
            per_flag_rows.setdefault(conf, []).append(row)

    out: Dict[str, Dict[str, Optional[float]]] = {}
    for entry in mapping:
        flag = entry["flag"]
        agg: Dict[str, Optional[float]] = {}
        rows_for_flag = per_flag_rows.get(flag, [])
        for m in entry["target_metrics"]:
            values = []
            for r in rows_for_flag:
                v = r.get(m)
                if v is None or v == "":
                    continue
                try:
                    values.append(float(v))
                except (TypeError, ValueError):
                    continue
            agg[m] = statistics.fmean(values) if values else None
        out[flag] = agg
    return out


def first_appearance_union(mapping: List[dict]) -> List[str]:
    """Metric columns for table2_ablation_sample.csv: order = first
    appearance walking the 8 mapping entries in file order (spec §3.4)."""
    seen: List[str] = []
    for entry in mapping:
        for m in entry["target_metrics"]:
            if m not in seen:
                seen.append(m)
    return seen
