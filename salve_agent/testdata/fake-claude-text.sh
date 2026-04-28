#!/usr/bin/env bash
# Reads merge prompt on stdin, writes the new CURRENT_STATE.md to stdout.
# In normal mode, just echoes a deterministic block so tests can assert.
set -euo pipefail
mode="${FAKE_CLAUDE_TEXT_MODE:-ok}"
case "$mode" in
  ok)
    cat <<'EOF'
## Tools
- updated by fake merge

## MCP Servers
- (none)
EOF
    ;;
  fail)
    echo "merge failed" 1>&2
    exit 1
    ;;
  *)
    echo "unknown FAKE_CLAUDE_TEXT_MODE" 1>&2
    exit 2
    ;;
esac
