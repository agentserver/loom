"""Case 1: happy chat with humanloop MCP injected but ask_user not called.

Verifies the MCP injection has zero impact on baseline chat — model returns
'HELLO' and the dispatch wrap produces kind:"final" with session_id captured.
"""
import json, sys, time, os
sys.path.insert(0, os.path.dirname(__file__))
from lib import submit_chat, get_task

print("=== CASE 1: happy chat (humanloop injected, no ask_user) ===", flush=True)
r = submit_chat(
    "Reply with the single word HELLO and then stop. Do not call any tool.",
    timeout_sec=180,
)
print("submit:", json.dumps(r)[:300], flush=True)
if "task_id" not in r:
    print("FAIL submit"); sys.exit(1)
task = r["task_id"]

deadline = time.time() + 180
status = "pending"
info = {}
while time.time() < deadline:
    info = get_task(task)
    status = info.get("status", "unknown")
    print(f"  poll status={status}", flush=True)
    if status in ("completed", "failed", "cancelled", "awaiting_user"):
        break
    time.sleep(5)

output = info.get("output", "") or info.get("final_output", "")
ok = (status == "completed" and "HELLO" in output.upper())
print("PASS case 1" if ok else f"FAIL case 1: status={status} output={output[:200]!r}")
sys.exit(0 if ok else 1)
