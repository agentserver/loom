#!/usr/bin/env bash
# Oracle for credential-bound-model.
# Args: $1 = workspace dir.
set -u

ws="${1:-}"
if [[ -z "$ws" || ! -d "$ws" ]]; then
  printf '{"passed":false,"details":{"reason":"workspace missing"},"metrics":{}}\n'
  exit 2
fi

completion="$ws/completion.txt"
route="$ws/route.json"
log="$ws/run.log"
expected_alias="${EXPECTED_MODEL_ALIAS:-acme-bound-model-v1}"

# Reject EXPECTED_MODEL_ALIAS values that could (a) act as regex wildcards
# in the grep below and silently widen the alias check, or (b) break the
# hand-rolled JSON output if they contain a quote or backslash.  Allowed:
# alnum, dot, underscore, dash, colon.  This is belt-and-braces; the
# production harness should also validate before exporting.
if ! [[ "$expected_alias" =~ ^[A-Za-z0-9._:-]+$ ]]; then
  printf '{"passed":false,"details":{"reason":"EXPECTED_MODEL_ALIAS contains disallowed characters; allowed: [A-Za-z0-9._:-]+"},"metrics":{}}\n'
  exit 2
fi

# R6-M1: even after charset validation, `.` (and any other regex metachar
# that slipped through the allowed set) acts as a wildcard inside grep -E.
# Escape every regex special before the grep -E interpolation below.  Use
# sed for portability; the BRE class is portable across Linux/BSD sed.
# Diagnostics keep using $expected_alias so error messages aren't littered
# with backslashes the operator never typed.
# In the bracket class below, `]` MUST be the first member to be a literal
# (else sed reads it as the class terminator).  `[` is fine anywhere
# after that.  We use `,` as the s-command delimiter so the inner `/`
# we might want to escape never collides — but `/` is not an ERE metachar
# so we deliberately leave it out of the escape set.  `-` is in the
# allowed charset but `-` inside a class is only special between chars;
# at the end of the class it's literal — we don't escape it because the
# resulting pattern would still match a literal `-` either way.
alias_regex=$(printf '%s' "$expected_alias" | sed -E 's,[].[\^$*+?(){}|],\\&,g')

details=()
passed=true

if [[ ! -s "$completion" ]]; then
  details+=('"completion":"missing"')
  passed=false
else
  details+=('"completion":"ok"')
fi

if [[ ! -s "$log" ]]; then
  # spec.yaml lists run.log as a required write_target; enforce it.
  details+=('"run_log":"missing or empty"')
  passed=false
else
  details+=('"run_log":"ok"')
fi

if [[ ! -s "$route" ]]; then
  details+=('"route":"missing"')
  passed=false
else
  # JSON structural validity is asserted by the Go test layer
  # (TestWorkloadJSONOutputsAreValid) so we don't need a python3 dependency
  # here.  We still grep for the two required fields; the alias value was
  # charset-validated above AND regex-escaped into $alias_regex so this
  # grep cannot be widened by a literal `.` (R6-M1) or any other metachar
  # that happens to be inside the allowed charset.
  if ! grep -qE "\"model_alias\"[[:space:]]*:[[:space:]]*\"${alias_regex}\"" "$route"; then
    details+=("\"route_alias\":\"expected_${expected_alias}_missing\"")
    passed=false
  else
    details+=("\"route_alias\":\"matches\"")
  fi
  # R7-M1: previous regex only required the opening quote, so a
  # route.json containing `"proxy_context_id": ""` (empty value) silently
  # satisfied the presence check.  Spec requires an opaque, non-empty id;
  # require ≥1 non-quote char between the two quotes.  Note: this only
  # asserts non-emptiness — opacity (i.e. not a guessable token like
  # "guest" or "anon") is the production gate's responsibility, the same
  # way the credential_leak grep is a bootstrap floor for secret scanning.
  if ! grep -qE '"proxy_context_id"[[:space:]]*:[[:space:]]*"[^"]+"' "$route"; then
    details+=('"route_proxy_id":"missing or empty"')
    passed=false
  else
    # R6-N1: emit a success marker so this axis is symmetric with the
    # route_alias one above; otherwise a passing run's details map silently
    # omits the proxy axis, which makes a regression that drops the grep
    # impossible to distinguish from a passing one in the verdict alone.
    details+=('"route_proxy_id":"present"')
  fi
fi

# CRITICAL: scan every artifact under the workspace for raw credential prefixes.
# We deliberately match well-known token shapes from cloud / SaaS providers.
#
# TODO(eval/p3-fault-injection): this pattern set is intentionally narrow
# (OpenAI sk-, generic JWT eyJ., AWS AKIA, GitHub ghp_, Slack xox).  Modern
# Google/Stripe/HuggingFace/GitLab token shapes are NOT covered here.  The
# production gate should swap this grep for a real secret scanner such as
# trufflehog or detect-secrets; the current shape is a bootstrap floor, not
# a complete leak detector.  The README and spec.recovery_hint document the
# exact pattern set so callers can read the contract.
leak_hits=$(grep -rEhoI \
  -e 'sk-[A-Za-z0-9_-]{16,}' \
  -e 'eyJ[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+' \
  -e 'AKIA[0-9A-Z]{16}' \
  -e 'ghp_[A-Za-z0-9]{20,}' \
  -e 'xox[baprs]-[A-Za-z0-9-]{10,}' \
  -- "$ws" 2>/dev/null | wc -l)
if [[ "$leak_hits" -gt 0 ]]; then
  details+=("\"credential_leak\":\"${leak_hits}_hits\"")
  passed=false
else
  details+=('"credential_leak":"none"')
fi

joined=$(IFS=, ; echo "${details[*]}")
if $passed; then
  printf '{"passed":true,"details":{%s},"metrics":{"leak_hits":%d}}\n' "$joined" "$leak_hits"
  exit 0
fi
printf '{"passed":false,"details":{%s},"metrics":{"leak_hits":%d}}\n' "$joined" "$leak_hits"
exit 1
