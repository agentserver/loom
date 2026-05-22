#!/usr/bin/env bash
# One-shot slave-agent bootstrap. Works on any Linux (amd64/arm64) and on
# Termux/Android (aarch64). No repo clone required.
#
# Run:
#   export LOOM_OBSERVER_URL=http://OBSERVER_HOST:8090 \
#          LOOM_WORKSPACE_ID=WS_ID \
#          LOOM_API_KEY='YOUR_API_KEY'
#   bash <(curl -fsSL \
#     https://github.com/agentserver/loom/releases/download/v0.0.1/bootstrap-slave.sh) \
#     --name slave-myhost            # foreground / Termux
#
#   bash <(curl -fsSL .../bootstrap-slave.sh) \
#     --name slave-myhost --systemd  # systemd-managed (Linux only; needs sudo)
#
# Layout after install:
#   ~/.loom/<NAME>/
#     ├── slave-agent          # amd64/arm64 binary, 0755
#     ├── config.yaml          # 0600, rendered
#     ├── slave.env            # 0600, only if --anthropic-key was passed
#     ├── observer.token       # 0600, auto-written on first boot
#     └── slave.log            # systemd output (only with --systemd)
#
# With --systemd: also writes /etc/systemd/system/slave-agent-<NAME>.service
# and enables + starts it.
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
USE_SYSTEMD=0

while (( $# )); do
  case "$1" in
    --name)          NAME="$2"; shift 2 ;;
    --observer-url)  OBSERVER_URL="$2"; shift 2 ;;
    --workspace)     WORKSPACE_ID="$2"; shift 2 ;;
    --api-key)       API_KEY="$2"; shift 2 ;;
    --anthropic-key) ANTHROPIC_KEY="$2"; shift 2 ;;
    --tag)           TAGS+=("$2"); shift 2 ;;
    --desc)          DESC="$2"; shift 2 ;;
    --systemd)       USE_SYSTEMD=1; shift ;;
    --release)       RELEASE_TAG="$2"; RELEASE_BASE="https://github.com/agentserver/loom/releases/download/${RELEASE_TAG}"; shift 2 ;;
    -h|--help)       sed -n '2,30p' "$0"; exit 0 ;;
    *)               echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

[[ -n "$NAME" ]]         || { echo "ERROR: --name is required" >&2; exit 2; }
[[ -n "$OBSERVER_URL" ]] || { echo "ERROR: --observer-url is required (or LOOM_OBSERVER_URL)" >&2; exit 2; }
[[ -n "$API_KEY" ]]      || { echo "ERROR: --api-key is required (or LOOM_API_KEY)" >&2; exit 2; }
DESC="${DESC:-loom slave-agent ($NAME)}"

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
if command -v pkg >/dev/null 2>&1; then          # Termux
  pkg install -y curl tar nodejs >/dev/null
elif command -v apt-get >/dev/null 2>&1; then    # Debian / Ubuntu
  sudo -n apt-get update -qq 2>/dev/null && sudo -n apt-get install -y -qq curl tar nodejs npm >/dev/null 2>&1 \
    || echo "    (skipped apt install — needs passwordless sudo; ensure curl/tar/nodejs are present manually)"
elif command -v dnf >/dev/null 2>&1; then        # Fedora / RHEL
  sudo -n dnf install -y -q curl tar nodejs npm >/dev/null 2>&1 \
    || echo "    (skipped dnf install — needs passwordless sudo)"
fi
for cmd in curl tar; do
  command -v "$cmd" >/dev/null 2>&1 || { echo "ERROR: '$cmd' not found and auto-install failed; install it then retry" >&2; exit 2; }
done
if ! command -v claude >/dev/null 2>&1; then
  if command -v npm >/dev/null 2>&1; then
    echo "==> installing claude code CLI (npm i -g @anthropic-ai/claude-code)"
    npm install -g @anthropic-ai/claude-code
  else
    echo "WARN: 'claude' not in PATH and 'npm' unavailable — install Node + 'npm i -g @anthropic-ai/claude-code' for the chat skill"
  fi
fi

mkdir -p "$LOOM_HOME"
chmod 0700 "$LOOM_HOME"

echo "==> downloading $BIN_ASSET"
curl -fL --progress-bar -o "$LOOM_HOME/slave-agent" "$RELEASE_BASE/$BIN_ASSET"
chmod 0755 "$LOOM_HOME/slave-agent"

if (( ${#TAGS[@]} == 0 )); then TAGS=(linux); fi
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

if (( USE_SYSTEMD )); then
  command -v systemctl >/dev/null 2>&1 || { echo "ERROR: --systemd requested but 'systemctl' not found (Termux has no systemd)" >&2; exit 2; }
  SERVICE_USER="${USER:-$(id -un)}"
  UNIT_PATH="/etc/systemd/system/slave-agent-$NAME.service"
  echo "==> writing $UNIT_PATH (sudo)"
  sudo tee "$UNIT_PATH" >/dev/null <<EOF
[Unit]
Description=loom slave-agent ($NAME)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$SERVICE_USER
WorkingDirectory=$LOOM_HOME
ExecStart=$LOOM_HOME/slave-agent $LOOM_HOME/config.yaml
Restart=on-failure
RestartSec=5s
StandardOutput=append:$LOOM_HOME/slave.log
StandardError=append:$LOOM_HOME/slave.log
Environment="HOME=$HOME"
EnvironmentFile=-$LOOM_HOME/slave.env

[Install]
WantedBy=multi-user.target
EOF
  sudo chmod 0644 "$UNIT_PATH"
  sudo systemctl daemon-reload
  sudo systemctl enable --now "slave-agent-$NAME.service"
  sleep 2
  sudo systemctl --no-pager status "slave-agent-$NAME.service" | head -10
  cat <<EOF

==> slave installed at $LOOM_HOME (systemd-managed)
    tail logs for the FIRST-RUN device-code URL:
       tail -f $LOOM_HOME/slave.log
    open the printed verification URL in a browser to approve.

EOF
  exit 0
fi

cat <<EOF

==> slave installed at $LOOM_HOME

==> start it (foreground; no systemd):
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

   To keep it alive in the background (Termux):
      termux-wake-lock
      nohup $LOOM_HOME/slave-agent $LOOM_HOME/config.yaml \\
        >$LOOM_HOME/slave.log 2>&1 &
      tail -f $LOOM_HOME/slave.log

EOF
