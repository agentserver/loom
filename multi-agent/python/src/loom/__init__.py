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

__version__ = "0.1.0.dev0"
__all__ = [
    "__version__",
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
