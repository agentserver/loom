"""Workflow context manager + fluent verb API.

A Workflow groups one or more tasks under a shared `goal` (Python-side metadata
only; not pushed to wire format in v0 — see spec § 6). Provides:
  - wf.chat(prompt, target, ...) → FutureTask
  - wf.submit(prompt, target, skill=, ...) → FutureTask (general form)
  - wf.list_slaves(skill=, mcp_tool=, name=) → list of display_names
  - wf.find_slave(skill=, mcp_tool=, name=) → unique display_name or raise
  - wf._wait_with_humanloop(future, timeout=) → TaskResult  (used by FutureTask.wait)
"""
from __future__ import annotations

from contextlib import contextmanager
from typing import Any, Callable, Iterator

from .capability import _filter_slaves
from .client import get_client
from .errors import AmbiguousTarget, SlaveNotFound
from .files import substitute_io_placeholders
from .humanloop import default_terminal_handler
from .tasks import FutureTask, Question, TaskResult


class Workflow:
    """Container for a sequence of tasks sharing a goal.

    v0: Python-side metadata only; not encoded into wire format.
    """

    def __init__(
        self,
        goal: str,
        success_criteria: list[str] | None = None,
        *,
        cancel_on_exit: bool = False,
    ):
        self.goal = goal
        self.success_criteria = success_criteria or []
        self.cancel_on_exit = cancel_on_exit
        self._tasks: list[FutureTask] = []
        self._client = get_client()

    def submit(
        self,
        prompt: str,
        target: str,
        *,
        skill: str = "chat",
        inputs: dict[str, str] | None = None,
        outputs: dict[str, str] | None = None,
        timeout_sec: int | None = None,
    ) -> FutureTask:
        """General task submission. Use wf.chat() for the common chat case."""
        in_map = inputs or {}
        out_map = outputs or {}
        # For v0 inputs/outputs are passed through to driver's read_paths/write_paths
        # AND substituted into the prompt. The actual slave-side path comes from
        # driver after submit; v0 substitutes the local path (driver will rewrite
        # internally). This is simpler than two-phase substitution and matches the
        # existing submit_task semantics.
        args: dict[str, Any] = {
            "prompt": substitute_io_placeholders(prompt, inputs=in_map, outputs=out_map),
            "target_display_name": target,
            "skill": skill,
        }
        if in_map:
            args["read_paths"] = list(in_map.values())
        if out_map:
            args["write_paths"] = [{"path": p, "overwrite": True} for p in out_map.values()]
        if timeout_sec is not None:
            args["timeout_sec"] = timeout_sec

        resp = self._client.call("submit_task", args)
        future = FutureTask(
            workflow=self,
            task_id=resp.get("task_id", ""),
            target=target,
            session_id=resp.get("session_id", ""),
            outputs_pending=dict(out_map),
        )
        self._tasks.append(future)
        return future

    def chat(self, prompt: str, target: str, **kw) -> FutureTask:
        return self.submit(prompt, target=target, skill="chat", **kw)

    def list_slaves(
        self,
        *,
        name: str | None = None,
        skill: str | None = None,
        mcp_tool: str | None = None,
    ) -> list[str]:
        """Return display_names of slaves matching all criteria."""
        agents = self._client.call("list_agents", {}).get("agents", [])
        matches = _filter_slaves(agents, name=name, skill=skill, mcp_tool=mcp_tool)
        return [a["display_name"] for a in matches]

    def find_slave(
        self,
        *,
        name: str | None = None,
        skill: str | None = None,
        mcp_tool: str | None = None,
    ) -> str:
        """Return the unique display_name of a matching slave; raise otherwise."""
        names = self.list_slaves(name=name, skill=skill, mcp_tool=mcp_tool)
        if not names:
            criteria = {"name": name, "skill": skill, "mcp_tool": mcp_tool}
            raise SlaveNotFound(f"no slave matches: {criteria}")
        if len(names) > 1:
            raise AmbiguousTarget(names)
        return names[0]

    def _wait_with_humanloop(
        self,
        future: FutureTask,
        *,
        timeout: float | None = None,
    ) -> TaskResult:
        """Block until terminal. If a question handler is attached and an
        awaiting_user is returned, call the handler and resume_task; loop."""
        current_task_id = future.task_id
        while True:
            wait_args = {"task_id": current_task_id}
            if timeout is not None:
                wait_args["timeout_sec"] = int(timeout)
            resp = self._client.call("wait_task", wait_args)
            outputs_resolved = dict(future.outputs_pending)
            result = TaskResult.from_driver_response(
                current_task_id, resp, outputs=outputs_resolved,
            )
            if not result.is_awaiting:
                return result
            # awaiting_user: handler-or-default → resume → loop
            handler = future._question_handler or default_terminal_handler
            answer = handler(result.question)
            # The current task is done; next round polls a new chat_resume task.
            current_task_id = result.task_id  # driver's wait_task returns the same id
            resume_resp = self._client.call("resume_task", {
                "last_task_id": current_task_id,
                "answer": answer,
            })
            # resume_task returns a wait_task-shaped response directly (it blocks
            # internally until the new chat_resume task terminates).
            outputs_resolved = dict(future.outputs_pending)
            result = TaskResult.from_driver_response(
                resume_resp.get("task_id", current_task_id),
                resume_resp,
                outputs=outputs_resolved,
            )
            if not result.is_awaiting:
                return result
            # Multi-round: another awaiting_user; loop with new task_id
            current_task_id = result.task_id


@contextmanager
def workflow(
    goal: str,
    success_criteria: list[str] | None = None,
    *,
    cancel_on_exit: bool = False,
) -> Iterator[Workflow]:
    """Entry point: ``with loom.workflow(goal=...) as wf: ...``."""
    wf = Workflow(goal, success_criteria, cancel_on_exit=cancel_on_exit)
    try:
        yield wf
    finally:
        if cancel_on_exit:
            for ft in wf._tasks:
                try:
                    wf._client.call("cancel_task", {"task_id": ft.task_id})
                except Exception:
                    pass
