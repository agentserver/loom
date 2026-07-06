#!/usr/bin/env bash
set -euo pipefail
# teardown.sh — WT-3-prod-multidevice shutdown checklist.
#
# Spec §7. Four operations:
#   1. Remove local OAuth token files.
#   2. Revoke agentserver workspace_id.
#   3. Destroy cloud droplet.
#   4. Uninstall Windows executor.
#
# Dual gate (spec §7.2):
#   --dry-run (default) → print planned commands, exit 0.
#   --execute           → if ALLOW_TEARDOWN=1 is set, actually run each
#                         command. Otherwise fall back to dry-run and
#                         warn on stderr.
#
# HARNESS-ONLY: this worktree ONLY exercises --dry-run (and --execute
# without env, which falls back to dry-run). Actual --execute-with-env
# is the responsibility of paper/v3/p3-prod-multidevice-run.

MODE=dry-run
VENDOR="${VENDOR:-digitalocean}"
WORKSPACE_ID="${WORKSPACE_ID:-<workspace-id-placeholder>}"
LOOM_HOME="${LOOM_HOME:-<loom-home-placeholder>}"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --dry-run) MODE=dry-run; shift ;;
        --execute) MODE=execute; shift ;;
        --vendor)  VENDOR="$2"; shift 2 ;;
        *) echo "teardown.sh: unknown arg: $1" >&2; exit 2 ;;
    esac
done

if [[ "$MODE" == "execute" ]] && [[ "${ALLOW_TEARDOWN:-0}" != "1" ]]; then
    echo "teardown.sh: ALLOW_TEARDOWN not set; refusing to execute; running dry-run instead" >&2
    MODE=dry-run
fi

# --- Command plan (identical in both modes; only the exec loop below
# --- toggles on MODE) ------------------------------------------------

cmd_rm_oauth=(rm -f "$LOOM_HOME/tokens/laptop.token" "$LOOM_HOME/tokens/headless.token" "$LOOM_HOME/tokens/windows.token" "$LOOM_HOME/tokens/cloud.token")

cmd_revoke_workspace=(curl -X DELETE "https://agent.cs.ac.cn/api/v1/workspaces/${WORKSPACE_ID}" -H "Authorization: Bearer <OAUTH_TOKEN_HERE_DO_NOT_COMMIT>")

case "$VENDOR" in
    digitalocean) cmd_destroy_droplet=(doctl compute droplet delete '<droplet_id>' --force) ;;
    e2b)          cmd_destroy_droplet=(e2b sandbox delete '<sandbox_id>') ;;
    vps)          cmd_destroy_droplet=(ssh '<vps-user>@<vps-host>' 'systemctl stop loom-slave') ;;
    *) echo "teardown.sh: vendor '$VENDOR' not in {digitalocean, e2b, vps}" >&2; exit 2 ;;
esac

cmd_windows_uninstall=(pwsh -Command "& $LOOM_HOME/uninstall.ps1 -Scope Process")

emit_cmd() {
    local label="$1"; shift
    printf '# %s\n%s\n' "$label" "$(printf '%q ' "$@")"
}

# 4 planned commands — print each on its own line, prefixed by a
# label comment so test_teardown_dry_run_prints_four's grep hits each.
emit_cmd "1. remove local OAuth token files" "${cmd_rm_oauth[@]}"
emit_cmd "2. revoke agentserver workspace_id" "${cmd_revoke_workspace[@]}"
emit_cmd "3. destroy cloud droplet (vendor=$VENDOR)" "${cmd_destroy_droplet[@]}"
emit_cmd "4. uninstall Windows executor" "${cmd_windows_uninstall[@]}"

if [[ "$MODE" == "dry-run" ]]; then
    echo "teardown.sh: dry-run complete (no commands executed)"
    exit 0
fi

# MODE=execute AND ALLOW_TEARDOWN=1 — actually run each command. This
# branch is unreachable in this worktree's tests; it is here for the
# paper/v3/p3-prod-multidevice-run worktree to consume.
echo "teardown.sh: EXECUTE mode — running commands now" >&2
"${cmd_rm_oauth[@]}"
"${cmd_revoke_workspace[@]}"
"${cmd_destroy_droplet[@]}"
"${cmd_windows_uninstall[@]}"
echo "teardown.sh: execute complete"
