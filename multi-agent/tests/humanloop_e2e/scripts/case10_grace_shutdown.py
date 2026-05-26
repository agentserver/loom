"""Case 10: graceful-shutdown timeout — backend that ignores stdin close → SIGTERM/SIGKILL → task failed.

Swaps in a stub claude binary inside the slave container that:
- emits one stream-json system frame (so session_id is captured),
- ignores stdin EOF and exec-sleeps forever,
- can only be killed by SIGTERM/SIGKILL.

Submits a chat. With humanloop.shutdown_grace_sec lowered (config edit),
the slave's chat executor closes stdin on pause / after writing prompt, fake
stub doesn't exit within grace window, SIGTERM then SIGKILL fire.
Expect: task status=failed with reason mentioning "grace window".

Restores claude binary + grace config at the end.
"""
import json, sys, time, os, subprocess
sys.path.insert(0, os.path.dirname(__file__))
from lib import submit_chat, get_task

CONTAINER = "loom-slave-local-prod"
CFG_PATH = "/root/.loom/slave-local/config.yaml"
STUB_PATH = "/usr/local/bin/claude-stub"

def docker(*args, **kwargs):
    return subprocess.run(["docker"] + list(args), capture_output=True, text=True, timeout=60, **kwargs)

def docker_sh(script):
    return docker("exec", CONTAINER, "sh", "-c", script)

print("=== CASE 10: grace-shutdown timeout ===", flush=True)

# 1. Drop the stub binary into the container
stub_body = r"""#!/bin/bash
echo '{"type":"system","session_id":"sess-stuck-c10"}'
trap '' PIPE
# Use exec sleep so SIGTERM hits sleep directly, not bash.
exec sleep 300 < /dev/null > /dev/null 2>&1
"""
docker_sh(f"cat > {STUB_PATH} <<'STUB_EOF'\n{stub_body}STUB_EOF\nchmod +x {STUB_PATH}")
chk = docker_sh(f"ls -la {STUB_PATH}").stdout.strip()
print(f"stub installed: {chk}", flush=True)

# 2. Lower the grace_sec. Use sed in place per memory prod_test_config_scp_trap.
# Add humanloop block if not present, otherwise patch it.
docker_sh(f"""
if grep -q '^humanloop:' {CFG_PATH}; then
  sed -i 's/^  shutdown_grace_sec:.*/  shutdown_grace_sec: 1/' {CFG_PATH}
else
  printf '\\nhumanloop:\\n  shutdown_grace_sec: 1\\n' >> {CFG_PATH}
fi
""")

# 3. Point claude bin to the stub
docker_sh(f"""
if grep -q '^    bin:' {CFG_PATH}; then
  sed -i 's|^    bin:.*|    bin: {STUB_PATH}|' {CFG_PATH}
else
  echo 'WARN: no  bin: line found'
fi
""")
chk = docker_sh(f"grep -E 'shutdown_grace_sec|bin:' {CFG_PATH}").stdout.strip()
print(f"config patched:\n{chk}", flush=True)

# 4. Restart slave to pick up new config
docker("restart", CONTAINER)
time.sleep(10)

ok = False
try:
    # 5. Submit a chat
    r = submit_chat("Reply with HELLO", timeout_sec=60)
    task = r["task_id"]
    print(f"task_id={task}", flush=True)

    # 6. Poll for terminal (should fail within ~grace_sec + 5s + slack)
    deadline = time.time() + 60
    info = {}
    while time.time() < deadline:
        info = get_task(task)
        s = info.get("status", "")
        if s in ("completed", "failed", "cancelled", "awaiting_user"):
            break
        time.sleep(3)
    print(f"final: {json.dumps(info)[:400]}", flush=True)
    status = info.get("status", "")
    reason = info.get("failure_reason", "") or info.get("output", "") or info.get("final_output", "")
    if status == "failed" and ("grace" in reason.lower() or "sigterm" in reason.lower()
                               or "killed" in reason.lower() or "exit status" in reason.lower()):
        print("PASS case 10")
        ok = True
    else:
        print(f"FAIL c10: status={status} reason={reason[:300]!r}")
finally:
    # 7. Restore real claude bin + 10s grace
    docker_sh(f"sed -i 's|^    bin: {STUB_PATH}|    bin: claude|' {CFG_PATH}")
    docker_sh(f"sed -i 's/^  shutdown_grace_sec: 1$/  shutdown_grace_sec: 10/' {CFG_PATH}")
    docker("restart", CONTAINER)
    time.sleep(10)
    print("restored claude bin + grace=10", flush=True)

sys.exit(0 if ok else 1)
