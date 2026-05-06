#!/usr/bin/env bash
# Behavior knobs (env):
#   FAKE_CLAUDE_MODE=normal|capability|nochange|exit1|sleep|garbage
#   FAKE_CLAUDE_SLEEP=seconds
set -euo pipefail
mode="${FAKE_CLAUDE_MODE:-nochange}"

emit() {
  printf '%s\n' "$1"
}

case "$mode" in
  normal)
    emit '{"type":"assistant","message":{"content":[{"type":"text","text":"hello "}]}}'
    emit '{"type":"assistant","message":{"content":[{"type":"text","text":"world"}]}}'
    emit '{"type":"result","subtype":"success"}'
    ;;
  capability)
    emit '{"type":"assistant","message":{"content":[{"type":"text","text":"installed foo\n=== CAPABILITY ===\nfoo CLI now available"}]}}'
    emit '{"type":"result","subtype":"success"}'
    ;;
  nochange)
    emit '{"type":"assistant","message":{"content":[{"type":"text","text":"answer\n=== CAPABILITY ===\nNO_CAPABILITY_CHANGE"}]}}'
    emit '{"type":"result","subtype":"success"}'
    ;;
  exit1)
    echo "boom" 1>&2
    exit 1
    ;;
  sleep)
    sleep "${FAKE_CLAUDE_SLEEP:-30}"
    ;;
  garbage)
    emit 'not json at all'
    emit '{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}]}}'
    emit '{"type":"result","subtype":"success"}'
    ;;
  *)
    echo "unknown FAKE_CLAUDE_MODE: $mode" 1>&2
    exit 2
    ;;
esac
