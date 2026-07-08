#!/usr/bin/env bash
# Plan-review r8 P1 — run.sh --results-root MUST reject symlink prefix
# escapes to /tmp or $HOME/.codex. Base under ALLOWLISTED root (NOT
# /tmp) so canonicalization regression is caught, not vacuous.
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
module_root="$(cd "$here/../../../.." && pwd)"
run_sh="$module_root/tools/eval/fulltable/run.sh"

base="$module_root/tests/eval/results/experiments/_symlink_test_sh_$$"
mkdir -p "$base"
trap "rm -rf '$base'" EXIT

fake_home="$base/fake_home"
mkdir -p "$fake_home/.codex"

case "$base" in
  /tmp/*) echo "SETUP FAIL: base $base under /tmp"; exit 2 ;;
esac

fail=0
report() { if [ "$1" -eq 0 ]; then echo "PASS  $2"; else echo "FAIL  $2"; fail=1; fi; }

# Case 1: symlink → /tmp, missing-child path via it.
ln -s /tmp "$base/link_to_tmp"
candidate1="$base/link_to_tmp/missing/deep"
case "$candidate1" in
  /tmp/*) echo "SETUP FAIL: candidate1 under /tmp"; exit 2 ;;
esac
if HOME="$fake_home" bash "$run_sh" --results-root "$candidate1" \
    --workload cross-device-code-mod --dry-run 2>"$base/case1.err"; then
  report 1 "case1: symlink→/tmp escape accepted"
  cat "$base/case1.err"
else
  report 0 "case1: symlink→/tmp escape rejected"
fi

# Case 2: symlink → $FAKE_HOME/.codex.
ln -s "$fake_home/.codex" "$base/link_to_codex"
candidate2="$base/link_to_codex/missing"
case "$candidate2" in
  /tmp/*) echo "SETUP FAIL: candidate2 under /tmp"; exit 2 ;;
esac
if HOME="$fake_home" bash "$run_sh" --results-root "$candidate2" \
    --workload cross-device-code-mod --dry-run 2>"$base/case2.err"; then
  report 1 "case2: symlink→\$HOME/.codex escape accepted"
  cat "$base/case2.err"
else
  report 0 "case2: symlink→\$HOME/.codex escape rejected"
fi

exit $fail
