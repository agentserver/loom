#!/usr/bin/env bash
# fake-slave-agent.sh — bats-fixture shim for slave-agent. Dumps env,
# does NOT bind any port (matches spec §4.2 stub: slave daemon is
# disabled in stub mode), just idles until killed. Its readiness gate
# in stub mode is a whoami round-trip against the stub, not this
# process's port.

set -euo pipefail
LOG="${LOOM_SHIM_LOG:-${TMPDIR:-/tmp}/slave-agent-shim.log}"
{
    printf 'invocation: %s\n' "$0 $*"
    printf '%s\n' 'env:'
    env | sort
    printf '%s\n' '---'
} >> "$LOG"
exec sleep 3600
