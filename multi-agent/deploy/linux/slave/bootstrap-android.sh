#!/usr/bin/env bash
# One-shot slave-agent bootstrap for Termux on Android (aarch64).
#
# Run:
#   export LOOM_OBSERVER_URL=http://OBSERVER_HOST:8090 \
#          LOOM_WORKSPACE_ID=WS_ID \
#          LOOM_API_KEY='YOUR_API_KEY'
#   pkg install -y bash curl && bash <(curl -fsSL \
#     https://github.com/agentserver/loom/releases/download/v0.0.1/bootstrap-slave-android.sh) \
#     --name slave-myandroid
#
# Layout after install (Termux $HOME = /data/data/com.termux/files/home):
#   ~/.loom/<NAME>/
#     ├── slave-agent          # arm64/amd64 binary, 0755
#     ├── config.yaml          # 0600, rendered
#     ├── slave.env            # 0600, only if --anthropic-key was passed
#     ├── observer.token       # 0600, auto-written on first boot
#     └── slave.log            # if you nohup it
#
# Required release asset at $RELEASE_BASE:
#   slave-agent.linux-{arm64,amd64}
#
# All of --observer-url / --workspace / --api-key can come from env vars
# LOOM_OBSERVER_URL / LOOM_WORKSPACE_ID / LOOM_API_KEY.

set -euo pipefail

RELEASE_TAG="${LOOM_RELEASE_TAG:-v0.0.1}"
RELEASE_BASE="https://github.com/agentserver/loom/releases/download/${RELEASE_TAG}"

NAME=""
OBSERVER_URL="${LOOM_OBSERVER_URL:-}"
WORKSPACE_ID="${LOOM_WORKSPACE_ID:-ws-default}"
API_KEY="${LOOM_API_KEY:-}"
ANTHROPIC_KEY="${LOOM_ANTHROPIC_KEY:-}"
DESC=""
TAGS=()

while (( $# )); do
  case "$1" in
    --name)          NAME="$2"; shift 2 ;;
    --observer-url)  OBSERVER_URL="$2"; shift 2 ;;
    --workspace)     WORKSPACE_ID="$2"; shift 2 ;;
    --api-key)       API_KEY="$2"; shift 2 ;;
    --anthropic-key) ANTHROPIC_KEY="$2"; shift 2 ;;
    --tag)           TAGS+=("$2"); shift 2 ;;
    --desc)          DESC="$2"; shift 2 ;;
    --release)       RELEASE_TAG="$2"; RELEASE_BASE="https://github.com/agentserver/loom/releases/download/${RELEASE_TAG}"; shift 2 ;;
    -h|--help)       sed -n '2,25p' "$0"; exit 0 ;;
    *)               echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

[[ -n "$NAME" ]]         || { echo "ERROR: --name is required" >&2; exit 2; }
[[ -n "$OBSERVER_URL" ]] || { echo "ERROR: --observer-url is required (or LOOM_OBSERVER_URL)" >&2; exit 2; }
[[ -n "$API_KEY" ]]      || { echo "ERROR: --api-key is required (or LOOM_API_KEY)" >&2; exit 2; }
DESC="${DESC:-Termux/Android slave-agent ($NAME)}"

arch="$(uname -m)"
case "$arch" in
  aarch64|arm64) BIN_ASSET="slave-agent.linux-arm64"; CPU_ARCH=aarch64 ;;
  x86_64|amd64)  BIN_ASSET="slave-agent.linux-amd64"; CPU_ARCH=amd64 ;;
  *) echo "ERROR: unsupported arch $arch" >&2; exit 2 ;;
esac

LOOM_HOME="$HOME/.loom/$NAME"
CPU_CORES="$(nproc 2>/dev/null || echo 1)"
MEMORY_GB="$(awk '/MemTotal/ {printf "%d", $2/1024/1024+0.5}' /proc/meminfo 2>/dev/null || echo 1)"

echo "==> ensuring deps (curl, tar, nodejs)"
if command -v pkg >/dev/null 2>&1; then
  pkg install -y curl tar nodejs >/dev/null
fi
if ! command -v claude >/dev/null 2>&1; then
  echo "==> installing claude code CLI (npm i -g @anthropic-ai/claude-code)"
  npm install -g @anthropic-ai/claude-code
fi

mkdir -p "$LOOM_HOME"
chmod 0700 "$LOOM_HOME"

echo "==> downloading $BIN_ASSET"
curl -fL --progress-bar -o "$LOOM_HOME/slave-agent" "$RELEASE_BASE/$BIN_ASSET"
chmod 0755 "$LOOM_HOME/slave-agent"

if (( ${#TAGS[@]} == 0 )); then TAGS=(linux android); fi
tag_lines=""
for t in "${TAGS[@]}"; do tag_lines+="    - $t"$'\n'; done

echo "==> writing config.yaml"
cat > "$LOOM_HOME/config.yaml" <<EOF
server:
  url: https://agent.cs.ac.cn
  name: $NAME

credentials:
  sandbox_id: ""
  tunnel_token: ""
  proxy_token: ""
  short_id: ""

claude:
  bin: claude
  workdir: $LOOM_HOME
  extra_args: []

mcp_servers: {}

discovery:
  display_name: $NAME
  description: $DESC
  skills:
    - chat
    - bash
    - register_mcp
    - claude_permissions
    - file

planner:
  bin: claude
  timeout_sec: 300
  extra_args: []

fanout:
  max_concurrency: 1
  default_policy: best_effort
  policy_by_skill: {}

resources:
  cpu:
    cores: $CPU_CORES
    arch: $CPU_ARCH
  memory_gb: $MEMORY_GB
  tags:
${tag_lines%$'\n'}

observer:
  enabled: true
  url: $OBSERVER_URL
  workspace_id: $WORKSPACE_ID
  agent_id: $NAME
  api_key: "$API_KEY"
  token_state_path: $LOOM_HOME/observer.token
EOF
chmod 0600 "$LOOM_HOME/config.yaml"

if [[ -n "$ANTHROPIC_KEY" ]]; then
  echo "==> writing slave.env"
  printf 'ANTHROPIC_API_KEY=%s\n' "$ANTHROPIC_KEY" > "$LOOM_HOME/slave.env"
  chmod 0600 "$LOOM_HOME/slave.env"
fi

cat <<EOF

==> slave installed at $LOOM_HOME

==> start it (foreground — Android has no systemd):
EOF

if [[ -f "$LOOM_HOME/slave.env" ]]; then
cat <<EOF
      set -a; source $LOOM_HOME/slave.env; set +a
      $LOOM_HOME/slave-agent $LOOM_HOME/config.yaml
EOF
else
cat <<EOF
      # if 'claude' is already logged in (claude login):
      $LOOM_HOME/slave-agent $LOOM_HOME/config.yaml
      # otherwise export the API key first:
      #   export ANTHROPIC_API_KEY='sk-ant-...'
EOF
fi

cat <<EOF

   First start prints a device-code URL on stderr — open it in a browser
   to approve; slave writes sandbox/tunnel creds back into config.yaml,
   then registers with observer.

   To keep it alive in the background:
      termux-wake-lock
      nohup $LOOM_HOME/slave-agent $LOOM_HOME/config.yaml \\
        >$LOOM_HOME/slave.log 2>&1 &
      tail -f $LOOM_HOME/slave.log

EOF
