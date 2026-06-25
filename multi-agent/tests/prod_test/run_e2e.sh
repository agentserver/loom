#!/usr/bin/env bash
# Unified E2E test runner — follows E2E_RUNBOOK.md steps automatically.
# Usage:
#   ./tests/prod_test/run_e2e.sh                  # full run (rebuild + all tests)
#   ./tests/prod_test/run_e2e.sh --no-rebuild      # skip binary rebuild
#   ./tests/prod_test/run_e2e.sh --skip-playwright  # skip Playwright live e2e
#   ./tests/prod_test/run_e2e.sh --skip-mcp         # skip driver MCP test
set -uo pipefail

# ── paths ──────────────────────────────────────────────────────────────────
PROD_TEST_DIR="/root/multi-agent/multi-agent/tests/prod_test"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# Worktree root: walk up from this script to find the go.mod
WORKTREE_ROOT="${WORKTREE_ROOT:-$(cd "$SCRIPT_DIR/../.." && pwd)}"
WEBAPP_DIR="$WORKTREE_ROOT/internal/commanderhub/webapp"

ARCH="amd64"
case "$(uname -m)" in
    aarch64|arm64) ARCH="arm64" ;;
esac

# ── flags ──────────────────────────────────────────────────────────────────
NO_REBUILD=false
SKIP_PLAYWRIGHT=false
SKIP_MCP=false

for arg in "$@"; do
    case "$arg" in
        --no-rebuild)      NO_REBUILD=true ;;
        --skip-playwright) SKIP_PLAYWRIGHT=true ;;
        --skip-mcp)        SKIP_MCP=true ;;
        -h|--help)
            head -6 "$0" | tail -5
            exit 0
            ;;
        *) echo "Unknown flag: $arg"; exit 2 ;;
    esac
done

# ── helpers ────────────────────────────────────────────────────────────────
log()  { echo "[e2e $(date '+%H:%M:%S')] $*"; }
die()  { log "FATAL: $*" >&2; exit 2; }
warn() { log "WARN: $*" >&2; }

OBSERVER_PID="" SLAVE_A_PID="" SLAVE_B_PID="" DRIVER_PID="" VITE_PID=""
MCP_EXIT="skip" PLAYWRIGHT_EXIT="skip"

wait_port() {
    local port=$1 timeout=${2:-30} label=${3:-""}
    local deadline=$((SECONDS + timeout))
    while ! ss -tlnp "sport = :$port" 2>/dev/null | grep -q LISTEN; do
        if ((SECONDS > deadline)); then
            log "FATAL: :$port ($label) not listening after ${timeout}s"
            return 1
        fi
        sleep 0.5
    done
    log "  :$port ($label) OK"
}

kill_pid() {
    local pid=$1
    [[ -z "$pid" ]] && return
    kill "$pid" 2>/dev/null && wait "$pid" 2>/dev/null
}

cleanup() {
    local exit_code=$?
    log ""
    log "════════════════ TEARDOWN ════════════════"
    kill_pid "$VITE_PID"
    kill_pid "$DRIVER_PID"
    kill_pid "$SLAVE_B_PID"
    kill_pid "$SLAVE_A_PID"
    kill_pid "$OBSERVER_PID"
    # Fallback: kill orphans from prior runs
    pkill -f 'slave-agent.*config\.yaml' 2>/dev/null || true
    pkill -f 'driver-agent.*serve-daemon' 2>/dev/null || true
    pkill -f 'observer-server.*observer\.yaml' 2>/dev/null || true

    log ""
    log "════════════════ RESULTS ════════════════"
    local result="PASS"
    if [[ "$MCP_EXIT" == "0" ]]; then
        log "  Driver MCP:   PASS ✓"
    elif [[ "$MCP_EXIT" == "skip" ]]; then
        log "  Driver MCP:   SKIP"
    else
        log "  Driver MCP:   FAIL (exit $MCP_EXIT)"
        result="FAIL"
    fi
    if [[ "$PLAYWRIGHT_EXIT" == "0" ]]; then
        log "  Playwright:   PASS ✓"
    elif [[ "$PLAYWRIGHT_EXIT" == "skip" ]]; then
        log "  Playwright:   SKIP"
    else
        log "  Playwright:   FAIL (exit $PLAYWRIGHT_EXIT)"
        result="FAIL"
    fi
    log ""
    if [[ "$result" == "PASS" ]]; then
        log "  Overall:      PASS"
    else
        log "  Overall:      FAIL"
    fi
    log "═════════════════════════════════════════"

    exit "$exit_code"
}
trap cleanup EXIT

# ── Phase 0: Validate environment ──────────────────────────────────────────
log "Phase 0: Validate environment"
[[ -n "${OPENAI_API_KEY:-}" ]] || die "OPENAI_API_KEY not set"
[[ -d "$PROD_TEST_DIR" ]] || die "prod_test dir not found: $PROD_TEST_DIR"
[[ -f "$WORKTREE_ROOT/go.mod" ]] || die "go.mod not found in WORKTREE_ROOT=$WORKTREE_ROOT"
log "  arch=$ARCH  worktree=$WORKTREE_ROOT"

# ── Phase 1: Pre-flight cleanup ───────────────────────────────────────────
log ""
log "Phase 1: Pre-flight cleanup"

# Kill stale processes
stale=$(ps -eo pid,args 2>/dev/null | grep -E 'slave-agent|driver-agent.*serve-daemon|observer-server' | grep -v grep | awk '{print $1}' || true)
if [[ -n "$stale" ]]; then
    log "  Killing stale procs: $stale"
    echo "$stale" | xargs kill 2>/dev/null || true
    sleep 2
fi

# Remove stale locks
rm -f "$PROD_TEST_DIR/slave-agent.lock" \
      "$PROD_TEST_DIR/slave-codex-local/slave-agent.lock" \
      "$PROD_TEST_DIR/slave-codex-local-b/slave-agent.lock"
log "  Stale locks removed"

# Token health
log "  Token health checks:"
for cfg in driver-codex-local/config.yaml slave-codex-local/config.yaml slave-codex-local-b/config.yaml; do
    full="$PROD_TEST_DIR/$cfg"
    [[ -f "$full" ]] || die "config not found: $full"
    tok=$(grep proxy_token "$full" | awk '{print $2}' | tr -d '"')
    status=$(curl -sS -o /dev/null -w "%{http_code}" \
        -H "Authorization: Bearer $tok" \
        https://agent.cs.ac.cn/api/agent/whoami 2>/dev/null || echo "000")
    if [[ "$status" == "200" ]]; then
        log "    $cfg: HTTP $status OK"
    elif [[ "$status" == "403" ]]; then
        warn "$cfg: HTTP $status — sandbox forbidden (may recover after tunnel reconnects)"
    else
        die "$cfg: HTTP $status — token invalid. See E2E_RUNBOOK.md re-register section."
    fi
done

# ── Phase 2: Rebuild binaries ─────────────────────────────────────────────
if [[ "$NO_REBUILD" == "true" ]]; then
    log ""
    log "Phase 2: SKIP (--no-rebuild)"
else
    log ""
    log "Phase 2: Rebuild binaries from worktree HEAD"
    cd "$WORKTREE_ROOT"
    for target in observer-server driver-agent slave-agent; do
        out="$PROD_TEST_DIR/bin/${target}.linux-${ARCH}"
        log "  Building $target → $out"
        CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' \
            -o "$out" "./cmd/$target" || die "go build $target failed"
    done
    log "  All binaries built"
fi

# Verify binaries exist and are executable
for bin in observer-server driver-agent slave-agent; do
    p="$PROD_TEST_DIR/bin/${bin}.linux-${ARCH}"
    [[ -x "$p" ]] || die "Binary not executable: $p"
done

# ── Phase 3: Start services ──────────────────────────────────────────────
log ""
log "Phase 3: Start services (observer → slaves → driver)"
BIN="$PROD_TEST_DIR/bin"

# Observer
log "  Starting observer..."
mkdir -p "$PROD_TEST_DIR/.commander-manual/logs"
"$BIN/observer-server.linux-$ARCH" -config "$PROD_TEST_DIR/.commander-manual/observer.yaml" \
    > "$PROD_TEST_DIR/.commander-manual/logs/observer.log" 2>&1 &
OBSERVER_PID=$!
wait_port 18091 30 "observer" || die "observer failed to start"

# Slave A
log "  Starting slave A..."
mkdir -p "$PROD_TEST_DIR/slave-codex-local/logs"
rm -f "$PROD_TEST_DIR/slave-codex-local/slave-agent.lock"
cd "$PROD_TEST_DIR/slave-codex-local"
"$BIN/slave-agent.linux-$ARCH" ./config.yaml \
    > logs/slave-agent.log 2>&1 &
SLAVE_A_PID=$!
wait_port 18093 30 "slave-A" || die "slave-A failed to start"

# Slave B
log "  Starting slave B..."
mkdir -p "$PROD_TEST_DIR/slave-codex-local-b/logs"
rm -f "$PROD_TEST_DIR/slave-codex-local-b/slave-agent.lock"
cd "$PROD_TEST_DIR/slave-codex-local-b"
"$BIN/slave-agent.linux-$ARCH" ./config.yaml \
    > logs/slave-agent.log 2>&1 &
SLAVE_B_PID=$!
wait_port 18094 30 "slave-B" || die "slave-B failed to start"

# Driver daemon
log "  Starting driver daemon..."
mkdir -p "$PROD_TEST_DIR/driver-codex-local/logs"
cd "$PROD_TEST_DIR/driver-codex-local"
"$BIN/driver-agent.linux-$ARCH" serve-daemon \
    --config ./config.yaml --listen 127.0.0.1:18092 \
    > logs/driver-daemon.log 2>&1 &
DRIVER_PID=$!
wait_port 18092 30 "driver" || die "driver failed to start"

# Post-start checks
log "  Waiting for daemon-link WS + PublishCard propagation..."
sleep 6

for d in slave-codex-local slave-codex-local-b; do
    logfile="$PROD_TEST_DIR/$d/logs/slave-agent.log"
    if grep -q "tunnel connected" "$logfile" 2>/dev/null; then
        log "    $d: tunnel connected ✓"
    else
        warn "$d: 'tunnel connected' not found in log"
    fi
    if grep -q "commander daemon ready" "$logfile" 2>/dev/null; then
        log "    $d: commander daemon ready ✓"
    else
        warn "$d: 'commander daemon ready' not found in log"
    fi
done

log "  All services up"

# ── Phase 4: Run tests ───────────────────────────────────────────────────
log ""
log "Phase 4: Run tests"

# 4a: Driver MCP test
if [[ "$SKIP_MCP" == "true" ]]; then
    log "  4a: Driver MCP test — SKIP (--skip-mcp)"
else
    log "  4a: Driver MCP test"
    log "  ─────────────────────────────────────────"
    set +e
    python3 "$PROD_TEST_DIR/driver_mcp_e2e.py" \
        --config "$PROD_TEST_DIR/driver-codex-local/config.yaml" \
        --binary "$BIN/driver-agent.linux-$ARCH" \
        --timeout 300
    MCP_EXIT=$?
    set -e
    if [[ "$MCP_EXIT" == "0" ]]; then
        log "  4a: Driver MCP test PASSED"
    else
        log "  4a: Driver MCP test FAILED (exit $MCP_EXIT)"
    fi
fi

# 4b: Playwright live e2e
if [[ "$SKIP_PLAYWRIGHT" == "true" ]]; then
    log ""
    log "  4b: Playwright live e2e — SKIP (--skip-playwright)"
else
    log ""
    log "  4b: Playwright live e2e"
    log "  ─────────────────────────────────────────"
    if [[ ! -d "$WEBAPP_DIR" ]]; then
        warn "webapp dir not found: $WEBAPP_DIR — skipping Playwright"
        PLAYWRIGHT_EXIT="skip"
    else
        cd "$WEBAPP_DIR"
        # Ensure deps are installed
        if [[ ! -d node_modules ]]; then
            log "    Installing npm dependencies..."
            npm ci --prefer-offline 2>&1 | tail -3
        fi
        set +e
        npx playwright test --config=playwright.live.config.ts
        PLAYWRIGHT_EXIT=$?
        set -e
        if [[ "$PLAYWRIGHT_EXIT" == "0" ]]; then
            log "  4b: Playwright live e2e PASSED"
        else
            log "  4b: Playwright live e2e FAILED (exit $PLAYWRIGHT_EXIT)"
        fi
    fi
fi

# ── Phase 5: Teardown (handled by trap) ──────────────────────────────────
log ""
log "Phase 5: Teardown"

# Set overall exit code
if [[ "$MCP_EXIT" != "0" && "$MCP_EXIT" != "skip" ]]; then
    exit "$MCP_EXIT"
elif [[ "$PLAYWRIGHT_EXIT" != "0" && "$PLAYWRIGHT_EXIT" != "skip" ]]; then
    exit "$PLAYWRIGHT_EXIT"
else
    exit 0
fi
