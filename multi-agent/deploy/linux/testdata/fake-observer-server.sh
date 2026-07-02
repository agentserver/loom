#!/usr/bin/env bash
# fake-observer-server.sh — shim for T15/T15b env-whitelist tests.
# Behaviour: dumps its own env to $LOOM_TEST_ENV_LOG (or /tmp/env.log if
# unset), then sleeps until killed. Never binds a real port.

set -euo pipefail

LOG="${LOOM_TEST_ENV_LOG:-/tmp/env.log}"
env | sort > "$LOG"
# Announce readiness so a pid-only smoke that only checks liveness sees the file.
echo "fake-observer-server: env dumped to $LOG" >&2
# Bind a socket so wait_tcp readiness gate passes (test harness sets --observer-port).
# The port comes from -config arg parsing in the real server; we cheat and read $2
# looking for a listen_addr.
if [[ $# -ge 2 && "$1" == "-config" ]]; then
    cfg="$2"
    if [[ -r "$cfg" ]]; then
        port=$(grep -oE 'listen_addr[[:space:]]*:[[:space:]]*"?127\.0\.0\.1:[0-9]+' "$cfg" | \
               grep -oE '[0-9]+$' || echo "18091")
    else
        port=18091
    fi
else
    port=18091
fi
# nc-based bind so wait_tcp sees LISTEN. If nc is not available we
# just sleep and let the test's wait_tcp time out on its own.
if command -v nc >/dev/null 2>&1; then
    exec nc -l -k "127.0.0.1" "$port"
else
    exec sleep 3600
fi
