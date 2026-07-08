#!/usr/bin/env bash
# Spec §4.3 P1#1 — --workload interacts correctly with --sample / --resume.
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
module_root="$(cd "$here/../../../.." && pwd)"
run_sh="$module_root/tools/eval/fulltable/run.sh"

fail=0
report() { if [ "$1" -eq 0 ]; then echo "PASS  $2"; else echo "FAIL  $2"; fail=1; fi; }

# 1) --workload X --dry-run under WRAPPER_SHIM → SHIM_WORKLOAD_FILTER + exit 0
out=$(LOOM_FULLTABLE_WRAPPER_SHIM=1 bash "$run_sh" --workload cross-device-code-mod --dry-run 2>&1)
rc=$?
if [ "$rc" -eq 0 ] && echo "$out" | grep -q "^SHIM_WORKLOAD_FILTER: cross-device-code-mod$"; then
  report 0 "case1: WRAPPER_SHIM --workload --dry-run → SHIM line"
else
  report 1 "case1: WRAPPER_SHIM --workload --dry-run (rc=$rc)"
  echo "$out"
fi

# 2) --workload X --sample 2 under WRAPPER_SHIM → SHIM_WORKLOAD_FILTER present
out=$(LOOM_FULLTABLE_WRAPPER_SHIM=1 bash "$run_sh" --workload cross-device-code-mod --sample 2 --dry-run 2>&1)
rc=$?
if [ "$rc" -eq 0 ] && echo "$out" | grep -q "SHIM_WORKLOAD_FILTER"; then
  report 0 "case2: WRAPPER_SHIM --workload --sample 2 --dry-run"
else
  report 1 "case2: (rc=$rc)"; echo "$out"
fi

# 3) SHIM without --dry-run: dual-guard active, SHIM path NOT taken
tmpdir=$(mktemp -d)
trap "rm -rf '$tmpdir'" EXIT

if LOOM_FULLTABLE_WRAPPER_SHIM=1 bash "$run_sh" --workload cross-device-code-mod --sample 1 >"$tmpdir/case3.out" 2>&1; then
  report 1 "case3: WRAPPER_SHIM without --dry-run should NOT bypass preflight"
  cat "$tmpdir/case3.out"
else
  report 0 "case3: WRAPPER_SHIM without --dry-run correctly does NOT bypass"
fi

# 3a) Plan-review Phase C P0: --workload '' MUST be rejected. Otherwise
# an explicit empty string bypasses filtering silently.
if bash "$run_sh" --workload '' --dry-run >"$tmpdir/case3a.out" 2>&1; then
  report 1 "case3a: --workload '' accepted; should exit 2"
  cat "$tmpdir/case3a.out"
else
  report 0 "case3a: --workload '' correctly rejected"
fi

# 3b) Plan-review Phase C P0: --workload regex '.*' MUST be rejected
# via EXACT match — grep -qx would treat this as a regex and accept.
if LOOM_FULLTABLE_WRAPPER_SHIM=1 bash "$run_sh" --workload '.*' --dry-run >"$tmpdir/case3b.out" 2>&1; then
  report 1 "case3b: --workload '.*' regex accepted; must be exact match rejection"
  cat "$tmpdir/case3b.out"
else
  report 0 "case3b: --workload '.*' regex correctly rejected (exact-match allowlist)"
fi

# 4) Non-SHIM --workload --dry-run → 12 lines
out=$(bash "$run_sh" --workload cross-device-code-mod --dry-run 2>&1)
rc=$?
if [ "$rc" -ne 0 ]; then
  report 1 "case4: non-shim exit=$rc"; echo "$out"
else
  lines=$(echo "$out" | grep -c "cross-device-code-mod" || true)
  if [ "$lines" -eq 12 ]; then
    report 0 "case4: non-shim --workload --dry-run → 12 lines"
  else
    report 1 "case4: expected 12 lines, got $lines"; echo "$out"
  fi
fi

# 5) Non-prefix workload — 12 lines (filter-before-sample proof)
out=$(bash "$run_sh" --workload credential-bound-model --dry-run 2>&1)
lines=$(echo "$out" | grep -c "credential-bound-model" || true)
if [ "$lines" -eq 12 ]; then
  report 0 "case5: non-prefix workload → 12 lines"
else
  report 1 "case5: expected 12, got $lines"; echo "$out"
fi

# 6) Non-shim planner sample subcommand — filter-first
alt_out=$(cd "$module_root" && PYTHONPATH="$module_root/tools/eval/fulltable" \
  python3 -m lib.plan \
    --matrix "$module_root/tools/eval/fulltable/matrix.yaml" \
    --smoke-root "tests/eval/results/smoke" \
    --filter-workload credential-bound-model \
    sample --n 2 2>&1)
alt_lines=$(echo "$alt_out" | grep -c "credential-bound-model" || true)
if [ "$alt_lines" -eq 2 ]; then
  report 0 "case6: planner sample --n 2 → 2 filtered rows"
else
  report 1 "case6: expected 2, got $alt_lines"; echo "$alt_out"
fi

# 7) Resume + E4-skip
alt_root="$module_root/tests/eval/results/experiments/_test_resume_$$"
mkdir -p "$alt_root/runs"
credbm_full_loom_rk="matrix__credential-bound-model__full_loom"
touch "$alt_root/runs/${credbm_full_loom_rk}__00000000-1111-2222-3333-444444444444.done"

clean_repo=$(mktemp -d)
git init -q "$clean_repo" >/dev/null 2>&1
git -C "$clean_repo" -c user.email=t@t -c user.name=t commit --allow-empty -m init -q >/dev/null 2>&1

shim_out=$(ALLOW_FULL_RUN=1 WORKTREE_ROOT="$clean_repo" LOOM_FULLTABLE_DISPATCH_SHIM=1 \
    bash "$run_sh" \
      --workload credential-bound-model \
      --results-root "$alt_root" \
      --sample 50 \
      --resume 2>&1)
shim_rc=$?
if [ "$shim_rc" -ne 0 ]; then
  report 1 "case7: dispatch-shim exit=$shim_rc"
  echo "$shim_out"
elif ! echo "$shim_out" | grep -qE "SHIM: would dispatch [0-9]+ rows"; then
  report 1 "case7: dispatch-shim did not reach dispatch-planning"
  echo "$shim_out"
else
  rows=$(echo "$shim_out" | grep -oE "would dispatch [0-9]+" | grep -oE "[0-9]+")
  # Plan-review Phase C P1: assert EXACTLY 11 (12 workload rows -
  # 1 sidecar-matched via ${rk}__*.done glob). Accepting 12 would
  # let a broken resume-sidecar skip pass silently.
  if [ "$rows" -ne 11 ]; then
    report 1 "case7: expected exactly 11 rows (12 workload minus 1 sidecar-matched); got $rows"
    echo "$shim_out"
  elif ! echo "$shim_out" | grep -q "resume: skip matrix__credential-bound-model__full_loom"; then
    report 1 "case7: expected stderr to contain 'resume: skip matrix__credential-bound-model__full_loom'"
    echo "$shim_out"
  else
    report 0 "case7: --workload --resume dispatched exactly 11 rows + skip line present"
  fi
fi
rm -rf "$alt_root" "$clean_repo"

exit $fail
