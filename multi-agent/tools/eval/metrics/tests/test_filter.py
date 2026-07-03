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
    # The denylist MUST catch the leading SELECT before the whitelist
    # walker runs. Fresh-Claude PR-review round-3 P2: pin to
    # ErrRunsFilterDenylist — a future denylist relaxation would be a
    # spec change and should force this test to be updated (rather than
    # silently succeeding via the whitelist fallback).
    with pytest.raises(ErrRunsFilterDenylist):
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


def test_tautology_bare_true_reject() -> None:
    """Bypass class: TRUE alone (round-1 codex code-review P0)."""
    import pytest
    from eval_metrics.filter import ErrRunsFilterTautology
    with pytest.raises(ErrRunsFilterTautology):
        compile_runs_filter("TRUE")


def test_tautology_bare_false_reject() -> None:
    import pytest
    from eval_metrics.filter import ErrRunsFilterTautology
    with pytest.raises(ErrRunsFilterTautology):
        compile_runs_filter("FALSE")


def test_tautology_or_true_bypass_reject() -> None:
    """`run_id = 'x' OR TRUE` broadens to every row — must reject."""
    import pytest
    from eval_metrics.filter import ErrRunsFilterTautology
    with pytest.raises(ErrRunsFilterTautology):
        compile_runs_filter("run_id = 'x' OR TRUE")


def test_tautology_or_false_reject() -> None:
    """`run_id = 'x' OR FALSE` is not a broadener but still a bypass shape;
    the tree-shape rule rejects any non-predicate leaf uniformly."""
    import pytest
    from eval_metrics.filter import ErrRunsFilterTautology
    with pytest.raises(ErrRunsFilterTautology):
        compile_runs_filter("run_id = 'x' OR FALSE")


def test_tautology_not_true_reject() -> None:
    """`NOT TRUE` — same class."""
    import pytest
    from eval_metrics.filter import ErrRunsFilterTautology
    with pytest.raises(ErrRunsFilterTautology):
        compile_runs_filter("NOT TRUE")


def test_accept_not_wraps_predicate() -> None:
    """`NOT <predicate>` should still work; the fix must not over-restrict."""
    where, params = compile_runs_filter("NOT run_id = 'x'")
    assert "run_id" in where
    assert params == ("x",)


def test_is_null_predicate_reject() -> None:
    """`col IS NULL` — the IS predicate is entirely disallowed by grammar."""
    import pytest
    from eval_metrics.filter import ErrRunsFilterTautology
    with pytest.raises(ErrRunsFilterTautology):
        compile_runs_filter("run_id IS NULL")


def test_is_not_null_predicate_reject() -> None:
    """`col IS NOT NULL` — schema-tautology on NOT NULL columns.

    Codex round-2 code-review P0: `workload_id IS NOT NULL` would
    silently select the full cohort because the schema declares that
    column NOT NULL. We reject the IS predicate wholesale.
    """
    import pytest
    from eval_metrics.filter import ErrRunsFilterTautology
    with pytest.raises(ErrRunsFilterTautology):
        compile_runs_filter("workload_id IS NOT NULL")


# --- Codex round-4 P0 regressions: RHS expression bypass class -------------


def test_expr_rhs_concat_reject() -> None:
    """`run_id = run_id || ''` — column-concat expression tautology."""
    import pytest
    from eval_metrics.filter import ErrRunsFilterColumnCompare
    with pytest.raises(ErrRunsFilterColumnCompare):
        compile_runs_filter("run_id = run_id || ''")


def test_expr_rhs_coalesce_reject() -> None:
    """`run_id = COALESCE(run_id, '')` — function wrapping the same column."""
    import pytest
    from eval_metrics.filter import ErrRunsFilterColumnCompare
    with pytest.raises(ErrRunsFilterColumnCompare):
        compile_runs_filter("run_id = COALESCE(run_id, '')")


def test_expr_rhs_lower_col_reject() -> None:
    """`run_id = LOWER(run_id)` — same-column function; schema-tautology on lowercase runs."""
    import pytest
    from eval_metrics.filter import ErrRunsFilterColumnCompare
    with pytest.raises(ErrRunsFilterColumnCompare):
        compile_runs_filter("run_id = LOWER(run_id)")


def test_expr_rhs_lower_literal_reject() -> None:
    """`run_id = LOWER('X')` — even function-over-literal fails the bare-literal rule."""
    import pytest
    from eval_metrics.filter import ErrRunsFilterColumnCompare
    with pytest.raises(ErrRunsFilterColumnCompare):
        compile_runs_filter("run_id = LOWER('X')")


def test_in_subquery_reject() -> None:
    """`experiment_id IN (SELECT ...)` — subquery caught by denylist + shape."""
    import pytest
    from eval_metrics.filter import RunsFilterError
    with pytest.raises(RunsFilterError):
        compile_runs_filter("experiment_id IN (SELECT experiment_id FROM runs)")


def test_between_col_bound_reject() -> None:
    """`run_id BETWEEN 'a' AND workload_id` — one bound is a Column."""
    import pytest
    from eval_metrics.filter import ErrRunsFilterColumnCompare
    with pytest.raises(ErrRunsFilterColumnCompare):
        compile_runs_filter("run_id BETWEEN 'a' AND workload_id")


def test_expr_lhs_function_reject() -> None:
    """`LOWER(run_id) = 'x'` — LHS must be a bare Column."""
    import pytest
    from eval_metrics.filter import ErrRunsFilterColumnCompare
    with pytest.raises(ErrRunsFilterColumnCompare):
        compile_runs_filter("LOWER(run_id) = 'x'")


def test_neg_column_reject() -> None:
    """`run_id = -workload_id` — Neg wraps a Column; reject."""
    import pytest
    from eval_metrics.filter import ErrRunsFilterColumnCompare
    with pytest.raises(ErrRunsFilterColumnCompare):
        compile_runs_filter("run_id = -workload_id")


def test_neg_neg_column_reject() -> None:
    """`run_id = -(-run_id)` — nested Neg still hides a Column."""
    import pytest
    from eval_metrics.filter import ErrRunsFilterColumnCompare
    with pytest.raises(ErrRunsFilterColumnCompare):
        compile_runs_filter("run_id = -(-run_id)")


def test_neg_paren_column_reject() -> None:
    """`run_id = -(workload_id)` — Paren wrapper does not launder a Column."""
    import pytest
    from eval_metrics.filter import ErrRunsFilterColumnCompare
    with pytest.raises(ErrRunsFilterColumnCompare):
        compile_runs_filter("run_id = -(workload_id)")


def test_neg_literal_accept() -> None:
    """`run_id = -1` and `run_id = -(1)` — Neg-of-literal is fine."""
    where, params = compile_runs_filter("run_id = -1")
    assert "run_id" in where
    assert params == ()
    where, params = compile_runs_filter("run_id = -(1)")
    assert "run_id" in where
    assert params == ()


# --- Round-2 fresh-Claude PR-review regressions ----------------------------


def test_bare_qmark_placeholder_reject() -> None:
    """`run_id = ?` — user-supplied placeholder is a spec §3.1 violation."""
    import pytest
    from eval_metrics.filter import ErrRunsFilterDenylist
    with pytest.raises(ErrRunsFilterDenylist):
        compile_runs_filter("run_id = ?")


def test_named_bind_colon_reject() -> None:
    """`run_id IN (:foo)` — named-parameter marker forbidden too."""
    import pytest
    from eval_metrics.filter import ErrRunsFilterDenylist
    with pytest.raises(ErrRunsFilterDenylist):
        compile_runs_filter("run_id IN (:foo)")


def test_named_bind_at_reject() -> None:
    """`run_id = @x` — SQLite `@name` marker forbidden."""
    import pytest
    from eval_metrics.filter import ErrRunsFilterDenylist
    with pytest.raises(ErrRunsFilterDenylist):
        compile_runs_filter("run_id = @x")


def test_qmark_inside_literal_accepted() -> None:
    """`run_id = 'sk?y'` — `?` inside a quoted literal is fine (belt: unquote first)."""
    where, params = compile_runs_filter("run_id = 'sk?y'")
    assert params == ("sk?y",)


def test_recursion_deep_parens_rejected_cleanly() -> None:
    """5000-nested parens must exit as ErrRunsFilterParseError, not RecursionError."""
    import pytest
    from eval_metrics.filter import ErrRunsFilterParseError
    deep = "(" * 5000 + "run_id = 'x'" + ")" * 5000
    with pytest.raises(ErrRunsFilterParseError):
        compile_runs_filter(deep)


def test_deep_not_tree_rejected() -> None:
    """A NOT-chain deeper than _MAX_TREE_DEPTH must reject cleanly (spec §3 exit 2)."""
    import pytest
    from eval_metrics.filter import ErrRunsFilterParseError
    # 40 NOTs > _MAX_TREE_DEPTH (32).
    frag = "NOT " * 40 + "run_id = 'x'"
    with pytest.raises(ErrRunsFilterParseError):
        compile_runs_filter(frag)


def test_empty_in_list_reject() -> None:
    """`run_id IN ()` — empty IN list rejected explicitly."""
    import pytest
    from eval_metrics.filter import ErrRunsFilterColumnCompare
    with pytest.raises(ErrRunsFilterColumnCompare):
        compile_runs_filter("run_id IN ()")


# --- Round-3 fresh-Claude PR-review regressions ----------------------------


def test_boolean_literal_rhs_reject() -> None:
    """`run_id = TRUE` — Boolean RHS is a spec §3.1 grammar violation."""
    import pytest
    from eval_metrics.filter import ErrRunsFilterColumnCompare
    with pytest.raises(ErrRunsFilterColumnCompare):
        compile_runs_filter("run_id = TRUE")


def test_boolean_false_literal_rhs_reject() -> None:
    import pytest
    from eval_metrics.filter import ErrRunsFilterColumnCompare
    with pytest.raises(ErrRunsFilterColumnCompare):
        compile_runs_filter("run_id = FALSE")


def test_null_literal_rhs_reject() -> None:
    """`run_id = NULL` — NULL RHS is a spec §3.1 grammar violation.

    Note: SQLite semantics say `col = NULL` is always NULL (never
    matches), so this filter was silently a "select nothing" clause
    if accepted. Rejecting it forces operators to use `IS NULL` —
    which itself is banned by the IS-predicate rule (round-2 P0), so
    NULL-checking filters are entirely out of scope for --runs-filter.
    """
    import pytest
    from eval_metrics.filter import ErrRunsFilterColumnCompare
    with pytest.raises(ErrRunsFilterColumnCompare):
        compile_runs_filter("run_id = NULL")


def test_null_in_in_list_reject() -> None:
    """`run_id IN (NULL)` — NULL element in IN list rejected."""
    import pytest
    from eval_metrics.filter import ErrRunsFilterColumnCompare
    with pytest.raises(ErrRunsFilterColumnCompare):
        compile_runs_filter("run_id IN (NULL)")


def test_qualified_column_reject() -> None:
    """`runs.run_id = 'x'` — table-qualified name rejected.

    The FROM list is hardcoded to `runs`; any qualifier is either
    redundant (`runs.run_id`) or references a table not in FROM
    (`route_reasons.run_id`, which would then fail at SQLite
    execute-time with `no such column`). Fresh-Claude PR-review
    round-3 P2 tripwire.
    """
    import pytest
    from eval_metrics.filter import ErrRunsFilterFieldNotAllowed
    with pytest.raises(ErrRunsFilterFieldNotAllowed):
        compile_runs_filter("runs.run_id = 'x'")


def test_cross_table_qualified_column_reject() -> None:
    """`route_reasons.run_id = 'x'` — non-`runs` qualifier rejected."""
    import pytest
    from eval_metrics.filter import ErrRunsFilterFieldNotAllowed
    with pytest.raises(ErrRunsFilterFieldNotAllowed):
        compile_runs_filter("route_reasons.run_id = 'x'")


def test_db_qualified_column_reject() -> None:
    """`main.runs.run_id = 'x'` — database-qualified reference rejected."""
    import pytest
    from eval_metrics.filter import ErrRunsFilterFieldNotAllowed
    with pytest.raises(ErrRunsFilterFieldNotAllowed):
        compile_runs_filter("main.runs.run_id = 'x'")
