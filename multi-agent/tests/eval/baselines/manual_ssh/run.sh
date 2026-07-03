#!/usr/bin/env bash
# manual_ssh baseline entrypoint. Wraps the compiled Go binary so the
# 15-run smoke can invoke each baseline with a uniform `bash run.sh
# --workload <id> [--dry-run]` shape.
#
# Contract: --out defaults to /tmp/manual_ssh-<workload>.csv when the
# caller doesn't provide one (the smoke matrix supplies its own path).
# --workload-dir defaults to the workloads dir relative to this repo
# root so callers can `cd multi-agent && bash tests/eval/baselines/manual_ssh/run.sh`.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
module_root="$(cd "$here/../../../.." && pwd)"

# Prefer a prebuilt binary at /tmp/manual_ssh (CI convention); otherwise
# `go run` from the module root.
if [[ -x /tmp/manual_ssh ]]; then
  runner=(/tmp/manual_ssh)
else
  runner=(go run "./tests/eval/baselines/manual_ssh")
fi

args=()
have_out=0
have_workload_dir=0
have_workload=0
workload=""
for arg in "$@"; do
  case "$arg" in
    --out=*)         have_out=1 ;;
    --out)           have_out=1 ;;
    --workload-dir=*) have_workload_dir=1 ;;
    --workload-dir)   have_workload_dir=1 ;;
    --workload=*)    have_workload=1; workload="${arg#--workload=}" ;;
    --workload)      have_workload=1 ;;
  esac
  # Capture the value that follows --workload / --out shortly after the
  # flag when supplied space-separated.
  args+=("$arg")
done

# If --workload was passed space-separated, its value is at args[i+1].
if [[ "$have_workload" -eq 1 && -z "$workload" ]]; then
  for ((i = 0; i < ${#args[@]}; i++)); do
    if [[ "${args[$i]}" == "--workload" ]]; then
      workload="${args[$((i + 1))]:-}"
      break
    fi
  done
fi

if [[ "$have_out" -eq 0 ]]; then
  args+=("--out" "/tmp/manual_ssh-${workload:-run}.csv")
fi
if [[ "$have_workload_dir" -eq 0 ]]; then
  args+=("--workload-dir" "$module_root/tests/eval/workloads")
fi

cd "$module_root"
exec "${runner[@]}" run "${args[@]}"
