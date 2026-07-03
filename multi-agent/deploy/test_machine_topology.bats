#!/usr/bin/env bats
# T11 (spec §6.2), T-machine-topo-* — machine_topology.sh emit contract.
# Run: `bats multi-agent/deploy/test_machine_topology.bats` (bats-core).

setup() {
    HELPER="${BATS_TEST_DIRNAME}/machine_topology.sh"
    SCHEMA="${BATS_TEST_DIRNAME}/topology_schema.json"
    TMP="$(mktemp -d)"
}

teardown() { rm -rf "$TMP"; }

# JSON validator selector — mirrors test_topology_schema.bats.
schema_validate() {
    local doc="$1"
    if command -v ajv >/dev/null 2>&1; then
        ajv validate -s "$SCHEMA" -d "$doc" >/dev/null 2>&1
    elif python3 -c 'import jsonschema' 2>/dev/null; then
        python3 -c "
import json, sys
from jsonschema import Draft7Validator
s = json.load(open('$SCHEMA'))
d = json.load(open('$doc'))
sys.exit(0 if not list(Draft7Validator(s).iter_errors(d)) else 1)
" 2>/dev/null
    else
        return 77
    fi
}

@test "T11: hostname with @ redacted to <hash>@<hash>" {
    LOOM_TEST_HOSTNAME='alice@corp-laptop' \
        run "$HELPER" --mode stub \
            --observer-port 18091 --driver-port 18092 \
            --slave-port 18093 --stub-port 18080
    [ "$status" -eq 0 ]
    host=$(printf '%s' "$output" | jq -r .host)
    [[ "$host" =~ ^[0-9a-f]{8}@[0-9a-f]{8}$ ]]
    # No plaintext leak of either raw side:
    ! echo "$output" | grep -q alice
    ! echo "$output" | grep -q corp-laptop
}

@test "T-machine-topo-plain-hostname: no @ → 8-hex only" {
    LOOM_TEST_HOSTNAME='plain-host' \
        run "$HELPER" --mode prod \
            --observer-port 18091 --driver-port 18092 --slave-port 18093
    [ "$status" -eq 0 ]
    host=$(printf '%s' "$output" | jq -r .host)
    [[ "$host" =~ ^[0-9a-f]{8}$ ]]
    ! echo "$output" | grep -q plain-host
}

@test "T-machine-topo-fields: every §5.1 required key present" {
    LOOM_TEST_HOSTNAME='h1' \
        run "$HELPER" --mode stub \
            --observer-port 18091 --driver-port 18092 \
            --slave-port 18093 --stub-port 18080
    [ "$status" -eq 0 ]
    for key in schema_version host os os_release arch kernel cpu_count mem_bytes mode component_ports collected_at_unix deploy_script_version; do
        printf '%s' "$output" | jq -e ".${key}" >/dev/null
    done
}

@test "T-machine-topo-prod-omits-stub: prod mode has no agentserver_stub key" {
    LOOM_TEST_HOSTNAME='h1' \
        run "$HELPER" --mode prod \
            --observer-port 18091 --driver-port 18092 --slave-port 18093
    [ "$status" -eq 0 ]
    printf '%s' "$output" | jq -e '.component_ports | has("agentserver_stub") | not' >/dev/null
}

@test "T-machine-topo-stub-port-required-in-stub" {
    run "$HELPER" --mode stub \
        --observer-port 18091 --driver-port 18092 --slave-port 18093
    [ "$status" -eq 2 ]
    [[ "$output" == *"--stub-port is required"* ]]
}

@test "T-machine-topo-stub-port-forbidden-in-prod" {
    run "$HELPER" --mode prod \
        --observer-port 18091 --driver-port 18092 --slave-port 18093 \
        --stub-port 18080
    [ "$status" -eq 2 ]
    [[ "$output" == *"--stub-port is only valid with --mode stub"* ]]
}

@test "T-machine-topo-out-file-perm-0600" {
    outf="$TMP/topo.json"
    LOOM_TEST_HOSTNAME='h1' \
        "$HELPER" --mode stub \
        --observer-port 18091 --driver-port 18092 \
        --slave-port 18093 --stub-port 18080 --out "$outf"
    [ -f "$outf" ]
    perms=$(stat -c '%a' "$outf" 2>/dev/null || stat -f '%A' "$outf")
    [ "$perms" = "600" ]
}

@test "T12: emitted topology validates against topology_schema.json" {
    outf="$TMP/topo.json"
    LOOM_TEST_HOSTNAME='alice@corp-laptop' \
        "$HELPER" --mode stub \
        --observer-port 18091 --driver-port 18092 \
        --slave-port 18093 --stub-port 18080 --out "$outf"
    run schema_validate "$outf"
    [ "$status" -eq 0 ] || [ "$status" -eq 77 ]   # 77 = skip when no validator
}

@test "T-machine-topo-fail-fast-preflight-first-line" {
    # Spec §7(c) — first executable line is `set -euo pipefail`.
    first_exec=$(grep -nE '^[^#[:space:]]' "$HELPER" | head -1 | cut -d: -f2-)
    [[ "$first_exec" == "set -euo pipefail" ]]
}
