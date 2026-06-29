#!/usr/bin/env bash
# eval-bootstrap.sh — self-check integration test for agentserver-stub.
#
#   1. Build the stub into a tmpdir.
#   2. Start it on a free loopback port; wait for /healthz.
#   3. Issue credentials for driver / slave-a / slave-b / observer.
#   4. curl /api/v1/agents/whoami with each proxy_token; assert short_id matches.
#   5. Kill the stub and clean up.
#
# Phase 1 WT-1-eval-runner-skeleton can crib this verbatim.
#
# ⚠️  NOT FOR PRODUCTION.

set -euo pipefail

PKG_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MODULE_ROOT="$(cd "${PKG_DIR}/../../.." && pwd)"
TMPDIR="$(mktemp -d -t agentserver-stub-bootstrap.XXXXXX)"
PORT="${PORT:-18080}"
SERVER_URL="http://127.0.0.1:${PORT}"
BIN="${TMPDIR}/agentserver-stub"
LOG="${TMPDIR}/stub.log"
PID_FILE="${TMPDIR}/stub.pid"

cleanup() {
  if [[ -f "${PID_FILE}" ]]; then
    local pid
    pid="$(cat "${PID_FILE}")"
    if kill -0 "${pid}" 2>/dev/null; then
      kill "${pid}" 2>/dev/null || true
      wait "${pid}" 2>/dev/null || true
    fi
  fi
  rm -rf "${TMPDIR}"
}
trap cleanup EXIT

echo "==> building agentserver-stub into ${BIN}"
(cd "${MODULE_ROOT}" && go build -o "${BIN}" ./tools/eval/agentserver-stub)

echo "==> starting stub on ${SERVER_URL}"
"${BIN}" --listen "127.0.0.1:${PORT}" --workspace-id auto >"${LOG}" 2>&1 &
echo $! >"${PID_FILE}"

echo "==> waiting for /healthz"
for _ in $(seq 1 50); do
  if curl -fsS "${SERVER_URL}/healthz" >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
done
curl -fsS "${SERVER_URL}/healthz" >/dev/null || {
  echo "FAIL: /healthz never came up; log:"; cat "${LOG}"; exit 1;
}

issue() {
  local role="$1" short_id="$2"
  "${BIN}" issue --server "${SERVER_URL}" --role "${role}" --short-id "${short_id}"
}

declare -A SHORT_IDS=(
  [driver]=drv-001
  [slave-a]=slv-a-001
  [slave-b]=slv-b-001
  [observer]=obs-001
)

# bash hashes don't preserve order, so iterate a fixed list.
ROLES=(driver slave-a slave-b observer)

echo "==> issuing credentials for 4 roles"
for role in "${ROLES[@]}"; do
  short_id="${SHORT_IDS[$role]}"
  out_file="${TMPDIR}/${role}.json"
  issue "${role}" "${short_id}" >"${out_file}"

  # cheap JSON-field extractor — avoid forcing a jq dependency on the eval host
  field() {
    grep -o "\"$1\"[[:space:]]*:[[:space:]]*\"[^\"]*\"" "${out_file}" | head -n1 | sed 's/.*"\([^"]*\)"$/\1/'
  }
  for f in sandbox_id tunnel_token proxy_token workspace_id short_id; do
    v="$(field "${f}")"
    if [[ -z "${v}" ]]; then
      echo "FAIL: ${role} credentials missing field ${f}"
      cat "${out_file}"
      exit 1
    fi
  done

  proxy_token="$(field proxy_token)"
  whoami_short_id="$(curl -fsS -H "Authorization: Bearer ${proxy_token}" \
    "${SERVER_URL}/api/v1/agents/whoami" \
    | grep -o '"short_id"[[:space:]]*:[[:space:]]*"[^"]*"' \
    | sed 's/.*"\([^"]*\)"$/\1/')"
  if [[ "${whoami_short_id}" != "${short_id}" ]]; then
    echo "FAIL: ${role} whoami short_id = '${whoami_short_id}', want '${short_id}'"
    exit 1
  fi
  echo "    ${role} short_id=${short_id} whoami=OK"
done

echo "==> heartbeat smoke (driver proxy_token)"
driver_proxy="$(grep -o '"proxy_token"[[:space:]]*:[[:space:]]*"[^"]*"' "${TMPDIR}/driver.json" \
  | sed 's/.*"\([^"]*\)"$/\1/')"
status="$(curl -s -o /dev/null -w '%{http_code}' -X POST \
  -H "Authorization: Bearer ${driver_proxy}" \
  -H "Content-Type: application/json" \
  -d '{"ts":0,"ok":true}' \
  "${SERVER_URL}/api/v1/agents/heartbeat")"
if [[ "${status}" != "204" ]]; then
  echo "FAIL: heartbeat status ${status}, want 204"; exit 1;
fi

echo "==> legacy alias smoke (/api/agent/whoami)"
legacy_short_id="$(curl -fsS -H "Authorization: Bearer ${driver_proxy}" \
  "${SERVER_URL}/api/agent/whoami" \
  | grep -o '"short_id"[[:space:]]*:[[:space:]]*"[^"]*"' \
  | sed 's/.*"\([^"]*\)"$/\1/')"
if [[ "${legacy_short_id}" != "drv-001" ]]; then
  echo "FAIL: legacy alias short_id = '${legacy_short_id}', want drv-001"; exit 1;
fi

echo
echo "PASS: agentserver-stub bootstrap self-check"
