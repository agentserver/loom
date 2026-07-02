"""argparse CLI for `eval-metrics extract` (spec §3).

Exit codes:
  0 — success
  2 — any input-validation rejection (§7 (b)–(e)) or DB open failure
  3 — I/O failure writing --out

Design notes:

- No default for `--format` (spec §3): a silent typo like `--format js`
  should fail loudly, not fall through to CSV. `required=True` on the
  argparse choice gets us exit-code 2 automatically.
- The top-level command uses a subparsers registry so future
  `report` / `sanity` subcommands can slot in without breaking flag
  parsing. Today only `extract` exists.
- Every metric that returns null emits a stderr warning per spec §7 (g)
  (unconditional, regardless of reason). Warnings live here rather
  than inside metric closures because closures should not know about
  file-descriptor plumbing.
"""

from __future__ import annotations

import argparse
import os
import sqlite3
import sys
from typing import Sequence

from eval_metrics import csv_out, json_out
from eval_metrics.db import open_observer_db
from eval_metrics.filter import (
    ALLOWED_COLUMNS,
    RunsFilterError,
    compile_runs_filter,
)
from eval_metrics.metrics import (
    NOTE_UPSTREAM_MISSING,
    Context,
    Metric,
    MetricResult,
    metrics_for_set,
    valid_metric_sets,
)
from eval_metrics.paths import (
    PathValidationError,
    open_out_file,
    validate_observer_db,
    validate_out_path,
)


# Owner hints for stderr warnings on upstream-missing metrics (spec §7 (g)
# template `warn: metric <name> returned null: upstream data missing
# (owner: 12号 §<section>)`). Only metrics whose owner is unambiguous
# from the spec §2 rows get an entry; the fallback prints without a
# trailing owner-parenthetical.
_OWNER_HINTS: dict[str, str] = {
    # §2.1 (lifecycle) upstream-missing metrics
    "ManualSetupStepCount": "12号 §D8",
    "ConfigTouchCount": "12号 §D8",
    "StateContinuityRate": "12号 §A6",
    # §2.2 (contracted)
    "ContractCompleteness": "12号 §D1 (cohort join key)",
    "PreExecutionFaultCatchRate": "12号 §A3",
    "ContractViolationRate": "12号 §A4",
    "MissingArtifactDetectionRate": "12号 §A3",
    "PolicyViolationPreventionRate": "12号 §A3",
    "RecoverySuccessRate": "12号 §A6",
    "DuplicateSideEffectRate": "12号 §A6",
    # §2.3 (user-promoted)
    "PromotionCandidateSurfacingRate": "12号 §B1",
    "UserInitiatedSynthesisSuccessRate": "12号 §B2",
    "ValidationFalseAcceptRate": "12号 §B3",
    "TimeFromUserDecisionToRegisteredMCP": "12号 §B6",
    "RegistryLookupHitRate": "12号 §B4",
    "CapabilityReuseRate": "12号 §B4",
    "RepeatedGenerationRate": "12号 §B4",
    "PromotionAdoptionRate": "12号 §B1/§B2",
    "AdHocScriptTaskShare": "12号 §B1",
    "GeneratedCapabilityDefectRate": "12号 §B4 + §D4 tag",
    "ReuseSpeedup": "12号 §B",
    "HumanEditCount": "12号 §D8 (new runs column)",
    "TokenUsage": "12号 §D8 / §D6a (new runs columns)",
    # §2.4 (semantic)
    "CapabilityRecall": "12号 §F4",
    "CapabilityPrecision": "12号 §F (smoke tests)",
    # §2.5 (overhead)
    "DriverPlanningOverhead": "12号 §D7",
    "TaskDispatchLatency": "12号 §D7",
    "TunnelOverhead": "12号 §D7",
    "ArtifactTransferThroughput": "12号 §D7",
    "ObserverOverhead": "12号 §D7",
    "ModelProxyOverhead": "12号 §D7",
    "TimeToFirstTask": "12号 §C4 / §D6c",
    "SetupFailureRate": "12号 §C4 / §D6c",
}


def _build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        prog="eval-metrics",
        description="Extract paper metrics from an observer SQLite DB (see docs/specs/wt2-metric-extract.spec.md).",
    )
    sub = p.add_subparsers(dest="cmd", metavar="cmd")
    # NOTE: not required=True, so `--help` at the top level still works
    # even without a subcommand; we defer the missing-subcommand error
    # to `main()` so it exits 2 (validation) not argparse's default 0
    # after printing usage.

    extract = sub.add_parser(
        "extract",
        help="Extract metrics from a runs-populated observer DB.",
        description="Emit a single CSV row or JSON object of paper metrics.",
    )
    extract.add_argument(
        "--observer-db", required=True,
        help="Path to observer SQLite DB (spec §7 (b)).",
    )
    extract.add_argument(
        "--runs-filter", default=None,
        help=(
            "Optional WHERE-clause fragment restricted to columns "
            f"{sorted(ALLOWED_COLUMNS)} (spec §7 (c))."
        ),
    )
    extract.add_argument(
        "--format", required=True, choices=("csv", "json"),
        help="Output format (no default — silent-typo hazard, spec §3).",
    )
    extract.add_argument(
        "--out", default=None,
        help="Output path; default stdout (spec §7 (d)).",
    )
    extract.add_argument(
        "--metric-set", default="full", choices=valid_metric_sets(),
        help="Subset of the 41-metric catalog (spec §3.2, §7 (e)).",
    )
    return p


def _emit_stderr_warning(name: str, note: str) -> None:
    """Emit one stderr line per spec §7 (g) template."""
    if note == NOTE_UPSTREAM_MISSING:
        owner = _OWNER_HINTS.get(name)
        suffix = f" (owner: {owner})" if owner else ""
        sys.stderr.write(
            f"[eval-metrics] warn: metric {name} returned null: upstream data missing{suffix}\n"
        )
    else:
        # denominator zero (the only other closed-set value)
        sys.stderr.write(
            f"[eval-metrics] warn: metric {name} returned null: {note}\n"
        )


def _select_runs(conn: sqlite3.Connection, filter_fragment: str | None) -> list:
    """Run the SELECT against `runs` with the optional --runs-filter.

    Column list is `runs.*`; we return sqlite3.Row objects so metric
    closures can look up columns by name (schema-drift resilience).
    """
    if filter_fragment is None:
        return conn.execute("SELECT * FROM runs").fetchall()
    where_sql, params = compile_runs_filter(filter_fragment)
    return conn.execute(
        f"SELECT * FROM runs WHERE {where_sql}", params
    ).fetchall()


def _run_extract(args: argparse.Namespace) -> int:
    # --- Path + DB validation (spec §7 (a)–(b)) ---
    try:
        resolved, fd = validate_observer_db(args.observer_db)
    except PathValidationError as e:
        sys.stderr.write(f"{e}\n")
        return 2
    try:
        conn = open_observer_db(resolved, fd)
    except sqlite3.Error as e:
        sys.stderr.write(f"[eval-metrics] cannot open DB: {e}\n")
        return 2

    # --- Output-path validation done BEFORE any computation to fail fast ---
    out_target = None  # type: ignore[assignment]
    if args.out is not None:
        try:
            out_target = validate_out_path(args.out)
        except PathValidationError as e:
            sys.stderr.write(f"{e}\n")
            return 2

    # --- Cohort selection (spec §3.1 --runs-filter) ---
    try:
        runs = _select_runs(conn, args.runs_filter)
    except RunsFilterError as e:
        sys.stderr.write(f"{e}\n")
        return 2
    except sqlite3.Error as e:
        # SELECT itself failed (e.g. runs table missing → operational
        # error). Treat as validation-level exit-2 rather than I/O.
        sys.stderr.write(f"[eval-metrics] SELECT failed: {e}\n")
        return 2

    row_count = len(runs)
    metrics: list[Metric] = metrics_for_set(args.metric_set)
    ctx = Context(conn=conn, runs=runs, row_count=row_count)

    # --- Compute every metric; collect results + emit stderr warnings ---
    results: dict[str, MetricResult] = {}
    for m in metrics:
        try:
            res = m.compute(ctx)
        except Exception as e:  # noqa: BLE001 — turn any compute bug into
            # a stderr line and a null cell; the extractor should never
            # crash a paper back-fill mid-batch.
            sys.stderr.write(
                f"[eval-metrics] error: metric {m.name} compute failed: {e}\n"
            )
            res = MetricResult(m.null_value(), NOTE_UPSTREAM_MISSING)
        results[m.name] = res
        if res.note is not None:
            _emit_stderr_warning(m.name, res.note)

    # --- Serialise + write ---
    def _write(fp) -> None:
        if args.format == "csv":
            csv_out.write_csv(fp, args.metric_set, row_count, metrics, results)
        else:
            json_out.write_json(fp, args.metric_set, row_count, metrics, results)

    if args.out is None:
        try:
            _write(sys.stdout)
        except OSError as e:
            sys.stderr.write(f"[eval-metrics] stdout write failed: {e}\n")
            return 3
        return 0

    assert out_target is not None  # unreachable — args.out was set
    try:
        out_fd = open_out_file(out_target)
    except OSError as e:
        # Spec §3 exit codes: 2 = input validation (including
        # --out atomic-open failure at spec §7 (d) step 4, since that
        # is a TOCTOU-detection reject, not an I/O failure); 3 is
        # reserved for **write-time** I/O failures below.
        # open_out_file already closed the pinned dir_fd via
        # target.close() in its `finally` before raising, so no leak.
        sys.stderr.write(f"[eval-metrics] --out open failed: {e}\n")
        return 2

    try:
        # Wrap the fd in a text stream that owns it (closefd=True).
        with os.fdopen(out_fd, "w", encoding="utf-8", closefd=True) as fp:
            _write(fp)
    except OSError as e:
        sys.stderr.write(f"[eval-metrics] --out write failed: {e}\n")
        return 3
    return 0


def main(argv: Sequence[str] | None = None) -> int:
    parser = _build_parser()
    args = parser.parse_args(argv)
    if args.cmd is None:
        parser.print_help(sys.stderr)
        return 2
    if args.cmd == "extract":
        return _run_extract(args)
    # Unreachable: argparse rejects unknown subcommands, but keep as
    # defense-in-depth.
    sys.stderr.write(f"[eval-metrics] unknown subcommand: {args.cmd}\n")
    return 2
