"""FutureTask + TaskResult: result objects returned by Workflow.chat/submit.

FutureTask is the in-flight handle that wraps wait/resume. TaskResult is the
terminal snapshot returned by .wait() — including the awaiting_user case,
which is terminal for the polling loop but means "needs human input next".
"""
from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any

from .errors import TaskCancelled, TaskFailed


@dataclass
class Question:
    """Surfaced when a chat task pauses on ask_user / request_permission."""

    kind: str                         # "ask_user" | "request_permission"
    question: str = ""
    options: list[str] = field(default_factory=list)
    context: str = ""
    intent: str = ""
    target: str = ""
    reason: str = ""

    @classmethod
    def from_dict(cls, d: dict) -> "Question":
        return cls(
            kind=d.get("kind", ""),
            question=d.get("question", ""),
            options=list(d.get("options", []) or []),
            context=d.get("context", ""),
            intent=d.get("intent", ""),
            target=d.get("target", ""),
            reason=d.get("reason", ""),
        )


@dataclass
class TaskResult:
    """Snapshot of a task after .wait() returns."""

    task_id: str
    status: str                       # completed | failed | cancelled | awaiting_user
    output: str
    session_id: str
    outputs: dict[str, str]           # logical name -> local path (from write_paths PUT-back)
    failure_reason: str = ""
    question: Question | None = None  # set iff status == "awaiting_user"
    raw: dict = field(default_factory=dict)  # original driver response

    def __post_init__(self):
        # Coerce question dict to Question for test ergonomics.
        if isinstance(self.question, dict):
            self.question = Question.from_dict(self.question)

    @property
    def is_terminal(self) -> bool:
        return self.status in ("completed", "failed", "cancelled", "awaiting_user")

    @property
    def is_awaiting(self) -> bool:
        return self.status == "awaiting_user"

    def unwrap(self) -> str:
        """Return output if completed; raise TaskFailed / TaskCancelled otherwise."""
        if self.status == "completed":
            return self.output
        if self.status == "failed":
            raise TaskFailed(self.task_id, self.failure_reason or "unknown")
        if self.status == "cancelled":
            raise TaskCancelled(self.task_id)
        # awaiting_user: caller should have handled via expect_or_ask
        raise RuntimeError(
            f"task {self.task_id} is awaiting_user; call .expect_or_ask(handler) before .wait()"
        )

    @classmethod
    def from_driver_response(cls, task_id: str, resp: dict,
                              outputs: dict[str, str] | None = None) -> "TaskResult":
        question = None
        if resp.get("status") == "awaiting_user" and resp.get("question"):
            qd = resp["question"]
            if isinstance(qd, str):
                import json as _json
                try:
                    qd = _json.loads(qd)
                except _json.JSONDecodeError:
                    qd = {"kind": "ask_user", "question": qd}
            question = Question.from_dict(qd)
        return cls(
            task_id=task_id,
            status=resp.get("status", ""),
            output=resp.get("output", "") or resp.get("final_output", ""),
            session_id=resp.get("session_id", ""),
            outputs=outputs or {},
            failure_reason=resp.get("failure_reason", ""),
            question=question,
            raw=resp,
        )


@dataclass
class FutureTask:
    """In-flight handle, returned by Workflow.chat / Workflow.submit.

    Holds the task_id + reference to its workflow so .wait(), .expect_or_ask()
    can dispatch back through the driver client. Not directly instantiated by
    users; created by Workflow methods.
    """

    workflow: Any                     # forward-ref to Workflow to avoid cycle
    task_id: str
    target: str
    session_id: str = ""              # captured from submit response
    outputs_pending: dict[str, str] = field(default_factory=dict)
    _question_handler: Any = None

    def expect_or_ask(self, handler: Any = None) -> "FutureTask":
        """Attach a handler that auto-answers awaiting_user pauses during .wait().

        handler signature: (Question) -> str
        If None, uses humanloop.default_terminal_handler.
        """
        self._question_handler = handler
        return self

    def wait(self, timeout: float | None = None) -> TaskResult:
        """Block until the task reaches a terminal state. If a question handler
        was attached, loop awaiting_user → handler → resume_task until completed.
        Delegates to workflow._wait_with_humanloop."""
        return self.workflow._wait_with_humanloop(self, timeout=timeout)
