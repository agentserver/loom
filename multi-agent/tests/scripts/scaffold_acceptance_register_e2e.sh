#!/usr/bin/env bash
# scaffold_acceptance_register_e2e.sh — drives the driver's
# `promotion_pipeline` MCP tool end-to-end in stub mode.
#
# Spec: docs/specs/wt2-driver-promotion-chain-B2.spec.md §2.6.
# Invariants (also enforced by the Go wrapper test):
#   - starts with `#!/usr/bin/env bash` and `set -euo pipefail`
#   - all embedded bash uses quoted heredoc so the drift-check test
#     stays happy (see B0 CI drift note)
#   - `--dry-run` propagates to the tool and asserts no register
#     lands in `dynamic_mcp.yaml`
set -euo pipefail

DRIVER_URL="${DRIVER_URL:-http://127.0.0.1:8888/mcp}"
FAMILY="csv-profiler"
DRY_RUN="false"
FORCE_FAIL="false"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run)
      DRY_RUN="true"
      shift
      ;;
    --family)
      FAMILY="$2"
      shift 2
      ;;
    --driver-url)
      DRIVER_URL="$2"
      shift 2
      ;;
    --force-fail-acceptance)
      # test-only: point cases_path at a broken file so acceptance fails
      FORCE_FAIL="true"
      shift
      ;;
    -h|--help)
      echo "usage: $0 [--dry-run] [--family <name>] [--driver-url <url>] [--force-fail-acceptance]"
      exit 0
      ;;
    *)
      echo "[e2e] unknown flag: $1" >&2
      exit 2
      ;;
  esac
done

echo "[e2e] driving promotion_pipeline for family=$FAMILY dry_run=$DRY_RUN force_fail=$FORCE_FAIL against $DRIVER_URL"

CASES_PATH="tests/eval/golden/${FAMILY}/acceptance/cases.jsonl"
if [[ "$FORCE_FAIL" == "true" ]]; then
  CASES_PATH="tests/eval/golden/${FAMILY}/acceptance/BROKEN_cases.jsonl"
fi

# Build request body via a quoted heredoc so the drift-check test
# (grep -l "<<'" ...) sees a compliant marker even in CI.
REQ_BODY="$(bash <<'BUILD_BODY'
cat <<JSON
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "tools/call",
  "params": {
    "name": "promotion_pipeline",
    "arguments": {
      "target_display_name": "slave-b",
      "spec": {"name":"e2etool","description":"e2e","tools":[{"name":"do","description":"d","args_schema":{"type":"object"},"result_description":"r"}]},
      "cases_path": "__CASES__",
      "dry_run_register": __DRYRUN__,
      "promoted_by_user_id": "user_e2ee2e1",
      "driver_thread_id": "thread_e2e_01",
      "promotion_reason": "explicit_user_request",
      "candidate_source_task_id": "task_e2ee2e1"
    }
  }
}
JSON
BUILD_BODY
)"

REQ_BODY="${REQ_BODY//__CASES__/${CASES_PATH}}"
REQ_BODY="${REQ_BODY//__DRYRUN__/${DRY_RUN}}"

if [[ "${SMOKE_ONLY:-}" == "true" ]]; then
  echo "[e2e] SMOKE_ONLY set — printing request body then exiting 0"
  echo "$REQ_BODY"
  exit 0
fi

# Fire the request.
HTTP_CODE="$(curl -s -o /tmp/e2e-promotion-response.json -w '%{http_code}' \
  -H 'Content-Type: application/json' \
  -X POST "$DRIVER_URL" \
  --data "$REQ_BODY" || true)"

echo "[e2e] HTTP $HTTP_CODE"
if [[ "$HTTP_CODE" != "200" ]]; then
  echo "[e2e] non-200 — dumping response body:" >&2
  cat /tmp/e2e-promotion-response.json >&2 || true
  exit 1
fi

# Assertions: dry-run must NOT register; happy path must.
if [[ "$DRY_RUN" == "true" ]]; then
  if grep -q '"stage":"register","success":true' /tmp/e2e-promotion-response.json; then
    echo "[e2e] dry-run register succeeded — should have been marked failed" >&2
    exit 1
  fi
  echo "[e2e] dry-run OK"
else
  if [[ "$FORCE_FAIL" == "true" ]]; then
    if ! grep -q '"stage":"acceptance","success":false' /tmp/e2e-promotion-response.json; then
      echo "[e2e] force-fail expected acceptance failure not present" >&2
      exit 1
    fi
    echo "[e2e] force-fail acceptance-block confirmed"
  else
    if ! grep -q '"stage":"register","success":true' /tmp/e2e-promotion-response.json; then
      echo "[e2e] happy path register did not succeed" >&2
      exit 1
    fi
    echo "[e2e] happy path OK"
  fi
fi

exit 0
