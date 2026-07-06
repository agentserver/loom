#!/usr/bin/env bash
set -euo pipefail
# cloud_up.sh — WT-3-prod-multidevice cloud sandbox (slave-C) wrapper.
#
# NO real droplet is created here. Cloud provisioning is print-only:
# the script prints `doctl` / `e2b` / `ssh` commands the operator would
# run. --mode prod gated on ALLOW_PROD_DEPLOY=1 (spec §7(k)).
#
# Uploaded cloud config is scanned via secretscrub_python.py before any
# print (spec §7(d)); a secret hit aborts.

CONFIG=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        --config) CONFIG="$2"; shift 2 ;;
        *) echo "cloud_up.sh: unknown arg: $1" >&2; exit 2 ;;
    esac
done
[[ -n "$CONFIG" ]] || { echo "cloud_up.sh: --config <path> required" >&2; exit 2; }
[[ -r "$CONFIG" ]] || { echo "cloud_up.sh: cannot read $CONFIG" >&2; exit 2; }

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
UPLOAD="$SCRIPT_DIR/cloud_upload.py"

# Secret scrub gate — refuses to proceed if the cloud config carries
# any raw token / credential. Exits non-zero on hit.
python3 "$UPLOAD" --input "$CONFIG" --dry-run

# Vendor pick — cloud.yaml.template has a single `vendor:` line.
VENDOR="$(sed -nE 's/^vendor:[[:space:]]*(.*)$/\1/p' "$CONFIG" | head -n1)"
case "$VENDOR" in
    digitalocean)
        echo "cloud_up.sh: would run: doctl compute droplet create loom-slave-c --region nyc3 --image ubuntu-22-04-x64 --size s-1vcpu-1gb"
        ;;
    e2b)
        echo "cloud_up.sh: would run: e2b sandbox create --template loom-base"
        ;;
    vps)
        echo "cloud_up.sh: would run: ssh <vps-host> 'systemctl start loom-slave'"
        ;;
    *)
        echo "cloud_up.sh: vendor '$VENDOR' not in {digitalocean, e2b, vps}" >&2
        exit 2
        ;;
esac

MODE="${LOOM_DEPLOY_MODE:-stub}"
if [[ "$MODE" == "prod" ]] && [[ "${ALLOW_PROD_DEPLOY:-0}" != "1" ]]; then
    echo "cloud_up.sh: ALLOW_PROD_DEPLOY not set; falling back to --mode stub" >&2
    MODE=stub
fi

DEPLOY_SH="$(cd "$SCRIPT_DIR/../../../../deploy/linux" 2>/dev/null && pwd)/deploy.sh"

if [[ ! -x "$DEPLOY_SH" ]]; then
    echo "cloud_up.sh: deploy.sh not executable at $DEPLOY_SH (dry-run mode)" >&2
    echo "would-exec-on-droplet: $DEPLOY_SH --mode $MODE --slave-port 18093"
    exit 0
fi

# In the real-run worktree, this ssh's into the droplet to run
# deploy.sh --mode prod. Here we only print what we would do.
echo "cloud_up.sh: would-ssh-and-exec: $DEPLOY_SH --mode $MODE --slave-port 18093"
