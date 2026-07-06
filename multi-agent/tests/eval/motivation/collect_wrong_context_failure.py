#!/usr/bin/env python3
"""Collector for canonical_key = wrong_context_failure_manual_baseline.

Reads a main-experiment `runs.csv` and computes the ratio of wrong_ctx=True
rows for `baseline_or_ablation = manual_ssh` at a given workload_id. Reads
`wrong_ctx_seed` from `motivation-e2e/spec.yaml`; never from env. See
docs/specs/wt3-mini-case.spec.md §3.4 row 2 for the contract.
"""

from __future__ import annotations

import argparse
import csv
import hashlib
import json
import re
import sys
from pathlib import Path

CANONICAL_KEY = "wrong_context_failure_manual_baseline"
UNIT = "ratio"


def _sha256_file(p: Path) -> str:
    h = hashlib.sha256()
    with p.open("rb") as f:
        for chunk in iter(lambda: f.read(65536), b""):
            h.update(chunk)
    return h.hexdigest()


def _load_seed_from_spec_yaml(spec_yaml_path: Path) -> int:
    """Read `wrong_ctx_seed:` from a workload spec.yaml.

    Deliberately avoids `os.environ` — the seed MUST be pinned in
    spec.yaml so reviewers can reproduce identical numbers (spec §7 (c)).
    Uses a tiny inline regex parser to avoid a pyyaml dep drift; the
    spec.yaml file is short and hand-authored.
    """
    if not spec_yaml_path.exists():
        raise FileNotFoundError(f"spec.yaml not found: {spec_yaml_path}")
    text = spec_yaml_path.read_text(encoding="utf-8")
    m = re.search(r"^\s*wrong_ctx_seed\s*:\s*(-?\d+)\s*$", text, re.MULTILINE)
    if not m:
        raise ValueError(
            f"wrong_ctx_seed: <int> not found in {spec_yaml_path}; "
            "seed MUST be published in spec.yaml (§7 (c))."
        )
    return int(m.group(1))


def collect(runs_csv: Path, baseline: str, workload_id: str, seed: int) -> dict:
    if not runs_csv.exists():
        print(f"error: runs.csv not found: {runs_csv}", file=sys.stderr)
        sys.exit(2)
    total = 0
    wrong = 0
    with runs_csv.open(newline="", encoding="utf-8") as f:
        for row in csv.DictReader(f):
            if row.get("workload_id") != workload_id:
                continue
            if row.get("baseline_or_ablation") != baseline:
                continue
            total += 1
            if str(row.get("wrong_ctx", "")).strip().lower() == "true":
                wrong += 1
    if total == 0:
        print(
            f"error: no rows in {runs_csv} for workload_id={workload_id!r} "
            f"baseline={baseline!r}",
            file=sys.stderr,
        )
        sys.exit(2)
    return {
        "canonical_key": CANONICAL_KEY,
        "workload_id": workload_id,
        "baseline": baseline,
        "seed": seed,
        "raw_value": float(wrong) / float(total),
        "unit": UNIT,
        "source_file": str(runs_csv.resolve()),
        "source_sha256": _sha256_file(runs_csv),
    }


def _cli() -> None:
    p = argparse.ArgumentParser(description="Collector for wrong_context_failure_manual_baseline.")
    p.add_argument("--runs-csv", required=True, type=Path)
    p.add_argument("--baseline", default="manual_ssh")
    p.add_argument("--workload-id", required=True)
    p.add_argument("--spec-yaml", required=True, type=Path,
                   help="Path to the workload spec.yaml (published seed).")
    p.add_argument("--out", type=Path, default=None)
    args = p.parse_args()
    seed = _load_seed_from_spec_yaml(args.spec_yaml)
    record = collect(args.runs_csv, args.baseline, args.workload_id, seed)
    payload = json.dumps(record, ensure_ascii=False, sort_keys=True)
    if args.out is None:
        print(payload)
    else:
        args.out.write_text(payload + "\n", encoding="utf-8")


if __name__ == "__main__":
    _cli()
