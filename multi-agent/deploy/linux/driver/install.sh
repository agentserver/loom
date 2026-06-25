#!/usr/bin/env bash
# Generic Linux driver install — sets up a Claude Code / Codex project that
# hosts the driver MCP server.
#
# Unlike the slave, the driver is NOT a long-running daemon: the agent CLI
# launches `driver-agent serve-mcp` on demand via the project MCP config,
# and shuts it down when the session ends. So there's no systemd unit here —
# just a project directory with binary + config + MCP registration.
#
# What it does:
#   1. Detects host arch (amd64 / arm64), picks ../bin/driver-agent.linux-<arch>.
#   2. Renders config.yaml from the template into PROJECT_DIR.
#   3. Writes the MCP registration (claude: .mcp.json, codex: .codex/config.toml).
#   4. Copies the skill/prompts bundle into the project dir.
#   5. Pre-creates the observer token-state parent dir.
#   6. Prints the one-time `register` command and launch hint.
#
# Usage:
#   ./install.sh --project ~/code/my-driver --name driver-myhost
#   ./install.sh --project ~/code/my-driver --name driver-myhost --api-key 'de4a8e22...'
#   ./install.sh --project ~/code/my-driver --name driver-myhost --agent codex
#
# Flags:
#   --project PATH        target project dir; will be created (REQUIRED)
#   --name NAME           agent display name (REQUIRED)
#   --observer-url URL    observer.url, e.g. http://observer.example.com:8090 (REQUIRED)
#   --workspace ID        observer.workspace_id (default: ws-default)
#   --desc TEXT           discovery.description
#   --api-key KEY         observer.api_key (skip manual edit; or hand-edit after)
#   --agent KIND          agent CLI to pair with: codex (default), claude, or opencode
#   --skill-bundle PATH   claude: directory of skills to copy into .claude/skills/
#                         codex: directory of skills to copy into .agents/skills/
#                         (default: auto-detected from local tree if present)
#   --token-dir PATH      observer token parent dir (default: ~/.loom/<NAME>)
#   --bin PATH            override driver-agent binary path
#                         (default: ../bin/driver-agent.linux-{arch})
#
# Binary download:
#   https://github.com/agentserver/loom/releases/latest/download/driver-agent.linux-amd64
#   https://github.com/agentserver/loom/releases/latest/download/driver-agent.linux-arm64

set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
BIN_DIR="$HERE/../bin"
REPO_ROOT="$(cd "$HERE/../../../.." && pwd)"

PROJECT=""
NAME=""
DESC=""
API_KEY=""
SKILL_BUNDLE=""
TOKEN_DIR=""
BIN_OVERRIDE=""
OBSERVER_URL=""
WORKSPACE_ID="ws-default"
AGENT="${LOOM_AGENT_KIND:-codex}"

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
    --agent)         AGENT="$2"; shift 2 ;;
    -h|--help)       sed -n '2,52p' "$0"; exit 0 ;;
    *)               echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

[[ -n "$PROJECT"      ]] || { echo "ERROR: --project is required" >&2; exit 2; }
[[ -n "$NAME"         ]] || { echo "ERROR: --name is required" >&2; exit 2; }
[[ -n "$OBSERVER_URL" ]] || { echo "ERROR: --observer-url is required (e.g. http://observer.example.com:8090)" >&2; exit 2; }
case "$AGENT" in claude|codex|opencode) ;; *) echo "ERROR: --agent must be claude, codex, or opencode" >&2; exit 2 ;; esac

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
  echo "    https://github.com/agentserver/loom/releases/latest/download/$BIN_NAME && chmod +x $BIN_DIR/$BIN_NAME" >&2
  echo "  or build from multi-agent/ :" >&2
  echo "    CGO_ENABLED=0 GOOS=linux GOARCH=$CPU_ARCH go build -trimpath -ldflags='-s -w' \\" >&2
  echo "      -o deploy/linux/bin/$BIN_NAME ./cmd/driver-agent" >&2
  exit 2
}

DESC="${DESC:-Linux driver-agent ($NAME)}"
TOKEN_DIR="${TOKEN_DIR:-$HOME/.loom/$NAME}"
PROJECT_ABS="$(mkdir -p "$PROJECT" && cd "$PROJECT" && pwd)"

# Auto-detect default skill/prompts bundle
if [[ -z "$SKILL_BUNDLE" ]]; then
  case "$AGENT" in
    claude)
      # Default: the multiagent skill shipped with the prod_test driver
      if [[ -d "$HERE/../../../tests/prod_test/driver/.claude/skills/multiagent" ]]; then
        SKILL_BUNDLE="$HERE/../../../tests/prod_test/driver/.claude/skills/multiagent"
      fi
      ;;
    codex|opencode)
      # Default: repo skills, copied to Codex/opencode's project-scoped .agents/skills.
      if [[ -d "$REPO_ROOT/skills" ]]; then
        SKILL_BUNDLE="$REPO_ROOT/skills"
      fi
      ;;
  esac
fi

echo "==> staging into $PROJECT_ABS"
install -m 0755 "$BIN" "$PROJECT_ABS/driver-agent"

# Render config.yaml from template. __PROJECT_DIR__ is substituted here
# so driver_defaults.workdir + agent.workdir (and any future placeholder)
# get the real absolute path; the MCP templates below also substitute it
# because they are rendered from a different template file.
sed \
  -e "s|__AGENT_NAME__|$NAME|g" \
  -e "s|__DESCRIPTION__|$DESC|g" \
  -e "s|__LOOM_HOME__|$TOKEN_DIR|g" \
  -e "s|__OBSERVER_URL__|$OBSERVER_URL|g" \
  -e "s|__WORKSPACE_ID__|$WORKSPACE_ID|g" \
  -e "s|__PROJECT_DIR__|$PROJECT_ABS|g" \
  -e "s|__AGENT_KIND__|$AGENT|g" \
  -e "s|__AGENT_BIN__|$AGENT|g" \
  "$HERE/config.yaml.template" > "$PROJECT_ABS/config.yaml"

chmod 0600 "$PROJECT_ABS/config.yaml"

if [[ -n "$API_KEY" ]]; then
  sed -i "s|api_key: \"\"|api_key: \"$API_KEY\"|" "$PROJECT_ABS/config.yaml"
fi

# Write MCP registration
case "$AGENT" in
  claude)
    sed \
      -e "s|__PROJECT_DIR__|$PROJECT_ABS|g" \
      "$HERE/.mcp.json.template" > "$PROJECT_ABS/.mcp.json"
    ;;
  codex)
    mkdir -p "$PROJECT_ABS/.codex"
    sed \
      -e "s|__PROJECT_DIR__|$PROJECT_ABS|g" \
      "$HERE/codex-mcp.toml.template" > "$PROJECT_ABS/.codex/config.toml"
    ;;
  opencode)
    # opencode CLI + desktop share the same MCP config file
    # (~/.config/opencode/opencode.json on Linux). Writing here means
    # both consume the driver server. If a config already exists, back
    # it up rather than merge JSON (merge is a follow-up if anyone
    # actually maintains a custom opencode.json alongside loom).
    OPENCODE_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/opencode"
    OPENCODE_CFG="$OPENCODE_DIR/opencode.json"
    mkdir -p "$OPENCODE_DIR"
    if [[ -f "$OPENCODE_CFG" ]]; then
      cp -f "$OPENCODE_CFG" "$OPENCODE_CFG.bak.$(date +%Y%m%d-%H%M%S)"
      echo "==> backed up existing opencode.json (loom install will overwrite)"
    fi
    sed \
      -e "s|__DRIVER_AGENT__|$PROJECT_ABS/driver-agent|g" \
      -e "s|__CONFIG__|$PROJECT_ABS/config.yaml|g" \
      "$HERE/opencode.json.template" > "$OPENCODE_CFG"
    echo "==> wrote $OPENCODE_CFG (consumed by both opencode CLI and desktop)"
    ;;
  *)
    echo "unsupported agent: $AGENT (expected claude|codex|opencode)" >&2
    exit 1
    ;;
esac

# Copy skill / prompts bundle
case "$AGENT" in
  claude)
    if [[ -n "$SKILL_BUNDLE" && -d "$SKILL_BUNDLE" ]]; then
      echo "==> copying skill bundle from $SKILL_BUNDLE"
      mkdir -p "$PROJECT_ABS/.claude/skills"
      cp -r "$SKILL_BUNDLE" "$PROJECT_ABS/.claude/skills/"
    fi
    ;;
  codex|opencode)
    # codex / opencode: copy AGENTS.md plus repo-scoped skills that AGENTS.md routes to.
    # opencode also reads project-root AGENTS.md (same convention as codex).
    if [[ -f "$HERE/prompts-codex/AGENTS.md" ]]; then
      echo "==> copying AGENTS.md from $HERE/prompts-codex"
      cp "$HERE/prompts-codex/AGENTS.md" "$PROJECT_ABS/AGENTS.md"
    elif [[ -n "$SKILL_BUNDLE" && -f "$SKILL_BUNDLE/AGENTS.md" ]]; then
      echo "==> copying AGENTS.md from $SKILL_BUNDLE"
      cp "$SKILL_BUNDLE/AGENTS.md" "$PROJECT_ABS/AGENTS.md"
    fi

    if [[ -n "$SKILL_BUNDLE" ]]; then
      CODEX_SKILL_BUNDLE="$SKILL_BUNDLE"
      if [[ -f "$SKILL_BUNDLE/AGENTS.md" && -d "$REPO_ROOT/skills" ]]; then
        CODEX_SKILL_BUNDLE="$REPO_ROOT/skills"
      fi

      if [[ -d "$CODEX_SKILL_BUNDLE" && ! -f "$CODEX_SKILL_BUNDLE/AGENTS.md" ]]; then
        echo "==> copying $AGENT skills from $CODEX_SKILL_BUNDLE"
        mkdir -p "$PROJECT_ABS/.agents/skills"
        if [[ -f "$CODEX_SKILL_BUNDLE/SKILL.md" ]]; then
          cp -r "$CODEX_SKILL_BUNDLE" "$PROJECT_ABS/.agents/skills/"
        else
          cp -r "$CODEX_SKILL_BUNDLE/." "$PROJECT_ABS/.agents/skills/"
        fi
      fi
    fi
    ;;
esac

mkdir -p "$TOKEN_DIR"
chmod 0700 "$TOKEN_DIR"
mkdir -p "$PROJECT_ABS/logs"

case "$AGENT" in
  claude)
    cat <<EOF

==> project ready at $PROJECT_ABS
    Files:
      driver-agent             # binary ($CPU_ARCH)
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
    ;;

  codex)
    cat <<EOF

==> project ready at $PROJECT_ABS
    Files:
      driver-agent             # binary ($CPU_ARCH)
      config.yaml              # 0600 — paste observer.api_key if you didn't pass --api-key
      .codex/config.toml       # Codex CLI MCP registration
      AGENTS.md                # Codex project notes (auto-read by codex)
      .agents/skills/...       # Codex skills used by AGENTS.md
      logs/                    # audit logs land here

==> one-time agentserver registration (device-code OAuth):
      $PROJECT_ABS/driver-agent register --config $PROJECT_ABS/config.yaml
    Open the printed verification URL in a browser; creds get written back into
    config.yaml.

EOF

    if [[ -z "$API_KEY" ]]; then
      echo "==> WARN: observer.api_key is empty in config.yaml — fill it in before launching Codex."
      echo
    fi

    cat <<EOF
==> launch (Codex):
      cd $PROJECT_ABS
      codex                   # first run will prompt to trust this directory
                              # — required for project-scoped .codex/config.toml
      # then inside codex:    mcp__driver__list_agents
      # auth: \`codex login\` (chat subscription) or export OPENAI_API_KEY
EOF
    ;;

  opencode)
    OPENCODE_DIR_DISPLAY="${XDG_CONFIG_HOME:-\$HOME/.config}/opencode"
    cat <<EOF

==> project ready at $PROJECT_ABS
    Files:
      driver-agent             # binary ($CPU_ARCH)
      config.yaml              # 0600 — paste observer.api_key if you didn't pass --api-key
      AGENTS.md                # opencode project notes (auto-read by opencode)
      .agents/skills/...       # opencode skills used by AGENTS.md
      logs/                    # audit logs land here
    Plus user-scoped:
      $OPENCODE_DIR_DISPLAY/opencode.json
                               # MCP registration (shared by opencode CLI + desktop)

==> one-time agentserver registration (device-code OAuth):
      $PROJECT_ABS/driver-agent register --config $PROJECT_ABS/config.yaml
    Open the printed verification URL in a browser; creds get written back into
    config.yaml.

EOF

    if [[ -z "$API_KEY" ]]; then
      echo "==> WARN: observer.api_key is empty in config.yaml — fill it in before launching opencode."
      echo
    fi

    cat <<EOF
==> launch (opencode):
      cd $PROJECT_ABS
      opencode                # picks up driver MCP server via ~/.config/opencode/opencode.json
      # then inside opencode: mcp__driver__list_agents
EOF
    ;;
esac
