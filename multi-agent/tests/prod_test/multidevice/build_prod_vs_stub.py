"""build_prod_vs_stub.py — WT-3-prod-multidevice prod-vs-stub comparison CLI.

Spec §5. Produces `prod_vs_stub.csv` with columns:
    workload,metric,stub_value,prod_value,abs_diff,rel_diff_pct,verdict

Plus comment-header meta lines:
    # stub_source_path=<path>
    # stub_source_sha256=<64-hex>
    # prod_dir=<path>
    # generated_at=<value from --generated-at>
    # script_version=v1

--sample-mode: reads bundled fixture stub table + bundled fake_prod
fixtures; the allowlist enforcement is bypassed with a warning (so the
sample smoke can round-trip verification of column structure + verdict
logic without depending on the real stub CSV shape). Test
`test_stub_source_allowlist_reject` covers the non-sample-mode path.

HARNESS-ONLY: this worktree only exercises --sample-mode. The real-run
worktree paper/v3/p3-prod-multidevice-run invokes without --sample-mode
against the paper table.
"""
from __future__ import annotations

import argparse
import csv
import hashlib
import json
import pathlib
import sys
from typing import Iterable

# Spec §5.2: `--stub-table` allow-list.
_STUB_ALLOWLIST = (
    "multi-agent/tests/eval/results/smoke/paper/table2_sample.csv",
    "multi-agent/tests/eval/results/paper/table2.csv",
)

# Spec §5.4: core metric list.
_CORE_METRICS = (
    "TaskSuccessRate",
    "TimeToCompletion",
    "WrongContextFailureRate",
    "RoutingAccuracy",
)

# Spec §5.4: tolerance table.
_ABS_DIFF_TOLERANCE_PP = {
    "TaskSuccessRate": 5.0,
    "WrongContextFailureRate": 5.0,
    "RoutingAccuracy": 5.0,
}
_REL_DIFF_TOLERANCE_PCT = {
    "TimeToCompletion": 100.0,
}

_SCRIPT_VERSION = "v1"


def _sha256_file(path: pathlib.Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(65536), b""):
            h.update(chunk)
    return h.hexdigest()


def _load_stub_table(path: pathlib.Path) -> dict[str, dict[str, float]]:
    """Return {workload_id: {metric: value}} from the stub CSV."""
    out: dict[str, dict[str, float]] = {}
    with path.open("r", encoding="utf-8", newline="") as f:
        reader = csv.DictReader(f)
        # DictReader gracefully handles subset columns.
        for row in reader:
            workload = row.get("workload_id")
            if not workload:
                continue
            per: dict[str, float] = {}
            for m in _CORE_METRICS:
                v = row.get(m)
                if v is None or v == "":
                    continue
                try:
                    per[m] = float(v)
                except ValueError:
                    continue
            if per:
                out[workload] = per
    return out


def _load_prod_dir(prod_dir: pathlib.Path) -> dict[str, dict[str, float]]:
    """Return {workload_id: {metric: median_value}} from prod dir JSONs.

    Each `<workload>.json` file is expected to have the shape used by
    fixtures/fake_prod/*.json (workload_id + median block). Files
    without those keys are skipped with a warning.
    """
    out: dict[str, dict[str, float]] = {}
    for jf in sorted(prod_dir.glob("*.json")):
        try:
            doc = json.loads(jf.read_text(encoding="utf-8"))
        except json.JSONDecodeError as exc:
            print(f"build_prod_vs_stub: skipping {jf.name}: {exc}", file=sys.stderr)
            continue
        wid = doc.get("workload_id")
        median = doc.get("median")
        if not wid or not isinstance(median, dict):
            print(
                f"build_prod_vs_stub: skipping {jf.name}: missing workload_id or median",
                file=sys.stderr,
            )
            continue
        vals: dict[str, float] = {}
        for m in _CORE_METRICS:
            if m in median:
                try:
                    vals[m] = float(median[m])
                except (TypeError, ValueError):
                    continue
        if vals:
            out[wid] = vals
    return out


def _verdict(metric: str, abs_diff: float, rel_diff_pct: float) -> str:
    """Return one of consistent / divergent-explained / divergent-unexplained.

    Sample-mode data lacks the analysis.md cross-reference, so we
    only ever emit `consistent` or `divergent-unexplained` here —
    `divergent-explained` requires the real-run worktree to annotate
    each finding.
    """
    if metric in _ABS_DIFF_TOLERANCE_PP:
        tol = _ABS_DIFF_TOLERANCE_PP[metric]
        if abs(abs_diff) * 100.0 <= tol:
            # `TaskSuccessRate` / `WrongContextFailureRate` / `RoutingAccuracy`
            # are ratios ∈ [0, 1]; convert diff to percentage-points before
            # comparing against the pp tolerance.
            return "consistent"
        return "divergent-unexplained"
    if metric in _REL_DIFF_TOLERANCE_PCT:
        tol = _REL_DIFF_TOLERANCE_PCT[metric]
        if abs(rel_diff_pct) <= tol:
            return "consistent"
        return "divergent-unexplained"
    # A metric that is neither pp-tolerant nor rel-tolerant is not
    # supposed to be in the core set at all.
    return "divergent-unexplained"


def _rel_diff_pct(stub: float, prod: float) -> float:
    if stub == 0:
        return 0.0 if prod == 0 else float("inf")
    return (prod - stub) / stub * 100.0


def _stub_path_is_allowlisted(stub_arg: str) -> bool:
    # Match on any suffix that exactly equals an allowlisted path — the
    # caller may pass a relative path from any cwd. Prevents typos in
    # the path from silently binding to a different CSV.
    for allowed in _STUB_ALLOWLIST:
        if stub_arg == allowed or stub_arg.endswith("/" + allowed):
            return True
    return False


def _emit(rows: Iterable[dict[str, object]], out_path: pathlib.Path,
          stub_path: pathlib.Path, stub_sha: str, prod_dir: pathlib.Path,
          generated_at: str) -> None:
    out_path.parent.mkdir(parents=True, exist_ok=True)
    with out_path.open("w", encoding="utf-8", newline="") as f:
        # Meta header (comment lines, prefixed `#`).
        f.write(f"# stub_source_path={stub_path}\n")
        f.write(f"# stub_source_sha256={stub_sha}\n")
        f.write(f"# prod_dir={prod_dir}\n")
        f.write(f"# generated_at={generated_at}\n")
        f.write(f"# script_version={_SCRIPT_VERSION}\n")
        writer = csv.DictWriter(
            f,
            fieldnames=(
                "workload", "metric", "stub_value", "prod_value",
                "abs_diff", "rel_diff_pct", "verdict",
            ),
        )
        writer.writeheader()
        for row in rows:
            writer.writerow(row)


def main() -> int:
    ap = argparse.ArgumentParser(prog="build_prod_vs_stub.py")
    ap.add_argument("--prod-dir", required=True, type=pathlib.Path)
    ap.add_argument("--stub-table", required=True, type=str,
                    help="Path to stub CSV. Must be in allow-list unless --sample-mode.")
    ap.add_argument("--out", required=True, type=pathlib.Path)
    ap.add_argument("--sample-mode", action="store_true",
                    help="Bypass allow-list; use bundled fixture stub table for column-structure verification.")
    ap.add_argument("--generated-at", required=True,
                    help="Deterministic timestamp string; the script never calls datetime.now(). "
                         "Real-run worktree passes ISO 8601 UTC.")
    args = ap.parse_args()

    if args.sample_mode:
        # Use bundled fixture stub table for deterministic column-
        # structure verification. Warn on stderr so it is visible.
        here = pathlib.Path(__file__).resolve().parent
        stub_path = here / "fixtures" / "fake_stub_table.csv"
        if not stub_path.is_file():
            print(f"build_prod_vs_stub: fixture stub missing: {stub_path}",
                  file=sys.stderr)
            return 2
        print(
            f"build_prod_vs_stub: --sample-mode active; using fixture "
            f"stub {stub_path} (allow-list bypassed intentionally).",
            file=sys.stderr,
        )
    else:
        if not _stub_path_is_allowlisted(args.stub_table):
            print(
                f"build_prod_vs_stub: stub source path not in allow-list: "
                f"{args.stub_table}. Allowed: {_STUB_ALLOWLIST}",
                file=sys.stderr,
            )
            return 2
        stub_path = pathlib.Path(args.stub_table)

    if not stub_path.is_file():
        print(f"build_prod_vs_stub: stub CSV not found: {stub_path}",
              file=sys.stderr)
        return 2
    if not args.prod_dir.is_dir():
        print(f"build_prod_vs_stub: prod-dir not found: {args.prod_dir}",
              file=sys.stderr)
        return 2

    stub_sha = _sha256_file(stub_path)
    stub_data = _load_stub_table(stub_path)
    prod_data = _load_prod_dir(args.prod_dir)

    rows: list[dict[str, object]] = []
    for workload in sorted(set(stub_data) & set(prod_data)):
        for metric in _CORE_METRICS:
            if metric not in stub_data[workload] or metric not in prod_data[workload]:
                continue
            stub_v = stub_data[workload][metric]
            prod_v = prod_data[workload][metric]
            abs_diff = prod_v - stub_v
            rel = _rel_diff_pct(stub_v, prod_v)
            verdict = _verdict(metric, abs_diff, rel)
            rows.append({
                "workload": workload,
                "metric": metric,
                "stub_value": stub_v,
                "prod_value": prod_v,
                "abs_diff": abs_diff,
                "rel_diff_pct": rel,
                "verdict": verdict,
            })

    _emit(rows, args.out, stub_path, stub_sha, args.prod_dir,
          args.generated_at)

    if not rows:
        print(
            "build_prod_vs_stub: WARNING: 0 comparison rows emitted "
            "(prod ∩ stub workload set was empty)",
            file=sys.stderr,
        )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
