#!/usr/bin/env bash
# Fake claude that emits a deterministic minimal Python MCP server,
# wrapped in the stream-json envelope that ClaudeExecutor expects.
#
# Behavior knobs (env):
#   FAKE_BUILD_CLAUDE_MODE = ok | bad_import | bad_syntax | crash
set -euo pipefail
mode="${FAKE_BUILD_CLAUDE_MODE:-ok}"

emit_text() {
  # Print a stream-json line carrying $1 as assistant text content.
  python3 -c '
import json, sys
text = sys.argv[1]
print(json.dumps({"type":"assistant","message":{"content":[{"type":"text","text":text}]}}))
print(json.dumps({"type":"result","subtype":"success"}))
' "$1"
}

case "$mode" in
  ok)
    emit_text '# placeholder header replaced by build pipeline
import sys, json
def main():
    for line in sys.stdin:
        try:
            req = json.loads(line)
        except Exception:
            continue
        method = req.get("method", "")
        rid = req.get("id", 0)
        if method == "tools/list":
            print(json.dumps({"jsonrpc":"2.0","id":rid,"result":{"tools":[{"name":"foo"}]}}), flush=True)
        elif method == "tools/call":
            print(json.dumps({"jsonrpc":"2.0","id":rid,"result":{"result":"called","capability_changed":False}}), flush=True)
        else:
            print(json.dumps({"jsonrpc":"2.0","id":rid,"error":{"message":"unknown method"}}), flush=True)
if __name__ == "__main__":
    main()
'
    ;;
  bad_import)
    emit_text 'import requests_html
import sys
'
    ;;
  bad_syntax)
    emit_text 'def broken(:
    pass
'
    ;;
  crash)
    echo "boom" >&2
    exit 1
    ;;
  *)
    echo "unknown FAKE_BUILD_CLAUDE_MODE: $mode" >&2
    exit 2
    ;;
esac
