#!/usr/bin/env bash
# Manual end-to-end check for salve-agent. Requires:
#   - agentserver reachable at $AGENTSERVER_URL
#   - claude on PATH and ANTHROPIC_API_KEY set
#   - npx for the everything MCP server
set -euo pipefail
: "${AGENTSERVER_URL:?must set AGENTSERVER_URL}"

# Run from anywhere; resolve the module root.
script_dir=$(cd "$(dirname "$0")" && pwd)
module_root=$(cd "$script_dir/../../.." && pwd)
cd "$module_root"

work=$(mktemp -d)
trap 'kill "${agent_pid:-0}" 2>/dev/null || true; rm -rf "$work"' EXIT

cat > "$work/config.yaml" <<EOF
server:
  url: $AGENTSERVER_URL
  name: salve-e2e
claude:
  bin: claude
mcp_servers:
  everything:
    transport: stdio
    command: npx
    args: ["-y", "@modelcontextprotocol/server-everything"]
discovery:
  display_name: salve-e2e
  description: e2e
  skills: [chat, mcp]
EOF

go build -o "$work/salve-agent" ./cmd/salve-agent
( cd "$work" && ./salve-agent config.yaml ) &
agent_pid=$!

echo "salve-agent running as pid $agent_pid in $work"
echo "manually:"
echo "  1. visit https://code-<shortID>.<base> to confirm dashboard"
echo "  2. POST a task to /api/workspaces/<wid>/tasks with target_id=<sandbox_id>, skill=chat"
echo "  3. POST a task with skill=mcp, prompt='{\"server\":\"everything\",\"tool\":\"echo\",\"args\":{\"message\":\"hi\"}}'"
echo "  4. POST a task with skill=chat, prompt that triggers a failure (e.g., empty)"
echo "  5. confirm data.db has 3 rows; journal/CURRENT_STATE.md updated; SSE stream produces chunks"
echo
echo "Press Ctrl-C to stop the agent."
wait "$agent_pid"
