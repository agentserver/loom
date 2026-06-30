#!/usr/bin/env bash
# Oracle for windows-only-artifact.
# Args: $1 = workspace dir.
# Stdout: one JSON line.
set -u

ws="${1:-}"
if [[ -z "$ws" || ! -d "$ws" ]]; then
  printf '{"passed":false,"details":{"reason":"workspace missing"},"metrics":{}}\n'
  exit 2
fi

art="$ws/artifact.bin"
meta="$ws/artifact.meta.json"
details=()
passed=true

if [[ ! -s "$art" ]]; then
  details+=('"artifact":"missing or empty"')
  passed=false
fi
if [[ ! -s "$meta" ]]; then
  details+=('"metadata":"missing"')
  passed=false
fi

actual=""
if [[ -s "$art" ]]; then
  actual=$(sha256sum "$art" | awk '{print $1}')
fi
# R9-M1: mirror remote-data-processing's R8-M1 guard.  If artifact.bin is
# non-empty but sha256sum produced no hash (binary missing, shim exits
# non-zero, or equivalent tool failure), integrity was not verified.  The
# R8-S3 `actual=""` branch below correctly reports `sha256:"skipped"`, but
# without this force-fail the oracle returned passed:true on a real artifact
# with zero hash verification.
if [[ -s "$art" && -z "$actual" ]]; then
  details+=('"sha256_tool":"unavailable"')
  passed=false
fi

declared=""
if [[ -s "$meta" ]]; then
  # Pull the "sha256":"..." value out of the metadata JSON without needing jq.
  # R9-S1: use grep+sed only; `head` is not part of the eval-oracle tool floor.
  declared=$(grep -oE '"sha256"[[:space:]]*:[[:space:]]*"[a-fA-F0-9]+"' "$meta" \
    | sed -nE '1{s/.*"([a-fA-F0-9]+)"$/\1/p;}')
fi

if [[ -z "$declared" ]]; then
  details+=('"metadata_sha256":"missing"')
  passed=false
elif [[ -z "$actual" ]]; then
  # R8-S3: artifact.bin is missing/empty — the "artifact":"missing or empty"
  # axis already names the cause.  Emitting `"sha256":"mismatch"` too lets
  # the symmetric R6-S1 / R7-M4 anti-pattern through: a regression that
  # silently drops artifact.bin would still satisfy a substring-assertion
  # for "sha256 mismatch".  Report `skipped` so the axis is reported but
  # the cause is honest.
  details+=('"sha256":"skipped"')
elif [[ "$actual" != "$declared" ]]; then
  details+=('"sha256":"mismatch"')
  passed=false
else
  details+=('"sha256":"matches"')
fi

size=0
# R3-M2: `stat -c %s` is GNU-only.  On macOS/BSD stat prints
# `stat: illegal option -- c` to stderr and the unbound size= broke the
# printf below.  Fall back to `wc -c < file` (POSIX, universally available)
# if stat fails.  The rdp oracle uses the identical idiom.
if [[ -s "$art" ]]; then
  size=$(stat -c %s "$art" 2>/dev/null || wc -c < "$art" 2>/dev/null || echo 0)
  # R5-S1: ONLY accept a string of decimal digits (with optional
  # surrounding whitespace from BSD wc's leading pad or a trailing newline
  # from GNU stat).  Anything else — floats from a hypothetical `1.5`
  # shim, scientific notation like `1e3`, multi-token output like `1 2`,
  # or a stray error message — falls back to 0.  Critical: the prior
  # `size=$(( ${size:-0} + 0 ))` ran bash arithmetic on uncontrolled
  # input, and any non-integer raised a syntax error to stderr; that
  # error became the first non-empty line of CombinedOutput, breaking
  # the JSON-first-line contract that downstream consumers rely on.
  # This regex-guard ensures `size` is always a plain integer literal
  # before the `printf '%d'` below ever sees it.
  if [[ "$size" =~ ^[[:space:]]*([0-9]+)[[:space:]]*$ ]]; then
    size="${BASH_REMATCH[1]}"
  else
    size=0
  fi
fi

joined=$(IFS=, ; echo "${details[*]}")
if $passed; then
  printf '{"passed":true,"details":{%s},"metrics":{"artifact_bytes":%d}}\n' "$joined" "$size"
  exit 0
fi
printf '{"passed":false,"details":{%s},"metrics":{"artifact_bytes":%d}}\n' "$joined" "$size"
exit 1
