#!/usr/bin/env python3
"""Collector for canonical_key = reuse_time_savings.

Reads a Stage A/B/C timing CSV (`e4_stages.csv`) and computes
    savings% = (t_A - t_C) / t_A * 100
for each (workload_id, family) group. When t_A == 0 emits null +
descriptive note (spec §7 (e)) — never NaN / inf.

See docs/specs/wt3-mini-case.spec.md §3.4 row 4 for the contract.
"""

from __future__ import annotations

import argparse
import csv
import hashlib
import json
import sys
from collections import defaultdict
from pathlib import Path

CANONICAL_KEY = "reuse_time_savings"
UNIT = "percent"


def _sha256_file(p: Path) -> str:
    h = hashlib.sha256()
    with p.open("rb") as f:
        for chunk in iter(lambda: f.read(65536), b""):
            h.update(chunk)
    return h.hexdigest()


def _collect_groups(e4_csv: Path, workload_id: str) -> list[tuple[str, dict[str, tuple[float, float]]]]:
    """Return [(family, {stage: (start, end)})...] for the given workload."""
    if not e4_csv.exists():
        print(f"error: e4_stages.csv not found: {e4_csv}", file=sys.stderr)
        sys.exit(2)
    groups: dict[str, dict[str, tuple[float, float]]] = defaultdict(dict)
    with e4_csv.open(newline="", encoding="utf-8") as f:
        for row in csv.DictReader(f):
            if row.get("workload_id") != workload_id:
                continue
            fam = row.get("family", "").strip()
            stage = row.get("stage", "").strip()
            if not fam or not stage:
                continue
            try:
                s = float(row["stage_start"])
                e = float(row["stage_end"])
            except (KeyError, ValueError):
                print(
                    f"error: malformed row in {e4_csv}: {row}",
                    file=sys.stderr,
                )
                sys.exit(2)
            groups[fam][stage] = (s, e)
    return sorted(groups.items())


def collect(e4_csv: Path, workload_id: str) -> list[dict]:
    groups = _collect_groups(e4_csv, workload_id)
    if not groups:
        print(f"error: no families for workload_id={workload_id!r} in {e4_csv}",
              file=sys.stderr)
        sys.exit(2)

    source_sha = _sha256_file(e4_csv)
    source_str = str(e4_csv.resolve())

    records: list[dict] = []
    for fam, stages in groups:
        if "A" not in stages or "C" not in stages:
            # Missing stage(s) → drop with a note (never NaN/inf).
            records.append({
                "canonical_key": CANONICAL_KEY,
                "workload_id": workload_id,
                "family": fam,
                "raw_value": None,
                "unit": UNIT,
                "note": "missing Stage A or Stage C row; savings undefined",
                "source_file": source_str,
                "source_sha256": source_sha,
            })
            continue
        t_a = stages["A"][1] - stages["A"][0]
        t_c = stages["C"][1] - stages["C"][0]
        rec: dict = {
            "canonical_key": CANONICAL_KEY,
            "workload_id": workload_id,
            "family": fam,
            "unit": UNIT,
            "source_file": source_str,
            "source_sha256": source_sha,
        }
        if t_a == 0:
            rec["raw_value"] = None
            rec["note"] = "first run instantaneous, savings undefined"
        else:
            rec["raw_value"] = (t_a - t_c) / t_a * 100.0
        records.append(rec)
    return records


def _cli() -> None:
    p = argparse.ArgumentParser(description="Collector for reuse_time_savings.")
    p.add_argument("--e4-stages-csv", required=True, type=Path)
    p.add_argument("--workload-id", required=True)
    p.add_argument("--out-dir", type=Path, default=None,
                   help="Directory to write one JSON per family (default: stdout).")
    args = p.parse_args()
    records = collect(args.e4_stages_csv, args.workload_id)
    if args.out_dir is None:
        for r in records:
            print(json.dumps(r, ensure_ascii=False, sort_keys=True))
    else:
        args.out_dir.mkdir(parents=True, exist_ok=True)
        for r in records:
            fam = r.get("family", "unknown")
            (args.out_dir / f"reuse_time_savings.{fam}.json").write_text(
                json.dumps(r, ensure_ascii=False, sort_keys=True) + "\n",
                encoding="utf-8",
            )


if __name__ == "__main__":
    _cli()
