#!/usr/bin/env bash
set -euo pipefail
# laptop_up.sh — WT-3-prod-multidevice Linux laptop (driver) wrapper.
#
# Calls Phase 2 deploy.sh with --mode stub for smoke. --mode prod is
# ONLY reached when ALLOW_PROD_DEPLOY=1 is set explicitly — otherwise
# the wrapper silently downgrades to stub (spec §7(k)). dry_run_all.sh
# NEVER sets ALLOW_PROD_DEPLOY, so smoke is always loopback stub.
#
# Bind endpoint is 127.0.0.1:18092 (loopback only per spec §7(b)).
# Real cross-device tunnels are signed by agentserver and never hard-
# coded here.

CONFIG=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        --config) CONFIG="$2"; shift 2 ;;
        *) echo "laptop_up.sh: unknown arg: $1" >&2; exit 2 ;;
    esac
done
[[ -n "$CONFIG" ]] || { echo "laptop_up.sh: --config <path> required" >&2; exit 2; }
[[ -r "$CONFIG" ]] || { echo "laptop_up.sh: cannot read $CONFIG" >&2; exit 2; }

MODE="${LOOM_DEPLOY_MODE:-stub}"
if [[ "$MODE" == "prod" ]] && [[ "${ALLOW_PROD_DEPLOY:-0}" != "1" ]]; then
    echo "laptop_up.sh: ALLOW_PROD_DEPLOY not set; falling back to --mode stub" >&2
    MODE=stub
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_SH="$(cd "$SCRIPT_DIR/../../../../deploy/linux" 2>/dev/null && pwd)/deploy.sh"

if [[ ! -x "$DEPLOY_SH" ]]; then
    echo "laptop_up.sh: deploy.sh not executable at $DEPLOY_SH (dry-run mode)" >&2
    echo "would-exec: $DEPLOY_SH --mode $MODE --driver-port 18092"
    exit 0
fi

# Loopback bind — driver-port 18092 (spec §2.1 device 1)
exec "$DEPLOY_SH" --mode "$MODE" --driver-port 18092
