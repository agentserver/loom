#!/usr/bin/env python3
"""Aggregate N raw canonical JSON into (median, iqr_low, iqr_high).

Percentile method is pinned to Python stdlib `statistics.quantiles(...,
n=4, method="exclusive")` (spec §3.5). No NaN / inf ever escapes.

Exits 2 `ErrInsufficientSamples` when fewer than 3 non-null samples remain
after dropping raw records whose `raw_value` is None (e.g. t_A=0 for
`reuse_time_savings`).
"""

from __future__ import annotations

import argparse
import json
import math
import statistics
import sys
from pathlib import Path

ERR_INSUFFICIENT = "ErrInsufficientSamples"


def _load_raw(raw_dir: Path, canonical_key: str) -> tuple[list, list[str]]:
    values: list[float] = []
    dropped: list[str] = []
    for jf in sorted(raw_dir.glob("*.json")):
        try:
            obj = json.loads(jf.read_text(encoding="utf-8"))
        except json.JSONDecodeError as exc:
            print(f"error: {jf} is not valid JSON: {exc}", file=sys.stderr)
            sys.exit(2)
        if obj.get("canonical_key") != canonical_key:
            continue
        v = obj.get("raw_value")
        if v is None:
            dropped.append(jf.name)
            continue
        values.append(float(v))
    return values, dropped


def aggregate(raw_dir: Path, canonical_key: str) -> dict:
    values, dropped = _load_raw(raw_dir, canonical_key)
    n = len(values)
    if n < 3:
        note = f"only {n} non-null samples"
        if dropped:
            note += f"; dropped: {', '.join(dropped)}"
        print(f"error: {ERR_INSUFFICIENT}: {note}", file=sys.stderr)
        sys.exit(2)
    median = statistics.median(values)
    q = statistics.quantiles(values, n=4, method="exclusive")
    iqr_low = q[0]
    iqr_high = q[2]
    for label, x in (("median", median), ("iqr_low", iqr_low), ("iqr_high", iqr_high)):
        if isinstance(x, float) and (math.isnan(x) or math.isinf(x)):
            print(f"error: aggregate produced non-finite {label}: {x}",
                  file=sys.stderr)
            sys.exit(2)
    out = {
        "canonical_key": canonical_key,
        "n_samples": n,
        "median": median,
        "iqr_low": iqr_low,
        "iqr_high": iqr_high,
    }
    if dropped:
        out["note"] = f"dropped {len(dropped)} null sample(s): {', '.join(dropped)}"
    return out


def _cli() -> None:
    p = argparse.ArgumentParser(description="Aggregate raw canonical JSON.")
    p.add_argument("--canonical-key", required=True)
    p.add_argument("--raw-dir", required=True, type=Path)
    p.add_argument("--out", type=Path, default=None)
    args = p.parse_args()
    if not args.raw_dir.is_dir():
        print(f"error: raw-dir not a directory: {args.raw_dir}", file=sys.stderr)
        sys.exit(2)
    result = aggregate(args.raw_dir, args.canonical_key)
    payload = json.dumps(result, ensure_ascii=False, sort_keys=True)
    if args.out is None:
        print(payload)
    else:
        args.out.write_text(payload + "\n", encoding="utf-8")


if __name__ == "__main__":
    _cli()
