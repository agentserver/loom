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
    # Also match any lingering python shim (fake-*-agent) whose stub
    # or observer bound our fixed ports (18080/18091/18092) — a
    # subsequent test spawning a new stub on :18080 would fail to bind
    # if the previous run's process wasn't fully reaped.
    if [[ -d "$LOOM_HOME/.pids" ]]; then
        for pf in "$LOOM_HOME"/.pids/*.pid; do
            [[ -f "$pf" ]] || continue
            pid=$(cat "$pf" 2>/dev/null || echo)
            [[ -n "$pid" ]] && kill -KILL "$pid" 2>/dev/null || true
        done
    fi
    # Belt-and-braces: kill any child python HTTP server binding our
    # fixed test ports. Under bats shim these leaks accumulate across
    # tests and break port binding on the next stub spawn.
    for port in 18080 18091 18092 18093; do
        pid=$(ss -tlnp 2>/dev/null | awk -v p=":$port" '$4~p{print $NF}' | grep -oE 'pid=[0-9]+' | head -1 | cut -d= -f2 || true)
        [[ -n "$pid" ]] && kill -KILL "$pid" 2>/dev/null || true
    done
    sleep 0.3
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

# ----- T15: env whitelist DROPS sensitive parent env keys ------------
@test "T15: emit_whitelisted_env drops AWS_/GITHUB_TOKEN/DOCKER_/NPM_ keys" {
    # Source deploy.sh's env-whitelist function via a tiny extractor.
    # We eval only the helper function definitions (no top-level code).
    extract=$(awk '
        /^emit_whitelisted_env\(\)/,/^}$/ { print }
        /^ALWAYS_ENV_KEYS=/ { print }
        /^IFSET_ENV_KEYS=/  { print }
        /^readonly ALWAYS_ENV_KEYS/ { print }
        /^readonly IFSET_ENV_KEYS/  { print }
    ' "$DEPLOY")
    # These arrays are declared with `readonly` at the top of deploy.sh —
    # strip the readonly-marker line so eval in the subshell doesn't refuse
    # to re-declare them.
    extract=$(printf '%s\n' "$extract" | sed -E 's/^readonly[[:space:]]+//')

    out=$(
        set +e
        MODE=stub
        ALLOW_MODEL_KEY=0
        AWS_ACCESS_KEY_ID=SHOULD_NOT_LEAK \
        GITHUB_TOKEN=SHOULD_NOT_LEAK \
        DOCKER_CONFIG=/etc/docker \
        NPM_TOKEN=SHOULD_NOT_LEAK \
        bash -c "MODE=stub ALLOW_MODEL_KEY=0; $extract; emit_whitelisted_env" \
             2>/dev/null
    )
    ! echo "$out" | grep -q '^AWS_ACCESS_KEY_ID='
    ! echo "$out" | grep -q '^GITHUB_TOKEN='
    ! echo "$out" | grep -q '^DOCKER_CONFIG='
    ! echo "$out" | grep -q '^NPM_TOKEN='
    ! echo "$out" | grep -q 'SHOULD_NOT_LEAK'
}

@test "T15b: emit_whitelisted_env passes always-allowed + if-set + LOOM_* keys" {
    extract=$(awk '
        /^emit_whitelisted_env\(\)/,/^}$/ { print }
        /^ALWAYS_ENV_KEYS=/ { print }
        /^IFSET_ENV_KEYS=/  { print }
        /^readonly ALWAYS_ENV_KEYS/ { print }
        /^readonly IFSET_ENV_KEYS/  { print }
    ' "$DEPLOY")
    extract=$(printf '%s\n' "$extract" | sed -E 's/^readonly[[:space:]]+//')

    out=$(
        # Present a controlled env (env -i strips everything else).
        /usr/bin/env -i \
            PATH="/usr/bin:/bin" HOME=/root LANG=C LC_ALL=C TZ=UTC USER=root \
            MOCK_MODEL_URL=http://127.0.0.1:9090 \
            AGENTSERVER_ROOT=/repo/agentserver \
            LOOM_OBSERVER_URL=http://127.0.0.1:18091 \
            LOOM_=empty_prefix_only \
            bash -c "MODE=stub ALLOW_MODEL_KEY=0; $extract; emit_whitelisted_env"
    )
    echo "$out" | grep -q '^PATH='
    echo "$out" | grep -q '^HOME=/root$'
    echo "$out" | grep -q '^MOCK_MODEL_URL=http://127.0.0.1:9090$'
    echo "$out" | grep -q '^AGENTSERVER_ROOT=/repo/agentserver$'
    echo "$out" | grep -q '^LOOM_OBSERVER_URL=http://127.0.0.1:18091$'
    # `LOOM_=empty_prefix_only` must NOT slip through (guard: len(k) > 5).
    ! echo "$out" | grep -q '^LOOM_=empty_prefix_only'
}

@test "T15c-i: --stub never propagates OPENAI_API_KEY / ANTHROPIC_API_KEY" {
    extract=$(awk '
        /^emit_whitelisted_env\(\)/,/^}$/ { print }
        /^ALWAYS_ENV_KEYS=/ { print }
        /^IFSET_ENV_KEYS=/  { print }
        /^readonly ALWAYS_ENV_KEYS/ { print }
        /^readonly IFSET_ENV_KEYS/  { print }
    ' "$DEPLOY")
    extract=$(printf '%s\n' "$extract" | sed -E 's/^readonly[[:space:]]+//')

    out=$(
        /usr/bin/env -i \
            PATH=/usr/bin OPENAI_API_KEY=STUB_KEY_9zzz ANTHROPIC_API_KEY=STUB_KEY_8yyy \
            bash -c "MODE=stub ALLOW_MODEL_KEY=1; $extract; emit_whitelisted_env" 2>/dev/null
    )
    ! echo "$out" | grep -q '^OPENAI_API_KEY='
    ! echo "$out" | grep -q '^ANTHROPIC_API_KEY='
}

@test "T15c-ii: --prod without --allow-model-key-passthrough drops OPENAI_API_KEY" {
    extract=$(awk '
        /^emit_whitelisted_env\(\)/,/^}$/ { print }
        /^ALWAYS_ENV_KEYS=/ { print }
        /^IFSET_ENV_KEYS=/  { print }
        /^readonly ALWAYS_ENV_KEYS/ { print }
        /^readonly IFSET_ENV_KEYS/  { print }
    ' "$DEPLOY")
    extract=$(printf '%s\n' "$extract" | sed -E 's/^readonly[[:space:]]+//')

    out=$(
        /usr/bin/env -i \
            PATH=/usr/bin OPENAI_API_KEY=PROD_KEY_1234 \
            bash -c "MODE=prod ALLOW_MODEL_KEY=0; $extract; emit_whitelisted_env" 2>/dev/null
    )
    ! echo "$out" | grep -q '^OPENAI_API_KEY='
}

@test "T15c-iii: --prod --allow-model-key-passthrough passes OPENAI_API_KEY with WARN" {
    extract=$(awk '
        /^emit_whitelisted_env\(\)/,/^}$/ { print }
        /^ALWAYS_ENV_KEYS=/ { print }
        /^IFSET_ENV_KEYS=/  { print }
        /^readonly ALWAYS_ENV_KEYS/ { print }
        /^readonly IFSET_ENV_KEYS/  { print }
    ' "$DEPLOY")
    extract=$(printf '%s\n' "$extract" | sed -E 's/^readonly[[:space:]]+//')

    # Capture stdout + stderr separately.
    tmp_out="$TMP/prod_pass.out"; tmp_err="$TMP/prod_pass.err"
    /usr/bin/env -i PATH=/usr/bin OPENAI_API_KEY=PROD_KEY_9999 \
        bash -c "MODE=prod ALLOW_MODEL_KEY=1; $extract; emit_whitelisted_env" \
        >"$tmp_out" 2>"$tmp_err" || true

    grep -q '^OPENAI_API_KEY=PROD_KEY_9999$' "$tmp_out"
    grep -q 'passing OPENAI_API_KEY through' "$tmp_err"
}

# ----- T-mode-invalid: --mode X rejects invalid values -------------
@test "T-mode-invalid: --mode nope exits 2" {
    run bash "$DEPLOY" --mode nope --dry-run --loom-home "$LOOM_HOME"
    [ "$status" -eq 2 ]
    [[ "$output" == *"invalid --mode value"* ]]
}

@test "T-mode-invalid-b: --mode STUB (wrong case) exits 2" {
    run bash "$DEPLOY" --mode STUB --dry-run --loom-home "$LOOM_HOME"
    [ "$status" -eq 2 ]
}

@test "T-mode-missing-arg: --mode (no value) exits 2, not 1" {
    run bash "$DEPLOY" --mode
    [ "$status" -eq 2 ]
    [[ "$output" == *"--mode requires a value"* ]]
}

@test "T-observer-port-missing-arg: --observer-port (no value) exits 2" {
    run bash "$DEPLOY" --observer-port
    [ "$status" -eq 2 ]
    [[ "$output" == *"--observer-port requires a value"* ]]
}

# ----- T1b: --dry-run is side-effect-free ---------------------------
@test "T1b: --dry-run does not create \$LOOM_HOME on disk" {
    fresh="$TMP/nonexistent-loom-$$"
    # Sanity: the path really does not exist.
    [ ! -e "$fresh" ]
    LOOM_TEST_HOSTNAME=h1 run bash "$DEPLOY" --stub --dry-run --loom-home "$fresh"
    [ "$status" -eq 0 ]
    [ ! -e "$fresh" ]
}

# ----- T6/T17/T18 runtime rows: full-stack shim-driven bring-up -----
#
# Fresh-review P1-9/P1-10/P1-11 (round 8): the previous suite only
# unit-tested emit_whitelisted_env in isolation; spawn_bg /
# run_whitelisted whitelisting was never exercised end-to-end, and
# the readiness-timeout / spawn-then-exit / -shutdown contracts were
# not runtime-tested. These rows drive `deploy.sh --stub` against a
# fake --bin-dir populated with our shims.

setup_shim_bindir() {
    # Populate the SHIM_BIN var and export LOOM_SHIM_LOG for the caller.
    # Invoked directly (`setup_shim_bindir`, NOT `$(setup_shim_bindir)`)
    # so the export propagates into the current test's shell.
    export LOOM_SHIM_LOG="$TMP/shim.log"
    SHIM_BIN="$TMP/bin"
    mkdir -p "$SHIM_BIN"
    local arch
    arch=$(uname -m | sed -e s/x86_64/amd64/ -e s/aarch64/arm64/)
    install -m 0755 "$BATS_TEST_DIRNAME/testdata/fake-agentserver-stub.sh" "$SHIM_BIN/agentserver-stub"
    install -m 0755 "$BATS_TEST_DIRNAME/testdata/fake-observer-server.sh"  "$SHIM_BIN/observer-server.linux-${arch}"
    install -m 0755 "$BATS_TEST_DIRNAME/testdata/fake-slave-agent.sh"      "$SHIM_BIN/slave-agent.linux-${arch}"
    install -m 0755 "$BATS_TEST_DIRNAME/testdata/fake-driver-agent.sh"     "$SHIM_BIN/driver-agent.linux-${arch}"
}

@test "T6-runtime: stub bring-up passes 127.0.0.1:PORT to agentserver-stub argv" {
    command -v python3 >/dev/null || skip "python3 needed for shim server"
    command -v yq >/dev/null || skip "yq needed for stub-mode yaml patching"
    setup_shim_bindir; bin="$SHIM_BIN"
    LOOM_TEST_HOSTNAME=h1 run bash "$DEPLOY" --stub --loom-home "$LOOM_HOME" --bin-dir "$bin"
    [ "$status" -eq 0 ]
    # Assert stub was invoked with --listen 127.0.0.1:18080
    grep -q "argv:.*\[--listen\] \[127\.0\.0\.1:18080\]" "$LOOM_SHIM_LOG"
    # Reap.
    bash "$DEPLOY" --shutdown --loom-home "$LOOM_HOME" >/dev/null 2>&1 || true
}

@test "T6c-runtime: slave config has server.url=http://127.0.0.1:STUB and daemon.auto_start=false" {
    command -v python3 >/dev/null || skip "python3 needed for shim server"
    command -v yq >/dev/null || skip "yq needed for stub-mode yaml patching"
    setup_shim_bindir; bin="$SHIM_BIN"
    LOOM_TEST_HOSTNAME=h1 run bash "$DEPLOY" --stub --loom-home "$LOOM_HOME" --bin-dir "$bin"
    [ "$status" -eq 0 ]
    grep -q 'server\.url\|server.url' "$LOOM_HOME/slave/config.yaml"
    yq eval '.server.url' "$LOOM_HOME/slave/config.yaml" | grep -q '127\.0\.0\.1:18080'
    [ "$(yq eval '.daemon.auto_start' "$LOOM_HOME/slave/config.yaml")" = "false" ]
    bash "$DEPLOY" --shutdown --loom-home "$LOOM_HOME" >/dev/null 2>&1 || true
}

@test "T15-runtime: subprocess env excludes AWS_/GITHUB_TOKEN/NPM_TOKEN even in end-to-end run" {
    command -v python3 >/dev/null || skip "python3 needed for shim server"
    command -v yq >/dev/null || skip "yq needed for stub-mode yaml patching"
    setup_shim_bindir; bin="$SHIM_BIN"
    AWS_ACCESS_KEY_ID=SHOULD_NOT_LEAK_AWS \
    GITHUB_TOKEN=SHOULD_NOT_LEAK_GH \
    NPM_TOKEN=SHOULD_NOT_LEAK_NPM \
    LOOM_TEST_HOSTNAME=h1 \
        run bash "$DEPLOY" --stub --loom-home "$LOOM_HOME" --bin-dir "$bin"
    [ "$status" -eq 0 ]
    # Shim log captures env of every shim invocation (stub, observer,
    # slave, driver, stub-issue, yq). None should carry the sentinels.
    ! grep -q 'SHOULD_NOT_LEAK_AWS' "$LOOM_SHIM_LOG"
    ! grep -q 'SHOULD_NOT_LEAK_GH'  "$LOOM_SHIM_LOG"
    ! grep -q 'SHOULD_NOT_LEAK_NPM' "$LOOM_SHIM_LOG"
    bash "$DEPLOY" --shutdown --loom-home "$LOOM_HOME" >/dev/null 2>&1 || true
}

@test "T18-runtime: spawn-then-exit writes .pids/*.pid with live PIDs" {
    command -v python3 >/dev/null || skip "python3 needed for shim server"
    command -v yq >/dev/null || skip "yq needed for stub-mode yaml patching"
    setup_shim_bindir; bin="$SHIM_BIN"
    LOOM_TEST_HOSTNAME=h1 run bash "$DEPLOY" --stub --loom-home "$LOOM_HOME" --bin-dir "$bin"
    [ "$status" -eq 0 ]
    for role in agentserver-stub observer slave driver; do
        pf="$LOOM_HOME/.pids/${role}.pid"
        [ -f "$pf" ]
        pid=$(cat "$pf")
        kill -0 "$pid" 2>/dev/null
    done
    bash "$DEPLOY" --shutdown --loom-home "$LOOM_HOME" >/dev/null 2>&1
}

@test "T18b-runtime: --shutdown reaps every PID and removes .pids/" {
    command -v python3 >/dev/null || skip "python3 needed for shim server"
    command -v yq >/dev/null || skip "yq needed for stub-mode yaml patching"
    setup_shim_bindir; bin="$SHIM_BIN"
    bash "$DEPLOY" --stub --loom-home "$LOOM_HOME" --bin-dir "$bin" >/dev/null
    pids=$(cat "$LOOM_HOME"/.pids/*.pid)
    bash "$DEPLOY" --shutdown --loom-home "$LOOM_HOME"
    sleep 1
    for p in $pids; do
        ! kill -0 "$p" 2>/dev/null
    done
    [ ! -d "$LOOM_HOME/.pids" ]
}

@test "T18c-runtime: readiness timeout on stalled stub → exit 4 with cleanup" {
    command -v python3 >/dev/null || skip "python3 needed for shim server"
    command -v yq >/dev/null || skip "yq needed for stub-mode yaml patching"
    setup_shim_bindir; bin="$SHIM_BIN"
    LOOM_TEST_HOSTNAME=h1 LOOM_SHIM_MODE=stalled LOOM_DEPLOY_READY_TIMEOUT_SEC=2 \
        run bash "$DEPLOY" --stub --loom-home "$LOOM_HOME" --bin-dir "$bin"
    [ "$status" -eq 4 ]
    # Trap should have removed .pids and killed the stub subprocess.
    [ ! -d "$LOOM_HOME/.pids" ]
}

# ----- T16: install.ps1 boundary (spec §0) ---------------------------
@test "T16: deploy/windows/slave/install.ps1 is unchanged vs origin" {
    # Fresh-review P1-7 (round 8): the previous test used `skip` when
    # origin/paper/v3-integration was absent. On GitHub Actions with
    # the default shallow fetch, `origin/paper/v3-integration` isn't
    # present unless the workflow explicitly fetches it — so T16
    # silently skipped (indistinguishable from PASS) on CI that would
    # miss a WT-0 boundary violation. We now fetch the ref on demand
    # (depth 1) and FAIL the test if fetch fails, so the boundary is
    # actually enforced.
    if ! git -C "$ROOT/.." rev-parse --verify origin/paper/v3-integration >/dev/null 2>&1; then
        git -C "$ROOT/.." fetch --depth=1 origin paper/v3-integration >/dev/null 2>&1 \
            || fail "T16: could not fetch origin/paper/v3-integration; the WT-0 install.ps1 boundary CANNOT be enforced without it. Run 'git fetch origin paper/v3-integration' or configure CI with fetch-depth: 0."
    fi
    d=$(git -C "$ROOT/.." diff origin/paper/v3-integration -- multi-agent/deploy/windows/slave/install.ps1)
    [ -z "$d" ]
}
