#!/usr/bin/env bash
# Task 19 — require_windows_host uname parametrized table.
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
common="$here/../_common.sh"
fail=0
report() { if [ "$1" -eq 0 ]; then echo "PASS  $2"; else echo "FAIL  $2"; fi; }

tmp=$(mktemp -d); trap "rm -rf '$tmp'" EXIT

# Stub uname that echoes $_UNAME_STUB_OUT.
cat > "$tmp/uname" <<'STUB'
#!/bin/sh
if [ "${1:-}" = "-s" ] || [ -z "${1:-}" ]; then
  printf '%s\n' "${_UNAME_STUB_OUT:-}"
else
  /usr/bin/uname "$@"
fi
STUB
chmod +x "$tmp/uname"
export PATH="$tmp:$PATH"

for accepted in \
    "Windows_NT" \
    "MINGW64_NT-10.0-19045" \
    "MSYS_NT-10.0" \
    "CYGWIN_NT-10.0-WOW"; do
  _UNAME_STUB_OUT="$accepted" bash -c "source '$common' && require_windows_host"
  rc=$?
  [ "$rc" -eq 0 ] && report 0 "accepts '$accepted'" \
                  || { report 1 "accepts '$accepted' (rc=$rc)"; fail=1; }
done

for rejected in \
    "Linux" \
    "Darwin" \
    "FreeBSD" \
    "SunOS" \
    ""; do
  _UNAME_STUB_OUT="$rejected" bash -c "source '$common' && require_windows_host" 2>/dev/null
  rc=$?
  [ "$rc" -eq 1 ] && report 0 "rejects '$rejected'" \
                  || { report 1 "rejects '$rejected' (got rc=$rc)"; fail=1; }
done

# uname missing entirely — build a minimal PATH with bash/sh/command
# but explicitly NO `uname`.
noname=$(mktemp -d)
for tool in bash sh; do
  src=$(command -v "$tool" 2>/dev/null || true)
  [ -n "$src" ] && ln -s "$src" "$noname/$tool" 2>/dev/null || true
done
PATH="$noname" bash -c "source '$common' && require_windows_host" 2>/dev/null
rc=$?
[ "$rc" -eq 1 ] && report 0 "handles missing uname" \
                || { report 1 "handles missing uname (rc=$rc)"; fail=1; }
rm -rf "$noname"

exit $fail
