#!/usr/bin/env bash
# _bats_shim.sh — micro-runner for .bats files when bats-core is not on
# PATH. Parses `@test "name" { ... }` blocks with a simple state machine
# and executes each in a subshell that mimics bats' setup/teardown/run
# contract closely enough for the assertions this worktree uses.
#
# NOT a bats replacement. Intended so `bash _bats_shim.sh <file>` gives
# the same pass/fail signal as `bats <file>` for our test suite when
# operators don't have bats installed. CI SHOULD run real bats.
#
# Assertions supported:
#   - `run <cmd>` → captures $status and $output (single string,
#     newlines preserved) exactly like bats does.
#   - `[ ... ]` / `[[ ... ]]` / `!` — plain shell, red on non-zero exit.
#   - `skip "reason"` — sets a per-test skip flag; test reports SKIP.
#   - `setup` / `teardown` — top-level functions if defined.
#
# Not supported: `bats-assert`, `bats-support`, load(), and file-scope
# `@test` names that contain literal newlines.
#
# Parser constraints (P2-3 fresh-review round 8) — these differ from
# real bats:
#   - Opening `{` MUST be on the same line as the `@test "name" {`
#     header (real bats also requires this, so no divergence).
#   - Closing `}` MUST be on its own line (bats accepts a `}` at end
#     of an inline block). If a test body needs a literal `}` on its
#     own line (e.g. a heredoc containing `\n}\n`), the shim will
#     truncate at that point — split the string or use printf.
#   - Real bats is authoritative; this shim exists so `bash
#     _bats_shim.sh file.bats` gives a pass/fail signal on hosts
#     without bats-core installed. Any divergence between shim and
#     real bats is a shim bug — file it as such.

set -euo pipefail

if (( $# != 1 )); then
    echo "usage: $0 <file.bats>" >&2
    exit 2
fi

BATS_FILE="$1"
BATS_TEST_DIRNAME="$(cd "$(dirname "$BATS_FILE")" && pwd)"
export BATS_TEST_DIRNAME

# --- parse @test blocks into (name, body) pairs ------------------------

names=()
bodies=()
prelude=""
in_test=0
cur_name=""
cur_body=""
brace_depth=0

while IFS= read -r line || [[ -n "$line" ]]; do
    if (( in_test == 0 )); then
        if [[ "$line" =~ ^@test[[:space:]]+\"(.*)\"[[:space:]]+\{[[:space:]]*$ ]]; then
            in_test=1
            cur_name="${BASH_REMATCH[1]}"
            cur_body=""
            continue
        fi
        prelude+="$line"$'\n'
    else
        # End-of-test = a standalone `}` on its own line (optionally
        # indented / trailing whitespace). This matches how bats itself
        # requires @test blocks to close (bats' own parser is line-based
        # too; a `}` mid-line is body content).
        if [[ "$line" =~ ^[[:space:]]*\}[[:space:]]*$ ]]; then
            names+=("$cur_name")
            bodies+=("$cur_body")
            in_test=0
        else
            cur_body+="$line"$'\n'
        fi
    fi
done < "$BATS_FILE"

# --- run each test body in a subshell ---------------------------------

total="${#names[@]}"
pass=0
fail=0
skip=0

# The outer script uses `set -e` while parsing; runtime we tolerate
# per-test failures (a failing test must not abort the whole suite).
set +e

for i in "${!names[@]}"; do
    name="${names[$i]}"
    body="${bodies[$i]}"

    result=$(
        set +e
        (
            # Evaluate prelude so setup/teardown/other funcs are defined.
            eval "$prelude"

            BATS_TEST_SKIPPED=""
            skip() { BATS_TEST_SKIPPED="${1:-skipped}"; return 0; }

            # `run` — capture $status and $output. Per bats contract,
            # `run` ALWAYS returns 0 (so `set -e` in the test body does
            # not abort on a captured-nonzero command; callers must
            # assert on $status explicitly).
            run() {
                local __out __rc
                { __out=$("$@" 2>&1); __rc=$?; } || __rc=$?
                status=$__rc
                output=$__out
                return 0
            }
            status=0
            output=""

            declare -F setup >/dev/null && setup

            # bats' contract: a test body runs under `set -e` such that
            # any failing command aborts the test. Our `run` wrapper
            # sets `status`/`output` and MUST always exit 0 so callers
            # can then assert on `$status`. `set -e` in the body then
            # trips on the first genuinely failing assertion.
            set -e
            eval "$body"
            body_rc=$?
            set +e

            declare -F teardown >/dev/null && teardown

            if [[ -n "$BATS_TEST_SKIPPED" ]]; then
                echo "__RESULT__=skip:$BATS_TEST_SKIPPED"
            elif (( body_rc == 0 )); then
                echo "__RESULT__=pass"
            else
                echo "__RESULT__=fail:rc=$body_rc"
            fi
        ) 2>&1
    )

    verdict=$(printf '%s\n' "$result" | grep '^__RESULT__=' | tail -1 | cut -d= -f2-)
    diag=$(printf '%s\n' "$result" | grep -v '^__RESULT__=' || true)

    case "$verdict" in
        pass)
            printf 'ok %d - %s\n' $((i+1)) "$name"
            pass=$((pass+1))
            ;;
        skip:*)
            printf 'ok %d - %s # skip %s\n' $((i+1)) "$name" "${verdict#skip:}"
            skip=$((skip+1))
            ;;
        fail:*)
            printf 'not ok %d - %s\n' $((i+1)) "$name"
            if [[ -n "$diag" ]]; then
                printf '# %s\n' "$diag" | sed 's/^/  /'
            fi
            fail=$((fail+1))
            ;;
        *)
            printf 'not ok %d - %s (harness error)\n' $((i+1)) "$name"
            printf '%s\n' "$result" | sed 's/^/  # /'
            fail=$((fail+1))
            ;;
    esac
done

printf '\n1..%d\n' "$total"
printf '# pass %d  fail %d  skip %d\n' "$pass" "$fail" "$skip"
exit $(( fail > 0 ? 1 : 0 ))
