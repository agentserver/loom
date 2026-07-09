#!/usr/bin/env bash
# Oracle for public-terminal-heterogeneous-dates.
# Args: $1 = workspace dir.
# Stdout: one JSON line {"passed": bool, "details": {...}, "metrics": {...}}
set -u

ws="${1:-}"
if [[ -z "$ws" || ! -d "$ws" ]]; then
  printf '{"passed":false,"details":{"reason":"workspace missing"},"metrics":{}}\n'
  exit 2
fi

result="$ws/avg_temp.txt"
expected="11.428571428571429"
details=()
passed=true

if [[ ! -s "$result" ]]; then
  details+=('"avg_temp":"missing"')
  passed=false
fi

value=""
if [[ -s "$result" ]]; then
  value="$(tr -d '[:space:]' < "$result")"
  if [[ ! "$value" =~ ^-?[0-9]+([.][0-9]+)?$ ]]; then
    details+=('"format":"non_numeric"')
    passed=false
  else
    verdict="$(awk -v got="$value" -v want="$expected" 'BEGIN {
      got3 = sprintf("%.3f", got + 0)
      want3 = sprintf("%.3f", want + 0)
      if (got3 == want3) {
        print "matches"
      } else {
        print "mismatch"
      }
    }')"
    details+=("\"avg_temp\":\"$verdict\"")
    if [[ "$verdict" != "matches" ]]; then
      passed=false
    fi
  fi
fi

size=0
if [[ -s "$result" ]]; then
  size=$(stat -c %s "$result" 2>/dev/null || wc -c < "$result" 2>/dev/null || echo 0)
  if [[ "$size" =~ ^[[:space:]]*([0-9]+)[[:space:]]*$ ]]; then
    size="${BASH_REMATCH[1]}"
  else
    size=0
  fi
fi

joined=$(IFS=, ; echo "${details[*]}")
if $passed; then
  printf '{"passed":true,"details":{%s},"metrics":{"result_bytes":%d}}\n' "$joined" "$size"
  exit 0
fi
printf '{"passed":false,"details":{%s},"metrics":{"result_bytes":%d}}\n' "$joined" "$size"
exit 1
