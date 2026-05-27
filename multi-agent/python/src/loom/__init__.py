"""loom — Python client for the loom multi-agent fabric on agentserver."""
from .errors import (
    LoomError,
    DriverUnavailable,
    TaskFailed,
    TaskCancelled,
    SessionLost,
    AcceptanceFailed,
    SlaveNotFound,
    AmbiguousTarget,
    NoInteractiveHandler,
)
from .tasks import FutureTask, TaskResult, Question
from .files import Blob
from .capability import MCPSpec
from .workflow import Workflow, workflow

__version__ = "0.1.0.dev0"
__all__ = [
    "__version__",
    "workflow",
    "Workflow",
    "FutureTask",
    "TaskResult",
    "Question",
    "Blob",
    "MCPSpec",
    "LoomError",
    "DriverUnavailable",
    "TaskFailed",
    "TaskCancelled",
    "SessionLost",
    "AcceptanceFailed",
    "SlaveNotFound",
    "AmbiguousTarget",
    "NoInteractiveHandler",
]
