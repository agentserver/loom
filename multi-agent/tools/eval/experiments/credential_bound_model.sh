#!/usr/bin/env bash
# tools/eval/experiments/credential_bound_model.sh
# Per-workload runner for credential-bound-model.
# Pins workload_id. Forwards to tools/eval/fulltable/run.sh.
set -euo pipefail

WORKLOAD="credential-bound-model"
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

# Additional preflight for credential-bound-model per spec §4.4
# (route-b unsupported in this PR).
#
# Plan-review r3 P1 fix: under `set -euo pipefail`, the form
# `route_a=$(cmd); route_a_rc=$?` triggers set -e on any non-zero exit
# from cmd BEFORE the second statement runs. Must use if/then/else.
if route_a=$(codex_config_has_route_a); then route_a_rc=0; else route_a_rc=$?; fi
if route_b=$(codex_config_has_route_b); then route_b_rc=0; else route_b_rc=$?; fi

# Helpers return `error` + rc=2 on parse failure. Die BEFORE the
# route logic to surface the parse failure clearly. Only when BOTH
# helpers succeeded (rc 0 or 1) do we apply route-a-required.
if [ "$route_a_rc" -eq 2 ] || [ "$route_b_rc" -eq 2 ]; then
  die "wrapper:$WORKLOAD: could not parse ~/.codex/config.toml (route_a=$route_a rc=$route_a_rc; route_b=$route_b rc=$route_b_rc); check file exists, is TOML-valid, and has at most one [model_providers.modelserver] table."
fi

# Log states only (never values / names)
echo "[wrapper:$WORKLOAD] route_a: $route_a, route_b: $route_b" >&2

# Route-a required, route-b unsupported.
if [ "$route_a" = "present" ]; then
  if [ "$route_b" = "present" ]; then
    echo "[wrapper:$WORKLOAD] note: route_b detected in ~/.codex/config.toml but route_a will be used (route_b support deferred; see docs/specs/wt4-codex-only.handoff.md §Route-b support)" >&2
  fi
elif [ "$route_b" = "present" ]; then
  die "wrapper:$WORKLOAD: route_b is present in ~/.codex/config.toml [model_providers.modelserver] but is not supported by this baseline yet; route_a (experimental_bearer_token) is required. See docs/specs/wt4-codex-only.handoff.md §Route-b support."
else
  die "wrapper:$WORKLOAD: credential-bound-model requires route (a) [experimental_bearer_token under model_providers.modelserver] in ~/.codex/config.toml. Route (b) support is deferred; see handoff."
fi

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
