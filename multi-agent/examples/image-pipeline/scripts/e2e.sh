#!/usr/bin/env bash
# Live end-to-end test for the image pipeline.
#
# Prerequisites (one-time):
#   - AGENTSERVER_URL set, agentserver reachable
#   - claude on PATH (claude CLI auth already configured locally — no
#     ANTHROPIC_API_KEY env var needed; the binary picks up its own session)
#   - go and sqlite3 on PATH
#   - Four pre-registered config files (with credentials filled by prior
#     interactive device-flow registration), paths supplied via env:
#       MASTER_CONFIG    config.yaml for cmd/master-agent
#       CAPTURE_CONFIG   config.yaml for examples/image-pipeline/agent-image-capture
#       COMPRESS_CONFIG  config.yaml for examples/image-pipeline/agent-image-compress
#       DRIVER_CONFIG    config.yaml for examples/image-pipeline/e2e-driver
#
# See README.md in this directory for first-time setup.
set -euo pipefail

require_env() {
  for v in AGENTSERVER_URL MASTER_CONFIG CAPTURE_CONFIG COMPRESS_CONFIG DRIVER_CONFIG; do
    if [ -z "${!v:-}" ]; then
      echo "missing required env: $v" >&2
      exit 2
    fi
  done
  for cmd in go claude sqlite3; do
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

# Build all four binaries.
mkdir -p "$work/bin"
echo "building binaries..."
go build -o "$work/bin/master-agent"          ./cmd/master-agent
go build -o "$work/bin/agent-image-capture"   ./examples/image-pipeline/agent-image-capture
go build -o "$work/bin/agent-image-compress"  ./examples/image-pipeline/agent-image-compress
go build -o "$work/bin/e2e-driver"            ./examples/image-pipeline/e2e-driver

# Lay out per-agent working dirs and copy configs.
for name in master capture compress; do
  mkdir -p "$work/$name"
done
cp "$MASTER_CONFIG"   "$work/master/config.yaml"
cp "$CAPTURE_CONFIG"  "$work/capture/config.yaml"
cp "$COMPRESS_CONFIG" "$work/compress/config.yaml"

# Launch the three long-running agents.
echo "launching agent-image-capture..."
( cd "$work/capture"  && "$work/bin/agent-image-capture"  --config config.yaml ) > "$work/capture.log"  2>&1 &
pids+=($!)
echo "launching agent-image-compress..."
( cd "$work/compress" && "$work/bin/agent-image-compress" --config config.yaml ) > "$work/compress.log" 2>&1 &
pids+=($!)
echo "launching master-agent..."
( cd "$work/master"   && "$work/bin/master-agent"         config.yaml         ) > "$work/master.log"   2>&1 &
pids+=($!)

echo "running e2e driver..."
set +e
"$work/bin/e2e-driver" \
  --config "$DRIVER_CONFIG" \
  --target-display-name master-e2e-image \
  --expect-agents image-capture,image-compress \
  --master-data-db "$work/master/data.db" \
  --timeout 300s
status=$?
set -e

if [ "$status" -ne 0 ]; then
  echo "e2e FAILED. Logs:" >&2
  for log in master.log capture.log compress.log; do
    echo "--- $work/$log ---" >&2
    tail -n 50 "$work/$log" >&2 || true
  done
  exit "$status"
fi

echo "OK image-pipeline e2e"
