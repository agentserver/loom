#!/usr/bin/env bash
# Manual end-to-end check for master-agent. Requires:
#   - agentserver reachable at $AGENTSERVER_URL
#   - claude on PATH and ANTHROPIC_API_KEY set
#   - at least 2 slave-agents already running and registered to the same workspace
set -euo pipefail
: "${AGENTSERVER_URL:?must set AGENTSERVER_URL}"

script_dir=$(cd "$(dirname "$0")" && pwd)
module_root=$(cd "$script_dir/../../.." && pwd)
cd "$module_root"

work=$(mktemp -d)
trap 'kill "${agent_pid:-0}" 2>/dev/null || true; rm -rf "$work"' EXIT

cat > "$work/config.yaml" <<EOF
server:
  url: $AGENTSERVER_URL
  name: master-e2e
claude: { bin: claude }
planner: { bin: claude, timeout_sec: 60 }
fanout:
  max_concurrency: 4
  default_policy: best_effort
  subtask_defaults: { timeout_sec: 600 }
discovery:
  display_name: master-e2e
  description: e2e
  skills: [route, fanout]
EOF

go build -o "$work/master-agent" ./cmd/master-agent
( cd "$work" && ./master-agent config.yaml ) &
agent_pid=$!

echo "master-agent running as pid $agent_pid in $work"
echo "manually:"
echo "  1. visit https://code-<shortID>.<base> to confirm dashboard"
echo "  2. POST a route task: skill=route, prompt='do X' → should pick a slave and return its output"
echo "  3. POST a fanout task: skill=fanout, prompt='research and summarize Y'"
echo "     → planner emits DAG; check /tasks/<id>/children for sub-task rows"
echo "     → SSE /tasks/<id>/stream shows subtask_dispatched + subtask_done events"
echo "     → final output is reducer's summary"
echo
echo "Press Ctrl-C to stop the agent."
wait "$agent_pid"
