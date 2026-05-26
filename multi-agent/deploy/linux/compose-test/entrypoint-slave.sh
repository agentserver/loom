#!/usr/bin/env bash
# Compose-test entrypoint for the slave.
# 1. Sanity-check the bind-mounted binary
# 2. Run deploy/linux/slave/install.sh to render config + stage binary
# 3. exec slave-agent — on first start it does device-code OAuth, prints a URL,
#    blocks until approved, then persists creds and starts publishing its
#    capability card to the observer.

set -euo pipefail

INSTANCE=compose-slave
LOOM=/var/lib/loom/$INSTANCE
BIN=/opt/loom/deploy/bin/slave-agent.linux-amd64

if [[ ! -x "$BIN" ]]; then
  cat <<EOF >&2
ERROR: missing $BIN
       Drop the slave binary into deploy/linux/bin/ before 'docker compose up':
         curl -L -o deploy/linux/bin/slave-agent.linux-amd64 \\
           https://github.com/agentserver/loom/releases/download/v0.0.2/slave-agent.linux-amd64
         chmod +x deploy/linux/bin/slave-agent.linux-amd64
EOF
  exit 1
fi

/opt/loom/deploy/slave/install.sh \
  --name "$INSTANCE" \
  --user root \
  --loom-home "$LOOM" \
  --observer-url "http://observer:8090" \
  --workspace ws-test \
  --api-key "$API_KEY" \
  --tag compose --tag test \
  --bin "$BIN"

cat <<EOF

================================================================
  slave: deploy succeeded.  Starting slave-agent in foreground.

  On first start it runs the device-code OAuth flow against
  agent.cs.ac.cn — watch for:

      Open this URL to authenticate:
          https://agent.cs.ac.cn/device?user_code=XXXX-YYYY

  Visit that URL, approve, and the slave will persist its
  sandbox + tunnel + proxy tokens, then connect to the observer.

  Note: the 'chat' skill needs the 'claude' CLI inside the
  container, which this image does NOT install. 'bash' / 'file'
  / 'register_mcp' / 'claude_permissions' work without it.
================================================================

EOF

exec "$LOOM/slave-agent" "$LOOM/config.yaml"
