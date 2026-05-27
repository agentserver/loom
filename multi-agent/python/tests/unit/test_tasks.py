"""Unit tests for FutureTask / TaskResult."""
import pytest

from loom.tasks import TaskResult, FutureTask
from loom.errors import TaskFailed, TaskCancelled


def test_taskresult_completed():
    r = TaskResult(task_id="T1", status="completed", output="HELLO",
                   session_id="S1", outputs={})
    assert r.is_terminal is True
    assert r.is_awaiting is False


def test_taskresult_awaiting_user():
    r = TaskResult(task_id="T1", status="awaiting_user", output="",
                   session_id="S1", outputs={},
                   question={"kind": "ask_user", "question": "pick", "options": ["a", "b"]})
    assert r.is_terminal is True   # terminal for the wait() loop's purposes
    assert r.is_awaiting is True
    assert r.question.kind == "ask_user"
    assert r.question.options == ["a", "b"]


def test_taskresult_failed_raises_on_unwrap():
    r = TaskResult(task_id="T1", status="failed", output="",
                   session_id="", outputs={},
                   failure_reason="claude exit: ...")
    with pytest.raises(TaskFailed) as exc:
        r.unwrap()
    assert exc.value.task_id == "T1"
    assert "claude exit" in str(exc.value)


def test_taskresult_cancelled_raises_on_unwrap():
    r = TaskResult(task_id="T1", status="cancelled", output="",
                   session_id="", outputs={})
    with pytest.raises(TaskCancelled):
        r.unwrap()


def test_taskresult_unwrap_returns_output_when_completed():
    r = TaskResult(task_id="T1", status="completed", output="HELLO",
                   session_id="S1", outputs={})
    assert r.unwrap() == "HELLO"
