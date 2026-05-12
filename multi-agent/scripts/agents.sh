#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd -P)"
RUN_DIR="${AGENTS_RUN_DIR:-${ROOT_DIR}/.run/agents}"
DRY_RUN=0
ACTION=""

usage() {
  cat <<'EOF'
Usage:
  scripts/agents.sh [--dry-run] start
  scripts/agents.sh [--dry-run] stop
  scripts/agents.sh [--dry-run] restart
  scripts/agents.sh [--dry-run] status
  scripts/agents.sh [--dry-run] register

Starts/stops the local master-agent, slave-agent, and driver-agent.
PID files and logs are stored under .run/agents by default.
EOF
}

rel() {
  local path="$1"
  if [[ "$path" == "$ROOT_DIR"/* ]]; then
    printf '%s\n' "${path#"$ROOT_DIR"/}"
  else
    printf '%s\n' "$path"
  fi
}

say_cmd() {
  printf '+ %s\n' "$*"
}

run_cmd() {
  say_cmd "$*"
  if [[ "$DRY_RUN" == "0" ]]; then
    (cd "$ROOT_DIR" && "$@")
  fi
}

ensure_dirs() {
  if [[ "$DRY_RUN" == "1" ]]; then
    say_cmd mkdir -p "$(rel "$RUN_DIR")"
  else
    mkdir -p "$RUN_DIR"
  fi
}

ensure_config() {
  local cfg="$1"
  local example="${cfg%.yaml}.example.yaml"
  if [[ -f "$ROOT_DIR/$cfg" ]]; then
    return
  fi
  if [[ ! -f "$ROOT_DIR/$example" ]]; then
    printf 'missing config: %s\n' "$cfg" >&2
    return 1
  fi
  run_cmd cp "$example" "$cfg"
}

has_proxy_token() {
  local cfg="$1"
  awk '
    $1 == "proxy_token:" {
      val = $0
      sub(/^[^:]+:[[:space:]]*/, "", val)
      gsub(/["'\'']/, "", val)
      if (val != "") found = 1
    }
    END { exit(found ? 0 : 1) }
  ' "$ROOT_DIR/$cfg"
}

build_agents() {
  run_cmd go build -o bin/master-agent ./cmd/master-agent
  run_cmd go build -o bin/slave-agent ./cmd/slave-agent
  run_cmd go build -o bin/driver-agent ./cmd/driver-agent
}

register_driver_if_needed() {
  local cfg="cmd/driver-agent/config.yaml"
  ensure_config "$cfg"
  if [[ "$DRY_RUN" == "1" ]]; then
    say_cmd bin/driver-agent register --config "$cfg"
    return
  fi
  if has_proxy_token "$cfg"; then
    printf 'driver-agent already registered (%s)\n' "$cfg"
    return
  fi
  run_cmd bin/driver-agent register --config "$cfg"
}

register_agents() {
  ensure_config cmd/master-agent/config.yaml
  ensure_config cmd/slave-agent/config.yaml
  register_driver_if_needed
  printf 'master-agent and slave-agent register automatically on first start if credentials are missing.\n'
}

pid_file() {
  printf '%s/%s.pid\n' "$RUN_DIR" "$1"
}

log_file() {
  printf '%s/%s.log\n' "$RUN_DIR" "$1"
}

is_running() {
  local pid="$1"
  [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null
}

start_daemon() {
  local name="$1"
  local cmd="$2"
  local pid_path
  local log_path
  pid_path="$(pid_file "$name")"
  log_path="$(log_file "$name")"

  if [[ "$DRY_RUN" == "1" ]]; then
    say_cmd "$cmd" ">" "$(rel "$log_path")" "2>&1" "& echo \$! >" "$(rel "$pid_path")"
    return
  fi

  if [[ -f "$pid_path" ]]; then
    local existing
    existing="$(cat "$pid_path" 2>/dev/null || true)"
    if is_running "$existing"; then
      printf '%s already running pid=%s log=%s\n' "$name" "$existing" "$(rel "$log_path")"
      return
    fi
  fi

  rm -f "$pid_path"
  (cd "$ROOT_DIR" && AGENT_PID_PATH="$pid_path" AGENT_CMD="$cmd" nohup setsid bash -lc 'echo $$ > "$AGENT_PID_PATH"; exec bash -lc "$AGENT_CMD"' >"$log_path" 2>&1 &)
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    if [[ -s "$pid_path" ]]; then
      break
    fi
    sleep 0.1
  done
  if [[ ! -s "$pid_path" ]]; then
    printf 'failed to start %s; pid file was not written, see %s\n' "$name" "$(rel "$log_path")" >&2
    return 1
  fi
  printf 'started %s pid=%s log=%s\n' "$name" "$(cat "$pid_path")" "$(rel "$log_path")"
}

start_agents() {
  ensure_dirs
  ensure_config cmd/master-agent/config.yaml
  ensure_config cmd/slave-agent/config.yaml
  ensure_config cmd/driver-agent/config.yaml
  build_agents
  register_driver_if_needed

  start_daemon master-agent "(cd cmd/master-agent && ../../bin/master-agent config.yaml)"
  start_daemon slave-agent "(cd cmd/slave-agent && ../../bin/slave-agent config.yaml)"
  # driver-agent serve-mcp exits when stdin reaches EOF. Keep a quiet pipe open
  # so the detached process can hold its agentserver tunnel.
  start_daemon driver-agent "sleep 2147483647 | bin/driver-agent serve-mcp --config cmd/driver-agent/config.yaml"
}

stop_pid_file() {
  local name="$1"
  local pid_path
  pid_path="$(pid_file "$name")"
  if [[ "$DRY_RUN" == "1" ]]; then
    say_cmd "if running, kill pid from $(rel "$pid_path")"
    return
  fi
  if [[ ! -f "$pid_path" ]]; then
    return
  fi
  local pid
  pid="$(cat "$pid_path" 2>/dev/null || true)"
  if is_running "$pid"; then
    kill -TERM -- "-$pid" 2>/dev/null || true
    pkill -TERM -P "$pid" 2>/dev/null || true
    kill "$pid" 2>/dev/null || true
    for _ in 1 2 3 4 5; do
      if ! is_running "$pid"; then
        break
      fi
      sleep 0.2
    done
    if is_running "$pid"; then
      kill -KILL -- "-$pid" 2>/dev/null || true
      pkill -KILL -P "$pid" 2>/dev/null || true
      kill -9 "$pid" 2>/dev/null || true
    fi
    printf 'stopped %s pid=%s\n' "$name" "$pid"
  fi
  rm -f "$pid_path"
}

stop_agents() {
  ensure_dirs
  stop_pid_file master-agent
  stop_pid_file slave-agent
  stop_pid_file driver-agent

  local root_pat
  root_pat="$(printf '%q' "$ROOT_DIR")"
  if [[ "$DRY_RUN" == "1" ]]; then
    say_cmd pkill -f "$root_pat.*/bin/master-agent|$root_pat.*/bin/slave-agent|$root_pat.*/bin/driver-agent"
    return
  fi
  pkill -f "$ROOT_DIR.*/bin/master-agent" 2>/dev/null || true
  pkill -f "$ROOT_DIR.*/bin/slave-agent" 2>/dev/null || true
  pkill -f "$ROOT_DIR.*/bin/driver-agent" 2>/dev/null || true
}

status_one() {
  local name="$1"
  local pid_path
  local log_path
  pid_path="$(pid_file "$name")"
  log_path="$(log_file "$name")"
  local pid=""
  if [[ -f "$pid_path" ]]; then
    pid="$(cat "$pid_path" 2>/dev/null || true)"
  fi
  local state="stopped"
  if [[ "$DRY_RUN" == "1" ]]; then
    state="unknown"
  elif is_running "$pid"; then
    state="running"
  fi
  printf '%-13s %-8s pid=%s pid_file=%s log=%s\n' "$name" "$state" "${pid:-}" "$(rel "$pid_path")" "$(rel "$log_path")"
}

status_agents() {
  ensure_dirs
  status_one master-agent
  status_one slave-agent
  status_one driver-agent
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run)
      DRY_RUN=1
      shift
      ;;
    -h|--help|help)
      usage
      exit 0
      ;;
    start|stop|restart|status|register)
      ACTION="$1"
      shift
      ;;
    *)
      printf 'unknown argument: %s\n' "$1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

case "$ACTION" in
  start)
    start_agents
    ;;
  stop)
    stop_agents
    ;;
  restart)
    stop_agents
    start_agents
    ;;
  status)
    status_agents
    ;;
  register)
    ensure_dirs
    build_agents
    register_agents
    ;;
  "")
    usage >&2
    exit 2
    ;;
esac
