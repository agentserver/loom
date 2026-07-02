"""Spec §7 (c) --runs-filter sanitizer."""

from __future__ import annotations

import pytest

from eval_metrics.filter import (
    ErrRunsFilterColumnCompare,
    ErrRunsFilterDenylist,
    ErrRunsFilterFieldNotAllowed,
    ErrRunsFilterTautology,
    compile_runs_filter,
)


# --- Denylist ---------------------------------------------------------------

def test_denylist_semicolon_reject() -> None:
    with pytest.raises(ErrRunsFilterDenylist):
        compile_runs_filter("run_id='x'; DROP TABLE runs")


def test_denylist_update_reject() -> None:
    with pytest.raises(ErrRunsFilterDenylist):
        compile_runs_filter("1=1; UPDATE runs SET success_oracle_result='pass'")


def test_denylist_select_reject() -> None:
    with pytest.raises(ErrRunsFilterDenylist):
        compile_runs_filter("run_id='x' OR EXISTS (SELECT 1 FROM sqlite_master)")


def test_denylist_comment_dashdash_reject() -> None:
    with pytest.raises(ErrRunsFilterDenylist):
        compile_runs_filter("run_id = 'x' -- ignore")


def test_denylist_comment_slashstar_reject() -> None:
    with pytest.raises(ErrRunsFilterDenylist):
        compile_runs_filter("run_id = 'x' /* comment */")


# --- Whitelist -------------------------------------------------------------

def test_field_whitelist_secret_col_reject() -> None:
    with pytest.raises(ErrRunsFilterFieldNotAllowed):
        compile_runs_filter("secret_col = 'x'")


def test_field_whitelist_sqlite_master_reject() -> None:
    # Denylist catches the leading SELECT first — either exception is
    # correct here (both map to exit 2); we accept both classes so a
    # future denylist relaxation does not break this test.
    with pytest.raises((ErrRunsFilterDenylist, ErrRunsFilterFieldNotAllowed)):
        compile_runs_filter("run_id IN (SELECT name FROM sqlite_master)")


# --- Tautology / column-compare -------------------------------------------

def test_tautology_1_eq_1_reject() -> None:
    with pytest.raises(ErrRunsFilterTautology):
        compile_runs_filter("run_id = 'x' OR 1 = 1")


def test_tautology_bare_reject() -> None:
    with pytest.raises(ErrRunsFilterTautology):
        compile_runs_filter("1 = 1")


def test_tautology_col_col_compare_reject() -> None:
    with pytest.raises(ErrRunsFilterColumnCompare):
        compile_runs_filter("run_id = workload_id")


def test_tautology_col_col_self_reject() -> None:
    with pytest.raises(ErrRunsFilterColumnCompare):
        compile_runs_filter("experiment_id = experiment_id")


# --- Parametric extraction -------------------------------------------------

def test_parametric_binding_used() -> None:
    """User string literal must land in params, not concatenated into SQL."""
    where, params = compile_runs_filter("run_id = 'run-abc'")
    assert "run-abc" not in where
    assert params == ("run-abc",)


# --- Accepted grammar ------------------------------------------------------

def test_accept_equality_literal() -> None:
    where, params = compile_runs_filter("run_id = 'run-abc'")
    assert "run_id" in where
    assert params == ("run-abc",)


def test_accept_in_literal_list() -> None:
    where, params = compile_runs_filter("experiment_id IN ('E1','E3')")
    assert "experiment_id" in where
    assert set(params) == {"E1", "E3"}


def test_accept_like_pattern() -> None:
    where, params = compile_runs_filter("workload_id LIKE 'code-mod-%'")
    assert "workload_id" in where
    assert params == ("code-mod-%",)


def test_accept_and_or_combination() -> None:
    where, params = compile_runs_filter(
        "baseline_or_ablation = 'FullLoom' AND workload_id LIKE 'code-mod-%'"
    )
    assert "baseline_or_ablation" in where
    assert "workload_id" in where
    assert set(params) == {"FullLoom", "code-mod-%"}
