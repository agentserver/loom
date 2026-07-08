#!/usr/bin/env bash
# Plan-review P0#1: --results-root MUST scope run.sh's own writes.
# WRAPPER_SHIM path — verifies SHIM output uses the alt root.
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
module_root="$(cd "$here/../../../.." && pwd)"
run_sh="$module_root/tools/eval/fulltable/run.sh"

alt_root="$module_root/tests/eval/results/experiments/_test_scoping_wrap_$$"
mkdir -p "$alt_root"
trap "rm -rf '$alt_root'" EXIT

smoke_dir="$module_root/tests/eval/results/smoke"
smoke_before=$(find "$smoke_dir" -type f 2>/dev/null | wc -l)

LOOM_FULLTABLE_WRAPPER_SHIM=1 bash "$run_sh" \
  --workload cross-device-code-mod \
  --results-root "$alt_root" \
  --dry-run > /dev/null

smoke_after=$(find "$smoke_dir" -type f 2>/dev/null | wc -l)
if [ "$smoke_after" -gt "$smoke_before" ]; then
  echo "FAIL: writes leaked into smoke/ under --results-root; before=$smoke_before after=$smoke_after"
  exit 1
fi

out=$(LOOM_FULLTABLE_WRAPPER_SHIM=1 bash "$run_sh" \
    --workload cross-device-code-mod \
    --results-root "$alt_root" \
    --dry-run 2>&1)
if ! echo "$out" | grep -q "SHIM_RESULTS_ROOT: $alt_root"; then
  echo "FAIL: SHIM output missing SHIM_RESULTS_ROOT: $alt_root"
  echo "$out"
  exit 1
fi

echo "OK"
