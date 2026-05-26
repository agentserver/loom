"""Case 5: cross-node — chat with ask_user on the arm64 jetson.

Verifies the humanloop pause/resume path works end-to-end across the WAN
to slave-jetson-prod (which lives at nvidia@10.128.185.180, arm64). Same
shape as case 2; only the target_display_name differs.

Pre-req: jetson must be re-registered into the same workspace as
driver-prod. Check with `python3 scripts/probe_fleet.py` — expect to see
`slave-jetson-prod` listed. If you only see `slave-local-prod`, the
jetson is still in the old workspace and needs the device-code dance.
"""
import json, sys, time, os
sys.path.insert(0, os.path.dirname(__file__))
from lib import call_tool, submit_chat, get_task, resume_with

print("=== CASE 5: cross-node chat with ask_user on jetson ===", flush=True)

# Probe fleet first
agents = call_tool("list_agents", {}).get("agents", [])
displays = [a.get("display_name") for a in agents]
print(f"visible agents: {displays}", flush=True)
if "slave-jetson-prod" not in displays:
    print("SKIP case 5: slave-jetson-prod not in current workspace. "
          "Re-register it (device-code dance) before re-running.")
    sys.exit(2)

prompt = (
    "Call ask_user with question=\"pick a color\" options=[\"red\",\"blue\"]. "
    "When you receive the user's answer in your next user turn, reply with exactly "
    "that color word and stop."
)
r = submit_chat(prompt, target="slave-jetson-prod", timeout_sec=480)
task = r["task_id"]
print(f"task_id={task}", flush=True)

deadline = time.time() + 480
info = {}
while time.time() < deadline:
    info = get_task(task)
    s = info.get("status", "")
    print(f"  poll status={s}", flush=True)
    if s in ("awaiting_user", "completed", "failed", "cancelled"):
        break
    time.sleep(5)
if info.get("status") != "awaiting_user":
    print(f"FAIL c5 setup: {json.dumps(info)[:300]}")
    sys.exit(1)

cur = info["current_task_id"]
r2 = resume_with(cur, "blue", timeout_sec=240)
status = r2.get("status", "")
output = (r2.get("output", "") or "").lower()
if status == "completed" and "blue" in output:
    print(f"PASS case 5 (cross-node round-trip on arm64 jetson; output={output[:80]!r})")
    sys.exit(0)
print(f"FAIL c5: status={status} output={output!r}")
sys.exit(1)
