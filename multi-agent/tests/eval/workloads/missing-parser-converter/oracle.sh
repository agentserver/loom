#!/usr/bin/env bash
# Oracle for missing-parser-converter.
# Args: $1 = workspace dir.
set -u

ws="${1:-}"
if [[ -z "$ws" || ! -d "$ws" ]]; then
  printf '{"passed":false,"details":{"reason":"workspace missing"},"metrics":{}}\n'
  exit 2
fi

mcp="$ws/synthesized.mcp.json"
converted="$ws/converted.out"
log="$ws/acceptance.log"
# R9-S2: locate committed golden fixtures with bash path expansion rather
# than external `dirname` (not part of the eval-oracle tool floor).
script_dir="${BASH_SOURCE[0]%/*}"
if [[ "$script_dir" == "${BASH_SOURCE[0]}" ]]; then
  script_dir="."
fi
golden_dir="$script_dir/fixtures/golden"

details=()
passed=true

# 1) The MCP must expose a "convert" tool name.  Structural JSON validity is
#    asserted by the Go test layer (TestWorkloadJSONOutputsAreValid); we
#    deliberately don't take a python3/jq dependency for that here.
if [[ ! -s "$mcp" ]]; then
  details+=('"mcp":"missing"')
  passed=false
elif ! grep -qE '"name"[[:space:]]*:[[:space:]]*"convert"' "$mcp"; then
  details+=('"mcp":"no_convert_tool"')
  passed=false
else
  details+=('"mcp":"ok"')
fi

# 2) Converted output must match the golden expected.out byte-for-byte.
#    Single-case by design: one golden expected.out vs one converted.out.
#    Multi-case support is intentionally out of scope for this bootstrap;
#    the previous structure ran a per-file loop comparing every golden to
#    the same converted.out, which was structurally unsatisfiable when
#    cases_total > 1.  To add multi-case later: pair converted_<case>.out
#    with expected_<case>.out and iterate explicitly.
# R6-N2: cases_pass was assigned but never read after the R5 refactor —
# the gating shifted to pass_lines / fail_lines / cases_total.  Drop the
# dead variable; cases_total is still load-bearing (printed in metrics).
cases_total=1
golden_file="$golden_dir/expected.out"
if [[ ! -s "$golden_file" ]]; then
  details+=('"golden":"missing"')
  passed=false
elif [[ -s "$converted" ]] && cmp -s "$golden_file" "$converted"; then
  details+=('"golden":"1/1"')
else
  details+=('"golden":"0/1"')
  passed=false
fi

# 3) Acceptance log must show every golden case as PASS and contain
#    NOTHING ELSE.  The README has always promised "One PASS/FAIL line per
#    golden case"; the round-4 oracle counted PASS / FAIL lines but did not
#    reject other lines, so a mocha "  1 failing", a jest "Tests: 1 failed,
#    4 passed", or a python traceback silently slipped past as long as at
#    least one PASS line preceded it.  R5-M1: explicitly reject any line
#    not matching ^(PASS|FAIL)\b (this includes blank lines and
#    framework-summary chatter).  `grep -cv` counts NON-matching lines.
#    The canonical fixture ends each line in \n, so the trailing newline
#    after a PASS line does not introduce a phantom blank line in `grep`'s
#    line count.
if [[ ! -s "$log" ]]; then
  details+=('"acceptance_log":"missing"')
  passed=false
else
  non_canonical=$(grep -cvE '^(PASS|FAIL)\b' "$log" || true)
  pass_lines=$(grep -cE '^PASS\b' "$log" || true)
  fail_lines=$(grep -cE '^FAIL\b' "$log" || true)
  if [[ "$non_canonical" -gt 0 ]]; then
    details+=("\"acceptance_log\":\"$non_canonical non-canonical lines\"")
    passed=false
  elif [[ "$fail_lines" -gt 0 || "$pass_lines" -ne "$cases_total" ]]; then
    # R8-M2: strict equality (not -lt) so that pass_lines > cases_total
    # also rejects.  With cases_total=1, 3 PASS lines previously satisfied
    # `3 < 1 == false` and the oracle reported passed:true with
    # acceptance_log:"3 pass".  The README has always promised "one
    # PASS/FAIL line per golden case"; equality is the contract.
    details+=("\"acceptance_log\":\"$pass_lines pass / $fail_lines fail\"")
    passed=false
  else
    details+=("\"acceptance_log\":\"$pass_lines pass\"")
  fi
fi

joined=$(IFS=, ; echo "${details[*]}")
if $passed; then
  printf '{"passed":true,"details":{%s},"metrics":{"cases":%d}}\n' "$joined" "$cases_total"
  exit 0
fi
printf '{"passed":false,"details":{%s},"metrics":{"cases":%d}}\n' "$joined" "$cases_total"
exit 1
