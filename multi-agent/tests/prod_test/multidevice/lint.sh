#!/usr/bin/env bash
set -euo pipefail
# lint.sh — static checks for WT-3-prod-multidevice multidevice/.
#
# Runs (in order):
#   1. Secret-scan (ERE) — spec §7(a) command; alternation with `|`,
#      not `\|`.
#   2. ExecutionPolicy scope check — every Set-ExecutionPolicy must
#      have `-Scope Process`; no LocalMachine/CurrentUser.
#   3. Bind-endpoint substring check — no `0.0.0.0:` / `[::]:` / bare
#      `:PORT` in wrappers (parser test does the full check; this is
#      a fast fail-early).
#
# Exit 0 = clean.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "lint.sh: 1. secret-scan (ERE)"
# Skip our own scanner regex file, lint.sh itself (which quotes the
# very patterns we scan for), and the tests/ subdir + __pycache__/
# subdirs (test source files quote the patterns as scanner
# infrastructure; compiled .pyc mirrors that). These are all
# scanner-infrastructure, not on-disk OAuth material.
matches=$(
    grep -REn \
        --exclude=secretscrub_python.py \
        --exclude=lint.sh \
        --exclude-dir=tests \
        --exclude-dir=__pycache__ \
        'sk-|ghp_|AKIA|xoxb|Bearer[[:space:]]+[A-Za-z0-9]|refresh_token' \
        "$SCRIPT_DIR" \
        || true
)
if [[ -n "$matches" ]]; then
    # A match on the placeholder itself is fine, but the placeholder
    # doesn't include any of the token-family prefixes — so any hit is
    # a real problem. Print and fail.
    echo "lint.sh: secret-scan hit:" >&2
    printf '%s\n' "$matches" >&2
    exit 3
fi

echo "lint.sh: 2. Windows ExecutionPolicy scope"
# Only scan .ps1 files. Test-source references + lint.sh's own
# comments about Set-ExecutionPolicy are scanner infrastructure and
# do not represent policy-scope changes we would ship.
bad_execpolicy=$(
    grep -REn --include='*.ps1' 'Set-ExecutionPolicy' "$SCRIPT_DIR" \
        | grep -Ev '\-Scope[[:space:]]+Process' \
        || true
)
if [[ -n "$bad_execpolicy" ]]; then
    echo "lint.sh: Set-ExecutionPolicy with non-Process scope found:" >&2
    printf '%s\n' "$bad_execpolicy" >&2
    exit 3
fi

echo "lint.sh: 3. bind-endpoint substring check"
# Exclude lint.sh itself — its comments quote the forbidden
# substrings by design (they document what we scan for).
bad_binds=$(
    grep -REn --include='*.sh' --include='*.ps1' --include='*.yaml*' \
        --exclude=lint.sh \
        -e '0\.0\.0\.0:' \
        -e '\[::\]:' \
        "$SCRIPT_DIR" \
        | grep -v '^Binary' \
        | grep -Ev ':[[:space:]]*#' \
        || true
)
if [[ -n "$bad_binds" ]]; then
    echo "lint.sh: disallowed wildcard bind endpoint found:" >&2
    printf '%s\n' "$bad_binds" >&2
    exit 3
fi

echo "lint.sh: clean"
