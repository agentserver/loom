#!/usr/bin/env bash
# Plan-review r4 P1 — LOOM_FULLTABLE_DISPATCH_SHIM=1 reaches the
# mkdir + planner path (but stops before real subprocess exec). Any
# missed smoke_root_abs reference in run.sh would create files under
# smoke/ during the mkdir loop → caught here.
#
# CRITICAL: do NOT pass --dry-run. --dry-run short-circuits before
# preflight AND before the mkdir loop.
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
module_root="$(cd "$here/../../../.." && pwd)"
run_sh="$module_root/tools/eval/fulltable/run.sh"

tmpdir=$(mktemp -d)
trap "rm -rf '$tmpdir'" EXIT

alt_root="$module_root/tests/eval/results/experiments/_test_scoping_$$"
mkdir -p "$alt_root"
trap "rm -rf '$alt_root' '$tmpdir'" EXIT

clean_repo="$tmpdir/clean_repo"
git init -q "$clean_repo" >/dev/null 2>&1
git -C "$clean_repo" -c user.email=t@t -c user.name=t commit --allow-empty -m init -q >/dev/null 2>&1

smoke_dir="$module_root/tests/eval/results/smoke"
snapshot_before="$tmpdir/smoke_before.txt"
snapshot_after="$tmpdir/smoke_after.txt"
find "$smoke_dir" \( -type f -o -type d \) 2>/dev/null | LC_ALL=C sort > "$snapshot_before"

ALLOW_FULL_RUN=1 WORKTREE_ROOT="$clean_repo" LOOM_FULLTABLE_DISPATCH_SHIM=1 \
  bash "$run_sh" \
    --workload cross-device-code-mod \
    --results-root "$alt_root" \
    --sample 3 > "$tmpdir/scope.out" 2>&1
rc=$?
if [ "$rc" -ne 0 ]; then
  echo "FAIL: dispatch-shim exit=$rc"
  cat "$tmpdir/scope.out"
  exit 1
fi
if ! grep -q "SHIM: would dispatch" "$tmpdir/scope.out"; then
  echo "FAIL: dispatch-shim did NOT reach dispatch-planning"
  cat "$tmpdir/scope.out"
  exit 1
fi

find "$smoke_dir" \( -type f -o -type d \) 2>/dev/null | LC_ALL=C sort > "$snapshot_after"
if ! diff -u "$snapshot_before" "$snapshot_after" > "$tmpdir/smoke_diff.out"; then
  echo "FAIL: DISPATCH_SHIM wrote to smoke/ under --results-root"
  cat "$tmpdir/smoke_diff.out"
  exit 1
fi

for sub in dbs runs paper; do
  if [ ! -d "$alt_root/$sub" ]; then
    echo "FAIL: expected $alt_root/$sub after DISPATCH_SHIM run.sh --results-root"
    ls -la "$alt_root/"
    exit 1
  fi
done

echo "OK"
