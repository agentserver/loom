#!/usr/bin/env python3
"""Collector for canonical_key = manual_steps_ssh.

**Primary source (required)**: `runs.csv` `ManualSetupStepCount` column.
**Optional cross-check**: `steps.log` transcript (spec §7 (d)); if present
its ^(ssh|scp|rsync|mkdir|cd) grep count MUST equal the runs.csv value,
otherwise the collector exits 2 ErrManualStepsMismatch. There is NO
transcript-only backdoor — this preserves the 唯一数据源约定
(evaluation_v3.md §6.7): the paper number always comes from the main
experiment.

Exit codes:
    0  success
    2  any error (specific error names printed to stderr)
"""

from __future__ import annotations

import argparse
import csv
import hashlib
import json
import re
import sys
from pathlib import Path

CANONICAL_KEY = "manual_steps_ssh"
UNIT = "int"

_TRANSCRIPT_RE = re.compile(r"^(ssh|scp|rsync|mkdir|cd) ")

# Stable error tokens (also grep-referenced from tests).
ERR_MISSING_SOURCE = "ErrManualStepsMissingSource"
ERR_MISSING_RUNS_CSV = "ErrManualStepsMissingRunsCSV"
ERR_MISMATCH = "ErrManualStepsMismatch"


def _sha256_file(p: Path) -> str:
    h = hashlib.sha256()
    with p.open("rb") as f:
        for chunk in iter(lambda: f.read(65536), b""):
            h.update(chunk)
    return h.hexdigest()


def _count_transcript_lines(steps_log: Path) -> int:
    n = 0
    for raw_line in steps_log.read_text(encoding="utf-8").splitlines():
        line = raw_line.rstrip()
        if not line or line.lstrip().startswith("#"):
            continue
        if _TRANSCRIPT_RE.match(line):
            n += 1
    return n


def _read_runs_csv_step_count(runs_csv: Path, workload_id: str, baseline: str) -> int:
    if not runs_csv.exists():
        print(f"error: runs.csv not found: {runs_csv}", file=sys.stderr)
        sys.exit(2)
    values: list[int] = []
    with runs_csv.open(newline="", encoding="utf-8") as f:
        for row in csv.DictReader(f):
            if row.get("workload_id") != workload_id:
                continue
            if row.get("baseline_or_ablation") != baseline:
                continue
            raw = row.get("ManualSetupStepCount", "").strip()
            if not raw:
                continue
            values.append(int(raw))
    if not values:
        print(
            f"error: no ManualSetupStepCount rows for workload_id={workload_id!r} "
            f"baseline={baseline!r} in {runs_csv}",
            file=sys.stderr,
        )
        sys.exit(2)
    # All manual_ssh rows for the same workload should share the same
    # ManualSetupStepCount (the count is a per-workload constant of the
    # baseline script, not a per-run measurement). Assert to catch
    # accidentally-varying fixtures.
    if len(set(values)) != 1:
        print(
            f"error: ManualSetupStepCount rows disagree for "
            f"workload_id={workload_id!r}, baseline={baseline!r}: {values}",
            file=sys.stderr,
        )
        sys.exit(2)
    return values[0]


def collect(
    runs_csv: Path | None,
    workload_id: str,
    baseline: str,
    steps_log: Path | None,
) -> dict:
    if runs_csv is None and steps_log is None:
        print(f"error: {ERR_MISSING_SOURCE}: pass --runs-csv (primary) "
              "and optionally --steps-log for cross-check",
              file=sys.stderr)
        sys.exit(2)
    if runs_csv is None:
        # Transcript-only path is deliberately forbidden — keeps the 唯一
        # 数据源 boundary intact.
        print(f"error: {ERR_MISSING_RUNS_CSV}: --steps-log requires --runs-csv "
              "(transcript is a cross-check, not an alternative source)",
              file=sys.stderr)
        sys.exit(2)

    primary = _read_runs_csv_step_count(runs_csv, workload_id, baseline)

    cross_check: dict = {"transcript_count": None, "transcript_source_sha256": None}
    if steps_log is not None:
        if not steps_log.exists():
            print(f"error: steps.log not found: {steps_log}", file=sys.stderr)
            sys.exit(2)
        transcript_count = _count_transcript_lines(steps_log)
        transcript_sha = _sha256_file(steps_log)
        cross_check = {
            "transcript_count": transcript_count,
            "transcript_source_sha256": transcript_sha,
        }
        if transcript_count != primary:
            print(
                f"error: {ERR_MISMATCH}: runs.csv ManualSetupStepCount={primary} "
                f"but steps.log transcript count={transcript_count} "
                f"(workload_id={workload_id!r} baseline={baseline!r})",
                file=sys.stderr,
            )
            sys.exit(2)

    return {
        "canonical_key": CANONICAL_KEY,
        "workload_id": workload_id,
        "baseline": baseline,
        "raw_value": int(primary),
        "unit": UNIT,
        "source_file": str(runs_csv.resolve()),
        "source_sha256": _sha256_file(runs_csv),
        "cross_check": cross_check,
    }


def _cli() -> None:
    p = argparse.ArgumentParser(description="Collector for manual_steps_ssh.")
    p.add_argument("--runs-csv", type=Path, default=None,
                   help="Main-experiment runs.csv (primary source; REQUIRED).")
    p.add_argument("--workload-id", required=True)
    p.add_argument("--baseline", default="manual_ssh")
    p.add_argument("--steps-log", type=Path, default=None,
                   help="Optional transcript file for cross-check.")
    p.add_argument("--out", type=Path, default=None)
    args = p.parse_args()

    if not args.workload_id:
        print("error: --workload-id required", file=sys.stderr)
        sys.exit(2)

    record = collect(args.runs_csv, args.workload_id, args.baseline, args.steps_log)
    payload = json.dumps(record, ensure_ascii=False, sort_keys=True)
    if args.out is None:
        print(payload)
    else:
        args.out.write_text(payload + "\n", encoding="utf-8")


if __name__ == "__main__":
    _cli()
