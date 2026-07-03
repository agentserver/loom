#!/usr/bin/env bash
set -euo pipefail
# deploy.sh — WT-2-deploy-scripts one-key stack bring-up for Linux.
#
# Spec: docs/specs/wt2-deploy-scripts.spec.md
# Plan: docs/specs/wt2-deploy-scripts.plan.md
#
# Usage:
#   deploy.sh [--stub | --prod | --mode {stub|prod}] \
#             [--observer-port N] [--driver-port N] [--slave-port N] \
#             [--stub-port N] [--loom-home DIR] [--bin-dir DIR] \
#             [--dry-run] [--allow-model-key-passthrough] \
#             [--topology-out PATH]
#   deploy.sh --shutdown [--loom-home DIR]
#
# Exit codes (spec §3.3):
#   0 — success (spawn+ready gates passed; PID files written)
#   2 — preflight failure (bad flag, bad port, missing prereq)
#   3 — sub-installer failure
#   4 — readiness gate timeout
#   5 — topology emit failed
#
# Security posture:
#   §7(a) stub bind is hard-coded 127.0.0.1; no override channel.
#   §7(b) no OAuth material written to disk from this script (prod is
#         spawn-only against operator-registered $LOOM_HOME).
#   §7(c) fail-fast preflight (set -euo pipefail is the first executable line).
#   §7(d) machine_topology.sh redacts hostname / never emits $USER.
#   §7(e) port validation + well-known blacklist.
#   §7(g) subprocess env whitelist (mirrors tools/eval/runner/subprocess.go).
#   §7(h) --dry-run redacts secrets before print.

# --- constants ---------------------------------------------------------

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly REPO_DEPLOY_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
readonly TOPOLOGY_HELPER="$REPO_DEPLOY_ROOT/machine_topology.sh"

# Well-known port blacklist per spec §7(e).
readonly WELL_KNOWN_PORTS=(22 23 25 53 80 110 143 443 465 587 993 995 3389 5432 6379 8080 8443)

# Env whitelist — mirror of tools/eval/runner/subprocess.go
# (AlwaysAllowedEnvKeys line 223 + AlwaysAllowedIfSetEnvKeys line 235).
readonly ALWAYS_ENV_KEYS=(PATH HOME LANG LC_ALL TZ USER)
readonly IFSET_ENV_KEYS=(AGENTSERVER_ROOT MODELSERVER_ROOT APP_ROOT MOCK_MODEL_URL)

# Readiness timeout (seconds). LOOM_DEPLOY_READY_TIMEOUT_SEC overrides.
readonly READY_TIMEOUT_DEFAULT=30

# --- CLI parse ---------------------------------------------------------

MODE=""
OBSERVER_PORT=18091
DRIVER_PORT=18092
SLAVE_PORT=18093
STUB_PORT=18080
# Track whether each port was explicitly supplied on the CLI so prod
# preflight can honor "omitted → adopt operator's yaml value" per spec
# §4.2 prod row (P1-3 fix, Codex round 2).
OBSERVER_PORT_SET=0
DRIVER_PORT_SET=0
SLAVE_PORT_SET=0
LOOM_HOME=""
BIN_DIR="$SCRIPT_DIR/bin"
DRY_RUN=0
ALLOW_MODEL_KEY=0
TOPOLOGY_OUT=""
SHUTDOWN=0

die() { echo "deploy.sh: $*" >&2; exit "${2:-2}"; }
usage() { sed -n '4,15p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; }

mode_set() {
    case "$1" in
        stub|prod) ;;
        *) die "invalid --mode value: '$1' (allowed: stub|prod)" ;;
    esac
    if [[ -n "$MODE" && "$MODE" != "$1" ]]; then
        die "conflicting mode flags: $MODE and $1"
    fi
    MODE="$1"
}

# require_arg <flag> — asserts $# is high enough for a value-taking flag.
# Called before every `$2` deref in the case below so `--mode` (with no
# value) exits preflight-2 with a clear error instead of hitting
# `set -u`'s "$2: unbound variable" (P1-1 fix, Codex round 3).
require_arg() {
    (( $# >= 2 )) || die "$1 requires a value"
}

while (( $# > 0 )); do
    case "$1" in
        --stub)                          mode_set stub; shift ;;
        --prod)                          mode_set prod; shift ;;
        --mode)                          require_arg "$@"; mode_set "$2"; shift 2 ;;
        --observer-port)                 require_arg "$@"; OBSERVER_PORT="$2"; OBSERVER_PORT_SET=1; shift 2 ;;
        --driver-port)                   require_arg "$@"; DRIVER_PORT="$2"; DRIVER_PORT_SET=1; shift 2 ;;
        --slave-port)                    require_arg "$@"; SLAVE_PORT="$2"; SLAVE_PORT_SET=1; shift 2 ;;
        --stub-port)                     require_arg "$@"; STUB_PORT="$2"; shift 2 ;;
        --loom-home)                     require_arg "$@"; LOOM_HOME="$2"; shift 2 ;;
        --bin-dir)                       require_arg "$@"; BIN_DIR="$2"; shift 2 ;;
        --dry-run)                       DRY_RUN=1; shift ;;
        --allow-model-key-passthrough)   ALLOW_MODEL_KEY=1; shift ;;
        --topology-out)                  require_arg "$@"; TOPOLOGY_OUT="$2"; shift 2 ;;
        --shutdown)                      SHUTDOWN=1; shift ;;
        -h|--help)                       usage; exit 0 ;;
        *)                               die "unknown flag: $1" ;;
    esac
done

# Default mode = stub (spec §3.1).
[[ -z "$MODE" && "$SHUTDOWN" -eq 0 ]] && MODE=stub

# LOOM_HOME default. Compute the intended path but DO NOT create it yet —
# --dry-run must not touch the filesystem (spec §3.3 code 0 "no side
# effects"). Only realize the directory when we actually spawn.
[[ -z "$LOOM_HOME" ]] && LOOM_HOME="${HOME:-/tmp}/.loom/eval-deploy"
# Resolve without mkdir. If the parent doesn't exist we fall back to the
# literal path — dry-run's planned_commands only needs a string.
if [[ -d "$LOOM_HOME" ]]; then
    LOOM_HOME="$(cd "$LOOM_HOME" && pwd)"
else
    # Normalize any relative segment via bash parameter expansion; do not
    # touch disk.
    case "$LOOM_HOME" in
        /*) : ;;                                                   # already absolute
        *) LOOM_HOME="$(cd "$(dirname "$LOOM_HOME")" 2>/dev/null && pwd)/$(basename "$LOOM_HOME")" || LOOM_HOME="$LOOM_HOME" ;;
    esac
fi
readonly LOOM_HOME BIN_DIR
PIDS_DIR="$LOOM_HOME/.pids"

# --- shutdown mode (bypass all other logic) ----------------------------

if (( SHUTDOWN == 1 )); then
    if [[ ! -d "$PIDS_DIR" ]]; then
        echo "deploy.sh: no .pids/ under $LOOM_HOME; nothing to shut down"
        exit 0
    fi
    reaped=0
    for pf in "$PIDS_DIR"/*.pid; do
        [[ -f "$pf" ]] || continue
        pid=$(cat "$pf" 2>/dev/null || echo)
        [[ -z "$pid" ]] && continue
        if kill -0 "$pid" 2>/dev/null; then
            kill -TERM "$pid" 2>/dev/null || true
            reaped=$((reaped+1))
        fi
    done
    # Grace period.
    for _ in $(seq 1 50); do
        alive=0
        for pf in "$PIDS_DIR"/*.pid; do
            [[ -f "$pf" ]] || continue
            pid=$(cat "$pf" 2>/dev/null || echo)
            [[ -z "$pid" ]] && continue
            if kill -0 "$pid" 2>/dev/null; then alive=$((alive+1)); fi
        done
        (( alive == 0 )) && break
        sleep 0.1
    done
    # SIGKILL any survivors.
    for pf in "$PIDS_DIR"/*.pid; do
        [[ -f "$pf" ]] || continue
        pid=$(cat "$pf" 2>/dev/null || echo)
        [[ -z "$pid" ]] && continue
        if kill -0 "$pid" 2>/dev/null; then
            kill -KILL "$pid" 2>/dev/null || true
        fi
    done
    rm -rf "$PIDS_DIR"
    echo "deploy.sh: shutdown complete ($reaped process(es) signalled)"
    exit 0
fi

# --- port validation (§7(e)) -------------------------------------------

validate_port() {
    local name="$1" val="$2"
    [[ "$val" =~ ^[0-9]+$ ]] || die "$name must be an integer, got '$val'"
    # `(( ... ))` under `set -e` aborts on a false result; wrap in `if`.
    if ! (( val >= 1024 && val <= 65535 )); then
        die "$name=$val out of range [1024,65535]"
    fi
    for bad in "${WELL_KNOWN_PORTS[@]}"; do
        if (( val == bad )); then
            die "$name=$val is a well-known port (blacklist: ${WELL_KNOWN_PORTS[*]})"
        fi
    done
}

validate_port --observer-port "$OBSERVER_PORT"
validate_port --driver-port "$DRIVER_PORT"
validate_port --slave-port "$SLAVE_PORT"
[[ "$MODE" == stub ]] && validate_port --stub-port "$STUB_PORT"

# Pairwise distinct.
declare -A seen_ports=()
for kv in "observer:$OBSERVER_PORT" "driver:$DRIVER_PORT" "slave:$SLAVE_PORT"; do
    p="${kv#*:}"
    [[ -n "${seen_ports[$p]:-}" ]] && die "port $p used by both ${seen_ports[$p]} and ${kv%:*}"
    seen_ports[$p]="${kv%:*}"
done
if [[ "$MODE" == stub ]]; then
    [[ -n "${seen_ports[$STUB_PORT]:-}" ]] && die "port $STUB_PORT used by both ${seen_ports[$STUB_PORT]} and stub"
fi

# --- mode-specific preflight -------------------------------------------

if [[ "$MODE" == prod && "$ALLOW_MODEL_KEY" -eq 0 ]]; then
    :  # no-op; keeping the branch for readability of §7(g) below
fi

if [[ "$MODE" == stub && "$ALLOW_MODEL_KEY" -eq 1 ]]; then
    die "--allow-model-key-passthrough is only valid with --mode prod (spec §7(g); the stub has no model plane)"
fi

# --- env whitelist (§7(g)) ---------------------------------------------

# Emit KEY=VAL pairs on stdout for the whitelisted subset of the parent env.
emit_whitelisted_env() {
    local k v
    for k in "${ALWAYS_ENV_KEYS[@]}"; do
        v="${!k:-}"
        printf '%s=%s\n' "$k" "$v"
    done
    for k in "${IFSET_ENV_KEYS[@]}"; do
        if [[ -n "${!k:-}" ]]; then
            printf '%s=%s\n' "$k" "${!k}"
        fi
    done
    # LOOM_* prefix passthrough (require ≥1 char after prefix; matches
    # subprocess.go:275-282). Use `/usr/bin/env` explicitly — a `env`
    # rc-shim on the user's PATH (e.g. ~/.local/bin/env) would
    # otherwise mutate output and drop LOOM_* keys.
    while IFS='=' read -r k v; do
        [[ -z "$k" ]] && continue
        if [[ "$k" == LOOM_* && ${#k} -gt 5 ]]; then
            printf '%s=%s\n' "$k" "$v"
        fi
    done < <(/usr/bin/env)
    # Model-key passthrough (prod + explicit flag).
    if [[ "$MODE" == prod && "$ALLOW_MODEL_KEY" -eq 1 ]]; then
        for k in OPENAI_API_KEY ANTHROPIC_API_KEY; do
            if [[ -n "${!k:-}" ]]; then
                echo "deploy.sh: passing $k through to subprocesses" >&2
                printf '%s=%s\n' "$k" "${!k}"
            fi
        done
    fi
}

# --- redaction (§7(h)) -------------------------------------------------

# json_string <raw>  — quote a string for JSON (delegates to jq).
json_string() {
    printf '%s' "$1" | jq -Rs .
}

# redact_env_kv <KEY=VALUE>  — for --dry-run env printing.
redact_env_kv_key() {
    # Return only the key portion; values are NEVER printed in dry-run.
    printf '%s' "${1%%=*}"
}

# redact_argv_value <flag> <value>  — if the flag is one that carries a
# secret, replace value with "<REDACTED>". Called by dry-run printer.
is_secret_flag() {
    case "$1" in
        --api-key|--token|--secret|--password|--bearer) return 0 ;;
        *) return 1 ;;
    esac
}

# --- plan builders -----------------------------------------------------

# planned_commands_json  — build the argv list JSON per spec §4.3.
planned_commands_json() {
    local -a cmds=()
    if [[ "$MODE" == stub ]]; then
        cmds+=("$(printf '[%s,%s,%s,%s,%s]' \
            "$(json_string "$BIN_DIR/agentserver-stub")" \
            "$(json_string "--listen")" \
            "$(json_string "127.0.0.1:$STUB_PORT")" \
            "$(json_string "--workspace-id")" \
            "$(json_string "auto")")")
        cmds+=("$(printf '[%s,%s,%s,%s,%s,%s,%s,%s,%s]' \
            "$(json_string "$SCRIPT_DIR/observer/install.sh")" \
            "$(json_string "--name")" "$(json_string "eval-obs")" \
            "$(json_string "--loom-home")" "$(json_string "$LOOM_HOME/observer")" \
            "$(json_string "--listen")" "$(json_string "127.0.0.1:$OBSERVER_PORT")" \
            "$(json_string "--api-key")" "$(json_string "<REDACTED>")" )")
        cmds+=("$(printf '[%s,%s,%s]' \
            "$(json_string "$LOOM_HOME/observer/observer-server")" \
            "$(json_string "-config")" "$(json_string "$LOOM_HOME/observer/observer.yaml")")")
        cmds+=("$(printf '[%s,%s,%s,%s,%s,%s,%s,%s,%s]' \
            "$(json_string "$SCRIPT_DIR/slave/install.sh")" \
            "$(json_string "--name")" "$(json_string "eval-slave")" \
            "$(json_string "--loom-home")" "$(json_string "$LOOM_HOME/slave")" \
            "$(json_string "--observer-url")" "$(json_string "http://127.0.0.1:$OBSERVER_PORT")" \
            "$(json_string "--workspace")" "$(json_string "ws-eval-auto")")")
        cmds+=("$(printf '[%s,%s,%s,%s,%s,%s,%s,%s,%s]' \
            "$(json_string "$BIN_DIR/agentserver-stub")" \
            "$(json_string "issue")" \
            "$(json_string "--server")" "$(json_string "http://127.0.0.1:$STUB_PORT")" \
            "$(json_string "--role")" "$(json_string "slave")" \
            "$(json_string "--short-id")" "$(json_string "slv-eval-001")" \
            "$(json_string "> creds/slave.json")")")
        cmds+=("$(printf '[%s,%s,%s,%s,%s,%s]' \
            "$(json_string "<yq-patch>")" \
            "$(json_string "$LOOM_HOME/slave/config.yaml")" \
            "$(json_string "server.url=http://127.0.0.1:$STUB_PORT")" \
            "$(json_string "credentials.*=<REDACTED>")" \
            "$(json_string "daemon.auto_start=false")" \
            "$(json_string "daemon.listen=127.0.0.1:$SLAVE_PORT")")")
        cmds+=("$(printf '[%s,%s]' \
            "$(json_string "$LOOM_HOME/slave/slave-agent")" \
            "$(json_string "$LOOM_HOME/slave/config.yaml")")")
        cmds+=("$(printf '[%s,%s,%s,%s,%s,%s,%s]' \
            "$(json_string "$SCRIPT_DIR/driver/install.sh")" \
            "$(json_string "--project")" "$(json_string "$LOOM_HOME/driver")" \
            "$(json_string "--name")" "$(json_string "eval-driver")" \
            "$(json_string "--observer-url")" "$(json_string "http://127.0.0.1:$OBSERVER_PORT")")")
        cmds+=("$(printf '[%s,%s,%s,%s,%s,%s,%s,%s,%s]' \
            "$(json_string "$BIN_DIR/agentserver-stub")" \
            "$(json_string "issue")" \
            "$(json_string "--server")" "$(json_string "http://127.0.0.1:$STUB_PORT")" \
            "$(json_string "--role")" "$(json_string "driver")" \
            "$(json_string "--short-id")" "$(json_string "drv-eval-001")" \
            "$(json_string "> creds/driver.json")")")
        cmds+=("$(printf '[%s,%s,%s,%s]' \
            "$(json_string "<yq-patch>")" \
            "$(json_string "$LOOM_HOME/driver/config.yaml")" \
            "$(json_string "server.url=http://127.0.0.1:$STUB_PORT")" \
            "$(json_string "credentials.*=<REDACTED>")" )")
        cmds+=("$(printf '[%s,%s,%s,%s,%s,%s]' \
            "$(json_string "$LOOM_HOME/driver/driver-agent")" \
            "$(json_string "serve-daemon")" \
            "$(json_string "--config")" "$(json_string "$LOOM_HOME/driver/config.yaml")" \
            "$(json_string "--listen")" "$(json_string "127.0.0.1:$DRIVER_PORT")")")
    else
        cmds+=("$(printf '[%s,%s,%s]' \
            "$(json_string "$LOOM_HOME/observer/observer-server")" \
            "$(json_string "-config")" "$(json_string "$LOOM_HOME/observer/observer.yaml")")")
        cmds+=("$(printf '[%s,%s]' \
            "$(json_string "$LOOM_HOME/slave/slave-agent")" \
            "$(json_string "$LOOM_HOME/slave/config.yaml")")")
        cmds+=("$(printf '[%s,%s,%s,%s,%s,%s]' \
            "$(json_string "$LOOM_HOME/driver/driver-agent")" \
            "$(json_string "serve-daemon")" \
            "$(json_string "--config")" "$(json_string "$LOOM_HOME/driver/config.yaml")" \
            "$(json_string "--listen")" "$(json_string "127.0.0.1:$DRIVER_PORT")")")
    fi
    # Join with commas.
    local out="[" i=0
    for c in "${cmds[@]}"; do
        (( i > 0 )) && out+=","
        out+="$c"
        i=$((i+1))
    done
    out+="]"
    printf '%s' "$out"
}

component_ports_json() {
    if [[ "$MODE" == stub ]]; then
        printf '{"agentserver_stub":%d,"observer":%d,"driver":%d,"slave":%d}' \
            "$STUB_PORT" "$OBSERVER_PORT" "$DRIVER_PORT" "$SLAVE_PORT"
    else
        printf '{"observer":%d,"driver":%d,"slave":%d}' \
            "$OBSERVER_PORT" "$DRIVER_PORT" "$SLAVE_PORT"
    fi
}

env_whitelist_json() {
    # KEY NAMES ONLY per §7(h) — values are NEVER printed in dry-run.
    local always_json ifset_json
    always_json=$(printf '"%s",' "${ALWAYS_ENV_KEYS[@]}"); always_json="[${always_json%,}]"
    ifset_json=$(printf '"%s",' "${IFSET_ENV_KEYS[@]}"); ifset_json="[${ifset_json%,}]"
    printf '{"always":%s,"if_set":%s,"prefix":["LOOM_*"]}' \
        "$always_json" "$ifset_json"
}

emit_dry_run() {
    # Emit topology-shape header (mode/host/os/arch/component_ports),
    # planned_commands, planned_env_whitelist, loom_home, bin_dir.
    local host os arch
    local raw_host="${LOOM_TEST_HOSTNAME:-$(hostname 2>/dev/null || echo unknown)}"
    if [[ "$raw_host" == *"@"* ]]; then
        local l="${raw_host%@*}" r="${raw_host##*@}"
        host="$(printf '%s' "$l" | sha256sum | cut -c1-8)@$(printf '%s' "$r" | sha256sum | cut -c1-8)"
    else
        host="$(printf '%s' "$raw_host" | sha256sum | cut -c1-8)"
    fi
    case "$(uname -s | tr '[:upper:]' '[:lower:]')" in
        linux) os=linux ;;
        darwin) os=darwin ;;
        *) os=linux ;;
    esac
    case "$(uname -m)" in
        x86_64) arch=amd64 ;;
        aarch64) arch=arm64 ;;
        *) arch="$(uname -m)" ;;
    esac
    local raw
    raw=$(printf '{"mode":%s,"host":%s,"os":%s,"arch":%s,"component_ports":%s,"planned_commands":%s,"planned_env_whitelist":%s,"loom_home":%s,"bin_dir":%s}' \
        "$(json_string "$MODE")" \
        "$(json_string "$host")" \
        "$(json_string "$os")" \
        "$(json_string "$arch")" \
        "$(component_ports_json)" \
        "$(planned_commands_json)" \
        "$(env_whitelist_json)" \
        "$(json_string "$LOOM_HOME")" \
        "$(json_string "$BIN_DIR")")
    # Pretty-print via jq. Any failure is a bug in our JSON assembly
    # (every scalar goes through json_string() escaping); propagate as
    # exit 5 through set -e + pipefail rather than swallow with a
    # fallback that would drop the exit-5 contract from spec §3.3
    # (fresh-review P2-2 round 8).
    printf '%s\n' "$raw" | jq .
}

# --- dry-run branch (§4.1 step 4) --------------------------------------

if (( DRY_RUN == 1 )); then
    emit_dry_run
    exit 0
fi

# --- non-dry-run: preflight prerequisites ------------------------------

if [[ "$MODE" == stub ]]; then
    command -v yq >/dev/null 2>&1 || die "yq not found on PATH; install yq (mikefarah's Go yq) or use --mode prod"
    command -v curl >/dev/null 2>&1 || die "curl not found on PATH"
fi

# --- prod preflight (§4.2 prod table) ----------------------------------

prod_preflight() {
    local f
    command -v yq >/dev/null 2>&1 || die "prod preflight failed: yq not found on PATH; see tests/prod_test/E2E_RUNBOOK.md:83-108"

    # --- observer -----------------------------------------------------
    f="$LOOM_HOME/observer/observer.yaml"
    [[ -r "$f" ]] || die "prod preflight failed: $f missing or unreadable; see tests/prod_test/E2E_RUNBOOK.md:83-108"
    [[ -x "$LOOM_HOME/observer/observer-server" ]] || die "prod preflight failed: $LOOM_HOME/observer/observer-server not executable; see tests/prod_test/E2E_RUNBOOK.md:83-108"
    # observer.yaml must have listen_addr matching --observer-port. The
    # yaml canonical shape is `listen_addr: "127.0.0.1:18091"` (see
    # deploy/linux/observer/config.yaml.template). Empty listen_addr
    # would let observer-server pick a random port — the readiness gate
    # would time out at exit 4, hiding an actionable misconfiguration.
    local obs_listen obs_port
    obs_listen=$(run_whitelisted yq eval '.listen_addr // ""' "$f")
    [[ -n "$obs_listen" ]] || die "prod preflight failed: $f listen_addr is empty; see tests/prod_test/E2E_RUNBOOK.md:83-108"
    obs_port="${obs_listen##*:}"
    if [[ "$obs_port" =~ ^[0-9]+$ ]]; then
        if (( OBSERVER_PORT_SET == 1 )); then
            if [[ "$obs_port" != "$OBSERVER_PORT" ]]; then
                die "prod preflight failed: operator-registered observer uses port $obs_port; --observer-port $OBSERVER_PORT must match or be omitted"
            fi
        else
            # CLI flag was not supplied — adopt the operator's yaml value
            # so the readiness gate probes the actual bind port.
            OBSERVER_PORT="$obs_port"
        fi
    fi

    # --- slave --------------------------------------------------------
    f="$LOOM_HOME/slave/config.yaml"
    [[ -r "$f" ]] || die "prod preflight failed: $f missing or unreadable; see tests/prod_test/E2E_RUNBOOK.md:83-108"
    local slave_token slave_short_id slave_ws slave_listen slave_port
    slave_token=$(run_whitelisted yq eval '.credentials.proxy_token // ""' "$f")
    [[ -n "$slave_token" ]] || die "prod preflight failed: $f credentials.proxy_token is empty; see tests/prod_test/E2E_RUNBOOK.md:83-108"
    slave_short_id=$(run_whitelisted yq eval '.credentials.short_id // ""' "$f")
    [[ -n "$slave_short_id" ]] || die "prod preflight failed: $f credentials.short_id is empty; see tests/prod_test/E2E_RUNBOOK.md:83-108"
    slave_ws=$(run_whitelisted yq eval '.credentials.workspace_id // ""' "$f")
    [[ -n "$slave_ws" ]] || die "prod preflight failed: $f credentials.workspace_id is empty; see tests/prod_test/E2E_RUNBOOK.md:83-108"
    slave_listen=$(run_whitelisted yq eval '.daemon.listen // ""' "$f")
    # daemon.listen is required in prod mode — an empty value would let
    # slave-agent pick 127.0.0.1:0 (see internal/config/config.go:213),
    # and our TCP LISTEN readiness gate would then wait on the wrong
    # port and time out at exit 4 instead of exit 2.
    [[ -n "$slave_listen" ]] || die "prod preflight failed: $f daemon.listen is empty; the readiness gate needs an explicit port; see tests/prod_test/E2E_RUNBOOK.md:83-108"
    slave_port="${slave_listen##*:}"
    if [[ "$slave_port" =~ ^[0-9]+$ ]]; then
        if (( SLAVE_PORT_SET == 1 )); then
            if [[ "$slave_port" != "$SLAVE_PORT" ]]; then
                die "prod preflight failed: operator-registered slave uses port $slave_port; --slave-port $SLAVE_PORT must match or be omitted"
            fi
        else
            SLAVE_PORT="$slave_port"
        fi
    fi
    [[ -x "$LOOM_HOME/slave/slave-agent" ]] || die "prod preflight failed: $LOOM_HOME/slave/slave-agent not executable"

    # --- driver -------------------------------------------------------
    f="$LOOM_HOME/driver/config.yaml"
    [[ -r "$f" ]] || die "prod preflight failed: $f missing or unreadable; see tests/prod_test/E2E_RUNBOOK.md:83-108"
    local drv_token drv_short_id
    drv_token=$(run_whitelisted yq eval '.credentials.proxy_token // ""' "$f")
    [[ -n "$drv_token" ]] || die "prod preflight failed: $f credentials.proxy_token is empty; see tests/prod_test/E2E_RUNBOOK.md:83-108"
    drv_short_id=$(run_whitelisted yq eval '.credentials.short_id // ""' "$f")
    [[ -n "$drv_short_id" ]] || die "prod preflight failed: $f credentials.short_id is empty; see tests/prod_test/E2E_RUNBOOK.md:83-108"
    [[ -x "$LOOM_HOME/driver/driver-agent" ]] || die "prod preflight failed: $LOOM_HOME/driver/driver-agent not executable"
}

# --- lifecycle scaffolding ---------------------------------------------

declare -a SPAWNED_PIDS=()
FAILED=0
# BRINGUP_COMPLETE flips to 1 after the switch on $MODE returns without
# throwing. Round-9 P1-B fix: previously any exit != 0 (including exit
# 5 = topology emit failure per spec §3.3) reaped the entire healthy
# daemon set, giving the operator the WORST possible outcome ("stack
# gone AND no topology JSON"). Once bring-up succeeded, a topology
# failure is a warning, not a stack teardown.
BRINGUP_COMPLETE=0

cleanup_on_failure() {
    local ec=$?
    (( ec == 0 )) && return 0
    if (( BRINGUP_COMPLETE == 1 )); then
        # Bring-up completed; failure is post-bring-up (topology emit
        # or similar). Do NOT reap the healthy daemons — the operator
        # can still `deploy.sh --shutdown` when done.
        echo "deploy.sh: post-bring-up failure (exit $ec); daemons LEFT RUNNING (use --shutdown to reap)." >&2
        exit "$ec"
    fi
    echo "deploy.sh: failure (exit $ec) — reaping spawned processes" >&2
    for pid in "${SPAWNED_PIDS[@]:-}"; do
        [[ -z "$pid" ]] && continue
        kill -TERM "$pid" 2>/dev/null || true
    done
    sleep 3
    for pid in "${SPAWNED_PIDS[@]:-}"; do
        [[ -z "$pid" ]] && continue
        kill -KILL "$pid" 2>/dev/null || true
    done
    rm -rf "$PIDS_DIR"
    exit "$ec"
}
trap cleanup_on_failure EXIT
# LOOM_HOME + .pids are only created here (post-dry-run branch, per §3.3
# no-side-effect contract for --dry-run).
mkdir -p "$LOOM_HOME" "$PIDS_DIR"
chmod 0700 "$PIDS_DIR"

# run_whitelisted <cmd...>  — foreground exec of a helper subprocess
# (install.sh, agentserver-stub issue, yq) with the same env whitelist
# spawn_bg applies to daemons. Bypassing this and running `bash
# install.sh ...` directly would silently inherit the operator's full
# env (AWS_*, GITHUB_TOKEN, OPENAI_API_KEY, etc.) — §7(g) requires the
# whitelist for EVERY spawned subprocess, not just the long-running
# ones (P0-1 fix, Codex round 2).
run_whitelisted() {
    local env_lines
    env_lines=$(emit_whitelisted_env)
    local -a env_argv=()
    while IFS= read -r kv; do
        [[ -n "$kv" ]] && env_argv+=("$kv")
    done <<< "$env_lines"
    /usr/bin/env -i "${env_argv[@]}" "$@"
}

# spawn_bg <role> <log-path> <cmd...>
spawn_bg() {
    local role="$1" log="$2"; shift 2
    mkdir -p "$(dirname "$log")"
    # env -i + explicit whitelist keeps the child's env small.
    local env_lines
    env_lines=$(emit_whitelisted_env)
    local -a env_argv=()
    while IFS= read -r kv; do
        [[ -n "$kv" ]] && env_argv+=("$kv")
    done <<< "$env_lines"

    # nohup+background, redirect stderr+stdout to log, write pid file.
    nohup /usr/bin/env -i "${env_argv[@]}" "$@" >"$log" 2>&1 &
    local pid=$!
    SPAWNED_PIDS+=("$pid")
    local pf="$PIDS_DIR/$role.pid"
    umask 0177
    printf '%d\n' "$pid" > "$pf"
    umask 0022
    echo "deploy.sh: spawned $role pid=$pid (log: $log)"
}

# wait_tcp <host> <port> [<timeout_s>]  — poll for TCP LISTEN.
wait_tcp() {
    local host="$1" port="$2" timeout="${3:-${LOOM_DEPLOY_READY_TIMEOUT_SEC:-$READY_TIMEOUT_DEFAULT}}"
    local deadline=$(( $(date +%s) + timeout ))
    while (( $(date +%s) < deadline )); do
        if (echo > "/dev/tcp/$host/$port") 2>/dev/null; then
            return 0
        fi
        sleep 0.2
    done
    return 1
}

# wait_http <url> [<timeout_s>]  — poll for any HTTP response.
wait_http_any() {
    local url="$1" timeout="${2:-${LOOM_DEPLOY_READY_TIMEOUT_SEC:-$READY_TIMEOUT_DEFAULT}}"
    local deadline=$(( $(date +%s) + timeout ))
    while (( $(date +%s) < deadline )); do
        if curl -fsS -o /dev/null "$url" 2>/dev/null; then
            return 0
        fi
        # -f fails on 4xx/5xx; try again without -f (any HTTP status = up).
        if curl -sS -o /dev/null -m 2 "$url" 2>/dev/null; then
            return 0
        fi
        sleep 0.2
    done
    return 1
}

# wait_http_ok <url> [<timeout_s>]  — poll for 200.
wait_http_ok() {
    local url="$1" timeout="${2:-${LOOM_DEPLOY_READY_TIMEOUT_SEC:-$READY_TIMEOUT_DEFAULT}}"
    local deadline=$(( $(date +%s) + timeout ))
    while (( $(date +%s) < deadline )); do
        if curl -fsS -o /dev/null "$url" 2>/dev/null; then
            return 0
        fi
        sleep 0.2
    done
    return 1
}

# wait_whoami <stub-port> <proxy-token> [<timeout_s>]  — 200 on /whoami.
wait_whoami() {
    local port="$1" token="$2" timeout="${3:-${LOOM_DEPLOY_READY_TIMEOUT_SEC:-$READY_TIMEOUT_DEFAULT}}"
    local deadline=$(( $(date +%s) + timeout ))
    while (( $(date +%s) < deadline )); do
        if curl -fsS -o /dev/null -H "Authorization: Bearer $token" \
                "http://127.0.0.1:$port/api/agent/whoami" 2>/dev/null; then
            return 0
        fi
        sleep 0.2
    done
    return 1
}

# --- bring-up: mode=stub -----------------------------------------------

bringup_stub() {
    local stub_bin="$BIN_DIR/agentserver-stub"
    [[ -x "$stub_bin" ]] || die "$stub_bin not executable; build with: cd multi-agent && go build -o deploy/linux/bin/agentserver-stub ./tools/eval/agentserver-stub"

    # Step 1: stub.
    spawn_bg agentserver-stub "$LOOM_HOME/logs/agentserver-stub.log" \
        "$stub_bin" --listen "127.0.0.1:$STUB_PORT" --workspace-id auto
    wait_http_ok "http://127.0.0.1:$STUB_PORT/healthz" \
        || { FAILED=4; echo "deploy.sh: stub /healthz did not respond within timeout" >&2; exit 4; }

    # Step 2: observer install + spawn.
    local obs_bin
    for candidate in "$BIN_DIR/observer-server.linux-amd64" "$BIN_DIR/observer-server.linux-arm64" "$BIN_DIR/observer-server"; do
        [[ -x "$candidate" ]] && obs_bin="$candidate" && break
    done
    [[ -n "${obs_bin:-}" ]] || die "no observer-server binary in $BIN_DIR (expected observer-server.linux-*)"
    local obs_apikey
    obs_apikey=$(head -c 16 /dev/urandom | xxd -p -c 32)
    if ! run_whitelisted bash "$SCRIPT_DIR/observer/install.sh" --name eval-obs \
            --loom-home "$LOOM_HOME/observer" \
            --listen "127.0.0.1:$OBSERVER_PORT" \
            --api-key "$obs_apikey" \
            --bin "$obs_bin" \
            >"$LOOM_HOME/logs/observer-install.log" 2>&1; then
        cat "$LOOM_HOME/logs/observer-install.log" >&2
        die "observer install.sh failed" 3
    fi
    spawn_bg observer "$LOOM_HOME/logs/observer.log" \
        "$LOOM_HOME/observer/observer-server" -config "$LOOM_HOME/observer/observer.yaml"
    wait_tcp 127.0.0.1 "$OBSERVER_PORT" \
        || { FAILED=4; echo "deploy.sh: observer :$OBSERVER_PORT did not LISTEN" >&2; exit 4; }

    # Step 3: slave install + patch + spawn.
    local slave_bin
    for candidate in "$BIN_DIR/slave-agent.linux-amd64" "$BIN_DIR/slave-agent.linux-arm64" "$BIN_DIR/slave-agent"; do
        [[ -x "$candidate" ]] && slave_bin="$candidate" && break
    done
    [[ -n "${slave_bin:-}" ]] || die "no slave-agent binary in $BIN_DIR"
    if ! run_whitelisted bash "$SCRIPT_DIR/slave/install.sh" --name eval-slave \
            --loom-home "$LOOM_HOME/slave" \
            --observer-url "http://127.0.0.1:$OBSERVER_PORT" \
            --workspace ws-eval-auto \
            --bin "$slave_bin" \
            >"$LOOM_HOME/logs/slave-install.log" 2>&1; then
        cat "$LOOM_HOME/logs/slave-install.log" >&2
        die "slave install.sh failed" 3
    fi
    # Issue stub credentials + patch config.
    local slave_creds slave_cfg="$LOOM_HOME/slave/config.yaml"
    mkdir -p "$LOOM_HOME/slave/creds"
    slave_creds="$LOOM_HOME/slave/creds/slave.json"
    umask 0177
    run_whitelisted "$stub_bin" issue --server "http://127.0.0.1:$STUB_PORT" \
        --role slave --short-id slv-eval-001 > "$slave_creds"
    umask 0022
    local slave_sandbox slave_tunnel slave_proxy slave_ws slave_short
    slave_sandbox=$(run_whitelisted jq -r .sandbox_id "$slave_creds")
    slave_tunnel=$(run_whitelisted jq -r .tunnel_token "$slave_creds")
    slave_proxy=$(run_whitelisted jq -r .proxy_token "$slave_creds")
    slave_ws=$(run_whitelisted jq -r .workspace_id "$slave_creds")
    slave_short=$(run_whitelisted jq -r .short_id "$slave_creds")
    run_whitelisted yq -i ".server.url = \"http://127.0.0.1:$STUB_PORT\"" "$slave_cfg"
    run_whitelisted yq -i ".credentials.sandbox_id = \"$slave_sandbox\"" "$slave_cfg"
    run_whitelisted yq -i ".credentials.tunnel_token = \"$slave_tunnel\"" "$slave_cfg"
    run_whitelisted yq -i ".credentials.proxy_token = \"$slave_proxy\"" "$slave_cfg"
    run_whitelisted yq -i ".credentials.workspace_id = \"$slave_ws\"" "$slave_cfg"
    run_whitelisted yq -i ".credentials.short_id = \"$slave_short\"" "$slave_cfg"
    run_whitelisted yq -i ".daemon.auto_start = false" "$slave_cfg"
    run_whitelisted yq -i ".daemon.listen = \"127.0.0.1:$SLAVE_PORT\"" "$slave_cfg"

    spawn_bg slave "$LOOM_HOME/logs/slave.log" \
        "$LOOM_HOME/slave/slave-agent" "$slave_cfg"
    wait_whoami "$STUB_PORT" "$slave_proxy" \
        || { FAILED=4; echo "deploy.sh: slave whoami round-trip failed" >&2; exit 4; }

    # Step 4: driver install + patch + spawn.
    local driver_bin
    for candidate in "$BIN_DIR/driver-agent.linux-amd64" "$BIN_DIR/driver-agent.linux-arm64" "$BIN_DIR/driver-agent"; do
        [[ -x "$candidate" ]] && driver_bin="$candidate" && break
    done
    [[ -n "${driver_bin:-}" ]] || die "no driver-agent binary in $BIN_DIR"
    if ! run_whitelisted bash "$SCRIPT_DIR/driver/install.sh" --project "$LOOM_HOME/driver" \
            --name eval-driver \
            --observer-url "http://127.0.0.1:$OBSERVER_PORT" \
            --bin "$driver_bin" \
            >"$LOOM_HOME/logs/driver-install.log" 2>&1; then
        cat "$LOOM_HOME/logs/driver-install.log" >&2
        die "driver install.sh failed" 3
    fi
    local driver_creds driver_cfg="$LOOM_HOME/driver/config.yaml"
    mkdir -p "$LOOM_HOME/driver/creds"
    driver_creds="$LOOM_HOME/driver/creds/driver.json"
    umask 0177
    run_whitelisted "$stub_bin" issue --server "http://127.0.0.1:$STUB_PORT" \
        --role driver --short-id drv-eval-001 > "$driver_creds"
    umask 0022
    local drv_sandbox drv_tunnel drv_proxy drv_ws drv_short
    drv_sandbox=$(run_whitelisted jq -r .sandbox_id "$driver_creds")
    drv_tunnel=$(run_whitelisted jq -r .tunnel_token "$driver_creds")
    drv_proxy=$(run_whitelisted jq -r .proxy_token "$driver_creds")
    drv_ws=$(run_whitelisted jq -r .workspace_id "$driver_creds")
    drv_short=$(run_whitelisted jq -r .short_id "$driver_creds")
    run_whitelisted yq -i ".server.url = \"http://127.0.0.1:$STUB_PORT\"" "$driver_cfg"
    run_whitelisted yq -i ".credentials.sandbox_id = \"$drv_sandbox\"" "$driver_cfg"
    run_whitelisted yq -i ".credentials.tunnel_token = \"$drv_tunnel\"" "$driver_cfg"
    run_whitelisted yq -i ".credentials.proxy_token = \"$drv_proxy\"" "$driver_cfg"
    run_whitelisted yq -i ".credentials.workspace_id = \"$drv_ws\"" "$driver_cfg"
    run_whitelisted yq -i ".credentials.short_id = \"$drv_short\"" "$driver_cfg"

    spawn_bg driver "$LOOM_HOME/logs/driver.log" \
        "$driver_bin" serve-daemon --config "$driver_cfg" --listen "127.0.0.1:$DRIVER_PORT"
    wait_tcp 127.0.0.1 "$DRIVER_PORT" \
        || { FAILED=4; echo "deploy.sh: driver :$DRIVER_PORT did not LISTEN" >&2; exit 4; }
    wait_http_any "http://127.0.0.1:$DRIVER_PORT/" 5 \
        || { FAILED=4; echo "deploy.sh: driver HTTP did not respond" >&2; exit 4; }
}

# --- bring-up: mode=prod -----------------------------------------------

bringup_prod() {
    prod_preflight
    spawn_bg observer "$LOOM_HOME/logs/observer.log" \
        "$LOOM_HOME/observer/observer-server" -config "$LOOM_HOME/observer/observer.yaml"
    wait_tcp 127.0.0.1 "$OBSERVER_PORT" \
        || { FAILED=4; echo "deploy.sh: observer :$OBSERVER_PORT did not LISTEN" >&2; exit 4; }
    spawn_bg slave "$LOOM_HOME/logs/slave.log" \
        "$LOOM_HOME/slave/slave-agent" "$LOOM_HOME/slave/config.yaml"
    wait_tcp 127.0.0.1 "$SLAVE_PORT" \
        || { FAILED=4; echo "deploy.sh: slave :$SLAVE_PORT did not LISTEN" >&2; exit 4; }
    spawn_bg driver "$LOOM_HOME/logs/driver.log" \
        "$LOOM_HOME/driver/driver-agent" serve-daemon --config "$LOOM_HOME/driver/config.yaml" --listen "127.0.0.1:$DRIVER_PORT"
    wait_tcp 127.0.0.1 "$DRIVER_PORT" \
        || { FAILED=4; echo "deploy.sh: driver :$DRIVER_PORT did not LISTEN" >&2; exit 4; }
    wait_http_any "http://127.0.0.1:$DRIVER_PORT/" 5 \
        || { FAILED=4; echo "deploy.sh: driver HTTP did not respond" >&2; exit 4; }
}

case "$MODE" in
    stub) bringup_stub ;;
    prod) bringup_prod ;;
esac

# Bring-up completed without throwing. From here on the daemons are
# healthy and any failure (topology emit, minor post-processing) must
# NOT cascade into a full stack teardown (round-9 P1-B).
BRINGUP_COMPLETE=1

# --- topology emit -----------------------------------------------------

topology_args=(--mode "$MODE"
    --observer-port "$OBSERVER_PORT"
    --driver-port "$DRIVER_PORT"
    --slave-port "$SLAVE_PORT")
[[ "$MODE" == stub ]] && topology_args+=(--stub-port "$STUB_PORT")

if [[ -n "$TOPOLOGY_OUT" ]]; then
    if ! bash "$TOPOLOGY_HELPER" "${topology_args[@]}" --out "$TOPOLOGY_OUT"; then
        echo "deploy.sh: topology emit failed" >&2
        exit 5
    fi
else
    # Redirect helper's own diagnostics to stderr; JSON only on stdout.
    if ! bash "$TOPOLOGY_HELPER" "${topology_args[@]}"; then
        echo "deploy.sh: topology emit failed" >&2
        exit 5
    fi
fi

exit 0
