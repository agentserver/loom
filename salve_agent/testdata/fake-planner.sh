#!/usr/bin/env bash
# Behavior knobs:
#   FAKE_PLANNER_MODE = route_a | route_empty | plan_diamond | plan_chain | plan_invalid_cycle |
#                       plan_invalid_json | reduce_ok | exit1 | sleep
#   FAKE_PLANNER_SLEEP = seconds
set -euo pipefail
mode="${FAKE_PLANNER_MODE:-reduce_ok}"
case "$mode" in
  route_a)            echo '{"target_id":"agent-a"}';;
  route_empty)        echo '{"target_id":""}';;
  plan_diamond)
    cat <<'EOF'
[
  {"id":"n1","target_id":"agent-a","prompt":"step 1"},
  {"id":"n2","target_id":"agent-b","prompt":"step 2 using {{n1.output}}","depends_on":["n1"]},
  {"id":"n3","target_id":"agent-c","prompt":"step 3 using {{n1.output}}","depends_on":["n1"]},
  {"id":"n4","target_id":"agent-d","prompt":"merge {{n2.output}} {{n3.output}}","depends_on":["n2","n3"]}
]
EOF
    ;;
  plan_chain)
    cat <<'EOF'
[
  {"id":"a","target_id":"agent-a","prompt":"first"},
  {"id":"b","target_id":"agent-b","prompt":"second using {{a.output}}","depends_on":["a"]}
]
EOF
    ;;
  plan_invalid_cycle)
    cat <<'EOF'
[
  {"id":"x","target_id":"agent-a","prompt":"x","depends_on":["y"]},
  {"id":"y","target_id":"agent-b","prompt":"y","depends_on":["x"]}
]
EOF
    ;;
  plan_invalid_json) echo "this is not json at all";;
  reduce_ok)         echo "REDUCED OUTPUT";;
  exit1)             echo "boom" 1>&2; exit 1;;
  sleep)             sleep "${FAKE_PLANNER_SLEEP:-30}";;
  *) echo "unknown FAKE_PLANNER_MODE: $mode" 1>&2; exit 2;;
esac
