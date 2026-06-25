#!/usr/bin/env python3
"""Driver MCP E2E test — direct stdio JSON-RPC against serve-mcp.

Exercises: initialize → tools/list → bind_thread → submit_task → wait_task → exit.
Asserts the #29 invariant: wait_task session_id is a backend-native UUID, NOT cse_*.
"""

import argparse
import json
import os
import re
import signal
import subprocess
import sys
import threading
import time
import uuid

PROD_TEST_DIR = "/root/multi-agent/multi-agent/tests/prod_test"
DRIVER_CONFIG = os.path.join(PROD_TEST_DIR, "driver-codex-local/config.yaml")
DRIVER_BINARY = os.path.join(PROD_TEST_DIR, "bin/driver-agent.linux-amd64")

UUID_RE = re.compile(
    r"^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$", re.I
)


class AssertionError(Exception):
    pass


class InfraError(Exception):
    pass


class MCPClient:
    def __init__(self, binary: str, config: str):
        self._id = 0
        self._proc = subprocess.Popen(
            [binary, "serve-mcp", "--config", config],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            cwd=os.path.dirname(config),
        )
        self._stderr_lines: list[str] = []
        self._stderr_thread = threading.Thread(
            target=self._drain_stderr, daemon=True
        )
        self._stderr_thread.start()

    def _drain_stderr(self):
        assert self._proc.stderr is not None
        for raw in self._proc.stderr:
            line = raw.decode("utf-8", errors="replace").rstrip()
            self._stderr_lines.append(line)

    def send(self, method: str, params: dict | None = None, *, is_notification: bool = False) -> dict | None:
        msg: dict = {"jsonrpc": "2.0", "method": method}
        if params is not None:
            msg["params"] = params
        if not is_notification:
            self._id += 1
            msg["id"] = self._id
        line = json.dumps(msg, separators=(",", ":")) + "\n"
        assert self._proc.stdin is not None
        self._proc.stdin.write(line.encode())
        self._proc.stdin.flush()
        if is_notification:
            return None
        return self._read_response(self._id)

    def _read_response(self, expected_id: int, timeout: float = 600) -> dict:
        assert self._proc.stdout is not None
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            self._proc.stdout.flush() if hasattr(self._proc.stdout, "flush") else None
            raw = self._proc.stdout.readline()
            if not raw:
                stderr = "\n".join(self._stderr_lines[-20:])
                raise InfraError(f"MCP process exited unexpectedly.\nStderr:\n{stderr}")
            try:
                resp = json.loads(raw)
            except json.JSONDecodeError:
                continue
            if resp.get("id") == expected_id:
                return resp
        raise InfraError(f"Timeout waiting for response id={expected_id}")

    def close(self):
        self.send("exit", is_notification=True)
        try:
            self._proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self._proc.kill()
            self._proc.wait()

    @property
    def stderr_tail(self) -> str:
        return "\n".join(self._stderr_lines[-30:])


def assert_eq(label: str, actual, expected):
    if actual != expected:
        raise AssertionError(f"{label}: expected {expected!r}, got {actual!r}")


def assert_true(label: str, value):
    if not value:
        raise AssertionError(f"{label}: expected truthy, got {value!r}")


def assert_match(label: str, value: str, pattern: re.Pattern):
    if not pattern.match(value):
        raise AssertionError(f"{label}: {value!r} does not match {pattern.pattern}")


def assert_not_startswith(label: str, value: str, prefix: str):
    if value.startswith(prefix):
        raise AssertionError(f"{label}: {value!r} starts with {prefix!r} (bridge id leaked)")


def run(binary: str, config: str, timeout: int):
    log("Spawning MCP server...")
    client = MCPClient(binary, config)

    try:
        # 1. initialize
        log("Step 1: initialize")
        resp = client.send("initialize", {"protocolVersion": "2024-11-05", "clientInfo": {"name": "e2e-test"}})
        assert_true("initialize has result", resp and "result" in resp)
        server_info = resp["result"].get("serverInfo", {})
        log(f"  serverInfo: {server_info}")

        # 2. tools/list
        log("Step 2: tools/list")
        resp = client.send("tools/list")
        assert_true("tools/list has result", resp and "result" in resp)
        tool_names = [t["name"] for t in resp["result"].get("tools", [])]
        for required in ("bind_thread", "submit_task", "wait_task", "list_agents"):
            assert_true(f"{required} in tools", required in tool_names)
        log(f"  {len(tool_names)} tools available")

        # 3. bind_thread
        thread_id = str(uuid.uuid4())
        log(f"Step 3: bind_thread (thread_id={thread_id})")
        resp = client.send("tools/call", {
            "name": "bind_thread",
            "arguments": {"thread_id": thread_id},
        })
        assert_true("bind_thread has result", resp and "result" in resp)
        content = resp["result"]["content"]
        bind_result = json.loads(content[0]["text"])
        assert_eq("bound", bind_result.get("bound"), True)
        assert_eq("thread_id echo", bind_result.get("thread_id"), thread_id)
        log("  bound OK")

        # 4. submit_task
        log("Step 4: submit_task (skill=chat, target=slave-codex-local)")
        resp = client.send("tools/call", {
            "name": "submit_task",
            "arguments": {
                "prompt": "Reply with exactly: E2E_OK",
                "target_display_name": "slave-codex-local",
                "skill": "chat",
                "timeout_sec": 120,
            },
        })
        if resp and "error" in resp:
            raise InfraError(f"submit_task error: {resp['error']}")
        assert_true("submit_task has result", resp and "result" in resp)
        content = resp["result"]["content"]
        submit_result = json.loads(content[0]["text"])
        task_id = submit_result["task_id"]
        assert_true("task_id non-empty", task_id)
        log(f"  task_id={task_id}")
        log(f"  bridge_session_id={submit_result.get('bridge_session_id', '(none)')}")

        # 5. wait_task
        log(f"Step 5: wait_task (timeout={timeout}s) — this may take a while...")
        resp = client.send("tools/call", {
            "name": "wait_task",
            "arguments": {
                "task_id": task_id,
                "poll_interval_sec": 5,
                "timeout_sec": timeout,
            },
        })
        if resp and "error" in resp:
            raise InfraError(f"wait_task error: {resp['error']}")
        assert_true("wait_task has result", resp and "result" in resp)
        content = resp["result"]["content"]
        wait_result = json.loads(content[0]["text"])

        log(f"  status={wait_result.get('status')}")
        log(f"  session_id={wait_result.get('session_id', '(none)')}")
        log(f"  bridge_session_id={wait_result.get('bridge_session_id', '(none)')}")
        log(f"  is_final={wait_result.get('is_final')}")
        output_preview = (wait_result.get("output") or "")[:200]
        log(f"  output (first 200 chars): {output_preview}")

        # 6. Assertions
        log("Step 6: Assertions")
        assert_eq("status", wait_result["status"], "completed")
        assert_true("is_final", wait_result.get("is_final"))

        session_id = wait_result.get("session_id", "")
        if session_id:
            assert_match("session_id is UUID", session_id, UUID_RE)
            assert_not_startswith("session_id not bridge", session_id, "cse_")

        output = wait_result.get("output") or wait_result.get("final_output") or ""
        assert_true("output non-empty", len(output) > 0)

        log("")
        log("=" * 50)
        log("  DRIVER MCP E2E: ALL ASSERTIONS PASSED")
        log("=" * 50)

    finally:
        log("Shutting down MCP server...")
        client.close()


def log(msg: str):
    print(f"[mcp-e2e] {msg}", flush=True)


def main():
    parser = argparse.ArgumentParser(description="Driver MCP E2E test")
    parser.add_argument("--config", default=DRIVER_CONFIG, help="Path to driver config.yaml")
    parser.add_argument("--binary", default=DRIVER_BINARY, help="Path to driver-agent binary")
    parser.add_argument("--timeout", type=int, default=300, help="wait_task timeout in seconds")
    args = parser.parse_args()

    if not os.path.isfile(args.binary):
        print(f"FATAL: binary not found: {args.binary}", file=sys.stderr)
        sys.exit(2)
    if not os.path.isfile(args.config):
        print(f"FATAL: config not found: {args.config}", file=sys.stderr)
        sys.exit(2)

    try:
        run(args.binary, args.config, args.timeout)
    except AssertionError as e:
        log(f"ASSERTION FAILED: {e}")
        sys.exit(1)
    except InfraError as e:
        log(f"INFRA ERROR: {e}")
        sys.exit(2)
    except KeyboardInterrupt:
        log("Interrupted")
        sys.exit(130)


if __name__ == "__main__":
    main()
