#!/usr/bin/env bash
# One-shot driver-agent bootstrap. Works on any Linux (amd64/arm64) and on
# Termux/Android (aarch64). No repo clone required.
#
# Run inside the directory you want to use as the Claude Code project:
#
#   export LOOM_OBSERVER_URL=http://OBSERVER_HOST:8090 \
#          LOOM_WORKSPACE_ID=WS_ID \
#          LOOM_API_KEY='YOUR_API_KEY'
#   bash <(curl -fsSL \
#     https://github.com/agentserver/loom/releases/download/v0.0.1/bootstrap-driver.sh) \
#     --name driver-myhost
#
# All three of --observer-url / --workspace / --api-key can also come from
# env vars LOOM_OBSERVER_URL / LOOM_WORKSPACE_ID / LOOM_API_KEY.
#
# What it lays down in $PWD:
#   driver-agent             # amd64/arm64 binary (downloaded from release)
#   config.yaml              # rendered, 0600
#   .mcp.json                # Claude Code MCP server registration
#   .claude/skills/...       # multiagent / mcp-acceptance / scaffold-mcp-server
#   logs/
#
# Required release assets at $RELEASE_BASE:
#   driver-agent.linux-{amd64,arm64}
#   driver-skills.tar.gz     # tar of .claude/skills/{multiagent,mcp-acceptance,scaffold-mcp-server}
#   bootstrap-driver-android.sh   (this file)

set -euo pipefail

RELEASE_TAG="${LOOM_RELEASE_TAG:-v0.0.1}"
RELEASE_BASE="https://github.com/agentserver/loom/releases/download/${RELEASE_TAG}"

NAME=""
OBSERVER_URL="${LOOM_OBSERVER_URL:-}"
WORKSPACE_ID="${LOOM_WORKSPACE_ID:-ws-default}"
API_KEY="${LOOM_API_KEY:-}"
DESC=""

while (( $# )); do
  case "$1" in
    --name)         NAME="$2"; shift 2 ;;
    --observer-url) OBSERVER_URL="$2"; shift 2 ;;
    --workspace)    WORKSPACE_ID="$2"; shift 2 ;;
    --api-key)      API_KEY="$2"; shift 2 ;;
    --desc)         DESC="$2"; shift 2 ;;
    --release)      RELEASE_TAG="$2"; RELEASE_BASE="https://github.com/agentserver/loom/releases/download/${RELEASE_TAG}"; shift 2 ;;
    -h|--help)      sed -n '2,22p' "$0"; exit 0 ;;
    *)              echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

[[ -n "$NAME" ]]         || { echo "ERROR: --name is required" >&2; exit 2; }
[[ -n "$OBSERVER_URL" ]] || { echo "ERROR: --observer-url is required (or set LOOM_OBSERVER_URL)" >&2; exit 2; }
[[ -n "$API_KEY" ]]      || { echo "ERROR: --api-key is required (or set LOOM_API_KEY)" >&2; exit 2; }
DESC="${DESC:-Termux/Android driver-agent ($NAME)}"

arch="$(uname -m)"
case "$arch" in
  aarch64|arm64) BIN_ASSET="driver-agent.linux-arm64" ;;
  x86_64|amd64)  BIN_ASSET="driver-agent.linux-amd64" ;;
  *) echo "ERROR: unsupported arch $arch" >&2; exit 2 ;;
esac

PROJECT="$(pwd)"
TOKEN_DIR="$HOME/.loom/$NAME"

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
    echo "WARN: 'claude' not in PATH and 'npm' unavailable — install Node + 'npm i -g @anthropic-ai/claude-code' before launching"
  fi
fi

echo "==> downloading $BIN_ASSET"
curl -fL --progress-bar -o "$PROJECT/driver-agent" "$RELEASE_BASE/$BIN_ASSET"
chmod +x "$PROJECT/driver-agent"

echo "==> downloading driver-skills.tar.gz"
mkdir -p "$PROJECT/.claude/skills"
tmp_tar="$(mktemp)"
curl -fL --progress-bar -o "$tmp_tar" "$RELEASE_BASE/driver-skills.tar.gz"
tar -xzf "$tmp_tar" -C "$PROJECT/.claude/skills/"
rm -f "$tmp_tar"

echo "==> writing config.yaml"
cat > "$PROJECT/config.yaml" <<EOF
server:
  url: https://agent.cs.ac.cn
  name: $NAME

credentials:
  sandbox_id: ""
  tunnel_token: ""
  proxy_token: ""
  workspace_id: ""
  short_id: ""

discovery:
  display_name: $NAME
  description: $DESC
  skills: []

listen_addr: 127.0.0.1:0

planner:
  bin: claude
  timeout_sec: 300
  extra_args: []

fanout:
  max_concurrency: 2
  default_policy: ""
  policy_by_skill: {}
  subtask_defaults:
    timeout_sec: 600
    max_budget_usd: 0

driver_defaults:
  target_display_name: ""
  task_timeout_sec: 600
  audit_log_dir: ./logs
  disable_uid_check: true
  max_dir_cache_entries: 50000
  artifact_transport: observer_lazy

observer:
  enabled: true
  url: $OBSERVER_URL
  workspace_id: $WORKSPACE_ID
  agent_id: $NAME
  api_key: "$API_KEY"
  token_state_path: $TOKEN_DIR/observer.token
EOF
chmod 0600 "$PROJECT/config.yaml"

echo "==> writing .mcp.json"
cat > "$PROJECT/.mcp.json" <<EOF
{
  "mcpServers": {
    "driver": {
      "command": "$PROJECT/driver-agent",
      "args": ["serve-mcp", "--config", "$PROJECT/config.yaml"]
    }
  }
}
EOF

mkdir -p "$PROJECT/logs" "$TOKEN_DIR"
chmod 0700 "$TOKEN_DIR"

cat <<EOF

==> project ready at $PROJECT
    layout:
      driver-agent             # arm64 binary
      config.yaml              # 0600
      .mcp.json                # Claude Code auto-launches the driver MCP from this
      .claude/skills/...       # auto-loaded by Claude Code on next start
      logs/

==> one-time agentserver registration (opens a device-code URL on stderr):
      $PROJECT/driver-agent register --config $PROJECT/config.yaml

==> launch:
      cd $PROJECT
      claude              # approve the 'driver' MCP server on first prompt
                          # then:  mcp__driver__list_agents

EOF
