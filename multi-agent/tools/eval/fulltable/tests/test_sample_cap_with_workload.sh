#!/usr/bin/env bash
# Spec §5 TestSampleCapEnforcedWithWorkloadFilter — --sample N>3 with
# --workload still requires ALLOW_FULL_RUN=1. Cap enforced at run.sh
# (not planner) per P0#4 round-1 resolution.
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
module_root="$(cd "$here/../../../.." && pwd)"
run_sh="$module_root/tools/eval/fulltable/run.sh"

# 1) --workload X --sample 4 without ALLOW_FULL_RUN → exit 2
tmpdir=$(mktemp -d)
trap "rm -rf '$tmpdir'" EXIT

if bash "$run_sh" --workload cross-device-code-mod --sample 4 --dry-run 2>"$tmpdir/cap1.err"; then
  echo "case1 fail — --sample 4 without ALLOW_FULL_RUN=1 should exit 2"
  exit 1
fi
grep -q "ErrFullRunNotAllowed\|--sample" "$tmpdir/cap1.err" \
  || { echo "case1 fail — missing cap error"; cat "$tmpdir/cap1.err"; exit 1; }
echo "PASS  case1"

# 2) --workload X --sample=4 (equals form) also blocked
if bash "$run_sh" --workload cross-device-code-mod --sample=4 --dry-run 2>"$tmpdir/cap2.err"; then
  echo "case2 fail — --sample=4 should exit 2"
  exit 1
fi
echo "PASS  case2"

# 3) With ALLOW_FULL_RUN=1 the cap does NOT fire. --dry-run mode
# always emits the FULL filtered set (dry-run doesn't apply --sample);
# what matters here is (a) exit 0 (cap bypassed) and (b) the "$workload"
# filter applied — expect 12 rows for one workload.
out=$(ALLOW_FULL_RUN=1 bash "$run_sh" --workload cross-device-code-mod --sample 4 --dry-run 2>&1)
rc=$?
if [ "$rc" -ne 0 ]; then
  echo "case3 fail — ALLOW_FULL_RUN=1 should unblock sample cap; exit=$rc"
  echo "$out"
  exit 1
fi
n=$(echo "$out" | grep -c "cross-device-code-mod" || true)
if [ "$n" -ne 12 ]; then
  echo "case3 fail — expected 12 filtered rows in dry-run output; got $n"
  echo "$out"
  exit 1
fi
echo "PASS  case3"
