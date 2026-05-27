"""Exception hierarchy for the loom client."""
from __future__ import annotations


class LoomError(Exception):
    """Base for all loom-py errors."""


class DriverUnavailable(LoomError):
    """driver-agent subprocess could not be started, or JSON-RPC failed."""


class TaskFailed(LoomError):
    """A task reached terminal state 'failed'."""

    def __init__(self, task_id: str, failure_reason: str, *, target: str | None = None):
        super().__init__(f"task {task_id} failed: {failure_reason}")
        self.task_id = task_id
        self.failure_reason = failure_reason
        self.target = target


class TaskCancelled(LoomError):
    """A task reached terminal state 'cancelled'."""

    def __init__(self, task_id: str):
        super().__init__(f"task {task_id} cancelled")
        self.task_id = task_id


class SessionLost(LoomError):
    """resume_task failed because the backend session no longer exists."""


class AcceptanceFailed(LoomError):
    """scaffold_and_register: the acceptance gate rejected the new MCP."""


class SlaveNotFound(LoomError):
    """find_slave: no agent in the workspace matches the criteria."""


class AmbiguousTarget(LoomError):
    """find_slave: multiple agents match; caller must disambiguate."""

    def __init__(self, candidates: list[str]):
        super().__init__(f"ambiguous target; candidates: {candidates}")
        self.candidates = candidates


class NoInteractiveHandler(LoomError):
    """expect_or_ask was called with the default terminal handler but stdin is not a TTY."""
