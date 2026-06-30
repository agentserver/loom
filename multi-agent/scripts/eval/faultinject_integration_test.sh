#!/usr/bin/env bash
# WT-1-fault-injection integration smoke (8 kinds).
#
# What this script proves:
#   1. The control plane (loopback only) accepts /inject for every one
#      of the 8 FaultKind values declared in tools/eval/faultinject/
#      kinds.go.
#   2. Each registered fault, when its hook point fires from a
#      bridged-driver / bridged-executor call site, produces the
#      expected typed error (or panic, for driver_restart).
#   3. The control plane is bound to 127.0.0.1 (not a routable IP).
#   4. The production binary (./cmd/driver-agent, built without
#      -tags=evaltool) contains zero `faultinject` symbols.
#
# Implementation notes:
#   - The integration is hosted inside a Go test program built with
#     -tags=evaltool. The shell wrapper merely runs that program, then
#     greps its output for the 8 PASS lines.
#   - We do not spawn a real driver-agent + slave-agent process pair —
#     the 8 fault kinds are exercised through the same hook bridge that
#     a real driver+slave would use. The "is the kind reachable from
#     the wire?" question is what this smoke answers.
#
# Exit codes:
#   0 — all 8 kinds bridged successfully; production binary clean.
#   1 — any kind failed, OR production binary contained faultinject
#       symbols, OR the control plane did not bind to loopback.
#
# Run from the multi-agent/ Go module root.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/../.."

echo "[faultinject-integration] running 8-kind matrix under -tags=evaltool"

# All 8 kinds are exercised via TestHookBridge_AllEightKinds_MatrixSmoke
# (one Run sub-test per kind). The shell script asserts every sub-test
# passes.

OUT=$(mktemp)
trap 'rm -f "$OUT"' EXIT

if ! go test ./tools/eval/faultinject/ \
        -tags=evaltool -run '^TestHookBridge_AllEightKinds_MatrixSmoke$' \
        -count=1 -race -v > "$OUT" 2>&1; then
    echo "[faultinject-integration] FAIL: 8-kind matrix test failed"
    cat "$OUT"
    exit 1
fi

expected=(
    "missing_file"
    "stale_capability"
    "wrong_os_version"
    "forbidden_cred"
    "slave_disconnect"
    "driver_restart"
    "model_route_failure"
    "duplicate_pickup"
)
for kind in "${expected[@]}"; do
    if ! grep -qE -- "--- PASS: TestHookBridge_AllEightKinds_MatrixSmoke/${kind}" "$OUT"; then
        echo "[faultinject-integration] FAIL: kind ${kind} did not pass"
        echo "---- test output ----"
        cat "$OUT"
        exit 1
    fi
done

echo "[faultinject-integration] 8/8 kinds PASS"

# Also assert the loopback-bind enforcement and the rate-limit boundary
# (these are the two highest-blast-radius security gates).
if ! go test ./tools/eval/faultinject/ \
        -tags=evaltool \
        -run '^(TestServer_RejectsNonLoopbackBind|TestServer_AcceptsLoopback|TestServer_InjectRateLimit_101st_400|TestStore_SentinelFakeCred_ByteEquality|TestHookBridge_DuplicatePickup_NoCommandReplay)$' \
        -count=1 -race > "$OUT" 2>&1; then
    echo "[faultinject-integration] FAIL: security gate tests failed"
    cat "$OUT"
    exit 1
fi
echo "[faultinject-integration] security gates PASS (loopback + rate-limit + sentinel + no-replay)"

# Production binary symbol-leak check.
PROD=$(mktemp)
trap 'rm -f "$OUT" "$PROD"' EXIT
if ! go build -o "$PROD" ./cmd/driver-agent; then
    echo "[faultinject-integration] FAIL: production build of cmd/driver-agent failed"
    exit 1
fi
n=$(nm "$PROD" 2>&1 | grep -ic faultinject || true)
if [[ "$n" -ne 0 ]]; then
    echo "[faultinject-integration] FAIL: production binary contains $n faultinject symbols"
    nm "$PROD" | grep -i faultinject | head
    exit 1
fi
echo "[faultinject-integration] production binary clean: 0 faultinject symbols"

echo "[faultinject-integration] ALL CHECKS PASS"
