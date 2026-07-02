#!/usr/bin/env bats
# deploy_test.bats — WT-2-deploy-scripts spec §6.2 test matrix.
# Runner: bats-core, or fallback ../multi-agent/deploy/_bats_shim.sh.

setup() {
    DEPLOY="${BATS_TEST_DIRNAME}/deploy.sh"
    ROOT="${BATS_TEST_DIRNAME}/../.."   # repo root: multi-agent/
    TMP="$(mktemp -d)"
    LOOM_HOME="$TMP/loom"
    mkdir -p "$LOOM_HOME"
}

teardown() {
    # Best-effort: reap anything deploy.sh might have spawned in a test.
    if [[ -d "$LOOM_HOME/.pids" ]]; then
        for pf in "$LOOM_HOME"/.pids/*.pid; do
            [[ -f "$pf" ]] || continue
            pid=$(cat "$pf" 2>/dev/null || echo)
            [[ -n "$pid" ]] && kill -KILL "$pid" 2>/dev/null || true
        done
    fi
    rm -rf "$TMP"
}

# ----- T1: stub dry-run output shape ---------------------------------
@test "T1: stub dry-run emits parseable JSON with 4 component_ports keys" {
    LOOM_TEST_HOSTNAME=h1 run bash "$DEPLOY" --stub --dry-run --loom-home "$LOOM_HOME"
    [ "$status" -eq 0 ]
    printf '%s' "$output" | jq -e '.mode == "stub"' >/dev/null
    printf '%s' "$output" | jq -e '.component_ports | keys | length == 4' >/dev/null
    printf '%s' "$output" | jq -e '.component_ports.agentserver_stub == 18080' >/dev/null
    printf '%s' "$output" | jq -e '.component_ports.observer == 18091' >/dev/null
    printf '%s' "$output" | jq -e '.component_ports.driver == 18092' >/dev/null
    printf '%s' "$output" | jq -e '.component_ports.slave == 18093' >/dev/null
}

# ----- T2: prod dry-run omits agentserver_stub ------------------------
@test "T2: prod dry-run omits agentserver_stub from component_ports and planned_commands" {
    LOOM_TEST_HOSTNAME=h1 run bash "$DEPLOY" --prod --dry-run --loom-home "$LOOM_HOME"
    [ "$status" -eq 0 ]
    printf '%s' "$output" | jq -e '.component_ports | has("agentserver_stub") | not' >/dev/null
    # No planned command references agentserver-stub binary.
    ! printf '%s' "$output" | jq -e '[.planned_commands[] | .[0]] | any(test("agentserver-stub"))' >/dev/null
}

# ----- T3: well-known port blacklist ---------------------------------
@test "T3: blacklist rejects well-known ports (3389=RDP)" {
    run bash "$DEPLOY" --stub --observer-port 3389 --dry-run --loom-home "$LOOM_HOME"
    [ "$status" -eq 2 ]
    [[ "$output" == *"well-known port"* ]]
}

@test "T3-sqrt: blacklist rejects port 5432 (Postgres)" {
    run bash "$DEPLOY" --stub --slave-port 5432 --dry-run --loom-home "$LOOM_HOME"
    [ "$status" -eq 2 ]
    [[ "$output" == *"well-known port"* ]]
}

# ----- T4: port range check ------------------------------------------
@test "T4: port > 65535 rejected" {
    run bash "$DEPLOY" --stub --observer-port 100000 --dry-run --loom-home "$LOOM_HOME"
    [ "$status" -eq 2 ]
    [[ "$output" == *"out of range"* ]]
}

@test "T4-low: port < 1024 rejected" {
    run bash "$DEPLOY" --stub --observer-port 80 --dry-run --loom-home "$LOOM_HOME"
    [ "$status" -eq 2 ]
    # 80 is in the blacklist too; either message is acceptable, both trip preflight.
    [[ "$output" == *"out of range"* || "$output" == *"well-known port"* ]]
}

# ----- T5: pairwise-distinct ports -----------------------------------
@test "T5: colliding ports rejected" {
    run bash "$DEPLOY" --stub --observer-port 18091 --driver-port 18091 --dry-run --loom-home "$LOOM_HOME"
    [ "$status" -eq 2 ]
    [[ "$output" == *"used by both"* ]]
}

# ----- T7: no bind-host override ------------------------------------
@test "T7-a: literal 0.0.0.0 does not appear in deploy.sh" {
    n=$(grep -F -c '0.0.0.0' "$DEPLOY" || true)
    [ "$n" = "0" ]
}

@test "T7-b: no --listen followed by \$ interpolation in deploy.sh" {
    n=$(grep -Ec '\-\-listen[[:space:]]+["'"'"']?(\$|\\\$\{)' "$DEPLOY" || true)
    [ "$n" = "0" ]
}

@test "T7-c: --listen 127.0.0.1: literal appears at least once" {
    n=$(grep -Ec '\-\-listen[[:space:]]+["'"'"']?127\.0\.0\.1:' "$DEPLOY" || true)
    [ "$n" -ge 1 ]
}

@test "T7-d: LOOM_STUB_LISTEN env override name is absent from deploy.sh source" {
    n=$(grep -Fc 'LOOM_STUB_LISTEN' "$DEPLOY" || true)
    [ "$n" = "0" ]
}

@test "T7-e: LOOM_STUB_LISTEN env at runtime does not change bind (dry-run argv unchanged)" {
    LOOM_STUB_LISTEN='0.0.0.0:18080' LOOM_TEST_HOSTNAME=h1 \
        run bash "$DEPLOY" --stub --dry-run --loom-home "$LOOM_HOME"
    [ "$status" -eq 0 ]
    # The argv for agentserver-stub must still say 127.0.0.1:
    printf '%s' "$output" | jq -e '.planned_commands[0][2] == "127.0.0.1:18080"' >/dev/null
    ! printf '%s' "$output" | jq -e '[.planned_commands[] | flatten | .[]] | any(test("0\\.0\\.0\\.0"))' >/dev/null
}

# ----- T8: secret redaction in --dry-run ------------------------------
@test "T8: LOOM_API_KEY does not appear in --dry-run stdout+stderr" {
    LOOM_API_KEY='SECRET_TOKEN_1234' \
    LOOM_TEST_HOSTNAME=h1 \
        run bash "$DEPLOY" --prod --dry-run --loom-home "$LOOM_HOME"
    [ "$status" -eq 0 ]
    ! printf '%s' "$output" | grep -q 'SECRET_TOKEN_1234'
    # Also base64 form (jq might encode).
    b64=$(printf '%s' 'SECRET_TOKEN_1234' | base64)
    ! printf '%s' "$output" | grep -q "$b64"
}

# ----- T10: fail-fast preflight first executable line ----------------
@test "T10: deploy.sh first executable line is set -euo pipefail" {
    first=$(grep -nE '^[^#[:space:]]' "$DEPLOY" | head -1 | cut -d: -f2-)
    [ "$first" = "set -euo pipefail" ]
}

# ----- T15d: model-key grep invariant --------------------------------
@test "T15d: 'allow-model-key-passthrough' appears exactly 3 times in deploy.sh" {
    n=$(grep -c 'allow-model-key-passthrough' "$DEPLOY" || true)
    [ "$n" -eq 3 ]
}

# ----- T15c(i)/(iv): model-key gate ----------------------------------
@test "T15c-iv: --allow-model-key-passthrough + --stub → exit 2" {
    run bash "$DEPLOY" --stub --allow-model-key-passthrough --dry-run --loom-home "$LOOM_HOME"
    [ "$status" -eq 2 ]
    [[ "$output" == *"only valid with --mode prod"* ]]
}

# ----- T16: install.ps1 boundary (spec §0) ---------------------------
@test "T16: deploy/windows/slave/install.ps1 is unchanged vs origin" {
    if ! git -C "$ROOT/.." rev-parse --verify origin/paper/v3-integration >/dev/null 2>&1; then
        skip "origin/paper/v3-integration not available"
    fi
    d=$(git -C "$ROOT/.." diff origin/paper/v3-integration -- multi-agent/deploy/windows/slave/install.ps1 || true)
    [ -z "$d" ]
}
