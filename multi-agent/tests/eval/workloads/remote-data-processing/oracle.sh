#!/usr/bin/env bash
# Oracle for remote-data-processing.
# Args: $1 = workspace dir.
# Stdout: one JSON line {"passed": bool, "details": {...}, "metrics": {...}}
set -u

ws="${1:-}"
if [[ -z "$ws" || ! -d "$ws" ]]; then
  printf '{"passed":false,"details":{"reason":"workspace missing"},"metrics":{}}\n'
  exit 2
fi

result="$ws/result.json"
checksum_file="$ws/result.sha256"
# R9-S2: locate committed golden fixtures with bash path expansion rather
# than external `dirname` (not part of the eval-oracle tool floor).  If this
# path disappears, the golden axis must not silently skip.
script_dir="${BASH_SOURCE[0]%/*}"
if [[ "$script_dir" == "${BASH_SOURCE[0]}" ]]; then
  script_dir="."
fi
golden_dir="$script_dir/fixtures/golden"
golden_checksum="$golden_dir/result.sha256"

details=()
passed=true

if [[ ! -s "$result" ]]; then
  details+=('"result":"missing"')
  passed=false
fi

# Validate result.json contains the required schema: {count, mean, sum}.
if [[ -s "$result" ]]; then
  for key in count mean sum; do
    if ! grep -qE "\"$key\"" "$result"; then
      details+=("\"schema_$key\":\"missing\"")
      passed=false
    fi
  done
fi

# Verify checksum file matches actual sha256 of result.json.
actual=""
if [[ -s "$result" ]]; then
  actual=$(sha256sum "$result" | awk '{print $1}')
fi
# R8-M1: when result.json is non-empty but `actual` is empty, sha256sum
# is unavailable (macOS without coreutils, busybox without sha256sum, or
# a PATH shim returning non-zero).  Without this guard, both the checksum
# block and the golden block fall through to their `actual=""` arms and
# emit `"skipped"` while passed stays true — a verdict of passed:true
# with ZERO integrity verification.  Force-fail and surface the missing
# capability via a dedicated detail key so the operator knows why.  Must
# come BEFORE the checksum/golden blocks so `passed=false` is locked in
# even when both downstream tiers report `skipped`.
if [[ -s "$result" && -z "$actual" ]]; then
  details+=('"sha256_tool":"unavailable"')
  passed=false
fi
declared=""
if [[ -s "$checksum_file" ]]; then
  declared=$(awk '{print $1}' "$checksum_file")
fi
if [[ -z "$declared" ]]; then
  details+=('"checksum":"missing"')
  passed=false
elif [[ -z "$actual" ]]; then
  # R7-M4: when result.json is absent or empty, `actual` was never
  # computed (the `if [[ -s "$result" ]]` block was skipped above), so a
  # string comparison `"" != "$declared"` would emit
  # `"checksum":"mismatch_with_result"` — a misleading false cause that
  # implies tampering when the real issue is the missing file.  The
  # canonical signal is already `"result":"missing"`; emit `"skipped"`
  # here so the axis is reported (operator visibility) but the cause is
  # honest.  R6-S1 fixed the symmetric bug in the golden block below; this
  # closes the same hole on the checksum block.
  details+=('"checksum":"skipped"')
elif [[ "$actual" != "$declared" ]]; then
  details+=('"checksum":"mismatch_with_result"')
  passed=false
else
  details+=('"checksum":"matches_result"')
fi

# If a golden checksum is committed, the result must match it byte-for-byte.
# R6-S1: skip the golden comparison when `actual` is empty (i.e.
# result.json is missing or empty).  The "result":"missing" axis already
# carries that failure; emitting "golden":"mismatch" too lets the
# RejectsGoldenMismatch test pass for the wrong reason — a regression
# that silently drops result.json would still trip its substring check.
# When actual is non-empty, the original two-arm equality decides.
# R7-S1: previously the `actual=""` arm was a `:` no-op so the golden
# axis silently disappeared from details when result.json was absent.
# Emit `"golden":"skipped"` instead so the axis is always reported
# (operator visibility); a future regression that drops the entire golden
# block would then be visible as a missing key rather than silently
# matching the prior absent-key behaviour.
if [[ -s "$golden_checksum" ]]; then
  golden=$(awk '{print $1}' "$golden_checksum")
  if [[ -z "$actual" ]]; then
    details+=('"golden":"skipped"')
  elif [[ "$actual" == "$golden" ]]; then
    details+=('"golden":"matches"')
  else
    details+=('"golden":"mismatch"')
    passed=false
  fi
fi

size=0
# R4-S1: `stat -c %s` is GNU-only.  On macOS/BSD stat prints
# `stat: illegal option -- c` to stderr and the prior `|| echo 0` silently
# reported result_bytes=0 for a real non-empty file.  Fall back to
# `wc -c < file` (POSIX, universally available) if stat fails.  This
# mirrors the windows-only-artifact oracle exactly.
if [[ -s "$result" ]]; then
  size=$(stat -c %s "$result" 2>/dev/null || wc -c < "$result" 2>/dev/null || echo 0)
  # R5-S1: ONLY accept a string of decimal digits (with optional
  # surrounding whitespace from BSD wc's leading pad or a trailing newline
  # from GNU stat).  Anything else — floats from a hypothetical `1.5`
  # shim, scientific notation like `1e3`, multi-token output like `1 2`,
  # or a stray error message — falls back to 0.  The prior
  # `size=$(( ${size:-0} + 0 ))` ran bash arithmetic on uncontrolled
  # input, and any non-integer raised a syntax error to stderr; that
  # error became the first non-empty line of CombinedOutput, breaking
  # the JSON-first-line contract.  This regex-guard mirrors the
  # windows-only-artifact oracle exactly.
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
# R8-S4: emit result_bytes on the failure branch too (symmetric with
# success and with the sibling windows-only-artifact oracle which keeps
# artifact_bytes on both branches).  Downstream consumers expecting the
# metric key on every result would otherwise NPE.  `size` is initialised
# to 0 at the top of the file and overwritten if result.json is readable,
# so it's always defined here.
printf '{"passed":false,"details":{%s},"metrics":{"result_bytes":%d}}\n' "$joined" "$size"
exit 1
