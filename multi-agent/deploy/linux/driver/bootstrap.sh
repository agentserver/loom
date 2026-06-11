#!/usr/bin/env bash
# One-shot driver-agent bootstrap. Works on any Linux (amd64/arm64) and on
# Termux/Android (aarch64). No repo clone required.
#
# Run inside the directory you want to use as the Claude Code / Codex project:
#
#   export LOOM_OBSERVER_URL=http://OBSERVER_HOST:8090 \
#          LOOM_WORKSPACE_ID=WS_ID \
#          LOOM_API_KEY='YOUR_API_KEY'
#   bash <(curl -fsSL \
#     https://github.com/agentserver/loom/releases/latest/download/bootstrap-driver.sh) \
#     --name driver-myhost
#
# All three of --observer-url / --workspace / --api-key can also come from
# env vars LOOM_OBSERVER_URL / LOOM_WORKSPACE_ID / LOOM_API_KEY.
#
# Release pinning: this script downloads from the latest release by default.
# Pin with `--release v0.0.2` or `export LOOM_RELEASE_TAG=v0.0.2` to roll
# back or get reproducible installs.
#
# What it lays down in $PWD:
#   driver-agent             # amd64/arm64 binary (downloaded from release)
#   config.yaml              # rendered, 0600
#
# claude mode (default):
#   .mcp.json                # Claude Code MCP server registration
#   .claude/skills/...       # multiagent / mcp-acceptance / scaffold-mcp-server
#
# codex mode (--agent codex):
#   .codex/config.toml       # Codex CLI MCP server registration
#   AGENTS.md                # Codex project notes (from driver-codex-prompts.tar.gz)
#   .agents/skills/...       # Codex skills used by AGENTS.md
#
#   logs/
#
# Required release assets at $RELEASE_BASE:
#   driver-agent.linux-{amd64,arm64}
#   driver-skills.tar.gz          # tar of skills/{multiagent,...}
#   driver-codex-prompts.tar.gz   # codex: tar containing AGENTS.md (built from
#                                 #   deploy/linux/driver/prompts-codex/)
#   bootstrap-driver.sh   (this file)

set -euo pipefail

RELEASE_TAG="${LOOM_RELEASE_TAG:-latest}"
# `latest` uses GitHub's /releases/latest/download/<asset> alias which
# 302-redirects to the newest published (non-prerelease) release. Any other
# value pins to /releases/download/<TAG>/<asset>. The two URL shapes differ
# structurally, so they cannot be folded into a single concat.
compute_release_base() {
  if [[ "$1" == "latest" ]]; then
    echo "https://github.com/agentserver/loom/releases/latest/download"
  else
    echo "https://github.com/agentserver/loom/releases/download/$1"
  fi
}
RELEASE_BASE="$(compute_release_base "$RELEASE_TAG")"

NAME=""
OBSERVER_URL="${LOOM_OBSERVER_URL:-}"
WORKSPACE_ID="${LOOM_WORKSPACE_ID:-ws-default}"
API_KEY="${LOOM_API_KEY:-}"
AGENT="${LOOM_AGENT_KIND:-claude}"
DESC=""

while (( $# )); do
  case "$1" in
    --name)         NAME="$2"; shift 2 ;;
    --observer-url) OBSERVER_URL="$2"; shift 2 ;;
    --workspace)    WORKSPACE_ID="$2"; shift 2 ;;
    --api-key)      API_KEY="$2"; shift 2 ;;
    --agent)        AGENT="$2"; shift 2 ;;
    --desc)         DESC="$2"; shift 2 ;;
    --release)      RELEASE_TAG="$2"; RELEASE_BASE="$(compute_release_base "$RELEASE_TAG")"; shift 2 ;;
    -h|--help)      sed -n '2,32p' "$0"; exit 0 ;;
    *)              echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

[[ -n "$NAME" ]]         || { echo "ERROR: --name is required" >&2; exit 2; }
[[ -n "$OBSERVER_URL" ]] || { echo "ERROR: --observer-url is required (or set LOOM_OBSERVER_URL)" >&2; exit 2; }
[[ -n "$API_KEY" ]]      || { echo "ERROR: --api-key is required (or set LOOM_API_KEY)" >&2; exit 2; }
case "$AGENT" in claude|codex) ;; *) echo "ERROR: --agent must be claude or codex" >&2; exit 2 ;; esac
DESC="${DESC:-driver-agent ($NAME)}"

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

if [[ "$AGENT" == "claude" ]]; then
  if ! command -v claude >/dev/null 2>&1; then
    if command -v npm >/dev/null 2>&1; then
      echo "==> installing claude code CLI (npm i -g @anthropic-ai/claude-code)"
      npm install -g @anthropic-ai/claude-code
    else
      echo "WARN: 'claude' not in PATH and 'npm' unavailable — install Node + 'npm i -g @anthropic-ai/claude-code' before launching"
    fi
  fi
else
  if ! command -v codex >/dev/null 2>&1; then
    if command -v npm >/dev/null 2>&1; then
      echo "==> installing openai codex CLI (npm i -g @openai/codex)"
      npm install -g @openai/codex || echo "WARN: codex install failed (requires Node >= 22); install manually"
    else
      echo "WARN: 'codex' not in PATH and 'npm' unavailable — install Node >= 22 + 'npm i -g @openai/codex' before launching"
    fi
  fi
fi

echo "==> downloading $BIN_ASSET"
curl -fL --progress-bar -o "$PROJECT/driver-agent" "$RELEASE_BASE/$BIN_ASSET"
chmod +x "$PROJECT/driver-agent"

if [[ "$AGENT" == "claude" ]]; then
  echo "==> downloading driver-skills.tar.gz"
  mkdir -p "$PROJECT/.claude/skills"
  tmp_tar="$(mktemp)"
  curl -fL --progress-bar -o "$tmp_tar" "$RELEASE_BASE/driver-skills.tar.gz"
  tar -xzf "$tmp_tar" -C "$PROJECT/.claude/skills/"
  rm -f "$tmp_tar"
else
  echo "==> downloading driver-codex-prompts.tar.gz"
  # NOTE: driver-codex-prompts.tar.gz is built from deploy/linux/driver/prompts-codex/
  # and may not be available in older releases — upgrade to the latest release tag if missing.
  tmp_tar="$(mktemp)"
  curl -fL --progress-bar -o "$tmp_tar" "$RELEASE_BASE/driver-codex-prompts.tar.gz"
  tar -xzf "$tmp_tar" -C "$PROJECT/"
  rm -f "$tmp_tar"

  echo "==> downloading driver-skills.tar.gz"
  mkdir -p "$PROJECT/.agents/skills"
  tmp_tar="$(mktemp)"
  curl -fL --progress-bar -o "$tmp_tar" "$RELEASE_BASE/driver-skills.tar.gz"
  tar -xzf "$tmp_tar" -C "$PROJECT/.agents/skills/"
  rm -f "$tmp_tar"
fi

echo "==> writing config.yaml"
if [[ "$AGENT" == "claude" ]]; then
  AGENT_BLOCK="agent:
  kind: claude

claude:
  bin: claude
  workdir: $PROJECT
  extra_args: []"
else
  AGENT_BLOCK="agent:
  kind: codex

codex:
  bin: codex
  workdir: $PROJECT
  extra_args: []"
fi

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

$AGENT_BLOCK

planner:
  bin: ""
  timeout_sec: 300
  extra_args: []

discovery:
  display_name: $NAME
  description: $DESC
  skills: []

listen_addr: 127.0.0.1:0

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
  telemetry_enabled: false
  url: $OBSERVER_URL
  workspace_id: $WORKSPACE_ID
  agent_id: $NAME
  api_key: "$API_KEY"
  token_state_path: $TOKEN_DIR/observer.token
EOF
chmod 0600 "$PROJECT/config.yaml"

if [[ "$AGENT" == "claude" ]]; then
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
else
  echo "==> writing .codex/config.toml"
  mkdir -p "$PROJECT/.codex"
  cat > "$PROJECT/.codex/config.toml" <<EOF
[mcp_servers.driver]
command = "$PROJECT/driver-agent"
args    = ["serve-mcp", "--config", "$PROJECT/config.yaml"]
startup_timeout_sec = 30
tool_timeout_sec    = 120
enabled             = true
EOF
fi

mkdir -p "$PROJECT/logs" "$TOKEN_DIR"
chmod 0700 "$TOKEN_DIR"

if [[ "$AGENT" == "claude" ]]; then
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
      claude                  # approve the 'driver' MCP server on first prompt

EOF
else
  cat <<EOF

==> project ready at $PROJECT
    layout:
      driver-agent             # binary
      config.yaml              # 0600
      .codex/config.toml       # Codex CLI MCP registration
      AGENTS.md                # Codex project notes (auto-read by codex)
      .agents/skills/...       # Codex skills used by AGENTS.md
      logs/

==> one-time agentserver registration (opens a device-code URL on stderr):
      $PROJECT/driver-agent register --config $PROJECT/config.yaml

==> launch (Codex):
      cd $PROJECT
      codex                   # first run will prompt to trust this directory
                              # — required for project-scoped .codex/config.toml
      # then inside codex:    mcp__driver__list_agents
      # auth: \`codex login\` (chat subscription) or export OPENAI_API_KEY

EOF
fi
