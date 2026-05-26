#!/usr/bin/env bash
# Compose-test entrypoint for the observer.
# 1. Sanity-check the bind-mounted binary
# 2. Run deploy/linux/observer/install.sh to render config + stage binary
# 3. Print the workspace credentials (so the operator can wire other agents)
# 4. exec observer-server in foreground

set -euo pipefail

INSTANCE=compose-observer
LOOM=/var/lib/loom/$INSTANCE
BIN=/opt/loom/deploy/bin/observer-server.linux-amd64

if [[ ! -x "$BIN" ]]; then
  cat <<EOF >&2
ERROR: missing $BIN
       Drop the observer binary into deploy/linux/bin/ before 'docker compose up':
         curl -L -o deploy/linux/bin/observer-server.linux-amd64 \\
           https://github.com/agentserver/loom/releases/download/v0.0.2/observer-server.linux-amd64
         chmod +x deploy/linux/bin/observer-server.linux-amd64
EOF
  exit 1
fi

/opt/loom/deploy/observer/install.sh \
  --name "$INSTANCE" \
  --user root \
  --loom-home "$LOOM" \
  --listen ":8090" \
  --workspace ws-test \
  --workspace-name "Compose Test Workspace" \
  --api-key "$API_KEY" \
  --bin "$BIN"

cat <<EOF

================================================================
  Observer is up.  Wire other agents to this workspace with:

    observer.url:          http://observer:8090     (inside the compose net)
                           http://127.0.0.1:18090   (from your host)
    observer.workspace_id: ws-test
    observer.api_key:      $API_KEY
================================================================

EOF

exec "$LOOM/observer-server" --config "$LOOM/observer.yaml"
