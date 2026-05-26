"""Case 3: two rounds of ask_user before final.

Verifies multi-round pause/resume — resume_task can itself return another
awaiting_user, and the caller loops.
"""
import json, sys, time, os
sys.path.insert(0, os.path.dirname(__file__))
from lib import submit_chat, get_task, resume_with

print("=== CASE 3: multi-round ask_user → resume → ask_user → resume → final ===", flush=True)
prompt = (
    "You need to collect two pieces of information from the user. "
    "First call ask_user with question=\"What is your name?\". "
    "Wait for their answer. "
    "Then call ask_user with question=\"What is your favorite color?\". "
    "Wait for that answer. "
    "Then reply with exactly: NAME=<name> COLOR=<color>"
)
r = submit_chat(prompt, timeout_sec=120)
task = r["task_id"]
print(f"task_id={task}", flush=True)

def poll_until_terminal(tid, label):
    deadline = time.time() + 180
    while time.time() < deadline:
        info = get_task(tid)
        s = info.get("status", "unknown")
        print(f"  [{label}] poll status={s}", flush=True)
        if s in ("completed", "failed", "cancelled", "awaiting_user"):
            return info
        time.sleep(5)
    return {"status": "timeout"}

w1 = poll_until_terminal(task, "round1")
if w1.get("status") != "awaiting_user":
    print(f"FAIL c3 round1: {json.dumps(w1)[:400]}"); sys.exit(1)
cur = w1["current_task_id"]

r1 = resume_with(cur, "Alice", timeout_sec=180)
if r1.get("status") != "awaiting_user":
    print(f"FAIL c3 round2: expected awaiting_user, got {r1.get('status')}: {json.dumps(r1)[:400]}")
    sys.exit(1)
cur = r1.get("current_task_id") or r1.get("task_id")
print(f"round2 awaiting OK, current_task_id={cur}", flush=True)

# Answer second question, expect completed with "A+Y"
r2 = resume_with(cur, "purple", timeout_sec=180)
status = r2.get("status", "")
output = (r2.get("output", "") or r2.get("final_output", "")).upper()
if status == "completed" and "ALICE" in output and "PURPLE" in output:
    print(f"PASS case 3 (output={output[:120]!r})")
    sys.exit(0)
print(f"FAIL c3 final: status={status} output={output!r} full={json.dumps(r2)[:400]}")
sys.exit(1)
