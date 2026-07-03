"""Semantic-routing metrics (spec §2.4 — metrics #28..#30).

Populated today:
  #28 RoutingAccuracy — hits over rows with non-empty `ground_truth_context`
      (the non-empty filter gates ground-truth availability; runs
      without a labeled ground truth are excluded from both numerator
      and denominator, matching spec §2.4 #28's denominator clause).

Emit null today:
  #29 CapabilityRecall    — needs capability-graph ground-truth labels
      (12号 §F4 not landed for the label side).
  #30 CapabilityPrecision — needs capability smoke tests (no observer
      schema field).

Metric-set memberships: all three are semantic-only. Cross-listed
lifecycle members #4 / #5 land in the `semantic` subset via
`metrics_for_set('semantic')` per spec §3.2, not here.
"""

from __future__ import annotations

from eval_metrics.metrics import (
    NOTE_DENOMINATOR_ZERO,
    NOTE_UPSTREAM_MISSING,
    Context,
    Metric,
    MetricResult,
)


def _routing_accuracy(ctx: Context) -> MetricResult:
    labeled = [r for r in ctx.runs if r["ground_truth_context"] != ""]
    if not labeled:
        return MetricResult(None, NOTE_DENOMINATOR_ZERO)
    hits = sum(
        1 for r in labeled
        if r["selected_context"] == r["ground_truth_context"]
    )
    return MetricResult(hits / len(labeled))


def _null_upstream(_ctx: Context) -> MetricResult:
    return MetricResult(None, NOTE_UPSTREAM_MISSING)


METRICS: list[Metric] = [
    Metric(number=28, name="RoutingAccuracy", section="semantic",
           metric_sets=frozenset({"semantic"}),
           subkeys=(), compute=_routing_accuracy),
    Metric(number=29, name="CapabilityRecall", section="semantic",
           metric_sets=frozenset({"semantic"}),
           subkeys=(), compute=_null_upstream),
    Metric(number=30, name="CapabilityPrecision", section="semantic",
           metric_sets=frozenset({"semantic"}),
           subkeys=(), compute=_null_upstream),
]
