#!/usr/bin/env bash
# Task 14-18 — per-wrapper: preflight passes + forwards --workload=<id>
# to run.sh via LOOM_FULLTABLE_WRAPPER_SHIM=1 test seam. Also asserts
# each wrapper REJECTS caller-supplied --workload / --filter-workload
# (spec §4.4 P0#3 belt).
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
exp_dir="$here/.."
module_root="$(cd "$here/../../../.." && pwd)"

fail=0
report() { if [ "$1" -eq 0 ]; then echo "PASS  $2"; else echo "FAIL  $2"; fail=1; fi; }

wrappers=(
  "cross_device_code_mod.sh:cross-device-code-mod:"
  "missing_parser_converter.sh:missing-parser-converter:"
  "remote_data_processing.sh:remote-data-processing:"
  "windows_only_artifact.sh:windows-only-artifact:--skip-if-not-windows"
  "credential_bound_model.sh:credential-bound-model:"
)

# Fake codex on PATH so codex_bin_present passes.
tmpdir=$(mktemp -d)
trap "rm -rf '$tmpdir'" EXIT
fake_bin="$tmpdir/bin"
mkdir -p "$fake_bin"
# Plan-review Phase D P1: fake codex must FAIL LOUDLY if invoked.
# An empty-executable would exit 0 silently, hiding an accidental
# `codex --version` / `codex doctor` bug in a wrapper.
cat > "$fake_bin/codex" <<'CODEX'
#!/bin/sh
echo "IMPOSSIBLE: wrapper invoked codex (preflight must NEVER exec it)" >&2
exit 99
CODEX
chmod +x "$fake_bin/codex"
export PATH="$fake_bin:$PATH"
export LOOM_CODEX_CONFIG_PATH="$here/fixtures/codex_config/01_route_a_only.toml"

for entry in "${wrappers[@]}"; do
  wrapper="${entry%%:*}"
  rest="${entry#*:}"
  workload_id="${rest%%:*}"
  extra="${rest#*:}"
  script="$exp_dir/$wrapper"

  if [ ! -x "$script" ]; then
    echo "SKIP  $wrapper (not created yet)"
    continue
  fi

  # Case A: preflight + forward under WRAPPER_SHIM
  if [ "$wrapper" = "windows_only_artifact.sh" ]; then
    out=$(LOOM_FULLTABLE_WRAPPER_SHIM=1 bash "$script" --skip-if-not-windows --dry-run 2>&1)
    rc=$?
    [ "$rc" -eq 0 ] && report 0 "$wrapper preflight (skip on Linux OK)" \
                     || { report 1 "$wrapper preflight (rc=$rc)"; echo "$out"; }
  else
    out=$(LOOM_FULLTABLE_WRAPPER_SHIM=1 bash "$script" --dry-run 2>&1)
    rc=$?
    if [ "$rc" -ne 0 ]; then
      report 1 "$wrapper preflight (rc=$rc)"
      echo "$out"
    else
      report 0 "$wrapper preflight"
      if ! echo "$out" | grep -q "SHIM_WORKLOAD_FILTER: $workload_id"; then
        report 1 "$wrapper forwards --workload=$workload_id"
        echo "$out"
      else
        report 0 "$wrapper forwards --workload=$workload_id"
      fi
    fi
  fi

  # Case B: caller-supplied --workload → exit 2, "pinned" message
  out=$(LOOM_FULLTABLE_WRAPPER_SHIM=1 bash "$script" --dry-run --workload other-id 2>&1)
  rc=$?
  if [ "$rc" -eq 2 ] && echo "$out" | grep -q "pinned"; then
    report 0 "$wrapper rejects caller --workload"
  else
    report 1 "$wrapper rejects caller --workload (rc=$rc)"
    echo "$out"
  fi

  # Case B-equals: caller-supplied --workload=OTHER (equals form)
  out=$(LOOM_FULLTABLE_WRAPPER_SHIM=1 bash "$script" --dry-run --workload=other-id 2>&1)
  rc=$?
  if [ "$rc" -eq 2 ] && echo "$out" | grep -q "pinned"; then
    report 0 "$wrapper rejects caller --workload=X (equals form)"
  else
    report 1 "$wrapper rejects caller --workload=X (rc=$rc)"
    echo "$out"
  fi

  # Case C: caller-supplied --filter-workload → also exit 2
  out=$(LOOM_FULLTABLE_WRAPPER_SHIM=1 bash "$script" --dry-run --filter-workload other-id 2>&1)
  rc=$?
  if [ "$rc" -eq 2 ] && echo "$out" | grep -q "pinned"; then
    report 0 "$wrapper rejects caller --filter-workload"
  else
    report 1 "$wrapper rejects caller --filter-workload (rc=$rc)"
    echo "$out"
  fi

  # Case C-equals: caller-supplied --filter-workload=OTHER (equals form)
  out=$(LOOM_FULLTABLE_WRAPPER_SHIM=1 bash "$script" --dry-run --filter-workload=other-id 2>&1)
  rc=$?
  if [ "$rc" -eq 2 ] && echo "$out" | grep -q "pinned"; then
    report 0 "$wrapper rejects caller --filter-workload=X (equals form)"
  else
    report 1 "$wrapper rejects caller --filter-workload=X (rc=$rc)"
    echo "$out"
  fi
done

# Plan-review Phase D P1: verify NO wrapper output contains the
# "IMPOSSIBLE" sentinel — that would mean a wrapper accidentally
# invoked the fake codex during preflight.
echo "--- IMPOSSIBLE sentinel scan ---"
for entry in "${wrappers[@]}"; do
  wrapper="${entry%%:*}"
  script="$exp_dir/$wrapper"
  [ -x "$script" ] || continue
  if [ "$wrapper" = "windows_only_artifact.sh" ]; then
    out=$(LOOM_FULLTABLE_WRAPPER_SHIM=1 bash "$script" --skip-if-not-windows --dry-run 2>&1)
  else
    out=$(LOOM_FULLTABLE_WRAPPER_SHIM=1 bash "$script" --dry-run 2>&1)
  fi
  if echo "$out" | grep -q "IMPOSSIBLE"; then
    report 1 "SECURITY $wrapper: preflight invoked codex (IMPOSSIBLE sentinel present)"
    echo "$out"
  else
    report 0 "$wrapper preflight did NOT invoke codex"
  fi
done

exit $fail
