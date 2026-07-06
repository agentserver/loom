"""Per-run CLI planning (spec §4).

Pure functions plus one narrow subprocess-free surface. Consumed by
`run.sh` (via `python3 -m lib.plan …`) and by the harness's own
Python callers (`build_paper_tables.py` and the tests).

Naming convention: `plan_command_for_matrix_row` / `plan_command_for_e4_row`
each return an argv list (list[str]) — never a shell string — so callers
can hand it to `subprocess.run(cmd)` without shell splitting.

No I/O beyond reading the YAML manifests handed in via CLI. Nothing
under `tests/eval/results/` gets written by this module — the writers
live in `paper_tables.py` and `run.sh`.
"""
from __future__ import annotations

import argparse
import json
import os
import sys
import uuid
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

import yaml


# ---------------------------------------------------------------------------
# Constants — mirror the runner / registry so a drift shows up here first.
# ---------------------------------------------------------------------------

WORKLOADS: tuple[str, ...] = (
    "cross-device-code-mod",
    "remote-data-processing",
    "windows-only-artifact",
    "missing-parser-converter",
    "credential-bound-model",
)

# 8 ablation flags — mirrors internal/ablation/registry.go canonicalFlags.
ABLATIONS: tuple[str, ...] = (
    "NoCapabilityDiscovery",
    "NoTypedContracts",
    "NoDryRun",
    "NoContractFormalization",
    "NoUserPromotionPath",
    "NoAcceptanceGate",
    "NoRegistryLookup",
    "NoObserver",
)

# 3 baseline CSV labels (matrix.yaml) → run.sh subdirectory name under
# multi-agent/tests/eval/baselines/. WT-2-baselines README §Baselines.
BASELINE_DIR: dict[str, str] = {
    "manual_ssh": "manual_ssh",
    "single_machine_claude_code": "single_machine",
    "cloud_sandbox_e2b": "cloud_sandbox",
}

CONFIGURATIONS: tuple[str, ...] = (
    ("full_loom",) + ABLATIONS + tuple(BASELINE_DIR.keys())
)

FAMILIES: tuple[str, ...] = (
    "api-wrapper-for-local-service",
    "csv-profiler",
    "image-metadata-extractor",
    "log-parser",
    "refund-policy-checker",
)
STAGES: tuple[str, ...] = ("A", "B", "C")
TASKS: tuple[str, ...] = ("first-task", "reuse-1", "reuse-2", "reuse-3")
E4_CONFIGURATIONS: tuple[str, ...] = (
    "full_loom",
    "NoUserPromotionPath",
    "NoAcceptanceGate",
    "NoRegistryLookup",
)

# Smoke output root — enforced by `_smoke_out_root`; the follow-up run
# worktree points this to `tests/eval/results/` and lifts the guard.
SMOKE_ROOT_RELATIVE = "tests/eval/results/smoke"

# Default per-row runner timeout for the fixture smoke — overrides
# spec.timeout_seconds (spec §4.2).
SMOKE_ROW_TIMEOUT = "60s"
# Default per-row runner timeout for the follow-up worktree's 60-run.
FULL_RUN_TIMEOUT = "3600s"


# ---------------------------------------------------------------------------
# Exceptions
# ---------------------------------------------------------------------------


class ErrCloudDryRunMissing(RuntimeError):
    """cloud_sandbox_e2b matrix row without `dry_run: true` (spec §7 (c))."""


class ErrNonLoopbackStubListen(RuntimeError):
    """stub-listen host is not loopback (spec §7 (a))."""


class ErrOutsideSmokeRoot(RuntimeError):
    """--out path is not under smoke/ (spec §4.2)."""


class ErrUnknownAblation(RuntimeError):
    """Matrix row baseline_or_ablation is not a known name."""


class ErrObserverDBCollision(RuntimeError):
    """Two planned rows target the same observer_db path (spec §7 (d)).

    SQLite serialises writers on a per-file basis and holds a fcntl
    lock; two parallel dispatches against one file get SQLITE_BUSY at
    random, which corrupts audit trails. Raised BEFORE dispatch so the
    operator sees the collision as a fatal at plan time, not as
    intermittent SQLITE_BUSY at write time.
    """


# ---------------------------------------------------------------------------
# Loopback + port helpers
# ---------------------------------------------------------------------------


def build_stub_listen(port: int, host: str = "127.0.0.1") -> str:
    """Return `--stub-listen` value; rejects any non-loopback host (§7 (a))."""
    if host not in ("127.0.0.1", "::1", "localhost"):
        raise ErrNonLoopbackStubListen(
            f"stub-listen host must be loopback, got {host!r}"
        )
    if host == "localhost":
        host = "127.0.0.1"
    if not (1 <= port <= 65535):
        raise ValueError(f"stub-listen port out of range: {port}")
    return f"{host}:{port}"


# ---------------------------------------------------------------------------
# Manifest loaders
# ---------------------------------------------------------------------------


def parse_matrix(path: Path) -> list[dict]:
    return yaml.safe_load(Path(path).read_text())


def parse_e4_stages(path: Path) -> list[dict]:
    return yaml.safe_load(Path(path).read_text())


# ---------------------------------------------------------------------------
# Resume keys — stable across plan invocations (spec §4.5).
# ---------------------------------------------------------------------------


def resume_key_for_matrix_row(row: dict) -> str:
    return f"matrix__{row['workload_id']}__{row['baseline_or_ablation']}"


def resume_key_for_e4_row(row: dict) -> str:
    return f"e4__{row['family']}__{row['stage']}__{row['task_id']}__{row['configuration']}"


def parse_resume_key(basename: str) -> str:
    """Recover the resume_key from `<resume_key>__<run_id>.done`.

    Uses `rsplit("__", 1)`; the resume_key itself contains `__`
    separators (2 for matrix, 4 for e4) which are preserved intact.
    """
    if basename.endswith(".done"):
        basename = basename[: -len(".done")]
    left, _run_id = basename.rsplit("__", 1)
    return left


# ---------------------------------------------------------------------------
# Output path builder — smoke root enforcement.
# ---------------------------------------------------------------------------


def _smoke_out_root(base: str) -> Path:
    """Return the smoke/ root and refuse anything else."""
    p = Path(base)
    # Accept an absolute path that ENDS in the smoke root, or a relative
    # path equal to the smoke root, or a relative path anchored at the
    # smoke root. The rule: the path (or one of its parents) must equal
    # `tests/eval/results/smoke`.
    parts = p.parts
    for i in range(len(parts) - 2):
        if parts[i:i + 3] == ("tests", "eval", "results") and (
            i + 3 < len(parts) and parts[i + 3] == "smoke"
        ):
            return p
    if str(p).rstrip("/").endswith(SMOKE_ROOT_RELATIVE):
        return p
    raise ErrOutsideSmokeRoot(
        f"out root must lie under {SMOKE_ROOT_RELATIVE}, got {base!r}"
    )


# ---------------------------------------------------------------------------
# Per-row command planning.
# ---------------------------------------------------------------------------


@dataclass
class RunPlan:
    """One planned dispatch — an argv list + a stable resume key."""

    kind: str           # "matrix" or "e4"
    resume_key: str
    argv: list[str]
    run_id: str
    workload_id: str
    configuration: str
    out_csv: str
    observer_db: str = ""
    baseline_dir: str = ""  # non-empty for baseline rows
    dry_run: bool = False
    extra: dict = field(default_factory=dict)


def _out_csv_for_matrix(
    smoke_root: Path, row: dict, run_id: str
) -> tuple[str, str]:
    """Return (out_csv, observer_db) paths — both under smoke_root."""
    resume_key = resume_key_for_matrix_row(row)
    out_csv = str(smoke_root / "runs" / f"{resume_key}__{run_id}.csv")
    observer_db = str(smoke_root / "dbs" / f"{run_id}.db")
    return out_csv, observer_db


def plan_command_for_matrix_row(
    row: dict,
    *,
    port: int,
    smoke_root: Path,
    run_id: str | None = None,
    timeout: str = SMOKE_ROW_TIMEOUT,
    module_root_prefix: str = "",
    dry_run: bool = False,
) -> RunPlan:
    """Build the argv for one matrix row.

    Row → command shape (spec §4.1):
      full_loom → eval-runner run --workload W --stub-listen 127.0.0.1:P
                   --out C --observer-db D --timeout T
      ablation  → same + --ablation <NAME>
      baseline  → bash tests/eval/baselines/<dir>/run.sh --workload W
                   --out C [--dry-run for cloud_sandbox_e2b]

    Reject any cloud_sandbox_e2b row without `dry_run: true` (§7 (c)).
    """
    smoke_root = _smoke_out_root(smoke_root)
    workload = row["workload_id"]
    conf = row["baseline_or_ablation"]
    if run_id is None:
        run_id = str(uuid.uuid4())
    resume_key = resume_key_for_matrix_row(row)

    if conf in BASELINE_DIR:
        baseline_dir = BASELINE_DIR[conf]
        # cloud must be dry-run.
        row_dry = bool(row.get("dry_run", False))
        if conf == "cloud_sandbox_e2b" and not row_dry:
            raise ErrCloudDryRunMissing(
                f"cloud_sandbox_e2b row missing dry_run: true (workload={workload})"
            )
        out_csv = str(smoke_root / "runs" / f"{resume_key}__{run_id}.csv")
        rel_script = f"{module_root_prefix}tests/eval/baselines/{baseline_dir}/run.sh"
        argv = [
            "bash", rel_script,
            "--workload", workload,
            "--out", out_csv,
        ]
        if row_dry:
            argv.append("--dry-run")
        return RunPlan(
            kind="matrix",
            resume_key=resume_key,
            argv=argv,
            run_id=run_id,
            workload_id=workload,
            configuration=conf,
            out_csv=out_csv,
            baseline_dir=baseline_dir,
            dry_run=row_dry,
        )

    if conf != "full_loom" and conf not in ABLATIONS:
        raise ErrUnknownAblation(
            f"unknown baseline_or_ablation {conf!r} for workload {workload!r}"
        )

    out_csv, observer_db = _out_csv_for_matrix(smoke_root, row, run_id)
    stub_listen = build_stub_listen(port)
    argv = [
        "eval-runner", "run",
        "--workload", workload,
        "--stub-listen", stub_listen,
        "--out", out_csv,
        "--observer-db", observer_db,
        "--timeout", timeout,
        "--run-id", run_id,
    ]
    if conf in ABLATIONS:
        argv += ["--ablation", conf]
    # full_loom rows pass NO --baseline-name — the runner default stamps
    # runs.baseline_or_ablation = "full_loom" (spec §3.2).
    return RunPlan(
        kind="matrix",
        resume_key=resume_key,
        argv=argv,
        run_id=run_id,
        workload_id=workload,
        configuration=conf,
        out_csv=out_csv,
        observer_db=observer_db,
        dry_run=dry_run,
    )


def plan_command_for_e4_row(
    row: dict,
    *,
    port: int,
    smoke_root: Path,
    run_id: str | None = None,
    timeout: str = SMOKE_ROW_TIMEOUT,
    module_root_prefix: str = "",
) -> RunPlan:
    """E4 dispatch — no --stage flag today (spec §4.4 handoff), so the
    argv is identical to a matrix row aside from the resume key.

    The follow-up run worktree adds `--stage {A,B,C}` to the runner and
    re-derives argv here; the smoke does not actually dispatch these.
    """
    smoke_root = _smoke_out_root(smoke_root)
    if run_id is None:
        run_id = str(uuid.uuid4())
    resume_key = resume_key_for_e4_row(row)
    out_csv = str(smoke_root / "runs" / f"{resume_key}__{run_id}.csv")
    observer_db = str(smoke_root / "dbs" / f"{run_id}.db")
    stub_listen = build_stub_listen(port)
    conf = row["configuration"]
    argv = [
        "eval-runner", "run",
        "--workload", row["family"],  # golden family stands in as workload id
        "--stub-listen", stub_listen,
        "--out", out_csv,
        "--observer-db", observer_db,
        "--timeout", timeout,
        "--run-id", run_id,
    ]
    if conf in ABLATIONS:
        argv += ["--ablation", conf]
    return RunPlan(
        kind="e4",
        resume_key=resume_key,
        argv=argv,
        run_id=run_id,
        workload_id=row["family"],
        configuration=conf,
        out_csv=out_csv,
        observer_db=observer_db,
        extra={"stage": row["stage"], "task_id": row["task_id"], "family": row["family"]},
    )


# ---------------------------------------------------------------------------
# Enumeration helpers used by run.sh.
# ---------------------------------------------------------------------------


def enumerate_e4_argvs(
    e4_path: Path,
    *,
    smoke_root: Path,
    starting_port: int = 18100,
    sample_n: int | None = None,
    module_root_prefix: str = "",
    timeout: str = SMOKE_ROW_TIMEOUT,
    use_port_pool: bool = False,
) -> list[RunPlan]:
    """Build the ordered plan list for the 240-row E4 manifest.

    Same shape as `enumerate_matrix_argvs`. `sample_n` truncates from
    the top of the manifest — E4 is only ever exercised end-to-end by
    the follow-up run worktree, but `--resume` in this worktree still
    needs to enumerate it so completed E4 sidecars skip correctly.
    """
    manifest = parse_e4_stages(e4_path)
    if sample_n is not None:
        manifest = manifest[:sample_n]
    plans: list[RunPlan] = []
    pool = None
    port = starting_port
    if use_port_pool:
        from lib.portpool import PortPool
        pool = PortPool(start=starting_port)
    for row in manifest:
        row_port = pool.assign_port() if pool is not None else port
        plan = plan_command_for_e4_row(
            row,
            port=row_port,
            smoke_root=smoke_root,
            timeout=timeout,
            module_root_prefix=module_root_prefix,
        )
        plans.append(plan)
        port += 1
    return plans


def enumerate_matrix_argvs(
    matrix_path: Path,
    *,
    smoke_root: Path,
    starting_port: int = 18100,
    sample_n: int | None = None,
    module_root_prefix: str = "",
    timeout: str = SMOKE_ROW_TIMEOUT,
    deterministic_run_ids: bool = False,
    use_port_pool: bool = False,
) -> list[RunPlan]:
    """Build the ordered plan list for `run.sh --sample N`/`--dry-run`.

    `deterministic_run_ids=True` swaps UUIDv4 for a stable per-row id
    derived from the resume key — required for `--dry-run` snapshot
    tests where the golden file must not churn.

    `use_port_pool=True` allocates each row's stub-listen port through
    `portpool.PortPool.assign_port()`, so an occupied 18100 skips to
    18101 on the real dispatch path (spec §7 (b)). Off by default so
    the deterministic dry-run snapshot stays stable across CI hosts
    (a busy 18100 on the CI runner would otherwise churn the golden).
    """
    matrix = parse_matrix(matrix_path)
    if sample_n is not None:
        matrix = matrix[:sample_n]
    plans: list[RunPlan] = []
    pool = None
    port = starting_port
    if use_port_pool:
        # Import inline so the loopback-guard tests don't have to drag
        # in the portpool socket dependency.
        from lib.portpool import PortPool
        pool = PortPool(start=starting_port)
    for row in matrix:
        run_id = None
        if deterministic_run_ids:
            # Namespace UUID from resume_key so ids stay stable across
            # dry-run snapshots yet remain UUID-shaped.
            resume_key = resume_key_for_matrix_row(row)
            run_id = str(uuid.uuid5(uuid.NAMESPACE_URL, resume_key))
        row_port = pool.assign_port() if pool is not None else port
        plan = plan_command_for_matrix_row(
            row,
            port=row_port,
            smoke_root=smoke_root,
            run_id=run_id,
            timeout=timeout,
            module_root_prefix=module_root_prefix,
        )
        plans.append(plan)
        # baseline rows don't use the stub, but keeping the port
        # counter monotonic makes the sequence easy to reason about
        # when pool is off.
        port += 1
    return plans


# ---------------------------------------------------------------------------
# CLI: `python3 -m lib.plan --dry-run …` / `--sample N …` — consumed by
# `run.sh`. Prints one shell-safe command per line to stdout.
# ---------------------------------------------------------------------------


def _quote(s: str) -> str:
    """Shell-quote for the dry-run output — mirrors shlex.quote but stays
    ASCII-friendly so the snapshot diff is stable."""
    import shlex
    return shlex.quote(s)


def _print_plan(plan: RunPlan) -> None:
    print(" ".join(_quote(a) for a in plan.argv))


def _emit_shim_line(count: int, inject_row: int | None = None) -> None:
    """LOOM_FULLTABLE_DISPATCH_SHIM=1 path."""
    print(f"SHIM: would dispatch {count} rows")


def _cmd_dry_run(args: argparse.Namespace) -> int:
    smoke_root = Path(args.smoke_root)
    plans = enumerate_matrix_argvs(
        Path(args.matrix),
        smoke_root=smoke_root,
        starting_port=args.starting_port,
        module_root_prefix=args.module_root_prefix,
        timeout=args.timeout,
        deterministic_run_ids=True,
    )
    for plan in plans:
        _print_plan(plan)
    return 0


def assert_no_observer_db_collision(plans: list["RunPlan"]) -> None:
    """Fatal guard: refuse a plan list where two rows share an observer_db
    path (spec §7 (d)). Baseline rows have an empty observer_db (they
    don't use the runner's observer) — skip those in the collision check.
    """
    seen: dict[str, str] = {}
    for plan in plans:
        db = plan.observer_db
        if not db:
            continue
        prev = seen.get(db)
        if prev is not None:
            raise ErrObserverDBCollision(
                f"observer_db collision on {db!r}: "
                f"resume_keys {prev!r} and {plan.resume_key!r}"
            )
        seen[db] = plan.resume_key


def _emit_plan_line(plan: RunPlan) -> None:
    payload = {
        "kind": plan.kind,
        "resume_key": plan.resume_key,
        "argv": plan.argv,
        "run_id": plan.run_id,
        "workload_id": plan.workload_id,
        "configuration": plan.configuration,
        "out_csv": plan.out_csv,
        "observer_db": plan.observer_db,
        "baseline_dir": plan.baseline_dir,
        "dry_run": plan.dry_run,
    }
    print(json.dumps(payload))


def _cmd_sample(args: argparse.Namespace) -> int:
    smoke_root = Path(args.smoke_root)
    plans = enumerate_matrix_argvs(
        Path(args.matrix),
        smoke_root=smoke_root,
        starting_port=args.starting_port,
        sample_n=args.n,
        module_root_prefix=args.module_root_prefix,
        timeout=args.timeout,
        use_port_pool=True,   # spec §7 (b): real dispatch retries on EADDRINUSE
    )
    if getattr(args, "include_e4", False) and args.e4:
        plans += enumerate_e4_argvs(
            Path(args.e4),
            smoke_root=smoke_root,
            starting_port=args.starting_port + max(args.n, 1),
            sample_n=args.n,
            module_root_prefix=args.module_root_prefix,
            timeout=args.timeout,
            use_port_pool=True,
        )
    # Spec §7 (d): refuse to emit a plan where two rows would race for
    # the same observer sqlite file. Raise BEFORE any dispatch runs.
    assert_no_observer_db_collision(plans)
    # Emit a JSON list so run.sh can iterate. One plan per line
    # (JSON-per-line) to keep bash parsing trivial.
    for plan in plans:
        _emit_plan_line(plan)
    return 0


def _cmd_print_stub_listen(args: argparse.Namespace) -> int:
    """Emit each planned row's --stub-listen value (spec §7 (b) test seam)."""
    smoke_root = Path(args.smoke_root)
    plans = enumerate_matrix_argvs(
        Path(args.matrix),
        smoke_root=smoke_root,
        starting_port=args.starting_port,
        sample_n=args.n,
        module_root_prefix=args.module_root_prefix,
        timeout=args.timeout,
        use_port_pool=True,
    )
    for plan in plans:
        if "--stub-listen" not in plan.argv:
            continue  # baseline row
        i = plan.argv.index("--stub-listen")
        print(plan.argv[i + 1])
    return 0


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog="lib.plan", description="fulltable planner")
    p.add_argument("--matrix", required=True, help="path to matrix.yaml")
    p.add_argument("--smoke-root", required=True, help="tests/eval/results/smoke")
    p.add_argument("--starting-port", type=int, default=18100)
    p.add_argument("--module-root-prefix", default="",
                   help="prepend to baseline run.sh paths (e.g. tests/eval/…)")
    p.add_argument("--timeout", default=SMOKE_ROW_TIMEOUT)
    sub = p.add_subparsers(dest="cmd", required=True)

    sp_dry = sub.add_parser("dry-run")
    sp_dry.set_defaults(fn=_cmd_dry_run)

    sp_sample = sub.add_parser("sample")
    sp_sample.add_argument("--n", type=int, required=True)
    sp_sample.add_argument("--include-e4", action="store_true",
                           help="also enumerate first N rows of --e4 manifest")
    sp_sample.add_argument("--e4", default="",
                           help="path to e4_stages.yaml (required with --include-e4)")
    sp_sample.set_defaults(fn=_cmd_sample)

    # print-planned-stub-listen: test seam for spec §7 (b). Runs the
    # real dispatch-planning path (use_port_pool=True) and prints
    # each row's --stub-listen value one per line, no subprocess.
    sp_print = sub.add_parser("print-planned-stub-listen")
    sp_print.add_argument("--n", type=int, required=True)
    sp_print.set_defaults(fn=_cmd_print_stub_listen)

    args = p.parse_args(argv)
    try:
        return args.fn(args)
    except ErrObserverDBCollision as e:
        print(f"ErrObserverDBCollision: {e}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
