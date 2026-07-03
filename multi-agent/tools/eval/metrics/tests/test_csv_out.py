"""Spec §7 (d) CSV formula-injection escape + §3.3 header stability."""

from __future__ import annotations

import io
from pathlib import Path

from eval_metrics import csv_out
from eval_metrics.metrics import Metric, MetricResult, metrics_for_set
from tests._extract_helpers import extract_csv_rows


def test_csv_formula_injection_escape_eq() -> None:
    assert csv_out.escape_cell("=SUM(A1:A9)") == "'=SUM(A1:A9)"


def test_csv_formula_injection_escape_plus() -> None:
    assert csv_out.escape_cell("+cmd") == "'+cmd"


def test_csv_formula_injection_escape_minus() -> None:
    assert csv_out.escape_cell("-cmd") == "'-cmd"


def test_csv_formula_injection_escape_at() -> None:
    assert csv_out.escape_cell("@import") == "'@import"


def test_csv_formula_injection_escape_tab() -> None:
    assert csv_out.escape_cell("\tinjected") == "'\tinjected"


def test_csv_formula_injection_escape_cr() -> None:
    assert csv_out.escape_cell("\rinjected") == "'\rinjected"


def test_csv_formula_injection_escape_newline() -> None:
    assert csv_out.escape_cell("\ninjected") == "'\ninjected"


def test_csv_no_escape_when_safe() -> None:
    assert csv_out.escape_cell("run-abc") == "run-abc"
    assert csv_out.escape_cell(0.6) == "0.6"
    assert csv_out.escape_cell(0) == "0"
    assert csv_out.escape_cell(None) == ""


def test_csv_header_order_stable(fixture_1_db: Path) -> None:
    h1, _ = extract_csv_rows(fixture_1_db, "full")
    h2, _ = extract_csv_rows(fixture_1_db, "full")
    assert h1 == h2
    # First columns fixed.
    assert h1[0] == "metric_set"
    assert h1[1] == "row_count"
    assert h1[-1] == "_notes"
    # 41 metrics; structured metrics contribute additional columns
    # (spec §2.5 flatten arithmetic: 3+3+3+3+5+6+3 = 26 for #31..#37,
    # plus TimeToCompletion (4), TimeFromUserDecisionToRegisteredMCP (4),
    # TokenUsage (3) = 41 structured cells for 10 structured metrics
    # of avg 4.1 cells each). Total header = 2 + <metric-columns> + 1.
    # 31 scalars + 10 structured contributing 4+4+3+3+3+3+3+5+6+3 = 37
    # cells → 31 + 37 = 68 metric columns → total header = 2 + 68 + 1 = 71.
    assert len(h1) == 71


def test_csv_notes_column_semicolon_separated(fixture_1_db: Path) -> None:
    """Multiple null metrics → `<a>: <r1>; <b>: <r2>` shape."""
    _, data = extract_csv_rows(fixture_1_db, "lifecycle")
    notes = data[-1]
    assert "ManualSetupStepCount: upstream data missing" in notes
    assert "; " in notes  # separator between metric-notes pairs
