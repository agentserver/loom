#!/usr/bin/env python3
"""CLI: metrics.csv + runs.csv → smoke/paper/*_sample.* (spec §5)."""
from __future__ import annotations

import argparse
import csv
import json
import sys
from pathlib import Path
from typing import Iterable

import yaml

# Make `import lib.…` resolve when run either as a script or via python -m.
_HERE = Path(__file__).resolve().parent
if str(_HERE) not in sys.path:
    sys.path.insert(0, str(_HERE))
# Make `from eval_metrics.metrics import ALL_METRICS` resolve regardless of
# CWD — the paper-table writers assert every metric column ∈ ALL_METRICS.
_MODULE_ROOT = _HERE.parent.parent.parent
_METRICS_PKG = _MODULE_ROOT / "tools" / "eval" / "metrics"
if str(_METRICS_PKG) not in sys.path:
    sys.path.insert(0, str(_METRICS_PKG))

from lib import aggregate, paper_tables, plan, stage_e4  # noqa: E402


def _load_csv(path: Path) -> list[dict]:
    with path.open() as f:
        return list(csv.DictReader(f))


def _load_yaml(path: Path):
    return yaml.safe_load(path.read_text())


def _merged_rows(runs: list[dict], metrics: list[dict]) -> list[dict]:
    """Join metrics.csv rows onto runs.csv by run_id.

    metrics.csv is the eval_metrics extractor output; each row carries
    a `run_id` and one metric per column. runs.csv carries
    `workload_id` + `baseline_or_ablation` (=configuration) — the
    aggregator needs the two together.
    """
    by_run: dict[str, dict] = {}
    for r in runs:
        by_run[r["run_id"]] = r
    merged = []
    for m in metrics:
        r = by_run.get(m.get("run_id"))
        if r is None:
            continue
        merged.append({
            **m,
            "workload_id": r.get("workload_id", ""),
            "configuration": r.get("baseline_or_ablation", ""),
        })
    return merged


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog="build_paper_tables",
                                description="fulltable paper table writer")
    p.add_argument("--metrics", required=True,
                   help="tests/eval/results/smoke/metrics.csv")
    p.add_argument("--runs", required=True,
                   help="tests/eval/results/smoke/runs.csv")
    p.add_argument("--ablation-mapping", required=True,
                   help="tools/eval/fulltable/ablation_mapping.yaml")
    p.add_argument("--e4-stages", required=True,
                   help="tools/eval/fulltable/e4_stages.yaml")
    p.add_argument("--out-dir", required=True,
                   help="tests/eval/results/smoke/paper/")
    p.add_argument("--sample-mode", action="store_true",
                   help="add _sample suffix to every output basename")
    args = p.parse_args(argv)

    metrics_path = Path(args.metrics)
    runs_path = Path(args.runs)
    mapping = _load_yaml(Path(args.ablation_mapping))
    e4_manifest = _load_yaml(Path(args.e4_stages))
    out_dir = Path(args.out_dir)
    out_dir.mkdir(parents=True, exist_ok=True)

    runs = _load_csv(runs_path)
    metrics_rows = _load_csv(metrics_path)
    merged = _merged_rows(runs, metrics_rows)

    # Table 2 (macrobenchmark).
    macro = aggregate.aggregate_by_configuration(
        merged, metric_names=paper_tables.TABLE2_COLUMNS[2:-1]
    )
    paper_tables.write_table2_sample(out_dir, macro, sample_mode=args.sample_mode)

    # Table 2 ablation.
    per_flag = aggregate.aggregate_by_flag(merged, mapping)
    paper_tables.write_table2_ablation_sample(
        out_dir, mapping, per_flag, sample_mode=args.sample_mode
    )

    # Figure 1: per-configuration RoutingAccuracy + WrongContextFailureRate.
    per_conf: dict[str, dict[str, float]] = {}
    for (workload, conf), agg in macro.items():
        per_conf.setdefault(conf, {})
    # Recompute per-configuration means for the figure-1 metrics.
    per_conf_agg = aggregate.aggregate_by_configuration(
        merged, metric_names=paper_tables.FIGURE1_METRICS
    )
    figure1_series: dict[str, dict[str, float]] = {}
    for (workload, conf), agg in per_conf_agg.items():
        figure1_series.setdefault(conf, {})
        for m in paper_tables.FIGURE1_METRICS:
            figure1_series[conf][m] = agg.get(m)
    # For any configuration in the 9-entry set that had no run, still
    # produce a dict (values will be None).
    for conf in ("full_loom",) + paper_tables.ABLATION_ORDER:
        figure1_series.setdefault(conf, {m: None for m in paper_tables.FIGURE1_METRICS})
    paper_tables.write_figure1_sample(
        out_dir, figure1_series, sample_mode=args.sample_mode
    )

    # Figure 2: parse AllFaultKinds from Go source at write-time.
    module_root = _HERE.parent.parent.parent  # tools/eval/fulltable → multi-agent/
    fault_kinds = paper_tables.parse_all_fault_kinds(module_root)
    per_kind: dict[str, dict[str, float]] = {
        k: {m: None for m in paper_tables.FIGURE2_METRICS} for k in fault_kinds
    }
    paper_tables.write_figure2_sample(
        out_dir, fault_kinds, per_kind, sample_mode=args.sample_mode
    )

    # Figure 3: E4 three-stage overlay from smoke-fixture e4 rows.
    e4_rows = stage_e4.build_smoke_rows(e4_manifest)
    paper_tables.write_figure3_sample(
        out_dir, e4_rows, sample_mode=args.sample_mode
    )

    # Figure 4: overhead breakdown — fixture-filled.
    per_component: dict[str, dict[str, float]] = {
        c: {"p50_ms": 0.0, "p95_ms": 0.0} for c in paper_tables.FIGURE4_COMPONENTS
    }
    extras = {
        "ArtifactTransferThroughput": {"MiBps_p50": 0.0, "MiBps_p95": 0.0},
        "RoutingLatencyP50P95": {"p50_ms": 0.0, "p95_ms": 0.0},
    }
    paper_tables.write_figure4_sample(
        out_dir, per_component, extras, sample_mode=args.sample_mode
    )

    # e4_stages_sample.csv (long-form).
    paper_tables.write_e4_stages_sample(
        out_dir, e4_rows, sample_mode=args.sample_mode
    )

    return 0


if __name__ == "__main__":
    sys.exit(main())
