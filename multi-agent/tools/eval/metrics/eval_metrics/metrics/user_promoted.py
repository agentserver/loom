"""User-promoted metrics (spec §2.3 — metrics #17..#27, #40, #41).

All 13 metrics emit `null` today. Data source is 12号 §B1/B2/B4/B6
(WT-2-driver-promotion-chain worktree) — not yet landed. #40
`HumanEditCount` requires a new `runs.human_edit_count` column; #41
`TokenUsage` requires `runs.model_input_tokens` + `runs.model_output_tokens`.
When those columns arrive the extractor probes them via
`db.runs_column_present` and flips from null to the sum — the current
compute closures handle that transition already.

Metric-set memberships:
  #22 CapabilityReuseRate     → user-promoted + lifecycle
  #23 RepeatedGenerationRate  → user-promoted + lifecycle
  #40 HumanEditCount          → user-promoted only
  #41 TokenUsage              → user-promoted only

The rest (#17..#21, #24..#27) are user-promoted only.
"""

from __future__ import annotations

from eval_metrics.db import runs_column_present
from eval_metrics.metrics import NOTE_UPSTREAM_MISSING, Context, Metric, MetricResult


def _null_upstream(_ctx: Context) -> MetricResult:
    return MetricResult(None, NOTE_UPSTREAM_MISSING)


# ---------------------------------------------------------------------------
# #20  TimeFromUserDecisionToRegisteredMCP — structured 4-key
# ---------------------------------------------------------------------------

_T20_SUBKEYS = ("p50_seconds", "p95_seconds", "mean_seconds", "count")


def _t20_null(_ctx: Context) -> MetricResult:
    return MetricResult({k: None for k in _T20_SUBKEYS}, NOTE_UPSTREAM_MISSING)


# ---------------------------------------------------------------------------
# #40  HumanEditCount — column-conditional sum (mirrors §2.1 #7 pattern)
# ---------------------------------------------------------------------------

def _human_edit_count(ctx: Context) -> MetricResult:
    if not runs_column_present(ctx.conn, "human_edit_count"):
        return MetricResult(None, NOTE_UPSTREAM_MISSING)
    run_ids = [r["run_id"] for r in ctx.runs]
    if not run_ids:
        # Column landed but cohort empty → real 0 per spec §3.3
        # empty-DB count-with-landed-upstream rule.
        return MetricResult(0)
    placeholders = ",".join("?" for _ in run_ids)
    row = ctx.conn.execute(
        f"SELECT COALESCE(SUM(human_edit_count), 0) FROM runs WHERE run_id IN ({placeholders})",
        run_ids,
    ).fetchone()
    return MetricResult(int(row[0]))


# ---------------------------------------------------------------------------
# #41  TokenUsage — structured 3-key {input_tokens, output_tokens, count}
# ---------------------------------------------------------------------------

_T41_SUBKEYS = ("input_tokens", "output_tokens", "count")


def _token_usage(ctx: Context) -> MetricResult:
    has_in = runs_column_present(ctx.conn, "model_input_tokens")
    has_out = runs_column_present(ctx.conn, "model_output_tokens")
    if not (has_in and has_out):
        return MetricResult(
            {k: None for k in _T41_SUBKEYS}, NOTE_UPSTREAM_MISSING
        )
    run_ids = [r["run_id"] for r in ctx.runs]
    if not run_ids:
        return MetricResult({"input_tokens": 0, "output_tokens": 0, "count": 0})
    placeholders = ",".join("?" for _ in run_ids)
    row = ctx.conn.execute(
        "SELECT COALESCE(SUM(model_input_tokens), 0), "
        "COALESCE(SUM(model_output_tokens), 0) "
        f"FROM runs WHERE run_id IN ({placeholders})",
        run_ids,
    ).fetchone()
    return MetricResult({
        "input_tokens": int(row[0]),
        "output_tokens": int(row[1]),
        "count": len(run_ids),
    })


# ---------------------------------------------------------------------------
# Registry — split into #17..#27 (before §2.4/§2.5) and #40/#41 (after §2.5).
# `__init__.py` splices METRICS_40_41 after the overhead section to
# preserve §2 catalog order.
# ---------------------------------------------------------------------------

# Metric-set membership for #22 / #23 (cross-listed into lifecycle
# per §2.1 membership table). All other #17..#27 rows are
# user-promoted only.
_CROSS_LIFECYCLE: frozenset[str] = frozenset({"user-promoted", "lifecycle"})
_UP_ONLY: frozenset[str] = frozenset({"user-promoted"})


METRICS_17_27: list[Metric] = [
    Metric(number=17, name="PromotionCandidateSurfacingRate",
           section="user-promoted", metric_sets=_UP_ONLY,
           subkeys=(), compute=_null_upstream),
    Metric(number=18, name="UserInitiatedSynthesisSuccessRate",
           section="user-promoted", metric_sets=_UP_ONLY,
           subkeys=(), compute=_null_upstream),
    Metric(number=19, name="ValidationFalseAcceptRate",
           section="user-promoted", metric_sets=_UP_ONLY,
           subkeys=(), compute=_null_upstream),
    Metric(number=20, name="TimeFromUserDecisionToRegisteredMCP",
           section="user-promoted", metric_sets=_UP_ONLY,
           subkeys=_T20_SUBKEYS, compute=_t20_null),
    Metric(number=21, name="RegistryLookupHitRate",
           section="user-promoted", metric_sets=_UP_ONLY,
           subkeys=(), compute=_null_upstream),
    Metric(number=22, name="CapabilityReuseRate",
           section="user-promoted", metric_sets=_CROSS_LIFECYCLE,
           subkeys=(), compute=_null_upstream),
    Metric(number=23, name="RepeatedGenerationRate",
           section="user-promoted", metric_sets=_CROSS_LIFECYCLE,
           subkeys=(), compute=_null_upstream),
    Metric(number=24, name="PromotionAdoptionRate",
           section="user-promoted", metric_sets=_UP_ONLY,
           subkeys=(), compute=_null_upstream),
    Metric(number=25, name="AdHocScriptTaskShare",
           section="user-promoted", metric_sets=_UP_ONLY,
           subkeys=(), compute=_null_upstream),
    Metric(number=26, name="GeneratedCapabilityDefectRate",
           section="user-promoted", metric_sets=_UP_ONLY,
           subkeys=(), compute=_null_upstream),
    Metric(number=27, name="ReuseSpeedup",
           section="user-promoted", metric_sets=_UP_ONLY,
           subkeys=(), compute=_null_upstream),
]

METRICS_40_41: list[Metric] = [
    Metric(number=40, name="HumanEditCount",
           section="user-promoted", metric_sets=_UP_ONLY,
           subkeys=(), compute=_human_edit_count),
    Metric(number=41, name="TokenUsage",
           section="user-promoted", metric_sets=_UP_ONLY,
           subkeys=_T41_SUBKEYS, compute=_token_usage),
]
