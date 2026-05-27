"""Unit tests for Workflow / FutureTask end-to-end (with mocked driver client)."""
import pytest

from loom import workflow
from loom.client import _DriverClient, set_client
from loom.errors import SlaveNotFound, AmbiguousTarget


class _MockClient:
    """Replace _DriverClient with scripted call() responses."""

    def __init__(self, script: list[tuple[str, dict]]):
        # script: list of (tool_name_expected, response_dict) in call order
        self.script = list(script)
        self.calls: list[tuple[str, dict]] = []

    def call(self, tool: str, arguments: dict, *, timeout=None):
        self.calls.append((tool, arguments))
        if not self.script:
            raise AssertionError(f"unexpected call: {tool}({arguments})")
        expected_tool, response = self.script.pop(0)
        assert tool == expected_tool, f"expected {expected_tool}, got {tool}"
        return response

    def close(self):
        pass

    def restart(self):
        pass


def test_workflow_chat_happy_path():
    mock = _MockClient([
        ("submit_task", {
            "task_id": "T1", "session_id": "", "target_id": "a1",
            "target_display_name": "slave-A", "manifest": {},
        }),
        ("wait_task", {
            "status": "completed", "output": "HELLO", "is_final": True,
            "final_output": "HELLO",
        }),
    ])
    set_client(mock)
    try:
        with workflow(goal="hi") as wf:
            res = wf.chat("Reply HELLO", target="slave-A").wait()
        assert res.status == "completed"
        assert res.output == "HELLO"
        # Verify submit_task was called with the prompt unchanged
        submit_args = mock.calls[0][1]
        assert submit_args["prompt"] == "Reply HELLO"
        assert submit_args["skill"] == "chat"
        assert submit_args["target_display_name"] == "slave-A"
    finally:
        set_client(None)


def test_workflow_chat_awaiting_user_then_resume():
    mock = _MockClient([
        ("submit_task", {"task_id": "T1", "session_id": "", "target_id": "a1",
                         "target_display_name": "slave-A", "manifest": {}}),
        ("wait_task", {
            "status": "awaiting_user",
            "session_id": "S-1", "current_task_id": "T1", "target_id": "a1",
            "question": {"kind": "ask_user", "question": "pick", "options": ["red", "blue"]},
            "is_final": False,
        }),
        ("resume_task", {
            "status": "completed", "output": "red", "is_final": True,
            "final_output": "red",
        }),
    ])
    set_client(mock)
    try:
        def handler(q):
            assert q.options == ["red", "blue"]
            return "red"
        with workflow(goal="…") as wf:
            res = wf.chat("pick a color", target="slave-A").expect_or_ask(handler).wait()
        assert res.status == "completed"
        assert res.output == "red"
        # Verify resume was called with the answer
        resume_args = mock.calls[2][1]
        assert resume_args["answer"] == "red"
    finally:
        set_client(None)


def test_workflow_find_slave_by_skill_unique():
    mock = _MockClient([
        ("list_agents", {"agents": [
            {"agent_id": "a1", "display_name": "slave-A", "skills": ["chat"]},
            {"agent_id": "a2", "display_name": "slave-B", "skills": ["bash"]},
        ]}),
    ])
    set_client(mock)
    try:
        with workflow(goal="…") as wf:
            name = wf.find_slave(skill="chat")
        assert name == "slave-A"
    finally:
        set_client(None)


def test_workflow_find_slave_none_raises():
    mock = _MockClient([
        ("list_agents", {"agents": [
            {"agent_id": "a1", "display_name": "slave-A", "skills": ["chat"]},
        ]}),
    ])
    set_client(mock)
    try:
        with workflow(goal="…") as wf:
            with pytest.raises(SlaveNotFound):
                wf.find_slave(skill="bash")
    finally:
        set_client(None)


def test_workflow_find_slave_ambiguous_raises():
    mock = _MockClient([
        ("list_agents", {"agents": [
            {"agent_id": "a1", "display_name": "slave-A", "skills": ["chat"]},
            {"agent_id": "a2", "display_name": "slave-B", "skills": ["chat"]},
        ]}),
    ])
    set_client(mock)
    try:
        with workflow(goal="…") as wf:
            with pytest.raises(AmbiguousTarget) as exc:
                wf.find_slave(skill="chat")
        assert set(exc.value.candidates) == {"slave-A", "slave-B"}
    finally:
        set_client(None)
