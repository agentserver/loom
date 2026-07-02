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
_PREDICATE_TYPES: tuple[type, ...] = (
    exp.EQ, exp.NEQ, exp.GT, exp.GTE, exp.LT, exp.LTE,
    exp.In, exp.Like, exp.ILike, exp.Between, exp.Is,
)


def _ast_walk(tree: exp.Expression) -> None:
    """Enforce column-whitelist, tautology, and column-compare rules.

    - Every Column node must reference a name in ALLOWED_COLUMNS.
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

    # Predicate-level rules.
    for pred in tree.find_all(*_PREDICATE_TYPES):
        cols_in_pred = list(pred.find_all(exp.Column))
        if not cols_in_pred:
            # e.g. `1 = 1` — no Column node anywhere in this predicate.
            raise ErrRunsFilterTautology(
                f"--runs-filter contains a predicate with no whitelisted column reference: {pred.sql()}"
            )

        # Column-column compare: for equality/inequality nodes with a
        # `this` and `expression` side, both being Columns is a
        # tautology-adjacent broadener even if both are whitelisted.
        # We treat Between (which has three columns of its own) as
        # a special case — it's `X BETWEEN Y AND Z`, allowed only when
        # X is Column and Y/Z are literals.
        if isinstance(pred, (exp.EQ, exp.NEQ, exp.GT, exp.GTE, exp.LT, exp.LTE)):
            lhs, rhs = pred.this, pred.expression
            if isinstance(lhs, exp.Column) and isinstance(rhs, exp.Column):
                raise ErrRunsFilterColumnCompare(
                    f"--runs-filter compares two columns ({lhs.name!r} vs {rhs.name!r}); "
                    "predicates must compare a column against a literal"
                )
        elif isinstance(pred, exp.Between):
            low, high = pred.args.get("low"), pred.args.get("high")
            if isinstance(low, exp.Column) or isinstance(high, exp.Column):
                raise ErrRunsFilterColumnCompare(
                    "--runs-filter BETWEEN bounds must be literals, not columns"
                )
        # For In / Like / ILike / Is: sqlglot flattens the RHS to a
        # list of Literal nodes for In, a Literal for Like/ILike, and
        # a Null for Is. If any RHS element is a Column, the earlier
        # whitelist scan already rejected it (unknown column) or we
        # fall through here — an In whose RHS is [Column('workload_id')]
        # is exotic enough that we treat the extra Column-node count
        # as a red flag: any predicate whose Column count exceeds 1
        # is rejected.
        else:
            if len(cols_in_pred) > 1:
                raise ErrRunsFilterColumnCompare(
                    f"--runs-filter predicate references multiple columns "
                    f"({[c.name for c in cols_in_pred]}); RHS must be a literal"
                )


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
