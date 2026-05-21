#!/usr/bin/env bash
# Generic Linux slave-agent installer.
#
# What it does:
#   1. Detects host arch (amd64 / arm64), picks the matching binary from ../bin/.
#   2. Renders config.yaml + (optional) systemd unit from the templates, filling
#      in agent name, install dir, service user, host resources.
#   3. Copies the binary + config (and optional slave.env) into LOOM_HOME.
#   4. With --systemd: installs the unit under /etc/systemd/system/, daemon-reloads,
#      enables + starts the service.
#   5. Without --systemd: prints the foreground command so you can smoke-test.
#
# On first start the slave will:
#   * Print a device-code verification URL on stderr (tail slave.log) — open
#     in a browser; agentserver creds get written back into config.yaml.
#   * POST /api/agents/register with observer.api_key, persist the returned
#     per-agent token at observer.token_state_path.
#
# Usage:
#   ./install.sh --name slave-foo                            # foreground-mode install
#   ./install.sh --name slave-foo --systemd                  # also install systemd unit
#   ./install.sh --name slave-foo --systemd --user alice     # run as user `alice`
#
# Flags:
#   --name NAME           agent display name (REQUIRED)
#   --observer-url URL    observer.url, e.g. http://observer.example.com:8090 (REQUIRED)
#   --workspace ID        observer.workspace_id (default: ws-default)
#   --systemd             install + enable systemd unit (needs sudo)
#   --user USER           service user (default: current $USER); home dir is read from /etc/passwd
#   --loom-home PATH      install dir (default: <service user's $HOME>/.loom/<NAME>)
#   --desc TEXT           discovery description (default: "Linux slave-agent (<NAME>)")
#   --tag TAG             extra discovery tag (repeatable)
#   --api-key KEY         observer.api_key (skips manual edit; otherwise you must paste it)
#   --anthropic-key KEY   write ANTHROPIC_API_KEY into slave.env
#
# Prereqs:
#   * Binary downloaded or built into ../bin/slave-agent.linux-{amd64,arm64}
#     (override with --bin PATH). Downloads:
#       https://github.com/agentserver/loom/releases/download/v0.0.1/slave-agent.linux-amd64
#       https://github.com/agentserver/loom/releases/download/v0.0.1/slave-agent.linux-arm64
#   * `claude` CLI installed and in PATH for the service user (or set claude.bin
#     in config.yaml to its absolute path post-install)

set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
BIN_DIR="$HERE/../bin"
BIN_OVERRIDE=""

NAME=""
SERVICE_USER="${USER:-$(id -un)}"
LOOM_HOME=""
USE_SYSTEMD=0
DESC=""
TAGS=()
API_KEY=""
ANTHROPIC_KEY=""
OBSERVER_URL=""
WORKSPACE_ID="ws-default"

while (( $# )); do
  case "$1" in
    --name)           NAME="$2"; shift 2 ;;
    --user)           SERVICE_USER="$2"; shift 2 ;;
    --loom-home)      LOOM_HOME="$2"; shift 2 ;;
    --systemd)        USE_SYSTEMD=1; shift ;;
    --desc)           DESC="$2"; shift 2 ;;
    --tag)            TAGS+=("$2"); shift 2 ;;
    --api-key)        API_KEY="$2"; shift 2 ;;
    --anthropic-key)  ANTHROPIC_KEY="$2"; shift 2 ;;
    --bin)            BIN_OVERRIDE="$2"; shift 2 ;;
    --observer-url)   OBSERVER_URL="$2"; shift 2 ;;
    --workspace)      WORKSPACE_ID="$2"; shift 2 ;;
    -h|--help)        sed -n '2,45p' "$0"; exit 0 ;;
    *)                echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

[[ -n "$NAME"         ]] || { echo "ERROR: --name is required" >&2; exit 2; }
[[ -n "$OBSERVER_URL" ]] || { echo "ERROR: --observer-url is required (e.g. http://observer.example.com:8090)" >&2; exit 2; }

# Resolve service user home
SERVICE_USER_HOME="$(getent passwd "$SERVICE_USER" | cut -d: -f6)"
[[ -n "$SERVICE_USER_HOME" ]] || { echo "ERROR: user $SERVICE_USER not found" >&2; exit 2; }
LOOM_HOME="${LOOM_HOME:-$SERVICE_USER_HOME/.loom/$NAME}"
DESC="${DESC:-Linux slave-agent ($NAME)}"

# Arch → binary
arch="$(uname -m)"
case "$arch" in
  x86_64|amd64) BIN_NAME="slave-agent.linux-amd64"; CPU_ARCH=amd64 ;;
  aarch64|arm64) BIN_NAME="slave-agent.linux-arm64"; CPU_ARCH=aarch64 ;;
  *) echo "ERROR: unsupported arch $arch" >&2; exit 2 ;;
esac
BIN="${BIN_OVERRIDE:-$BIN_DIR/$BIN_NAME}"
[[ -x "$BIN" ]] || {
  echo "ERROR: missing $BIN" >&2
  echo "  download:  curl -L -o $BIN_DIR/$BIN_NAME \\" >&2
  echo "    https://github.com/agentserver/loom/releases/download/v0.0.1/$BIN_NAME && chmod +x $BIN_DIR/$BIN_NAME" >&2
  echo "  or build from multi-agent/ :" >&2
  echo "    CGO_ENABLED=0 GOOS=linux GOARCH=$CPU_ARCH go build -trimpath -ldflags='-s -w' \\" >&2
  echo "      -o deploy/linux/bin/$BIN_NAME ./cmd/slave-agent" >&2
  exit 2
}

# Host resources for the discovery card
CPU_CORES="$(nproc 2>/dev/null || echo 1)"
MEMORY_GB="$(awk '/MemTotal/ {printf "%d", $2/1024/1024+0.5}' /proc/meminfo 2>/dev/null || echo 1)"
TAG_LINES=""
for t in "${TAGS[@]:-linux}"; do TAG_LINES+="    - $t"$'\n'; done
[[ -z "$TAG_LINES" ]] && TAG_LINES="    - linux"$'\n'

# Render config
CONFIG_OUT="$(mktemp)"
sed \
  -e "s|__AGENT_NAME__|$NAME|g" \
  -e "s|__LOOM_HOME__|$LOOM_HOME|g" \
  -e "s|__DESCRIPTION__|$DESC|g" \
  -e "s|__CPU_CORES__|$CPU_CORES|g" \
  -e "s|__CPU_ARCH__|$CPU_ARCH|g" \
  -e "s|__MEMORY_GB__|$MEMORY_GB|g" \
  -e "s|__OBSERVER_URL__|$OBSERVER_URL|g" \
  -e "s|__WORKSPACE_ID__|$WORKSPACE_ID|g" \
  "$HERE/config.yaml.template" > "$CONFIG_OUT"

# Replace the placeholder tag block with the user-supplied tags
python3 - "$CONFIG_OUT" "$TAG_LINES" <<'PY'
import sys, pathlib
p = pathlib.Path(sys.argv[1])
text = p.read_text()
text = text.replace("    - __TAG__                       # add more tags as needed\n", sys.argv[2])
p.write_text(text)
PY

[[ -n "$API_KEY" ]] && sed -i "s|api_key: \"\"|api_key: \"$API_KEY\"|" "$CONFIG_OUT"

echo "==> creating $LOOM_HOME"
sudo -u "$SERVICE_USER" mkdir -p "$LOOM_HOME"
sudo install -o "$SERVICE_USER" -g "$SERVICE_USER" -m 0755 "$BIN" "$LOOM_HOME/slave-agent"
sudo install -o "$SERVICE_USER" -g "$SERVICE_USER" -m 0600 "$CONFIG_OUT" "$LOOM_HOME/config.yaml"
rm -f "$CONFIG_OUT"

if [[ -n "$ANTHROPIC_KEY" ]]; then
  ENV_TMP="$(mktemp)"
  printf 'ANTHROPIC_API_KEY=%s\n' "$ANTHROPIC_KEY" > "$ENV_TMP"
  sudo install -o "$SERVICE_USER" -g "$SERVICE_USER" -m 0600 "$ENV_TMP" "$LOOM_HOME/slave.env"
  rm -f "$ENV_TMP"
fi

if [[ -z "$API_KEY" ]]; then
  echo "==> WARN: observer.api_key is empty in $LOOM_HOME/config.yaml — fill it in before starting."
fi

if (( USE_SYSTEMD )); then
  UNIT_OUT="$(mktemp)"
  sed \
    -e "s|__AGENT_NAME__|$NAME|g" \
    -e "s|__LOOM_HOME__|$LOOM_HOME|g" \
    -e "s|__SERVICE_USER__|$SERVICE_USER|g" \
    -e "s|__SERVICE_USER_HOME__|$SERVICE_USER_HOME|g" \
    "$HERE/slave-agent.service.template" > "$UNIT_OUT"
  UNIT_PATH="/etc/systemd/system/slave-agent-$NAME.service"
  echo "==> installing $UNIT_PATH"
  sudo install -o root -g root -m 0644 "$UNIT_OUT" "$UNIT_PATH"
  rm -f "$UNIT_OUT"
  sudo systemctl daemon-reload
  sudo systemctl enable --now "slave-agent-$NAME.service"
  sleep 2
  sudo systemctl --no-pager status "slave-agent-$NAME.service" | head -15
  cat <<EOF

==> done. Tail the log for the FIRST-RUN device-code URL:
      sudo tail -f $LOOM_HOME/slave.log
    Open the printed verification URL in a browser and approve.
EOF
else
  cat <<EOF

==> done (foreground mode). Start it manually:
      sudo -u $SERVICE_USER $LOOM_HOME/slave-agent $LOOM_HOME/config.yaml
    Watch stderr for the device-code URL on first run.

    To convert to a managed service later, re-run with --systemd.
EOF
fi
