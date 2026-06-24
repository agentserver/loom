#!/usr/bin/env bash
# discover-thread_test.sh — bash tests for discover-thread.sh.
# TAP-shaped output: "1..<N>" header, "ok N - <name>" / "not ok N - <name>".
# Linux-only /proc tests skip on macOS / WSL-without-/proc / etc.

set -u
TEST_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
SCRIPT="$TEST_DIR/discover-thread.sh"

UUID_OK='019ef3bd-42c8-7731-85b7-7177ae747389'
UUID_OK2='019ef3bd-42c8-7731-85b7-7177ae747390'

passed=0
failed=0
n=0
ok()      { n=$((n+1)); passed=$((passed+1)); echo "ok $n - $1"; }
not_ok()  { n=$((n+1)); failed=$((failed+1)); echo "not ok $n - $1"; }
skip()    { n=$((n+1)); echo "ok $n - $1 # SKIP $2"; }

assert_exit() {
    local label=$1 expected=$2 actual=$3
    if [[ "$expected" == "$actual" ]]; then
        ok "$label (exit=$expected)"
    else
        not_ok "$label (expected exit=$expected, got=$actual)"
    fi
}

assert_stdout() {
    local label=$1 expected=$2 actual=$3
    if [[ "$expected" == "$actual" ]]; then
        ok "$label (stdout matches)"
    else
        not_ok "$label (expected stdout=$expected, got=$actual)"
    fi
}

# 1) Source 1 (env): valid UUID returns immediately.
tmpdir=$(mktemp -d)
out=$(CODEX_THREAD_ID="$UUID_OK" CODEX_HOME="$tmpdir" "$SCRIPT" 2>/dev/null)
rc=$?
assert_exit "env_valid_uuid_returns_immediately" 0 "$rc"
assert_stdout "env_valid_uuid_returns_immediately stdout" "$UUID_OK" "$out"
rm -rf "$tmpdir"

# 2) Source 1: invalid format falls through; CODEX_HOME points at empty dir
#    so the /proc walk does NOT match anything from the outer runner.
#    Disable set -e for the command since we expect non-zero. Capture
#    stderr to a file rather than `|| true` so we get the SCRIPT's exit
#    code, not `true`'s.
tmpdir=$(mktemp -d)
errfile=$(mktemp)
set +e
CODEX_THREAD_ID='thr-from-env' CODEX_HOME="$tmpdir" "$SCRIPT" >/dev/null 2>"$errfile"
rc=$?
set -e
assert_exit "env_invalid_format_falls_through" 1 "$rc"
rm -rf "$tmpdir" "$errfile"

# 3) Source 1: unexpanded placeholder.
tmpdir=$(mktemp -d)
set +e
CODEX_THREAD_ID='${CODEX_THREAD_ID}' CODEX_HOME="$tmpdir" "$SCRIPT" >/dev/null 2>/dev/null
rc=$?
set -e
assert_exit "env_unexpanded_placeholder_falls_through" 1 "$rc"
rm -rf "$tmpdir"

# 4) Source 1: whitespace-only.
tmpdir=$(mktemp -d)
set +e
CODEX_THREAD_ID='   ' CODEX_HOME="$tmpdir" "$SCRIPT" >/dev/null 2>/dev/null
rc=$?
set -e
assert_exit "env_whitespace_falls_through" 1 "$rc"
rm -rf "$tmpdir"

# 5) Source 2 (Linux only): single /proc fd match returns UUID. Uses the
#    parent-holds-fd, script-runs-as-child topology — the harness shell
#    opens the rollout on fd 9 and `bash "$SCRIPT"` runs as its child, so
#    the script's $PPID points at the harness.
if [[ -d /proc/$$/fd ]]; then
    tmpdir=$(mktemp -d)
    mkdir -p "$tmpdir/sessions/2026/06/23"
    rollout="$tmpdir/sessions/2026/06/23/rollout-2026-06-23T17-08-20-$UUID_OK.jsonl"
    : > "$rollout"
    out=$(unset CODEX_THREAD_ID
          CODEX_HOME="$tmpdir" ROLLOUT_PATH="$rollout" SCRIPT="$SCRIPT" \
          bash -c '
              exec 9< "$ROLLOUT_PATH"
              bash "$SCRIPT"
          ')
    rc=$?
    assert_exit "proc_fd_single_match_returns_uuid" 0 "$rc"
    assert_stdout "proc_fd_single_match_returns_uuid stdout" "$UUID_OK" "$out"
    rm -rf "$tmpdir"
else
    skip "proc_fd_single_match_returns_uuid" "no /proc on this host"
fi

# 6) Source 2: rollout path OUTSIDE $CODEX_HOME/sessions/ → not picked up.
if [[ -d /proc/$$/fd ]]; then
    tmpdir=$(mktemp -d)
    elsewhere=$(mktemp -d)
    mkdir -p "$tmpdir/sessions"
    bad="$elsewhere/rollout-2026-06-23T17-08-20-$UUID_OK.jsonl"
    : > "$bad"
    rc=$(unset CODEX_THREAD_ID
          CODEX_HOME="$tmpdir" ROLLOUT_PATH="$bad" SCRIPT="$SCRIPT" \
          bash -c '
              exec 9< "$ROLLOUT_PATH"
              bash "$SCRIPT" >/dev/null 2>&1
              echo $?
          ')
    assert_exit "proc_fd_rejects_path_outside_sessions_root" 1 "$rc"
    rm -rf "$tmpdir" "$elsewhere"
else
    skip "proc_fd_rejects_path_outside_sessions_root" "no /proc on this host"
fi

# 7) Source 2: non-UUID basename ignored.
if [[ -d /proc/$$/fd ]]; then
    tmpdir=$(mktemp -d)
    mkdir -p "$tmpdir/sessions"
    bad="$tmpdir/sessions/rollout-2026-06-23T17-08-20-not-a-uuid.jsonl"
    : > "$bad"
    rc=$(unset CODEX_THREAD_ID
          CODEX_HOME="$tmpdir" ROLLOUT_PATH="$bad" SCRIPT="$SCRIPT" \
          bash -c '
              exec 9< "$ROLLOUT_PATH"
              bash "$SCRIPT" >/dev/null 2>&1
              echo $?
          ')
    assert_exit "proc_fd_rejects_non_uuid_basename" 1 "$rc"
    rm -rf "$tmpdir"
else
    skip "proc_fd_rejects_non_uuid_basename" "no /proc on this host"
fi

# 8) Source 2: multi-candidate → exit 2, stderr lists both.
if [[ -d /proc/$$/fd ]]; then
    tmpdir=$(mktemp -d)
    mkdir -p "$tmpdir/sessions"
    r1="$tmpdir/sessions/rollout-2026-06-23T17-08-20-$UUID_OK.jsonl"
    r2="$tmpdir/sessions/rollout-2026-06-23T17-08-21-$UUID_OK2.jsonl"
    : > "$r1"
    : > "$r2"
    output=$(unset CODEX_THREAD_ID
          CODEX_HOME="$tmpdir" R1="$r1" R2="$r2" SCRIPT="$SCRIPT" \
          bash -c '
              exec 9< "$R1"
              exec 8< "$R2"
              bash "$SCRIPT" 2>&1 >/dev/null
              echo "rc=$?"
          ')
    rc=$(echo "$output" | sed -nE 's/^rc=([0-9]+)$/\1/p' | tail -n 1)
    assert_exit "proc_fd_multi_candidate_fails_loud" 2 "$rc"
    case "$output" in
        *"$UUID_OK"*"$UUID_OK2"*|*"$UUID_OK2"*"$UUID_OK"*) ok "proc_fd_multi_candidate_lists_both_in_stderr";;
        *) not_ok "proc_fd_multi_candidate_lists_both_in_stderr";;
    esac
    rm -rf "$tmpdir"
else
    skip "proc_fd_multi_candidate_fails_loud" "no /proc on this host"
    skip "proc_fd_multi_candidate_lists_both_in_stderr" "no /proc on this host"
fi

# 9) Source 2: same UUID opened twice → dedupes to single candidate.
if [[ -d /proc/$$/fd ]]; then
    tmpdir=$(mktemp -d)
    mkdir -p "$tmpdir/sessions"
    r1="$tmpdir/sessions/rollout-2026-06-23T17-08-20-$UUID_OK.jsonl"
    : > "$r1"
    out=$(unset CODEX_THREAD_ID
          CODEX_HOME="$tmpdir" R1="$r1" SCRIPT="$SCRIPT" \
          bash -c '
              exec 9< "$R1"
              exec 8< "$R1"
              bash "$SCRIPT"
          ')
    rc=$?
    assert_exit "proc_fd_dedupe_same_uuid_across_walk" 0 "$rc"
    assert_stdout "proc_fd_dedupe_same_uuid_across_walk stdout" "$UUID_OK" "$out"
    rm -rf "$tmpdir"
else
    skip "proc_fd_dedupe_same_uuid_across_walk" "no /proc on this host"
fi

# 10) All sources fail → exit 1 with operator-actionable stderr.
#     Capture stderr to a temp file and the script's exit code via $?
#     (NOT via `|| true` — that would mask the true exit code as 0).
tmpdir=$(mktemp -d)
errfile=$(mktemp)
set +e
# Use /usr/bin/env explicitly — avoid any PATH-shimming wrapper that may
# swallow arguments without forwarding them to the real env binary.
/usr/bin/env -u CODEX_THREAD_ID CODEX_HOME="$tmpdir" "$SCRIPT" >/dev/null 2>"$errfile"
rc=$?
set -e
err=$(cat "$errfile")
assert_exit "all_sources_fail_explicit_message" 1 "$rc"
case "$err" in
    *"/status"*"thread_id"*) ok "all_sources_fail_mentions_status";;
    *) not_ok "all_sources_fail_mentions_status";;
esac
rm -rf "$tmpdir" "$errfile"

# TAP header (printed last because we computed n dynamically).
echo "1..$n"
[[ "$failed" -eq 0 ]]
