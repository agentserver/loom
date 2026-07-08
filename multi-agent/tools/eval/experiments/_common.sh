#!/usr/bin/env bash
# _common.sh — shared preflight helpers for tools/eval/experiments/
# per-workload wrappers.
#
# Sourced, not executed. Every helper returns exit code (0 = OK,
# 1 = negative-but-non-fatal, 2 = fatal). Config parsers NEVER print
# key names or values — only `present` / `absent` / `error`.
#
# Global constraints (spec §Global Constraints):
# - Preflight probes MUST NOT execute `codex` (no `codex doctor`,
#   `codex exec`, `codex --version`). Only `command -v codex`.
# - Config parsing is read-only and NEVER logs values.
# - No unnamespaced env vars leaked to sub-scripts.
#
# All helpers are safe to call under `set -euo pipefail`.

# Where's the codex config? Real code uses ~/.codex/config.toml; tests
# override via LOOM_CODEX_CONFIG_PATH.
# TEST SEAM: LOOM_CODEX_CONFIG_PATH overrides the default path.
# Fresh-review P2: fall back to a stable placeholder when HOME is
# unset (systemd PrivateHome / sandbox) so `set -u` doesn't abort
# BEFORE codex_config_readable can return 2 cleanly.
_codex_config_path() {
  local home="${HOME:-/nonexistent-home}"
  printf '%s\n' "${LOOM_CODEX_CONFIG_PATH:-$home/.codex/config.toml}"
}

codex_bin_present() {
  # NEVER invoke codex — only look it up on PATH.
  command -v codex >/dev/null 2>&1 || return 2
  return 0
}

codex_config_readable() {
  local p; p=$(_codex_config_path)
  [ -r "$p" ] || return 2
  return 0
}

# Read-only TOML probe. Emits ONLY the token `present` or `absent`
# to stdout; on parse failure, `error` and rc=2. NEVER emits key names
# or values.
codex_config_has_route_a() {
  local p; p=$(_codex_config_path)
  [ -r "$p" ] || { printf 'error\n'; return 2; }
  local out
  out=$(python3 - "$p" <<'PY' 2>/dev/null
import sys, tomllib
try:
    with open(sys.argv[1], "rb") as f:
        data = tomllib.load(f)
except Exception:
    print("error"); sys.exit(2)
node = data.get("model_providers", {}).get("modelserver", {})
if isinstance(node, dict) and "experimental_bearer_token" in node:
    val = node["experimental_bearer_token"]
    if isinstance(val, str) and val.strip():
        print("present"); sys.exit(0)
print("absent"); sys.exit(1)
PY
)
  local rc=$?
  case "$out" in
    present|absent|error) printf '%s\n' "$out" ;;
    *) printf 'error\n'; rc=2 ;;
  esac
  return "$rc"
}

codex_config_has_route_b() {
  local p; p=$(_codex_config_path)
  [ -r "$p" ] || { printf 'error\n'; return 2; }
  local out
  out=$(python3 - "$p" <<'PY' 2>/dev/null
import sys, tomllib
try:
    with open(sys.argv[1], "rb") as f:
        data = tomllib.load(f)
except Exception:
    print("error"); sys.exit(2)
node = data.get("model_providers", {}).get("modelserver", {})
if isinstance(node, dict) and "env_key" in node:
    val = node["env_key"]
    if isinstance(val, str) and val.strip():
        print("present"); sys.exit(0)
print("absent"); sys.exit(1)
PY
)
  local rc=$?
  case "$out" in
    present|absent|error) printf '%s\n' "$out" ;;
    *) printf 'error\n'; rc=2 ;;
  esac
  return "$rc"
}

require_writable_tmp() {
  local d
  d=$(mktemp -d 2>/dev/null) || return 2
  rmdir "$d" 2>/dev/null || return 2
  return 0
}

require_fixture() {
  local p="${1:?require_fixture: path arg required}"
  [ -e "$p" ] || {
    printf '[_common.sh] required fixture missing: %s\n' "$p" >&2
    return 2
  }
  return 0
}

require_windows_host() {
  # Guard against missing `uname` explicitly (bash "command -v" is a
  # shell builtin, doesn't need PATH).
  local out=""
  if command -v uname >/dev/null 2>&1; then
    out="$(uname -s 2>/dev/null || true)"
  fi
  case "$out" in
    *NT*|MSYS*|CYGWIN*|MINGW*) return 0 ;;
    *) return 1 ;;
  esac
}

die() {
  printf '%s\n' "$*" >&2
  exit 2
}

warn_and_exit_zero() {
  printf '%s\n' "$*" >&2
  exit 0
}
