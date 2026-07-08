#!/usr/bin/env bash
# Spec §5 test_run_sh_duplicate_workload — --workload may be given at
# most once. Resolves P2 round-1 (upgraded to acceptance test).
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
module_root="$(cd "$here/../../../.." && pwd)"
run_sh="$module_root/tools/eval/fulltable/run.sh"

tmpdir=$(mktemp -d)
trap "rm -rf '$tmpdir'" EXIT

if bash "$run_sh" --workload cross-device-code-mod --workload credential-bound-model --sample 1 --dry-run 2>"$tmpdir/dup1.err"; then
  echo "case1 fail — duplicate --workload should exit 2"
  cat "$tmpdir/dup1.err"
  exit 1
fi
grep -q -e "--workload may be given at most once" "$tmpdir/dup1.err" \
  || { echo "case1 fail — missing duplicate-workload message"; cat "$tmpdir/dup1.err"; exit 1; }
echo "PASS  case1"

if bash "$run_sh" --workload=cross-device-code-mod --workload=credential-bound-model --sample 1 --dry-run 2>"$tmpdir/dup2.err"; then
  echo "case2 fail — duplicate --workload= should exit 2"
  exit 1
fi
echo "PASS  case2"

LOOM_FULLTABLE_WRAPPER_SHIM=1 bash "$run_sh" --workload cross-device-code-mod --dry-run >/dev/null 2>&1 \
  || { echo "case3 fail — single --workload should be accepted"; exit 1; }
echo "PASS  case3"
