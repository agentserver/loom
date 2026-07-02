"""Contracted metrics (spec §2.2 — metrics #10..#16).

All 7 metrics emit `null` today. Rationale is per-metric in spec §2.2;
in summary:

  #10 ContractCompleteness      → cohort-attribution missing
                                    (per-run→contract join key not in
                                    schema; 12号 §D1 follow-up owner).
  #11 PreExecutionFaultCatchRate → upstream missing (12号 §A3).
  #12 ContractViolationRate      → upstream missing (12号 §A4).
  #13 MissingArtifactDetectionRate → upstream missing (12号 §A3).
  #14 PolicyViolationPreventionRate → upstream missing (12号 §A3).
  #15 RecoverySuccessRate         → upstream missing (12号 §A6).
  #16 DuplicateSideEffectRate     → upstream missing (12号 §A6).

Rather than expressing each metric as its own null-emitting closure,
we generate them from a table below — the metric numbers, names, and
`(_metric-set membership)` are the only things that differ.
"""

from __future__ import annotations

from eval_metrics.metrics import NOTE_UPSTREAM_MISSING, Context, Metric, MetricResult


def _null_upstream(_ctx: Context) -> MetricResult:
    """All §2.2 metrics share this compute today (upstream missing)."""
    return MetricResult(None, NOTE_UPSTREAM_MISSING)


_ROWS: tuple[tuple[int, str], ...] = (
    (10, "ContractCompleteness"),
    (11, "PreExecutionFaultCatchRate"),
    (12, "ContractViolationRate"),
    (13, "MissingArtifactDetectionRate"),
    (14, "PolicyViolationPreventionRate"),
    (15, "RecoverySuccessRate"),
    (16, "DuplicateSideEffectRate"),
)


METRICS: list[Metric] = [
    Metric(
        number=num,
        name=name,
        section="contracted",
        metric_sets=frozenset({"contracted"}),
        subkeys=(),
        compute=_null_upstream,
    )
    for num, name in _ROWS
]
