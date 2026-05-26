"""Tiny JSON-RPC shim around `driver-agent serve-mcp` for e2e scripts.

Spawns a fresh driver-agent process per call_tool() and reads back the
inner tool result. Stateless; no daemon to manage.
"""
import json
import os
import subprocess

# Defaults point at tests/prod_test/. Override via env if you've moved things.
PROD_TEST = os.environ.get(
    "LOOM_PROD_TEST_DIR",
    "/root/multi-agent/multi-agent/tests/prod_test",
)
DRIVER_BIN = os.environ.get(
    "LOOM_DRIVER_BIN", os.path.join(PROD_TEST, "bin", "driver-agent.linux-amd64"))
DRIVER_CFG = os.environ.get(
    "LOOM_DRIVER_CFG", os.path.join(PROD_TEST, "driver", "config.yaml"))
DRIVER_DIR = os.environ.get(
    "LOOM_DRIVER_DIR", os.path.join(PROD_TEST, "driver"))


def call_tool(tool: str, args: dict, timeout: int = 300) -> dict:
    """Drive one tools/call on a fresh driver-agent process. Returns the
    parsed inner JSON dict from the tool's response.text; returns
    {"_error": ...} when MCP returned an error, {"_raw_text": ...} when the
    tool returned non-JSON text, {"_no_response": stdout_tail} on no reply.
    """
    req_init = {
        "jsonrpc": "2.0", "id": 1, "method": "initialize",
        "params": {
            "protocolVersion": "2024-11-05", "capabilities": {},
            "clientInfo": {"name": "e2e", "version": "0"},
        },
    }
    notif = {"jsonrpc": "2.0", "method": "notifications/initialized", "params": {}}
    req_call = {
        "jsonrpc": "2.0", "id": 2, "method": "tools/call",
        "params": {"name": tool, "arguments": args},
    }
    stdin = "\n".join(json.dumps(o) for o in (req_init, notif, req_call)) + "\n"
    p = subprocess.run(
        [DRIVER_BIN, "serve-mcp", "--config", DRIVER_CFG],
        input=stdin, capture_output=True, text=True,
        cwd=DRIVER_DIR, timeout=timeout)
    for line in p.stdout.splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            obj = json.loads(line)
        except json.JSONDecodeError:
            continue
        if obj.get("id") == 2:
            if "error" in obj:
                return {"_error": obj["error"]}
            text = obj.get("result", {}).get("content", [{}])[0].get("text", "")
            try:
                return json.loads(text)
            except json.JSONDecodeError:
                return {"_raw_text": text}
    return {"_no_response": p.stdout[-500:], "_stderr": p.stderr[-500:]}


def submit_chat(prompt: str, target: str = "slave-local-prod", timeout_sec: int = 60) -> dict:
    return call_tool("submit_task", {
        "prompt": prompt, "target_display_name": target,
        "skill": "chat", "timeout_sec": timeout_sec,
    })


def wait_for(task_id: str, timeout_sec: int = 120) -> dict:
    return call_tool("wait_task", {"task_id": task_id, "timeout_sec": timeout_sec})


def get_task(task_id: str) -> dict:
    return call_tool("get_task", {"task_id": task_id})


def resume_with(last_task_id: str, answer: str, timeout_sec: int = 120) -> dict:
    return call_tool("resume_task", {
        "last_task_id": last_task_id, "answer": answer, "timeout_sec": timeout_sec,
    })
