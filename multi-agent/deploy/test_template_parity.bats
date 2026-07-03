#!/usr/bin/env bats
# T19 — observer template parity (spec §3.2 clause 2).
#
# Locks the invariant that deploy/windows/observer/config.yaml.template
# is a byte-for-byte duplicate of deploy/linux/observer/config.yaml.template
# (modulo the single-line header) and that the placeholder set exactly
# matches the golden list. Any Linux-side placeholder addition/rename
# breaks the build here until the Windows template + inline-render in
# deploy.ps1 are updated.

setup() {
    LINUX="${BATS_TEST_DIRNAME}/linux/observer/config.yaml.template"
    WIN="${BATS_TEST_DIRNAME}/windows/observer/config.yaml.template"
    EXPECTED="${BATS_TEST_DIRNAME}/windows/observer/expected_placeholders.txt"
}

@test "T19-a: windows template first line is the header comment" {
    first="$(head -n 1 "$WIN")"
    [[ "$first" == \#\ duplicated\ from\ linux/observer/config.yaml.template* ]]
}

@test "T19-b: windows template body (tail -n +2) byte-equals linux template" {
    diff <(tail -n +2 "$WIN") "$LINUX"
}

@test "T19-c: linux template placeholder set equals golden list" {
    diff <(grep -oE '__[A-Z_]+__' "$LINUX" | sort -u) <(sort -u "$EXPECTED")
}

@test "T19-d: golden list is not empty" {
    lines="$(grep -Ec '__[A-Z_]+__' "$EXPECTED")"
    [ "$lines" -ge 1 ]
}
