#!/usr/bin/env bash
# Plan-review r5 P1 — real-path dispatch-shim (NO WRAPPER_SHIM). Proves
# wrappers don't create files that dirty the worktree before run.sh
# preflight runs.
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
exp_dir="$here/.."
module_root="$(cd "$here/../../../.." && pwd)"

fail=0
report() { if [ "$1" -eq 0 ]; then echo "PASS  $2"; else echo "FAIL  $2"; fail=1; fi; }

tmpdir=$(mktemp -d)
trap "rm -rf '$tmpdir'" EXIT

clean_repo="$tmpdir/clean_repo"
git init -q "$clean_repo"
git -C "$clean_repo" -c user.email=t@t -c user.name=t commit --allow-empty -m init -q

fake_bin="$tmpdir/bin"
mkdir -p "$fake_bin"
cat > "$fake_bin/codex" <<'CODEX'
#!/bin/sh
echo "IMPOSSIBLE: wrapper invoked codex (preflight must NEVER exec it)" >&2
exit 99
CODEX
chmod +x "$fake_bin/codex"
export PATH="$fake_bin:$PATH"
export LOOM_CODEX_CONFIG_PATH="$here/fixtures/codex_config/01_route_a_only.toml"

wrapper="$exp_dir/cross_device_code_mod.sh"
if [ ! -x "$wrapper" ]; then
  echo "SKIP: $wrapper not yet created"
  exit 0
fi

out=$(ALLOW_FULL_RUN=1 WORKTREE_ROOT="$clean_repo" LOOM_FULLTABLE_DISPATCH_SHIM=1 \
    bash "$wrapper" --sample 1 2>&1)
rc=$?
if [ "$rc" -ne 0 ]; then
  report 1 "cross_device wrapper dispatch-shim preflight (rc=$rc)"
  echo "$out"
else
  report 0 "cross_device wrapper dispatch-shim preflight passes"
fi

untracked=$(cd "$module_root" && git status --short -- tests/eval/results/experiments/ 2>/dev/null | wc -l)
if [ "$untracked" -gt 0 ]; then
  report 1 "cross_device wrapper left $untracked untracked files under experiments/"
  cd "$module_root" && git status --short -- tests/eval/results/experiments/
else
  report 0 "cross_device wrapper left no untracked experiments/ files (gitignored)"
fi

exit $fail
