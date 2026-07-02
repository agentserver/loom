"""--runs-filter sanitizer (spec §7 (c)).

Four-step pipeline:

1. Denylist scan: reject `;`, comments (`--`, `/*`, `*/`), and any of
   the SQL keywords SELECT / WITH / UPDATE / DELETE / INSERT / DROP /
   ALTER / ATTACH / DETACH / PRAGMA / CREATE / REPLACE / TRIGGER /
   INDEX / TRANSACTION / BEGIN / COMMIT / ROLLBACK / VACUUM /
   LOAD_EXTENSION. Case-insensitive; belt against AST bypasses.
2. Parametric extraction: pull string literals out to a params list;
   replace with `?` placeholders. Numeric literals stay inline (they
   cannot inject; SQLite parses them safely).
3. AST parse with sqlglot; walk Column, Comparison / In / Like /
   Between / Is nodes; enforce (a) every Column is in the whitelist,
   (b) every predicate references at least one Column (rejects
   `1 = 1`), (c) every predicate's non-Column side is a literal (rejects
   `col1 = col2` including self-compare `experiment_id = experiment_id`).
4. Wrap and bind: return the sanitized SQL fragment + positional params
   for cursor.execute.
"""

from __future__ import annotations

import re
from typing import Any

import sqlglot
from sqlglot import expressions as exp

# Columns the operator is allowed to reference in --runs-filter. Any
# other column name → ErrRunsFilterFieldNotAllowed. This is the
# authoritative list; spec §3.1 mirrors it verbatim.
ALLOWED_COLUMNS: frozenset[str] = frozenset({
    "run_id",
    "workload_id",
    "claim_id",
    "experiment_id",
    "baseline_or_ablation",
})

# Keywords / patterns caught pre-parse (belt). Case-insensitive match
# against a whitespace-and-punctuation-delimited token stream — that
# way `runID` (imaginary column) does not trigger on "id".
_DENYLIST_TOKENS: tuple[str, ...] = (
    "SELECT", "WITH", "UPDATE", "DELETE", "INSERT", "DROP", "ALTER",
    "ATTACH", "DETACH", "PRAGMA", "CREATE", "REPLACE", "TRIGGER",
    "INDEX", "TRANSACTION", "BEGIN", "COMMIT", "ROLLBACK", "VACUUM",
    "LOAD_EXTENSION",
)

# Substring patterns rejected pre-parse (statement terminators and
# comment markers). These cannot appear inside a legitimate WHERE
# fragment — no false-positive risk.
_DENYLIST_SUBSTRINGS: tuple[str, ...] = (";", "--", "/*", "*/")


class RunsFilterError(Exception):
    """Base class for --runs-filter validation failures.

    All subclasses map to CLI exit-code 2. Subclass names are stable
    (tests match by class, not message text).
    """

    exit_code: int = 2


class ErrRunsFilterDenylist(RunsFilterError):
    """A denylisted token or substring appeared in the fragment."""


class ErrRunsFilterFieldNotAllowed(RunsFilterError):
    """A Column node referenced an identifier outside ALLOWED_COLUMNS."""


class ErrRunsFilterTautology(RunsFilterError):
    """A predicate does not reference any whitelisted column (e.g. `1 = 1`)."""


class ErrRunsFilterColumnCompare(RunsFilterError):
    """A predicate compares two columns (e.g. `col1 = col2`); rejected as tautological."""


class ErrRunsFilterParseError(RunsFilterError):
    """sqlglot could not parse the fragment."""


class ErrRunsFilterEmpty(RunsFilterError):
    """The fragment is empty / whitespace-only."""


# --- Step 1: denylist scan --------------------------------------------------

# Word-boundary regex for the token denylist; case-insensitive.
_TOKEN_RE = re.compile(
    r"\b(" + "|".join(_DENYLIST_TOKENS) + r")\b",
    re.IGNORECASE,
)


def _denylist_scan(fragment: str) -> None:
    """Raise ErrRunsFilterDenylist if any denylisted substring/token appears."""
    for sub in _DENYLIST_SUBSTRINGS:
        if sub in fragment:
            raise ErrRunsFilterDenylist(
                f"--runs-filter contains denylisted substring {sub!r}"
            )
    m = _TOKEN_RE.search(fragment)
    if m:
        raise ErrRunsFilterDenylist(
            f"--runs-filter contains denylisted keyword {m.group(1).upper()!r}"
        )


# --- Step 2: parametric extraction ------------------------------------------

# String literal in single quotes with SQLite-style escaping ('' inside
# a literal is a literal single-quote). We capture the literal contents
# without the surrounding quotes, then rebuild with ? placeholders.
_STRING_LITERAL_RE = re.compile(r"'((?:[^']|'')*)'")


def _extract_string_literals(fragment: str) -> tuple[str, list[str]]:
    """Return (fragment_with_placeholders, params_list).

    Only string literals are extracted; numeric literals stay inline
    because SQLite parses them safely and sqlglot's AST walk still
    sees them as `Literal` nodes we can inspect for tautology.
    """
    params: list[str] = []

    def _swap(match: re.Match[str]) -> str:
        body = match.group(1).replace("''", "'")  # unescape SQL-doubled quotes
        params.append(body)
        return "?"

    replaced = _STRING_LITERAL_RE.sub(_swap, fragment)
    return replaced, params


# --- Step 3: AST parse + column / predicate whitelist -----------------------

# Predicate node types we walk to enforce tautology + column-compare rules.
# These are stable across sqlglot 20..26 (each is a concrete Expression
# subclass under sqlglot.expressions). If a future major bump renames
# them, tests/test_filter.py catches it — the pyproject cap `<27`
# documents the boundary.
#
# Note: exp.Is (SQL `col IS NULL` / `col IS NOT NULL`) is intentionally
# absent. On the observer `runs` schema most whitelisted columns
# (`run_id`, `workload_id`, `claim_id`, `experiment_id`,
# `baseline_or_ablation`) are declared `NOT NULL`, which makes
# `<col> IS NOT NULL` a schema-level tautology that would select the
# full cohort while looking like a legitimate narrowing filter. Rather
# than teach the AST walker about per-column NULL-ability, we simply
# refuse the entire IS predicate — the spec §3.1 grammar only names
# `col = 'lit'`, `IN (...)`, `LIKE 'pat'`, and their AND/OR
# combinations. Codex round-2 code-review P0.
# The set below is a superset of spec §3.1's stated grammar (=, IN,
# LIKE, AND/OR). NEQ (`!=`), GT/GTE/LT/LTE, ILIKE, and BETWEEN are
# accepted as convenience because each still requires a whitelisted
# column on one side and a literal on the other (enforced by the
# tautology / column-compare walker). The reason to be strict at
# the predicate-type level is to reject IS (which we do above) —
# once IS is excluded, adding !=/BETWEEN does not open any bypass
# because the tree-shape + tautology + column-compare rules apply
# uniformly. If the paper ever needs strict grammar conformance, the
# operator can be told to use `= 'lit'` / `IN (...)` / `LIKE 'pat'`
# only; the extractor does not narrow their queries.
_PREDICATE_TYPES: tuple[type, ...] = (
    exp.EQ, exp.NEQ, exp.GT, exp.GTE, exp.LT, exp.LTE,
    exp.In, exp.Like, exp.ILike, exp.Between,
)


def _reject_non_predicate_leaves(node: exp.Expression) -> None:
    """Walk the boolean tree; every leaf must be a predicate.

    Recurses through the AND/OR/NOT/Paren combinators; when it reaches
    a non-combinator node, that node MUST be a member of
    `_PREDICATE_TYPES`. A `Boolean` literal (`TRUE`/`FALSE`), a bare
    `Literal` (a numeric or string truthy), or any other expression
    triggers `ErrRunsFilterTautology`.

    This closes the `TRUE` / `run_id = 'x' OR TRUE` / `run_id = 'x'
    OR (SELECT ...)` (parser would already reject the latter via the
    denylist, but the check is defence-in-depth) bypass that a
    predicate-only walk misses. Any future combinator sqlglot adds
    at the AST level (unlikely — AND/OR/NOT/Paren are stable) must be
    added here explicitly, so a silent broadening cannot happen.
    """
    # Peel a Paren wrapper — `(...)` is a no-op grouping.
    if isinstance(node, exp.Paren):
        _reject_non_predicate_leaves(node.this)
        return
    # NOT wraps a single sub-expression; recurse into it.
    if isinstance(node, exp.Not):
        _reject_non_predicate_leaves(node.this)
        return
    # AND / OR wrap two sub-expressions (`this` + `expression`).
    if isinstance(node, (exp.And, exp.Or)):
        _reject_non_predicate_leaves(node.this)
        _reject_non_predicate_leaves(node.expression)
        return
    # Predicate leaf — OK.
    if isinstance(node, _PREDICATE_TYPES):
        return
    # Anything else at a boolean-tree position is a bypass. The most
    # common offender is `exp.Boolean` (TRUE/FALSE literal); we also
    # catch bare `exp.Literal` (numeric truthy) and any exotic
    # expression sqlglot parsed for us.
    raise ErrRunsFilterTautology(
        f"--runs-filter contains non-predicate expression at boolean position: {node.sql()} "
        f"(type={type(node).__name__}); every leaf must be a comparison, IN, LIKE, BETWEEN, or IS"
    )


# Node types we allow as the RHS of a comparison / IN element / LIKE
# pattern / BETWEEN bound. Everything else (Function, Concat, Cast,
# arithmetic Ops, nested Columns) is rejected as a tautology bypass —
# they can evaluate to a value that always equals the LHS column
# (schema-tautology) or introduce covert Column references that the
# whitelist walker cannot spot behind a Function wrapper.
#
# `exp.Neg` (unary minus) is deliberately absent: `-workload_id` parses
# as Neg(Column('workload_id')), which slips a Column into the RHS
# past a simple isinstance check. If a numeric literal is needed, the
# user writes `-1` in the fragment — but even then, the `-` is a Neg
# wrapper around a Literal, and we route that through `_is_bare_rhs`
# below which recognises Neg-of-Literal but NOT Neg-of-Column.
_RHS_ATOMIC_TYPES: tuple[type, ...] = (
    exp.Literal,      # numeric or string literal (post extract, string → Placeholder)
    exp.Placeholder,  # `?` from parametric extraction
    exp.Null,         # explicit NULL literal (rare in WHERE fragments)
    exp.Boolean,      # TRUE/FALSE — the tree-shape walker rejects it at boolean position, but a value context is fine
)


def _is_bare_rhs(node: exp.Expression) -> bool:
    """True iff `node` is a bare literal / placeholder / negated-literal.

    Recognises `-<literal>` (Neg(Literal)) as a bare literal — the
    parser wraps `= -1` in a Neg node, and rejecting Neg outright would
    make `= -1` unusable. But `-<column>` (Neg(Column)) is a bypass —
    we recurse into the Neg's child and require it to be one of the
    _RHS_ATOMIC_TYPES too, closing the `run_id = -workload_id` /
    `run_id = -(-run_id)` bypass class Codex round-5 code-review
    P0 flagged.
    """
    if isinstance(node, (exp.Neg, exp.Paren)):
        # Neg peels a unary minus; Paren peels a `(...)` grouping.
        # Recursing into both handles `-(1)` (Neg(Paren(Literal))) as
        # well as the bare `-1` (Neg(Literal)) case.
        return _is_bare_rhs(node.this)
    return isinstance(node, _RHS_ATOMIC_TYPES)


def _enforce_predicate_shape(pred: exp.Expression) -> None:
    """LHS must be a bare Column; RHS must be a bare literal / placeholder.

    This is stricter than "at least one Column, no Column-Column
    comparison" because it also rejects `col = f(col)`, `col = col || ''`,
    `col = COALESCE(col, '')`, and any other expression that could
    schema-tautology on a NOT NULL column. Codex round-4 code-review
    P0 tripwire.

    Applied to every predicate (EQ / NEQ / GT / GTE / LT / LTE / In /
    Like / ILike / Between) — see `_ast_walk`. The tree-shape and
    tautology-count walkers above still run first; this is the
    narrowest final gate.
    """
    if isinstance(pred, (exp.EQ, exp.NEQ, exp.GT, exp.GTE, exp.LT, exp.LTE)):
        lhs, rhs = pred.this, pred.expression
        if not isinstance(lhs, exp.Column):
            raise ErrRunsFilterColumnCompare(
                f"--runs-filter predicate LHS must be a bare column: {pred.sql()!r} "
                f"(LHS type={type(lhs).__name__})"
            )
        if not _is_bare_rhs(rhs):
            raise ErrRunsFilterColumnCompare(
                f"--runs-filter predicate RHS must be a bare literal/placeholder: {pred.sql()!r} "
                f"(RHS type={type(rhs).__name__})"
            )
    elif isinstance(pred, exp.Between):
        subject = pred.this
        low, high = pred.args.get("low"), pred.args.get("high")
        if not isinstance(subject, exp.Column):
            raise ErrRunsFilterColumnCompare(
                f"--runs-filter BETWEEN subject must be a bare column: {pred.sql()!r}"
            )
        for bound in (low, high):
            if not _is_bare_rhs(bound):
                raise ErrRunsFilterColumnCompare(
                    f"--runs-filter BETWEEN bounds must be literals: {pred.sql()!r}"
                )
    elif isinstance(pred, (exp.Like, exp.ILike)):
        subject, pattern = pred.this, pred.expression
        if not isinstance(subject, exp.Column):
            raise ErrRunsFilterColumnCompare(
                f"--runs-filter LIKE subject must be a bare column: {pred.sql()!r}"
            )
        if not _is_bare_rhs(pattern):
            raise ErrRunsFilterColumnCompare(
                f"--runs-filter LIKE pattern must be a bare literal: {pred.sql()!r}"
            )
    elif isinstance(pred, exp.In):
        subject = pred.this
        if not isinstance(subject, exp.Column):
            raise ErrRunsFilterColumnCompare(
                f"--runs-filter IN subject must be a bare column: {pred.sql()!r}"
            )
        # IN's RHS is either `expressions` (a list) or `query` (a subquery
        # — already rejected by denylist SELECT/WITH). Accept only the
        # explicit-list form with all-atomic elements.
        if pred.args.get("query") is not None:
            raise ErrRunsFilterColumnCompare(
                f"--runs-filter IN with subquery not allowed: {pred.sql()!r}"
            )
        for elem in (pred.args.get("expressions") or []):
            if not _is_bare_rhs(elem):
                raise ErrRunsFilterColumnCompare(
                    f"--runs-filter IN element must be a bare literal: {elem.sql()!r}"
                )


def _ast_walk(tree: exp.Expression) -> None:
    """Enforce column-whitelist, tautology, and column-compare rules.

    - Every Column node must reference a name in ALLOWED_COLUMNS.
    - Every leaf of the AND/OR/NOT/Paren tree must be a predicate node
      (a member of `_PREDICATE_TYPES`); bare Boolean literals (`TRUE`,
      `FALSE`) or bare numeric-literal-truthiness expressions are
      rejected as tautologies. Without this, `TRUE` or `run_id = 'x'
      OR TRUE` would bypass the predicate-level checks below.
    - Every predicate node must reference AT LEAST one Column
      (rejects `1 = 1`).
    - Every predicate's non-Column side must be a Literal /
      Placeholder / list-of-literals — rejects `col1 = col2`
      including self-compare `experiment_id = experiment_id`.
    """
    # Whitelist scan first — even a single unknown Column is a hard
    # reject and gives the clearest error.
    for col in tree.find_all(exp.Column):
        name = col.name
        # sqlglot may parse a bare identifier as either exp.Column or
        # exp.Identifier depending on dialect; .name normalises both.
        if name not in ALLOWED_COLUMNS:
            raise ErrRunsFilterFieldNotAllowed(
                f"--runs-filter references field {name!r} not in whitelist {sorted(ALLOWED_COLUMNS)}"
            )

    # Tree-shape rule: the top-level tree must be a boolean tree whose
    # leaves are all predicates. Walk every node; the only permitted
    # non-predicate node types are AND / OR / NOT / Paren (logical
    # combinators) plus the predicates themselves and their internal
    # constituents (Column, Literal, Placeholder, Null, and the list
    # container used by IN). Anything else at a position where the
    # boolean tree expects a predicate — most importantly `exp.Boolean`
    # (the `TRUE` / `FALSE` literal) — is a tautology bypass and is
    # rejected. This closes the `TRUE` / `run_id = 'x' OR TRUE` bypass.
    _reject_non_predicate_leaves(tree)

    # Predicate-level rules.
    for pred in tree.find_all(*_PREDICATE_TYPES):
        cols_in_pred = list(pred.find_all(exp.Column))
        if not cols_in_pred:
            # e.g. `1 = 1` — no Column node anywhere in this predicate.
            raise ErrRunsFilterTautology(
                f"--runs-filter contains a predicate with no whitelisted column reference: {pred.sql()}"
            )

        # Strict shape enforcement: LHS must be a bare Column; RHS
        # must be a bare literal / placeholder / null (or list of
        # these for IN). No function calls, no concatenation, no
        # expressions — otherwise sqlglot-recognised operators like
        # `col = col || ''` or `col = COALESCE(col, '')` would evaluate
        # to a schema-tautology at runtime while looking like a
        # narrowing filter. Codex round-4 code-review P0 tripwire.
        _enforce_predicate_shape(pred)


# --- Step 4: full pipeline --------------------------------------------------


def compile_runs_filter(fragment: str) -> tuple[str, tuple[Any, ...]]:
    """Full pipeline. Returns (sql_fragment_with_placeholders, params_tuple).

    Caller does:
        where, params = compile_runs_filter(user_fragment)
        conn.execute(f"SELECT ... FROM runs WHERE {where}", params)

    The returned `where` is safe to interpolate because:
      - denylist rejected `;`, comments, DDL/DML keywords;
      - all string literals are `?` placeholders bound via params;
      - all Column references are in ALLOWED_COLUMNS.
    """
    if not fragment or not fragment.strip():
        raise ErrRunsFilterEmpty("--runs-filter is empty")

    _denylist_scan(fragment)
    with_placeholders, params = _extract_string_literals(fragment)

    try:
        tree = sqlglot.parse_one(with_placeholders, dialect="sqlite")
    except sqlglot.errors.ParseError as e:
        raise ErrRunsFilterParseError(f"--runs-filter parse error: {e}") from e

    if tree is None:
        raise ErrRunsFilterParseError("--runs-filter parsed to empty AST")

    _ast_walk(tree)

    # Re-emit through sqlglot to normalise whitespace / quoting. We
    # keep the sqlite dialect so identifiers stay unquoted, matching
    # the raw column names in the observer schema.
    normalised = tree.sql(dialect="sqlite")
    return normalised, tuple(params)
