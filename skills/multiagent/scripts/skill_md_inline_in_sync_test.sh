#!/usr/bin/env bash
# skill_md_inline_in_sync_test.sh — assert the inlined heredoc body in
# SKILL.md is byte-identical to scripts/discover-thread.sh. Without this
# gate the inline copy can silently drift and the model would run a stale
# script.

set -euo pipefail

TEST_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
SKILL_DIR="$(cd "$TEST_DIR/.." && pwd -P)"
SCRIPT_FILE="$TEST_DIR/discover-thread.sh"
SKILL_MD="$SKILL_DIR/SKILL.md"

if [[ ! -f "$SCRIPT_FILE" ]]; then
    echo "skill_md_inline_in_sync_test: $SCRIPT_FILE missing" >&2
    exit 1
fi
if [[ ! -f "$SKILL_MD" ]]; then
    echo "skill_md_inline_in_sync_test: $SKILL_MD missing" >&2
    exit 1
fi

# Extract the heredoc body. Tightened awk state machine:
#   p=1 after "## Initialization" header (defense-in-depth section anchor)
#   flag=1 after the ACTUAL heredoc opener `^bash <<'DISCOVER_EOF'$`
#     — anchored to start-of-line so prose backtick-mentions on lines that
#       read <<'DISCOVER_EOF' mid-line are ignored.
#   exit when we hit the closing `DISCOVER_EOF` line.
#
# Stream awk's output STRAIGHT TO A FILE (not a command-substitution
# variable) — `$(awk ...)` strips ALL trailing newlines, so a heredoc
# that drifted by adding blank lines before its terminator would be
# rebuilt as a single trailing `\n` via `printf '%s\n'` and falsely
# compare equal to the canonical script.
extracted=$(mktemp -t skill-md-extract.XXXXXX)
trap 'rm -f "$extracted" /tmp/skill-drift.$$.diff' EXIT
awk '
    /^## Initialization/ {p=1}
    p && /^bash <<.DISCOVER_EOF.$/ {flag=1; next}
    flag && /^DISCOVER_EOF$/ {exit}
    flag {print}
' "$SKILL_MD" > "$extracted"

if [[ ! -s "$extracted" ]]; then
    echo "skill_md_inline_in_sync_test: could not extract heredoc body from $SKILL_MD" >&2
    echo "Expected a 'bash <<'\'DISCOVER_EOF\''' block inside the '## Initialization' section." >&2
    exit 1
fi

# Compare byte-for-byte by diffing the two real files. `diff` honors
# trailing newlines and intra-file blank lines exactly; no shell-string
# truncation can hide drift.
if ! diff -u "$SCRIPT_FILE" "$extracted" >/tmp/skill-drift.$$.diff 2>&1; then
    {
        echo "skill_md_inline_in_sync_test: FAILED — SKILL.md heredoc body has drifted from $SCRIPT_FILE."
        echo "Either copy the canonical script into the heredoc, or update the script and re-paste."
        echo "---"
        cat /tmp/skill-drift.$$.diff
    } >&2
    exit 1
fi

echo "ok - SKILL.md heredoc matches discover-thread.sh"
