#!/usr/bin/env bash
# Oracle for motivation-e2e (scaffold self-check workload).
# Contract (13号 §1):
#   Args:   $1 = workspace dir
#   Stdout: one JSON line {"passed":bool,"details":{...},"metrics":{...}}
#   Exit:   0 pass, non-zero fail.
#
# The oracle checks that the three trace outputs the p3-mini-case chain
# consumes are present. It does NOT sample the four motivation numbers —
# that's done by the collectors in tests/eval/motivation/ from the main
# experiment metric dir (see spec §1 唯一数据源约定).
set -u

ws="${1:-}"
if [[ -z "$ws" || ! -d "$ws" ]]; then
  printf '{"passed":false,"details":{"reason":"workspace missing"},"metrics":{}}\n'
  exit 2
fi

route="$ws/route_reasons.sqlite"
steps="$ws/steps.log"
e4="$ws/e4_stages.csv"

details=()
passed=true

if [[ ! -s "$route" ]]; then
  details+=('"route_reasons.sqlite":"missing or empty"')
  passed=false
fi
if [[ ! -s "$steps" ]]; then
  details+=('"steps.log":"missing or empty"')
  passed=false
fi
if [[ ! -s "$e4" ]]; then
  details+=('"e4_stages.csv":"missing or empty"')
  passed=false
fi

route_size=0
[[ -f "$route" ]] && route_size=$(wc -c < "$route" | tr -d ' ')
steps_lines=0
[[ -f "$steps" ]] && steps_lines=$(grep -cE '^(ssh|scp|rsync|mkdir|cd) ' "$steps" || true)

if [[ "$passed" == true ]]; then
  details+=('"reason":"all trace outputs present"')
fi

details_joined=$(IFS=,; echo "${details[*]}")
printf '{"passed":%s,"details":{%s},"metrics":{"route_reasons_size_bytes":%d,"steps_log_line_count":%d}}\n' \
  "$passed" "$details_joined" "$route_size" "$steps_lines"

if [[ "$passed" == true ]]; then exit 0; else exit 1; fi
