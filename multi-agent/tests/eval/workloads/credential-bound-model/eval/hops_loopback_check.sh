#!/usr/bin/env bash
# hops_loopback_check.sh — WT-2-credential-workload spec §2.4, §7(f).
#
# Given a route.json and a mode (a|b), assert that the recorded
# hops[].proxy_addr values match the mode's loopback expectation:
#
#   mode=a  at least one hop.proxy_addr host is EXACTLY 127.0.0.1 / ::1 / localhost
#   mode=b  NO hop.proxy_addr host matches that allowlist
#
# The check uses grep + shell string comparison — no jq dependency. The
# workload's route.json is canonical one-field-per-line JSON; anything
# else is already caught by TestWorkloadJSONOutputsAreValid upstream.
#
# EXIT codes:
#   0  check passed
#   1  check failed (details on stderr)
#   2  usage / arg error
#
# NOTE on §7(f) exact-vs-substring: we split the recorded proxy_addr
# on the LAST `:` (host:port) or the trailing `]` (bracketed IPv6),
# then compare the host component against the allowlist with STRING
# EQUALITY. A substring check would let 127.0.0.1.evil.com masquerade;
# see testdata/hop_fake_loopback.json for the negative case.

set -u

usage() {
  cat >&2 <<'EOF'
usage: hops_loopback_check.sh <route.json> <mode>
       hops_loopback_check.sh --self-test

  route.json  path to the workload's route.json (produced by the runner)
  mode        "a" (local proxy) or "b" (upstream direct)
  --self-test run the built-in fixture suite; exit 0 iff every fixture
              yields its expected verdict.
EOF
}

# strip_hostport <proxy_addr> → prints the host component
#   127.0.0.1:53452       → 127.0.0.1
#   [::1]:8080            → ::1
#   ::1                   → ::1
#   127.0.0.1.evil.com    → 127.0.0.1.evil.com  (unchanged; equality will reject)
#   localhost             → localhost
strip_hostport() {
  local addr="$1"
  # bracketed IPv6 form: [host]:port  or  [host]
  if [[ "$addr" == \[*\]* ]]; then
    local body="${addr#[}"
    body="${body%%]*}"
    printf '%s' "$body"
    return
  fi
  # bare form: host:port or host. Only strip a trailing :<digits> so we
  # don't confuse the last `:` of a bare IPv6 address with a port
  # separator.
  if [[ "$addr" =~ ^(.+):([0-9]+)$ ]]; then
    # A bare IPv6 with no port has multiple colons and no [] — the
    # regex above would eat its final segment. Detect that shape and
    # keep the whole address instead.
    local head="${BASH_REMATCH[1]}"
    if [[ "$head" == *:* && "$head" != *.* ]]; then
      # multi-colon, no dot → looks like bare IPv6 (::1, fe80::1, …).
      printf '%s' "$addr"
      return
    fi
    printf '%s' "$head"
    return
  fi
  printf '%s' "$addr"
}

# is_loopback_host <host> → exit 0 iff host is EXACTLY one of the allowlist
is_loopback_host() {
  local h="$1"
  case "$h" in
    127.0.0.1|::1|localhost) return 0 ;;
    *) return 1 ;;
  esac
}

# extract_proxy_addrs <route.json> → one host per line (empty if none)
extract_proxy_addrs() {
  local route="$1"
  # Match "proxy_addr" : "value" — non-greedy value between two quotes.
  # sed extracts the value.
  grep -oE '"proxy_addr"[[:space:]]*:[[:space:]]*"[^"]+"' "$route" 2>/dev/null \
    | sed -E 's/^"proxy_addr"[[:space:]]*:[[:space:]]*"([^"]+)".*$/\1/' \
    | while IFS= read -r addr; do
        strip_hostport "$addr"
        printf '\n'
      done
}

# check_route <route.json> <mode> → exit 0 pass / 1 fail
check_route() {
  local route="$1"
  local mode="$2"

  if [[ ! -s "$route" ]]; then
    printf 'hops_loopback_check: route.json missing or empty: %s\n' "$route" >&2
    return 1
  fi

  local hosts
  hosts=$(extract_proxy_addrs "$route")

  local seen_loopback=0
  local seen_any=0
  while IFS= read -r host; do
    [[ -z "$host" ]] && continue
    seen_any=1
    if is_loopback_host "$host"; then
      seen_loopback=1
    fi
  done <<<"$hosts"

  case "$mode" in
    a)
      if [[ "$seen_loopback" -eq 1 ]]; then
        return 0
      fi
      if [[ "$seen_any" -eq 0 ]]; then
        printf 'hops_loopback_check: mode=a requires a loopback hop but hops[] carries no proxy_addr (empty hops array): %s\n' "$route" >&2
      else
        printf 'hops_loopback_check: mode=a requires an EXACT-match loopback proxy_addr (127.0.0.1/::1/localhost); none found in: %s\n' "$route" >&2
        printf 'hops_loopback_check: observed hosts:\n' >&2
        printf '  %s\n' $hosts >&2
      fi
      return 1
      ;;
    b)
      if [[ "$seen_loopback" -eq 1 ]]; then
        printf 'hops_loopback_check: mode=b forbids a loopback hop but route.json records one: %s\n' "$route" >&2
        return 1
      fi
      return 0
      ;;
    *)
      printf 'hops_loopback_check: mode must be "a" or "b", got %q\n' "$mode" >&2
      return 2
      ;;
  esac
}

# --- self-test harness ---------------------------------------------------

self_test() {
  local dir
  dir="$(cd -- "$(dirname -- "$0")" && pwd)"
  local td="$dir/testdata"
  if [[ ! -d "$td" ]]; then
    printf 'hops_loopback_check: --self-test: testdata dir missing: %s\n' "$td" >&2
    return 2
  fi

  local total=0
  local failed=0
  local fixture mode expected verdict

  # <fixture>|<mode>|<expected>  where expected is "pass" or "fail"
  local cases=(
    "hop_loopback_ok.json|a|pass"
    "hop_no_proxy.json|b|pass"
    "hop_fake_loopback.json|a|fail"
    "hop_empty.json|a|fail"
    "hop_leaks_loopback.json|b|fail"
  )

  for row in "${cases[@]}"; do
    total=$((total + 1))
    IFS='|' read -r fixture mode expected <<<"$row"
    if check_route "$td/$fixture" "$mode" >/dev/null 2>&1; then
      verdict="pass"
    else
      verdict="fail"
    fi
    if [[ "$verdict" == "$expected" ]]; then
      printf 'OK   %s (mode=%s expected=%s got=%s)\n' "$fixture" "$mode" "$expected" "$verdict"
    else
      failed=$((failed + 1))
      printf 'FAIL %s (mode=%s expected=%s got=%s)\n' "$fixture" "$mode" "$expected" "$verdict" >&2
    fi
  done

  printf 'hops_loopback_check --self-test: %d cases, %d failed\n' "$total" "$failed"
  [[ "$failed" -eq 0 ]]
}

# --- main ----------------------------------------------------------------

if [[ $# -eq 1 && "$1" == "--self-test" ]]; then
  self_test
  exit $?
fi

if [[ $# -lt 2 ]]; then
  usage
  exit 2
fi

check_route "$1" "$2"
exit $?
