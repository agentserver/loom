"""Case 2: single ask_user → resume → final.

Model invokes ask_user, executor pauses chat, driver wait_task returns
status:"awaiting_user" with the question payload and session_id.
resume_task feeds an answer, slave runs `claude --resume <S>` with
"User answered: ...", model finishes with the answered value.
"""
import json, sys, time, os
sys.path.insert(0, os.path.dirname(__file__))
from lib import submit_chat, get_task, resume_with

print("=== CASE 2: single ask_user → resume → final ===", flush=True)
prompt = (
    "Call the ask_user tool with question=\"pick a color\" "
    "options=[\"red\",\"blue\"]. When you receive the user's answer "
    "in your next user turn, reply with exactly that color word and stop."
)
r = submit_chat(prompt, timeout_sec=120)
print("submit:", json.dumps(r)[:300], flush=True)
task = r["task_id"]

# Poll until awaiting_user (or terminal)
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

if status != "awaiting_user":
    print(f"FAIL case 2: expected awaiting_user, got {status}: {json.dumps(info)[:400]}")
    sys.exit(1)

current = info["current_task_id"]
print(f"awaiting_user OK; session_id={info.get('session_id','')!r} current_task_id={current}", flush=True)

# Resume with the answer
r2 = resume_with(current, "blue", timeout_sec=180)
status2 = r2.get("status", "unknown")
output2 = (r2.get("output", "") or r2.get("final_output", "")).lower()
if status2 == "completed" and "blue" in output2:
    print(f"PASS case 2 (output={output2[:80]!r})")
    sys.exit(0)
print(f"FAIL case 2: resume status={status2} output={output2!r} full={json.dumps(r2)[:400]}")
sys.exit(1)
