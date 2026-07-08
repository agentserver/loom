#!/usr/bin/env bash
# Task 13 — credential_bound_model.sh wrapper preflight against 10
# fixture TOML files. Verifies spec §4.5 outcome table + secret-non-leak.
#
# Skipped gracefully if credential_bound_model.sh does not yet exist
# (Task 18 creates it).
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
module_root="$(cd "$here/../../../.." && pwd)"
wrapper="$module_root/tools/eval/experiments/credential_bound_model.sh"
fixtures="$here/fixtures/codex_config"

if [ ! -x "$wrapper" ]; then
  echo "SKIP: $wrapper not yet created (Task 18)"
  exit 0
fi

fail=0
report() {
  if [ "$1" -eq 0 ]; then
    echo "PASS  $2"
  else
    echo "FAIL  $2"
    fail=1
  fi
}

tmpdir=$(mktemp -d)
trap "rm -rf '$tmpdir'" EXIT

# Fake codex on PATH so codex_bin_present passes without invoking it.
fake_bin="$tmpdir/bin"
mkdir -p "$fake_bin"
touch "$fake_bin/codex"; chmod +x "$fake_bin/codex"

run_preflight() {
  local fixture="$1"
  PATH="$fake_bin:$PATH" \
    LOOM_CODEX_CONFIG_PATH="$fixture" \
    LOOM_FULLTABLE_WRAPPER_SHIM=1 \
    bash "$wrapper" --dry-run 2>&1
}

# Case 1 — route-a-only → exit 0
out1=$(run_preflight "$fixtures/01_route_a_only.toml"); rc=$?
[ "$rc" -eq 0 ] && report 0 "01 route-a-only → exit 0" || { report 1 "01 (rc=$rc)"; echo "$out1"; }

# Case 2 — route-b-only, env set → exit 2 (unsupported)
out2=$(SOME_FIXTURE_ENV_NAME=xyz run_preflight "$fixtures/02_route_b_only_env_set.toml"); rc=$?
[ "$rc" -eq 2 ] && report 0 "02 route-b-only env-set → exit 2 unsupported" || { report 1 "02 (rc=$rc)"; echo "$out2"; }

# Case 3 — route-b-only, env unset → exit 2
out3=$(run_preflight "$fixtures/03_route_b_only_env_unset.toml"); rc=$?
[ "$rc" -eq 2 ] && report 0 "03 route-b-only env-unset → exit 2 unsupported" || { report 1 "03 (rc=$rc)"; echo "$out3"; }

# Case 4 — both → exit 0, informational route-b-detected msg
out4=$(run_preflight "$fixtures/04_both.toml"); rc=$?
if [ "$rc" -eq 0 ] && echo "$out4" | grep -q "route_b detected"; then
  report 0 "04 both → exit 0 with route-b-detected note"
else
  report 1 "04 (rc=$rc); no note?"
  echo "$out4"
fi

# Case 5 — neither → exit 2
out5=$(run_preflight "$fixtures/05_neither.toml"); rc=$?
[ "$rc" -eq 2 ] && report 0 "05 neither → exit 2" || { report 1 "05 (rc=$rc)"; echo "$out5"; }

# Case 6 — route-a commented → exit 2
out6=$(run_preflight "$fixtures/06_route_a_commented.toml"); rc=$?
[ "$rc" -eq 2 ] && report 0 "06 route-a commented → exit 2 (not counted)" || { report 1 "06 (rc=$rc)"; echo "$out6"; }

# Case 7 — route-a in different table → exit 2
out7=$(run_preflight "$fixtures/07_route_a_wrong_table.toml"); rc=$?
[ "$rc" -eq 2 ] && report 0 "07 route-a wrong-table → exit 2" || { report 1 "07 (rc=$rc)"; echo "$out7"; }

# Case 8 — malformed TOML → exit 2 with parse-error msg + no sk- leak
out8=$(run_preflight "$fixtures/08_malformed.toml"); rc=$?
if [ "$rc" -eq 2 ] && echo "$out8" | grep -q "could not parse"; then
  report 0 "08 malformed → exit 2 with parse-error message"
else
  report 1 "08 malformed (rc=$rc)"; echo "$out8"
fi
if echo "$out8" | grep -q "sk-"; then
  report 1 "SECURITY 08 → stderr contains sk-*"
else
  report 0 "08 malformed → no sk- leak"
fi

# Case 9 — duplicate tables → exit 2 with parse-error msg
out9=$(run_preflight "$fixtures/09_duplicate_tables.toml"); rc=$?
if [ "$rc" -eq 2 ] && echo "$out9" | grep -q "could not parse"; then
  report 0 "09 duplicate tables → exit 2 with parse-error message"
else
  report 1 "09 duplicate (rc=$rc)"; echo "$out9"
fi

# Case 10 — token-shaped value → exit 0 AND no leak
out10=$(run_preflight "$fixtures/10_token_shaped_value.toml"); rc=$?
[ "$rc" -eq 0 ] && report 0 "10 token-shaped → exit 0" || { report 1 "10 (rc=$rc)"; echo "$out10"; }
if echo "$out10" | grep -qE "sk-TEST-NOT-REAL-abc123leakcheck|leakcheckXYZ"; then
  report 1 "SECURITY 10 → stderr LEAKED token value"
  echo "$out10"
else
  report 0 "10 token-shaped → NO leak in stderr"
fi

exit $fail
