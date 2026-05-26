"""Case 9: per-process question quota — Nth+1 ask_user call returns 'refused'.

Note on design: the slave's chat executor pauses on the FIRST IPC payload,
so in normal flow the model never reaches the quota in a single Run.
The quota is defense-in-depth for misbehaving models that emit multiple
ask_user tool_use blocks in one assistant message. The unit test
internal/humanloop/quota_test.go covers the in-process logic.

This live e2e drives the deployed slave-agent's humanloop-mcp subcommand
directly via docker exec, mimicking what claude would do as the MCP client:
send N+1 tools/call invocations with max=N, verify first N get 'submitted'
and the (N+1)th gets 'refused'. No claude involved; exercises the real
binary inside the prod_test container.
"""
import json, sys, os, subprocess

CONTAINER = "loom-slave-local-prod"
MAX = 2

print("=== CASE 9: per-process quota (deployed binary, docker exec) ===", flush=True)

# Build N+1 = 3 tools/call requests + the MCP handshake.
calls = "\n".join([
    json.dumps({"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}),
    json.dumps({"jsonrpc":"2.0","method":"notifications/initialized","params":{}}),
    json.dumps({"jsonrpc":"2.0","id":10,"method":"tools/call",
                "params":{"name":"ask_user","arguments":{"question":"round 1"}}}),
    json.dumps({"jsonrpc":"2.0","id":11,"method":"tools/call",
                "params":{"name":"ask_user","arguments":{"question":"round 2"}}}),
    json.dumps({"jsonrpc":"2.0","id":12,"method":"tools/call",
                "params":{"name":"ask_user","arguments":{"question":"round 3"}}}),
]) + "\n"

# Spawn a background socket listener inside the container that drains IPC
# payloads so handleCall's client.Send doesn't block on an unaccepted dial.
# Use a shell script so we can chain it with the humanloop-mcp invocation.
script = f"""
SOCK=/tmp/case9-quota.sock
rm -f $SOCK
python3 -c '
import socket, sys
s=socket.socket(socket.AF_UNIX); s.bind("'$SOCK'"); s.listen(8)
while True:
    try: c,_=s.accept(); c.recv(4096); c.close()
    except: break
' &
LPID=$!
sleep 1
echo '{calls.strip()}' | /usr/local/bin/slave-agent humanloop-mcp $SOCK {MAX}
kill $LPID 2>/dev/null
"""
# json-escape the inner script for docker exec
r = subprocess.run(
    ["docker", "exec", CONTAINER, "bash", "-c",
     f"SOCK=/tmp/case9.sock; rm -f $SOCK; "
     f"python3 -c \"import socket,sys; s=socket.socket(socket.AF_UNIX); s.bind('$SOCK'); s.listen(8)\n"
     f"while True:\n  try: c,_=s.accept(); c.recv(4096); c.close()\n  except: break\" &\n"
     f"LPID=$!; sleep 1; "
     f"printf '%s' '{calls.replace(chr(39), chr(39) + chr(92) + chr(39) + chr(39))}' "
     f"| /usr/local/bin/slave-agent humanloop-mcp $SOCK {MAX}; "
     f"kill $LPID 2>/dev/null"],
    capture_output=True, text=True, timeout=30)

print(f"rc={r.returncode}", flush=True)
print(f"stderr: {r.stderr[:300]}", flush=True)

# Parse stdout lines as JSON-RPC responses
responses = []
for line in r.stdout.splitlines():
    line = line.strip()
    if not line: continue
    try:
        obj = json.loads(line)
        responses.append(obj)
    except json.JSONDecodeError:
        continue

# Print responses to ids 10, 11, 12
results = {}
for obj in responses:
    if obj.get("id") in (10, 11, 12):
        text = obj.get("result", {}).get("content", [{}])[0].get("text", "")
        results[obj["id"]] = text
        print(f"  id={obj['id']} text={text[:100]!r}", flush=True)

# Expect:
#   id=10 → "submitted" (call 1)
#   id=11 → "submitted" (call 2, at max boundary)
#   id=12 → "refused"   (call 3, over max=2)
ok = (
    "submitted" in results.get(10, "")
    and "submitted" in results.get(11, "")
    and "refused"   in results.get(12, "")
)
if ok:
    print("PASS case 9 (deployed binary enforces per-process quota)")
    sys.exit(0)
print(f"FAIL c9: results={results}")
sys.exit(1)
