#!/usr/bin/env bash
# Manual end-to-end check. Requires:
#   - agentserver reachable at $AGENTSERVER_URL
#   - claude on PATH and ANTHROPIC_API_KEY set
#   - at least 2 salve_agents already running and registered to the same workspace
set -euo pipefail
: "${AGENTSERVER_URL:?must set AGENTSERVER_URL}"

work=$(mktemp -d)
trap 'kill %1 2>/dev/null || true; rm -rf "$work"' EXIT

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

(cd salve_agent && go build -o "$work/master-agent" ./cmd/master-agent)
( cd "$work" && ./master-agent config.yaml ) &

echo "master agent running in $work (pid $!)"
echo "manually:"
echo "  1. visit https://code-<shortID>.<base> to confirm dashboard"
echo "  2. POST a route task: skill=route, prompt='do X' → should pick a salve and return its output"
echo "  3. POST a fanout task: skill=fanout, prompt='research and summarize Y'"
echo "     → planner emits DAG; check /tasks/<id>/children for sub-task rows"
echo "     → SSE /tasks/<id>/stream shows subtask_dispatched + subtask_done events"
echo "     → final output is reducer's summary"
echo
echo "Press Ctrl-C to stop."
wait
