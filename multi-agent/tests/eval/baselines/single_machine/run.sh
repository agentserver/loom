#!/usr/bin/env bash
# single_machine baseline entrypoint. See ../manual_ssh/run.sh for the
# shared shape; this wrapper only exists so the smoke matrix in
# docs/specs/wt2-baselines.spec.md §8 can invoke each baseline
# uniformly with `bash run.sh --workload <id> [--dry-run]`.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
module_root="$(cd "$here/../../../.." && pwd)"

if [[ -x /tmp/single_machine ]]; then
  runner=(/tmp/single_machine)
else
  runner=(go run "./tests/eval/baselines/single_machine")
fi

args=()
have_out=0
have_workload_dir=0
have_workload=0
workload=""
for arg in "$@"; do
  case "$arg" in
    --out=*|--out) have_out=1 ;;
    --workload-dir=*|--workload-dir) have_workload_dir=1 ;;
    --workload=*) have_workload=1; workload="${arg#--workload=}" ;;
    --workload)   have_workload=1 ;;
  esac
  args+=("$arg")
done
if [[ "$have_workload" -eq 1 && -z "$workload" ]]; then
  for ((i = 0; i < ${#args[@]}; i++)); do
    if [[ "${args[$i]}" == "--workload" ]]; then
      workload="${args[$((i + 1))]:-}"
      break
    fi
  done
fi
if [[ "$have_out" -eq 0 ]]; then
  args+=("--out" "/tmp/single_machine-${workload:-run}.csv")
fi
if [[ "$have_workload_dir" -eq 0 ]]; then
  args+=("--workload-dir" "$module_root/tests/eval/workloads")
fi

cd "$module_root"
exec "${runner[@]}" run "${args[@]}"
