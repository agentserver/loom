#!/usr/bin/env bash
# Generic Linux driver install — sets up a Claude Code project that hosts
# the driver MCP server.
#
# Unlike the slave, the driver is NOT a long-running daemon: Claude Code
# launches `driver-agent serve-mcp` on demand via the project's .mcp.json,
# and shuts it down when the Claude session ends. So there's no systemd
# unit here — just a project directory with binary + config + .mcp.json.
#
# What it does:
#   1. Detects host arch (amd64 / arm64), picks ../bin/driver-agent.linux-<arch>.
#   2. Renders config.yaml + .mcp.json from templates into PROJECT_DIR.
#   3. Pre-creates the observer token-state parent dir.
#   4. Prints the one-time `register` command.
#
# Usage:
#   ./install.sh --project ~/code/my-driver --name driver-myhost
#   ./install.sh --project ~/code/my-driver --name driver-myhost --api-key 'de4a8e22…'
#
# Flags:
#   --project PATH        target project dir; will be created (REQUIRED)
#   --name NAME           agent display name (REQUIRED)
#   --observer-url URL    observer.url, e.g. http://observer.example.com:8090 (REQUIRED)
#   --workspace ID        observer.workspace_id (default: ws-default)
#   --desc TEXT           discovery.description
#   --api-key KEY         observer.api_key (skip manual edit; or hand-edit after)
#   --skill-bundle PATH   copy a multiagent skill bundle into .claude/skills/
#                         (default: ../../../tests/prod_test/driver/.claude/skills/multiagent if present)
#   --token-dir PATH      observer token parent dir (default: ~/.loom/<NAME>)
#   --bin PATH            override driver-agent binary path
#                         (default: ../bin/driver-agent.linux-{arch})
#
# Binary download:
#   https://github.com/agentserver/loom/releases/download/v0.0.1/driver-agent.linux-amd64
#   https://github.com/agentserver/loom/releases/download/v0.0.1/driver-agent.linux-arm64

set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
BIN_DIR="$HERE/../bin"

PROJECT=""
NAME=""
DESC=""
API_KEY=""
SKILL_BUNDLE=""
TOKEN_DIR=""
BIN_OVERRIDE=""
OBSERVER_URL=""
WORKSPACE_ID="ws-default"

while (( $# )); do
  case "$1" in
    --project)       PROJECT="$2"; shift 2 ;;
    --name)          NAME="$2"; shift 2 ;;
    --desc)          DESC="$2"; shift 2 ;;
    --api-key)       API_KEY="$2"; shift 2 ;;
    --skill-bundle)  SKILL_BUNDLE="$2"; shift 2 ;;
    --token-dir)     TOKEN_DIR="$2"; shift 2 ;;
    --bin)           BIN_OVERRIDE="$2"; shift 2 ;;
    --observer-url)  OBSERVER_URL="$2"; shift 2 ;;
    --workspace)     WORKSPACE_ID="$2"; shift 2 ;;
    -h|--help)       sed -n '2,40p' "$0"; exit 0 ;;
    *)               echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

[[ -n "$PROJECT"      ]] || { echo "ERROR: --project is required" >&2; exit 2; }
[[ -n "$NAME"         ]] || { echo "ERROR: --name is required" >&2; exit 2; }
[[ -n "$OBSERVER_URL" ]] || { echo "ERROR: --observer-url is required (e.g. http://observer.example.com:8090)" >&2; exit 2; }

arch="$(uname -m)"
case "$arch" in
  x86_64|amd64)  CPU_ARCH=amd64 ;;
  aarch64|arm64) CPU_ARCH=arm64 ;;
  *) echo "ERROR: unsupported arch $arch" >&2; exit 2 ;;
esac
BIN_NAME="driver-agent.linux-$CPU_ARCH"
BIN="${BIN_OVERRIDE:-$BIN_DIR/$BIN_NAME}"
[[ -x "$BIN" ]] || {
  echo "ERROR: missing $BIN" >&2
  echo "  download:  curl -L -o $BIN_DIR/$BIN_NAME \\" >&2
  echo "    https://github.com/agentserver/loom/releases/download/v0.0.1/$BIN_NAME && chmod +x $BIN_DIR/$BIN_NAME" >&2
  echo "  or build from multi-agent/ :" >&2
  echo "    CGO_ENABLED=0 GOOS=linux GOARCH=$CPU_ARCH go build -trimpath -ldflags='-s -w' \\" >&2
  echo "      -o deploy/linux/bin/$BIN_NAME ./cmd/driver-agent" >&2
  exit 2
}

DESC="${DESC:-Linux driver-agent ($NAME)}"
TOKEN_DIR="${TOKEN_DIR:-$HOME/.loom/$NAME}"
PROJECT_ABS="$(mkdir -p "$PROJECT" && cd "$PROJECT" && pwd)"

# Default skill bundle = the multiagent skill shipped with the prod_test driver
if [[ -z "$SKILL_BUNDLE" && -d "$HERE/../../../tests/prod_test/driver/.claude/skills/multiagent" ]]; then
  SKILL_BUNDLE="$HERE/../../../tests/prod_test/driver/.claude/skills/multiagent"
fi

echo "==> staging into $PROJECT_ABS"
install -m 0755 "$BIN" "$PROJECT_ABS/driver-agent"

sed \
  -e "s|__AGENT_NAME__|$NAME|g" \
  -e "s|__DESCRIPTION__|$DESC|g" \
  -e "s|__LOOM_HOME__|$TOKEN_DIR|g" \
  -e "s|__OBSERVER_URL__|$OBSERVER_URL|g" \
  -e "s|__WORKSPACE_ID__|$WORKSPACE_ID|g" \
  "$HERE/config.yaml.template" > "$PROJECT_ABS/config.yaml"
chmod 0600 "$PROJECT_ABS/config.yaml"

sed \
  -e "s|__PROJECT_DIR__|$PROJECT_ABS|g" \
  "$HERE/.mcp.json.template" > "$PROJECT_ABS/.mcp.json"

if [[ -n "$API_KEY" ]]; then
  sed -i "s|api_key: \"\"|api_key: \"$API_KEY\"|" "$PROJECT_ABS/config.yaml"
fi

if [[ -n "$SKILL_BUNDLE" && -d "$SKILL_BUNDLE" ]]; then
  echo "==> copying skill bundle from $SKILL_BUNDLE"
  mkdir -p "$PROJECT_ABS/.claude/skills"
  cp -r "$SKILL_BUNDLE" "$PROJECT_ABS/.claude/skills/"
fi

mkdir -p "$TOKEN_DIR"
chmod 0700 "$TOKEN_DIR"
mkdir -p "$PROJECT_ABS/logs"

cat <<EOF

==> project ready at $PROJECT_ABS
    Files:
      driver-agent             # binary (amd64)
      config.yaml              # 0600 — paste observer.api_key if you didn't pass --api-key
      .mcp.json                # tells Claude Code how to launch the MCP server
      .claude/skills/...       # multiagent skill bundle (if found)
      logs/                    # audit logs land here

==> one-time agentserver registration (device-code OAuth):
      $PROJECT_ABS/driver-agent register --config $PROJECT_ABS/config.yaml
    Open the printed verification URL in a browser; creds get written back into
    config.yaml.

EOF

if [[ -z "$API_KEY" ]]; then
  echo "==> WARN: observer.api_key is empty in config.yaml — fill it in before launching Claude Code."
  echo
fi

cat <<EOF
==> launch:
      cd $PROJECT_ABS
      claude                   # Claude Code will start the driver MCP server on demand
    Then in the Claude prompt:
      mcp__driver__list_agents
EOF
