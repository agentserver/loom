"""Case 4: request_permission marker is distinct from ask_user.

Driver surfaces intent + target + reason from the marker.
"""
import json, sys, time, os
sys.path.insert(0, os.path.dirname(__file__))
from lib import submit_chat, get_task, resume_with

print("=== CASE 4: request_permission marker ===", flush=True)
prompt = (
    "Call the request_permission tool with intent=\"run_bash\" "
    "target=\"rm -rf /tmp/some_dir\" reason=\"cleanup\". "
    "When you receive the user's answer in your next user turn, reply with "
    "exactly that answer verbatim and stop."
)
r = submit_chat(prompt, timeout_sec=120)
task = r["task_id"]
print(f"task_id={task}", flush=True)

deadline = time.time() + 180
info = {}
while time.time() < deadline:
    info = get_task(task)
    s = info.get("status", "unknown")
    print(f"  poll status={s}", flush=True)
    if s in ("completed", "failed", "cancelled", "awaiting_user"):
        break
    time.sleep(5)

if info.get("status") != "awaiting_user":
    print(f"FAIL c4 wait: {json.dumps(info)[:400]}"); sys.exit(1)

q = info.get("question", {})
if q.get("kind") != "request_permission":
    print(f"FAIL c4 kind: {q.get('kind')!r}"); sys.exit(1)
if q.get("intent") != "run_bash":
    print(f"FAIL c4 intent: {q.get('intent')!r}"); sys.exit(1)
if q.get("target") != "rm -rf /tmp/some_dir":
    print(f"FAIL c4 target: {q.get('target')!r}"); sys.exit(1)

cur = info["current_task_id"]
r2 = resume_with(cur, "denied", timeout_sec=180)
if r2.get("status") != "completed":
    print(f"FAIL c4 resume status: {json.dumps(r2)[:400]}"); sys.exit(1)
print("PASS case 4"); sys.exit(0)
