#!/usr/bin/env bash
# Behavior knobs:
#   FAKE_PLANNER_MODE = route_a | route_empty | plan_diamond | plan_chain | plan_parallel |
#                       plan_mcp_valid | plan_mcp_invalid_arg | plan_mcp_validation_replan |
#                       plan_optional_failure |
#                       plan_build_spec_field | plan_build_mcp_bad_text |
#                       plan_invalid_cycle |
#                       plan_invalid_json | plan_with_skill | reduce_ok | exit1 | sleep
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
  plan_parallel)
    cat <<'EOF'
[
  {"id":"fail","target_id":"agent-a","prompt":"fail"},
  {"id":"slow","target_id":"agent-b","prompt":"slow"}
]
EOF
    ;;
  plan_mcp_valid)
    cat <<'EOF'
[
  {"id":"n0","target_id":"agent-a","skill":"mcp","prompt":"{\"server\":\"srv\",\"tool\":\"render\",\"args\":{\"n\":7}}"}
]
EOF
    ;;
  plan_mcp_invalid_arg)
    cat <<'EOF'
[
  {"id":"n0","target_id":"agent-a","skill":"mcp","prompt":"{\"server\":\"srv\",\"tool\":\"render\",\"args\":{\"n\":7,\"put_url_128\":\"http://x\"}}"}
]
EOF
    ;;
  plan_mcp_validation_replan)
    rf="${FAKE_PLANNER_ROUND_FILE:-/tmp/_fpround}"
    r=$(cat "$rf" 2>/dev/null || echo 0)
    case "$r" in
      0) cat <<'EOF'
[
  {"id":"n0","target_id":"agent-a","skill":"mcp","prompt":"{\"server\":\"srv\",\"tool\":\"render\",\"args\":{\"n\":7,\"put_url_128\":\"http://x\"}}"}
]
EOF
         ;;
      1) cat <<'EOF'
[
  {"id":"n1","target_id":"agent-a","skill":"mcp","prompt":"{\"server\":\"srv\",\"tool\":\"render\",\"args\":{\"n\":7}}"}
]
EOF
         ;;
      *) echo "REDUCED";;
    esac
    echo $((r+1)) > "$rf"
    ;;
  plan_optional_failure)
    cat <<'EOF'
[
  {"id":"required","target_id":"agent-a","prompt":"required"},
  {"id":"optional","target_id":"agent-b","prompt":"optional","depends_on":["required"],"optional":true}
]
EOF
    ;;
  plan_build_spec_field)
    cat <<'EOF'
[
  {"id":"n0","target_id":"agent-a","kind":"build_mcp","skill":"build_mcp","build_spec":{"name":"foo","description":"d","tools":[{"name":"render","description":"d","args_schema":{"type":"object"},"result_description":"r"}]}}
]
EOF
    ;;
  plan_build_mcp_bad_text)
    cat <<'EOF'
[
  {"id":"n0","target_id":"agent-a","kind":"build_mcp","skill":"build_mcp","prompt":"build a reusable server"}
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
  plan_with_skill)
    cat <<'EOF'
[
  {"id":"n0","target_id":"agent-a","prompt":"{\"server\":\"x\",\"tool\":\"y\"}","skill":"mcp"}
]
EOF
    ;;
  negotiate_then_succeed)
    rf="${FAKE_PLANNER_ROUND_FILE:-/tmp/_fpround}"
    r=$(cat "$rf" 2>/dev/null || echo 0)
    case "$r" in
      0) cat <<'EOF'
[{"id":"n0","target_id":"agent-a","kind":"build_mcp","skill":"build_mcp","prompt":"{\"name\":\"foo\",\"description\":\"d\",\"tools\":[{\"name\":\"a\",\"description\":\"d\",\"args_schema\":{\"type\":\"object\"},\"result_description\":\"r\"}],\"iteration\":1}"}]
EOF
         ;;
      1) cat <<'EOF'
[{"id":"n1","target_id":"agent-a","kind":"build_mcp","skill":"build_mcp","prompt":"{\"name\":\"foo\",\"description\":\"d\",\"tools\":[{\"name\":\"a\",\"description\":\"d\",\"args_schema\":{\"type\":\"object\"},\"result_description\":\"r\"}],\"iteration\":2}"}]
EOF
         ;;
      2) cat <<'EOF'
[{"id":"n2","target_id":"agent-a","skill":"mcp","prompt":"{\"server\":\"foo\",\"tool\":\"a\",\"args\":{}}"}]
EOF
         ;;
      *) echo "REDUCED";;
    esac
    echo $((r+1)) > "$rf"
    ;;
  negotiate_forever)
    rf="${FAKE_PLANNER_ROUND_FILE:-/tmp/_fpround}"
    r=$(cat "$rf" 2>/dev/null || echo 0)
    cat <<EOF
[{"id":"n${r}","target_id":"agent-a","kind":"build_mcp","skill":"build_mcp","prompt":"{\"name\":\"foo\",\"description\":\"d\",\"tools\":[{\"name\":\"a\",\"description\":\"d\",\"args_schema\":{\"type\":\"object\"},\"result_description\":\"r\"}],\"iteration\":${r}}"}]
EOF
    echo $((r+1)) > "$rf"
    ;;
  reduce_ok)         echo "REDUCED OUTPUT";;
  exit1)             echo "boom" 1>&2; exit 1;;
  sleep)             sleep "${FAKE_PLANNER_SLEEP:-30}";;
  *) echo "unknown FAKE_PLANNER_MODE: $mode" 1>&2; exit 2;;
esac
