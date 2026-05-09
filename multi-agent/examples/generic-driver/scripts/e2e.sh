#!/usr/bin/env bash
# E2E for the generic-driver demo. Requires:
#   AGENTSERVER_URL    - reachable agentserver including the new peer-proxy route
#   DRIVER_CONFIG      - path to a registered driver.yaml
#   MASTER_CONFIG      - path to a registered master-agent yaml
#   FILECONCAT_CONFIG  - path to a registered agent-fileconcat yaml
set -euo pipefail
cd "$(dirname "$0")/../../.."   # → multi-agent/

: "${AGENTSERVER_URL:?must be set}"
: "${DRIVER_CONFIG:?must be set}"
: "${MASTER_CONFIG:?must be set}"
: "${FILECONCAT_CONFIG:?must be set}"

echo "==> building binaries"
go build -o ./bin/driver-agent ./cmd/driver-agent
go build -o ./bin/master-agent ./cmd/master-agent
go build -o ./bin/agent-fileconcat ./examples/generic-driver/agent-fileconcat
go build -o ./bin/e2e ./examples/generic-driver/e2e

# Plumb the slave's proxy_token via env (agentboot doesn't surface it through
# the task handler today; v1 workaround documented in agent-fileconcat/main.go).
export FILECONCAT_PROXY_TOKEN
FILECONCAT_PROXY_TOKEN="$(awk '/proxy_token:/{print $2}' "$FILECONCAT_CONFIG" | tr -d '"')"
[ -n "$FILECONCAT_PROXY_TOKEN" ] || { echo "fileconcat proxy_token missing in $FILECONCAT_CONFIG"; exit 1; }

cleanup() {
  echo "==> stopping background processes"
  jobs -p | xargs -r kill 2>/dev/null || true
}
trap cleanup EXIT

echo "==> starting master"
./bin/master-agent --config "$MASTER_CONFIG" >/tmp/genericdriver-master.log 2>&1 &

echo "==> starting fileconcat slave"
./bin/agent-fileconcat --config "$FILECONCAT_CONFIG" >/tmp/genericdriver-fileconcat.log 2>&1 &

# Give the agents a few seconds to register and publish cards.
sleep 5

echo "==> running e2e"
./bin/e2e --driver-bin ./bin/driver-agent --driver-config "$DRIVER_CONFIG" --mode full
