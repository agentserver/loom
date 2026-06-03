#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd -P)"
RUN_DIR="${OBSERVER_RUN_DIR:-${ROOT_DIR}/.run/observer}"
CONFIG_PATH="${OBSERVER_CONFIG:-observer.yaml}"
DRY_RUN=0
ACTION=""

usage() {
  cat <<'EOF'
Usage:
  scripts/observer.sh [--dry-run] start
  scripts/observer.sh [--dry-run] stop
  scripts/observer.sh [--dry-run] restart
  scripts/observer.sh [--dry-run] status

Starts/stops the local observer-server.
PID files and logs are stored under .run/observer by default.
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

pid_file() {
  printf '%s/observer-server.pid\n' "$RUN_DIR"
}

log_file() {
  printf '%s/observer-server.log\n' "$RUN_DIR"
}

is_running() {
  local pid="$1"
  [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null
}

ensure_config() {
	local cfg="$CONFIG_PATH"
	if [[ "$DRY_RUN" == "1" ]]; then
		say_cmd cp cmd/observer-server/config.example.yaml "$cfg"
		return
	fi
	if [[ -f "$ROOT_DIR/$cfg" ]]; then
		return
	fi
  run_cmd cp cmd/observer-server/config.example.yaml "$cfg"
}

build_observer() {
  run_cmd go build -o bin/observer-server ./cmd/observer-server
}

print_urls() {
  printf 'Observer base URL: http://127.0.0.1:8090\n'
  printf '  (HTTP API only; HTML dashboard was removed for multi-user safety.)\n'
  printf '  POST /api/agents/register   (Bearer <api_key>)\n'
  printf '  POST /api/events            (Bearer <agent_token>)\n'
  printf '  GET  /api/tasks/{id}/progress (Bearer <agent_token>)\n'
  printf '  GET  /api/workspaces        (admin, gated by $OBSERVER_WEB_TOKEN if set)\n'
}

start_observer() {
  ensure_dirs
  ensure_config
  build_observer

  local pid_path
  local log_path
  pid_path="$(pid_file)"
  log_path="$(log_file)"
  local cmd="bin/observer-server --config ${CONFIG_PATH}"

  if [[ "$DRY_RUN" == "1" ]]; then
    say_cmd "$cmd" ">" "$(rel "$log_path")" "2>&1" "& echo \$! >" "$(rel "$pid_path")"
    print_urls
    return
  fi

  if [[ -f "$pid_path" ]]; then
    local existing
    existing="$(cat "$pid_path" 2>/dev/null || true)"
    if is_running "$existing"; then
      printf 'observer-server already running pid=%s log=%s\n' "$existing" "$(rel "$log_path")"
      print_urls
      return
    fi
  fi

  rm -f "$pid_path"
  (cd "$ROOT_DIR" && OBSERVER_PID_PATH="$pid_path" OBSERVER_CMD="$cmd" nohup setsid bash -lc 'echo $$ > "$OBSERVER_PID_PATH"; exec bash -lc "$OBSERVER_CMD"' >"$log_path" 2>&1 &)
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    if [[ -s "$pid_path" ]]; then
      break
    fi
    sleep 0.1
  done
  if [[ ! -s "$pid_path" ]]; then
    printf 'failed to start observer-server; pid file was not written, see %s\n' "$(rel "$log_path")" >&2
    return 1
  fi
  printf 'started observer-server pid=%s log=%s\n' "$(cat "$pid_path")" "$(rel "$log_path")"
  print_urls
}

stop_observer() {
  ensure_dirs
  local pid_path
  pid_path="$(pid_file)"
  if [[ "$DRY_RUN" == "1" ]]; then
    say_cmd "if running, kill pid from $(rel "$pid_path")"
    say_cmd pkill -f "$(rel "$ROOT_DIR")/.*bin/observer-server --config ${CONFIG_PATH}"
    return
  fi

  if [[ -f "$pid_path" ]]; then
    local pid
    pid="$(cat "$pid_path" 2>/dev/null || true)"
    if is_running "$pid"; then
      kill -TERM -- "-$pid" 2>/dev/null || true
      kill "$pid" 2>/dev/null || true
      for _ in 1 2 3 4 5; do
        if ! is_running "$pid"; then
          break
        fi
        sleep 0.2
      done
      if is_running "$pid"; then
        kill -KILL -- "-$pid" 2>/dev/null || true
        kill -9 "$pid" 2>/dev/null || true
      fi
      printf 'stopped observer-server pid=%s\n' "$pid"
    fi
    rm -f "$pid_path"
  fi
  pkill -f "$ROOT_DIR.*/bin/observer-server --config ${CONFIG_PATH}" 2>/dev/null || true
}

status_observer() {
  ensure_dirs
  local pid_path
  local log_path
  pid_path="$(pid_file)"
  log_path="$(log_file)"
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
  printf '%-15s %-8s pid=%s pid_file=%s log=%s\n' "observer-server" "$state" "${pid:-}" "$(rel "$pid_path")" "$(rel "$log_path")"
  print_urls
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
    start|stop|restart|status)
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
    start_observer
    ;;
  stop)
    stop_observer
    ;;
  restart)
    stop_observer
    start_observer
    ;;
  status)
    status_observer
    ;;
  "")
    usage >&2
    exit 2
    ;;
esac
