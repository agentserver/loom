#!/usr/bin/env bash
# Generic Linux observer-server installer.
#
# What it does:
#   1. Detects host arch (amd64 / arm64), picks ../bin/observer-server.linux-<arch>.
#   2. Renders observer.yaml from the template, filling in install dir,
#      listen address, and a bootstrap api-key entry.
#   3. Generates a random api-key if --api-key was not passed (printed once).
#   4. Copies the binary + config into LOOM_HOME.
#   5. With --systemd: installs the unit under /etc/systemd/system/, daemon-reloads,
#      enables + starts the service.
#   6. Without --systemd: prints the foreground command so you can smoke-test.
#
# Workspaces are NOT enumerated in observer config — they are created lazily
# at register time from each agent's declared observer.workspace_id, gated by
# a valid bootstrap api-key. --workspace / --workspace-name on this script are
# operator hints (used only in the post-install echo to remind you which
# workspace the key is intended for) and do not feed observer.yaml.
#
# Usage:
#   ./install.sh --name obs-prod                                  # foreground-mode install
#   ./install.sh --name obs-prod --systemd                        # also install systemd unit
#   ./install.sh --name obs-prod --workspace ws-prod --api-key XX --systemd
#
# Flags:
#   --name NAME           instance name (REQUIRED); becomes unit name and install dir suffix
#   --systemd             install + enable systemd unit (needs sudo)
#   --user USER           service user (default: current $USER); home dir is read from /etc/passwd
#   --loom-home PATH      install dir (default: <service user's $HOME>/.loom/<NAME>)
#   --listen ADDR         listen_addr (default: ":8090")
#   --workspace ID        operator hint for which workspace the api-key targets
#                         (default: ws-default); see note above
#   --workspace-name TEXT operator hint; defaults to --workspace
#   --api-key KEY         bootstrap api-key any agent can use to register
#                         (default: 32-byte random hex printed to stdout)
#   --bin PATH            override observer-server binary path
#                         (default: ../bin/observer-server.linux-<arch>)
#
# Binary download:
#   https://github.com/agentserver/loom/releases/latest/download/observer-server.linux-amd64
#   https://github.com/agentserver/loom/releases/latest/download/observer-server.linux-arm64
#
# Prereqs:
#   * Binary at ../bin/observer-server.linux-<arch> or --bin PATH
#   * sudo if installing the systemd unit

set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
BIN_DIR="$HERE/../bin"

NAME=""
SERVICE_USER="${USER:-$(id -un)}"
LOOM_HOME=""
USE_SYSTEMD=0
LISTEN_ADDR=":8090"
WORKSPACE_ID="ws-default"
WORKSPACE_NAME=""
API_KEY=""
BIN_OVERRIDE=""

while (( $# )); do
  case "$1" in
    --name)            NAME="$2"; shift 2 ;;
    --user)            SERVICE_USER="$2"; shift 2 ;;
    --loom-home)       LOOM_HOME="$2"; shift 2 ;;
    --systemd)         USE_SYSTEMD=1; shift ;;
    --listen)          LISTEN_ADDR="$2"; shift 2 ;;
    --workspace)       WORKSPACE_ID="$2"; shift 2 ;;
    --workspace-name)  WORKSPACE_NAME="$2"; shift 2 ;;
    --api-key)         API_KEY="$2"; shift 2 ;;
    --bin)             BIN_OVERRIDE="$2"; shift 2 ;;
    -h|--help)         sed -n '2,40p' "$0"; exit 0 ;;
    *)                 echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

[[ -n "$NAME" ]] || { echo "ERROR: --name is required" >&2; exit 2; }

SERVICE_USER_HOME="$(getent passwd "$SERVICE_USER" | cut -d: -f6)"
[[ -n "$SERVICE_USER_HOME" ]] || { echo "ERROR: user $SERVICE_USER not found" >&2; exit 2; }
LOOM_HOME="${LOOM_HOME:-$SERVICE_USER_HOME/.loom/$NAME}"
WORKSPACE_NAME="${WORKSPACE_NAME:-$WORKSPACE_ID}"

arch="$(uname -m)"
case "$arch" in
  x86_64|amd64)  CPU_ARCH=amd64 ;;
  aarch64|arm64) CPU_ARCH=arm64 ;;
  *) echo "ERROR: unsupported arch $arch" >&2; exit 2 ;;
esac
BIN_NAME="observer-server.linux-$CPU_ARCH"
BIN="${BIN_OVERRIDE:-$BIN_DIR/$BIN_NAME}"
[[ -x "$BIN" ]] || {
  echo "ERROR: missing $BIN" >&2
  echo "  download:  curl -L -o $BIN_DIR/$BIN_NAME \\" >&2
  echo "    https://github.com/agentserver/loom/releases/latest/download/$BIN_NAME && chmod +x $BIN_DIR/$BIN_NAME" >&2
  echo "  or build from multi-agent/ :" >&2
  echo "    CGO_ENABLED=0 GOOS=linux GOARCH=$CPU_ARCH go build -trimpath -ldflags='-s -w' \\" >&2
  echo "      -o deploy/linux/bin/$BIN_NAME ./cmd/observer-server" >&2
  exit 2
}

# Generate a 32-byte random hex key if none was supplied
GENERATED_KEY=0
if [[ -z "$API_KEY" ]]; then
  API_KEY="$(head -c 32 /dev/urandom | xxd -p -c 64)"
  GENERATED_KEY=1
fi

# Render config. Workspace id/name are no longer pinned in observer config —
# they're lazy-upserted from the agent's observer.workspace_id at register
# time. The --workspace / --workspace-name flags are kept on this script for
# back-compat with operator muscle memory but only feed the post-install
# echo, not the config file.
CONFIG_OUT="$(mktemp)"
sed \
  -e "s|__LISTEN_ADDR__|$LISTEN_ADDR|g" \
  -e "s|__LOOM_HOME__|$LOOM_HOME|g" \
  -e "s|__WS_APIKEY__|$API_KEY|g" \
  "$HERE/config.yaml.template" > "$CONFIG_OUT"

echo "==> creating $LOOM_HOME"
sudo -u "$SERVICE_USER" mkdir -p "$LOOM_HOME"
sudo install -o "$SERVICE_USER" -g "$SERVICE_USER" -m 0755 "$BIN" "$LOOM_HOME/observer-server"
sudo install -o "$SERVICE_USER" -g "$SERVICE_USER" -m 0600 "$CONFIG_OUT" "$LOOM_HOME/observer.yaml"
rm -f "$CONFIG_OUT"

if (( USE_SYSTEMD )); then
  UNIT_OUT="$(mktemp)"
  sed \
    -e "s|__INSTANCE_NAME__|$NAME|g" \
    -e "s|__LOOM_HOME__|$LOOM_HOME|g" \
    -e "s|__SERVICE_USER__|$SERVICE_USER|g" \
    "$HERE/observer-server.service.template" > "$UNIT_OUT"
  UNIT_PATH="/etc/systemd/system/observer-server-$NAME.service"
  echo "==> installing $UNIT_PATH"
  sudo install -o root -g root -m 0644 "$UNIT_OUT" "$UNIT_PATH"
  rm -f "$UNIT_OUT"
  sudo systemctl daemon-reload
  sudo systemctl enable --now "observer-server-$NAME.service"
  sleep 2
  sudo systemctl --no-pager status "observer-server-$NAME.service" | head -15
fi

cat <<EOF

==> done.
    listen_addr:  $LISTEN_ADDR
    db_path:      $LOOM_HOME/observer.db
    config:       $LOOM_HOME/observer.yaml (0600)
EOF

if (( GENERATED_KEY )); then
  cat <<EOF

==> Generated bootstrap api-key for workspace "$WORKSPACE_ID" — store it now,
    it is also written into $LOOM_HOME/observer.yaml but won't be re-shown.

      WORKSPACE: $WORKSPACE_ID
      API_KEY:   $API_KEY

    Slaves and drivers authenticate via proxy token (device-code OAuth),
    so this key is NOT required in their config. It is kept for legacy setups.
EOF
fi

if (( ! USE_SYSTEMD )); then
  cat <<EOF

==> foreground mode. Start it manually:
      sudo -u $SERVICE_USER $LOOM_HOME/observer-server --config $LOOM_HOME/observer.yaml

    To convert to a managed service later, re-run with --systemd.
EOF
fi
