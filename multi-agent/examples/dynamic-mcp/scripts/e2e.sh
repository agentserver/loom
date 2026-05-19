#!/usr/bin/env bash
# Live end-to-end test for the dynamic-mcp feature.
#
# Prerequisites (one-time):
#   - AGENTSERVER_URL set, agentserver reachable
#   - claude on PATH (logged in)
#   - go and python3 on PATH
#   - Three pre-registered config files (with credentials filled in by prior
#     interactive device-flow registration), paths supplied via env:
#       MASTER_CONFIG    config.yaml for cmd/master-agent
#       BUILDER_CONFIG   config.yaml for cmd/slave-agent (with register_mcp skill)
#       DRIVER_CONFIG    config.yaml for examples/dynamic-mcp/e2e-driver
#
# See README.md in this directory for first-time setup.
set -euo pipefail

require_env() {
  for v in AGENTSERVER_URL MASTER_CONFIG BUILDER_CONFIG DRIVER_CONFIG; do
    if [ -z "${!v:-}" ]; then
      echo "missing required env: $v" >&2
      exit 2
    fi
  done
  for cmd in go claude python3; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
      echo "missing required command: $cmd" >&2
      exit 2
    fi
  done
}
require_env

script_dir=$(cd "$(dirname "$0")" && pwd)
module_root=$(cd "$script_dir/../../.." && pwd)
cd "$module_root"

work=$(mktemp -d)
echo "work dir: $work"
pids=()
cleanup() {
  for p in "${pids[@]:-}"; do
    [ -n "$p" ] && kill "$p" 2>/dev/null || true
  done
  rm -rf "$work"
}
trap cleanup EXIT

mkdir -p "$work/bin"
echo "building binaries..."
go build -o "$work/bin/master-agent"   ./cmd/master-agent
go build -o "$work/bin/slave-agent"    ./cmd/slave-agent
go build -o "$work/bin/e2e-driver"     ./examples/dynamic-mcp/e2e-driver

builder_dir=$(dirname "$BUILDER_CONFIG")
master_dir=$(dirname "$MASTER_CONFIG")

echo "launching builder slave-agent (cwd=$builder_dir)..."
( cd "$builder_dir" && "$work/bin/slave-agent" "$(basename "$BUILDER_CONFIG")" ) > "$work/builder.log" 2>&1 &
pids+=($!)

echo "launching master-agent (cwd=$master_dir)..."
( cd "$master_dir" && "$work/bin/master-agent" "$(basename "$MASTER_CONFIG")" ) > "$work/master.log" 2>&1 &
pids+=($!)

echo "running e2e driver..."
set +e
"$work/bin/e2e-driver" \
  --config "$DRIVER_CONFIG" \
  --target-display-name master-dynmcp \
  --expect-agents dynmcp-builder \
  --builder-dir "$builder_dir" \
  --timeout 600s
status=$?
set -e

if [ "$status" -ne 0 ]; then
  echo "e2e FAILED. Logs:" >&2
  for log in master.log builder.log; do
    echo "--- $work/$log ---" >&2
    tail -n 80 "$work/$log" >&2 || true
  done
  exit "$status"
fi

echo "OK dynamic-mcp e2e"
