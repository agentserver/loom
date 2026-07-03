#!/usr/bin/env bats
# T-schema-1..T-schema-3 — assert topology_schema.json accepts the good
# fixture and rejects the bad fixture, per plan Task 1.
#
# Runner: bats-core (Arch: `pacman -S bats`, Ubuntu: `apt install bats`).
# Fallback for environments without bats: `bash test_topology_schema.bats.sh`
# runs the same three assertions in raw bash.
#
# Validator preference: `ajv` (npm i -g ajv-cli) → `python3 -m jsonschema`.
# If neither is present, tests are skipped with a diagnostic.

setup() {
    SCHEMA="${BATS_TEST_DIRNAME}/topology_schema.json"
    VALID="${BATS_TEST_DIRNAME}/testdata/topology_valid.json"
    INVALID="${BATS_TEST_DIRNAME}/testdata/topology_invalid.json"

    if command -v ajv >/dev/null 2>&1; then
        VALIDATOR="ajv"
    elif python3 -c 'import jsonschema' 2>/dev/null; then
        VALIDATOR="python-jsonschema"
    else
        VALIDATOR=""
    fi
}

validate() {
    local schema="$1" doc="$2"
    case "$VALIDATOR" in
        ajv)
            ajv validate -s "$schema" -d "$doc" >/dev/null 2>&1
            ;;
        python-jsonschema)
            python3 -c "
import json, sys
from jsonschema import Draft7Validator
with open('$schema') as f: s = json.load(f)
with open('$doc') as f: d = json.load(f)
errs = list(Draft7Validator(s).iter_errors(d))
sys.exit(0 if not errs else 1)
" 2>/dev/null
            ;;
        *)
            return 77   # bats "skip" convention
            ;;
    esac
}

@test "T-schema-1: schema file parses as JSON" {
    jq empty "$SCHEMA"
}

@test "T-schema-2: valid fixture validates" {
    [ -n "$VALIDATOR" ] || skip "no JSON schema validator on PATH (install ajv or python-jsonschema)"
    validate "$SCHEMA" "$VALID"
}

@test "T-schema-3: invalid fixture rejected" {
    [ -n "$VALIDATOR" ] || skip "no JSON schema validator on PATH"
    ! validate "$SCHEMA" "$INVALID"
}
