#!/usr/bin/env bash
# fake-observer-server.sh — bats-fixture shim for observer-server.
# Dumps its own env to $LOOM_SHIM_LOG (for §7(g) whitelist assertions),
# renders as a real binary would (arch-suffixed name), then binds a
# loopback TCP port and idles.
#
# Behaviour driven by $LOOM_SHIM_MODE (see fake-agentserver-stub.sh
# for the shared mode contract):
#   healthy (default) — binds the port from -config's listen_addr,
#                       serves any request with 200, dumps env.
#   stalled  — never binds (used by future readiness-fail tests).

set -euo pipefail

LOG="${LOOM_SHIM_LOG:-${TMPDIR:-/tmp}/observer-server-shim.log}"
MODE="${LOOM_SHIM_MODE:-healthy}"

{
    printf 'invocation: %s\n' "$0 $*"
    printf '%s\n' 'env:'
    env | sort
    printf '%s\n' '---'
} >> "$LOG"

if [[ "$MODE" == "stalled" ]]; then
    exec sleep 3600
fi

# Extract listen_addr from the config file after `-config`.
CFG=""
for ((i=1; i<=$#; i++)); do
    if [[ "${!i}" == "-config" ]]; then
        j=$((i+1)); CFG="${!j}"
    fi
done
PORT=18091
if [[ -r "$CFG" ]]; then
    la=$(grep -oE 'listen_addr[[:space:]]*:[[:space:]]*"?[^"[:space:]]+' "$CFG" | head -1 | grep -oE '[0-9]+$' || echo)
    [[ -n "$la" ]] && PORT="$la"
fi

exec python3 - "$PORT" <<'PY'
import sys, http.server, socketserver
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):  self.send_response(200); self.end_headers(); self.wfile.write(b'ok')
    def do_POST(self): self.send_response(200); self.end_headers(); self.wfile.write(b'ok')
    def log_message(self, *a, **k): pass
http.server.HTTPServer(('127.0.0.1', int(sys.argv[1])), H).serve_forever()
PY
