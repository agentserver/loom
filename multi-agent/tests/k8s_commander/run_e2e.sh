#!/usr/bin/env bash
# Commander state-persistence e2e: assert the multi-pod fix works.
#
# Topology stood up by manifests.yaml:
#   - 3 × observer-server (replicas) behind a Service with NO sessionAffinity
#   - 1 × postgres (shared store, where commander_logins + commander_sessions live)
#   - 1 × mock-agentserver (deterministic /api/oauth2/* + /api/agent/whoami)
#
# What the driver proves end-to-end:
#   1. POST /api/commander/login on pod A → 200 with login_id
#   2. GET  /api/commander/login/poll?id=lid on pod B → 200 pending
#      (pod B had no in-memory state for this lid — pre-fix it would 404)
#   3. GET  /poll on pod A or pod C → eventually 200 ok + Set-Cookie
#      (the cookie's sid is randomized via crypto/rand on whichever pod ran [C1])
#   4. The cookie authenticates on a DIFFERENT pod (asserted via
#      /api/commander/tree, which exercises CommanderIdentity → GetSession
#      against the shared DB)
#   5. POST /api/commander/logout on one pod → cookie rejected on every pod
#      (cross-pod logout — implicit fix that came with this PR)
#
# Each numbered step prints "PASS step N: <desc>". Any failure exits non-zero
# with the curl response body for diagnosis.

set -euo pipefail

NS="commander-e2e"
SERVICE_PORT_LOCAL=18190   # local port that forwards to the Service (round-robin)
declare -A POD_PORT        # podName -> local port (per-pod targeted forwards)

cleanup() {
    set +e
    if [ -n "${PF_PIDS:-}" ]; then
        for pid in $PF_PIDS; do kill "$pid" 2>/dev/null; done
        wait 2>/dev/null
    fi
}
trap cleanup EXIT

log() { printf '[e2e] %s\n' "$*" >&2; }
fail() { printf '[e2e][FAIL] %s\n' "$*" >&2; exit 1; }
pass() { printf '[e2e][PASS] %s\n' "$*" >&2; }

# Wait for the deployment's rollout to settle (no stale pods mid-restart).
log "waiting for observer-server rollout to settle..."
kubectl -n "$NS" rollout status deploy/observer-server --timeout=180s >/dev/null
kubectl -n "$NS" wait --for=condition=Ready pod \
    -l app=observer-server,pod-template-hash="$(kubectl -n "$NS" get deploy observer-server -o jsonpath='{.metadata.annotations.deployment\.kubernetes\.io/revision}')" \
    --timeout=60s >/dev/null 2>&1 || true

# Pick only pods from the deployment's CURRENT ReplicaSet so a hash of stale
# Terminating pods doesn't confuse the test.
CURRENT_RS=$(kubectl -n "$NS" get rs -l app=observer-server \
    --sort-by=.metadata.creationTimestamp -o jsonpath='{.items[-1:].metadata.name}')
PODS=( $(kubectl -n "$NS" get pods -l app=observer-server \
    -o jsonpath="{.items[?(@.metadata.ownerReferences[0].name=='$CURRENT_RS')].metadata.name}") )
[ "${#PODS[@]}" -eq 3 ] || fail "expected 3 observer pods on current RS '$CURRENT_RS', got ${#PODS[@]}: ${PODS[*]}"
log "pods: ${PODS[*]}"

# Port-forward the Service (round-robin) + each pod individually.
PF_PIDS=""
kubectl -n "$NS" port-forward service/observer "$SERVICE_PORT_LOCAL":8090 >/tmp/pf-svc.log 2>&1 &
PF_PIDS="$PF_PIDS $!"
NEXT_PORT=18191
for pod in "${PODS[@]}"; do
    POD_PORT[$pod]=$NEXT_PORT
    kubectl -n "$NS" port-forward "pod/$pod" "$NEXT_PORT":8090 >"/tmp/pf-$pod.log" 2>&1 &
    PF_PIDS="$PF_PIDS $!"
    NEXT_PORT=$((NEXT_PORT+1))
done

# Wait for every forward to accept connections.
wait_port() {
    local port=$1
    for _ in $(seq 1 30); do
        if curl -sS -o /dev/null -w '' "http://127.0.0.1:$port/readyz" 2>/dev/null; then return 0; fi
        sleep 0.5
    done
    fail "port-forward on :$port never became reachable"
}
wait_port "$SERVICE_PORT_LOCAL"
for pod in "${PODS[@]}"; do wait_port "${POD_PORT[$pod]}"; done

POD_A="${PODS[0]}"
POD_B="${PODS[1]}"
POD_C="${PODS[2]}"
URL_A="http://127.0.0.1:${POD_PORT[$POD_A]}"
URL_B="http://127.0.0.1:${POD_PORT[$POD_B]}"
URL_C="http://127.0.0.1:${POD_PORT[$POD_C]}"
log "URL_A=$URL_A (pod $POD_A)"
log "URL_B=$URL_B (pod $POD_B)"
log "URL_C=$URL_C (pod $POD_C)"

# -----------------------------------------------------------------------------
# Step 1: POST /login on pod A.
# -----------------------------------------------------------------------------
login_resp=$(curl -sS -X POST -w $'\n%{http_code}' "$URL_A/api/commander/login")
login_body=$(printf '%s\n' "$login_resp" | head -n -1)
login_code=$(printf '%s\n' "$login_resp" | tail -n 1)
[ "$login_code" = "200" ] || fail "step 1: expected 200, got $login_code, body=$login_body"
LOGIN_ID=$(printf '%s' "$login_body" | sed -nE 's/.*"login_id"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/p')
[ -n "$LOGIN_ID" ] || fail "step 1: no login_id in response: $login_body"
pass "step 1: POST /login on pod A returned login_id=$LOGIN_ID"

# -----------------------------------------------------------------------------
# Step 2: GET /poll on pod B (different pod) must succeed (pre-fix: 404).
# Mock returns authorization_pending on the first /token call so we expect
# a 200 with "pending".
# -----------------------------------------------------------------------------
poll1_resp=$(curl -sS -w $'\n%{http_code}' "$URL_B/api/commander/login/poll?id=$LOGIN_ID")
poll1_body=$(printf '%s\n' "$poll1_resp" | head -n -1)
poll1_code=$(printf '%s\n' "$poll1_resp" | tail -n 1)
[ "$poll1_code" = "200" ] || fail "step 2: pod B /poll on lid born-on-A expected 200, got $poll1_code, body=$poll1_body"
printf '%s' "$poll1_body" | grep -q '"pending"' || fail "step 2: expected pending, got $poll1_body"
pass "step 2: GET /poll on pod B returned 200 pending (pre-fix this would have been 404)"

# Mock auto-approves on the SECOND /token call. The next /poll triggers a
# fresh PollOnce which lands [C1]. But the throttle (next_poll_at) blocks it
# for a few seconds — use the Service-level URL so we can also confirm the
# round-robin Service path works. Retry up to 12 times with 1s spacing.
log "polling Service URL (round-robin) until [C1] completes..."
COOKIE_HEADER=""
for attempt in $(seq 1 12); do
    sleep 1
    poll_resp=$(curl -sS -i -w '\n%{http_code}' "http://127.0.0.1:$SERVICE_PORT_LOCAL/api/commander/login/poll?id=$LOGIN_ID")
    code=$(printf '%s\n' "$poll_resp" | tail -n 1)
    if [ "$code" = "200" ] && printf '%s\n' "$poll_resp" | grep -q '"ok"'; then
        COOKIE_HEADER=$(printf '%s\n' "$poll_resp" | grep -i '^set-cookie:' | head -n 1 || true)
        break
    fi
done
[ -n "$COOKIE_HEADER" ] || fail "step 3: never got [C1] OK after 12 attempts; last poll: $poll_resp"
COOKIE_VALUE=$(printf '%s' "$COOKIE_HEADER" | sed -nE 's/.*commander_sess=([^;[:space:]]+).*/\1/Ip')
[ -n "$COOKIE_VALUE" ] || fail "step 3: Set-Cookie missing commander_sess=...: $COOKIE_HEADER"
pass "step 3: [C1] returned 200 ok + Set-Cookie; sid prefix=${COOKIE_VALUE:0:8}…"

# -----------------------------------------------------------------------------
# Step 4: cookie must authenticate on EVERY pod (CommanderIdentity ->
# GetSession against the shared DB). Hit /api/commander/tree on each pod
# directly. Empty tree (200 + JSON) is fine; auth pass is what we assert.
# -----------------------------------------------------------------------------
for i in 0 1 2; do
    pod="${PODS[$i]}"
    url="http://127.0.0.1:${POD_PORT[$pod]}"
    resp=$(curl -sS -w '\n%{http_code}' --cookie "commander_sess=$COOKIE_VALUE" "$url/api/commander/tree")
    code=$(printf '%s\n' "$resp" | tail -n 1)
    body=$(printf '%s\n' "$resp" | head -n -1)
    [ "$code" = "200" ] || fail "step 4: pod $pod /tree expected 200, got $code, body=$body"
done
pass "step 4: cookie authenticates on all 3 pods (cross-pod GetSession via shared DB)"

# -----------------------------------------------------------------------------
# Step 5: logout on pod A invalidates cookie everywhere (the implicit
# cross-pod-logout fix).
# -----------------------------------------------------------------------------
logout_code=$(curl -sS -o /dev/null -w '%{http_code}' -X POST --cookie "commander_sess=$COOKIE_VALUE" "$URL_A/api/commander/logout")
[ "$logout_code" = "200" ] || fail "step 5: logout expected 200, got $logout_code"

for i in 0 1 2; do
    pod="${PODS[$i]}"
    url="http://127.0.0.1:${POD_PORT[$pod]}"
    code=$(curl -sS -o /dev/null -w '%{http_code}' --cookie "commander_sess=$COOKIE_VALUE" "$url/api/commander/tree")
    [ "$code" = "401" ] || fail "step 5: pod $pod expected 401 after logout, got $code"
done
pass "step 5: logout on pod A invalidates cookie on every pod"

# -----------------------------------------------------------------------------
# Step 6: cap stress — drive 1100 concurrent /login through the Service.
# pg_advisory_xact_lock must hold the in-flight pending logins at <=
# MaxActiveLogins (1024). The test is "200 count never exceeds the cap".
# We tolerate curl/portforward churn under heavy parallelism since
# kubectl port-forward serializes through a single tunnel.
#
# To make the 429-observed assertion stable across runs (the DB may
# already hold rows from previous attempts), TRUNCATE first if we have
# psql access via kubectl exec. Without that, we skip the
# "429s-observed" assertion and only assert correctness (no overrun).
# -----------------------------------------------------------------------------
if kubectl -n "$NS" exec deploy/postgres -- \
        psql -U observer -d observer -c 'TRUNCATE commander_logins, commander_sessions' \
        >/dev/null 2>&1; then
    log "step 6: pre-truncated commander_logins for a clean cap stress"
    CAN_ASSERT_429=true
else
    warn "step 6: could not TRUNCATE commander_logins (no kubectl exec on postgres?); 429-observed assertion relaxed"
    CAN_ASSERT_429=false
fi

log "step 6: launching 1100 concurrent POST /login through the Service..."
TMPDIR=$(mktemp -d)
# Cap parallelism at 16 — port-forward is a single tunnel; higher concurrency
# just produces connection-resets that look like transport errors.
seq 1 1100 | xargs -P 16 -I{} sh -c "
    curl -sS -o /dev/null -w '%{http_code}\n' --max-time 15 -X POST 'http://127.0.0.1:$SERVICE_PORT_LOCAL/api/commander/login' >>'$TMPDIR/codes' 2>/dev/null || echo 'curl-fail' >>'$TMPDIR/codes'
"
ok_count=$(grep -c '^200$' "$TMPDIR/codes" || true)
cap_count=$(grep -c '^429$' "$TMPDIR/codes" || true)
err_count=$(grep -vc '^\(200\|429\)$' "$TMPDIR/codes" || true)
log "step 6: 200=$ok_count, 429=$cap_count, other/err=$err_count, total=$(wc -l <"$TMPDIR/codes")"
# Cap correctness: 200s must never exceed MaxActiveLogins, period.
[ "$ok_count" -le 1024 ] || fail "step 6: 200 count $ok_count exceeds cap 1024. pg_advisory_xact_lock NOT serializing"
if [ "$CAN_ASSERT_429" = "true" ]; then
    [ "$cap_count" -gt 0 ] || fail "step 6 (clean DB): no 429s observed despite 1100 requests; cap not enforced"
    [ "$ok_count" -ge 1000 ] || fail "step 6 (clean DB): only $ok_count 200s — transport churn too high to trust the signal"
fi
rm -rf "$TMPDIR"
pass "step 6: cap holds ($ok_count <= 1024); 429s=$cap_count, transport errs=$err_count"

printf '\n[e2e] ALL %d STEPS PASSED — multi-pod commander state persistence verified.\n' 6
