#!/usr/bin/env bash
# fake-agentserver-stub.sh — bats-fixture shim standing in for the
# real Go `agentserver-stub` binary. Used by deploy_test.bats runtime
# rows (T6, T6b, T6c, T17, T18*) so we can drive `deploy.sh --stub`
# end-to-end without any Go build step.
#
# Modes (driven by env, not by --flag so `deploy.sh` doesn't need to
# know it's talking to a shim):
#   LOOM_SHIM_MODE=healthy   (default) — serves /healthz 200, /issue
#                                        returns deterministic 5-tuple,
#                                        /whoami returns 200 for any
#                                        Bearer token, /agents-tunnel
#                                        connects then blocks.
#   LOOM_SHIM_MODE=stalled   — TCP LISTEN comes up but /healthz never
#                              responds (200 or otherwise). Used by
#                              T18c readiness-timeout test.
#   LOOM_SHIM_MODE=exit1     — exits 1 immediately (used by future
#                              exit-3 sub-installer tests).
#
# Argv:
#   agentserver-stub [--listen HOST:PORT] [--workspace-id ID]
#   agentserver-stub issue --server URL --role R --short-id S
#
# Every invocation also appends its argv + parent env to
# $LOOM_SHIM_LOG (default $TMPDIR/agentserver-stub-shim.log). Tests grep
# that log to falsify the argv-loopback + env-whitelist assertions.

set -euo pipefail

LOG="${LOOM_SHIM_LOG:-${TMPDIR:-/tmp}/agentserver-stub-shim.log}"
MODE="${LOOM_SHIM_MODE:-healthy}"

log_invocation() {
    {
        printf 'invocation: %s\n' "$0 $*"
        printf '%s' 'argv:'
        for a in "$@"; do printf ' [%s]' "$a"; done
        printf '%s\n' ''
        printf 'env: (whitelist audit)\n'
        env | sort
        printf '%s\n' '---'
    } >> "$LOG"
}

log_invocation "$@"

# `issue` subcommand — deterministic 5-tuple.
if [[ "${1:-}" == "issue" ]]; then
    if [[ "$MODE" == "exit1" ]]; then
        echo "shim: exit1 mode" >&2
        exit 1
    fi
    # Parse --role for token differentiation only.
    role=""
    for ((i=1; i<$#; i++)); do
        if [[ "${!i}" == "--role" ]]; then
            j=$((i+1)); role="${!j}"
        fi
    done
    printf '{"sandbox_id":"sbx-shim-%s","tunnel_token":"ttok-shim-%s","proxy_token":"ptok-shim-%s","workspace_id":"ws-eval-auto","short_id":"shim-%s-001"}\n' \
        "$role" "$role" "$role" "$role"
    exit 0
fi

# `serve` (default) — bind loopback + serve minimal HTTP.
LISTEN="127.0.0.1:18080"
for ((i=1; i<=$#; i++)); do
    if [[ "${!i}" == "--listen" ]]; then
        j=$((i+1)); LISTEN="${!j}"
    fi
done
HOST="${LISTEN%%:*}"
PORT="${LISTEN##*:}"

if [[ "$MODE" == "exit1" ]]; then
    echo "shim: exit1 mode" >&2
    exit 1
fi

# Use a minimal python http.server for portability. `exec` so the
# recorded PID belongs to python — makes `kill -TERM $bash_pid` from
# deploy.sh's trap actually kill the server, not orphan python as a
# lingering child. Critical for T18c-runtime (readiness timeout);
# without exec, the python subprocess outlives the bash shim and
# ties up port :18080 across tests.
exec python3 - "$HOST" "$PORT" "$MODE" <<'PY'
import sys, threading, socketserver
from http.server import BaseHTTPRequestHandler, HTTPServer

host, port, mode = sys.argv[1], int(sys.argv[2]), sys.argv[3]

class H(BaseHTTPRequestHandler):
    def _respond(self, code, body=b'', ctype='application/json'):
        self.send_response(code)
        self.send_header('Content-Type', ctype)
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == '/healthz':
            if mode == 'stalled':
                # Never respond — hold the connection open a short
                # time so the client's short-timeout probe fails but
                # SIGTERM can still tear us down promptly.
                import time
                try: time.sleep(1.0)
                except (KeyboardInterrupt, SystemExit): pass
                return
            self._respond(200, b'{"ok":true}')
            return
        if self.path.endswith('/whoami'):
            self._respond(200, b'{"user_id":"shim","workspace_id":"ws-eval-auto","workspace_name":"shim","sandbox_id":"sbx-shim","short_id":"shim-001","role":"shim"}')
            return
        if self.path.endswith('/agents-tunnel'):
            # yamux upgrade would happen here in the real stub; we just
            # 404 like real stub does.
            self._respond(404, b'not implemented')
            return
        self._respond(404, b'not found')

    def do_POST(self):
        length = int(self.headers.get('Content-Length', '0'))
        _ = self.rfile.read(length)
        if self.path.endswith('/heartbeat'):
            self.send_response(204); self.end_headers(); return
        if self.path.endswith('/register'):
            self._respond(200, b'{"sandbox_id":"sbx-shim","tunnel_token":"ttok-shim","proxy_token":"ptok-shim","workspace_id":"ws-eval-auto","short_id":"shim-001"}')
            return
        self._respond(404, b'not found')

    def log_message(self, *a, **k): pass  # quiet

srv = HTTPServer((host, port), H)
srv.serve_forever()
PY
