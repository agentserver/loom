#!/usr/bin/env bash
set -euo pipefail
# headless_up.sh — WT-3-prod-multidevice Linux headless (observer + slave-A) wrapper.
#
# Bind endpoints: observer 127.0.0.1:18091, slave-A 127.0.0.1:18093.
# --mode prod gated on ALLOW_PROD_DEPLOY=1 (spec §7(k)).

CONFIG=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        --config) CONFIG="$2"; shift 2 ;;
        *) echo "headless_up.sh: unknown arg: $1" >&2; exit 2 ;;
    esac
done
[[ -n "$CONFIG" ]] || { echo "headless_up.sh: --config <path> required" >&2; exit 2; }
[[ -r "$CONFIG" ]] || { echo "headless_up.sh: cannot read $CONFIG" >&2; exit 2; }

MODE="${LOOM_DEPLOY_MODE:-stub}"
if [[ "$MODE" == "prod" ]] && [[ "${ALLOW_PROD_DEPLOY:-0}" != "1" ]]; then
    echo "headless_up.sh: ALLOW_PROD_DEPLOY not set; falling back to --mode stub" >&2
    MODE=stub
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_SH="$(cd "$SCRIPT_DIR/../../../../deploy/linux" 2>/dev/null && pwd)/deploy.sh"

if [[ ! -x "$DEPLOY_SH" ]]; then
    echo "headless_up.sh: deploy.sh not executable at $DEPLOY_SH (dry-run mode)" >&2
    echo "would-exec: $DEPLOY_SH --mode $MODE --observer-port 18091 --slave-port 18093"
    exit 0
fi

exec "$DEPLOY_SH" --mode "$MODE" --observer-port 18091 --slave-port 18093
