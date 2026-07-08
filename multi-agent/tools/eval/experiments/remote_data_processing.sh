#!/usr/bin/env bash
# tools/eval/experiments/remote_data_processing.sh
# Per-workload runner for remote-data-processing.
# Pins workload_id. Forwards to tools/eval/fulltable/run.sh.
set -euo pipefail

WORKLOAD="remote-data-processing"
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
for arg in "$@"; do
  case "$arg" in
    --workload|--workload=*|--filter-workload|--filter-workload=*)
      die "wrapper:$WORKLOAD: --workload / --filter-workload is pinned; remove from argv"
      ;;
    -h|--help) usage; exit 0 ;;
  esac
done

# Preflight
codex_bin_present || die "wrapper:$WORKLOAD: codex binary not on \$PATH; install via 'npm i -g @openai/codex'"
codex_config_readable || die "wrapper:$WORKLOAD: ~/.codex/config.toml not readable"
# Workload-specific: /tmp writable + workload spec present
require_writable_tmp || die "wrapper:$WORKLOAD: /tmp not writable"
require_fixture "$module_root/tests/eval/workloads/remote-data-processing/spec.yaml" \
  || die "wrapper:$WORKLOAD: workload spec.yaml missing at expected path"

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
