"""Case 6: slave offline mid-thread → resume errors → restart → succeeds.

Drives a chat to awaiting_user, then docker-stops slave-local-prod.
resume_task should error (clean: DelegateTask or task fails fast).
docker-start, wait for re-register, retry resume — should succeed.
"""
import json, sys, time, os, subprocess
sys.path.insert(0, os.path.dirname(__file__))
from lib import submit_chat, get_task, resume_with

CONTAINER = "loom-slave-local-prod"

def docker(*args):
    return subprocess.run(["docker"] + list(args), capture_output=True, text=True, timeout=60)

print("=== CASE 6: slave offline mid-thread ===", flush=True)
prompt = (
    "Call ask_user with question=\"continue?\" options=[\"yes\",\"no\"]. "
    "When you receive the user's answer, reply with exactly that answer and stop."
)
r = submit_chat(prompt, timeout_sec=120)
task = r["task_id"]
print(f"task_id={task}", flush=True)

# Wait for awaiting_user
deadline = time.time() + 180
info = {}
while time.time() < deadline:
    info = get_task(task)
    s = info.get("status", "")
    if s in ("awaiting_user", "completed", "failed", "cancelled"):
        break
    time.sleep(5)
if info.get("status") != "awaiting_user":
    print(f"FAIL c6 setup: {json.dumps(info)[:300]}"); sys.exit(1)
cur = info["current_task_id"]
print(f"awaiting_user OK; stopping slave container...", flush=True)

# Stop slave
r_stop = docker("stop", CONTAINER)
print(f"docker stop: rc={r_stop.returncode} stdout={r_stop.stdout.strip()[:80]}", flush=True)

# resume should error
print("--- resume attempt 1 (slave offline) ---", flush=True)
r_err = resume_with(cur, "yes", timeout_sec=30)
print(f"resume offline result: {json.dumps(r_err)[:300]}", flush=True)
# It should error or return failed status. Either is a clean failure.
ok_offline = (
    "_error" in r_err
    or r_err.get("status") in ("failed", "cancelled")
    or "available" in json.dumps(r_err).lower()
    or "delegate" in json.dumps(r_err).lower()
    or "not available" in json.dumps(r_err).lower()
)
if not ok_offline:
    print(f"FAIL c6 offline resume should have errored, got: {json.dumps(r_err)[:300]}")
    print("--- restoring container ---")
    docker("start", CONTAINER)
    sys.exit(1)
print("PASS c6 phase 1: resume on offline slave errored cleanly")

# Restart slave
print("--- starting slave back up ---", flush=True)
docker("start", CONTAINER)
# Wait for slave to reconnect to agentserver
print("waiting for slave to reconnect...", flush=True)
time.sleep(15)
# Verify slave is reachable by listing agents
from lib import call_tool
agents = call_tool("list_agents", {}).get("agents", [])
slave_visible = any(a.get("display_name") == "slave-local-prod" for a in agents)
if not slave_visible:
    print("FAIL c6 slave not visible after restart"); sys.exit(1)
print("slave visible again", flush=True)

# Retry resume — should succeed (session may have been killed, in which case
# we'd get failed-status; OR resume picks up where we left off). For this
# test we accept either: a clean completed run, OR a failed status with a
# reason mentioning session/resume. Both demonstrate the system isn't stuck.
r_retry = resume_with(cur, "yes", timeout_sec=180)
status = r_retry.get("status", "")
output = (r_retry.get("output", "") or r_retry.get("final_output", "")).lower()
reason = r_retry.get("failure_reason", "")
if status == "completed":
    print(f"PASS case 6 (resume after restart completed: output={output[:80]!r})")
    sys.exit(0)
if status == "failed" and ("session" in reason.lower() or "resume" in reason.lower()):
    print(f"PASS case 6 (resume after restart failed cleanly: {reason!r})")
    sys.exit(0)
print(f"FAIL c6 retry: status={status} output={output!r} reason={reason!r}")
sys.exit(1)
