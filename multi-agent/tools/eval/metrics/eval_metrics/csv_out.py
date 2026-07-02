"""CSV serializer (spec §3.3 CSV + §7 (d) formula-injection escape).

Output shape:

    Row 1 (header):  metric_set, row_count,
                     <41 metrics in §2 order, structured metrics
                      flattened to <metric>.<subkey>>,
                     _notes
    Row 2 (data):    single record per invocation (even for empty cohort)

The `_notes` companion cell is a semicolon-separated list of
`<metric>: <reason>` pairs; reason strings come from the two-value
closed set defined in `metrics.NOTE_UPSTREAM_MISSING` /
`NOTE_DENOMINATOR_ZERO`.

Formula-injection escape: any cell whose first byte is one of
`= + - @ \\t \\r \\n` is prefixed with a single quote (`'`). Matches
the WT-1-run-schema §7 (e) rule and cmd/evalrun-export main.go:283
so downstream tooling can share escape logic if desired.
"""

from __future__ import annotations

import csv
import io
from typing import Any, Iterable

from eval_metrics.metrics import Metric, MetricResult


# Cells whose first byte matches one of these are prefixed with `'`.
_INJECT_LEADERS: frozenset[str] = frozenset({"=", "+", "-", "@", "\t", "\r", "\n"})


def escape_cell(value: Any) -> str:
    """Return the CSV cell text for `value`, applying §7 (d) escape.

    - `None` → empty string (spec §7 (f) denominator-zero contract).
    - `bool` handled before `int` because `bool` is a subclass of int
      and we do not want `True`/`False` slipping through as CSV `1`/`0`.
    - Numeric types → `str(value)`. We deliberately do NOT round; §5.2
      requires full float64 precision.
    - Other → `str(value)` then formula-injection escape.
    """
    if value is None:
        return ""
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, (int, float)):
        text = repr(value) if isinstance(value, float) else str(value)
    else:
        text = str(value)
    if text and text[0] in _INJECT_LEADERS:
        return "'" + text
    return text


def build_header(metrics: list[Metric]) -> list[str]:
    """Return the CSV header row.

    Structure: `metric_set`, `row_count`, then each metric — scalars
    contribute one column `<name>`; structured metrics contribute one
    column per subkey `<name>.<subkey>` — then a trailing `_notes`
    column.

    The header order matches spec §3.3 exactly; changing it is a spec
    change, not an impl change.
    """
    header: list[str] = ["metric_set", "row_count"]
    for m in metrics:
        if m.is_structured:
            header.extend(f"{m.name}.{k}" for k in m.subkeys)
        else:
            header.append(m.name)
    header.append("_notes")
    return header


def build_data_row(
    metric_set_value: str,
    row_count: int,
    metrics: list[Metric],
    results: dict[str, MetricResult],
) -> list[str]:
    """Return the single data row for the invocation.

    `results[m.name]` is the MetricResult produced by `m.compute(ctx)`;
    scalar values land in one cell (or `None` → empty cell), structured
    values expand to one cell per subkey (a subkey mapping to `None`
    also becomes an empty cell).
    """
    row: list[str] = [escape_cell(metric_set_value), escape_cell(row_count)]
    note_parts: list[str] = []

    for m in metrics:
        res = results[m.name]
        if m.is_structured:
            # Guaranteed dict; missing subkeys would be a bug in the
            # compute closure — we assert-early rather than emit a
            # confusing empty cell of unknown provenance.
            assert isinstance(res.value, dict), f"{m.name} structured value not dict"
            for k in m.subkeys:
                row.append(escape_cell(res.value.get(k)))
        else:
            row.append(escape_cell(res.value))
        if res.note is not None:
            note_parts.append(f"{m.name}: {res.note}")

    # _notes cell — semicolon-separated. Empty cohort with 0 nulls =
    # empty string.
    row.append(escape_cell("; ".join(note_parts)))
    return row


def write_csv(
    fp: io.TextIOBase,
    metric_set_value: str,
    row_count: int,
    metrics: list[Metric],
    results: dict[str, MetricResult],
) -> None:
    """Write header + one data row to `fp`.

    The csv module handles quoting of embedded commas / quotes; we still
    do the leading-char escape on cell text before handing it to
    csv.writer because csv does NOT know about formula-injection.
    """
    w = csv.writer(fp, lineterminator="\n")
    w.writerow(build_header(metrics))
    w.writerow(build_data_row(metric_set_value, row_count, metrics, results))
