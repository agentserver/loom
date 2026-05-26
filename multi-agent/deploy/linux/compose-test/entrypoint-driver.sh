#!/usr/bin/env bash
# Compose-test entrypoint for the driver.
# 1. Sanity-check the bind-mounted binary
# 2. Run deploy/linux/driver/install.sh to render project dir + config + .mcp.json
# 3. Run `driver-agent register` — blocks on a device-code URL printed on stdout
# 4. After approval, register exits 0; the container exits (the next step,
#    `claude` opening the .mcp.json, is up to the operator)

set -euo pipefail

INSTANCE=compose-driver
PROJECT=/var/lib/loom/$INSTANCE
BIN=/opt/loom/deploy/bin/driver-agent.linux-amd64

if [[ ! -x "$BIN" ]]; then
  cat <<EOF >&2
ERROR: missing $BIN
       Drop the driver binary into deploy/linux/bin/ before 'docker compose up':
         curl -L -o deploy/linux/bin/driver-agent.linux-amd64 \\
           https://github.com/agentserver/loom/releases/download/v0.0.2/driver-agent.linux-amd64
         chmod +x deploy/linux/bin/driver-agent.linux-amd64
EOF
  exit 1
fi

# install.sh's --skill-bundle default path doesn't resolve inside the
# container's mount layout; pass empty (no bundle) explicitly.
/opt/loom/deploy/driver/install.sh \
  --project "$PROJECT" \
  --name "$INSTANCE" \
  --observer-url "http://observer:8090" \
  --workspace ws-test \
  --api-key "$API_KEY" \
  --token-dir "$PROJECT" \
  --skill-bundle "" \
  --agent "${LOOM_AGENT_KIND:-claude}" \
  --bin "$BIN"

cat <<EOF

================================================================
  driver: deploy succeeded.  Now running 'driver-agent register'
  to mint agentserver credentials via device-code OAuth.

  In a few seconds you'll see a line like:

      Open this URL to authenticate:
          https://agent.cs.ac.cn/device?user_code=XXXX-YYYY

  Visit that URL, approve, and the register step will write
  sandbox + tunnel + proxy tokens back into config.yaml.
================================================================

EOF

exec "$PROJECT/driver-agent" register --config "$PROJECT/config.yaml"
