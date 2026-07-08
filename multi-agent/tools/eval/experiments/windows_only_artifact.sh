#!/usr/bin/env bash
# tools/eval/experiments/cross_device_code_mod.sh
# Per-workload runner for windows-only-artifact.
# Pins workload_id. Forwards to tools/eval/fulltable/run.sh.
set -euo pipefail

WORKLOAD="windows-only-artifact"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
common="$here/_common.sh"
module_root="$(cd "$here/../../.." && pwd)"
run_sh="$module_root/tools/eval/fulltable/run.sh"

# shellcheck source=/dev/null
source "$common"

usage() {
  cat <<EOF >&2
Usage: $(basename "$0") [--dry-run] [--sample N] [--results-root <abs-dir>]

Per-workload runner for ${WORKLOAD}. Pins workload id; forwards other
flags to tools/eval/fulltable/run.sh --workload ${WORKLOAD}.

Preflight (fatal on failure):
  1. codex binary on \$PATH (via command -v; never invokes codex)
  2. ~/.codex/config.toml readable

Not accepted (rejected with exit 2):
  --workload, --filter-workload (pinned by this wrapper)
EOF
}

# Reject caller-supplied --workload / --filter-workload BEFORE any
# other processing (spec §4.4 P0#3 wrapper-side belt).
skip_if_not_windows=0
new_args=()
for arg in "$@"; do
  case "$arg" in
    --workload|--workload=*|--filter-workload|--filter-workload=*)
      die "wrapper:$WORKLOAD: --workload / --filter-workload is pinned; remove from argv"
      ;;
    -h|--help) usage; exit 0 ;;
    --skip-if-not-windows) skip_if_not_windows=1 ;;
    *) new_args+=("$arg") ;;
  esac
done
# Strip --skip-if-not-windows before forwarding to run.sh (run.sh
# doesn't know that flag).
set -- ${new_args[@]+"${new_args[@]}"}

# Guard: on non-Windows, either honor the opt-out and exit 0, or fail.
if ! require_windows_host; then
  if [ "$skip_if_not_windows" -eq 1 ]; then
    warn_and_exit_zero "wrapper:$WORKLOAD: not a Windows host (uname=$(uname -s)); --skip-if-not-windows honored, exit 0"
  else
    die "wrapper:$WORKLOAD: requires a Windows host (uname=$(uname -s)); pass --skip-if-not-windows to acknowledge and exit 0"
  fi
fi

# Preflight
codex_bin_present || die "wrapper:$WORKLOAD: codex binary not on \$PATH; install via 'npm i -g @openai/codex'"
codex_config_readable || die "wrapper:$WORKLOAD: ~/.codex/config.toml not readable"

# Default results-root: per-invocation subdir under module-root.
# Do NOT mkdir here (plan-review r5 P1 — dirties tree before run.sh
# preflight). Compute only; run.sh creates lazily after preflight.
# The tests/eval/results/experiments/ dir is gitignored (Task 13.5).
default_root_base="$module_root/tests/eval/results/experiments/$WORKLOAD"
subdir="$(date -u +%Y-%m-%dT%H-%M-%SZ)-$$"
default_results_root="$default_root_base/$subdir"

have_results_root=0
for arg in "$@"; do
  case "$arg" in --results-root|--results-root=*) have_results_root=1 ;; esac
done

echo "[wrapper:$WORKLOAD] preflight OK" >&2
if [ "$have_results_root" -eq 0 ]; then
  echo "[wrapper:$WORKLOAD] results-root: $default_results_root" >&2
  exec bash "$run_sh" --workload "$WORKLOAD" --results-root "$default_results_root" "$@"
else
  echo "[wrapper:$WORKLOAD] results-root: <caller-supplied>" >&2
  exec bash "$run_sh" --workload "$WORKLOAD" "$@"
fi
