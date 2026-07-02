"""Lifecycle metrics (spec §2.1 — metrics #1..#9).

Populated today (data source landed):
  #1 TaskSuccessRate, #2 LifecycleClosureRate, #3 TimeToCompletion,
  #4 HumanContextSelectionCount, #5 WrongContextFailureRate,
  #6 ArtifactCorrectnessRate.

Emit null today (upstream missing per spec §2.1 note):
  #7 ManualSetupStepCount (needs `runs.manual_setup_step_count`),
  #8 ConfigTouchCount (needs `runs.config_touch_count`),
  #9 StateContinuityRate (needs 12号 §A6 resume-audit; the paper's
     "traced across driver/slave/restart" join is not derivable from
     `runs` alone).

Metric-set cross-listings per spec §2.1 membership table:
  #1, #3       → also user-promoted (08:185-186 E4 Stage-A/B line)
  #4, #5       → also semantic (08:140 E2 metrics list)
  #7, #8       → also overhead (08:87 / E6)
  #22, #23 (cross into lifecycle from §2.3) — handled in user_promoted.py.
"""

from __future__ import annotations

from datetime import datetime
from typing import Iterable

from eval_metrics.db import runs_column_present
from eval_metrics.metrics import (
    NOTE_DENOMINATOR_ZERO,
    NOTE_UPSTREAM_MISSING,
    Context,
    Metric,
    MetricResult,
)


# ---------------------------------------------------------------------------
# Helpers shared with §2.5 RoutingLatencyP50P95 (percentile calc).
# ---------------------------------------------------------------------------

def linear_percentile(sorted_data: list[float], q: float) -> float:
    """(n-1)*q linear-interp percentile.

    Matches numpy `np.percentile(..., method='linear')` and
    `statistics.quantiles(..., method='inclusive')` — the convention the
    spec §4.1 hand-computed values were derived under. Kept local
    (no numpy dependency) per the plan §4 "no pandas / numpy in
    production" rule.

    Precondition: sorted_data is non-empty and sorted ascending.
    """
    n = len(sorted_data)
    if n == 1:
        return float(sorted_data[0])
    idx = (n - 1) * q
    lo = int(idx)
    hi = min(lo + 1, n - 1)
    frac = idx - lo
    a = float(sorted_data[lo])
    b = float(sorted_data[hi])
    return a + frac * (b - a)


def _parse_ts(ts: str) -> float:
    """Return POSIX seconds for an ISO-8601 UTC timestamp.

    Kept tolerant of both `...Z` and `...+00:00`; the observer writer
    emits `Z` (via `time.Now().UTC().Format(time.RFC3339)`), but
    fixture builders that go through `datetime.isoformat` emit `+00:00`.
    """
    if ts.endswith("Z"):
        ts = ts[:-1] + "+00:00"
    return datetime.fromisoformat(ts).timestamp()


# ---------------------------------------------------------------------------
# #1  TaskSuccessRate
# ---------------------------------------------------------------------------

def _task_success_rate(ctx: Context) -> MetricResult:
    if ctx.row_count == 0:
        return MetricResult(None, NOTE_DENOMINATOR_ZERO)
    hits = sum(1 for r in ctx.runs if r["success_oracle_result"] == "pass")
    return MetricResult(hits / ctx.row_count)


# ---------------------------------------------------------------------------
# #2  LifecycleClosureRate
# ---------------------------------------------------------------------------

def _lifecycle_closure_rate(ctx: Context) -> MetricResult:
    if ctx.row_count == 0:
        return MetricResult(None, NOTE_DENOMINATOR_ZERO)
    # 5-column AND per spec §2.1 #2. "artifact_hashes != '[]'" — we
    # compare the raw string, matching the observer DEFAULT '[]'.
    def _closed(r) -> bool:
        return (
            r["success_oracle_result"] == "pass"
            and r["capability_snapshot_hash"] != ""
            and r["task_contract_hash"] != ""
            and r["artifact_hashes"] != "[]"
            and r["observer_trace_path"] != ""
        )

    hits = sum(1 for r in ctx.runs if _closed(r))
    return MetricResult(hits / ctx.row_count)


# ---------------------------------------------------------------------------
# #3  TimeToCompletion — structured {p50_seconds, p95_seconds, mean_seconds, count}
# ---------------------------------------------------------------------------

_TTC_SUBKEYS = ("p50_seconds", "p95_seconds", "mean_seconds", "count")


def _time_to_completion(ctx: Context) -> MetricResult:
    # Excludes rows where either timestamp is empty (spec §2.1 #3).
    durations: list[float] = []
    for r in ctx.runs:
        start, end = r["start_time"], r["end_time"]
        if not start or not end:
            continue
        durations.append(_parse_ts(end) - _parse_ts(start))
    if not durations:
        # Denominator zero: no row supplied both timestamps. Structured
        # null shape = every subkey None (spec §2.5 rule generalises).
        return MetricResult(
            {k: None for k in _TTC_SUBKEYS}, NOTE_DENOMINATOR_ZERO
        )
    durations.sort()
    return MetricResult({
        "p50_seconds": linear_percentile(durations, 0.5),
        "p95_seconds": linear_percentile(durations, 0.95),
        "mean_seconds": sum(durations) / len(durations),
        "count": len(durations),
    })


# ---------------------------------------------------------------------------
# #4  HumanContextSelectionCount — sum, not a rate
# ---------------------------------------------------------------------------

def _human_context_selection_count(ctx: Context) -> MetricResult:
    # spec §3.3 empty-DB contract: count metric with LANDED upstream on
    # an empty cohort emits 0, NOT null. `runs.human_intervention_count`
    # is a landed D1 column (PR #56), so this metric returns 0 for
    # ctx.row_count == 0 rather than nulling.
    return MetricResult(sum(int(r["human_intervention_count"]) for r in ctx.runs))


# ---------------------------------------------------------------------------
# #5  WrongContextFailureRate — 5-tag D4 taxonomy per spec §2.1 #5
# ---------------------------------------------------------------------------

_WRONG_CONTEXT_TAGS: frozenset[str] = frozenset({
    "wrong-context",
    "missing-file",
    "wrong-version",
    "forbidden-cred",
    "stale-capability",
})


def _wrong_context_failure_rate(ctx: Context) -> MetricResult:
    if ctx.row_count == 0:
        return MetricResult(None, NOTE_DENOMINATOR_ZERO)
    hits = sum(1 for r in ctx.runs if r["failure_category"] in _WRONG_CONTEXT_TAGS)
    return MetricResult(hits / ctx.row_count)


# ---------------------------------------------------------------------------
# #6  ArtifactCorrectnessRate — denominator scoped to non-empty artifacts
# ---------------------------------------------------------------------------

def _artifact_correctness_rate(ctx: Context) -> MetricResult:
    denom = [r for r in ctx.runs if r["artifact_hashes"] != "[]"]
    if not denom:
        return MetricResult(None, NOTE_DENOMINATOR_ZERO)
    hits = sum(1 for r in denom if r["success_oracle_result"] == "pass")
    return MetricResult(hits / len(denom))


# ---------------------------------------------------------------------------
# #7  ManualSetupStepCount / #8  ConfigTouchCount — column-conditional sums
# ---------------------------------------------------------------------------

def _make_optional_column_sum(column: str):
    """Factory for #7 / #8 / #40 — sum(column) if landed, else null.

    Spec §2.1 note: the extractor probes `PRAGMA table_info(runs)` and
    switches from `null` to `sum(...)` transparently. The `runs` DDL
    landed by PR #56 does not include these columns, so the null branch
    is the current behavior; a future 12号 §D8 patch flips them
    automatically.
    """
    def _compute(ctx: Context) -> MetricResult:
        if not runs_column_present(ctx.conn, column):
            return MetricResult(None, NOTE_UPSTREAM_MISSING)
        # column is present but the row objects come from the same
        # SELECT we ran earlier; we re-issue a small aggregate query
        # so we do not have to re-select all rows with the extra column.
        # NOTE: this stays inside the cohort by joining on run_id.
        run_ids = [r["run_id"] for r in ctx.runs]
        if not run_ids:
            # column landed but cohort empty → real 0 (spec §3.3).
            return MetricResult(0)
        placeholders = ",".join("?" for _ in run_ids)
        row = ctx.conn.execute(
            f"SELECT COALESCE(SUM({column}), 0) FROM runs WHERE run_id IN ({placeholders})",
            run_ids,
        ).fetchone()
        return MetricResult(int(row[0]))

    return _compute


# ---------------------------------------------------------------------------
# #9  StateContinuityRate — upstream missing (12号 §A6 resume-audit)
# ---------------------------------------------------------------------------

def _state_continuity_rate(_ctx: Context) -> MetricResult:
    # Rationale: 08:39 numerator explicitly requires "traced across
    # driver/slave/restart", which needs 12号 §A6's write_id dedup +
    # resume-event stream. That worktree has not landed; a resume-side
    # signal is not derivable from `runs` alone. Emit null unconditionally.
    return MetricResult(None, NOTE_UPSTREAM_MISSING)


# ---------------------------------------------------------------------------
# Registry
# ---------------------------------------------------------------------------

METRICS: list[Metric] = [
    Metric(
        number=1, name="TaskSuccessRate", section="lifecycle",
        metric_sets=frozenset({"lifecycle", "user-promoted"}),
        subkeys=(), compute=_task_success_rate,
    ),
    Metric(
        number=2, name="LifecycleClosureRate", section="lifecycle",
        metric_sets=frozenset({"lifecycle"}),
        subkeys=(), compute=_lifecycle_closure_rate,
    ),
    Metric(
        number=3, name="TimeToCompletion", section="lifecycle",
        metric_sets=frozenset({"lifecycle", "user-promoted"}),
        subkeys=_TTC_SUBKEYS, compute=_time_to_completion,
    ),
    Metric(
        number=4, name="HumanContextSelectionCount", section="lifecycle",
        metric_sets=frozenset({"lifecycle", "semantic"}),
        subkeys=(), compute=_human_context_selection_count,
    ),
    Metric(
        number=5, name="WrongContextFailureRate", section="lifecycle",
        metric_sets=frozenset({"lifecycle", "semantic"}),
        subkeys=(), compute=_wrong_context_failure_rate,
    ),
    Metric(
        number=6, name="ArtifactCorrectnessRate", section="lifecycle",
        metric_sets=frozenset({"lifecycle"}),
        subkeys=(), compute=_artifact_correctness_rate,
    ),
    Metric(
        number=7, name="ManualSetupStepCount", section="lifecycle",
        metric_sets=frozenset({"lifecycle", "overhead"}),
        subkeys=(), compute=_make_optional_column_sum("manual_setup_step_count"),
    ),
    Metric(
        number=8, name="ConfigTouchCount", section="lifecycle",
        metric_sets=frozenset({"lifecycle", "overhead"}),
        subkeys=(), compute=_make_optional_column_sum("config_touch_count"),
    ),
    Metric(
        number=9, name="StateContinuityRate", section="lifecycle",
        metric_sets=frozenset({"lifecycle"}),
        subkeys=(), compute=_state_continuity_rate,
    ),
]
