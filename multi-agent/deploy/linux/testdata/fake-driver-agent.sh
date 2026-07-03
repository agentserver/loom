#!/usr/bin/env bash
# fake-driver-agent.sh — bats-fixture shim for driver-agent. Extracts
# `--listen host:port` and binds it (readiness gate for driver in
# stub mode is TCP LISTEN + any HTTP response). Dumps env.

set -euo pipefail
LOG="${LOOM_SHIM_LOG:-${TMPDIR:-/tmp}/driver-agent-shim.log}"
{
    printf 'invocation: %s\n' "$0 $*"
    printf '%s\n' 'env:'
    env | sort
    printf '%s\n' '---'
} >> "$LOG"

LISTEN="127.0.0.1:18092"
for ((i=1; i<=$#; i++)); do
    if [[ "${!i}" == "--listen" ]]; then
        j=$((i+1)); LISTEN="${!j}"
    fi
done
HOST="${LISTEN%%:*}"; PORT="${LISTEN##*:}"

exec python3 - "$HOST" "$PORT" <<'PY'
import sys, http.server
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):  self.send_response(401); self.end_headers(); self.wfile.write(b'')
    def do_POST(self): self.send_response(401); self.end_headers(); self.wfile.write(b'')
    def log_message(self, *a, **k): pass
http.server.HTTPServer((sys.argv[1], int(sys.argv[2])), H).serve_forever()
PY
