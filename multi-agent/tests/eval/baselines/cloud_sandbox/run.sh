#!/usr/bin/env bash
# cloud_sandbox baseline entrypoint. See ../manual_ssh/run.sh for the
# shared shape. Two extra guards:
#  1. spec §7(h) — if CI=true and neither --dry-run nor
#     --container-codex is present in args, refuse the run here (the Go
#     binary also enforces, but bailing at the shell level makes the
#     intent visible in the smoke recipe).
#  2. --forward-e2b-api-key requires the operator to have set the env
#     var named by --e2b-api-key-env; the Go layer handles the empty
#     case cleanly (Bearer header omitted) but a real run will fail
#     against E2B without the key.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
module_root="$(cd "$here/../../../.." && pwd)"

if [[ -x /tmp/cloud_sandbox ]]; then
  runner=(/tmp/cloud_sandbox)
else
  runner=(go run "./tests/eval/baselines/cloud_sandbox")
fi

# §7(h) shell-level guard
have_dry_run=0
have_container_codex=0
for arg in "$@"; do
  case "$arg" in
    --dry-run|--dry-run=*) have_dry_run=1 ;;
    --container-codex|--container-codex=*) have_container_codex=1 ;;
  esac
done
if [[ "${CI:-}" == "true" && "$have_dry_run" -eq 0 && "$have_container_codex" -eq 0 ]]; then
  echo "cloud_sandbox/run.sh: CI=true detected and neither --dry-run nor --container-codex was passed; refusing (spec §7(h))" >&2
  exit 2
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
  args+=("--out" "/tmp/cloud_sandbox-${workload:-run}.csv")
fi
if [[ "$have_workload_dir" -eq 0 ]]; then
  args+=("--workload-dir" "$module_root/tests/eval/workloads")
fi

cd "$module_root"
exec "${runner[@]}" run "${args[@]}"
