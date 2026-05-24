#!/usr/bin/env bash
# One-shot observer-server bootstrap. Works on any Linux (amd64/arm64).
# No repo clone required.
#
# Run:
#   export LOOM_WORKSPACE_ID=ws-prod          # optional, default ws-default
#   export LOOM_API_KEY='YOUR_API_KEY'        # optional; random 32-byte hex if unset
#   bash <(curl -fsSL \
#     https://github.com/agentserver/loom/releases/download/v0.0.1/bootstrap-observer.sh) \
#     --name obs-prod            # foreground
#
#   bash <(curl -fsSL .../bootstrap-observer.sh) \
#     --name obs-prod --systemd  # systemd-managed (needs sudo)
#
# Layout after install (default — see --loom-home below to override):
#   ~/.loom/<NAME>/
#     ├── observer-server      # amd64/arm64 binary, 0755
#     ├── observer.yaml        # 0600, rendered
#     ├── observer.db          # SQLite, created on first boot
#     └── observer.log         # systemd output (only with --systemd)
#
# With --systemd: also writes /etc/systemd/system/observer-<NAME>.service
# and enables + starts it.
#
# --loom-home PATH overrides the install dir (default: $HOME/.loom/<NAME>).
# Use `--loom-home .` to install in the current directory, or any absolute /
# relative path. Resolved to an absolute path before being baked into the
# systemd unit so it survives WorkingDirectory / ExecStart.
#
# Required release asset at $RELEASE_BASE:
#   observer-server.linux-{amd64,arm64}

set -euo pipefail

RELEASE_TAG="${LOOM_RELEASE_TAG:-v0.0.1}"
RELEASE_BASE="https://github.com/agentserver/loom/releases/download/${RELEASE_TAG}"

NAME=""
LISTEN_ADDR="${LOOM_LISTEN_ADDR:-:8090}"
WORKSPACE_ID="${LOOM_WORKSPACE_ID:-ws-default}"
WORKSPACE_NAME=""
API_KEY="${LOOM_API_KEY:-}"
LOOM_HOME_OVERRIDE=""
USE_SYSTEMD=0

while (( $# )); do
  case "$1" in
    --name)            NAME="$2"; shift 2 ;;
    --listen)          LISTEN_ADDR="$2"; shift 2 ;;
    --workspace)       WORKSPACE_ID="$2"; shift 2 ;;
    --workspace-name)  WORKSPACE_NAME="$2"; shift 2 ;;
    --api-key)         API_KEY="$2"; shift 2 ;;
    --loom-home)       LOOM_HOME_OVERRIDE="$2"; shift 2 ;;
    --systemd)         USE_SYSTEMD=1; shift ;;
    --release)         RELEASE_TAG="$2"; RELEASE_BASE="https://github.com/agentserver/loom/releases/download/${RELEASE_TAG}"; shift 2 ;;
    -h|--help)         sed -n '2,28p' "$0"; exit 0 ;;
    *)                 echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

[[ -n "$NAME" ]] || { echo "ERROR: --name is required" >&2; exit 2; }
WORKSPACE_NAME="${WORKSPACE_NAME:-$WORKSPACE_ID}"

arch="$(uname -m)"
case "$arch" in
  aarch64|arm64) BIN_ASSET="observer-server.linux-arm64" ;;
  x86_64|amd64)  BIN_ASSET="observer-server.linux-amd64" ;;
  *) echo "ERROR: unsupported arch $arch" >&2; exit 2 ;;
esac

LOOM_HOME="${LOOM_HOME_OVERRIDE:-$HOME/.loom/$NAME}"
mkdir -p "$LOOM_HOME"
LOOM_HOME="$(cd "$LOOM_HOME" && pwd -P)"

echo "==> ensuring deps (curl)"
if command -v apt-get >/dev/null 2>&1; then
  sudo -n apt-get update -qq 2>/dev/null && sudo -n apt-get install -y -qq curl >/dev/null 2>&1 \
    || echo "    (skipped apt install — needs passwordless sudo)"
elif command -v dnf >/dev/null 2>&1; then
  sudo -n dnf install -y -q curl >/dev/null 2>&1 \
    || echo "    (skipped dnf install — needs passwordless sudo)"
fi
command -v curl >/dev/null 2>&1 || { echo "ERROR: 'curl' not found" >&2; exit 2; }

if [[ -z "$API_KEY" ]]; then
  if command -v openssl >/dev/null 2>&1; then
    API_KEY="$(openssl rand -hex 32)"
  else
    API_KEY="$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  fi
  echo "==> generated bootstrap api-key for workspace '$WORKSPACE_ID':"
  echo "    $API_KEY"
  echo "    (save it — slaves/drivers will need this as --api-key)"
fi

mkdir -p "$LOOM_HOME"
chmod 0700 "$LOOM_HOME"

echo "==> downloading $BIN_ASSET"
curl -fL --progress-bar -o "$LOOM_HOME/observer-server" "$RELEASE_BASE/$BIN_ASSET"
chmod 0755 "$LOOM_HOME/observer-server"

echo "==> writing observer.yaml"
cat > "$LOOM_HOME/observer.yaml" <<EOF
listen_addr: "$LISTEN_ADDR"
db_path: $LOOM_HOME/observer.db

workspaces:
  - id: $WORKSPACE_ID
    name: $WORKSPACE_NAME
    api_keys:
      - id: bootstrap
        key: "$API_KEY"
EOF
chmod 0600 "$LOOM_HOME/observer.yaml"

if (( USE_SYSTEMD )); then
  command -v systemctl >/dev/null 2>&1 || { echo "ERROR: --systemd requested but 'systemctl' not found" >&2; exit 2; }
  SERVICE_USER="${USER:-$(id -un)}"
  UNIT_PATH="/etc/systemd/system/observer-$NAME.service"
  echo "==> writing $UNIT_PATH (sudo)"
  sudo tee "$UNIT_PATH" >/dev/null <<EOF
[Unit]
Description=loom observer-server ($NAME)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$SERVICE_USER
WorkingDirectory=$LOOM_HOME
ExecStart=$LOOM_HOME/observer-server --config $LOOM_HOME/observer.yaml
Restart=on-failure
RestartSec=5s
StandardOutput=append:$LOOM_HOME/observer.log
StandardError=append:$LOOM_HOME/observer.log
NoNewPrivileges=true
ProtectSystem=full
ProtectHome=read-only
ReadWritePaths=$LOOM_HOME
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF
  sudo chmod 0644 "$UNIT_PATH"
  sudo systemctl daemon-reload
  sudo systemctl enable --now "observer-$NAME.service"
  sleep 2
  sudo systemctl --no-pager status "observer-$NAME.service" | head -10
  cat <<EOF

==> observer installed at $LOOM_HOME (systemd-managed)
    listen:    $LISTEN_ADDR
    workspace: $WORKSPACE_ID  (api-key above)
    logs:      $LOOM_HOME/observer.log
EOF
  exit 0
fi

cat <<EOF

==> observer installed at $LOOM_HOME

==> start it (foreground; no systemd):
      $LOOM_HOME/observer-server --config $LOOM_HOME/observer.yaml

    listen:    $LISTEN_ADDR
    workspace: $WORKSPACE_ID  (api-key above)

   To keep it alive in the background:
      nohup $LOOM_HOME/observer-server --config $LOOM_HOME/observer.yaml \\
        >$LOOM_HOME/observer.log 2>&1 &
      tail -f $LOOM_HOME/observer.log

EOF
