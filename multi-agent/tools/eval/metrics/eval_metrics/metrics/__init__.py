"""Metric registry (spec §2 catalog, §3.2 subset table).

Every metric in spec §2 (41 total) is defined by a `Metric` dataclass
carrying:

- `number` — the 1-based position in the spec §2 catalog (1..41).
  Kept even though `ALL_METRICS` is already ordered by it: fixture
  golden JSONs and test names cite the number, so exposing it as an
  attribute avoids a second source of truth.
- `name` — the paper-facing name (e.g. `TaskSuccessRate`).
- `section` — canonical §2 subsection this metric lives in
  (`lifecycle` | `contracted` | `user-promoted` | `semantic` |
  `overhead`).
- `metric_sets` — every `--metric-set` value that includes this metric,
  per the spec §2.1 membership table + §3.2 subset table. A metric's
  own `section` is always in this set; cross-listed metrics also carry
  the other section(s).
- `subkeys` — empty tuple for scalar metrics; a tuple of sub-column
  names for structured metrics (spec §3.3 flatten list). Structured
  metrics ALWAYS emit their full subkey set, filled with `None` when
  the metric is null (see §2.5 "empty sub-cells are still emitted").
- `compute` — a callable `Context -> MetricResult`; see
  `MetricResult` docstring.

The subset-selection helper `metrics_for_set(name)` returns the
`ALL_METRICS`-order subset for a given `--metric-set` value, so CSV
column ordering and JSON key ordering stay stable (spec §3.3).
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Callable, Optional


# Two-value closed set of `_notes` / `notes` reasons per spec §3.3.
NOTE_UPSTREAM_MISSING = "upstream data missing"
NOTE_DENOMINATOR_ZERO = "denominator zero"


@dataclass(frozen=True)
class Context:
    """Everything a metric function needs to compute its value.

    - `conn` — read-only sqlite3 connection (opened via `db.py`).
    - `runs` — the pre-selected list of `runs` rows (already filtered
      through `--runs-filter`; may be empty for an empty cohort).
    - `row_count` — `len(runs)`, provided verbatim so metrics do not
      have to recompute.
    """

    conn: Any  # sqlite3.Connection; kept as Any to avoid runtime import here
    runs: list  # list[sqlite3.Row]
    row_count: int


@dataclass(frozen=True)
class MetricResult:
    """One metric's output.

    - `value`:
      - For scalar metrics: a Python number, or `None` when the metric
        is null (upstream missing OR denominator zero).
      - For structured metrics: a dict[str, number|None] whose keys are
        exactly `metric.subkeys`. When the metric is null, EVERY subkey
        maps to `None` (spec §2.5 empty sub-cell rule).
    - `note`: `None` when the value is present; otherwise one of the
      two closed-set strings `NOTE_UPSTREAM_MISSING` /
      `NOTE_DENOMINATOR_ZERO` (spec §3.3, §7 (f), §7 (g)).
    """

    value: Any
    note: Optional[str] = None


@dataclass(frozen=True)
class Metric:
    """Static description of one paper metric."""

    number: int
    name: str
    section: str
    metric_sets: frozenset[str]
    subkeys: tuple[str, ...]
    compute: Callable[[Context], MetricResult]

    @property
    def is_structured(self) -> bool:
        return bool(self.subkeys)

    def null_value(self) -> Any:
        """Return the shape-appropriate null value.

        Scalar → `None`. Structured → `{k: None for k in subkeys}`.
        Kept here so metric-compute functions can return
        `MetricResult(metric.null_value(), reason)` without knowing
        their own subkey layout.
        """
        if self.is_structured:
            return {k: None for k in self.subkeys}
        return None


# Lazy import of the per-section modules to keep this file free of
# circular-import hazards; the modules only import the dataclasses
# above (which do not touch anything else here).
from eval_metrics.metrics import (  # noqa: E402 -- see comment above
    contracted,
    lifecycle,
    overhead,
    semantic,
    user_promoted,
)


# §2 catalog order — the ONE authoritative sequence. CSV / JSON emit
# headers in this order, and the subset filter preserves it.
ALL_METRICS: list[Metric] = [
    # §2.1 Lifecycle (#1..#9)
    *lifecycle.METRICS,
    # §2.2 Contracted (#10..#16)
    *contracted.METRICS,
    # §2.3 User-promoted (#17..#27, #40, #41) — the #40/#41 rows land
    # in the same module because they belong to `user-promoted` per
    # spec §2.3 header.
    *user_promoted.METRICS_17_27,
    # §2.4 Semantic (#28..#30)
    *semantic.METRICS,
    # §2.5 Overhead (#31..#39)
    *overhead.METRICS,
    # §2.3 tail (#40, #41) — spec §2 order is 1..39 then 40, 41 tacked
    # on the user-promoted section, so they land AFTER §2.5.
    *user_promoted.METRICS_40_41,
]


def _self_check() -> None:
    """Belt: catalog must have 41 rows numbered 1..39 + 40, 41 in order."""
    assert len(ALL_METRICS) == 41, f"catalog size drift: {len(ALL_METRICS)}"
    expected = list(range(1, 40)) + [40, 41]
    actual = [m.number for m in ALL_METRICS]
    assert actual == expected, f"catalog order drift: {actual}"
    names = [m.name for m in ALL_METRICS]
    assert len(set(names)) == 41, f"duplicate metric names: {names}"


_self_check()


# `--metric-set` -> ordered list of metrics per spec §3.2 subset table.
# Keys match the `--metric-set` enum values verbatim; unknown values
# are rejected upstream in cli.py so this dict is safe to index.
_METRIC_SETS: tuple[str, ...] = (
    "full",
    "lifecycle",
    "contracted",
    "user-promoted",
    "semantic",
    "overhead",
)


def metrics_for_set(name: str) -> list[Metric]:
    """Return the ordered metric list for a `--metric-set` value.

    Order is always §2 catalog order (ALL_METRICS order). The subset
    filter picks metrics whose `metric_sets` includes `name`; `full`
    returns every metric.

    Cross-listed metrics land in every subset they belong to per the
    spec §2.1 membership table and §3.2 subset table.
    """
    if name == "full":
        return list(ALL_METRICS)
    if name not in _METRIC_SETS:
        raise ValueError(f"unknown metric set: {name}")
    return [m for m in ALL_METRICS if name in m.metric_sets]


def valid_metric_sets() -> tuple[str, ...]:
    """Return the tuple of accepted `--metric-set` values (for CLI enum)."""
    return _METRIC_SETS
