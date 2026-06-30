#!/usr/bin/env bash
# Oracle for cross-device-code-mod.
# Args: $1 = workspace dir (where the workload wrote outputs)
# Stdout: one JSON line {"passed": bool, "details": {...}, "metrics": {...}}
# Exit: 0 on pass, non-zero on fail.
set -u

ws="${1:-}"
if [[ -z "$ws" || ! -d "$ws" ]]; then
  printf '{"passed":false,"details":{"reason":"workspace missing"},"metrics":{}}\n'
  exit 2
fi

patch="$ws/patch.diff"
log="$ws/test.log"
details=()
passed=true

if [[ ! -s "$patch" ]]; then
  details+=('"patch":"missing or empty"')
  passed=false
else
  # Must look like a real unified diff:
  #   1. at least one file header (`diff --git `, `--- `, or `+++ `)
  #   2. at least one hunk header (`^@@ ... @@`)
  #   3. at least one change line (`^+x` or `^-x` where x is not + or -)
  # The earlier version accepted a stub containing only a `+++ b/foo` line,
  # which lets a model satisfy the artifact check with no real change.
  has_header=false
  has_hunk=false
  has_change=false
  grep -qE '^(diff --git |--- |\+\+\+ )' "$patch" && has_header=true
  grep -qE '^@@ ' "$patch" && has_hunk=true
  grep -qE '^[+-][^+-]' "$patch" && has_change=true
  if ! $has_header; then
    details+=('"patch":"no diff header"')
    passed=false
  elif ! $has_hunk; then
    details+=('"patch":"no hunk header"')
    passed=false
  elif ! $has_change; then
    details+=('"patch":"no content lines"')
    passed=false
  else
    details+=('"patch":"ok"')
  fi
fi

if [[ ! -s "$log" ]]; then
  details+=('"test_log":"missing or empty"')
  passed=false
else
  # Look for a pass marker the synthetic suite must emit, anchored to the
  # start of a line so we don't false-positive on prose like
  # "5 passed, 3 failed" or "looks ok? no".  Accepted shapes mirror real
  # test-runner output (Go `go test`, pytest summary, ctest):
  #   ^PASS$ / ^PASS<space|tab>...   -- go test package summary
  #   ^ok<space|tab>...               -- go test "ok  pkg  0.001s"
  #   ^--- PASS:                       -- go test verbose
  #   ^=+ N passed                     -- pytest summary
  # AND we reject if any explicit failure marker is present.
  #
  # The "N failed" alternations explicitly require N >= 1.  We accept a
  # zero-padded prefix (`0*[1-9][0-9]*`) so `01 failed` is still flagged
  # while `00 failed` / `0 failed` continue to pass.  See
  # TestCrossDeviceCodeMod_AcceptsZeroFailedSummary (round 2) and
  # TestCrossDeviceCodeMod_HandlesZeroPaddedFailedCount (R3-S1).
  #
  # Round 3 (R3-M1): the round-2 regex missed four real-world failure
  # shapes — jest `Tests:.*N failed,...`, mocha bare-line `N failing`,
  # GNU `make: *** [target] Error N`, and `^Traceback (most recent call last):`.
  # Each is anchored at start-of-line so "no errors here" prose can't
  # false-positive.  See TestCrossDeviceCodeMod_RejectsAdditionalFailureShapes.
  #
  # R6-M2: the bare `[[:space:]]*0*[1-9][0-9]*[[:space:]]+failed` and
  # `... failing` branches added in round 3 were too greedy — they matched
  # natural prose like `  3 failed allocations recovered, all passing now`
  # and `\t1 failed retry succeeded`.  Tighten each prose-friendly branch:
  #   * mocha-failing: require the whole indented line to END at `failing`
  #     so "  1 failing" matches but "We noted 5 failing services" doesn't
  #     (line continues past `failing`).
  #   * bare-line failed: split into TWO branches — one requiring the line
  #     to end at `failed` (`bare summary "1 failed"`) and one requiring a
  #     comma right after `failed` (`"1 failed, 4 passed"` summary).  Both
  #     reject prose `3 failed allocations recovered` because `allocations`
  #     is neither EOL nor a comma.
  #
  # R7-M2: R6-M2 narrowed `ERROR\b` to `ERROR$`, which killed real failure
  # shapes (`ERROR: connection refused`, `ERROR\tTestFoo failed`, bare
  # `FAILED`, bare `FAILURE`, pytest one-line summary `1 failed in 0.50s`).
  # Add explicit anchored arms for each shape (line-anchored, so prose
  # `no ERROR here` mid-sentence still won't trip).
  # R7-N1: relax the mocha-failing leading whitespace from `[[:space:]]+`
  # to `[[:space:]]*` so column-zero `1 failing` also trips.  The `$`
  # anchor still rejects `5 failing services prior to the fix` prose.
  # R7-M3 (pass arm only): the pytest-banner pass arm previously used
  # `[0-9]+` for the count, which accepted `==== 0 passed in 0.01s ====`
  # — asymmetric with the failure side which uses `0*[1-9][0-9]*`.
  # Tighten the pass-banner count to `0*[1-9][0-9]*` so a 0-passed banner
  # falls through to the "no pass marker" branch.  The other pass arms
  # (`^PASS$`, `^PASS<sp>`, `^ok<sp>`, `^--- PASS:`) are not count-based
  # and stay as-is.
  #
  # Covered failure shapes after R7-M2/N1 broadening:
  #   ^--- FAIL                       go test verbose
  #   ^FAIL$                            bare FAIL marker
  #   ^FAIL[[:space:]:]                 FAIL: TestX / FAIL pkg
  #   ^FAILED$                          bare FAILED marker          (R7-M2)
  #   ^FAILURE$                         bare FAILURE marker         (R7-M2)
  #   ^=+ N failed                      pytest banner (N >= 1)
  #   ^Tests:.* N failed                jest summary (N >= 1)
  #   ^space* N failing<EOL>            mocha "1 failing" or "  1 failing"
  #   ^space* N failed<EOL>             bare "1 failed" line
  #   ^space* N failed,                 bare "1 failed, 4 passed" line
  #   ^space* N failed in <space>       pytest one-line summary    (R7-M2)
  #   ^--- ERROR                        go test verbose ERROR
  #   ^ERROR$                            bare ERROR marker
  #   ^ERROR[:[:space:]]                ERROR: ... / ERROR\t...    (R7-M2)
  #   ^make[...] *** ... Error N        GNU make non-zero error
  #   ^Traceback (most recent call last):  python traceback header
  # R8-S2: GNU grep `$` matches before `\n` but does not consume `\r`, so a
  # CRLF-terminated `FAILED\r\n` was not matched by `^FAILED$` and slipped
  # through.  Allow trailing whitespace (`[[:space:]]*$`) on the bare
  # FAILED/FAILURE arms so CRLF logs from Windows-style writers are still
  # rejected.  The other failure arms either already permit trailing slack
  # via `[[:space:]]*$` (mocha-failing, bare-failed) or include a literal
  # character after the marker that defangs `\r` (comma, `in `).
  if grep -qE '^(--- FAIL|FAIL$|FAIL[[:space:]:]|FAILED[[:space:]]*$|FAILURE[[:space:]]*$|=+[[:space:]]+0*[1-9][0-9]*[[:space:]]+failed|Tests:.*[[:space:]]0*[1-9][0-9]*[[:space:]]+failed|[[:space:]]*0*[1-9][0-9]*[[:space:]]+failing[[:space:]]*$|[[:space:]]*0*[1-9][0-9]*[[:space:]]+failed[[:space:]]*$|[[:space:]]*0*[1-9][0-9]*[[:space:]]+failed[[:space:]]*,|[[:space:]]*0*[1-9][0-9]*[[:space:]]+failed[[:space:]]+in[[:space:]]|--- ERROR|ERROR$|ERROR[:[:space:]]|make(\[[0-9]+\])?.*Error[[:space:]]+0*[1-9][0-9]*|Traceback[[:space:]]\(most[[:space:]]recent[[:space:]]call[[:space:]]last\):)' "$log"; then
    details+=('"test_log":"failure marker present"')
    passed=false
  elif grep -qE '^(PASS$|PASS[[:space:]]|ok[[:space:]]|--- PASS:|=+[[:space:]]+0*[1-9][0-9]*[[:space:]]+passed)' "$log"; then
    details+=('"test_log":"pass marker found"')
  else
    details+=('"test_log":"no pass marker"')
    passed=false
  fi
fi

# Tiny metric: lines added in patch (rough churn proxy).
added=0
if [[ -s "$patch" ]]; then
  added=$(grep -cE '^\+[^+]' "$patch" || true)
fi

joined=$(IFS=, ; echo "${details[*]}")
if $passed; then
  printf '{"passed":true,"details":{%s},"metrics":{"patch_lines_added":%d}}\n' "$joined" "$added"
  exit 0
fi
printf '{"passed":false,"details":{%s},"metrics":{"patch_lines_added":%d}}\n' "$joined" "$added"
exit 1
