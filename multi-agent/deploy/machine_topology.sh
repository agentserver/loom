#!/usr/bin/env bash
# machine_topology.sh — emit the machine_topology JSON payload defined in
# docs/specs/wt2-deploy-scripts.spec.md §5.1. Consumed by deploy.sh's
# final pipeline step and, indirectly, by internal/evalrun/writer.go's
# Schema.MachineTopology TEXT column.
#
# Usage:
#   machine_topology.sh --mode {stub|prod} \
#       --observer-port <int> --driver-port <int> --slave-port <int> \
#       [--stub-port <int>]                # required iff --mode stub
#       [--out PATH]                       # default: emit to stdout
#
# Exit codes (see spec §3.3):
#   0 — success
#   2 — bad flag (mode invalid, port out of range, --stub-port + prod, etc.)
#   5 — hostname redaction failed (SHA-256 unavailable on host)
#
# Security §7(d): hostnames like `alice@corp-laptop` are split on the last
# `@` and each side hashed independently (SHA-256, first 8 hex).
# LOOM_TEST_HOSTNAME is a test seam — when set, we use it instead of
# `hostname` output. The `$USER` value is NEVER emitted.

set -euo pipefail

# --- helper ------------------------------------------------------------

die() {
    echo "machine_topology.sh: $*" >&2
    exit "${2:-2}"
}

usage() {
    sed -n '3,17p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

# sha256_hex_8 <input>  -> first 8 hex chars of sha256sum(input)
sha256_hex_8() {
    local input="$1"
    if command -v sha256sum >/dev/null 2>&1; then
        printf '%s' "$input" | sha256sum | awk '{print substr($1, 1, 8)}'
    elif command -v shasum >/dev/null 2>&1; then
        printf '%s' "$input" | shasum -a 256 | awk '{print substr($1, 1, 8)}'
    else
        return 1
    fi
}

# redact_hostname <raw>  -> redacted per spec §5.3
redact_hostname() {
    local raw="$1" left right
    if [[ "$raw" == *"@"* ]]; then
        left="${raw%@*}"
        right="${raw##*@}"
        local h1 h2
        h1=$(sha256_hex_8 "$left") || return 1
        h2=$(sha256_hex_8 "$right") || return 1
        printf '%s@%s' "$h1" "$h2"
    else
        sha256_hex_8 "$raw" || return 1
    fi
}

# strip_hostname_from <raw_release> <raw_hostname>
# Best-effort: remove `hostname=<raw>` substring + any bare token equal to
# raw_hostname's first `@`-separated segment. Non-destructive on strings
# that never mentioned the hostname.
strip_hostname_from() {
    local text="$1" host="$2"
    local first="${host%@*}"
    # Escape regex metachars in `first` before passing to sed.
    local esc
    esc=$(printf '%s' "$first" | sed 's/[][\/.^$*]/\\&/g')
    text=$(printf '%s' "$text" | sed -E "s/[[:space:]]*hostname=[^[:space:]]+//g")
    if [[ -n "$esc" ]]; then
        # Word-boundary via POSIX character classes rather than `\b`
        # (which is a GNU-sed extension — BSD/macOS sed treats it as
        # a literal `b`, so the strip silently no-ops there).
        # `(^|[^A-Za-z0-9_])` is the leading edge; we emit the matched
        # non-word char back via `\1` so it isn't consumed.
        # Fresh-review P2-4 round 8.
        text=$(printf '%s' "$text" | sed -E "s/(^|[^A-Za-z0-9_])${esc}([^A-Za-z0-9_]|$)/\1\2/g")
    fi
    printf '%s' "$text"
}

# --- flag parsing ------------------------------------------------------

MODE=""
OBSERVER_PORT=""
DRIVER_PORT=""
SLAVE_PORT=""
STUB_PORT=""
OUT_PATH=""

while (( $# > 0 )); do
    case "$1" in
        --mode)             MODE="$2"; shift 2 ;;
        --observer-port)    OBSERVER_PORT="$2"; shift 2 ;;
        --driver-port)      DRIVER_PORT="$2"; shift 2 ;;
        --slave-port)       SLAVE_PORT="$2"; shift 2 ;;
        --stub-port)        STUB_PORT="$2"; shift 2 ;;
        --out)              OUT_PATH="$2"; shift 2 ;;
        -h|--help)          usage; exit 0 ;;
        *)                  die "unknown flag: $1" ;;
    esac
done

case "$MODE" in
    stub|prod) ;;
    *) die "--mode must be one of {stub,prod}" ;;
esac

for var in OBSERVER_PORT DRIVER_PORT SLAVE_PORT; do
    if [[ -z "${!var}" ]] || ! [[ "${!var}" =~ ^[0-9]+$ ]]; then
        die "--${var,,//_/-} must be a positive integer"
    fi
done

if [[ "$MODE" == stub ]]; then
    if [[ -z "$STUB_PORT" ]] || ! [[ "$STUB_PORT" =~ ^[0-9]+$ ]]; then
        die "--stub-port is required and numeric when --mode stub"
    fi
elif [[ -n "$STUB_PORT" ]]; then
    die "--stub-port is only valid with --mode stub"
fi

# --- collection --------------------------------------------------------

RAW_HOSTNAME="${LOOM_TEST_HOSTNAME:-$(hostname 2>/dev/null || echo "unknown")}"
HOST_REDACTED=$(redact_hostname "$RAW_HOSTNAME") || die "hostname redaction failed (sha256sum/shasum missing)" 5

OS_KERNEL="$(uname -s 2>/dev/null | tr '[:upper:]' '[:lower:]' || echo unknown)"
case "$OS_KERNEL" in
    linux)   OS="linux" ;;
    darwin)  OS="darwin" ;;
    mingw*|msys*|cygwin*|windows*) OS="windows" ;;
    *)       OS="linux" ;;   # best-effort default
esac
KERNEL_REL="$(uname -r 2>/dev/null || echo '')"
OS_RELEASE="$(uname -sr 2>/dev/null || echo '')"
OS_RELEASE="$(strip_hostname_from "$OS_RELEASE" "$RAW_HOSTNAME")"

ARCH_RAW="$(uname -m 2>/dev/null || echo unknown)"
case "$ARCH_RAW" in
    x86_64) ARCH="amd64" ;;
    aarch64) ARCH="arm64" ;;
    arm64|amd64) ARCH="$ARCH_RAW" ;;
    *) ARCH="$ARCH_RAW" ;;
esac

CPU_COUNT="$(nproc 2>/dev/null || getconf _NPROCESSORS_ONLN 2>/dev/null || echo 1)"
if [[ -r /proc/meminfo ]]; then
    MEM_KB="$(awk '/^MemTotal:/ {print $2}' /proc/meminfo)"
    MEM_BYTES=$(( MEM_KB * 1024 ))
elif command -v sysctl >/dev/null 2>&1; then
    MEM_BYTES="$(sysctl -n hw.memsize 2>/dev/null || echo 0)"
else
    MEM_BYTES=0
fi

COLLECTED_AT="${LOOM_TEST_UNIX_TS:-$(date -u +%s)}"

# Deploy script version — attempt git rev-parse against the enclosing
# repo (best-effort; falls back to a static "unversioned" marker so the
# schema-required field is never empty).
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if git -C "$SCRIPT_DIR" rev-parse --short=12 HEAD >/dev/null 2>&1; then
    GIT_SHA="$(git -C "$SCRIPT_DIR" rev-parse --short=12 HEAD)"
    DEPLOY_VERSION="wt2-deploy-scripts@${GIT_SHA}"
else
    DEPLOY_VERSION="wt2-deploy-scripts@0000000"
fi

# --- JSON emit ---------------------------------------------------------

# We assemble the payload via jq if available (safest for escaping);
# otherwise a hand-written builder with careful escaping. jq is a
# preflight requirement for deploy.sh anyway (see §4.5 stub credential
# seeding), so this fallback is exercised only when this script is
# invoked in isolation from a shell that lacks jq — hence a build-out
# rather than a hard failure.

json_string() {
    # Escape a raw string for JSON: replace \, ", and control chars.
    # Delegates to jq when present for correctness on unicode.
    if command -v jq >/dev/null 2>&1; then
        printf '%s' "$1" | jq -Rs .
    else
        local s="$1"
        s="${s//\\/\\\\}"
        s="${s//\"/\\\"}"
        s="${s//$'\t'/\\t}"
        s="${s//$'\n'/\\n}"
        s="${s//$'\r'/\\r}"
        printf '"%s"' "$s"
    fi
}

# component_ports object
if [[ "$MODE" == stub ]]; then
    CP=$(printf '{"agentserver_stub":%d,"observer":%d,"driver":%d,"slave":%d}' \
        "$STUB_PORT" "$OBSERVER_PORT" "$DRIVER_PORT" "$SLAVE_PORT")
else
    CP=$(printf '{"observer":%d,"driver":%d,"slave":%d}' \
        "$OBSERVER_PORT" "$DRIVER_PORT" "$SLAVE_PORT")
fi

payload=$(cat <<EOF
{"schema_version":1,"host":$(json_string "$HOST_REDACTED"),"os":$(json_string "$OS"),"os_release":$(json_string "$OS_RELEASE"),"arch":$(json_string "$ARCH"),"kernel":$(json_string "$KERNEL_REL"),"cpu_count":${CPU_COUNT},"mem_bytes":${MEM_BYTES},"mode":$(json_string "$MODE"),"component_ports":${CP},"collected_at_unix":${COLLECTED_AT},"deploy_script_version":$(json_string "$DEPLOY_VERSION")}
EOF
)

# --- output ------------------------------------------------------------

if [[ -n "$OUT_PATH" ]]; then
    # 0600 via umask (`install` may not exist on all busybox targets).
    umask_prev=$(umask)
    umask 0177
    tmp="${OUT_PATH}.tmp.$$"
    printf '%s\n' "$payload" > "$tmp"
    mv -f "$tmp" "$OUT_PATH"
    umask "$umask_prev"
else
    printf '%s\n' "$payload"
fi
