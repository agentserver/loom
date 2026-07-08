#!/usr/bin/env bash
# Task 12 — _common.sh unit tests.
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
common_sh="$here/../_common.sh"

fail=0
report() {
  if [ "$1" -eq 0 ]; then
    echo "PASS  $2"
  else
    echo "FAIL  $2"
    fail=1
  fi
}

# --- codex_bin_present ---
tmp=$(mktemp -d)
tmp2=$(mktemp -d)
trap "rm -rf '$tmp' '$tmp2'" EXIT
touch "$tmp/codex"; chmod +x "$tmp/codex"
PATH="$tmp:/usr/bin" bash -c "source '$common_sh' && codex_bin_present"
report $? "codex_bin_present with fake codex → 0"

# Build a minimal PATH with a shadow of only the tools bash needs,
# NO `codex` (the host may have codex in /usr/bin — must exclude it).
noc=$(mktemp -d)
for tool in bash sh command; do
  src=$(command -v "$tool" 2>/dev/null || true)
  [ -n "$src" ] && ln -s "$src" "$noc/$tool" 2>/dev/null || true
done
PATH="$noc" bash -c "source '$common_sh' && codex_bin_present" 2>/dev/null
[ $? -eq 2 ] && report 0 "codex_bin_present without codex → 2" || report 1 "codex_bin_present without codex → 2"
rm -rf "$noc"

# codex_bin_present MUST NOT invoke codex.
cat > "$tmp/codex" <<'EOF'
#!/bin/sh
echo "IMPOSSIBLE: codex was executed" >&2
exit 99
EOF
chmod +x "$tmp/codex"
out=$(PATH="$tmp:/usr/bin" bash -c "source '$common_sh' && codex_bin_present" 2>&1)
if echo "$out" | grep -q "IMPOSSIBLE"; then
  report 1 "codex_bin_present must not invoke codex"
else
  report 0 "codex_bin_present must not invoke codex"
fi

# --- codex_config_readable ---
mkdir -p "$tmp2/.codex"
touch "$tmp2/.codex/config.toml"

LOOM_CODEX_CONFIG_PATH="$tmp2/.codex/config.toml" bash -c "source '$common_sh' && codex_config_readable"
report $? "codex_config_readable with readable file → 0"

LOOM_CODEX_CONFIG_PATH="/nonexistent/config.toml" bash -c "source '$common_sh' && codex_config_readable" 2>/dev/null
[ $? -eq 2 ] && report 0 "codex_config_readable with missing file → 2" || report 1 "codex_config_readable with missing file → 2"

# --- codex_config_has_route_a ---
cat > "$tmp2/.codex/config.toml" <<'EOF'
[model_providers.modelserver]
experimental_bearer_token = "sk-TEST-NOT-REAL-01aaaa"
EOF
out=$(LOOM_CODEX_CONFIG_PATH="$tmp2/.codex/config.toml" bash -c "source '$common_sh' && codex_config_has_route_a" 2>&1)
[ "$out" = "present" ] && report 0 "route_a present detected" || report 1 "route_a present (got '$out')"

# CRITICAL: token value MUST NOT leak
if echo "$out" | grep -q "sk-TEST-NOT-REAL"; then
  report 1 "SECURITY: route_a value leaked into output"
else
  report 0 "route_a token value NOT leaked"
fi

# route-a commented → absent
cat > "$tmp2/.codex/config.toml" <<'EOF'
[model_providers.modelserver]
# experimental_bearer_token = "sk-TEST-NOT-REAL-01aaaa"
EOF
out=$(LOOM_CODEX_CONFIG_PATH="$tmp2/.codex/config.toml" bash -c "source '$common_sh' && codex_config_has_route_a" 2>&1)
[ "$out" = "absent" ] && report 0 "route_a commented → absent" || report 1 "route_a commented (got '$out')"

# route-a in different table → absent
cat > "$tmp2/.codex/config.toml" <<'EOF'
[model_providers.other]
experimental_bearer_token = "sk-not-ours"
EOF
out=$(LOOM_CODEX_CONFIG_PATH="$tmp2/.codex/config.toml" bash -c "source '$common_sh' && codex_config_has_route_a" 2>&1)
[ "$out" = "absent" ] && report 0 "route_a in .other → absent" || report 1 "route_a in .other (got '$out')"

# --- codex_config_has_route_b ---
cat > "$tmp2/.codex/config.toml" <<'EOF'
[model_providers.modelserver]
env_key = "SOME_ENV_NAME"
EOF
out=$(LOOM_CODEX_CONFIG_PATH="$tmp2/.codex/config.toml" bash -c "source '$common_sh' && codex_config_has_route_b" 2>&1)
[ "$out" = "present" ] && report 0 "route_b present detected" || report 1 "route_b present (got '$out')"

if echo "$out" | grep -q "SOME_ENV_NAME"; then
  report 1 "SECURITY: route_b env-key name leaked"
else
  report 0 "route_b env-key name NOT leaked"
fi

# --- require_writable_tmp ---
bash -c "source '$common_sh' && require_writable_tmp"
report $? "require_writable_tmp → 0"

# --- require_fixture ---
bash -c "source '$common_sh' && require_fixture '$tmp2/.codex/config.toml'"
report $? "require_fixture present → 0"

bash -c "source '$common_sh' && require_fixture '/nonexistent/xyz'" 2>/dev/null
[ $? -eq 2 ] && report 0 "require_fixture absent → 2" || report 1 "require_fixture absent → 2"

# --- require_windows_host ---
bash -c "source '$common_sh' && require_windows_host" 2>/dev/null
rc=$?
uname_out=$(uname -s)
case "$uname_out" in
  *NT*|MSYS*|CYGWIN*|MINGW*) expected=0 ;;
  *) expected=1 ;;
esac
[ "$rc" -eq "$expected" ] && report 0 "require_windows_host respects uname (uname=$uname_out)" \
                          || report 1 "require_windows_host wrong (uname=$uname_out expected=$expected got=$rc)"

# --- die ---
out=$(bash -c "source '$common_sh' && die 'boom'" 2>&1)
rc=$?
[ "$rc" -eq 2 ] && [ "$out" = "boom" ] && report 0 "die → exit 2 with msg" || report 1 "die (rc=$rc out='$out')"

# --- warn_and_exit_zero ---
out=$(bash -c "source '$common_sh' && warn_and_exit_zero 'skipping'" 2>&1)
rc=$?
[ "$rc" -eq 0 ] && [ "$out" = "skipping" ] && report 0 "warn_and_exit_zero → exit 0" || report 1 "warn (rc=$rc)"

exit $fail
