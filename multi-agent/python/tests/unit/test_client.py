"""Unit tests for loom.client (fake subprocess via io.BytesIO)."""
import io
import json

import pytest

from loom.client import _DriverClient, _next_id
from loom.errors import DriverUnavailable


class _FakeProc:
    """Stand-in for subprocess.Popen with stdin/stdout BytesIO and a planned reply."""

    def __init__(self, planned_responses: list[dict]):
        self.stdin = io.BytesIO()
        # Pre-fill stdout with newline-delimited JSON the client expects.
        self.stdout = io.BytesIO(
            b"".join(json.dumps(r).encode() + b"\n" for r in planned_responses)
        )
        self.returncode = None
        self.terminated = False
        self.killed = False

    def poll(self):
        return self.returncode

    def terminate(self):
        self.terminated = True
        self.returncode = 0

    def kill(self):
        self.killed = True
        self.returncode = -9

    def wait(self, timeout=None):
        return self.returncode or 0


def test_next_id_monotonic():
    a = _next_id()
    b = _next_id()
    assert b > a


def test_call_serializes_request_and_parses_response(monkeypatch):
    planned = [
        {"jsonrpc": "2.0", "id": 1, "result": {
            "protocolVersion": "2024-11-05", "capabilities": {},
        }},
        {"jsonrpc": "2.0", "id": 2, "result": {
            "content": [{"type": "text", "text": json.dumps({"agents": []})}]
        }},
    ]
    fake = _FakeProc(planned)
    monkeypatch.setattr("loom.client._spawn_driver", lambda *a, **kw: fake)

    client = _DriverClient(bin_path="/fake/driver-agent")
    out = client.call("list_agents", {})
    assert out == {"agents": []}
    # Request sent on stdin should contain tools/call for list_agents
    sent = fake.stdin.getvalue().decode()
    assert '"method":"initialize"' in sent
    assert '"name":"list_agents"' in sent


def test_call_propagates_jsonrpc_error(monkeypatch):
    planned = [
        {"jsonrpc": "2.0", "id": 1, "result": {
            "protocolVersion": "2024-11-05", "capabilities": {},
        }},
        {"jsonrpc": "2.0", "id": 2, "error": {
            "code": -32000, "message": "task failed something"
        }},
    ]
    fake = _FakeProc(planned)
    monkeypatch.setattr("loom.client._spawn_driver", lambda *a, **kw: fake)

    client = _DriverClient(bin_path="/fake/driver-agent")
    with pytest.raises(DriverUnavailable, match="task failed something"):
        client.call("submit_task", {"prompt": "x"})


def test_singleton_default_instance(monkeypatch):
    """get_client() returns the same instance across calls in one process."""
    monkeypatch.setattr(
        "loom.client.resolve_driver_bin", lambda: "/fake/driver-agent")
    monkeypatch.setattr(
        "loom.client.resolve_driver_cfg", lambda: None)
    monkeypatch.setattr(
        "loom.client._spawn_driver", lambda *a, **kw: _FakeProc([
            {"jsonrpc": "2.0", "id": 1, "result": {}}
        ]))
    # reset module-level singleton
    import loom.client
    loom.client._singleton = None
    a = loom.client.get_client()
    b = loom.client.get_client()
    assert a is b
