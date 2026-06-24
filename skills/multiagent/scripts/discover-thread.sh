#!/usr/bin/env bash
# discover-thread.sh — print the current codex thread_id to stdout, or
# exit non-zero with an explanation on stderr. Deterministic; no heuristics.
#
# Signals allowed:
#   1. $CODEX_THREAD_ID (codex itself wrote it, or a forwarder did)
#   2. /proc/<ppid>/fd open rollout file under $CODEX_HOME/sessions/
#      with exact UUID suffix (Linux)
#
# Cwd-based or "newest thread" sqlite lookup is INTENTIONALLY OMITTED —
# the same cwd routinely hosts multiple codex threads, and "newest" can
# silently mis-attribute to the wrong one.
#
# /proc/<ppid>/fd matches MUST be:
#   - under $CODEX_HOME/sessions/ (canonicalized prefix)
#   - basename ends with -<UUID>.jsonl
# Across the parent-chain walk we collect UNIQUE thread_id candidates;
# exactly 1 → emit it; 0 → fall to manual fallback; >1 → fail loud and
# print the candidates so operators can see the invariant violation.

set -euo pipefail

UUID_RE='^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$'
valid() { [[ "$1" =~ $UUID_RE ]]; }

# Source 1: env
if [[ -n "${CODEX_THREAD_ID:-}" ]] && valid "$CODEX_THREAD_ID"; then
    echo "$CODEX_THREAD_ID"
    exit 0
fi

# Source 2: Linux /proc/<pid>/fd walk
CODEX_HOME="${CODEX_HOME:-$HOME/.codex}"
# Canonicalize so symlinked CODEX_HOME doesn't false-negative the prefix
# check (gopsutil-side fd readlinks return realpath).
SESSIONS_ROOT=$(cd "$CODEX_HOME/sessions" 2>/dev/null && pwd -P || true)

if [[ -n "$SESSIONS_ROOT" && -d "/proc/$PPID/fd" ]]; then
    declare -A seen=()
    pid=$PPID
    for _ in 1 2 3 4 5; do
        [[ "$pid" -gt 1 ]] || break
        if [[ -d "/proc/$pid/fd" ]]; then
            for fd in /proc/$pid/fd/*; do
                target=$(readlink -f "$fd" 2>/dev/null) || continue
                # Reject anything not under sessions root
                [[ "$target" == "$SESSIONS_ROOT"/* ]] || continue
                # Extract trailing UUID (validate strict shape)
                cand=$(printf '%s\n' "$target" | sed -nE \
                    's|.*-([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})\.jsonl$|\1|p')
                if [[ -n "$cand" ]] && valid "$cand"; then
                    seen["$cand"]=1
                fi
            done
        fi
        pid=$(awk '/^PPid:/{print $2}' "/proc/$pid/status" 2>/dev/null || echo 1)
    done

    case "${#seen[@]}" in
        1)
            for k in "${!seen[@]}"; do echo "$k"; done
            exit 0
            ;;
        0)
            : # fall through to manual-fallback message below
            ;;
        *)
            {
                echo "discover-thread: parent codex appears to hold MORE THAN ONE"
                echo "open rollout fd under $SESSIONS_ROOT. Refusing to guess."
                echo "Candidate thread_ids:"
                for k in "${!seen[@]}"; do echo "  - $k"; done
                echo "Next: use /status in codex to identify the correct id"
                echo "and call driver.bind_thread(thread_id=...) directly."
            } >&2
            exit 2
            ;;
    esac
fi

# All sources failed — explicit fail with operator-actionable message.
{
    echo "discover-thread: could not determine the parent codex thread_id."
    echo ""
    echo "Tried:"
    echo "  1. \$CODEX_THREAD_ID env (unset or malformed)"
    if [[ -d "/proc/$PPID/fd" ]]; then
        if [[ -z "${SESSIONS_ROOT:-}" ]]; then
            echo "  2. /proc/<ppid>/fd scan (skipped — \$CODEX_HOME/sessions missing)"
        else
            echo "  2. /proc/<ppid>/fd scan under $SESSIONS_ROOT (no match)"
        fi
    else
        echo "  2. /proc/<ppid>/fd scan (skipped — not on Linux)"
    fi
    echo ""
    echo "Next steps:"
    echo "  - In your codex session, type /status. Find the line"
    echo "    starting with 'thread_id:' and copy the UUID."
    echo "  - Then call driver.bind_thread(thread_id=<that uuid>) manually."
} >&2
exit 1
