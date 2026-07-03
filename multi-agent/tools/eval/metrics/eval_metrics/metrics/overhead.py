"""Overhead metrics (spec §2.5 — metrics #31..#39).

Populated today:
  #37 RoutingLatencyP50P95 — structured {p50_ns, p95_ns, count} joined
      from `route_reasons.decision_duration_ns` where the row's
      `decision_started_at` falls inside at least one selected run's
      [start_time, end_time] window. `runs` has no conversation_id,
      so the join key is the time window (spec §2.5 #37 clause).

Emit null today (upstream missing per spec §2.5 note):
  #31 DriverPlanningOverhead   → {p50_ns, p95_ns, count}
  #32 TaskDispatchLatency      → {p50_ns, p95_ns, count}
  #33 TunnelOverhead           → {p50_ns, p95_ns, count}
  #34 ArtifactTransferThroughput → {p50_bytes_per_sec, p95_bytes_per_sec, count}
  #35 ObserverOverhead         → 5-key (latency p50/p95 + CPU delta + mem delta + count)
  #36 ModelProxyOverhead       → 6-key (first-token p50/p95 + tokens/sec p50/p95 + e2e p95 + count)
  #38 TimeToFirstTask          → scalar (12号 §C4/§D6c not landed)
  #39 SetupFailureRate         → scalar (same as #38)

Companion table `route_reasons` may be MISSING (schema not applied →
upstream missing) or PRESENT-BUT-EMPTY (schema applied, cohort has no
dispatches → denominator zero) per spec §4.4 branch — the extractor
distinguishes via `db.companion_table_status`.
"""

from __future__ import annotations

from eval_metrics.db import TableStatus, companion_table_status
from eval_metrics.metrics import (
    NOTE_DENOMINATOR_ZERO,
    NOTE_UPSTREAM_MISSING,
    Context,
    Metric,
    MetricResult,
)
from eval_metrics.metrics.lifecycle import _parse_ts, linear_percentile


# ---------------------------------------------------------------------------
# Structured subkey sets per spec §2.5 / §3.3 flatten list.
# ---------------------------------------------------------------------------

_SUB_NS3 = ("p50_ns", "p95_ns", "count")
_SUB_BPS3 = ("p50_bytes_per_sec", "p95_bytes_per_sec", "count")
_SUB_OBSERVER5 = (
    "latency_p50_ns", "latency_p95_ns",
    "cpu_delta_pct", "mem_delta_bytes",
    "count",
)
_SUB_PROXY6 = (
    "first_token_latency_p50_ns", "first_token_latency_p95_ns",
    "tokens_per_sec_p50", "tokens_per_sec_p95",
    "e2e_latency_p95_ns",
    "count",
)


def _null_struct(subkeys: tuple[str, ...]):
    """Factory: emit `null` in every subkey with upstream-missing note."""
    def _compute(_ctx: Context) -> MetricResult:
        return MetricResult({k: None for k in subkeys}, NOTE_UPSTREAM_MISSING)
    return _compute


def _null_upstream_scalar(_ctx: Context) -> MetricResult:
    return MetricResult(None, NOTE_UPSTREAM_MISSING)


# ---------------------------------------------------------------------------
# #37  RoutingLatencyP50P95 — time-window join on route_reasons
# ---------------------------------------------------------------------------

def _routing_latency_p50p95(ctx: Context) -> MetricResult:
    # Three-way status: missing table = "schema wasn't applied" =
    # operational error, but we surface it as upstream missing per the
    # spec §4.4 asymmetry ("missing companion table treated as null +
    # upstream data missing").
    status = companion_table_status(ctx.conn, "route_reasons")
    if status is TableStatus.MISSING:
        return MetricResult({k: None for k in _SUB_NS3}, NOTE_UPSTREAM_MISSING)
    # If the cohort of runs is empty, no join can possibly land →
    # denominator-zero.
    if ctx.row_count == 0:
        return MetricResult({k: None for k in _SUB_NS3}, NOTE_DENOMINATOR_ZERO)

    # Materialise per-run [start, end] windows once. Use POSIX seconds
    # for the boundary comparison; route_reasons.decision_started_at is
    # an ISO-8601 timestamp too. Rows with unparseable timestamps are
    # skipped (belt against fixture regressions — not expected under
    # the WT-1-run-schema DDL).
    windows: list[tuple[float, float]] = []
    for r in ctx.runs:
        st, et = r["start_time"], r["end_time"]
        if not st or not et:
            continue
        try:
            windows.append((_parse_ts(st), _parse_ts(et)))
        except ValueError:
            continue
    if not windows:
        return MetricResult({k: None for k in _SUB_NS3}, NOTE_DENOMINATOR_ZERO)

    # Pull every route_reasons row; on the paper's cohort size (~10^4
    # runs × maybe similar decisions) this fits comfortably in memory
    # per spec §6 non-goals ("No streaming"). Filtering in Python (vs
    # a SQL BETWEEN join) keeps the join-key rule visible in one place.
    rows = ctx.conn.execute(
        "SELECT decision_started_at, decision_duration_ns FROM route_reasons"
    ).fetchall()
    if not rows:
        # Companion table present but empty → denominator zero (spec
        # §4.4: "data source landed, cohort just has no rows").
        return MetricResult({k: None for k in _SUB_NS3}, NOTE_DENOMINATOR_ZERO)

    durations: list[int] = []
    for rr in rows:
        try:
            ts = _parse_ts(rr["decision_started_at"])
        except ValueError:
            continue
        # Match iff ts falls inside ANY selected run's window
        # (spec §2.5 #37 "for at least one run in the selection").
        if any(start <= ts <= end for (start, end) in windows):
            durations.append(int(rr["decision_duration_ns"]))
    if not durations:
        return MetricResult({k: None for k in _SUB_NS3}, NOTE_DENOMINATOR_ZERO)

    durations.sort()
    return MetricResult({
        "p50_ns": linear_percentile([float(d) for d in durations], 0.5),
        "p95_ns": linear_percentile([float(d) for d in durations], 0.95),
        "count": len(durations),
    })


# ---------------------------------------------------------------------------
# Registry
# ---------------------------------------------------------------------------

METRICS: list[Metric] = [
    Metric(number=31, name="DriverPlanningOverhead", section="overhead",
           metric_sets=frozenset({"overhead"}),
           subkeys=_SUB_NS3, compute=_null_struct(_SUB_NS3)),
    Metric(number=32, name="TaskDispatchLatency", section="overhead",
           metric_sets=frozenset({"overhead"}),
           subkeys=_SUB_NS3, compute=_null_struct(_SUB_NS3)),
    Metric(number=33, name="TunnelOverhead", section="overhead",
           metric_sets=frozenset({"overhead"}),
           subkeys=_SUB_NS3, compute=_null_struct(_SUB_NS3)),
    Metric(number=34, name="ArtifactTransferThroughput", section="overhead",
           metric_sets=frozenset({"overhead"}),
           subkeys=_SUB_BPS3, compute=_null_struct(_SUB_BPS3)),
    Metric(number=35, name="ObserverOverhead", section="overhead",
           metric_sets=frozenset({"overhead"}),
           subkeys=_SUB_OBSERVER5, compute=_null_struct(_SUB_OBSERVER5)),
    Metric(number=36, name="ModelProxyOverhead", section="overhead",
           metric_sets=frozenset({"overhead"}),
           subkeys=_SUB_PROXY6, compute=_null_struct(_SUB_PROXY6)),
    Metric(number=37, name="RoutingLatencyP50P95", section="overhead",
           metric_sets=frozenset({"overhead"}),
           subkeys=_SUB_NS3, compute=_routing_latency_p50p95),
    Metric(number=38, name="TimeToFirstTask", section="overhead",
           metric_sets=frozenset({"overhead"}),
           subkeys=(), compute=_null_upstream_scalar),
    Metric(number=39, name="SetupFailureRate", section="overhead",
           metric_sets=frozenset({"overhead"}),
           subkeys=(), compute=_null_upstream_scalar),
]
